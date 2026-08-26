package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// This file is what the one-module JWT dependency cost. golang-jwt/jwt/v5
// ships no JWKS client, so the key set is written here over crypto/rsa,
// crypto/ecdsa and encoding/json. go-jose would have deleted it for 459 KB
// and a lower-level call site — the trade is recorded in docs/spec.md 4.7.

const (
	// jwksTimeout caps one fetch. It is not a config key: a key set that
	// takes longer than this to arrive is an outage, not a slow day.
	jwksTimeout = 10 * time.Second
	// jwksMinRefetch is the floor between two fetches. It is what stops a
	// stream of junk kids becoming a stream of requests to the issuer. Ten
	// seconds, not a minute: at six requests a minute the flood costs the
	// issuer nothing, and a real key rotation heals within ten seconds
	// rather than within one.
	jwksMinRefetch = 10 * time.Second
	// jwksMaxBytes caps the body. A key set is a few hundred bytes; anything
	// approaching this is not one.
	jwksMaxBytes = 1 << 20
)

// keySet is the issuer's published signing keys, fetched lazily and kept by
// kid.
//
// Lazily, not at startup, and that is deliberate: fetching at startup would
// turn an auth-server outage into a service that will not boot, which is the
// same mistake as a liveness probe that touches a dependency.
//
// There is no background refresh goroutine either. An issuer that rotates
// keys publishes the new one before it signs with it, so the first token
// carrying an unknown kid is the only signal needed, and a goroutine polling
// an endpoint nothing has asked for is a Component's worth of lifecycle for
// no gain.
type keySet struct {
	url    string
	client *http.Client
	// minRefetch is a field rather than the constant so the internal test
	// can drive rotation without waiting ten seconds for it.
	minRefetch time.Duration

	// mu is held across the fetch. That serialises verification for as long
	// as one HTTP round trip, which is the ceiling this design accepts: the
	// fetch happens once at startup and once per rotation, and a request
	// that waits for it would otherwise be a 401. Single-flight would be the
	// upgrade if verification throughput ever came to matter.
	mu sync.Mutex
	// keys is kid -> the keys published under it, and it is a SLICE because
	// a kid is not a unique identifier. A key set may publish several keys
	// with no kid at all, which all land under "", and an issuer part way
	// through a rotation may briefly publish two under one name. A map to a
	// single key silently keeps whichever was parsed last, and then a token
	// signed by any of the others fails to verify for no visible reason.
	keys    map[string][]any
	fetched time.Time
}

func newKeySet(url string) *keySet {
	return &keySet{
		url:        url,
		client:     &http.Client{Timeout: jwksTimeout},
		minRefetch: jwksMinRefetch,
	}
}

// keyfunc is what the parser calls once it has read the header and before it
// checks the signature. A kid it does not hold triggers ONE refetch, rate
// limited by minRefetch.
func (k *keySet) keyfunc(t *jwt.Token) (any, error) {
	kid, _ := t.Header["kid"].(string)

	k.mu.Lock()
	defer k.mu.Unlock()

	// A kid the cache does not hold triggers ONE refetch, rate limited by
	// minRefetch. No kid on the token is legal — an issuer publishing a
	// single key often omits it on both sides — and then every key is a
	// candidate and the signature decides, which is the only thing that
	// should.
	if set, ok := k.candidates(kid); ok {
		return set, nil
	}
	if err := k.refresh(kid); err != nil {
		return nil, err
	}
	if set, ok := k.candidates(kid); ok {
		return set, nil
	}
	return nil, fmt.Errorf("security.jwt.jwksUrl: no signing key %q in the key set at %s", kid, k.url)
}

// candidates is the keys worth trying for this kid: the ones published under
// it, or every key when the token named none. It is called with mu held.
//
// The parser tries each in turn, so returning more than one costs a failed
// signature check per extra key and never widens what verifies: a token still
// has to be signed by one of the keys this issuer published.
func (k *keySet) candidates(kid string) (jwt.VerificationKeySet, bool) {
	var keys []any
	if kid == "" {
		for _, ks := range k.keys {
			keys = append(keys, ks...)
		}
	} else {
		keys = k.keys[kid]
	}
	set := jwt.VerificationKeySet{Keys: make([]jwt.VerificationKey, 0, len(keys))}
	for _, key := range keys {
		set.Keys = append(set.Keys, key)
	}
	return set, len(set.Keys) > 0
}

// refresh fetches once, subject to the floor. It is called with mu held.
func (k *keySet) refresh(kid string) error {
	if !k.fetched.IsZero() && time.Since(k.fetched) < k.minRefetch {
		return fmt.Errorf("security.jwt.jwksUrl: no signing key %q in the key set, and it was refreshed less than %s ago", kid, k.minRefetch)
	}
	return k.fetch()
}

