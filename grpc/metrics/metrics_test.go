package metrics_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/actuator"
	greetv1 "github.com/squall-chua/go-boot/internal/gen/greet/v1"
	"github.com/squall-chua/go-boot/internal/gen/greet/v1/greetv1connect"

	"github.com/squall-chua/go-boot/grpc"
	"github.com/squall-chua/go-boot/grpc/metrics"
)

// The two procedures the generated service exposes. Written out rather than
// built from a constant, because the label a dashboard is grouped by is what
// these tests are pinning.
const (
	greet  = "/greet.v1.GreetService/Greet"
	stream = "/greet.v1.GreetService/GreetStream"
)

// No test in this file calls t.Parallel. The metrics live on
// prometheus.DefaultRegisterer, which is one package-level registry shared by
// the whole binary, so every test here reads a counter that the others also
// write. Running them one at a time is what makes a before/after delta mean
// what it says.

// greeter is the Service Layer, and it is a copy of the one in grpc_test.go
// rather than an import: that one is in an external test package, which no
// other package can reach.
type greeter struct {
	err   error
	panic bool
	abort bool
}

// grpcGreeter is the adapter type docs/spec.md 4.4 calls mandatory.
type grpcGreeter struct{ svc *greeter }

func (g *grpcGreeter) Greet(_ context.Context, req *connect.Request[greetv1.GreetRequest]) (*connect.Response[greetv1.GreetResponse], error) {
	if g.svc.abort {
		panic(http.ErrAbortHandler)
	}
	if g.svc.panic {
		panic("handler exploded")
	}
	if g.svc.err != nil {
		return nil, g.svc.err // bare: the sanitiser owns what the caller sees
	}
	return connect.NewResponse(&greetv1.GreetResponse{Greeting: "hello " + req.Msg.GetName()}), nil
}

func (g *grpcGreeter) GreetStream(_ context.Context, req *connect.Request[greetv1.GreetStreamRequest], s *connect.ServerStream[greetv1.GreetStreamResponse]) error {
	if g.svc.err != nil {
		return g.svc.err
	}
	return s.Send(&greetv1.GreetStreamResponse{Greeting: "hello " + req.Msg.GetName()})
}

// mount serves the greet service with the options a user writes: the Starter's
// defaults, then this package's appended to them. That order is the one the
// documentation prints, and it is the order that puts connect.WithRecover
// OUTSIDE this package's interceptor.
func mount(t *testing.T, svc *greeter) greetv1connect.GreetServiceClient {
	t.Helper()

	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mux := http.NewServeMux()
	mux.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc: svc},
		append(grpc.DefaultOptions(log), metrics.Options()...)...))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return greetv1connect.NewGreetServiceClient(http.DefaultClient, srv.URL)
}

// reading is one procedure/code pair as the Actuator would serve it: the
// counter's value and the histogram's sample count, read out of
// prometheus.DefaultGatherer rather than out of any variable this package
// holds. Reading the gatherer is the whole claim — a metric registered
// somewhere else is a metric /actuator/metrics cannot see.
type reading struct {
	count    float64
	observed uint64
	seconds  float64
}

func read(t *testing.T, procedure, code string) reading {
	t.Helper()

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	var got reading
	for _, f := range families {
		for _, m := range f.GetMetric() {
			// The label pair is matched inline rather than in a helper,
			// which would have to name a *dto.Metric and so make
			// prometheus/client_model a direct dependency of go-boot for
			// one test's convenience.
			var gotProcedure, gotCode string
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "procedure":
					gotProcedure = l.GetValue()
				case "code":
					gotCode = l.GetValue()
				}
			}
			if gotProcedure != procedure || gotCode != code {
				continue
			}
			switch f.GetName() {
			case "rpc_requests_total":
				got.count = m.GetCounter().GetValue()
			case "rpc_duration_seconds":
				got.observed = m.GetHistogram().GetSampleCount()
				got.seconds = m.GetHistogram().GetSampleSum()
			}
		}
	}
	return got
}

