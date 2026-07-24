// kubernetes.go watches EndpointSlice objects for a named Service and
// reconciles the backend pool whenever pods come and go.
//
// INFORMERS, NOT RAW WATCHES
//
// The Kubernetes API supports a raw Watch verb, but using it directly means
// handling connection drops, bookmark events, re-list on 410 Gone, and
// resource-version tracking. client-go's SharedInformerFactory wraps all of
// that into a cache that fires Add/Update/Delete callbacks. Every production
// Kubernetes controller uses informers, not raw watches.
//
// RECONCILIATION, NOT EVENT PROCESSING
//
// The callbacks do not try to apply a diff. They call reconcile(), which reads
// the ENTIRE current state from the informer cache and rebuilds the backend
// list from scratch. This is deliberately simple and correct: a diff-based
// approach is faster but has to handle every combination of add/update/delete
// across multiple EndpointSlice objects, and getting it wrong means a stale
// pool. Reconciliation is O(n) where n is the number of endpoints, and n is
// small — a few hundred at most.
//
// ENDPOINTSLICE, NOT ENDPOINTS
//
// The older Endpoints resource packs every address into one object. At scale,
// every pod change rewrites the entire thing — O(n) updates for O(1) events.
// EndpointSlice shards addresses across multiple smaller objects.
// Endpoints is deprecated since Kubernetes 1.33.
package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/breaker"
	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
)

// KubernetesConfig holds what the watcher needs.
type KubernetesConfig struct {
	// ServiceName is the Kubernetes Service whose endpoints we track.
	ServiceName string

	// Namespace is the Kubernetes namespace. Defaults to "default".
	Namespace string

	// PortName selects which port from the EndpointSlice to use. If empty,
	// the first port is used.
	PortName string

	// BreakerCfg is attached to every backend if non-nil.
	BreakerCfg *breaker.Config

	// DialTimeout and ResponseTimeout are passed to backend.New.
	DialTimeout     time.Duration
	ResponseTimeout time.Duration
}

// KubernetesProvider watches EndpointSlices and updates the pool.
type KubernetesProvider struct {
	client kubernetes.Interface
	pool   *pool.Pool
	cfg    KubernetesConfig
	log    *slog.Logger
}

// NewKubernetesProvider builds a provider. The client can be a real clientset
// (from kubeconfig or in-cluster config) or a fake one for tests.
func NewKubernetesProvider(client kubernetes.Interface, p *pool.Pool, cfg KubernetesConfig, log *slog.Logger) *KubernetesProvider {
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	return &KubernetesProvider{
		client: client,
		pool:   p,
		cfg:    cfg,
		log:    log,
	}
}

// Run starts the informer and blocks until ctx is cancelled.
func (k *KubernetesProvider) Run(ctx context.Context) error {
	// A SharedInformerFactory builds informers that share the underlying
	// connection and cache. The namespace filter limits what we watch so the
	// balancer doesn't need cluster-wide permissions.
	factory := informers.NewSharedInformerFactoryWithOptions(
		k.client,
		30*time.Second, // resync interval — full re-list as a consistency check
		informers.WithNamespace(k.cfg.Namespace),
	)

	informer := factory.Discovery().V1().EndpointSlices().Informer()

	// The event handler fires on every change. All three callbacks do the same
	// thing: reconcile from scratch. This is correct, simple, and cheap.
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { k.reconcile(informer) },
		UpdateFunc: func(old, new interface{}) { k.reconcile(informer) },
		DeleteFunc: func(obj interface{}) { k.reconcile(informer) },
	})

	k.log.Info("kubernetes discovery started",
		"service", k.cfg.ServiceName,
		"namespace", k.cfg.Namespace,
	)

	// Start the informer. This is non-blocking — it starts goroutines.
	factory.Start(ctx.Done())

	// Wait for the initial list to complete. Without this, the first few
	// requests could see an empty pool because the cache hasn't populated yet.
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return fmt.Errorf("timed out waiting for EndpointSlice cache sync")
	}

	k.log.Info("kubernetes endpoint cache synced")
	k.reconcile(informer)

	// Block until shutdown.
	<-ctx.Done()
	k.log.Info("kubernetes discovery stopped")
	return nil
}

// reconcile reads the full state from the informer cache and rebuilds the pool.
func (k *KubernetesProvider) reconcile(informer cache.SharedIndexInformer) {
	items := informer.GetStore().List()

	var backends []*backend.Backend
	seen := map[string]bool{}

	for _, item := range items {
		eps, ok := item.(*discoveryv1.EndpointSlice)
		if !ok {
			continue
		}

		// EndpointSlices are labelled with the Service they belong to.
		// Filter for our Service.
		svcName := eps.Labels[discoveryv1.LabelServiceName]
		if svcName != k.cfg.ServiceName {
			continue
		}

		// Find the port number. If PortName is set, match by name; otherwise
		// take the first one.
		port := k.findPort(eps)
		if port == 0 {
			continue
		}

		for _, endpoint := range eps.Endpoints {
			// Only route to endpoints that Kubernetes says are ready.
			// A pod in the middle of shutting down has Ready=false and must
			// not receive new traffic.
			if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
				continue
			}

			for _, addr := range endpoint.Addresses {
				name := fmt.Sprintf("%s-%s:%d", k.cfg.ServiceName, addr, port)

				if seen[name] {
					continue // deduplicate across overlapping slices
				}
				seen[name] = true

				url := fmt.Sprintf("http://%s:%d", addr, port)
				b, err := backend.New(name, url, 1, k.cfg.DialTimeout, k.cfg.ResponseTimeout)
				if err != nil {
					k.log.Error("skipping endpoint", "addr", addr, "error", err)
					continue
				}

				if k.cfg.BreakerCfg != nil {
					b.Breaker = breaker.New(*k.cfg.BreakerCfg)
				}

				backends = append(backends, b)
			}
		}
	}

	k.pool.Set(backends)
	k.log.Info("endpoints reconciled",
		"service", k.cfg.ServiceName,
		"backends", len(backends),
	)
}

// findPort returns the port number to use from an EndpointSlice.
func (k *KubernetesProvider) findPort(eps *discoveryv1.EndpointSlice) int32 {
	if len(eps.Ports) == 0 {
		return 0
	}

	if k.cfg.PortName != "" {
		for _, p := range eps.Ports {
			if p.Name != nil && *p.Name == k.cfg.PortName {
				if p.Port != nil {
					return *p.Port
				}
			}
		}
		return 0 // named port not found
	}

	// Default: first port.
	if eps.Ports[0].Port != nil {
		return *eps.Ports[0].Port
	}
	return 0
}

var _ Provider = (*KubernetesProvider)(nil)
