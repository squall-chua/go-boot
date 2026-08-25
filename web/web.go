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
//
// There is no writeTimeout: it stays off because gRPC streams share this
// server (ADR 0006), and a write deadline would cut a long stream in half.
type Config struct {
	Addr              string        `yaml:"addr"`              // ":8080"
	ReadHeaderTimeout time.Duration `yaml:"readHeaderTimeout"` // 5s
	IdleTimeout       time.Duration `yaml:"idleTimeout"`       // 120s
	MaxBodyBytes      int64         `yaml:"maxBodyBytes"`      // 1 MiB; the cap DecodeJSON applies
	// TLS is two keys and nothing else. There is no autocert: a service
	// behind an ingress does not need one, and one in front of the internet
	// needs a story about storage and renewal that go-boot does not have.
	TLS struct {
		CertFile string `yaml:"certFile"`
		KeyFile  string `yaml:"keyFile"`
	} `yaml:"tls"`
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

	maxBody int64
	tls     struct{ certFile, keyFile string }
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
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = defaultMaxBodyBytes
	}
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	s := &Server{
		log: log,
		mux: mux,
		srv: &http.Server{
			Addr:              cfg.Addr,
			Handler:           mux,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			IdleTimeout:       cfg.IdleTimeout,
			// WriteTimeout stays off: gRPC streams share this server.
			Protocols: protocols(),
		},
		errc:    make(chan error, 1),
		maxBody: cfg.MaxBodyBytes,
	}
	s.tls.certFile, s.tls.keyFile = cfg.TLS.CertFile, cfg.TLS.KeyFile
	return s
}

// protocols turns on cleartext HTTP/2 alongside HTTP/1. This is what makes
// ADR 0006 true: the gRPC protocol needs HTTP/2, and behind an ingress the
// hop go-boot answers is cleartext, so without this a plain gRPC client gets
// `frame too large, note that the frame header looked like an HTTP/1.1
// header` and nothing else, and the access log records a 400 for method=PRI.
// Measured for [#28]: delete a line here and both
// TestCleartextHTTP2IsOn below and TestAGRPCClientIsUnaffectedByTheProtocolHeader
// in goboot/grpc fail.
//
// Go's default leaves HTTP/2 to TLS only. With both HTTP/1 and unencrypted
// HTTP/2 on, net/http reads the client preface to tell them apart, so one
// port answers gRPC, gRPC-Web, Connect JSON and plain REST at once. Since Go
// 1.24 this needs no golang.org/x/net/http2 and no h2c wrapper.
//
// It is not a config key. A port that refuses gRPC is not a choice go-boot
// offers, because the whole gRPC Starter rests on this.
func protocols() *http.Protocols {
	p := &http.Protocols{}
	p.SetHTTP1(true)
	p.SetHTTP2(true)            // over TLS, by ALPN
	p.SetUnencryptedHTTP2(true) // h2c, which is the hop behind an ingress
	return p
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
	// One key without the other is a half-finished config, most likely a
	// misspelt path. Checked here, before the listener opens, so it comes
	// back from Start rather than arriving on errc a moment later.
	if (s.tls.certFile == "") != (s.tls.keyFile == "") {
		return nil, errors.New("web: tls needs both certFile and keyFile, or neither")
	}
	// Wrap the mux itself, never s.srv.Handler, so a second Start does not
	// wrap the middleware round a second time.
	var h http.Handler = s.mux
	for i := len(s.mw) - 1; i >= 0; i-- { // first listed ends up outermost
		h = s.mw[i](h)
	}
	// Outermost of all, so DecodeJSON sees the configured cap whatever the
	// developer did to the middleware slice.
	s.srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(withMaxBody(r.Context(), s.maxBody)))
	})

	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return nil, err
	}
	s.ln = ln
	go func() {
		err := s.serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.errc <- err
	}()
	s.log.Info("listening", "component", s.Name(), "addr", ln.Addr().String())
	return s.errc, nil
}

// serve picks the plain or the TLS listener. Start has already rejected one
// key without the other, so testing either is enough.
func (s *Server) serve(ln net.Listener) error {
	if s.tls.certFile != "" {
		return s.srv.ServeTLS(ln, s.tls.certFile, s.tls.keyFile)
	}
	return s.srv.Serve(ln)
}

// Stop refuses new connections and waits for the open ones, up to the
// deadline on ctx.
func (s *Server) Stop(ctx context.Context) error { return s.srv.Shutdown(ctx) }
