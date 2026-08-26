package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// These are the key set's own mechanics: rotation, the refetch floor and the
// JWK reader. They are internal because the floor is ten seconds in
// production, and a test that waited for it would be a test nobody runs.

// jwksServer serves whatever the caller currently wants, and counts fetches.
type jwksServer struct {
	*httptest.Server
	body    atomic.Value // string
	fetches atomic.Int64
}

func newJWKSServer(t *testing.T, body string) *jwksServer {
	j := &jwksServer{}
	j.body.Store(body)
	j.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j.fetches.Add(1)
		_, _ = io.WriteString(w, j.body.Load().(string))
	}))
	t.Cleanup(j.Close)
	return j
}

func rsaJWKS(t *testing.T, kid string) (string, *rsa.PrivateKey) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return fmt.Sprintf(`{"keys":[{"kty":"RSA","use":"sig","kid":%q,"n":%q,"e":%q}]}`,
		kid, enc(k.N.Bytes()), enc(big.NewInt(int64(k.E)).Bytes())), k
}

func tokenWithKid(kid string) *jwt.Token {
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{})
	tok.Header["kid"] = kid
	return tok
}

// TestAnUnknownKidRefetchesTheKeySet is rotation: the issuer swaps its key,
// and the first token carrying the new kid is the only signal the set needs.
func TestAnUnknownKidRefetchesTheKeySet(t *testing.T) {
	body1, _ := rsaJWKS(t, "k1")
	srv := newJWKSServer(t, body1)
	ks := newKeySet(srv.URL)
	ks.minRefetch = 0 // the floor is what the next test covers

	if _, err := ks.keyfunc(tokenWithKid("k1")); err != nil {
		t.Fatalf("k1: %v", err)
	}
	if got := srv.fetches.Load(); got != 1 {
		t.Fatalf("fetches = %d, want 1", got)
	}
	// A second token on the same kid must NOT go back to the issuer.
	if _, err := ks.keyfunc(tokenWithKid("k1")); err != nil {
		t.Fatalf("k1 again: %v", err)
	}
	if got := srv.fetches.Load(); got != 1 {
		t.Fatalf("fetches = %d after a cached kid, want 1", got)
	}

	body2, _ := rsaJWKS(t, "k2")
	srv.body.Store(body2)
	if _, err := ks.keyfunc(tokenWithKid("k2")); err != nil {
		t.Fatalf("k2 after rotation: %v", err)
	}
	if got := srv.fetches.Load(); got != 2 {
		t.Fatalf("fetches = %d after rotation, want 2", got)
	}
	// The withdrawn key is gone, not merged: an issuer that removes a key
	// means it, and a merged cache would go on trusting a revoked one.
	if _, err := ks.keyfunc(tokenWithKid("k1")); err == nil {
		t.Fatal("the withdrawn kid k1 still verifies")
	}
}

// TestTheRefetchFloorHolds is what stops a stream of junk kids becoming a
// stream of requests to the issuer.
func TestTheRefetchFloorHolds(t *testing.T) {
	body, _ := rsaJWKS(t, "k1")
	srv := newJWKSServer(t, body)
	ks := newKeySet(srv.URL)

	for i := range 50 {
		_, err := ks.keyfunc(tokenWithKid(fmt.Sprintf("junk-%d", i)))
		if err == nil {
			t.Fatalf("junk-%d verified", i)
		}
	}
	if got := srv.fetches.Load(); got != 1 {
		t.Fatalf("fetches = %d for 50 unknown kids, want 1", got)
	}
	if ks.minRefetch != jwksMinRefetch {
		t.Fatalf("minRefetch = %s, want the %s default", ks.minRefetch, jwksMinRefetch)
	}
}

