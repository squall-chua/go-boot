// Package trace is a THROWAWAY stub of the go-boot tracing Starter (#10).
//
// SIGNATURE ONLY. The real one imports OTel — +9.4 MB stripped and 19 indirect
// modules, the heaviest single Starter. This stub imports nothing, so the
// prototype's own weight numbers stay comparable with earlier tickets. The
// call-site shape is what #14 is measuring, and that is real.
package trace

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"goboot-prototype/goboot"
	"goboot-prototype/goboot/web"
)

type Config struct {
	Endpoint    string  `yaml:"endpoint"`
	ServiceName string  `yaml:"serviceName"`
	SampleRatio float64 `yaml:"sampleRatio"`
}

type Component struct {
	cfg Config
	log *slog.Logger
}

func New(cfg Config, log *slog.Logger) (*Component, error) {
	return &Component{cfg: cfg, log: log}, nil
}

// DefaultMiddleware is web.DefaultMiddleware with tracing inserted in the one
// position that works. Order matters and Use cannot express it: Use appends,
// so a later Use lands INNERMOST (pinned by web.TestUseOrder). Tracing must
// run AFTER RequestID, so the span joins the request-scoped logger, and
// BEFORE Logging, so the access-log line carries the trace ID.
//
// Importing goboot/web from here costs nothing: goboot/web links no
// third-party module.
func DefaultMiddleware(log *slog.Logger) []web.Middleware {
	return []web.Middleware{web.RequestID, Middleware(), web.Logging(log), web.Recovery(log)}
}

// Middleware is otelhttp with the span name taken from r.Pattern, and with
// RPC requests filtered out — otelhttp plus otelconnect gives two nested spans
// per RPC (measured, #12).
func Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}

// IsRPC is exported so the filter stays editable. The rule is exact, not a
// heuristic: gRPC content type, or a Connect protocol header.
func IsRPC(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") ||
		r.Header.Get("Connect-Protocol-Version") != ""
}

func (c *Component) Name() string      { return "trace" }
func (c *Component) Tier() goboot.Tier { return goboot.TierObserve }

func (c *Component) Start(ctx context.Context) (<-chan error, error) { return nil, nil }
func (c *Component) Stop(ctx context.Context) error                  { return nil }
