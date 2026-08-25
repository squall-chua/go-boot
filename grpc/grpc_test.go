package grpc_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/grpc"
	greetv1 "github.com/squall-chua/go-boot/internal/gen/greet/v1"
	"github.com/squall-chua/go-boot/internal/gen/greet/v1/greetv1connect"
	"github.com/squall-chua/go-boot/web"
)

// leak is the string #12 measured going out on the wire from a bare error,
// host and username and all. Every sanitiser test uses this one so a failure
// reads as the real thing rather than as "test error".
const leak = `pq: password authentication failed for user "app" at 10.0.0.5:5432`

// greeter is the Service Layer. It knows nothing about gRPC, which is the
// point of the adapter below.
type greeter struct {
	err   error
	panic bool
}

func (g *greeter) Greet(_ context.Context, name string) (string, error) {
	if g.panic {
		panic("handler exploded")
	}
	if g.err != nil {
		return "", g.err
	}
	return "hello " + name, nil
}

// grpcGreeter is the gRPC Transport, and docs/spec.md 4.4 calls it mandatory
// rather than stylistic. The generated interface wants
// Greet(ctx, *connect.Request[...]) while the Service Layer already owns the
// name Greet, so embedding gives a confusing ambiguity error instead. A
// separate thin type is the way out.
type grpcGreeter struct{ svc *greeter }

func (g *grpcGreeter) Greet(ctx context.Context, req *connect.Request[greetv1.GreetRequest]) (*connect.Response[greetv1.GreetResponse], error) {
	out, err := g.svc.Greet(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err // deliberately bare: the sanitiser is what stands between this and the caller
	}
	goboot.LoggerFrom(ctx).Info("greeting", "name", req.Msg.GetName())
	return connect.NewResponse(&greetv1.GreetResponse{Greeting: out}), nil
}

