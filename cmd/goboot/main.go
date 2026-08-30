// Command goboot is the Scaffold: it writes a new go-boot project and then
// gets out of the way. It has one subcommand and one flag.
//
//	go run github.com/squall-chua/go-boot/cmd/goboot@latest new github.com/acme/orders
//	go run github.com/squall-chua/go-boot/cmd/goboot@latest new -grpc github.com/acme/orders
//
// There is one flag because -grpc changes which FILES are written: the buf
// config, a sample proto and an adapter type, plus a codegen step to run
// before the service compiles. Everything else the Scaffold could have made
// optional is lines in a main.go the user owns from the first second, and
// deleting lines needs no flag.
//
// What it writes is cmd/goboot/scaffold, which holds two ordinary package main
// projects that go build ./... compiles like anything else in this module. The
// project a user gets cannot rot into something that does not build, because
// it is something this repository builds.
package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// goFloor is go-boot's OWN minimum Go version, copied from the go.mod beside
// this command. A generated project gets this rather than the version of the
// toolchain that happened to run the Scaffold.
//
// Both halves of that were measured. Writing the toolchain's version pushes
// every teammate onto whatever Go the person who typed `goboot new` had, for a
// reason go-boot does not have. Writing no `go` line at all does not help
// either: `go mod tidy` then fills in the toolchain's version anyway. An
// explicit floor is the only thing that works, and it survives `go mod tidy`.
//
// It is a pinned constant, and TestGoFloorMatchesTheModule is what stops it
// going stale: that test reads ../../go.mod and fails if the two disagree.
const goFloor = "1.25.7"

//go:embed scaffold
var scaffold embed.FS

const usage = `usage: goboot new [-grpc] <module-path>

Writes a new go-boot service into a directory named after the last element of
the module path.

  goboot new github.com/acme/orders          an HTTP service with a database
  goboot new -grpc github.com/acme/orders    the same, plus gRPC and buf
`

// errUsage is the one error printed as the usage text rather than as a
// message, because "goboot: usage: goboot new ..." reads like a failure with a
// manual stapled to it.
var errUsage = errors.New("usage")

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errUsage) || errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "goboot:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "new" {
		return errUsage
	}
	fset := flag.NewFlagSet("new", flag.ContinueOnError)
	fset.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	grpc := fset.Bool("grpc", false, "also write the buf files, a sample proto and the gRPC adapter type")
	if err := fset.Parse(args[1:]); err != nil {
		return err
	}
	if fset.NArg() != 1 {
		return errUsage
	}
	name, written, err := write(".", fset.Arg(0), *grpc)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s/ (%d files)\n\nnext:\n  cd %s\n", name, written, name)
	if *grpc {
		// Before `go mod tidy`, not after. internal/gen is not checked in, so
		// until buf has written it tidy goes looking for the user's own module
		// on the network and fails. The generated README says the same order.
		fmt.Println("  buf generate")
	}
	fmt.Printf("  go mod tidy\n  go run . migrate\n  go run .\n\nRead %s/README.md before you deploy it.\n", name)
	return nil
}

// serviceName is the last element of the module path, and it is also the
// directory, the binary and the `myservice migrate` command name. It is
// validated because it becomes a path this command creates.
func serviceName(module string) (string, error) {
	if module == "" || strings.ContainsAny(module, " \t\n") || strings.HasPrefix(module, "-") {
		return "", fmt.Errorf("%q is not a module path", module)
	}
	name := path.Base(module)
	if name == "." || name == ".." || name == "/" || name == "" {
		return "", fmt.Errorf("%q ends in nothing that can name a directory", module)
	}
	return name, nil
}

// write copies one of the two projects into parent/<service name>,
// substituting as it goes, and returns the directory it made and how many
// files it wrote. The name is derived here rather than passed alongside the
// module path, so the caller and the copy cannot disagree about it.
//
// It refuses to write into an existing directory: this command creates a
// project, it does not merge into one.
func write(parent, module string, grpc bool) (string, int, error) {
	name, err := serviceName(module)
	if err != nil {
		return "", 0, err
	}
	src := "scaffold/http"
	if grpc {
		src = "scaffold/grpc"
	}
	dst := filepath.Join(parent, name)
	if _, err := os.Stat(dst); err == nil {
		return name, 0, fmt.Errorf("%s already exists", dst)
	}

	// Four textual substitutions, and nothing that needs a parser. Every one
	// of them lands inside a string literal, a comment or an import path, so
	// the project compiles before the copy and after it.
	//
	// The first is what lets a project have packages of its own. In THIS
	// repository the feature packages sit at
	// github.com/squall-chua/go-boot/cmd/goboot/scaffold/<project>/internal/...,
	// because that is where they are and go build ./... compiles them there.
	// In the copy they have to be <module>/internal/..., and rewriting the
	// prefix is the whole of it: add internal/orders beside internal/greeting
	// and it is carried over with no change here.
	sub := strings.NewReplacer(
		"github.com/squall-chua/go-boot/cmd/goboot/"+src+"/internal/", module+"/internal/",
		"MYSERVICE_", envPrefix(name),
		"myservice", name,
		"github.com/squall-chua/go-boot/internal/gen", module+"/internal/gen",
	)

	n := 0
	err = fs.WalkDir(scaffold, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dst, filepath.FromSlash(strings.TrimPrefix(p, src)))
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := scaffold.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		return os.WriteFile(out, []byte(sub.Replace(string(b))), 0o644)
	})
	if err != nil {
		return name, n, err
	}
	// go.mod is generated rather than copied: a real go.mod in either project
	// directory would make that directory a nested module, which takes it out
	// of go build ./... and so out of the one check this design rests on.
	// `go mod tidy` fills in the requires.
	n++
	return name, n, os.WriteFile(filepath.Join(dst, "go.mod"),
		[]byte("module "+module+"\n\ngo "+goFloor+"\n"), 0o644)
}

// envPrefix is the name goboot.Load reads environment overrides under.
// Anything that is not a letter or a digit becomes an underscore, because
// my-service is a fine directory name and MY-SERVICE_ is not an environment
// variable.
func envPrefix(name string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, name)
	return strings.ToUpper(clean) + "_"
}
