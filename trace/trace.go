// Package trace is the tracing Starter, and it is opt-in by import: a service
// that does not import it links no OpenTelemetry at all.
//
// The split is measured, not stylistic. One HTTP service is 3 modules and
// 6,807,817 bytes stripped without tracing and 26 modules and 16,498,953 with
// it, so the OTLP stack is +9.69 MB, the heaviest single dependency in go-boot
// (#10 estimated +9.4 MB from a stub; #30 measured this from the real thing).
// Put it inside goboot/actuator and every Actuator user pays it, so it lives
// here, in a package you have to ask for.
//
// What the package exports, and why each one has to exist:
//
//   - Component, a TierObserve lifecycle piece that builds the provider on
//     Start and flushes it on Stop.
//   - Middleware, the otelhttp span, with RPC requests filtered out.
//   - DefaultMiddleware, the slice that puts Middleware in the one position
//     where the access log can carry the trace ID.
//   - IsRPC, RouteSpanName and WithIDs, the three parts DefaultMiddleware is
//     built from. They are exported because the slice is one you can rebuild
//     by hand, which is the whole difference between a default and a secret.
package trace

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/web"
)

// Config is the trace section. Every field is optional, and an empty field
// means "let the environment say", because the OTEL_* variables are the
// interface an operator already knows.
type Config struct {
	// Endpoint is the collector, written the way OTEL_EXPORTER_OTLP_ENDPOINT
	// is written: a URL, so the scheme decides TLS.
	// "http://localhost:4317" is cleartext; "https://..." is not. Empty
	// leaves it to the environment, which defaults to https://localhost:4317.
	Endpoint string `yaml:"endpoint"`
	// ServiceName is the name the traces are filed under. Empty leaves it to
	// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES.
	ServiceName string `yaml:"serviceName"`
	// SampleRatio keeps that share of the traces that arrive with no sampling
	// decision of their own. Zero does NOT mean "keep nothing": it means "not
	// set", and leaves the choice to OTEL_TRACES_SAMPLER, which keeps
	// everything by default. To keep almost nothing, write 0.0001.
	SampleRatio float64 `yaml:"sampleRatio"`
}

// Component is the lifecycle half: it owns the provider, and nothing else in
// the package needs it, because otelhttp reads the provider off the global
// that Start sets.
type Component struct {
	cfg Config
	log *slog.Logger
	tp  *sdktrace.TracerProvider
}

// New checks the config and holds it. Nothing is built here: the exporter
// belongs to Start, which has the start timeout on its ctx.
func New(cfg Config, log *slog.Logger) (*Component, error) {
	if cfg.SampleRatio < 0 || cfg.SampleRatio > 1 {
		return nil, fmt.Errorf("trace.sampleRatio: %v is not a share between 0 and 1", cfg.SampleRatio)
	}
	if log == nil {
		log = slog.Default()
	}
	return &Component{cfg: cfg, log: log}, nil
}

// DefaultMiddleware is web.DefaultMiddleware with tracing spliced into the one
// position that works:
//
//	srv.Use(trace.DefaultMiddleware(app.Log)...)
//
// One word different from web.DefaultMiddleware at the call site, and it has
// to be a separate call rather than a second Use, because Use appends. Adding
// tracing afterwards —
//
//	srv.Use(web.DefaultMiddleware(app.Log)...)
//	srv.Use(trace.Middleware())
//
// — puts the span INSIDE Logging, where the access-log line cannot carry the
// trace ID, because Logging read its context before the span existed. The
// order that works is RequestID, trace, Logging, Recovery.
//
// Logging is handed a logger that copies the trace and span IDs out of the
// context onto every line it writes, which is what makes the access-log line
// join up with the trace. Drop Logging from the returned slice and that goes
// with it.
//
// The fifth entry is the span name, and it is last because it has to be: see
// RouteSpanName.
func DefaultMiddleware(log *slog.Logger) []web.Middleware {
	log = WithIDs(log)
	return []web.Middleware{web.RequestID, Middleware(), web.Logging(log), web.Recovery(log), RouteSpanName}
}

// Middleware is otelhttp with RPC requests filtered out. It starts the span
// and puts it in the request context, which is all the Service Layer needs:
// it already takes a ctx first.
//
// Mounted on its own it names spans by method alone. RouteSpanName is what
// adds the route, and DefaultMiddleware carries both.
func Middleware() func(http.Handler) http.Handler {
	return otelhttp.NewMiddleware("", otelhttp.WithFilter(func(r *http.Request) bool { return !IsRPC(r) }))
}

// IsRPC is exported so the filter stays editable, and the rule is exact rather
// than a guess at the path. Measured in #12: otelhttp and otelconnect mounted
// together give TWO nested spans per RPC, a redundant HTTP parent wrapping the
// real one. This filter removes the parent, whether or not goboot/trace/rpc is
// imported.
//
// The two tests cover all four protocols connect speaks — gRPC, gRPC-Web,
// Connect proto and Connect JSON. A service with no gRPC never sees either
// header and pays nothing.
func IsRPC(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") ||
		r.Header.Get("Connect-Protocol-Version") != ""
}

