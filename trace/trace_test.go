package trace_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/trace"
	"github.com/squall-chua/go-boot/web"
)

// spans is the global tracer provider these tests read back. It is global
// because the thing under test is global: otelhttp finds its provider through
// otel.GetTracerProvider, which is a process-wide slot, so NOTHING in this
// file may call t.Parallel.
var (
	spans    *tracetest.SpanRecorder
	recorder *sdktrace.TracerProvider
)

func TestMain(m *testing.M) {
	spans = tracetest.NewSpanRecorder()
	recorder = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	otel.SetTracerProvider(recorder)
	os.Exit(m.Run())
}

// TestTheAccessLogLineCarriesTheTraceID is the first half of what
// DefaultMiddleware is for. The second half is the test below it.
func TestTheAccessLogLineCarriesTheTraceID(t *testing.T) {
	spans.Reset()
	log := newLogSink()

	base := serve(t, func(srv *web.Server) {
		srv.Use(trace.DefaultMiddleware(log.logger)...)
	})
	get(t, base+"/hello/bob")

	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("got %d spans, want 1", len(ended))
	}
	rec := accessLine(t, log)
	if got, want := rec["traceId"], ended[0].SpanContext().TraceID().String(); got != want {
		t.Errorf("access line traceId = %v, want the span's %s", got, want)
	}
	if got, want := rec["spanId"], ended[0].SpanContext().SpanID().String(); got != want {
		t.Errorf("access line spanId = %v, want the span's %s", got, want)
	}
	// The request ID is still there. Tracing adds to the line, it does not
	// replace what web.Logging already wrote.
	if rec["requestId"] == "" || rec["requestId"] == nil {
		t.Errorf("access line lost its requestId: %v", rec)
	}
}

// TestTracingAppendedLaterLosesTheTraceID is why DefaultMiddleware exists at
// all. Use APPENDS, so the two calls below put tracing INNERMOST, and by the
// time the span exists web.Logging has already captured a context without it.
//
// The logger here is trace.WithIDs, the same one DefaultMiddleware hands to
// web.Logging, so the ONLY difference from the test above is the position. A
// user who writes web.DefaultMiddleware(app.Log) instead loses the trace ID
// twice over, for this reason and for the plain logger.
func TestTracingAppendedLaterLosesTheTraceID(t *testing.T) {
	spans.Reset()
	log := newLogSink()

	base := serve(t, func(srv *web.Server) {
		srv.Use(web.DefaultMiddleware(trace.WithIDs(log.logger))...)
		srv.Use(trace.Middleware())
	})
	get(t, base+"/hello/bob")

	// Tracing itself still works. That is what makes this the trap it is:
	// the spans are exported and only the log line is quietly wrong.
	if len(spans.Ended()) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans.Ended()))
	}
	if rec := accessLine(t, log); rec["traceId"] != nil {
		t.Errorf("access line carries traceId %v, so the ordering trap has gone: rewrite this test, not the code", rec["traceId"])
	}
}

// TestSpanNamesComeFromTheRouteTemplate pins the low-cardinality name. A span
// named after the path gives one name per customer ID, which is a bill rather
// than a trace.
func TestSpanNamesComeFromTheRouteTemplate(t *testing.T) {
	spans.Reset()
	log := newLogSink()

	base := serve(t, func(srv *web.Server) {
		srv.Use(trace.DefaultMiddleware(log.logger)...)
	})
	get(t, base+"/hello/bob")

	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("got %d spans, want 1", len(ended))
	}
	if got, want := ended[0].Name(), "GET /hello/{name}"; got != want {
		t.Errorf("span name = %q, want %q", got, want)
	}
	if strings.Contains(ended[0].Name(), "bob") {
		t.Errorf("span name %q carries the path segment, which is the cardinality bug", ended[0].Name())
	}
	if got := attr(ended[0].Attributes(), "http.route"); got != "/hello/{name}" {
		t.Errorf("http.route = %q, want %q", got, "/hello/{name}")
	}
}

