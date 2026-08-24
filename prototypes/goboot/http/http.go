// Package http is a THROWAWAY stub of the go-boot HTTP Transport Starter.
//
// NOTE the import-name collision: a call site that also uses net/http must
// alias one of the two. See cmd/*/main.go.
package http

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type Config struct {
	Addr              string        `yaml:"addr"`
	ReadHeaderTimeout time.Duration `yaml:"readheadertimeout"`
}

// Server is a Component wrapping one net/http listener and its ServeMux.
type Server struct {
	Mux *http.ServeMux

	name string
	log  *slog.Logger
	srv  *http.Server
	ln   net.Listener
	errc chan error
}

func New(cfg Config, log *slog.Logger) *Server { return named("http", cfg, log) }

// Named exists so other Starters (actuator, grpc) can reuse this Component
// without reporting themselves as "http" in lifecycle logs.
func Named(name string, cfg Config, log *slog.Logger) *Server { return named(name, cfg, log) }

func named(name string, cfg Config, log *slog.Logger) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.ReadHeaderTimeout == 0 {
		cfg.ReadHeaderTimeout = 5 * time.Second
	}
	mux := http.NewServeMux()
	return &Server{
		Mux:  mux,
		name: name,
		log:  log,
		srv: &http.Server{
			Addr:              cfg.Addr,
			Handler:           mux,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			// Go 1.24+ serves HTTP/1 and cleartext HTTP/2 on one port, so the
			// gRPC Transport needs no h2c wrapper.
			Protocols: new(http.Protocols),
		},
		errc: make(chan error, 1),
	}
}

// Handle mounts a handler. Accepts the two-value return of a connect-go
// constructor unchanged: srv.Handle(greetv1connect.NewGreetServiceHandler(s)).
func (s *Server) Handle(pattern string, h http.Handler) { s.Mux.Handle(pattern, h) }

// Use wraps the whole mux (middleware, otelhttp, ...).
func (s *Server) Use(mw func(http.Handler) http.Handler) { s.srv.Handler = mw(s.srv.Handler) }

func (s *Server) Name() string { return s.name }

// Addr is the real bound address, valid after Start (supports :0 in tests).
func (s *Server) Addr() string { return s.ln.Addr().String() }

func (s *Server) Start(ctx context.Context) error {
	s.srv.Protocols.SetHTTP1(true)
	s.srv.Protocols.SetUnencryptedHTTP2(true)
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return err
	}
	s.ln = ln
	go func() { s.errc <- s.srv.Serve(ln) }()
	s.log.Info("listening", "component", s.name, "addr", ln.Addr().String())
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	if err := s.srv.Shutdown(ctx); err != nil {
		return err
	}
	if err := <-s.errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
