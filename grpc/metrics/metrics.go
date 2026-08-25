// Package metrics counts and times RPCs, and it is opt-in by import: a
// service that does not import it links none of it.
//
// # One pipeline, one endpoint
//
// The counter and the histogram are registered on
// prometheus.DefaultRegisterer, which is the registry /actuator/metrics
// serves. That is not an implementation detail, it is the decision #41 had to
// make before it could add a single metric: Prometheus owns every metric
// go-boot emits, and OTel owns traces and nothing else, so an operator asking
// "how many of my RPCs failed" has one place to look. See docs/spec.md 4.2.
//
// The alternative was to let otelconnect emit the metrics it already has and
// bridge the OTel meter into the Prometheus registry. It was refused for two
// reasons: it adds the OTel metric SDK and its exporter as modules, and it
// makes RPC metrics conditional on tracing being imported AND enabled, so a
// service that wants a counter and runs no collector could not have one.
// goboot/trace/rpc goes on passing otelconnect.WithoutMetrics().
//
// Nothing here is linked by a user who does not import it, which is what
// keeps the Actuator from growing a second Prometheus dependency for people
// who never enable metrics. .github/check-imports.sh asserts it on every push.
package metrics

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// The two metrics, registered at package init rather than inside Options.
// connect options are per SERVICE, so Options is called once per mount, and a
// registration inside it would panic on the second service. Registering here
// is also why Options has no error to return, which is the one place this
// package's signature differs from trace/rpc.Options.
//
// No Prometheus type appears in this package's API, per docs/spec.md 4.2.
// Both labels are bounded: procedures are fixed at compile time and there are
// seventeen connect codes, so neither can grow a cardinality problem.
var (
	requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rpc_requests_total",
		Help: "RPCs handled, by procedure and by the connect code the caller received.",
	}, []string{"procedure", "code"})

	duration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "rpc_duration_seconds",
		Help:    "RPC handler latency in seconds, by procedure and by the connect code the caller received.",
		Buckets: prometheus.DefBuckets,
	}, []string{"procedure", "code"})
)

// Options returns the connect handler options that count and time an RPC.
// They append to the ones every service already passes, the same shape
// trace/rpc.Options has:
//
//	srv.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc},
//		append(grpc.DefaultOptions(app.Log), metrics.Options()...)...))
//
// That order matters and is the documented one. grpc.DefaultOptions puts
// connect.WithRecover first, and connect chains interceptors first-outermost,
// so recovery WRAPS this one — see the deferred record below.
func Options() []connect.HandlerOption {
	return []connect.HandlerOption{connect.WithInterceptors(recorder{})}
}

type recorder struct{}

func (recorder) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (_ connect.AnyResponse, err error) {
		defer record(req.Spec().Procedure, time.Now(), &err)
		return next(ctx, req)
	}
}

// WrapStreamingHandler counts a stream once, when it ends, and times the whole
// stream's lifetime. Streaming is supported by the Starter, so leaving it out
// would be a hole in the same answer. Anything finer than one figure per
// stream needs a metric the handler owns.
func (recorder) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) (err error) {
		defer record(conn.Spec().Procedure, time.Now(), &err)
		return next(ctx, conn)
	}
}

// WrapStreamingClient does nothing, the same as the sanitiser's: these options
// are mounted on a handler, so the client half is never reached.
func (recorder) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// record writes the two metrics. It is deferred, not called after next, and
// that is load-bearing: connect.WithRecover is an interceptor too and
// grpc.DefaultOptions puts it outermost, so a panicking handler unwinds PAST
// anything written after next() and the failed RPC goes uncounted — the one
// call an operator counting failures most wants to see.
//
// start and the error pointer are both evaluated at defer time. On a panic the
// error is still nil, so the panic is recovered here to label the RPC with the
// CodeInternal that WithRecover is about to send the caller, then re-panicked
// so WithRecover still logs it and writes that error.
func record(procedure string, start time.Time, err *error) {
	p := recover()

	// http.ErrAbortHandler is a handler saying "drop this connection
	// quietly", and it goes back untouched and UNCOUNTED. Every other layer
	// around this one already treats it that way: connect re-panics it rather
	// than building an error (recover.go), web.Recovery re-panics it rather
	// than writing a 500, and web.Logging writes no access line for it at all,
	// because it logs after the call rather than in a defer. A counter that
	// alone called this an internal error would be the odd one out, and it
	// would be wrong: nothing failed, the caller left.
	if p == http.ErrAbortHandler {
		panic(p)
	}

	code := connect.CodeInternal.String()
	if p == nil {
		code = "ok"
		if *err != nil {
			code = connect.CodeOf(*err).String()
		}
	}

	requests.WithLabelValues(procedure, code).Inc()
	duration.WithLabelValues(procedure, code).Observe(time.Since(start).Seconds())

	if p != nil {
		panic(p)
	}
}
