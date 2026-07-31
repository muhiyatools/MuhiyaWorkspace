package pricing

import "gateway/money"

// Cache entry lifetimes that providers price differently.
const (
	TTLDefault = "default"
	TTL5m      = "5m"
	TTL1h      = "1h"
)

// TTLRate is one operator-configured cache rate for a specific entry lifetime,
// optionally scoped to a single context tier.
//
// Both rates are pointers so that "not configured" is distinguishable from
// "configured as free". A nil rate inherits; a zero rate really is zero.
type TTLRate struct {
	// MinInputTokensExclusive nil pairs this rate with the model's base
	// rates; a value pairs it with the context tier at that threshold.
	MinInputTokensExclusive *int64         `json:"min_input_tokens_exclusive,omitempty"`
	TTL                     string         `json:"ttl"`
	CacheReadPerMillion     *money.NanoUSD `json:"cache_read_nano_usd_per_million,omitempty"`
	CacheWritePerMillion    *money.NanoUSD `json:"cache_write_nano_usd_per_million,omitempty"`
}

// OverlayTTLRates returns base with any TTL-specific rates applied for one
// tier threshold. Pass nil for the model's base rates.
//
// Rates left unset stay zero, which the rate resolver reads as "inherit the
// default-TTL rate" — that inheritance is what keeps models with no TTL
// configuration pricing exactly as they did before TTL support existed.
func OverlayTTLRates(base Rates, ttlRates []TTLRate, threshold *int64) Rates {
	for _, rate := range ttlRates {
		if !sameThreshold(rate.MinInputTokensExclusive, threshold) {
			continue
		}
		switch rate.TTL {
		case TTL5m:
			if rate.CacheReadPerMillion != nil {
				base.CacheRead5mPerMillion = *rate.CacheReadPerMillion
			}
			if rate.CacheWritePerMillion != nil {
				base.CacheWrite5mPerMillion = *rate.CacheWritePerMillion
			}
		case TTL1h:
			if rate.CacheWritePerMillion != nil {
				base.CacheWrite1hPerMillion = *rate.CacheWritePerMillion
			}
			// A one-hour cache READ is not a distinct class: providers price
			// reads by whether the entry was hit, not by how long it had left
			// to live. Only the write side varies with requested lifetime.
		case TTLDefault:
			if rate.CacheReadPerMillion != nil {
				base.CacheReadPerMillion = *rate.CacheReadPerMillion
			}
			if rate.CacheWritePerMillion != nil {
				base.CacheWritePerMillion = *rate.CacheWritePerMillion
			}
		}
	}
	return base
}

func sameThreshold(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// ValidTTL reports whether a stored lifetime string is one the engine knows.
func ValidTTL(ttl string) bool {
	return ttl == TTLDefault || ttl == TTL5m || ttl == TTL1h
}