// TestAPanickingHandlerStillGetsItsRouteName is the case that gets the naming
// wrong if RouteSpanName renames on the way out rather than in a defer. The
// panic unwinds straight past a plain post-call rename, and web.Recovery sits
// OUTSIDE RouteSpanName, so it catches the panic after the chance to name the
// span has gone.
//
// This is the span that matters most: it is the one holding the 500 someone is
// chasing, and "GET" is the least useful name it could have.
func TestAPanickingHandlerStillGetsItsRouteName(t *testing.T) {
	spans.Reset()
	log := newLogSink()

	srv, err := web.New(web.Config{Addr: "127.0.0.1:0"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv.Use(trace.DefaultMiddleware(log.logger)...)
	srv.HandleFunc("GET /boom/{id}", func(http.ResponseWriter, *http.Request) {
		panic("handler exploded")
	})
	if _, err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})

	resp, err := http.Get("http://" + srv.Addr() + "/boom/7")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}

	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("got %d spans, want 1", len(ended))
	}
	if got, want := ended[0].Name(), "GET /boom/{id}"; got != want {
		t.Errorf("span name = %q, want %q", got, want)
	}
	if got := attr(ended[0].Attributes(), "http.route"); got != "/boom/{id}" {
		t.Errorf("http.route = %q, want %q", got, "/boom/{id}")
	}
}

