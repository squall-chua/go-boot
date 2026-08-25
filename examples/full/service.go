package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	"github.com/squall-chua/go-boot/web"

	greetv1 "github.com/squall-chua/go-boot/internal/gen/greet/v1"
)

// greeter is the Service Layer: plain Go holding the behaviour, knowing
// nothing about HTTP or gRPC. Both Transports call into it, and both forms of
// main share it unchanged.
type greeter struct {
	db       *sql.DB
	greeting string
}

func (g *greeter) Greet(ctx context.Context, name string) (string, error) {
	greeting := g.greeting // the service's own config key, the fallback
	err := g.db.QueryRowContext(ctx, `SELECT greeting FROM greetings WHERE lang = $1`, "en").Scan(&greeting)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return greeting + " " + name, nil
}

// httpGreet is the HTTP Transport: a thin shell over the Service Layer.
func httpGreet(g *greeter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := g.Greet(r.Context(), r.PathValue("name"))
		if err != nil {
			http.Error(w, "greet failed", http.StatusInternalServerError)
			return
		}
		web.WriteJSON(w, http.StatusOK, map[string]string{"greeting": out})
	})
}

// grpcGreeter is the gRPC Transport. It has to be a SEPARATE type from the
// Service Layer: connect-go's generated interface wants
// Greet(ctx, *connect.Request[...]) and greeter already owns the name Greet.
//
// Both methods return the Service Layer's error BARE. That is the convention
// of docs/spec.md 4.0 and it is not a shortcut. connect.NewError(code, err)
// makes err's own text the message the caller receives, and the sanitiser in
// grpc.DefaultOptions passes a *connect.Error through untouched on purpose —
// so wrapping here would put the error's text on the wire with every option
// correctly wired. Returned bare, the sanitiser logs the real error and sends
// a bare CodeUnknown.
//
// To tell a caller something useful, write the text yourself:
//
//	return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
type grpcGreeter struct{ svc *greeter }

func (g *grpcGreeter) Greet(ctx context.Context, req *connect.Request[greetv1.GreetRequest]) (*connect.Response[greetv1.GreetResponse], error) {
	out, err := g.svc.Greet(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err // bare: the sanitiser owns what the caller sees
	}
	return connect.NewResponse(&greetv1.GreetResponse{Greeting: out}), nil
}

// GreetStream is the streaming half of the same service. It shares the one
// listener with HTTP, which is why web.Config has no write timeout.
func (g *grpcGreeter) GreetStream(ctx context.Context, req *connect.Request[greetv1.GreetStreamRequest], stream *connect.ServerStream[greetv1.GreetStreamResponse]) error {
	out, err := g.svc.Greet(ctx, req.Msg.GetName())
	if err != nil {
		return err // bare: the sanitiser owns what the caller sees
	}
	return stream.Send(&greetv1.GreetStreamResponse{Greeting: out})
}
