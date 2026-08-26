package security_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/security"
	"github.com/squall-chua/go-boot/web"
)

// quick keeps the drain delay out of the way, the same reason goboot/web's
// tests do it: these tests are about tokens, not about shutdown.
var quick = goboot.LifecycleConfig{DrainDelay: time.Nanosecond}

const audience = "orders-api"

// issuer is a real OAuth2 issuer, small enough to fit here: one signing key,
// one JWKS endpoint over a real listener, and a signer. Nothing in these
// tests is a double for anything go-boot owns, and nothing is a double for
// the token format either — every token below is signed and verified for
// real.
type issuer struct {
	t       *testing.T
	srv     *httptest.Server
	fetches atomic.Int64

	rsaKey *rsa.PrivateKey
	ecKey  *ecdsa.PrivateKey
	kid    string
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ec key: %v", err)
	}
	i := &issuer{t: t, rsaKey: rk, ecKey: ek, kid: "k1"}
	i.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i.fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, i.jwks())
	}))
	t.Cleanup(i.srv.Close)
	return i
}

func (i *issuer) url() string     { return i.srv.URL }
func (i *issuer) jwksURL() string { return i.srv.URL + "/jwks" }

// rotate replaces the signing key and its kid, which is what an issuer does
// and what the key set below has to survive.
func (i *issuer) rotate() {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		i.t.Fatalf("rsa key: %v", err)
	}
	i.rsaKey, i.kid = k, "k2"
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (i *issuer) jwks() string {
	pub := i.rsaKey.PublicKey
	ec := i.ecKey.PublicKey
	return fmt.Sprintf(`{"keys":[
	  {"kty":"RSA","use":"sig","alg":"RS256","kid":%q,"n":%q,"e":%q},
	  {"kty":"EC","use":"sig","alg":"ES256","kid":"ec1","crv":"P-256","x":%q,"y":%q}
	]}`,
		i.kid, b64(pub.N.Bytes()), b64(big.NewInt(int64(pub.E)).Bytes()),
		b64(ec.X.FillBytes(make([]byte, 32))), b64(ec.Y.FillBytes(make([]byte, 32))))
}

// claims is a token that would be accepted, so each test below changes one
// thing and says what that one thing costs.
func (i *issuer) claims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":   i.url(),
		"aud":   audience,
		"sub":   "user-7",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
		"scope": "orders:read orders:write",
	}
}

func (i *issuer) sign(c jwt.MapClaims) string {
	i.t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	tok.Header["kid"] = i.kid
	s, err := tok.SignedString(i.rsaKey)
	if err != nil {
		i.t.Fatalf("sign: %v", err)
	}
	return s
}

// signES signs with the EC key, to show the second algorithm family is real.
func (i *issuer) signES(c jwt.MapClaims) string {
	i.t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	tok.Header["kid"] = "ec1"
	s, err := tok.SignedString(i.ecKey)
	if err != nil {
		i.t.Fatalf("sign: %v", err)
	}
	return s
}

func (i *issuer) jwtConfig() security.JWTConfig {
	return security.JWTConfig{Issuer: i.url(), Audience: []string{audience}, JWKSURL: i.jwksURL()}
}

// service is a real App with a real web.Server on a real port, wired the way
// docs/spec.md 4.7 documents. It mounts one guarded route, one open route,
// and reports the address.
type service struct {
	addr string
}

func serve(t *testing.T, cfg security.Config, mount func(*web.Server)) *service {
	t.Helper()
	app, err := goboot.New(goboot.Config{Log: goboot.LogConfig{Level: "ERROR"}, Lifecycle: quick})
	if err != nil {
		t.Fatalf("goboot.New: %v", err)
	}
	srv, err := web.New(web.Config{Addr: "127.0.0.1:0"}, app.Log)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	sec, err := security.DefaultMiddleware(cfg)
	if err != nil {
		t.Fatalf("security.DefaultMiddleware: %v", err)
	}
	srv.Use(append(web.DefaultMiddleware(app.Log), sec...)...)
	mount(srv)
	app.Add(srv)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(t.Context()) })
	return &service{addr: srv.Addr()}
}

