// Package secure holds the small cryptographic helpers shared by every
// component: random tokens, token hashing and HMAC-signed blobs. Anything that
// creates or checks a credential goes through here, so there is exactly one
// implementation to review.
package secure

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Token returns prefix + n random bytes, base64url-encoded. Prefixes
// (hilo_pat_, hilo_at_…) make leaked tokens recognisable to secret scanners.
func Token(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails on supported platforms; if it does, nothing
		// that depends on it can be trusted, so stop.
		panic("secure: crypto/rand failed: " + err.Error())
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// Hash is the at-rest form of every token: hex(SHA-256). Tokens are 256-bit
// random values, so a fast hash is the right choice (no password stretching
// needed) and lookups can be by equality on an indexed column.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Equal compares two strings in constant time.
func Equal(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

// PKCES256 computes the S256 code challenge for a verifier (RFC 7636).
func PKCES256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ErrInvalidBlob is returned for tampered, malformed or expired blobs.
var ErrInvalidBlob = errors.New("secure: invalid or expired blob")

type envelope struct {
	Purpose string          `json:"p"`
	Exp     int64           `json:"e"`
	Data    json.RawMessage `json:"d"`
}

// Sign serialises v into a URL-safe, HMAC-SHA256-signed blob valid for ttl.
// purpose is bound into the signature so a blob minted for one flow can never
// be replayed into another.
func Sign(key []byte, purpose string, v any, ttl time.Duration) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(envelope{Purpose: purpose, Exp: time.Now().Add(ttl).Unix(), Data: data})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	return payload + "." + mac(key, payload), nil
}

// Verify checks a blob produced by Sign for the same purpose and decodes it
// into v.
func Verify(key []byte, purpose, blob string, v any) error {
	payload, sig, ok := strings.Cut(blob, ".")
	if !ok || payload == "" || !Equal(sig, mac(key, payload)) {
		return ErrInvalidBlob
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return ErrInvalidBlob
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Purpose != purpose || time.Now().Unix() >= env.Exp {
		return ErrInvalidBlob
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		return ErrInvalidBlob
	}
	return nil
}

func mac(key []byte, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
