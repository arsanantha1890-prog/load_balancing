package retryctx

import "context"

// State is a mutable per-request slot for the transport error.
type State struct {
	Err error
}

type ctxKey struct{}

// With returns a context carrying a fresh State, plus the State itself.
func With(ctx context.Context) (context.Context, *State) {
	s := &State{}
	return context.WithValue(ctx, ctxKey{}, s), s
}

// From retrieves the State, or nil if the context has none. A nil result means nobody is watching for retries, so an ErrorHandler should write a 502 itself.
func From(ctx context.Context) *State {
	s, _ := ctx.Value(ctxKey{}).(*State)
	return s
}
