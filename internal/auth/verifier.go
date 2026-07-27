// Package auth verifies Supabase access tokens.
//
// Supabase migrated new projects to ASYMMETRIC JWT signing keys (ES256 /
// ECDSA P-256) — the default for projects created since Oct 2025. Tokens
// are therefore no longer signed with the legacy HS256 shared secret, so
// verifying with []byte(secret) fails for every request (a 401 that logs
// users straight back out).
//
// TokenVerifier fetches the project's public keys from the JWKS endpoint
//
//	https://<project>.supabase.co/auth/v1/.well-known/jwks.json
//
// and verifies ES256 tokens against them, while still accepting legacy
// HS256 tokens (Supabase also serves the old shared secret in the JWKS
// as an "oct" key, and we keep the configured secret as a final
// fallback). Uses only the standard library plus golang-jwt/v5, which is
// already a dependency — nothing new to download.
package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type TokenVerifier struct {
	jwksURL      string
	sharedSecret []byte
	client       *http.Client

	mu        sync.RWMutex
	keys      map[string]interface{} // kid -> *ecdsa.PublicKey (ES256) or []byte (HS256)
	fetchedAt time.Time
}

// NewTokenVerifier builds a verifier for the given Supabase project URL
// (e.g. https://abc.supabase.co) and legacy shared secret. It attempts an
// initial JWKS fetch so the first request is fast, but a failure here is
// non-fatal — it retries lazily on the first token it can't resolve.
func NewTokenVerifier(supabaseURL, sharedSecret string) *TokenVerifier {
	v := &TokenVerifier{
		jwksURL:      strings.TrimRight(supabaseURL, "/") + "/auth/v1/.well-known/jwks.json",
		sharedSecret: []byte(sharedSecret),
		client:       &http.Client{Timeout: 10 * time.Second},
		keys:         map[string]interface{}{},
	}
	_ = v.refresh() // best-effort warm-up
	return v
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	K   string `json:"k"`
}

func (v *TokenVerifier) refresh() error {
	resp, err := v.client.Get(v.jwksURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch: unexpected status %d", resp.StatusCode)
	}

	var body struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}

	parsed := make(map[string]interface{}, len(body.Keys))
	for _, k := range body.Keys {
		switch k.Kty {
		case "EC":
			if pub, err := ecPublicKey(k); err == nil {
				parsed[k.Kid] = pub
			}
		case "oct":
			if b, err := base64.RawURLEncoding.DecodeString(k.K); err == nil {
				parsed[k.Kid] = b
			}
		}
	}

	v.mu.Lock()
	v.keys = parsed
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return nil
}

func ecPublicKey(k jwk) (*ecdsa.PublicKey, error) {
	if k.Crv != "P-256" {
		return nil, fmt.Errorf("unsupported EC curve %q", k.Crv)
	}
	xb, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, err
	}
	yb, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, err
	}
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(xb),
		Y:     new(big.Int).SetBytes(yb),
	}, nil
}

func (v *TokenVerifier) lookup(kid string) (interface{}, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	k, ok := v.keys[kid]
	return k, ok
}

// refreshThrottled avoids hammering the JWKS endpoint when a token has an
// unknown kid (e.g. a bad token) — but always allows a refresh if we've
// never successfully fetched.
func (v *TokenVerifier) refreshThrottled() {
	v.mu.RLock()
	stale := v.fetchedAt.IsZero() || time.Since(v.fetchedAt) > 30*time.Second
	v.mu.RUnlock()
	if stale {
		_ = v.refresh()
	}
}

// Keyfunc is a jwt.Keyfunc: it returns the verification key for a token
// based on its kid and signing method. A newly-rotated key triggers one
// throttled JWKS refresh before giving up.
func (v *TokenVerifier) Keyfunc(t *jwt.Token) (interface{}, error) {
	kid, _ := t.Header["kid"].(string)

	key, ok := v.lookup(kid)
	if !ok {
		v.refreshThrottled()
		key, ok = v.lookup(kid)
	}

	switch t.Method.(type) {
	case *jwt.SigningMethodECDSA:
		if pub, isEC := key.(*ecdsa.PublicKey); isEC {
			return pub, nil
		}
		return nil, errors.New("no EC public key for token kid")
	case *jwt.SigningMethodHMAC:
		if b, isOct := key.([]byte); isOct && len(b) > 0 {
			return b, nil
		}
		if len(v.sharedSecret) > 0 {
			return v.sharedSecret, nil // legacy HS256 fallback
		}
		return nil, errors.New("no HMAC key available for token")
	default:
		return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
	}
}

// Parse verifies a raw token string and returns its claims. Only ES256
// and HS256 are permitted (guards against alg-confusion attacks).
func (v *TokenVerifier) Parse(raw string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, v.Keyfunc,
		jwt.WithValidMethods([]string{"ES256", "HS256"}))
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, jwt.ErrTokenInvalidClaims
	}
	return claims, nil
}
