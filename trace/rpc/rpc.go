// Package rpc is the tracing instrumentation for connect handlers, and it is
// opt-in by import: an HTTP-only service that traces links none of it.
//
// It is a subpackage of goboot/trace for the same reason goboot/trace is a
// subpackage of go-boot. Go links by import, so a dependency only some users
// need lives in a package they must import.
//
// # No RPC metrics in v1, on purpose
//
// otelconnect emits metrics as well as spans, and these options turn the
// metrics off. The reason is that they would go into the OTel pipeline, and
// /actuator/metrics reads the Prometheus registry, which cannot see them
// (#12). go-boot chose two pipelines rather than one — Prometheus for
// metrics, OTel for traces — and half a metric surface, visible only to
// whoever runs the collector, is worse than none.
//
// The cost is real and it is written down as a known gap: v1 reports no RPC
// count and no RPC latency by procedure. RPCs get spans, and a span carries
// the duration and the status code, so the data is there per request; what is
// missing is the aggregate.
package rpc

import (
	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
)

// Options returns the connect handler options that put ONE span on an RPC.
// They append to the ones every service already passes:
//
//	opts, err := rpc.Options()
//	if err != nil {
//		return err
//	}
//	srv.Handle(greetv1connect.NewGreetServiceHandler(svc,
//		append(grpc.DefaultOptions(app.Log), opts...)...))
//
// One span, not two, because goboot/trace filters otelhttp for exactly these
// requests — see trace.IsRPC. The filter is on whether or not this package is
// imported, so nothing here has to be switched on to make it true.
//
// The tracer is read from the global provider, which trace.Component.Start
// installs. Reading it here, while main is still wiring and Start has not run,
// is safe: OTel's global provider hands out a tracer that forwards to
// whatever is installed later.
func Options() ([]connect.HandlerOption, error) {
	interceptor, err := otelconnect.NewInterceptor(otelconnect.WithoutMetrics())
	if err != nil {
		return nil, err
	}
	return []connect.HandlerOption{connect.WithInterceptors(interceptor)}, nil
}