// wantOneMore is the assertion every test below shares: exactly one more call
// counted and exactly one more latency observed for that procedure and code.
func wantOneMore(t *testing.T, before reading, procedure, code string) {
	t.Helper()

	after := read(t, procedure, code)
	if got := after.count - before.count; got != 1 {
		t.Errorf("rpc_requests_total{procedure=%q,code=%q} moved by %v, want 1", procedure, code, got)
	}
	if got := after.observed - before.observed; got != 1 {
		t.Errorf("rpc_duration_seconds{procedure=%q,code=%q} observed %d more, want 1", procedure, code, got)
	}
	if after.seconds <= before.seconds {
		t.Errorf("rpc_duration_seconds{procedure=%q,code=%q} sum did not move: %v", procedure, code, after.seconds)
	}
}

// TestASuccessfulRpcIsCountedAndTimed is the acceptance criterion of #41 in
// one test: a count and a latency, by procedure, readable from the registry
// /actuator/metrics serves.
func TestASuccessfulRpcIsCountedAndTimed(t *testing.T) {
	before := read(t, greet, "ok")

	client := mount(t, &greeter{})
	if _, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"})); err != nil {
		t.Fatalf("Greet: %v", err)
	}

	wantOneMore(t, before, greet, "ok")
}

// TestAFailedRpcCarriesTheCodeTheCallerGot. The handler returns a bare error
// and the sanitiser turns it into CodeUnknown, so "unknown" is what the caller
// sees and "unknown" is what an operator counting failures must find. This
// interceptor sits INSIDE the sanitiser and so reads the raw error, which is
// why the label is connect.CodeOf and not the sanitised error's code.
func TestAFailedRpcCarriesTheCodeTheCallerGot(t *testing.T) {
	before := read(t, greet, "unknown")

	client := mount(t, &greeter{err: errors.New("boom")})
	_, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"}))
	if err == nil {
		t.Fatal("Greet succeeded, want an error")
	}
	if got := connect.CodeOf(err); got != connect.CodeUnknown {
		t.Fatalf("the caller got code %v, want %v", got, connect.CodeUnknown)
	}

	wantOneMore(t, before, greet, "unknown")
}

// TestAHandlerErrorKeepsItsOwnCode. A *connect.Error passes through the
// sanitiser untouched, so the label must be the code the handler chose rather
// than a flattened "unknown".
func TestAHandlerErrorKeepsItsOwnCode(t *testing.T) {
	before := read(t, greet, "invalid_argument")

	svc := &greeter{err: connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))}
	client := mount(t, svc)
	if _, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{})); err == nil {
		t.Fatal("Greet succeeded, want an error")
	}

	wantOneMore(t, before, greet, "invalid_argument")
}

// TestAPanickingRpcIsCounted is the one that fixes the order. connect's
// WithRecover is an interceptor, and grpc.DefaultOptions puts it first, so it
// wraps this package's interceptor: a panic unwinds PAST any code that runs
// after next(), and the RPC would go uncounted. The caller is told
// CodeInternal, so that is the label.
func TestAPanickingRpcIsCounted(t *testing.T) {
	before := read(t, greet, "internal")

	client := mount(t, &greeter{panic: true})
	_, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"}))
	if err == nil {
		t.Fatal("Greet succeeded, want an error")
	}
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Fatalf("the caller got code %v, want %v", got, connect.CodeInternal)
	}

	wantOneMore(t, before, greet, "internal")
}

// TestAStreamingRpcIsCountedOnce. Streaming is supported by the Starter, so a
// stream that is never counted is a hole in the same answer. One count for the
// whole stream, not one per message.
func TestAStreamingRpcIsCountedOnce(t *testing.T) {
	before := read(t, stream, "ok")

	client := mount(t, &greeter{})
	s, err := client.GreetStream(t.Context(), connect.NewRequest(&greetv1.GreetStreamRequest{Name: "ada"}))
	if err != nil {
		t.Fatalf("GreetStream: %v", err)
	}
	for s.Receive() { //nolint:revive // draining the stream is the point
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	wantOneMore(t, before, stream, "ok")
}

// TestOptionsCanBeMountedOnMoreThanOneService. connect options are per
// service, so Options is called once per mount. Registration therefore cannot
// live inside it: a second call would re-register on the default registry and
// panic. The assertion is that nothing panics and both mounts serve, so there
// is nothing to compare — a t.Fatal from the second mount is the failure.
func TestOptionsCanBeMountedOnMoreThanOneService(t *testing.T) {
	for i := range 2 {
		client := mount(t, &greeter{})
		if _, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"})); err != nil {
			t.Fatalf("mount %d: %v", i, err)
		}
	}
}

