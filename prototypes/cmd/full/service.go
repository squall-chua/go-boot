package main

import (
	"context"
	"database/sql"
	"net/http"

	"connectrpc.com/connect"

	greetv1 "goboot-prototype/internal/gen/greet/v1"
)

// greeter is the Service Layer: plain Go, knows nothing about HTTP or gRPC.
// Both Transports call into it. Shared by the Preset and explicit forms.
type greeter struct{ db *sql.DB }

func (g *greeter) Greet(ctx context.Context, name string) (string, error) {
	var greeting string
	err := g.db.QueryRowContext(ctx, `SELECT greeting FROM greetings WHERE lang = $1`, "en").Scan(&greeting)
	if err != nil {
		return "", err
	}
	return greeting + " " + name, nil
}

// httpGreet is the HTTP Transport's thin adapter.
func httpGreet(g *greeter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := g.Greet(r.Context(), r.PathValue("name"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte(out + "\n"))
	})
}

// grpcGreeter is the gRPC Transport's thin adapter. It has to be a SEPARATE
// type: connect-go's generated interface wants Greet(ctx, *connect.Request[...])
// and the Service Layer already owns the name Greet.
type grpcGreeter struct{ svc *greeter }

func (g *grpcGreeter) Greet(ctx context.Context, req *connect.Request[greetv1.GreetRequest]) (*connect.Response[greetv1.GreetResponse], error) {
	out, err := g.svc.Greet(ctx, req.Msg.GetName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&greetv1.GreetResponse{Greeting: out}), nil
}
