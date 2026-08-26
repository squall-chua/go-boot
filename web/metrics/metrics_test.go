package metrics_test

import (
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/actuator"
	"github.com/squall-chua/go-boot/web"
	"github.com/squall-chua/go-boot/web/metrics"

	"github.com/prometheus/client_golang/prometheus"
)

// No test in this file calls t.Parallel. The metrics live on
// prometheus.DefaultRegisterer, which is one package-level registry shared by
// the whole binary, so every test here reads a counter the others also write.
// Running them one at a time is what makes a before/after delta mean what it
// says. Each test uses a route of its own for the same reason.

// discardLog is the logger the default middleware needs. Nothing here asserts
// on a log line — the access log is goboot/web's own test.
func discardLog() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// serve mounts pattern behind mw, outermost first, and returns the base URL.
// The error log is discarded so a deliberate panic does not dump a stack
// trace over the test output.
func serve(t *testing.T, mw []web.Middleware, pattern string, h http.HandlerFunc) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(pattern, h)
	var wrapped http.Handler = mux
	for i := len(mw) - 1; i >= 0; i-- {
		wrapped = mw[i](wrapped)
	}
	ts := httptest.NewUnstartedServer(wrapped)
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
	ts.Start()
	t.Cleanup(ts.Close)
	return ts.URL
}

// appended is the wiring the documentation prints: the default set, with this
// package's middleware appended. Use appends, so it lands INNERMOST, inside
// Recovery — which is the hard placement, and so the one most tests use.
func appended() []web.Middleware {
	return append(web.DefaultMiddleware(discardLog()), metrics.Middleware)
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// reading is one route/status pair as the Actuator would serve it: the
// counter's value and the histogram's sample count, read out of
// prometheus.DefaultGatherer rather than out of any variable this package
// holds. Reading the gatherer is the whole claim — a metric registered
// somewhere else is a metric /actuator/metrics cannot see.
type reading struct {
	count    float64
	observed uint64
	seconds  float64
}

func read(t *testing.T, route string, status int) reading {
	t.Helper()

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	want := strconv.Itoa(status)
	var got reading
	for _, f := range families {
		for _, m := range f.GetMetric() {
			// The label pair is matched inline rather than in a helper,
			// which would have to name a *dto.Metric and so make
			// prometheus/client_model a direct dependency of go-boot for
			// one test's convenience.
			var gotRoute, gotStatus string
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "route":
					gotRoute = l.GetValue()
				case "status":
					gotStatus = l.GetValue()
				}
			}
			if gotRoute != route || gotStatus != want {
				continue
			}
			switch f.GetName() {
			case "http_requests_total":
				got.count = m.GetCounter().GetValue()
			case "http_request_duration_seconds":
				got.observed = m.GetHistogram().GetSampleCount()
				got.seconds = m.GetHistogram().GetSampleSum()
			}
		}
	}
	return got
}

// wantMore is the assertion most tests below share: exactly n more requests
// counted and exactly n more latencies observed for that route and status.
func wantMore(t *testing.T, before reading, route string, status, n int) {
	t.Helper()

	after := read(t, route, status)
	if got := after.count - before.count; got != float64(n) {
		t.Errorf("http_requests_total{route=%q,status=%q} moved by %v, want %d",
			route, strconv.Itoa(status), got, n)
	}
	if got := after.observed - before.observed; got != uint64(n) { //nolint:gosec // n is a small literal in every caller
		t.Errorf("http_request_duration_seconds{route=%q,status=%q} observed %d more, want %d",
			route, strconv.Itoa(status), got, n)
	}
	if after.seconds <= before.seconds {
		t.Errorf("http_request_duration_seconds{route=%q,status=%q} sum did not move: %v",
			route, strconv.Itoa(status), after.seconds)
	}
}

