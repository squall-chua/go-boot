// Package grpc is a THROWAWAY stub of the go-boot gRPC Transport Starter,
// built on connectrpc.com/connect. One cleartext port serves gRPC, gRPC-Web
// and Connect JSON; no h2c wrapper since Go 1.24.
package grpc

import (
	"context"
	"log/slog"
	"net/http"

	gbhttp "goboot-prototype/goboot/http"
)

type Config struct {
	Addr string `yaml:"addr"`
}

type Server struct {
	srv *gbhttp.Server
}

func New(cfg Config, log *slog.Logger) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":8081"
	}
	return &Server{srv: gbhttp.Named("grpc", gbhttp.Config{Addr: cfg.Addr}, log)}
}

// Mount takes the exact two-value return of a connect-go generated
// constructor, so the call site is:
//
//	grpcSrv.Mount(greetv1connect.NewGreetServiceHandler(svc))
func (s *Server) Mount(pattern string, h http.Handler) { s.srv.Handle(pattern, h) }

func (s *Server) Name() string                    { return "grpc" }
func (s *Server) Start(ctx context.Context) error { return s.srv.Start(ctx) }
func (s *Server) Stop(ctx context.Context) error  { return s.srv.Stop(ctx) }
func (s *Server) Addr() string                    { return s.srv.Addr() }
