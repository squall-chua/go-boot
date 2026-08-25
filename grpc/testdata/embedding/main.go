// This package does not compile, and that is the point. The README says why
// the gRPC Transport has to be its own type rather than the Service Layer with
// the generated interface embedded, and it quotes the two compiler errors.
// TestEmbeddingTheServiceLayerDoesNotCompile builds this and checks the errors
// are still the ones the README shows.
//
// It lives under testdata so that `go build ./...` never reaches it.
package main

import (
	"context"

	"connectrpc.com/connect"

	greetv1 "github.com/squall-chua/go-boot/internal/gen/greet/v1"
	"github.com/squall-chua/go-boot/internal/gen/greet/v1/greetv1connect"
)

// greeter is the Service Layer, which already owns the name Greet.
type greeter struct{}

func (g *greeter) Greet(_ context.Context, name string) (string, error) { return "hello " + name, nil }

// badA embeds the Service Layer and hopes.
type badA struct{ *greeter }

func (b *badA) GreetStream(context.Context, *connect.Request[greetv1.GreetStreamRequest], *connect.ServerStream[greetv1.GreetStreamResponse]) error {
	return nil
}

var _ greetv1connect.GreetServiceHandler = (*badA)(nil)

// badB also embeds the generated Unimplemented type, which is what a user does
// for forward compatibility. This is the confusing one.
type badB struct {
	*greeter
	greetv1connect.UnimplementedGreetServiceHandler
}

var _ greetv1connect.GreetServiceHandler = (*badB)(nil)

func main() {}