// TestAFailedFetchIsNotRetriedPerRequest: an issuer that is down must not be
// hammered once per inbound request.
func TestAFailedFetchIsNotRetriedPerRequest(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ks := newKeySet(srv.URL)
	for range 20 {
		if _, err := ks.keyfunc(tokenWithKid("k1")); err == nil {
			t.Fatal("a 503 key set verified a token")
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d against a failing issuer, want 1", got)
	}
}

// TestParseJWKS covers the reader itself, including the two documents that
// must be refused rather than half-read.
func TestParseJWKS(t *testing.T) {
	rsaBody, _ := rsaJWKS(t, "k1")
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	ecKey := fmt.Sprintf(`{"kty":"EC","kid":"ec1","crv":"P-256","x":%q,"y":%q}`,
		enc(ec.X.FillBytes(make([]byte, 32))), enc(ec.Y.FillBytes(make([]byte, 32))))

	t.Run("rsa and ec together", func(t *testing.T) {
		body := `{"keys":[` + ecKey + `,` + strings.TrimSuffix(strings.TrimPrefix(rsaBody, `{"keys":[`), `]}`) + `]}`
		keys, err := parseJWKS([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) != 2 {
			t.Fatalf("got %d keys, want 2", len(keys))
		}
		if len(keys["ec1"]) != 1 {
			t.Fatalf("ec1 holds %d keys, want 1", len(keys["ec1"]))
		}
		if _, ok := keys["ec1"][0].(*ecdsa.PublicKey); !ok {
			t.Fatalf("ec1 is %T", keys["ec1"][0])
		}
		if _, ok := keys["k1"][0].(*rsa.PublicKey); !ok {
			t.Fatalf("k1 is %T", keys["k1"][0])
		}
	})

	t.Run("an unreadable key beside a good one is skipped", func(t *testing.T) {
		body := `{"keys":[{"kty":"OKP","kid":"ed1","crv":"Ed25519","x":"AAAA"},` +
			strings.TrimSuffix(strings.TrimPrefix(rsaBody, `{"keys":[`), `]}`) + `]}`
		keys, err := parseJWKS([]byte(body))
		if err != nil {
			t.Fatalf("one unsupported key broke the whole set: %v", err)
		}
		if len(keys) != 1 {
			t.Fatalf("got %d keys, want only the RSA one", len(keys))
		}
	})

	t.Run("an encryption key is not a signing key", func(t *testing.T) {
		body := strings.Replace(rsaBody, `"use":"sig"`, `"use":"enc"`, 1)
		if _, err := parseJWKS([]byte(body)); err == nil {
			t.Fatal("an enc-only key set was accepted for verification")
		}
	})

	t.Run("an off-curve point is refused", func(t *testing.T) {
		// y is replaced with a value that is not the curve's, which is a
		// known way to attack a verifier that assembles the key by field.
		bad := fmt.Sprintf(`{"keys":[{"kty":"EC","kid":"ec1","crv":"P-256","x":%q,"y":%q}]}`,
			enc(ec.X.FillBytes(make([]byte, 32))), enc(make([]byte, 32)))
		if _, err := parseJWKS([]byte(bad)); err == nil {
			t.Fatal("an off-curve point was accepted")
		}
	})

	t.Run("several keys with no kid all survive", func(t *testing.T) {
		// They collide on the empty kid, and a map to a single key would
		// keep only the last. TestATokenWithNoKidTriesEveryKey is the same
		// rule seen from the parser's side.
		one := `{"kty":"RSA","use":"sig","n":"` + enc(big.NewInt(0).SetBytes([]byte(strings.Repeat("\x01", 256))).Bytes()) + `","e":"AQAB"}`
		body := `{"keys":[` + one + `,` + one + `]}`
		keys, err := parseJWKS([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if len(keys[""]) != 2 {
			t.Fatalf("the empty kid holds %d keys, want 2", len(keys[""]))
		}
	})

	t.Run("an empty document is an error", func(t *testing.T) {
		for _, body := range []string{`{"keys":[]}`, `{}`, `not json`} {
			if _, err := parseJWKS([]byte(body)); err == nil {
				t.Errorf("%q was accepted as a key set", body)
			}
		}
	})

	t.Run("a silly rsa exponent is refused", func(t *testing.T) {
		body := strings.Replace(rsaBody, `"e":"AQAB"`, `"e":"`+enc(big.NewInt(1<<40).Bytes())+`"`, 1)
		if body == rsaBody {
			t.Skip("exponent was not AQAB")
		}
		if _, err := parseJWKS([]byte(body)); err == nil {
			t.Fatal("an out-of-range exponent was accepted")
		}
	})
}

// TestBearer covers the header parsing, which is the one place a caller
// controls the input shape.
func TestBearer(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer abc", "abc"},
		{"bearer abc", "abc"}, // RFC 7235 says the scheme is case-insensitive
		{"BEARER abc", "abc"},
		{"Bearer  abc ", "abc"},
		{"", ""},
		{"Bearer", ""},
		{"Bearer ", ""},
		{"Basic abc", ""},
		{"abc", ""},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}
		got, ok := bearer(r)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("bearer(%q) = %q, %v; want %q", tc.header, got, ok, tc.want)
		}
	}
}

// TestPrincipalHas is the scope arithmetic on its own, since the two
// quantifiers are easy to get backwards.
func TestPrincipalHas(t *testing.T) {
	p := &Principal{Scopes: []string{"a", "b"}}
	cases := []struct {
		want []string
		all  bool
		ok   bool
	}{
		{[]string{"a"}, true, true},
		{[]string{"a", "b"}, true, true},
		{[]string{"a", "c"}, true, false},
		{[]string{"a", "c"}, false, true},
		{[]string{"c", "d"}, false, false},
		// An empty want is "no scope required" for BOTH quantifiers. Read
		// literally, "at least one of nothing" is false, which would make
		// RequireAnyScope() with no arguments refuse everybody — a footgun
		// with no use behind it.
		{nil, true, true},
		{nil, false, true},
	}
	for _, tc := range cases {
		if got := p.has(tc.want, tc.all); got != tc.ok {
			t.Errorf("has(%v, all=%v) = %v, want %v", tc.want, tc.all, got, tc.ok)
		}
	}
	empty := &Principal{}
	if empty.has([]string{"a"}, true) || empty.has([]string{"a"}, false) {
		t.Error("a Principal with no scopes satisfied a scope check")
	}
}

// TestTheKeySetBodyIsCapped pins the read limit, and it is written so that
// the LIMIT is what fails rather than the client timeout. The document below
// is valid JSON with a usable key at the end of it, and it is bigger than the
// cap: truncated, it stops being JSON, so the fetch is refused. Written as an
// endless stream instead, the 10s client timeout would refuse it either way
// and the cap would be untested.
func TestTheKeySetBodyIsCapped(t *testing.T) {
	good, _ := rsaJWKS(t, "k1")
	inner := strings.TrimSuffix(strings.TrimPrefix(good, `{"keys":[`), `]}`)
	padding := strings.Repeat("A", jwksMaxBytes)
	body := `{"keys":[{"kty":"oct","kid":"pad","k":"` + padding + `"},` + inner + `]}`

	srv := newJWKSServer(t, body)
	ks := newKeySet(srv.URL)
	if _, err := ks.keyfunc(tokenWithKid("k1")); err == nil {
		t.Fatal("a key set larger than the cap was read whole")
	}
}

// TestTheAlgorithmSetIsWhatTheSpecSays pins the list itself. It is a weak
// test on purpose, and the comment on allowedAlgs says why: golang-jwt's key
// type check already refuses HS256 and "none" against a public key, so no
// end-to-end test can tell the list's absence from its presence. What this
// holds is the promise in docs/spec.md 4.7 — these nine and nothing symmetric.
func TestTheAlgorithmSetIsWhatTheSpecSays(t *testing.T) {
	want := []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}
	if len(allowedAlgs) != len(want) {
		t.Fatalf("allowedAlgs = %v, want %v", allowedAlgs, want)
	}
	for i, a := range want {
		if allowedAlgs[i] != a {
			t.Fatalf("allowedAlgs = %v, want %v", allowedAlgs, want)
		}
	}
	for _, a := range allowedAlgs {
		if strings.HasPrefix(a, "HS") || a == "none" {
			t.Fatalf("%q is symmetric or unsigned and must never be on the list", a)
		}
	}
}

