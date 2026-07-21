// Package retryctx carries per-request failure information from a backend's
// ReverseProxy ErrorHandler back to the balancer.
//
// WHY THIS EXISTS
//
// httputil.ReverseProxy writes directly to the client's ResponseWriter. By the
// time you see a 503 in the response, the bytes are already on the wire and
// retrying is impossible -- you cannot un-send a response.
//
// But ReverseProxy calls its ErrorHandler only for TRANSPORT failures: refused
// connections, DNS failures, timeouts before the response line arrives. At that
// moment nothing has been written yet, so a retry is still legal.
//
// The default ErrorHandler writes a 502 immediately, which forecloses that
// option. So each backend's ErrorHandler instead records the error into this
// context-carried struct and writes nothing, leaving the balancer to decide:
// retry elsewhere, or give up and write the 502 itself.
//
// This is the mechanism that makes retries possible at all, and it is a good
// answer to "why can't you just retry any failed request?" -- because once the
// response has started, you can't.
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

// From retrieves the State, or nil if the context has none. A nil result means
// nobody is watching for retries, so an ErrorHandler should write a 502 itself.
func From(ctx context.Context) *State {
	s, _ := ctx.Value(ctxKey{}).(*State)
	return s
}