// whoami answers with the Principal it found, so a test can assert what the
// middleware actually put in the context rather than only its status code.
func whoami(w http.ResponseWriter, r *http.Request) {
	p, ok := security.PrincipalFrom(r.Context())
	if !ok {
		web.WriteJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{
		"authenticated": true, "sub": p.Subject, "iss": p.Issuer, "scopes": p.Scopes,
	})
}

// get makes one request, optionally bearing a token, and returns the
// response. The body is read and closed here so no test has to remember.
func get(t *testing.T, s *service, path, token string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+s.addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

// mountBoth is the wiring docs/spec.md 4.7 documents: a guarded route wrapped
// where it is mounted, and an open one that nothing wraps.
func mountBoth(srv *web.Server) {
	srv.Handle("GET /orders", security.RequireScope("orders:write")(http.HandlerFunc(whoami)))
	srv.HandleFunc("GET /open", whoami)
}

func jwtOnly(i *issuer) security.Config {
	return security.Config{JWT: i.jwtConfig()}
}

// TestAValidTokenReachesAGuardedRoute is the tracer bullet: a real issuer, a
// real signed token, a real listener, and the Principal a handler can see.
func TestAValidTokenReachesAGuardedRoute(t *testing.T) {
	i := newIssuer(t)
	s := serve(t, jwtOnly(i), mountBoth)

	resp, body := get(t, s, "/orders", i.sign(i.claims()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d %s, want 200", resp.StatusCode, body)
	}
	var got struct {
		Authenticated bool     `json:"authenticated"`
		Sub           string   `json:"sub"`
		Iss           string   `json:"iss"`
		Scopes        []string `json:"scopes"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	if !got.Authenticated || got.Sub != "user-7" || got.Iss != i.url() {
		t.Fatalf("principal = %+v", got)
	}
	if len(got.Scopes) != 2 {
		t.Fatalf("scopes = %v, want the two from the scope claim", got.Scopes)
	}
}

// TestAnES256TokenIsAccepted pins the second algorithm family, so the
// allowlist in 4.7 is not RS-only by accident.
func TestAnES256TokenIsAccepted(t *testing.T) {
	i := newIssuer(t)
	s := serve(t, jwtOnly(i), mountBoth)
	if resp, body := get(t, s, "/orders", i.signES(i.claims())); resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d %s, want 200", resp.StatusCode, body)
	}
}

// TestAnUnguardedRouteNeedsNoToken is the other half of "authentication is
// not a global gate": no token, no Principal, and the handler still runs.
func TestAnUnguardedRouteNeedsNoToken(t *testing.T) {
	i := newIssuer(t)
	s := serve(t, jwtOnly(i), mountBoth)
	resp, body := get(t, s, "/open", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"authenticated":false`) {
		t.Fatalf("got %d %s, want 200 with no principal", resp.StatusCode, body)
	}
}

// TestProbePathsAnswerWithSecurityWired is why the gate is not global: the
// Actuator's paths share this listener, so a middleware that demanded a token
// on every request would lock Kubernetes out of its own probes.
func TestProbePathsAnswerWithSecurityWired(t *testing.T) {
	i := newIssuer(t)
	s := serve(t, jwtOnly(i), func(srv *web.Server) {
		mountBoth(srv)
		srv.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "ok")
		})
	})
	if resp, body := get(t, s, "/livez", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d %s, want 200", resp.StatusCode, body)
	}
}

// TestNoTokenOnAGuardedRouteIs401 is RequireScope doing the rejecting, since
// Authenticate does not.
func TestNoTokenOnAGuardedRouteIs401(t *testing.T) {
	i := newIssuer(t)
	s := serve(t, jwtOnly(i), mountBoth)
	resp, body := get(t, s, "/orders", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d %s, want 401", resp.StatusCode, body)
	}
	if !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("WWW-Authenticate = %q", resp.Header.Get("WWW-Authenticate"))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want the RFC 7807 one", ct)
	}
}

