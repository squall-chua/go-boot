// Package web is the HTTP Transport Starter. It is named web, not http,
// because every main imports net/http and goboot/http would need an alias at
// every call site (ADR 0005).
package web

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/squall-chua/go-boot"
)

// Config is the web section. Every field has a working default.
type Config struct {
	Addr              string        `yaml:"addr"`              // ":8080"
	ReadHeaderTimeout time.Duration `yaml:"readHeaderTimeout"` // 5s
	IdleTimeout       time.Duration `yaml:"idleTimeout"`       // 120s
}

// Middleware wraps a handler. It is the plain net/http shape, so anything
// written for net/http works here unchanged.
type Middleware = func(http.Handler) http.Handler

// Server is a Component wrapping one net/http listener and its ServeMux.
type Server struct {
	log  *slog.Logger
	mux  *http.ServeMux
	srv  *http.Server
	mw   []Middleware
	ln   net.Listener
	errc chan error
}

// New builds the Server. It cannot fail, so it returns no error: the listener
// is opened in Start, which does return one. A nil logger falls back to
// slog.Default() rather than panicking later.
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
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	return &Server{
		log: log,
		mux: mux,
		srv: &http.Server{
			Addr:              cfg.Addr,
			Handler:           mux,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			IdleTimeout:       cfg.IdleTimeout,
			// WriteTimeout stays off: gRPC streams share this server.
		},
		errc: make(chan error, 1),
	}
}

// Handle mounts a handler. It takes the two-value return of a connect-go
// constructor unchanged.
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

// HandleFunc mounts an ordinary handler function.
func (s *Server) HandleFunc(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, h) }

// Use appends middleware. The FIRST entry listed ends up outermost, which is
// how the line reads, so a later Use call lands innermost.
func (s *Server) Use(mw ...Middleware) { s.mw = append(s.mw, mw...) }

// Addr reports the address the listener bound to, which is what you need
// after asking for port zero. Before Start it reports the configured address.
func (s *Server) Addr() string {
	if s.ln == nil {
		return s.srv.Addr
	}
	return s.ln.Addr().String()
}

// Name is the Component name, also the key the Actuator files its Check under.
func (s *Server) Name() string { return "web" }

// Tier puts the Server last to start and first to stop.
func (s *Server) Tier() goboot.Tier { return goboot.TierTransport }

// Start opens the listener and serves in the background. It returns once the
// port is bound, so a caller can read Addr straight after.
func (s *Server) Start(ctx context.Context) (<-chan error, error) {
	// Wrap the mux itself, never s.srv.Handler, so a second Start does not
	// wrap the middleware round a second time.
	var h http.Handler = s.mux
	for i := len(s.mw) - 1; i >= 0; i-- { // first listed ends up outermost
		h = s.mw[i](h)
	}
	s.srv.Handler = h

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

// Stop refuses new connections and waits for the open ones, up to the
// deadline on ctx.
func (s *Server) Stop(ctx context.Context) error { return s.srv.Shutdown(ctx) }