// TestIsRPCIsExactNotAHeuristic covers all four protocols connect speaks, plus
// the REST call that must NOT be filtered. Getting this wrong in either
// direction is silent: too wide and REST loses its spans, too narrow and every
// RPC gets a redundant parent.
func TestIsRPCIsExactNotAHeuristic(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		connectVsn  string
		want        bool
	}{
		{"grpc", "application/grpc", "", true},
		{"grpc proto", "application/grpc+proto", "", true},
		{"grpc-web", "application/grpc-web+proto", "", true},
		{"connect proto", "application/proto", "1", true},
		{"connect json", "application/json", "1", true},
		{"plain REST POST", "application/json", "", false},
		{"plain REST GET", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest(http.MethodPost, "/x", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			if tc.connectVsn != "" {
				r.Header.Set("Connect-Protocol-Version", tc.connectVsn)
			}
			if got := trace.IsRPC(r); got != tc.want {
				t.Errorf("IsRPC = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTheComponentIsObserveTierAndFlushesOnStop walks the lifecycle. The
// endpoint points at a port nothing is listening on, and Start still succeeds:
// the OTLP exporter connects lazily, so a collector that is down is not a
// service that refuses to start.
func TestTheComponentIsObserveTierAndFlushesOnStop(t *testing.T) {
	// Start installs a provider globally, which is the recorder's slot.
	t.Cleanup(func() { otel.SetTracerProvider(recorder) })

	c, err := trace.New(trace.Config{
		Endpoint:    "http://127.0.0.1:1",
		ServiceName: "orders",
		SampleRatio: 0.5,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Name() != "trace" {
		t.Errorf("Name = %q, want %q", c.Name(), "trace")
	}
	if c.Tier() != goboot.TierObserve {
		t.Errorf("Tier = %v, want TierObserve", c.Tier())
	}
	errc, err := c.Start(t.Context())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if errc != nil {
		t.Error("Start returned a channel; a collector going away must not bring the service down")
	}
	if otel.GetTracerProvider() == recorder {
		t.Error("Start did not install a tracer provider")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// TestNewRejectsASampleRatioThatIsNotAShare catches the config typo that would
// otherwise sample nothing and look like a broken collector.
func TestNewRejectsASampleRatioThatIsNotAShare(t *testing.T) {
	for _, ratio := range []float64{-1, 1.5, 100} {
		if _, err := trace.New(trace.Config{SampleRatio: ratio}, nil); err == nil {
			t.Errorf("New with sampleRatio %v returned no error", ratio)
		}
	}
}

// TestStopBeforeStartIsNotAnError covers the App that fails to start half way
// through and stops what it has.
func TestStopBeforeStartIsNotAnError(t *testing.T) {
	c, err := trace.New(trace.Config{}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Stop(t.Context()); err != nil {
		t.Errorf("Stop before Start: %v", err)
	}
}

// TestABuildWithNoTracingLinksNoTracing reads the optional-subpackage rule
// straight off the linked module list. OTel is +9.4 MB stripped and 19
// indirect modules, so this is the assertion the whole Starter exists for.
//
// The other half matters too: goboot/trace must NOT link otelconnect, or an
// HTTP-only service that traces pays for RPC instrumentation it cannot use.
func TestABuildWithNoTracingLinksNoTracing(t *testing.T) {
	const (
		otelMod    = "go.opentelemetry.io/otel"
		httpMod    = "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
		connectMod = "connectrpc.com/otelconnect"
	)
	for _, tc := range []struct {
		pkg      string
		linked   []string
		unlinked []string
	}{
		{"", nil, []string{otelMod, httpMod, connectMod}},
		{"/web", nil, []string{otelMod, httpMod, connectMod}},
		{"/actuator", nil, []string{otelMod, httpMod, connectMod}},
		{"/db", nil, []string{otelMod, httpMod, connectMod}},
		{"/grpc", nil, []string{otelMod, httpMod, connectMod}},
		// The Preset is the one that had to be split in two: naming
		// goboot/trace here would put OTel in every Preset user's binary,
		// which is why traced.Full is a copy and not an option.
		{"/preset", nil, []string{otelMod, httpMod, connectMod}},
		{"/preset/traced", []string{otelMod, httpMod}, []string{connectMod}},
		{"/examples/http-only", nil, []string{otelMod, httpMod, connectMod}},
		{"/examples/http-actuator-config", nil, []string{otelMod, httpMod, connectMod}},
		{"/examples/full", []string{otelMod, httpMod}, []string{connectMod}},
		{"/trace", []string{otelMod, httpMod}, []string{connectMod}},
		{"/trace/rpc", []string{connectMod}, nil},
	} {
		pkg := "github.com/squall-chua/go-boot" + tc.pkg
		mods := linkedModules(t, pkg)
		for _, want := range tc.linked {
			if !slices.Contains(mods, want) {
				t.Errorf("%s does not link %s: %v", pkg, want, mods)
			}
		}
		for _, notWant := range tc.unlinked {
			if slices.Contains(mods, notWant) {
				t.Errorf("%s links %s, which only its importers should pay for", pkg, notWant)
			}
		}
	}
}

// linkedModules is what ends up in the binary, one module path per line.
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

// serve runs a real web.Server on a free port, so the middleware order under
// test is the one web.Server.Start builds rather than one the test rebuilt.
func serve(t *testing.T, use func(*web.Server)) string {
	t.Helper()
	srv, err := web.New(web.Config{Addr: "127.0.0.1:0"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	use(srv)
	srv.HandleFunc("GET /hello/{name}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello "+r.PathValue("name"))
	})
	if _, err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})
	return "http://" + srv.Addr()
}

func get(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("reading %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
}

func attr(attrs []attribute.KeyValue, key string) string {
	for _, a := range attrs {
		if string(a.Key) == key {
			return a.Value.Emit()
		}
	}
	return ""
}

// logSink collects the JSON log lines so a test can assert on fields rather
// than on the shape of a formatted line.
type logSink struct {
	mu     sync.Mutex
	buf    strings.Builder
	logger *slog.Logger
}

func newLogSink() *logSink {
	s := &logSink{}
	s.logger = slog.New(slog.NewJSONHandler(s, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return s
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// accessLine returns the one line web.Logging wrote for the request.
func accessLine(t *testing.T, s *logSink) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []map[string]any
	for line := range strings.Lines(s.buf.String()) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if m["msg"] == "request" {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d access-log lines, want 1: %s", len(found), s.buf.String())
	}
	return found[0]
}