// TestCheckJWKSURL is the constructor rule that matters most in this package:
// the key set is the root of trust, so a URL an attacker can rewrite is a
// total authentication bypass that looks like nothing in a config file.
func TestCheckJWKSURL(t *testing.T) {
	ok := []string{
		"https://auth.example.com/.well-known/jwks.json",
		"http://127.0.0.1:8080/jwks",
		"http://[::1]:8080/jwks",
		"http://localhost:8080/jwks",
	}
	bad := []string{
		"http://auth.example.com/jwks",
		"http://10.0.0.5/jwks",
		"http://evil.test/jwks",
		"file:///etc/jwks.json",
		"ftp://auth.example.com/jwks",
		"auth.example.com/jwks", // no scheme at all
	}
	for _, u := range ok {
		if err := checkJWKSURL(u); err != nil {
			t.Errorf("checkJWKSURL(%q) = %v, want nil", u, err)
		}
	}
	for _, u := range bad {
		if err := checkJWKSURL(u); err == nil {
			t.Errorf("checkJWKSURL(%q) was accepted", u)
		} else if !strings.HasPrefix(err.Error(), "security.jwt.jwksUrl") {
			t.Errorf("checkJWKSURL(%q) = %v, which does not open with the config key", u, err)
		}
	}
}

// TestATokenWithNoKidTriesEveryKey: an issuer publishing one key often omits
// kid on both sides. Picking one key out of the map would be picking whichever
// iteration order handed over.
func TestATokenWithNoKidTriesEveryKey(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString
	var keys []*rsa.PrivateKey
	var entries []string
	for range 3 {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
		// No kid on any of them, which is what makes this the hard case.
		entries = append(entries, fmt.Sprintf(`{"kty":"RSA","use":"sig","n":%q,"e":%q}`,
			enc(k.N.Bytes()), enc(big.NewInt(int64(k.E)).Bytes())))
	}
	srv := newJWKSServer(t, `{"keys":[`+strings.Join(entries, ",")+`]}`)
	ks := newKeySet(srv.URL)

	// Every one of the three must verify, whichever the map happens to hold
	// last. Signing with each in turn is what makes the map order irrelevant
	// to the result rather than merely usually right.
	for i, key := range keys {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{})
		signed, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"})).
			Parse(signed, ks.keyfunc)
		if err != nil || !parsed.Valid {
			t.Fatalf("key %d: %v", i, err)
		}
	}
	if got := srv.fetches.Load(); got != 1 {
		t.Fatalf("fetches = %d, want 1 — the keyless set should be cached like any other", got)
	}
}
