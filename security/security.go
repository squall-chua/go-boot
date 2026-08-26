// Package security is the Security Starter: security headers, CORS, a JWT
// bearer middleware over a JWKS key set, and per-route scope checks. It owns
// the ground goboot/web deliberately left empty, and it is where the two
// things docs/spec.md 10 refused for v1 — CORS and security headers — come
// back, as something a user opts into rather than a default every user links.
//
// # It is one package, not two
//
// The ticket that opened it expected a heavy subpackage. Measured first:
// github.com/golang-jwt/jwt/v5 links ONE module and adds 36 KB to a bare
// net/http binary, with no transitive dependencies at all. goboot/web/metrics
// was split out at 9 modules and goboot/grpc/metrics at 10, so one module is
// below every line this repo has drawn and there is nothing here to hide in a
// subpackage. See docs/spec.md 4.7.
//
// What that choice costs is jwks.go: golang-jwt ships no JWKS client, so the
// key set is written here over crypto/rsa and crypto/ecdsa.
//
// # Authentication is not a global gate
//
// Authenticate verifies a bearer token WHEN ONE IS THERE and puts the
// Principal in the request context. It does not reject a request that carried
// none. Rejecting is RequireScope's job, at the mount:
//
//	sec, err := security.DefaultMiddleware(cfg.Security)
//	srv.Use(append(web.DefaultMiddleware(app.Log), sec...)...)
//	srv.Handle("POST /orders", security.RequireScope("orders:write")(orders))
//
// A middleware that demanded a token on every request cannot work on this
// server: /livez, /readyz and /actuator/* share the listener (ADR 0003, ADR
// 0006), so a global gate either locks Kubernetes out of its own probes or
// grows a path allowlist — a security decision written in a config file that
// no compiler checks.
//
// The trap that leaves is real: a route nobody wrapped is a route with no
// authorization, and neither Go nor go-boot can catch it. What go-boot can do
// is keep the wrapper short enough that its absence is visible in review.
package security

import (
	"net/http"
	"strconv"
	"time"

	"github.com/squall-chua/go-boot/web"
)

// Config is the security section. Every field has a working default, and a
// section left out entirely turns that piece off rather than failing.
type Config struct {
	Headers HeadersConfig `yaml:"headers"`
	CORS    CORSConfig    `yaml:"cors"`
	JWT     JWTConfig     `yaml:"jwt"`
}

// HeadersConfig is one key, because three of the four headers below have no
// setting worth offering.
type HeadersConfig struct {
	// HSTSMaxAge is 0, which is OFF, and that default is deliberate: sent
	// over plain HTTP on a developer's machine, HSTS pins localhost to HTTPS
	// in that browser for the whole max-age, and undoing it means a trip
	// into browser internals.
	HSTSMaxAge time.Duration `yaml:"hstsMaxAge"`
}

// DefaultMiddleware is Headers, CORS and Authenticate, outermost first. It is
// a slice you can edit, not hidden behaviour, the same shape
// web.DefaultMiddleware has:
//
//	srv.Use(append(web.DefaultMiddleware(app.Log), sec...)...)
//
// Headers is always there. CORS joins it once cors.allowedOrigins names
// something, and Authenticate once the jwt section is filled in. A jwt
// section that is HALF filled in is an error rather than a skip — the same
// rule web.New applies to half a TLS pair, and for the same reason: a
// misspelt key must never leave a service quietly unauthenticated.
func DefaultMiddleware(cfg Config) ([]web.Middleware, error) {
	mw := []web.Middleware{Headers(cfg.Headers)}
	// CORS is asked for its middleware whatever the config says, so that
	// every rule about a cors section lives in CORS and a caller wiring it
	// by hand gets the same answers. It returns a pass-through when nothing
	// is configured, and that is the only case skipped here.
	c, err := CORS(cfg.CORS)
	if err != nil {
		return nil, err
	}
	if len(cfg.CORS.AllowedOrigins) > 0 {
		mw = append(mw, c)
	}
	if !cfg.JWT.isZero() {
		a, err := Authenticate(cfg.JWT)
		if err != nil {
			return nil, err
		}
		mw = append(mw, a)
	}
	return mw, nil
}

// Headers sets the four in docs/spec.md 4.7's table. It cannot fail, so it
// returns no error: HSTSMaxAge is the only key and any duration is valid.
//
// X-Frame-Options is deliberately absent. frame-ancestors 'none' is what
// every current browser reads, and docs/spec.md 4.3 already said the header
// does nothing on a JSON API.
//
// They are set on the way IN, before the handler runs, so a handler that
// writes its status straight away still carries them.
func Headers(cfg HeadersConfig) web.Middleware {
	var hsts string
	if cfg.HSTSMaxAge > 0 {
		hsts = "max-age=" + seconds(cfg.HSTSMaxAge)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			// The one that matters on a JSON API: it stops a browser
			// deciding a response is HTML.
			h.Set("X-Content-Type-Options", "nosniff")
			// Nothing an API response holds should ever load or be framed.
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			// An API URL routinely carries an id.
			h.Set("Referrer-Policy", "no-referrer")
			if hsts != "" {
				h.Set("Strict-Transport-Security", hsts)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// seconds is a duration as the whole-second string an HTTP header wants.
// Both Strict-Transport-Security and Access-Control-Max-Age take one.
func seconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10)
}
