package security

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/squall-chua/go-boot/web"
)

// CORSConfig is off until AllowedOrigins names something.
type CORSConfig struct {
	AllowedOrigins   []string      `yaml:"allowedOrigins"`   // exact origins, or the single entry "*"
	AllowedMethods   []string      `yaml:"allowedMethods"`   // GET, POST, PUT, PATCH, DELETE
	AllowedHeaders   []string      `yaml:"allowedHeaders"`   // Authorization, Content-Type
	AllowCredentials bool          `yaml:"allowCredentials"` // false
	MaxAge           time.Duration `yaml:"maxAge"`           // 10m
}

// The defaults. Methods are the five a JSON API uses; OPTIONS is not in the
// list because the preflight never reaches a handler. Headers are the two a
// browser must be told about explicitly — the rest of what a fetch() sends is
// on the CORS-safelisted list already.
var (
	defaultCORSMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
	defaultCORSHeaders = []string{"Authorization", "Content-Type"}
)

const defaultCORSMaxAge = 10 * time.Minute

// CORS answers preflights and adds the response headers a browser needs.
//
// It refuses the dangerous mistake in the CONSTRUCTOR, which is the
// measurement docs/spec.md 10 asked for: "*" together with allowCredentials
// is a startup error. A browser refuses that pair anyway, so the config that
// reads as "let everyone log in" in fact lets nobody, and finding that out at
// boot beats finding it out from a support ticket.
//
// Origins are matched EXACTLY. There is no pattern syntax, because a wildcard
// in the middle of an origin is how evil-example.com gets matched by a rule
// meant for app.example.com.
//
// An empty AllowedOrigins gives a middleware that does nothing, so a service
// that wants headers only pays a function call.
func CORS(cfg CORSConfig) (web.Middleware, error) {
	wildcard := slices.Contains(cfg.AllowedOrigins, "*")
	if wildcard && cfg.AllowCredentials {
		return nil, errors.New(`security.cors: allowedOrigins "*" cannot be used with allowCredentials, and a browser would refuse the pair`)
	}
	if len(cfg.AllowedOrigins) == 0 {
		if cfg.AllowCredentials {
			// Credentials with nothing to allow them for is the same
			// half-finished config, and returning the pass-through would
			// read to its author as CORS working.
			return nil, errors.New("security.cors: allowCredentials is on but allowedOrigins is empty")
		}
		return func(next http.Handler) http.Handler { return next }, nil
	}
	methods := strings.Join(cmpOr(cfg.AllowedMethods, defaultCORSMethods), ", ")
	headers := strings.Join(cmpOr(cfg.AllowedHeaders, defaultCORSHeaders), ", ")
	maxAge := cfg.MaxAge
	if maxAge == 0 {
		maxAge = defaultCORSMaxAge
	}
	age := seconds(maxAge)
	origins := slices.Clone(cfg.AllowedOrigins)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			// Vary goes on EVERY response, allowed origin or not, or a
			// shared cache serves one origin's answer to another. Add, not
			// Set: something else may already have varied on a header.
			h.Add("Vary", "Origin")

			origin := r.Header.Get("Origin")
			// A preflight is an OPTIONS carrying the method the real request
			// will use. Nothing but a browser sends that header, so an
			// ordinary OPTIONS still reaches the mux.
			preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""

			if origin != "" && (wildcard || slices.Contains(origins, origin)) {
				// The origin is echoed rather than "*" even in the wildcard
				// case, so the response is correct whether or not a
				// credentialed request ever arrives.
				h.Set("Access-Control-Allow-Origin", origin)
				if cfg.AllowCredentials {
					h.Set("Access-Control-Allow-Credentials", "true")
				}
				if preflight {
					h.Set("Access-Control-Allow-Methods", methods)
					h.Set("Access-Control-Allow-Headers", headers)
					h.Set("Access-Control-Max-Age", age)
				}
			}
			if preflight {
				// Answered here either way. A preflight the origin rule
				// rejected leaves without the allow headers, which is what
				// the browser reads as "no", and the handler below never
				// sees a request that was never meant for it.
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

// cmpOr is cmp.Or for slices, which cmp.Or itself cannot do: a nil slice is
// not comparable, so the generic version does not apply.
func cmpOr(v, fallback []string) []string {
	if len(v) == 0 {
		return fallback
	}
	return v
}