// TestAMissingScopeIs403 separates "who are you" from "may you". A token that
// verified but does not carry the scope is a 403, not a 401: re-authenticating
// would not help.
func TestAMissingScopeIs403(t *testing.T) {
	i := newIssuer(t)
	s := serve(t, jwtOnly(i), mountBoth)
	c := i.claims()
	c["scope"] = "orders:read"
	if resp, body := get(t, s, "/orders", i.sign(c)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d %s, want 403", resp.StatusCode, body)
	}
}

// TestRequireAnyScope is the other authorization shape: one of the list is
// enough.
func TestRequireAnyScope(t *testing.T) {
	i := newIssuer(t)
	s := serve(t, jwtOnly(i), func(srv *web.Server) {
		srv.Handle("GET /any", security.RequireAnyScope("admin", "orders:read")(http.HandlerFunc(whoami)))
		srv.Handle("GET /all", security.RequireScope("admin", "orders:read")(http.HandlerFunc(whoami)))
	})
	tok := i.sign(i.claims())
	if resp, body := get(t, s, "/any", tok); resp.StatusCode != http.StatusOK {
		t.Fatalf("any: got %d %s, want 200", resp.StatusCode, body)
	}
	if resp, body := get(t, s, "/all", tok); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("all: got %d %s, want 403 — the token has no admin scope", resp.StatusCode, body)
	}
}

// TestTheScpClaimIsReadToo covers the issuers that spell the claim the other
// way, as an array rather than a space-separated string.
func TestTheScpClaimIsReadToo(t *testing.T) {
	i := newIssuer(t)
	s := serve(t, jwtOnly(i), mountBoth)
	c := i.claims()
	delete(c, "scope")
	c["scp"] = []string{"orders:read", "orders:write"}
	if resp, body := get(t, s, "/orders", i.sign(c)); resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d %s, want 200", resp.StatusCode, body)
	}
}

// badTokens are the rejections that matter, each changing exactly one thing
// about a token that would otherwise be accepted.
func TestABadTokenIs401(t *testing.T) {
	i := newIssuer(t)
	other := newIssuer(t)

	cases := []struct {
		name  string
		token func() string
	}{
		{"expired", func() string {
			c := i.claims()
			c["exp"] = time.Now().Add(-time.Hour).Unix()
			return i.sign(c)
		}},
		{"no exp at all", func() string {
			c := i.claims()
			delete(c, "exp")
			return i.sign(c)
		}},
		{"not yet valid", func() string {
			c := i.claims()
			c["nbf"] = time.Now().Add(time.Hour).Unix()
			return i.sign(c)
		}},
		{"another audience", func() string {
			c := i.claims()
			c["aud"] = "billing-api"
			return i.sign(c)
		}},
		{"another issuer", func() string {
			// This service's OWN key and kid, so the only thing wrong is
			// the iss claim. Signing with the other issuer's key would fail
			// on the signature and never reach the issuer check at all —
			// which is how this case first passed for the wrong reason.
			c := i.claims()
			c["iss"] = other.url()
			return i.sign(c)
		}},
		{"alg none", func() string {
			c := i.claims()
			tok := jwt.NewWithClaims(jwt.SigningMethodNone, c)
			tok.Header["kid"] = i.kid
			s, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
			if err != nil {
				t.Fatalf("sign none: %v", err)
			}
			return s
		}},
		{"signed by the wrong key", func() string {
			return other.sign(func() jwt.MapClaims { c := other.claims(); c["iss"] = i.url(); return c }())
		}},
		{"not a token at all", func() string { return "not.a.token" }},
	}

	s := serve(t, jwtOnly(i), mountBoth)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The OPEN route, on purpose: a token that is present and bad is
			// a 401 even where no scope was required, because carrying it
			// forward as "no Principal" would answer the wrong question.
			resp, body := get(t, s, "/open", tc.token())
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("got %d %s, want 401", resp.StatusCode, body)
			}
			if a := resp.Header.Get("WWW-Authenticate"); !strings.Contains(a, `error="invalid_token"`) {
				t.Fatalf("WWW-Authenticate = %q", a)
			}
			if strings.Contains(body, "orders-api") || strings.Contains(body, i.url()) {
				t.Fatalf("the 401 body says why it failed: %q", body)
			}
		})
	}
}

