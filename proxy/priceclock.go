package proxy

import (
	"context"
	"net/http"
	"time"
)

// Time-of-day pricing makes "when was this priced?" a billing question rather
// than a cosmetic one. A request admitted at 16:29:58 and settled at 16:30:04
// straddles a peak-pricing boundary; if admission and settlement each read the
// clock independently, the customer is charged a rate they were never quoted.
//
// One instant is pinned when the request is admitted and carried on its
// context for the rest of its life. Settlement re-uses it rather than calling
// time.Now(), so the quote is always the charge and replaying a request log
// reproduces the same cost deterministically.

type pricedAtContextKey struct{}

// withPricedAt pins the pricing instant onto a request context.
func withPricedAt(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, pricedAtContextKey{}, at)
}

// pinPricedAt stamps a request with its pricing instant, returning the request
// to use for the remainder of the call.
func pinPricedAt(r *http.Request) *http.Request {
	if r == nil {
		return nil
	}
	if _, ok := r.Context().Value(pricedAtContextKey{}).(time.Time); ok {
		return r // A retry or nested call must keep the original pin.
	}
	return r.WithContext(withPricedAt(r.Context(), time.Now()))
}

// pricedAtFrom returns the pinned instant, falling back to the current time
// for paths that never went through admission (health probes, tests). The
// fallback is deliberately the clock rather than the zero time: a zero instant
// means "no window applies", which would silently un-price peak hours.
func pricedAtFrom(ctx context.Context) time.Time {
	if ctx != nil {
		if at, ok := ctx.Value(pricedAtContextKey{}).(time.Time); ok {
			return at
		}
	}
	return time.Now()
}