// RouteSpanName names the span after the route template, and it is INNERMOST
// in DefaultMiddleware because it has to be. ServeMux fills r.Pattern in place
// on the request handed to it, so only a middleware below the last one to call
// r.WithContext sees it — and web.Logging calls it. Anywhere further out,
// including inside otelhttp itself, r.Pattern reads empty and the span keeps
// the name "GET".
//
// The path is never used. One span name per customer ID is a cardinality bill,
// not a trace.
//
// The naming is DEFERRED, not written after the call. A panicking handler
// unwinds straight past a plain post-call rename, and web.Recovery sits outside
// this, so it catches the panic only once the chance has gone — leaving the one
// span somebody is actually chasing named "GET".
func RouteSpanName(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			route := routeOf(r.Pattern)
			if route == "" {
				return // no route matched: a 404, and there is nothing low-cardinality to say
			}
			span := oteltrace.SpanFromContext(r.Context())
			span.SetName(r.Method + " " + route)
			span.SetAttributes(semconv.HTTPRoute(route))
		}()
		next.ServeHTTP(w, r)
	})
}

// routeOf drops the method and the host from a ServeMux pattern, leaving the
// path template. "GET example.com/hello/{name}" becomes "/hello/{name}".
func routeOf(pattern string) string {
	if i := strings.IndexByte(pattern, '/'); i >= 0 {
		return pattern[i:]
	}
	return ""
}

// Name is the Component name, also the key the Actuator would file a Check
// under. There is no Check: a collector that is down is not a reason to take
// the pod out of the load balancer.
func (c *Component) Name() string { return "trace" }

// Tier starts tracing first and stops it last, so a span from another
// Component's Start is still exported and a span from its Stop still is too.
func (c *Component) Tier() goboot.Tier { return goboot.TierObserve }

// Start builds the provider and installs it globally, which is how otelhttp
// and otelconnect find it without being handed anything.
//
// It returns a nil channel. The OTLP exporter connects lazily and retries on
// its own, so a collector that is missing at startup is not a startup failure
// and a collector that goes away later is not a reason to bring the service
// down.
func (c *Component) Start(ctx context.Context) (<-chan error, error) {
	var expOpts []otlptracegrpc.Option
	if c.cfg.Endpoint != "" {
		expOpts = append(expOpts, otlptracegrpc.WithEndpointURL(c.cfg.Endpoint))
	}
	exp, err := otlptracegrpc.New(ctx, expOpts...)
	if err != nil {
		return nil, err
	}
	res, err := c.resource()
	if err != nil {
		return nil, err
	}
	opts := []sdktrace.TracerProviderOption{sdktrace.WithBatcher(exp), sdktrace.WithResource(res)}
	if c.cfg.SampleRatio > 0 {
		// ParentBased, so a sampled trace that arrives from another service
		// is not thrown away half way through.
		opts = append(opts, sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(c.cfg.SampleRatio))))
	}
	c.tp = sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(c.tp)
	// Both halves of W3C context. Without the propagator the spans are real
	// but every service starts a new trace, which is the failure that looks
	// like success.
	//
	// This one is NOT left to the environment, unlike every field in Config.
	// OTEL_PROPAGATORS is a specification variable that opentelemetry-go does
	// not read — honouring it means importing contrib's autoprop and the four
	// vendor formats behind it — so there is no environment here to defer to,
	// and W3C is the format the rest of go-boot's defaults assume.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	// Said out loud because the exporter never fails loudly: an endpoint
	// typed wrong drops every span in silence otherwise.
	c.log.Info("exporting traces", "component", c.Name(), "endpoint", c.endpoint())
	return nil, nil
}

// endpoint is for the log line only. An empty Endpoint is the environment's
// job, and saying so beats printing "".
func (c *Component) endpoint() string {
	if c.cfg.Endpoint != "" {
		return c.cfg.Endpoint
	}
	return "from OTEL_EXPORTER_OTLP_ENDPOINT"
}

// resource is resource.Default, which already reads OTEL_SERVICE_NAME and
// OTEL_RESOURCE_ATTRIBUTES, with the configured service name layered over it.
func (c *Component) resource() (*resource.Resource, error) {
	if c.cfg.ServiceName == "" {
		return resource.Default(), nil
	}
	return resource.Merge(resource.Default(), resource.NewSchemaless(semconv.ServiceName(c.cfg.ServiceName)))
}

// Stop flushes. It is the whole reason tracing is a Component and not a
// middleware alone: the batch processor holds spans in memory, and a process
// that exits without this loses the last batch, which is the batch that
// contains the error you are chasing.
func (c *Component) Stop(ctx context.Context) error {
	if c.tp == nil {
		return nil // never started
	}
	return c.tp.Shutdown(ctx)
}

// WithIDs returns a logger that writes traceId and spanId on every line
// logged with a context that carries a span. This is what puts the trace ID on
// the access-log line: web.Logging logs with the request context, and by then
// Middleware has put the span in it.
//
// It is exported for the same reason IsRPC is: DefaultMiddleware is a slice
// you can edit, and rebuilding it by hand needs every part of it.
//
// The slog.Handler is wrapped rather than the logger, because web.Logging
// calls log.With to tag the request ID, and With goes through the handler.
func WithIDs(log *slog.Logger) *slog.Logger {
	return slog.New(traceHandler{log.Handler()})
}

type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, rec slog.Record) error {
	if sc := oteltrace.SpanContextFromContext(ctx); sc.IsValid() {
		rec.AddAttrs(
			slog.String("traceId", sc.TraceID().String()),
			slog.String("spanId", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, rec)
}

// WithAttrs and WithGroup are overridden, not inherited. The embedded
// handler's own versions return the INNER handler, which would drop this
// wrapper the first time web.Logging called log.With.
func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}
