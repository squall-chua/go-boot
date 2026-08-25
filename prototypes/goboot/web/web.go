// Package web is a THROWAWAY stub of the go-boot HTTP Transport Starter as
// settled in #11. Renamed from goboot/http, which collided with net/http at
// every call site.
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"goboot-prototype/goboot"
)

type Config struct {
	Addr              string        `yaml:"addr"`
	ReadHeaderTimeout time.Duration `yaml:"readHeaderTimeout"` // 5s
	IdleTimeout       time.Duration `yaml:"idleTimeout"`       // 120s
	MaxBodyBytes      int64         `yaml:"maxBodyBytes"`      // 1 MiB
	TLS               struct {
		CertFile string `yaml:"certFile"`
		KeyFile  string `yaml:"keyFile"`
	} `yaml:"tls"`
}

type Middleware = func(http.Handler) http.Handler

// Server is a Component wrapping one net/http listener and its ServeMux.
type Server struct {
	mux  *http.ServeMux
	log  *slog.Logger
	srv  *http.Server
	mw   []Middleware
	ln   net.Listener
	errc chan error
}

func New(cfg Config, log *slog.Logger) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.ReadHeaderTimeout == 0 {
		cfg.ReadHeaderTimeout = 5 * time.Second
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 120 * time.Second
	}
	mux := http.NewServeMux()
	return &Server{
		mux: mux,
		log: log,
		srv: &http.Server{
			Addr:              cfg.Addr,
			Handler:           mux,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			IdleTimeout:       cfg.IdleTimeout,
			// writeTimeout stays off: gRPC streams share this server (#11).
			Protocols: new(http.Protocols),
		},
		errc: make(chan error, 1),
	}
}

// Handle mounts a handler. It takes the two-value return of a connect-go
// constructor unchanged: srv.Handle(greetv1connect.NewGreetServiceHandler(s)).
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

func (s *Server) HandleFunc(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, h) }

// Use adds middleware. The FIRST entry listed ends up outermost, which is how
// the line reads.
func (s *Server) Use(mw ...Middleware) { s.mw = append(s.mw, mw...) }

// DefaultMiddleware is a slice you can edit, not hidden behaviour. Recovery
// sits inside Logging so its 500 is what gets logged.
func DefaultMiddleware(log *slog.Logger) []Middleware {
	return []Middleware{RequestID, Logging(log), Recovery(log)}
}

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if len(id) == 0 || len(id) > 64 {
			b := make([]byte, 16)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-Id", id)
		ctx := goboot.WithLogger(r.Context(), slog.Default().With("requestId", id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func Logging(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isProbe(r.URL.Path) { // ~17k lines/day saying nothing
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			next.ServeHTTP(w, r)
			log.Info("request", "method", r.Method, "path", r.URL.Path,
				"route", r.Pattern, "duration", time.Since(start))
		})
	}
}

func Recovery(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					log.Error("panic", "err", v, "route", r.Pattern)
					WriteProblem(w, http.StatusInternalServerError, "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func isProbe(p string) bool {
	return p == "/livez" || p == "/readyz" || len(p) >= 10 && p[:10] == "/actuator/"
}

// WriteProblem writes an RFC 7807 document. It is a function the user calls,
// not a handler signature.
func WriteProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"title": http.StatusText(status), "status": status, "detail": detail,
	})
}

func (s *Server) Name() string      { return "web" }
func (s *Server) Tier() goboot.Tier { return goboot.TierTransport }
func (s *Server) Addr() string      { return s.ln.Addr().String() }

func (s *Server) Start(ctx context.Context) (<-chan error, error) {
	h := s.srv.Handler
	for i := len(s.mw) - 1; i >= 0; i-- { // first listed ends outermost
		h = s.mw[i](h)
	}
	s.srv.Handler = h
	s.srv.Protocols.SetHTTP1(true)
	s.srv.Protocols.SetUnencryptedHTTP2(true)
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return nil, err
	}
	s.ln = ln
	go func() {
		err := s.srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.errc <- err
	}()
	s.log.Info("listening", "component", s.Name(), "addr", ln.Addr().String())
	return s.errc, nil
}

func (s *Server) Stop(ctx context.Context) error { return s.srv.Shutdown(ctx) }