// TestTheLabelsAreTheDocumentedOnes pins the metric names and the label names
// themselves. docs/spec.md 4.4 prints them, and an operator's dashboard breaks
// on a rename exactly as a compile breaks on a renamed function.
func TestTheLabelsAreTheDocumentedOnes(t *testing.T) {
	client := mount(t, &greeter{})
	if _, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"})); err != nil {
		t.Fatalf("Greet: %v", err)
	}

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	found := map[string]bool{}
	for _, f := range families {
		name := f.GetName()
		if name != "rpc_requests_total" && name != "rpc_duration_seconds" {
			continue
		}
		found[name] = true
		for _, m := range f.GetMetric() {
			var names []string
			for _, l := range m.GetLabel() {
				names = append(names, l.GetName())
			}
			// Prometheus sorts label names, so the pair arrives as
			// code, procedure whichever order they were declared in.
			if want := []string{"code", "procedure"}; !slices.Equal(names, want) {
				t.Errorf("%s has labels %v, want %v", name, names, want)
			}
		}
	}
	for _, want := range []string{"rpc_requests_total", "rpc_duration_seconds"} {
		if !found[want] {
			t.Errorf("%s is not in the default registry", want)
		}
	}
}

// TestTheMetricsReachTheActuatorEndpoint is #41's acceptance criterion read
// end to end: count and latency by procedure, observable from ONE endpoint.
// Every other test here reads prometheus.DefaultGatherer, which proves the
// registration but not the serving. This one calls an RPC and then scrapes
// /actuator/metrics for it.
//
// Mounting the Actuator here costs nothing in the golden file: goboot is
// already in this package's test graph through goboot/grpc, and the Actuator
// links no module goboot/grpc/metrics does not link already. The direction
// that would cost something is the other one, and assertion 2 of
// .github/check-imports.sh holds it — goboot/actuator must never reach this
// package, or every Actuator user pays for RPC metrics they did not ask for.
func TestTheMetricsReachTheActuatorEndpoint(t *testing.T) {
	app, err := goboot.New(goboot.Config{
		Lifecycle: goboot.LifecycleConfig{DrainDelay: time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The whitelist is what it is: "metrics" has to be named, or the endpoint
	// answers 404. That rule is not relaxed for this package.
	act, err := actuator.New(actuator.Config{Expose: []string{"metrics"}}, app)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	act.MountOn(mux)

	client := mount(t, &greeter{})
	if _, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"})); err != nil {
		t.Fatalf("Greet: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/actuator/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/actuator/metrics = %d, want 200", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`rpc_requests_total{code="ok",procedure="` + greet + `"}`,
		`rpc_duration_seconds_count{code="ok",procedure="` + greet + `"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/actuator/metrics does not carry %s", want)
		}
	}
}

// TestAnAbortedConnectionIsNotCounted. http.ErrAbortHandler is a handler
// saying "drop this connection quietly", and every layer around this one
// already treats it that way: connect re-panics it instead of building an
// error, web.Recovery re-panics it instead of writing a 500, and web.Logging
// writes no access line for it at all. Counting it as an internal error would
// make this the one place that calls a deliberate abort a server failure.
//
// The panic must still reach net/http, so it is re-panicked, not swallowed —
// which is why this test reads the counter rather than the caller's error.
func TestAnAbortedConnectionIsNotCounted(t *testing.T) {
	before := read(t, greet, "internal")

	client := mount(t, &greeter{abort: true})
	// The connection is dropped, so the client gets a transport error rather
	// than a code. What it gets is not the claim; the counter is.
	_, _ = client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "ada"}))

	if got := read(t, greet, "internal"); got.count != before.count {
		t.Errorf("an aborted connection was counted as internal: %v became %v", before.count, got.count)
	}
}
