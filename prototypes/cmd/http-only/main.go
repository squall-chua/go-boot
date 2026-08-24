// Command http-only is the smallest useful go-boot service: PRESET FORM.
// Run the explicit form this Preset expands to with: ./http-only explicit
package main

import (
	"context"
	"net/http"
	"os"

	"goboot-prototype/goboot/preset"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "explicit" {
		mainExplicit()
		return
	}

	app, err := preset.HTTP(":8080")
	if err != nil {
		panic(err)
	}
	app.HTTP.Handle("GET /hello/{name}", http.HandlerFunc(hello))
	if err := app.Run(context.Background()); err != nil {
		app.Log.Error("exit", "err", err)
		os.Exit(1)
	}
}

// hello stands in for the Service Layer.
func hello(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("hello " + r.PathValue("name") + "\n"))
}