func (g *grpcGreeter) GreetStream(ctx context.Context, req *connect.Request[greetv1.GreetStreamRequest], stream *connect.ServerStream[greetv1.GreetStreamResponse]) error {
	if g.svc.err != nil {
		return g.svc.err
	}
	for i := range 3 {
		msg := &greetv1.GreetStreamResponse{Greeting: fmt.Sprintf("hello %s %d", req.Msg.GetName(), i)}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
	return nil
}

// syncBuf is a bytes.Buffer a slog handler and a test can both touch. The
// server writes from its own goroutine.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// mount builds a running App with one connect service on it and returns the
// base URL and the log. opts is what the mount is given, so a test can drop
// an entry from DefaultOptions and see what changes.
func mount(t *testing.T, svc *greeter, opts func(log *slog.Logger) []connect.HandlerOption) (string, *syncBuf) {
	t.Helper()

	logs := &syncBuf{}
	log := slog.New(slog.NewJSONHandler(logs, nil))

	app, err := goboot.New(goboot.Config{
		Lifecycle: goboot.LifecycleConfig{DrainDelay: time.Nanosecond},
		Log:       goboot.LogConfig{Level: "debug"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	app.Log = log

	srv := web.New(web.Config{Addr: "127.0.0.1:0"}, log)
	srv.Use(web.DefaultMiddleware(log)...)
	// The whole of acceptance criterion 1: the generated constructor's two
	// return values go straight into Handle. No adapter, no second port.
	srv.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc: svc}, opts(log)...))
	app.Add(srv)

	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return "http://" + srv.Addr(), logs
}

func defaults(log *slog.Logger) []connect.HandlerOption { return grpc.DefaultOptions(log) }

// TestGeneratedConstructorMountsOnTheHTTPListener is the tracer bullet: one
// port, no adapter between connect and web, and a real round trip.
func TestGeneratedConstructorMountsOnTheHTTPListener(t *testing.T) {
	t.Parallel()
	base, _ := mount(t, &greeter{}, defaults)

	client := greetv1connect.NewGreetServiceClient(http.DefaultClient, base)
	res, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"}))
	if err != nil {
		t.Fatalf("Greet: %v", err)
	}
	if got := res.Msg.GetGreeting(); got != "hello ada" {
		t.Errorf("got %q, want %q", got, "hello ada")
	}
}

// TestDefaultOptionsIsThreeEditableEntries pins the slice's shape. The three
// are named in docs/spec.md 4.4 and the count is what a user edits.
func TestDefaultOptionsIsThreeEditableEntries(t *testing.T) {
	t.Parallel()
	if n := len(grpc.DefaultOptions(slog.New(slog.DiscardHandler))); n != 3 {
		t.Errorf("DefaultOptions has %d entries, want 3", n)
	}
}

// TestABareErrorPutsNoTextOnTheWire is the one that matters. The second half
// removes the sanitiser and shows the same string reaching the caller, so the
// test fails if the sanitiser ever becomes a no-op.
func TestABareErrorPutsNoTextOnTheWire(t *testing.T) {
	t.Parallel()

	base, _ := mount(t, &greeter{err: errors.New(leak)}, defaults)
	client := greetv1connect.NewGreetServiceClient(http.DefaultClient, base)
	_, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"}))
	if err == nil {
		t.Fatal("Greet succeeded, want an error")
	}
	if strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "10.0.0.5") {
		t.Errorf("the handler's error text reached the caller: %v", err)
	}
	if got := connect.CodeOf(err); got != connect.CodeUnknown {
		t.Errorf("got code %v, want %v", got, connect.CodeUnknown)
	}

	// Without the sanitiser. Everything else is the same.
	noSanitiser := func(log *slog.Logger) []connect.HandlerOption {
		all := grpc.DefaultOptions(log)
		return append(all[:1:1], all[2:]...)
	}
	base, _ = mount(t, &greeter{err: errors.New(leak)}, noSanitiser)
	client = greetv1connect.NewGreetServiceClient(http.DefaultClient, base)
	_, err = client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"}))
	if err == nil {
		t.Fatal("Greet succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "password authentication failed") {
		t.Errorf("drop the sanitiser and the text should reach the caller, but got: %v", err)
	}
}

// TestTheSanitiserLogsTheProcedureAndTheRealError: the caller is told
// nothing, so the log line is the only place the failure is written down.
// The access log cannot do it — see the 200 test below.
func TestTheSanitiserLogsTheProcedureAndTheRealError(t *testing.T) {
	t.Parallel()
	base, logs := mount(t, &greeter{err: errors.New(leak)}, defaults)

	client := greetv1connect.NewGreetServiceClient(http.DefaultClient, base)
	if _, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"})); err == nil {
		t.Fatal("Greet succeeded, want an error")
	}

	out := logs.String()
	for _, want := range []string{"rpc failed", greetv1connect.GreetServiceGreetProcedure, "password authentication failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log is missing %q:\n%s", want, out)
		}
	}
	// The requestId is what joins this line to the access line for the same
	// request, and the access line says 200, so without it the failure cannot
	// be found from the access log at all.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(line, `"msg":"rpc failed"`) && !strings.Contains(line, `"requestId"`) {
			t.Errorf("the sanitiser line is not request-scoped: %s", line)
		}
	}
}

// TestTheSanitiserFallsBackToTheLoggerItWasGiven: mounted on a bare mux with
// no web.DefaultMiddleware, there is no request-scoped logger, and the line
// must still land on the App's logger rather than on slog.Default().
func TestTheSanitiserFallsBackToTheLoggerItWasGiven(t *testing.T) {
	t.Parallel()

	logs := &syncBuf{}
	log := slog.New(slog.NewJSONHandler(logs, nil))

	mux := http.NewServeMux()
	mux.Handle(greetv1connect.NewGreetServiceHandler(
		&grpcGreeter{svc: &greeter{err: errors.New(leak)}}, grpc.DefaultOptions(log)...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := greetv1connect.NewGreetServiceClient(http.DefaultClient, srv.URL)
	if _, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"})); err == nil {
		t.Fatal("Greet succeeded, want an error")
	}
	if !strings.Contains(logs.String(), "password authentication failed") {
		t.Errorf("the fallback logger got nothing:\n%s", logs.String())
	}
}

// TestAPanickingHandlerReturnsACleanInternalError. Without recovery the panic
// reaches net/http, which resets the stream and tells the caller nothing at
// all.
func TestAPanickingHandlerReturnsACleanInternalError(t *testing.T) {
	t.Parallel()
	base, logs := mount(t, &greeter{panic: true}, defaults)

	client := greetv1connect.NewGreetServiceClient(http.DefaultClient, base)
	_, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"}))
	if err == nil {
		t.Fatal("Greet succeeded, want an error")
	}
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("got code %v, want %v", got, connect.CodeInternal)
	}
	if strings.Contains(err.Error(), "exploded") {
		t.Errorf("the panic value reached the caller: %v", err)
	}
	if !strings.Contains(logs.String(), "handler exploded") {
		t.Errorf("the panic was not logged:\n%s", logs.String())
	}
}

