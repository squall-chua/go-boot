package main

import (
	"bytes"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestWriteProducesAProjectThatParses is the check the whole Scaffold rests
// on. The two projects under scaffold/ are compiled by go build ./..., so the
// only thing a copy can break is the substitution: a replacement that lands
// outside a string literal, a comment or an import path leaves Go that no
// longer parses — or, short of that, Go that no longer sorts.
func TestWriteProducesAProjectThatParses(t *testing.T) {
	for _, grpc := range []bool{false, true} {
		dir := t.TempDir()
		if _, _, err := write(dir, "github.com/acme/orders", grpc); err != nil {
			t.Fatalf("grpc=%v: %v", grpc, err)
		}
		for _, p := range goFiles(t, filepath.Join(dir, "orders")) {
			if _, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution); err != nil {
				t.Errorf("grpc=%v: %v", grpc, err)
				continue
			}
			// Parsing is not enough. The substitutions rewrite IMPORT PATHS,
			// and a rewritten path sorts differently from the one written
			// here — so a project that parses can still land unsorted, and
			// the user's first `gofmt -l` reports files they never wrote.
			// Keeping each rewritten import in a group of its own is what
			// avoids that, and this is the check that says so.
			src, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			want, err := format.Source(src)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(src, want) {
				t.Errorf("grpc=%v: %s is not gofmt-clean as written", grpc, p)
			}
		}
	}
}

// TestWriteLeavesNoPlaceholderBehind pins the substitutions themselves. A
// missed one ships a service that reads ORDERS_ config under the name
// MYSERVICE_, which nothing else here would catch.
func TestWriteLeavesNoPlaceholderBehind(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := write(dir, "github.com/acme/orders", true); err != nil {
		t.Fatal(err)
	}
	for _, p := range allFiles(t, filepath.Join(dir, "orders")) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"myservice", "MYSERVICE_", "squall-chua/go-boot/internal/gen", "cmd/goboot/scaffold"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s still contains %q", p, bad)
			}
		}
	}
}

// TestTheGeneratedFileSet is the whole promise of "what it writes", and the
// -grpc flag's whole reason for existing: it changes which FILES appear.
func TestTheGeneratedFileSet(t *testing.T) {
	base := []string{"README.md", "app.yaml", "go.mod",
		"internal/greeting/greeting.go", "internal/greeting/greeting_test.go",
		"internal/greeting/entity/entity.go", "internal/greeting/entity/postgres.go",
		"internal/greeting/rest/rest.go",
		"internal/transport/transport.go", "internal/transport/transport_test.go",
		"main.go", "migrations/00001_greeting.sql", "routes.go"}
	grpcOnly := []string{"buf.gen.yaml", "buf.yaml", "internal/greeting/rpc/rpc.go", "proto/greet/v1/greet.proto"}

	for _, tc := range []struct {
		grpc bool
		want []string
	}{
		{false, base},
		{true, append(append([]string{}, base...), grpcOnly...)},
	} {
		dir := t.TempDir()
		name, n, err := write(dir, "github.com/acme/orders", tc.grpc)
		if err != nil {
			t.Fatal(err)
		}
		if name != "orders" {
			t.Fatalf("write made %q, want orders", name)
		}
		got := relFiles(t, filepath.Join(dir, "orders"))
		slices.Sort(got)
		slices.Sort(tc.want)
		if !slices.Equal(got, tc.want) {
			t.Errorf("grpc=%v: wrote %v, want %v", tc.grpc, got, tc.want)
		}
		if n != len(tc.want) {
			t.Errorf("grpc=%v: reported %d files, wrote %d", tc.grpc, n, len(tc.want))
		}
	}
}

// TestTheTwoProjectsShareWhatTheyShare is the price of holding two complete
// projects rather than one plus an overlay. Both have to compile, and a
// compilable project needs its own app.yaml and migrations beside the
// //go:embed lines that name them — so those files are duplicated, and only a
// test keeps the copies together.
//
// Two files are byte for byte equal. The other three legitimately differ,
// because the gRPC project adds to them, so what is asserted there is that
// the HTTP file is still a SUBSET: every one of its lines is present, in
// order, in the gRPC one. That is what catches the real drift — editing one
// of the pair and forgetting the other — and it is most of the duplication,
// which byte equality alone would have left unchecked.
func TestTheTwoProjectsShareWhatTheyShare(t *testing.T) {
	for _, name := range []string{"app.yaml", "migrations/00001_greeting.sql",
		"internal/greeting/greeting.go", "internal/greeting/greeting_test.go",
		"internal/greeting/entity/entity.go", "internal/greeting/entity/postgres.go",
		"internal/greeting/rest/rest.go",
		"internal/transport/transport.go", "internal/transport/transport_test.go"} {
		if a, b := readPair(t, name); a != b {
			t.Errorf("scaffold/http/%s and scaffold/grpc/%s have drifted apart", name, name)
		}
	}
	for _, name := range []string{"main.go", "routes.go", "README.md"} {
		a, b := readPair(t, name)
		if line, ok := firstLineNotIn(a, b); !ok {
			t.Errorf("scaffold/http/%s has a line scaffold/grpc/%s does not: %q", name, name, line)
		}
	}
}

