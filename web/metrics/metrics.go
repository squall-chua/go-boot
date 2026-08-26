// Package metrics counts and times HTTP requests by route, and it is opt-in
// by import: a service that does not import it links none of it.
//
// # Why it is not in goboot/web
//
// goboot/web links 2 modules, which is the leanest package in the repo, and
// it is the one every HTTP user imports. Naming
// github.com/prometheus/client_golang there would charge every one of them
// for Prometheus to get a counter most of them never scrape — the exact leak
// assertion 2 of .github/check-imports.sh exists to catch. So this is a
// subpackage, the same shape goboot/grpc/metrics has, and the wiring costs
// the user one entry in the middleware slice.
//
// # One pipeline, one endpoint
//
// The counter and the histogram are registered on
// prometheus.DefaultRegisterer, which is the registry /actuator/metrics
// serves. That is ADR 0012's rule, settled by #41 before it could add a
// single metric and spent here: a metric go-boot ships is registered on
// prometheus.DefaultRegisterer and is readable at /actuator/metrics. So an
// operator asking "how many of my requests failed" has one place to look, and
// it is the same place that answers it for RPCs. See docs/spec.md 4.3.
//
// # Use
//
// Use appends, so this lands innermost:
//
//	srv.Use(append(web.DefaultMiddleware(app.Log), metrics.Middleware)...)
//
// It records in a defer, so that placement is safe — see record below.
//
// APPEND it; do not splice it above web.Logging. Logging hands the layers
// below it a new request, and http.ServeMux fills r.Pattern in place on the
// one it routed, so a middleware placed above Logging reads an EMPTY route
// and every route collapses onto the series meant for requests that matched
// nothing. trace.RouteSpanName is innermost in trace.DefaultMiddleware for
// this same reason. Measured and pinned by #47.
//
// It is not wired by preset.Full, because a Preset that imported this
// package would charge every Preset user for Prometheus. A Preset user who wants these
// metrics copies the body of Full, which is the documented escape hatch.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// The two metrics, registered at package init rather than inside a
// constructor, the same as goboot/grpc/metrics: there is one registry per
// process and registering twice on it panics.
//
// No Prometheus type appears in this package's API, per docs/spec.md 4.2.
//
// Both labels are bounded, and that is the whole cardinality argument:
//
//   - route is r.Pattern, NEVER r.URL.Path. Patterns are registered at
//     startup, so the set is fixed at compile time; paths are whatever a
//     caller sends. web.Logging already draws this line and says why — "path
//     is what was asked for; route is the low-cardinality label to group by"
//     — and a metric is where getting it wrong costs a Prometheus outage
//     rather than a wide log line.
//   - status is written by the server, not asked for by the client.
//
// There is deliberately no method label. r.Pattern already carries the method
// for a method-bound pattern ("GET /users/{id}"), and on the path where it
// does not — a request that routed nowhere, where r.Pattern is empty — the
// method is an arbitrary token the caller chose, so it would be the one
// unbounded label left. Measured: a 404 and a 405 both arrive with r.Pattern
// empty.
var (
	requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests handled, by route and by the status the caller received. An empty route is a request that matched no pattern.",
	}, []string{"route", "status"})

	duration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP handler latency in seconds, by route and by the status the caller received.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route", "status"})
)

// Middleware counts and times one request. It is the plain net/http shape, so
// it is a web.Middleware and needs no constructor: unlike web.Logging it
// holds no logger, and unlike goboot/grpc/metrics it has no per-mount options
// to build.
//
// Probe paths are counted. web.Logging skips /livez, /readyz and /actuator/*,
// and this is the one place the answer is deliberately different: the log
// skips them because of VOLUME, roughly 17,000 lines a day saying nothing,
// and a metric has no volume — a probe adds one series, not 17,000 anything.
// An operator who does not want probes in a graph excludes them in PromQL,
// and nothing recovers a measurement that was never taken, so the latency of
// /readyz, which runs every readiness Check, stays available.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		defer record(r, rec, time.Now())
		next.ServeHTTP(rec, r)
	})
}

