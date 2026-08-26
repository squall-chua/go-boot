package security

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/web"
)

// JWTConfig points at one OAuth2 issuer. Three of the four keys are required,
// and the constructor says which one is missing.
type JWTConfig struct {
	Issuer   string `yaml:"issuer"`   // required; the exact iss claim
	Audience string `yaml:"audience"` // required; the exact aud this service answers to
	JWKSURL  string `yaml:"jwksUrl"`  // required; where the signing keys are published
	// Leeway is the clock skew allowed on exp and nbf. Default 30s.
	Leeway time.Duration `yaml:"leeway"`
}

// allowedAlgs is an allowlist, and it is not a config key: every entry is
// asymmetric, so "none" and the HMAC-verified-with-the-RSA-public-key trick
// are both outside it and neither is a thing a user can switch on from a YAML
// file.
//
// It is the SECOND lock, not the first, and that is worth writing down. The
// first is the key type: keyfunc hands back an *rsa.PublicKey or an
// *ecdsa.PublicKey, and golang-jwt refuses to verify an HS256 or a "none"
// token with either — measured, by deleting this line and watching every
// rejection still hold. What the list adds is that the set of algorithms this
// service accepts is the set docs/spec.md 4.7 names, rather than whatever
// golang-jwt happens to support next.
var allowedAlgs = []string{
	"RS256", "RS384", "RS512", // RSASSA-PKCS1-v1_5, what most issuers sign with
	"PS256", "PS384", "PS512", // RSASSA-PSS, the same keys under the newer scheme
	"ES256", "ES384", "ES512", // ECDSA
}

// Principal is what a verified token became. Claims holds the whole payload,
// so a claim go-boot does not name is still reachable — roles, for one, which
// have no standard claim name: Keycloak writes realm_access.roles and Azure
// writes roles, so go-boot names neither and hands over the map.
type Principal struct {
	Subject string
	Issuer  string
	Scopes  []string
	Claims  map[string]any
}

// principalKey is unexported, so nothing outside this package can put a
// Principal in a context without a verified token behind it. That is the
// whole reason there is no WithPrincipal.
type principalKey struct{}

// PrincipalFrom returns the Principal a verified token left, if there was
// one. A request that carried no token reports false rather than an empty
// Principal, so "anonymous" and "subject happens to be empty" cannot be
// confused.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok
}

// has reports whether the Principal carries the wanted scopes: all of them
// when all is true, at least one when it is false.
//
// An EMPTY want means "no scope required", and it means that for both
// quantifiers. Read literally, "at least one of nothing" would be false and
// RequireAnyScope() with no arguments would refuse every caller — a footgun
// with no use, since the only reason to write it is to mean "just be
// authenticated". Both no-argument forms say that instead.
func (p *Principal) has(want []string, all bool) bool {
	if len(want) == 0 {
		return true
	}
	if all {
		for _, w := range want {
			if !slices.Contains(p.Scopes, w) {
				return false
			}
		}
		return true
	}
	for _, w := range want {
		if slices.Contains(p.Scopes, w) {
			return true
		}
	}
	return false
}

// Authenticate verifies a bearer token when one is there. It does NOT reject
// a request that carried none — see the package doc for why the gate is not
// global.
//
// A token that is present and BAD is a 401 straight away, even on a route no
// RequireScope wraps. Carrying a broken token forward as "no Principal" would
// turn an expired token on an open route into a silent 200 and the same token
// on a guarded route into a 401 that says the wrong thing.
//
// Misconfiguration comes back from here, never from a 401 in production
// (docs/spec.md 4.0). Audience is required and that is not fussiness: a
// resource server that does not check aud accepts every token the issuer
// minted, including the one meant for a different client of the same issuer.
func Authenticate(cfg JWTConfig) (web.Middleware, error) {
	switch {
	case cfg.Issuer == "":
		return nil, errors.New("security.jwt.issuer: required, and must be the exact iss claim the issuer writes")
	case cfg.Audience == "":
		return nil, errors.New("security.jwt.audience: required, or this service accepts every token the issuer minted")
	case cfg.JWKSURL == "":
		return nil, errors.New("security.jwt.jwksUrl: required, and is the only key source")
	}
	if err := checkJWKSURL(cfg.JWKSURL); err != nil {
		return nil, err
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = 30 * time.Second
	}
	keys := newKeySet(cfg.JWKSURL)
	parser := jwt.NewParser(
		jwt.WithValidMethods(allowedAlgs),
		jwt.WithIssuer(cfg.Issuer),
		jwt.WithAudience(cfg.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(cfg.Leeway),
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearer(r)
			if !ok {
				next.ServeHTTP(w, r) // no token, no Principal, and that is not an error
				return
			}
			claims := jwt.MapClaims{}
			if _, err := parser.ParseWithClaims(raw, claims, keys.keyfunc); err != nil {
				// The REASON goes to the request logger, which carries the
				// same requestId as web.Logging's access line, and the
				// caller gets none of it. The token itself is never logged:
				// it is a bearer credential, and a log file is not where one
				// belongs.
				goboot.LoggerFrom(r.Context()).Warn("token rejected", "err", err)
				invalidToken(w)
				return
			}
			p := principalOf(claims)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
		})
	}, nil
}

