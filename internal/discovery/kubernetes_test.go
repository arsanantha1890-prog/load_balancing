package discovery_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/arsanantha1890-prog/load_balancing/internal/discovery"
	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func boolPtr(b bool) *bool       { return &b }
func int32Ptr(i int32) *int32    { return &i }
func strPtr(s string) *string    { return &s }

// makeSlice builds an EndpointSlice with the given addresses, all ready.
func makeSlice(name, namespace, serviceName string, port int32, addrs ...string) *discoveryv1.EndpointSlice {
	endpoints := make([]discoveryv1.Endpoint, 0, len(addrs))
	for _, addr := range addrs {
		endpoints = append(endpoints, discoveryv1.Endpoint{
			Addresses:  []string{addr},
			Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)},
		})
	}

	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				discoveryv1.LabelServiceName: serviceName,
			},
		},
		Ports: []discoveryv1.EndpointPort{
			{Port: int32Ptr(port)},
		},
		Endpoints: endpoints,
	}
}

// TestDiscoversEndpoints verifies the happy path: an EndpointSlice exists
// before the provider starts, and the pool is populated on startup.
func TestDiscoversEndpoints(t *testing.T) {
	slice := makeSlice("my-svc-abc", "default", "my-svc", 8080,
		"10.0.0.1", "10.0.0.2", "10.0.0.3")

	client := fake.NewSimpleClientset(slice)
	p := pool.New(nil)

	provider := discovery.NewKubernetesProvider(client, p, discovery.KubernetesConfig{
		ServiceName:     "my-svc",
		Namespace:       "default",
		DialTimeout:     time.Second,
		ResponseTimeout: time.Second,
	}, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go provider.Run(ctx)

	// Wait for the informer cache to sync and reconcile to fire.
	waitFor(t, "3 backends in pool", 5*time.Second, func() bool {
		return len(p.Healthy()) == 3
	})

	backends := p.Healthy()
	for _, b := range backends {
		t.Logf("discovered: %s -> %s", b.Name, b.URL)
	}
}

// TestScaleUp verifies that adding a new EndpointSlice entry results in a new
// backend appearing in the pool, just as kubectl scale would.
func TestScaleUp(t *testing.T) {
	slice := makeSlice("my-svc-abc", "default", "my-svc", 8080,
		"10.0.0.1")

	client := fake.NewSimpleClientset(slice)
	p := pool.New(nil)

	provider := discovery.NewKubernetesProvider(client, p, discovery.KubernetesConfig{
		ServiceName:     "my-svc",
		Namespace:       "default",
		DialTimeout:     time.Second,
		ResponseTimeout: time.Second,
	}, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go provider.Run(ctx)

	waitFor(t, "1 backend", 5*time.Second, func() bool {
		return len(p.Healthy()) == 1
	})

	// Scale up: add two more addresses.
	scaled := makeSlice("my-svc-abc", "default", "my-svc", 8080,
		"10.0.0.1", "10.0.0.2", "10.0.0.3")

	_, err := client.DiscoveryV1().EndpointSlices("default").Update(
		ctx, scaled, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("update EndpointSlice: %v", err)
	}

	waitFor(t, "3 backends after scale-up", 5*time.Second, func() bool {
		return len(p.Healthy()) == 3
	})
}

// TestScaleDown verifies that removing addresses from an EndpointSlice removes
// backends from the pool.
func TestScaleDown(t *testing.T) {
	slice := makeSlice("my-svc-abc", "default", "my-svc", 8080,
		"10.0.0.1", "10.0.0.2", "10.0.0.3")

	client := fake.NewSimpleClientset(slice)
	p := pool.New(nil)

	provider := discovery.NewKubernetesProvider(client, p, discovery.KubernetesConfig{
		ServiceName:     "my-svc",
		Namespace:       "default",
		DialTimeout:     time.Second,
		ResponseTimeout: time.Second,
	}, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go provider.Run(ctx)

	waitFor(t, "3 backends", 5*time.Second, func() bool {
		return len(p.Healthy()) == 3
	})

	// Scale down to 1.
	scaled := makeSlice("my-svc-abc", "default", "my-svc", 8080, "10.0.0.1")

	_, err := client.DiscoveryV1().EndpointSlices("default").Update(
		ctx, scaled, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("update EndpointSlice: %v", err)
	}

	waitFor(t, "1 backend after scale-down", 5*time.Second, func() bool {
		return len(p.Healthy()) == 1
	})
}

// TestIgnoresNotReadyEndpoints verifies that pods shutting down (Ready=false)
// are excluded from the pool.
func TestIgnoresNotReadyEndpoints(t *testing.T) {
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-svc-abc",
			Namespace: "default",
			Labels: map[string]string{
				discoveryv1.LabelServiceName: "my-svc",
			},
		},
		Ports: []discoveryv1.EndpointPort{
			{Port: int32Ptr(8080)},
		},
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{"10.0.0.1"},
				Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)},
			},
			{
				Addresses:  []string{"10.0.0.2"},
				Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(false)}, // shutting down
			},
			{
				Addresses:  []string{"10.0.0.3"},
				Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)},
			},
		},
	}

	client := fake.NewSimpleClientset(slice)
	p := pool.New(nil)

	provider := discovery.NewKubernetesProvider(client, p, discovery.KubernetesConfig{
		ServiceName:     "my-svc",
		Namespace:       "default",
		DialTimeout:     time.Second,
		ResponseTimeout: time.Second,
	}, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go provider.Run(ctx)

	waitFor(t, "2 backends (not-ready excluded)", 5*time.Second, func() bool {
		return len(p.Healthy()) == 2
	})

	// Verify the not-ready endpoint is not in the pool.
	for _, b := range p.Healthy() {
		if b.URL.Host == "10.0.0.2:8080" {
			t.Error("not-ready endpoint 10.0.0.2 should be excluded from the pool")
		}
	}
}