// record writes the two metrics. It is deferred, not called after next, and
// that is what makes "append it to DefaultMiddleware" a safe instruction:
// Use appends, so this middleware lands INSIDE web.Recovery, and a panicking
// handler unwinds past anything written after next(). The one failure an
// operator most wants to see would be the one failure not counted.
//
// r.Pattern is read here rather than in Middleware because http.ServeMux only
// fills it in once it has routed, which happens inside next. It fills it IN
// PLACE, on the request it was handed, which is what makes the placement rule
// in the package doc above a real rule and not a preference.
//
// A recovered panic is labelled with the status the caller actually got, and
// is then re-panicked so Recovery still logs it and still writes what it
// writes. Nothing written yet means Recovery is about to send a 500, so 500
// is the label; a panic AFTER a partial write means Recovery writes nothing
// and the client keeps the status already on the wire, so that is the label.
// Spliced OUTSIDE Recovery instead, this middleware sees no panic at all and
// the recorder already holds the same answer either way, so the status label
// does not depend on which side of Recovery the user put it. That is the only
// placement this paragraph settles: web.Logging is one this middleware must
// stay BELOW, for the separate reason given above.
func record(r *http.Request, rec *recorder, start time.Time) {
	p := recover()

	// http.ErrAbortHandler is a handler saying "drop this connection
	// quietly", and it goes back untouched and UNCOUNTED. Every other layer
	// around this one already treats it that way: web.Recovery re-panics it
	// rather than writing a 500, web.Logging writes no access line for it at
	// all, and goboot/grpc/metrics does not count it either. A counter that
	// alone called this a server error would be the odd one out, and it would
	// be wrong: nothing failed, the caller left.
	if p == http.ErrAbortHandler {
		panic(p)
	}

	// A panic is labelled 500 only when nothing has been written yet, which
	// is the SAME condition web.Recovery uses to decide whether to write one.
	// A response already on the wire cannot be taken back: Recovery logs the
	// panic and returns without touching the status line, so the client keeps
	// the status the handler had already sent and the access log records it.
	// Forcing 500 here would make the metric the only place claiming a status
	// nobody received.
	code := rec.status
	if p != nil && !rec.wrote {
		code = http.StatusInternalServerError
	}

	// Named for the labels they become, so the two WithLabelValues calls
	// below can be read against the label names without counting arguments.
	route, status := r.Pattern, strconv.Itoa(code)
	requests.WithLabelValues(route, status).Inc()
	duration.WithLabelValues(route, status).Observe(time.Since(start).Seconds())

	if p != nil {
		panic(p)
	}
}

// recorder remembers the status this middleware labels by. It is a second,
// smaller copy of the one in goboot/web, which is unexported and so cannot be
// read from here. Exporting that one instead would cost goboot/web no
// dependency — it is not the Prometheus leak this package exists to prevent —
// but it would add a ResponseWriter wrapper to the frozen public surface of
// docs/spec.md 12 for one package's internal convenience, and it would export
// a type whose whole job is to be wrapped around another. Twenty lines of
// copy is the cheaper of the two.
//
// Unwrap and Flush keep it transparent to http.ResponseController and to
// streaming handlers, which matters because gRPC shares this server
// (ADR 0006).
type recorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *recorder) WriteHeader(status int) {
	// A 1xx is informational, and the handler still owes the client a real
	// status. Same condition and same reason as goboot/web's copy, which
	// carries the argument; #47 fixed both, because either one alone
	// swallows the final status on the documented wiring.
	if status >= 100 && status <= 199 && status != http.StatusSwitchingProtocols {
		r.ResponseWriter.WriteHeader(status)
		return
	}
	if r.wrote {
		return // net/http already logs the superfluous call
	}
	r.wrote = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Flush marks the response written: flushing puts the status line on the
// wire, so nothing can be taken back after it.
func (r *recorder) Flush() {
	r.wrote = true
	//nolint:errcheck // a ResponseWriter that cannot flush is not an error here
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}