// TestTheTokenNeverReachesTheLog is a claim 4.7 makes, so it is a test. The
// request logger is the one goboot.LoggerFrom returns, so the check is to put
// a buffer one in the context and read what came out.
func TestTheTokenNeverReachesTheLog(t *testing.T) {
	i := newIssuer(t)
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	authn, err := security.Authenticate(i.jwtConfig())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	c := i.claims()
	c["exp"] = time.Now().Add(-time.Hour).Unix()
	token := i.sign(c)

	h := authn(http.HandlerFunc(whoami))
	req := httptest.NewRequest(http.MethodGet, "/open", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req = req.WithContext(goboot.WithLogger(req.Context(), log))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	if buf.Len() == 0 {
		t.Fatal("nothing was logged; an operator cannot tell why the 401 happened")
	}
	if strings.Contains(buf.String(), token) {
		t.Fatalf("the bearer token is in the log: %s", buf.String())
	}
	// The signature is the half that is secret even in pieces.
	if sig := token[strings.LastIndex(token, ".")+1:]; strings.Contains(buf.String(), sig) {
		t.Fatalf("the token signature is in the log: %s", buf.String())
	}
}

// TestHeadersAreOnEveryResponse covers the four in 4.7's table, and that HSTS
// is off until it is configured.
func TestHeadersAreOnEveryResponse(t *testing.T) {
	i := newIssuer(t)
	s := serve(t, jwtOnly(i), mountBoth)
	resp, _ := get(t, s, "/open", "")
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
		"Referrer-Policy":         "no-referrer",
	}
	for k, v := range want {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if got := resp.Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS = %q, want it off until hstsMaxAge is set", got)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "" {
		t.Errorf("X-Frame-Options = %q; 4.7 leaves it out", got)
	}
}

// TestHSTSIsOnWhenConfigured is the other half of the default above.
func TestHSTSIsOnWhenConfigured(t *testing.T) {
	i := newIssuer(t)
	cfg := jwtOnly(i)
	cfg.Headers.HSTSMaxAge = 180 * 24 * time.Hour
	s := serve(t, cfg, mountBoth)
	resp, _ := get(t, s, "/open", "")
	if got := resp.Header.Get("Strict-Transport-Security"); got != "max-age=15552000" {
		t.Fatalf("HSTS = %q, want max-age=15552000", got)
	}
}

// TestCORS covers the preflight, the exact-match rule, and Vary.
func TestCORS(t *testing.T) {
	i := newIssuer(t)
	cfg := jwtOnly(i)
	cfg.CORS.AllowedOrigins = []string{"https://app.example.com"}
	s := serve(t, cfg, mountBoth)

	t.Run("preflight is answered", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodOptions, "http://"+s.addr+"/orders", nil)
		req.Header.Set("Origin", "https://app.example.com")
		req.Header.Set("Access-Control-Request-Method", "GET")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("got %d, want 204", resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
			t.Fatalf("Allow-Origin = %q", got)
		}
		if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") {
			t.Fatalf("Allow-Headers = %q, want Authorization in it", got)
		}
	})

	t.Run("another origin gets no allow header", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://"+s.addr+"/open", nil)
		req.Header.Set("Origin", "https://app.example.com.evil.test")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("Allow-Origin = %q for an origin that only looks like the allowed one", got)
		}
		if got := resp.Header.Get("Vary"); !strings.Contains(got, "Origin") {
			t.Fatalf("Vary = %q; a shared cache would serve one origin's answer to another", got)
		}
	})
}