// bearer pulls the token out of the Authorization header. The scheme is
// matched case-insensitively, because RFC 7235 says it is case-insensitive
// and real clients send "bearer".
func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, token, found := strings.Cut(h, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// principalOf reads the claims go-boot names and keeps the rest.
func principalOf(c jwt.MapClaims) *Principal {
	sub, _ := c["sub"].(string)
	iss, _ := c["iss"].(string)
	return &Principal{Subject: sub, Issuer: iss, Scopes: scopesOf(c), Claims: c}
}

// scopesOf reads the two claim names in use and the two shapes each arrives
// in. RFC 8693 says "scope" is one space-separated string; Azure AD writes
// "scp", sometimes as an array. Reading both is four lines, and a service
// that guessed wrong would fail every authorization check with no clue why.
func scopesOf(c jwt.MapClaims) []string {
	for _, name := range []string{"scope", "scp"} {
		switch v := c[name].(type) {
		case string:
			if f := strings.Fields(v); len(f) > 0 {
				return f
			}
		case []any:
			var out []string
			for _, s := range v {
				if s, ok := s.(string); ok {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

// RequireScope guards one route: the Principal must carry EVERY scope named.
// It is an ordinary web.Middleware, so it wraps the handler where it is
// mounted and nothing else about Server.Handle changes:
//
//	srv.Handle("POST /orders", security.RequireScope("orders:write")(orders))
func RequireScope(scope ...string) web.Middleware { return require(scope, true) }

// RequireAnyScope guards one route on AT LEAST ONE of the scopes named.
func RequireAnyScope(scope ...string) web.Middleware { return require(scope, false) }

// require is both of the above. The two statuses are the point: no token at
// all is 401, because authenticating would help; a token that verified but
// lacks the scope is 403, because it would not.
func require(want []string, all bool) web.Middleware {
	// RFC 6750 names the scopes on the 403. They are not a secret — the
	// caller could read them in the API docs — and without them the caller
	// cannot tell which of their tokens to ask for.
	insufficient := fmt.Sprintf(`Bearer error="insufficient_scope", scope=%q`, strings.Join(want, " "))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				w.Header().Set("WWW-Authenticate", "Bearer")
				web.WriteProblem(w, http.StatusUnauthorized, "authentication required")
				return
			}
			if !p.has(want, all) {
				w.Header().Set("WWW-Authenticate", insufficient)
				web.WriteProblem(w, http.StatusForbidden, "insufficient scope")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// invalidToken is the one 401 Authenticate writes. The detail says nothing
// about WHY, on purpose: expired, wrong audience and unknown key are three
// different facts about this service's configuration, and the caller holding
// a bad token is the last party who should be told which one it was.
func invalidToken(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	web.WriteProblem(w, http.StatusUnauthorized, "invalid token")
}

// checkJWKSURL refuses a key set this service could not trust.
//
// Plain http is the whole point of the check: the key set IS the root of
// trust, so anyone who can rewrite that response chooses who this service
// believes — an on-path attacker publishes their own key and mints any
// identity they like. It is a total authentication bypass, and it looks like
// nothing at all in a config file.
//
// Loopback is the one exception, because a JWKS on 127.0.0.1 has no path an
// attacker sits on, and without it no test and no local issuer could ever be
// pointed at. This is the same carve-out RFC 8252 makes for native-app
// redirect URIs, and for the same reason.
func checkJWKSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("security.jwt.jwksUrl: %q is not a URL: %w", raw, err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("security.jwt.jwksUrl: %q is plain http, and the key set is what this service trusts; use https, or a loopback host for local work", raw)
	}
	return fmt.Errorf("security.jwt.jwksUrl: %q must be an http or https URL", raw)
}

// isLoopback covers the literal addresses and the name that resolves to them.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
