// Package reflection is the gRPC server reflection service, and it is opt-in
// by import: a service that does not import it links none of it.
//
// This is Spring Boot's optional-jar auto-configuration translated. Go has no
// classpath conditionals, so "optional jar" becomes "a package you must
// import". Once imported it works with no configuration at all.
//
// It is named reflection and not reflect, so that a main importing the
// standard library's reflect does not have to alias one of them. That is the
// rule ADR 0005 exists to keep.
package reflection

import (
	"net/http"

	"connectrpc.com/grpcreflect"
)

// Handler is the structural interface MountOn takes. *web.Server satisfies
// it, so this package imports no Starter. It is the Actuator's interface,
// repeated rather than shared, because sharing it would put a dependency
// between two packages that have nothing else to say to each other.
type Handler interface {
	Handle(pattern string, h http.Handler)
}

// MountOn registers the reflection service for the services named, which are
// the generated `...ServiceName` constants:
//
//	reflection.MountOn(srv, greetv1connect.GreetServiceName)
//
// The names are given rather than discovered because connect keeps no
// registry of what has been mounted. The descriptors behind them are found in
// the process-wide protobuf registry, which the generated package fills on
// import, so the names are the only thing left to say.
//
// It mounts BOTH the current handler and the older v1alpha one, because
// grpcurl still asks for v1alpha. Two mounts is why this is MountOn rather
// than a pair returned to Handle the way health.New does it.
func MountOn(h Handler, services ...string) {
	reflector := grpcreflect.NewStaticReflector(services...)
	h.Handle(grpcreflect.NewHandlerV1(reflector))
	h.Handle(grpcreflect.NewHandlerV1Alpha(reflector))
}