// TestASuccessfulRequestIsCountedAndTimed is the acceptance criterion of #45
// in one test: a count and a latency, BY ROUTE, readable from the registry
// /actuator/metrics serves.
func TestASuccessfulRequestIsCountedAndTimed(t *testing.T) {
	const route = "GET /ok/{id}"
	before := read(t, route, http.StatusOK)

	url := serve(t, appended(), route, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	})
	if resp := get(t, url+"/ok/7"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ok/7 = %d, want 200", resp.StatusCode)
	}

	wantMore(t, before, route, http.StatusOK, 1)
}

// TestAPathWithAnIdInItDoesNotCreateANewLabelValue is the cardinality
// criterion of #45, and the reason the label is r.Pattern and never
// r.URL.Path. Two different ids are two requests on ONE series. A metric
// labelled by path is an unbounded label set and a Prometheus outage.
func TestAPathWithAnIdInItDoesNotCreateANewLabelValue(t *testing.T) {
	const route = "GET /orders/{id}"
	before := read(t, route, http.StatusOK)

	url := serve(t, appended(), route, func(http.ResponseWriter, *http.Request) {})
	for _, id := range []string{"1", "2", "3"} {
		get(t, url+"/orders/"+id)
	}

	// Three requests, one series.
	wantMore(t, before, route, http.StatusOK, 3)

	// And no series named after a path. This is the half that would still
	// pass if the label were r.URL.Path, so it is asserted separately.
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "http_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "route" && strings.HasPrefix(l.GetValue(), "/orders/") {
					t.Errorf("a path became a label value: route=%q", l.GetValue())
				}
			}
		}
	}
}

// TestAnUnroutedRequestCollapsesToOneLabelValue. Nothing matched, so
// http.ServeMux leaves r.Pattern empty and every scan of /wp-admin,
// /.env and the rest lands on the SAME series. That empty label value is
// what keeps a scanner from inventing series, and it is why there is no
// separate method label: a 405 and a 404 both arrive with no pattern, so
// method would be the one thing left varying, and a method is an arbitrary
// token.
func TestAnUnroutedRequestCollapsesToOneLabelValue(t *testing.T) {
	before404 := read(t, "", http.StatusNotFound)
	before405 := read(t, "", http.StatusMethodNotAllowed)

	url := serve(t, appended(), "GET /known", func(http.ResponseWriter, *http.Request) {})
	for _, p := range []string{"/wp-admin", "/.env", "/nope"} {
		if resp := get(t, url+p); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", p, resp.StatusCode)
		}
	}

	wantMore(t, before404, "", http.StatusNotFound, 3)

	// ServeMux answers a known path with the wrong method itself, and it
	// routes nothing to do it, so this lands on the empty route too.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/known", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /known: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /known = %d, want 405", resp.StatusCode)
	}
	wantMore(t, before405, "", http.StatusMethodNotAllowed, 1)
}

