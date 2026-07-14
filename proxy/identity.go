package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// DeriveUserID computes a stable, opaque per-caller identifier for the upstream
// "user" field. It is HMAC-SHA256(secret, userID) rendered as "mu-" followed by
// the first 40 hex characters of the digest, matching ^mu-[0-9a-f]{40}$ (43
// chars total). Using an HMAC keyed by a server-side secret means the value is
// deterministic for a given caller yet reveals nothing about the original
// userID upstream, and cannot be forged without the secret.
//
// It returns "" when either the secret or the userID is empty; an empty result
// signals callers that identity injection is disabled and no "user" field
// should be sent.
func DeriveUserID(secret, userID string) string {
	if secret == "" || userID == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(userID))
	return "mu-" + hex.EncodeToString(mac.Sum(nil))[:40]
}