func readPair(t *testing.T, name string) (string, string) {
	t.Helper()
	return readProject(t, "http", name), readProject(t, "grpc", name)
}

// readProject reads one project's file with its OWN feature-package import
// prefix rewritten to a placeholder, which is the first substitution write
// applies on copy. Without it the pair differs on the one line that names the
// directory each project lives in — which is not drift, and is the only
// difference the feature packages introduced.
func readProject(t *testing.T, project, name string) string {
	b, err := scaffold.ReadFile("scaffold/" + project + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b),
		"github.com/squall-chua/go-boot/cmd/goboot/scaffold/"+project+"/internal/",
		"<module>/internal/")
}

// firstLineNotIn reports the first non-blank line of a that is not in b at or
// after the line matching a's previous one. In order, because two files with
// the same lines shuffled are still drift.
func firstLineNotIn(a, b string) (string, bool) {
	rest := strings.Split(b, "\n")
	for _, line := range strings.Split(a, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		i := slices.Index(rest, line)
		if i < 0 {
			return line, false
		}
		rest = rest[i+1:]
	}
	return "", true
}

// TestWriteRefusesAnExistingDirectory: this command creates a project. It
// does not merge into one, and silently overwriting a main.go somebody wrote
// is the one unrecoverable thing it could do.
func TestWriteRefusesAnExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "orders"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := write(dir, "github.com/acme/orders", false); err == nil {
		t.Fatal("wrote into an existing directory")
	}
}

// TestRunRejectsWhatIsNotAnInvocation covers the entry point: everything that
// is not `new <module-path>` has to come back as usage, not as a project
// written into the wrong place.
func TestRunRejectsWhatIsNotAnInvocation(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"init", "github.com/acme/orders"},
		{"new"},
		{"new", "github.com/acme/orders", "extra"},
		{"new", "-badflag", "github.com/acme/orders"},
	} {
		if err := run(args); err == nil {
			t.Errorf("run(%q) returned no error", args)
		}
	}
}

// TestGoFloorMatchesTheModule is what lets goFloor be a constant. A pinned
// number with nothing reading the source of truth is exactly the staleness
// this design avoids everywhere else, so the pin is checked rather than
// trusted: the generated go.mod must name go-boot's floor, not the toolchain
// that ran the Scaffold.
func TestGoFloorMatchesTheModule(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	want := ""
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "go "); ok {
			want = strings.TrimSpace(rest)
			break
		}
	}
	if want == "" {
		t.Fatal("../../go.mod has no go directive, so the floor cannot be checked at all")
	}
	if want != goFloor {
		t.Errorf("goFloor is %q, but go-boot's go.mod says %q", goFloor, want)
	}
}

// TestGeneratedGoModNamesTheFloor pins the file the constant exists for. A
// project that names the toolchain's version drags every teammate onto it.
func TestGeneratedGoModNamesTheFloor(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := write(dir, "github.com/acme/orders", false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "orders", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), "module github.com/acme/orders\n\ngo "+goFloor+"\n"; got != want {
		t.Errorf("go.mod is %q, want %q", got, want)
	}
}

func TestServiceName(t *testing.T) {
	for _, tc := range []struct{ module, want string }{
		{"github.com/acme/orders", "orders"},
		{"orders", "orders"},
		{"github.com/acme/order-service", "order-service"},
	} {
		got, err := serviceName(tc.module)
		if err != nil || got != tc.want {
			t.Errorf("serviceName(%q) = %q, %v; want %q", tc.module, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "  ", "-grpc", "/"} {
		if _, err := serviceName(bad); err == nil {
			t.Errorf("serviceName(%q) returned no error", bad)
		}
	}
}

// TestEnvPrefixIsUsable: a directory may be called order-service, and
// ORDER-SERVICE_ is not an environment variable.
func TestEnvPrefixIsUsable(t *testing.T) {
	if got := envPrefix("order-service"); got != "ORDER_SERVICE_" {
		t.Errorf("envPrefix(order-service) = %q", got)
	}
}

func goFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, p := range allFiles(t, root) {
		if strings.HasSuffix(p, ".go") {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s: no Go files, so nothing was checked", root)
	}
	return out
}

func allFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func relFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, p := range allFiles(t, root) {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}