// TestTheStatusIsTheOneTheClientGot pins that the label is the response's
// status and not a flattened success, for a status the handler chose.
func TestTheStatusIsTheOneTheClientGot(t *testing.T) {
	const route = "GET /teapot"
	before := read(t, route, http.StatusTeapot)

	url := serve(t, appended(), route, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	get(t, url+"/teapot")

	wantMore(t, before, route, http.StatusTeapot, 1)
}

// TestAPanickingRequestIsCountedAs500 is this package's version of the lesson
// goboot/grpc/metrics learned. Use APPENDS, so the documented wiring puts
// this middleware INSIDE Recovery: a panic unwinds past anything written
// after next(), so the one failure an operator most wants to see would be the
// one failure not counted. It records in a defer, and labels the panic with
// the 500 Recovery is about to write.
func TestAPanickingRequestIsCountedAs500(t *testing.T) {
	const route = "GET /boom"
	before := read(t, route, http.StatusInternalServerError)

	url := serve(t, appended(), route, func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	if resp := get(t, url+"/boom"); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("GET /boom = %d, want 500", resp.StatusCode)
	}

	wantMore(t, before, route, http.StatusInternalServerError, 1)
}

// TestItRecordsWhereverItLandsInTheSlice is the claim that makes "append it"
// a safe instruction. DefaultMiddleware is a slice a user can edit, so this
// middleware can end up inside Recovery (append) or outside it (splice), and
// a panicking request must be counted as a 500 either way. Outside Recovery
// no panic ever reaches this middleware and the recorder sees the real 500;
// inside it, the defer sees the panic and labels it. The metric is the same.
func TestItRecordsWhereverItLandsInTheSlice(t *testing.T) {
	const route = "GET /spliced"
	before := read(t, route, http.StatusInternalServerError)

	// Spliced outside Recovery: RequestID, Logging, metrics, Recovery.
	log := discardLog()
	mw := []web.Middleware{web.RequestID, web.Logging(log), metrics.Middleware, web.Recovery(log)}
	url := serve(t, mw, route, func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	if resp := get(t, url+"/spliced"); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("GET /spliced = %d, want 500", resp.StatusCode)
	}

	wantMore(t, before, route, http.StatusInternalServerError, 1)
}

// TestAnAbortedConnectionIsNotCounted. http.ErrAbortHandler is a handler
// saying "drop this connection quietly", and every other layer already treats
// it that way: connect re-panics it instead of building an error,
// web.Recovery re-panics it instead of writing a 500, and web.Logging writes
// no access line for it at all. Counting it as a 500 would make this the one
// place that calls a deliberate abort a server failure.
//
// The panic must still reach net/http, so it is re-panicked, not swallowed —
// which is why this test reads the counter rather than the client's error.
func TestAnAbortedConnectionIsNotCounted(t *testing.T) {
	const route = "GET /abort"
	before := read(t, route, http.StatusInternalServerError)
	beforeOK := read(t, route, http.StatusOK)

	url := serve(t, appended(), route, func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/abort", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	// The connection is dropped, so the client gets a transport error rather
	// than a status. What it gets is not the claim; the counter is.
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}

	if got := read(t, route, http.StatusInternalServerError); got.count != before.count {
		t.Errorf("an aborted connection was counted as a 500: %v became %v", before.count, got.count)
	}
	if got := read(t, route, http.StatusOK); got.count != beforeOK.count {
		t.Errorf("an aborted connection was counted as a 200: %v became %v", beforeOK.count, got.count)
	}
}

// TestProbePathsAreCounted pins the one place this middleware deliberately
// answers differently from web.Logging, which skips /livez, /readyz and
// /actuator/*. The log skips them because of VOLUME — roughly 17,000 lines a
// day saying nothing. A metric has no volume: a probe adds one series, not
// 17,000 anything, and an operator who does not want probes in a graph
// filters them in PromQL. Nothing recovers a measurement that was never
// taken, so the latency of /readyz — which runs every Check — stays
// available.
func TestProbePathsAreCounted(t *testing.T) {
	for _, route := range []string{"GET /livez", "GET /readyz", "GET /actuator/metrics"} {
		before := read(t, route, http.StatusOK)

		path := strings.TrimPrefix(route, "GET ")
		url := serve(t, appended(), route, func(http.ResponseWriter, *http.Request) {})
		get(t, url+path)

		wantMore(t, before, route, http.StatusOK, 1)
	}
}

// TestStreamingStillWorks. gRPC shares this listener (ADR 0006), so the
// ResponseWriter this middleware wraps has to stay transparent to a streaming
// handler, or one turns into a handler that buffers until it returns.
//
// The flush goes through the BARE w.(http.Flusher) assertion, not through
// http.ResponseController. That is deliberate: ResponseController follows
// Unwrap, so it keeps working on a wrapper that has Unwrap and no Flush, and
// a test written that way passes with the Flush method deleted — measured.
// A plain type assertion does no unwrapping, so it is the idiom that actually
// breaks, and it is the one every hand-written SSE handler uses.
func TestStreamingStillWorks(t *testing.T) {
	const route = "GET /stream"
	got := make(chan string, 1)

	url := serve(t, appended(), route, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "first")
		f, ok := w.(http.Flusher)
		if !ok {
			// Returning completes the response, so the read below still
			// gets its bytes and the test fails on the line above rather
			// than hanging.
			t.Error("the wrapped ResponseWriter is not an http.Flusher")
			return
		}
		f.Flush()
		// Block until the client has read the flushed half. Without a
		// working Flush the read below never returns and this test times
		// out, which is the failure being pinned.
		<-got
	})

	resp := get(t, url+"/stream")
	buf := make([]byte, len("first"))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("read the flushed half: %v", err)
	}
	got <- string(buf)
	if string(buf) != "first" {
		t.Errorf("read %q, want %q", buf, "first")
	}
}

