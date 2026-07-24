// Package discovery abstracts WHERE the list of backends comes from.
//
// Week 1 used a static list from gobalance.yaml. Week 4 added hot reload.
// Week 5 adds a second source: Kubernetes EndpointSlice objects, so backends
// are added and removed automatically when pods scale up and down.
//
// The Provider interface lets the balancer not care which source is active.
package discovery

import (
	"context"
)

// Provider feeds backends into the pool from some external source.
type Provider interface {
	// Run watches the source and updates the pool until ctx is cancelled.
	// It blocks, so call it in a goroutine.
	Run(ctx context.Context) error
}

// StaticProvider is the week-1 behaviour: backends come from the config file.
// Hot reload (week 4) handles changes. This satisfies Provider so main.go can
// treat both sources uniformly.
type StaticProvider struct{}

func (StaticProvider) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

var _ Provider = StaticProvider{}