// TestAMissingProtocolHeaderIsRejectedWithItsOwnFix. The rejection has to
// name the header, because the first person to hit it will be holding a curl
// command and nothing else.
func TestAMissingProtocolHeaderIsRejectedWithItsOwnFix(t *testing.T) {
	t.Parallel()
	base, _ := mount(t, &greeter{}, defaults)

	url := base + greetv1connect.GreetServiceGreetProcedure
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(`{"name":"ada"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("the call succeeded without the protocol header: %s", body)
	}
	if !strings.Contains(string(body), "Connect-Protocol-Version") {
		t.Errorf("the rejection does not name the header that fixes it: %s", body)
	}
}

// TestAGRPCClientIsUnaffectedByTheProtocolHeader. The requirement is a
// Connect-protocol rule, so gRPC must be untouched by it. This is also the
// one-cleartext-port claim in ADR 0006, run.
func TestAGRPCClientIsUnaffectedByTheProtocolHeader(t *testing.T) {
	t.Parallel()
	base, _ := mount(t, &greeter{}, defaults)

	client := greetv1connect.NewGreetServiceClient(h2cClient(), base, connect.WithGRPC())
	res, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"}))
	if err != nil {
		t.Fatalf("a plain gRPC call was rejected: %v", err)
	}
	if got := res.Msg.GetGreeting(); got != "hello ada" {
		t.Errorf("got %q, want %q", got, "hello ada")
	}
}

// TestTheRequestScopedLoggerReachesTheRPCHandler: no gRPC interceptor of
// go-boot's own does this. web.DefaultMiddleware already ran under the shared
// listener, so the handler's line carries the same requestId as the access
// line.
func TestTheRequestScopedLoggerReachesTheRPCHandler(t *testing.T) {
	t.Parallel()
	base, logs := mount(t, &greeter{}, defaults)

	client := greetv1connect.NewGreetServiceClient(http.DefaultClient, base)
	if _, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"})); err != nil {
		t.Fatalf("Greet: %v", err)
	}

	out := logs.String()
	if !strings.Contains(out, `"msg":"greeting"`) {
		t.Fatalf("the handler's own line is missing:\n%s", out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(line, `"msg":"greeting"`) && !strings.Contains(line, `"requestId"`) {
			t.Errorf("the handler's logger is not the request-scoped one: %s", line)
		}
	}
}

// TestTheAccessLogRecords200ForAFailedRPC. Measured in #12: the status rides
// in trailers, so gRPC and gRPC-Web both show 200. It is documented rather
// than fixed, and this test is what stops the documentation going stale.
func TestTheAccessLogRecords200ForAFailedRPC(t *testing.T) {
	t.Parallel()
	base, logs := mount(t, &greeter{err: errors.New(leak)}, defaults)

	client := greetv1connect.NewGreetServiceClient(h2cClient(), base, connect.WithGRPC())
	if _, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"})); err == nil {
		t.Fatal("Greet succeeded, want an error")
	}

	var access string
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if strings.Contains(line, `"msg":"request"`) {
			access = line
		}
	}
	if access == "" {
		t.Fatalf("no access log line:\n%s", logs.String())
	}
	if !strings.Contains(access, `"status":200`) {
		t.Errorf("the documented 200 is no longer what happens: %s", access)
	}
}

// TestStreamingWorks. docs/spec.md 4.4 says streaming is supported because it
// costs nothing, and a claim in that file is meant to have a test under it.
func TestStreamingWorks(t *testing.T) {
	t.Parallel()
	base, _ := mount(t, &greeter{}, defaults)

	client := greetv1connect.NewGreetServiceClient(http.DefaultClient, base)
	stream, err := client.GreetStream(t.Context(), connect.NewRequest(&greetv1.GreetStreamRequest{Name: "ada"}))
	if err != nil {
		t.Fatalf("GreetStream: %v", err)
	}
	defer stream.Close()

	var got []string
	for stream.Receive() {
		got = append(got, stream.Msg().GetGreeting())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	want := []string{"hello ada 0", "hello ada 1", "hello ada 2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestAStreamingHandlerAlsoSanitises. The interceptor covers streaming as
// well as unary, because a streaming handler leaks the same string.
func TestAStreamingHandlerAlsoSanitises(t *testing.T) {
	t.Parallel()
	base, _ := mount(t, &greeter{err: errors.New(leak)}, defaults)

	client := greetv1connect.NewGreetServiceClient(http.DefaultClient, base)
	stream, err := client.GreetStream(t.Context(), connect.NewRequest(&greetv1.GreetStreamRequest{Name: "ada"}))
	if err == nil {
		defer stream.Close()
		for stream.Receive() { //nolint:revive // drain to reach the error
		}
		err = stream.Err()
	}
	if err == nil {
		t.Fatal("the stream succeeded, want an error")
	}
	if strings.Contains(err.Error(), "password") {
		t.Errorf("a streaming handler leaked its error text: %v", err)
	}
}

// h2cClient speaks HTTP/2 over cleartext, which is what the gRPC protocol
// needs. Since Go 1.24 net/http does this itself, so there is no
// golang.org/x/net/http2 here and no extra module in go.sum.
func h2cClient() *http.Client {
	t := &http.Transport{Protocols: &http.Protocols{}}
	t.Protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: t}
}

// TestEmbeddingTheServiceLayerDoesNotCompile keeps the README's two compiler
// errors from rotting. The README tells a reader to write the gRPC Transport
// as its own type and quotes what the compiler says if they embed the Service
// Layer instead; this builds testdata/embedding, which is written to fail, and
// checks the errors are still those.
func TestEmbeddingTheServiceLayerDoesNotCompile(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", os.DevNull, "./testdata/embedding")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("testdata/embedding compiled, so the README's warning is stale:\n%s", out)
	}
	// The first is readable. The second is the one the README calls confusing,
	// and it appears only because badB also embeds the generated
	// Unimplemented type, which is what a user does for forward compatibility.
	for _, want := range []string{
		"(wrong type for method Greet)",
		"(ambiguous selector *badB.Greet)",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the compiler no longer says %q, so the README is wrong:\n%s", want, out)
		}
	}
}

// TestTheOptionalSubpackagesAreNotLinkedByTheTransport reads the
// optional-subpackage rule straight off the linked module list. Go links by
// import, so the only proof that goboot/grpc/health and goboot/grpc/reflection
// are free for a service that wants neither is that neither module appears in
// the Transport's own dependency list.
//
// The other half matters too: each subpackage must link ITS module and not
// the other one, or importing health would quietly pay for reflection.
func TestTheOptionalSubpackagesAreNotLinkedByTheTransport(t *testing.T) {
	t.Parallel()

	const (
		healthMod     = "connectrpc.com/grpchealth"
		reflectionMod = "connectrpc.com/grpcreflect"
	)
	for _, tc := range []struct {
		pkg      string
		linked   []string
		unlinked []string
	}{
		{"grpc", nil, []string{healthMod, reflectionMod}},
		{"grpc/health", []string{healthMod}, []string{reflectionMod}},
		{"grpc/reflection", []string{reflectionMod}, []string{healthMod}},
	} {
		mods := linkedModules(t, "github.com/squall-chua/go-boot/"+tc.pkg)
		for _, want := range tc.linked {
			if !slices.Contains(mods, want) {
				t.Errorf("goboot/%s does not link %s: %v", tc.pkg, want, mods)
			}
		}
		for _, notWant := range tc.unlinked {
			if slices.Contains(mods, notWant) {
				t.Errorf("goboot/%s links %s, which only its importers should pay for", tc.pkg, notWant)
			}
		}
	}
}

// linkedModules is what ends up in the binary, one module path per line. The
// build dependencies only: test imports are a separate question, answered by
// assertion 1 of the import-leak check in docs/spec.md 8.1.
func linkedModules(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(),
		"go", "list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}", pkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pkg, err, out)
	}
	var mods []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" && !slices.Contains(mods, line) {
			mods = append(mods, line)
		}
	}
	return mods
}