// TestTheLabelsAreTheDocumentedOnes pins the metric names and the label names
// themselves. docs/spec.md 4.3 prints them, and an operator's dashboard
// breaks on a rename exactly as a compile breaks on a renamed function.
func TestTheLabelsAreTheDocumentedOnes(t *testing.T) {
	url := serve(t, appended(), "GET /labels", func(http.ResponseWriter, *http.Request) {})
	get(t, url+"/labels")

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	found := map[string]bool{}
	for _, f := range families {
		name := f.GetName()
		if name != "http_requests_total" && name != "http_request_duration_seconds" {
			continue
		}
		found[name] = true
		for _, m := range f.GetMetric() {
			var names []string
			for _, l := range m.GetLabel() {
				names = append(names, l.GetName())
			}
			// Prometheus sorts label names, so the pair arrives as
			// route, status whichever order they were declared in.
			if want := []string{"route", "status"}; !slices.Equal(names, want) {
				t.Errorf("%s has labels %v, want %v", name, names, want)
			}
		}
	}
	for _, want := range []string{"http_requests_total", "http_request_duration_seconds"} {
		if !found[want] {
			t.Errorf("%s is not in the default registry", want)
		}
	}
}

// TestTheMetricsReachTheActuatorEndpoint is #45's acceptance criterion read
// end to end: count and latency by route, observable from ONE endpoint, under
// the ADR 0012 rule and no other pipeline. Every other test here reads
// prometheus.DefaultGatherer, which proves the registration but not the
// serving. This one serves a request and then scrapes /actuator/metrics for
// it.
//
// The Actuator is mounted on a mux of its own rather than behind the
// middleware, so the scrape does not count itself into the numbers it is
// reading.
func TestTheMetricsReachTheActuatorEndpoint(t *testing.T) {
	const route = "GET /scraped/{id}"

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

	url := serve(t, appended(), route, func(http.ResponseWriter, *http.Request) {})
	get(t, url+"/scraped/7")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/actuator/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/actuator/metrics = %d, want 200", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`http_requests_total{route="` + route + `",status="200"}`,
		`http_request_duration_seconds_count{route="` + route + `",status="200"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/actuator/metrics does not carry %s", want)
		}
	}
}

// TestAPanicAfterAPartialWriteKeepsTheStatusTheClientGot. A response already
// on the wire cannot be taken back, so web.Recovery checks whether anything
// was written and, if it was, logs the panic and writes NO 500 — the status
// line the client got is whatever the handler had already sent. Forcing 500
// here would make the metric disagree with both the access log and the
// client for the same request, which is worse than either answer alone.
//
// So a panic is labelled 500 only when nothing was written yet, which is the
// same condition web.Recovery uses to decide whether to write one.
func TestAPanicAfterAPartialWriteKeepsTheStatusTheClientGot(t *testing.T) {
	const route = "GET /half"
	before200 := read(t, route, http.StatusOK)
	before500 := read(t, route, http.StatusInternalServerError)

	url := serve(t, appended(), route, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "half a response")
		panic("boom")
	})
	// The client got a 200 and a truncated body: the status line went out
	// before the handler exploded.
	resp := get(t, url+"/half")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /half = %d, want 200 — the status line was already on the wire", resp.StatusCode)
	}

	wantMore(t, before200, route, http.StatusOK, 1)
	if got := read(t, route, http.StatusInternalServerError); got.count != before500.count {
		t.Errorf("counted as a 500 the client never received: %v became %v", before500.count, got.count)
	}
}
