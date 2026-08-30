// Package rpc is the gRPC adapter for the greeting feature. It sits beside
// the rest package because both are shells over the same Service Layer, and
// gRPC shares the one HTTP listener, so there is no second port.
//
// It is called rpc, not grpc, so that the go-boot grpc package it needs does
// not have to be imported under an alias.
package rpc

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/squall-chua/go-boot/grpc"
	"github.com/squall-chua/go-boot/web"

	"github.com/squall-chua/go-boot/cmd/goboot/scaffold/grpc/internal/greeting"

	greetv1 "github.com/squall-chua/go-boot/internal/gen/greet/v1"
	"github.com/squall-chua/go-boot/internal/gen/greet/v1/greetv1connect"
)

// Routes mounts the gRPC half of this feature. The mount names your OWN
// generated package, which is why no Preset can wire it. Leave
// grpc.DefaultOptions off and the error sanitiser goes with it.
func Routes(srv *web.Server, log *slog.Logger, s *greeting.Service) {
	srv.Handle(greetv1connect.NewGreetServiceHandler(&server{svc: s}, grpc.DefaultOptions(log)...))
}

// server is the adapter type. It has to be SEPARATE from greeting.Service
// because connect-go's generated interface wants
// Greet(ctx, *connect.Request[...]), and it stays unexported because only
// Routes above ever builds one.
//
// Both methods return the Service Layer's error BARE. The sanitiser in
// grpc.DefaultOptions logs the real error and sends a bare CodeUnknown, so
// nothing internal reaches the caller. To tell a caller something useful,
// write the text yourself:
//
//	connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
type server struct{ svc *greeting.Service }

func (g *server) Greet(ctx context.Context, req *connect.Request[greetv1.GreetRequest]) (*connect.Response[greetv1.GreetResponse], error) {
	out, err := g.svc.Greet(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err // bare: the sanitiser owns what the caller sees
	}
	return connect.NewResponse(&greetv1.GreetResponse{Greeting: out}), nil
}

// GreetStream is the streaming half. It shares the one listener with HTTP,
// which is why web.Config has no write timeout.
func (g *server) GreetStream(ctx context.Context, req *connect.Request[greetv1.GreetStreamRequest], stream *connect.ServerStream[greetv1.GreetStreamResponse]) error {
	out, err := g.svc.Greet(ctx, req.Msg.GetName())
	if err != nil {
		return err // bare: the sanitiser owns what the caller sees
	}
	return stream.Send(&greetv1.GreetStreamResponse{Greeting: out})
}
