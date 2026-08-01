package discovery

import (
	"context"
)

// Provider feeds backends into the pool from some external source.
type Provider interface {

	Run(ctx context.Context) error
}

type StaticProvider struct{}

func (StaticProvider) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

var _ Provider = StaticProvider{}
