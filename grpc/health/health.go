// Package health is the standard gRPC health service, and it is opt-in by
// import: a service that does not import it links none of it.
//
// This is Spring Boot's optional-jar auto-configuration translated. Go has no
// classpath conditionals, so "optional jar" becomes "a package you must
// import". Once imported it works with no configuration at all.
//
// The answer it gives is the same answer /readyz gives, read from the same
// App. Only the empty service name is answered; per-service statuses are not
// in v1, so any other name is NOT_FOUND. The streaming Watch RPC is not
// implemented either: grpc-health-probe and Kubernetes both poll Check.
package health

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"

	"github.com/squall-chua/go-boot"
)

// New returns the path and the handler, the same pair a generated connect
// constructor returns, so it mounts on the HTTP Starter with no adapter:
//
//	srv.Handle(health.New(app))
//
// There is no options argument. grpc.DefaultOptions guards a handler the user
// wrote; this one puts nothing on the wire but a status, so the sanitiser has
// nothing to sanitise. A Check that panics is caught by web.DefaultMiddleware
// under the shared listener, exactly as it is on /readyz.
func New(app *goboot.App) (string, http.Handler) {
	return grpchealth.NewHandler(checker{app: app})
}

// checker reads exactly what the Actuator's /readyz reads: the App's
// readiness, followed by every Component's Check. Reading one and not the
// other would let a database outage answer 503 on HTTP and SERVING on gRPC.
//
// The two agree on the drain because of ADR 0001's ordering and not by
// accident: /readyz has a draining flag of its own, and the only reason this
// package does not need one is that the App turns readiness false BEFORE any
// Drain runs. Move that and the two answers split.
//
// The Checks are pulled per request rather than kept, because New is called
// while main is still wiring and the Components are not all added yet. That
// is the Actuator's reason too; it just gets to do it in Start instead.
type checker struct{ app *goboot.App }

func (c checker) Check(ctx context.Context, req *grpchealth.CheckRequest) (*grpchealth.CheckResponse, error) {
	if req.Service != "" {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("unknown service %q", req.Service))
	}
	if !c.app.Ready() {
		return notServing(), nil
	}
	// Nothing is logged here. The Actuator already writes one "check failed"
	// line per failing Check, and a second copy of it per gRPC probe is noise,
	// not evidence.
	for _, check := range c.app.Checks() {
		if err := check.Check(ctx); err != nil {
			return notServing(), nil
		}
	}
	return &grpchealth.CheckResponse{Status: grpchealth.StatusServing}, nil
}

func notServing() *grpchealth.CheckResponse {
	return &grpchealth.CheckResponse{Status: grpchealth.StatusNotServing}
}