// TestPreflightIsNotAuthenticated: a browser never sends Authorization on a
// preflight, so a preflight that reached the guarded route would be answered
// 401 and the real request would never be sent.
//
// What holds it is that CORS answers the preflight ABOVE the mux, so
// RequireScope — which is a per-route wrapper, and so sits below the mux —
// never sees it. Measured: making the preflight fall through to the mux
// instead fails this test. The order of CORS against Authenticate does NOT
// matter here, and was checked rather than assumed: Authenticate passes a
// request with no token straight through, so the preflight survives it either
// way.
func TestPreflightIsNotAuthenticated(t *testing.T) {
	i := newIssuer(t)
	cfg := jwtOnly(i)
	cfg.CORS.AllowedOrigins = []string{"https://app.example.com"}
	s := serve(t, cfg, mountBoth)

	req, _ := http.NewRequest(http.MethodOptions, "http://"+s.addr+"/orders", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("got %d, want 204 — a preflight carries no token and must not be rejected", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Fatal("no allow-origin on the preflight")
	}
}

// The aud claim is legally a string OR an array of strings, and an issuer
// with a misconfigured mapper emits an empty array. All three shapes are
// pinned, because docs/spec.md 4.7 makes audience a REQUIRED key and the
// whole value of that rule is what it rejects.
func TestTheAudienceClaimInEveryShape(t *testing.T) {
	i := newIssuer(t)
	s := serve(t, jwtOnly(i), mountBoth)

	t.Run("an array containing the audience is accepted", func(t *testing.T) {
		c := i.claims()
		c["aud"] = []string{"billing-api", audience, "other"}
		if resp, body := get(t, s, "/orders", i.sign(c)); resp.StatusCode != http.StatusOK {
			t.Fatalf("got %d %s, want 200", resp.StatusCode, body)
		}
	})
	t.Run("no aud claim at all is refused", func(t *testing.T) {
		c := i.claims()
		delete(c, "aud")
		if resp, _ := get(t, s, "/open", i.sign(c)); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", resp.StatusCode)
		}
	})
	t.Run("an empty aud array is refused", func(t *testing.T) {
		c := i.claims()
		c["aud"] = []string{}
		if resp, _ := get(t, s, "/open", i.sign(c)); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", resp.StatusCode)
		}
	})
}

// TestWhatAnIssuerOutageCosts is the price of making JWKS the only key
// source, measured rather than argued. Lazy fetch means the key set is not a
// startup dependency — but it IS a first-token dependency, and that is the
// half worth writing a test for so nobody has to rediscover it.
func TestWhatAnIssuerOutageCosts(t *testing.T) {
	t.Run("a cold start with the issuer down rejects every token", func(t *testing.T) {
		i := newIssuer(t)
		tok := i.sign(i.claims())
		s := serve(t, jwtOnly(i), mountBoth)
		i.srv.Close() // down before the first token ever arrives
		if resp, _ := get(t, s, "/orders", tok); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", resp.StatusCode)
		}
	})
	t.Run("a warm cache survives the issuer going down", func(t *testing.T) {
		i := newIssuer(t)
		s := serve(t, jwtOnly(i), mountBoth)
		tok := i.sign(i.claims())
		if resp, _ := get(t, s, "/orders", tok); resp.StatusCode != http.StatusOK {
			t.Fatalf("warm-up failed: %d", resp.StatusCode)
		}
		i.srv.Close()
		// A kid already in the cache is answered from it before any refetch
		// is considered, so a steady-state outage costs nothing.
		if resp, _ := get(t, s, "/orders", tok); resp.StatusCode != http.StatusOK {
			t.Fatalf("a cached key stopped working when the issuer went down: %d", resp.StatusCode)
		}
	})
	t.Run("an open route is unaffected either way", func(t *testing.T) {
		i := newIssuer(t)
		s := serve(t, jwtOnly(i), mountBoth)
		i.srv.Close()
		if resp, _ := get(t, s, "/open", ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("got %d, want 200", resp.StatusCode)
		}
	})
}