// TestIgnoresOtherServices verifies that EndpointSlices for a different Service
// are not picked up.
func TestIgnoresOtherServices(t *testing.T) {
	ours := makeSlice("my-svc-abc", "default", "my-svc", 8080, "10.0.0.1")
	theirs := makeSlice("other-svc-xyz", "default", "other-svc", 9090,
		"10.0.1.1", "10.0.1.2")

	client := fake.NewSimpleClientset(ours, theirs)
	p := pool.New(nil)

	provider := discovery.NewKubernetesProvider(client, p, discovery.KubernetesConfig{
		ServiceName:     "my-svc",
		Namespace:       "default",
		DialTimeout:     time.Second,
		ResponseTimeout: time.Second,
	}, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go provider.Run(ctx)

	waitFor(t, "1 backend (only our service)", 5*time.Second, func() bool {
		return len(p.Healthy()) == 1
	})

	// Give it extra time to make sure it doesn't pick up the other service.
	time.Sleep(200 * time.Millisecond)
	if got := len(p.Healthy()); got != 1 {
		t.Fatalf("expected 1 backend, got %d — may have picked up the wrong service", got)
	}
}

// TestNamedPort verifies that when PortName is set, only the matching port is used.
func TestNamedPort(t *testing.T) {
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-svc-abc",
			Namespace: "default",
			Labels: map[string]string{
				discoveryv1.LabelServiceName: "my-svc",
			},
		},
		Ports: []discoveryv1.EndpointPort{
			{Name: strPtr("metrics"), Port: int32Ptr(9090)},
			{Name: strPtr("http"), Port: int32Ptr(8080)},
		},
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{"10.0.0.1"},
				Conditions: discoveryv1.EndpointConditions{Ready: boolPtr(true)},
			},
		},
	}

	client := fake.NewSimpleClientset(slice)
	p := pool.New(nil)

	provider := discovery.NewKubernetesProvider(client, p, discovery.KubernetesConfig{
		ServiceName:     "my-svc",
		Namespace:       "default",
		PortName:        "http",
		DialTimeout:     time.Second,
		ResponseTimeout: time.Second,
	}, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go provider.Run(ctx)

	waitFor(t, "1 backend on port 8080", 5*time.Second, func() bool {
		return len(p.Healthy()) == 1
	})

	b := p.Healthy()[0]
	if b.URL.Host != "10.0.0.1:8080" {
		t.Errorf("backend URL = %s, want 10.0.0.1:8080 (the http port, not metrics)", b.URL)
	}
}

// TestDeleteEndpointSlice verifies that deleting the entire EndpointSlice
// removes all backends from the pool.
func TestDeleteEndpointSlice(t *testing.T) {
	slice := makeSlice("my-svc-abc", "default", "my-svc", 8080,
		"10.0.0.1", "10.0.0.2")

	client := fake.NewSimpleClientset(slice)
	p := pool.New(nil)

	provider := discovery.NewKubernetesProvider(client, p, discovery.KubernetesConfig{
		ServiceName:     "my-svc",
		Namespace:       "default",
		DialTimeout:     time.Second,
		ResponseTimeout: time.Second,
	}, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go provider.Run(ctx)

	waitFor(t, "2 backends", 5*time.Second, func() bool {
		return len(p.Healthy()) == 2
	})

	err := client.DiscoveryV1().EndpointSlices("default").Delete(
		ctx, "my-svc-abc", metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("delete EndpointSlice: %v", err)
	}

	waitFor(t, "0 backends after delete", 5*time.Second, func() bool {
		return len(p.All()) == 0
	})
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}
