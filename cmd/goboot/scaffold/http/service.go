package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/squall-chua/go-boot/web"
)

// greeter is the Service Layer: plain Go holding the behaviour, knowing
// nothing about HTTP. Delete it and write your own — it is here so that
// `go run .` answers something on the first try.
type greeter struct {
	db       *sql.DB
	greeting string
}

func (g *greeter) Greet(ctx context.Context, name string) (string, error) {
	greeting := g.greeting // this service's own config key, the fallback
	err := g.db.QueryRowContext(ctx, `SELECT message FROM greeting WHERE lang = $1`, "en").Scan(&greeting)
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
			// The real error is already on the access log line. Do not put
			// it on the wire.
			http.Error(w, "greet failed", http.StatusInternalServerError)
			return
		}
		web.WriteJSON(w, http.StatusOK, map[string]string{"greeting": out})
	})
}