// TestTheOtherKeySources covers jwksFile and publicKeyFile end to end. They
// exist for a service that cannot make an outbound request, or has no issuer
// to make one to, so the thing to prove is that a token verifies with NO
// network in the picture at all.
func TestTheOtherKeySources(t *testing.T) {
	i := newIssuer(t)
	dir := t.TempDir()

	jwksPath := filepath.Join(dir, "jwks.json")
	if err := os.WriteFile(jwksPath, []byte(i.jwks()), 0o600); err != nil {
		t.Fatal(err)
	}
	pemPath := filepath.Join(dir, "key.pem")
	der, err := x509.MarshalPKIXPublicKey(&i.rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pemPath, pem.EncodeToMemory(
		&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	base := func() security.JWTConfig {
		return security.JWTConfig{Issuer: i.url(), Audience: []string{audience}}
	}
	for _, tc := range []struct {
		name string
		cfg  security.JWTConfig
	}{
		{"jwksFile", func() security.JWTConfig { c := base(); c.JWKSFile = jwksPath; return c }()},
		{"publicKeyFile", func() security.JWTConfig { c := base(); c.PublicKeyFile = pemPath; return c }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := serve(t, security.Config{JWT: tc.cfg}, mountBoth)
			// The issuer's HTTP endpoint is closed, so anything that
			// reached for it would fail rather than quietly succeed.
			i.srv.Close()
			if resp, body := get(t, s, "/orders", i.sign(i.claims())); resp.StatusCode != http.StatusOK {
				t.Fatalf("got %d %s, want 200 with no network", resp.StatusCode, body)
			}
			c := i.claims()
			c["aud"] = "billing-api"
			if resp, _ := get(t, s, "/open", i.sign(c)); resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("a token for another audience was accepted: %d", resp.StatusCode)
			}
		})
	}
}

// TestAnyOfTheAudiencesIsEnough is why the key is a list: a service being
// renamed answers to both names at once.
func TestAnyOfTheAudiencesIsEnough(t *testing.T) {
	i := newIssuer(t)
	cfg := i.jwtConfig()
	cfg.Audience = []string{"orders-api", "orders.example.com"}
	s := serve(t, security.Config{JWT: cfg}, mountBoth)

	for _, aud := range []string{"orders-api", "orders.example.com"} {
		c := i.claims()
		c["aud"] = aud
		if resp, body := get(t, s, "/orders", i.sign(c)); resp.StatusCode != http.StatusOK {
			t.Errorf("aud %q: got %d %s, want 200", aud, resp.StatusCode, body)
		}
	}
	c := i.claims()
	c["aud"] = "billing-api"
	if resp, _ := get(t, s, "/open", i.sign(c)); resp.StatusCode != http.StatusUnauthorized {
		t.Error("a third audience was accepted")
	}
}