// fetch replaces the whole key set. It is called with mu held.
//
// The new set replaces the old one outright rather than merging: an issuer
// that withdraws a key means it, and a merged cache would go on trusting a
// key that was revoked.
func (k *keySet) fetch() error {
	// fetched is stamped whether or not the fetch worked, so an issuer that
	// is down cannot be hammered once per request.
	k.fetched = time.Now()

	resp, err := k.client.Get(k.url)
	if err != nil {
		return fmt.Errorf("security.jwt.jwksUrl: fetching the key set from %s: %w", k.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("security.jwt.jwksUrl: fetching the key set from %s: %s", k.url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, jwksMaxBytes))
	if err != nil {
		return fmt.Errorf("security.jwt.jwksUrl: reading the key set from %s: %w", k.url, err)
	}
	keys, err := parseJWKS(body)
	if err != nil {
		return fmt.Errorf("security.jwt.jwksUrl: the key set at %s: %w", k.url, err)
	}
	k.keys = keys
	return nil
}

// jwk is the subset of RFC 7517 go-boot reads: the two key types every OAuth2
// issuer publishes.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`

	N string `json:"n"` // RSA modulus
	E string `json:"e"` // RSA exponent

	Crv string `json:"crv"` // EC curve
	X   string `json:"x"`
	Y   string `json:"y"`
}

// parseJWKS turns the published document into public keys by kid.
//
// A key it cannot read is SKIPPED rather than fatal — an issuer publishing an
// OKP key beside its RSA ones must not break every verification — but a
// document that yields no usable key at all is an error, because that is
// indistinguishable from pointing jwksUrl at the wrong endpoint.
func parseJWKS(body []byte) (map[string][]any, error) {
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("is not JSON: %w", err)
	}
	out := map[string][]any{}
	for _, j := range doc.Keys {
		// "use" is optional; when it IS there and says encryption, the key
		// is not for verifying signatures.
		if j.Use != "" && j.Use != "sig" {
			continue
		}
		key, err := j.publicKey()
		if err != nil {
			continue
		}
		// Appended, not assigned: see the comment on keySet.keys.
		out[j.Kid] = append(out[j.Kid], key)
	}
	if len(out) == 0 {
		return nil, errors.New("holds no usable signing key")
	}
	return out, nil
}

func (j jwk) publicKey() (any, error) {
	switch j.Kty {
	case "RSA":
		return j.rsaKey()
	case "EC":
		return j.ecKey()
	}
	return nil, fmt.Errorf("unsupported key type %q", j.Kty)
}

func (j jwk) rsaKey() (*rsa.PublicKey, error) {
	n, err := b64uint(j.N)
	if err != nil {
		return nil, err
	}
	e, err := b64uint(j.E)
	if err != nil {
		return nil, err
	}
	// The exponent is an int in crypto/rsa, and a published one is 65537.
	// Anything that does not fit is not a key this can hold, and truncating
	// it would build a DIFFERENT key that verifies nothing.
	if !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
		return nil, fmt.Errorf("rsa exponent %s is out of range", e)
	}
	return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}

func (j jwk) ecKey() (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	var size int
	switch j.Crv {
	case "P-256":
		curve, size = elliptic.P256(), 32
	case "P-384":
		curve, size = elliptic.P384(), 48
	case "P-521":
		curve, size = elliptic.P521(), 66
	default:
		return nil, fmt.Errorf("unsupported curve %q", j.Crv)
	}
	x, err := base64.RawURLEncoding.DecodeString(j.X)
	if err != nil {
		return nil, fmt.Errorf("ec x: %w", err)
	}
	y, err := base64.RawURLEncoding.DecodeString(j.Y)
	if err != nil {
		return nil, fmt.Errorf("ec y: %w", err)
	}
	if len(x) != size || len(y) != size {
		return nil, fmt.Errorf("ec coordinates are %d and %d bytes, want %d for %s", len(x), len(y), size, j.Crv)
	}
	// Built as an uncompressed SEC 1 point and handed to crypto/ecdsa rather
	// than assembled field by field, because this call REJECTS a point that
	// is not on the curve. A hand-built ecdsa.PublicKey would accept one,
	// and an off-curve point is a known way to attack a verifier.
	point := append([]byte{4}, append(x, y...)...)
	return ecdsa.ParseUncompressedPublicKey(curve, point)
}

// b64uint reads one base64url big-endian unsigned integer, which is how RFC
// 7518 encodes every RSA field.
func b64uint(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("is not base64url: %w", err)
	}
	if len(b) == 0 {
		return nil, errors.New("is empty")
	}
	return new(big.Int).SetBytes(b), nil
}
