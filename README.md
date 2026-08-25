# go-boot

go-boot gives a Go service the operational spine a Spring Boot developer expects: an ordered
Component lifecycle and a real Actuator, built on the standard library, neutral about the query
layer.

**go-boot is not for a single-endpoint HTTP service with no database and no Actuator.** It has no
lifecycle to order, so there is nothing for go-boot to encode. Use `net/http` directly. This was
measured: at one Component go-boot made `main` *longer*, and a 78-line standard-library file beat
it outright.

## Status

Early. What works today is one HTTP route served by a real listener, started and stopped in Tier
order, with a clean shutdown on SIGTERM. The rest of the v1 surface is being built ticket by
ticket against `docs/spec.md`.

## Install

```
go get github.com/squall-chua/go-boot
```

## Use

```go
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/web"
)

func main() {
	app, err := goboot.New(goboot.Config{})
	if err != nil {
		log.Fatal(err)
	}

	srv := web.New(web.Config{Addr: ":8080"}, app.Log)
	srv.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	})
	app.Add(srv)

	if err := app.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
```

`app.Add` ignores the order you write. Each Component declares its own Tier, and go-boot starts
from the lowest Tier to the highest and stops in reverse. So wiring in the wrong order is not a
mistake you can make.

## Go version

The floor is **Go 1.25.0**.

It will rise. Once the database Starter lands, go-boot depends on `goose/v3`, which declares a
patch-level `go` directive. The highest floor in a module wins, so `go mod tidy` will write
**`go 1.25.7`** into `go.mod`, and Go 1.25.0 through 1.25.6 will no longer build go-boot at all —
even for a user who never imports the database Starter.

A stock Go 1.22 toolchain handles this on its own: it downloads the newer toolchain in about
fifteen seconds. But that needs the toolchain switch and the module proxy to be working, so
`GOTOOLCHAIN=local`, `GOPROXY=off` and any Go below 1.21 are unsupported.

## Documentation

- `docs/spec.md` — the locked v1 spec, and the design authority for every ticket.
- `CONTEXT.md` — the words go-boot uses, and what each one means.
- `docs/adr/` — the decisions, one file each.

## Licence

See `LICENSE`.