// TestTheConstructorRejectsABadConfig is 4.0's rule: misconfiguration comes
// back from the constructor, never from a 401 in production.
func TestTheConstructorRejectsABadConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  security.Config
		want string
	}{
		{"cors * with credentials",
			security.Config{CORS: security.CORSConfig{AllowedOrigins: []string{"*"}, AllowCredentials: true}},
			"security.cors"},
		{"jwt with no audience",
			security.Config{JWT: security.JWTConfig{Issuer: "https://i.test", JWKSURL: "https://i.test/jwks"}},
			"security.jwt.audience"},
		{"jwt with no issuer",
			security.Config{JWT: security.JWTConfig{Audience: []string{audience}, JWKSURL: "https://i.test/jwks"}},
			"security.jwt.issuer"},
		{"jwt with no jwksUrl",
			security.Config{JWT: security.JWTConfig{Issuer: "https://i.test", Audience: []string{audience}}},
			"security.jwt"},
		{"jwt naming two key sources",
			security.Config{JWT: security.JWTConfig{
				Issuer: "https://i.test", Audience: []string{audience},
				JWKSURL: "https://i.test/jwks", JWKSFile: "/tmp/jwks.json"}},
			"security.jwt"},
		{"jwt with an empty audience entry",
			security.Config{JWT: security.JWTConfig{
				Issuer: "https://i.test", Audience: []string{audience, "  "},
				JWKSURL: "https://i.test/jwks"}},
			"security.jwt.audience"},
		{"a publicKeyFile that is not there",
			security.Config{JWT: security.JWTConfig{
				Issuer: "https://i.test", Audience: []string{audience},
				PublicKeyFile: "/nonexistent/key.pem"}},
			"security.jwt.publicKeyFile"},
		{"a jwksFile that is not there",
			security.Config{JWT: security.JWTConfig{
				Issuer: "https://i.test", Audience: []string{audience},
				JWKSFile: "/nonexistent/jwks.json"}},
			"security.jwt.jwksFile"},
		// The key set IS the root of trust, so plain http over a network an
		// attacker can sit on is a total authentication bypass. Loopback is
		// the exception, and every other test in this file relies on it.
		{"a jwksUrl on plain http",
			security.Config{JWT: security.JWTConfig{
				Issuer: "https://i.test", Audience: []string{audience}, JWKSURL: "http://i.test/jwks"}},
			"security.jwt.jwksUrl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := security.DefaultMiddleware(tc.cfg)
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("error %q does not open with the config key %q", err, tc.want)
			}
		})
	}
}

// TestCORSAloneRejectsTheSameConfigs: every rule about a cors section lives
// in CORS, so a caller wiring it by hand rather than through DefaultMiddleware
// gets the same answers rather than a silent pass-through.
func TestCORSAloneRejectsTheSameConfigs(t *testing.T) {
	bad := []security.CORSConfig{
		{AllowedOrigins: []string{"*"}, AllowCredentials: true},
		{AllowCredentials: true}, // credentials with nothing to allow them for
	}
	for _, cfg := range bad {
		if _, err := security.CORS(cfg); err == nil {
			t.Errorf("CORS(%+v) returned no error", cfg)
		} else if !strings.HasPrefix(err.Error(), "security.cors") {
			t.Errorf("error %q does not open with the config key", err)
		}
	}
	// Nothing configured is not a fault: it is a service that wants headers
	// only, and it gets a middleware that does nothing.
	mw, err := security.CORS(security.CORSConfig{})
	if err != nil || mw == nil {
		t.Fatalf("CORS(zero) = %v, %v", mw, err)
	}
}

// TestNoScopesMeansAuthenticatedOnly pins both no-argument forms to the same
// meaning. "At least one of nothing" read literally would refuse everybody.
func TestNoScopesMeansAuthenticatedOnly(t *testing.T) {
	i := newIssuer(t)
	s := serve(t, jwtOnly(i), func(srv *web.Server) {
		srv.Handle("GET /all", security.RequireScope()(http.HandlerFunc(whoami)))
		srv.Handle("GET /any", security.RequireAnyScope()(http.HandlerFunc(whoami)))
	})
	tok := i.sign(i.claims())
	for _, path := range []string{"/all", "/any"} {
		if resp, body := get(t, s, path, tok); resp.StatusCode != http.StatusOK {
			t.Errorf("%s with a token: got %d %s, want 200", path, resp.StatusCode, body)
		}
		if resp, _ := get(t, s, path, ""); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with no token: got %d, want 401", path, resp.StatusCode)
		}
	}
}

// TestAnEmptyJWTSectionSkipsAuthentication is the half-finished-config rule
// the other way round: nothing configured is not a fault, it is a service
// that wants headers and CORS only.
func TestAnEmptyJWTSectionSkipsAuthentication(t *testing.T) {
	mw, err := security.DefaultMiddleware(security.Config{})
	if err != nil {
		t.Fatalf("DefaultMiddleware: %v", err)
	}
	if len(mw) != 1 {
		t.Fatalf("got %d middlewares, want only Headers", len(mw))
	}
}
