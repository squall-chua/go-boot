package actuator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/actuator"
)

// stub is a Component that offers no Check. hook runs inside the phase it is
// named for, so a test can look at the endpoints mid-lifecycle.
type stub struct {
	name  string
	tier  goboot.Tier
	start func()
	drain func()
	stop  func()
}

func (s *stub) Name() string      { return s.name }
func (s *stub) Tier() goboot.Tier { return s.tier }

func (s *stub) Start(ctx context.Context) (<-chan error, error) {
	if s.start != nil {
		s.start()
	}
	return nil, nil
}

func (s *stub) Stop(ctx context.Context) error {
	if s.stop != nil {
		s.stop()
	}
	return nil
}

// drainStub takes part in the drain phase. Drainer is optional, so it cannot
// live on stub itself.
type drainStub struct{ *stub }

func (d *drainStub) Drain(ctx context.Context) {
	if d.drain != nil {
		d.drain()
	}
}

// checkStub offers a Check. Checker is optional too, so this is a second type
// rather than a nil field on stub.
type checkStub struct {
	*stub
	check func(ctx context.Context) error
}

func (c *checkStub) Check(ctx context.Context) error { return c.check(ctx) }

// fixture is an App with an Actuator mounted on a plain ServeMux. The mux is
// all MountOn needs, which is the point of the structural interface: these
// tests import no Transport.
type fixture struct {
	app *goboot.App
	act *actuator.Actuator
	mux *http.ServeMux
	log *bytes.Buffer
}

func setup(t *testing.T, cfg actuator.Config, comps ...goboot.Component) *fixture {
	t.Helper()
	app, err := goboot.New(goboot.Config{
		// The drain delay is what a shutdown test would otherwise wait out.
		Lifecycle: goboot.LifecycleConfig{DrainDelay: time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	// Log is a public field, so a test can read what the Actuator writes. The
	// level is the App's own LevelVar, which is what /actuator/loglevel sets.
	app.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: app.Level}))
	act, err := actuator.New(cfg, app)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	act.MountOn(mux)
	app.Add(act)
	app.Add(comps...)
	return &fixture{app: app, act: act, mux: mux, log: &buf}
}

func (f *fixture) start(t *testing.T) {
	t.Helper()
	if err := f.app.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = f.app.Stop(context.Background()) })
}

// do sends a request through the mux the Actuator mounted on.
func (f *fixture) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) get(path string) *httptest.ResponseRecorder {
	return f.do(httptest.NewRequest(http.MethodGet, path, nil))
}

// status reads the status member out of a health body.
func status(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return body.Status
}

func failing(name string, err error) *checkStub {
	return &checkStub{
		stub:  &stub{name: name, tier: goboot.TierResource},
		check: func(ctx context.Context) error { return err },
	}
}

// TestDefaultWhitelist pins the default: liveness, readiness and build info
// are exposed, and everything else answers 404 rather than 403, so a wrong
// Ingress rule has nothing to leak.
func TestDefaultWhitelist(t *testing.T) {
	f := setup(t, actuator.Config{})
	f.start(t)

	for _, path := range []string{"/livez", "/actuator/livez", "/readyz", "/actuator/readyz", "/actuator/info"} {
		if code := f.get(path).Code; code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, code)
		}
	}
	for _, path := range []string{"/actuator/metrics", "/actuator/loglevel", "/actuator/pprof/", "/actuator/pprof/heap"} {
		if code := f.get(path).Code; code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, code)
		}
	}
}

// TestUnknownExposeEntryIsRefusedByNew covers the typo in a probe path: a
// failure worth having at boot, not at the first probe. New and not Start is
// the convention of spec 4.0 and ADR 0011 — the whitelist is pure config
// validation, so it is checked before anything is built. The message opens
// with the config key path, so an operator knows which YAML line to edit.
func TestUnknownExposeEntryIsRefusedByNew(t *testing.T) {
	app, err := goboot.New(goboot.Config{})
	if err != nil {
		t.Fatal(err)
	}
	act, err := actuator.New(actuator.Config{Expose: []string{"livez", "metric"}}, app)
	if err == nil {
		t.Fatal("New accepted an expose entry naming no endpoint")
	}
	if act != nil {
		t.Fatal("New returned an Actuator alongside its error")
	}
	if !strings.HasPrefix(err.Error(), "actuator.expose: ") || !strings.Contains(err.Error(), `"metric"`) {
		t.Errorf(`err = %q, want it to open with "actuator.expose: " and name "metric"`, err)
	}
}

// TestRepeatedExposeEntryDoesNotPanic: net/http panics on a pattern
// registered twice, and a repeated YAML entry must not take the process down.
func TestRepeatedExposeEntryDoesNotPanic(t *testing.T) {
	f := setup(t, actuator.Config{Expose: []string{"livez", "livez"}})
	f.start(t)

	if code := f.get("/livez").Code; code != http.StatusOK {
		t.Errorf("GET /livez = %d, want 200", code)
	}
}

// TestLivezIgnoresChecks is the restart-storm rule: a liveness test that
// touches a dependency turns a database outage into a restart loop.
func TestLivezIgnoresChecks(t *testing.T) {
	f := setup(t, actuator.Config{}, failing("db", errors.New("connection refused")))
	f.start(t)

	live := f.get("/livez")
	if live.Code != http.StatusOK || status(t, live) != "UP" {
		t.Errorf("GET /livez = %d %q, want 200 UP", live.Code, status(t, live))
	}
	ready := f.get("/readyz")
	if ready.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz = %d, want 503", ready.Code)
	}
}

// TestReadyzIsUnavailableDuringStartup probes from inside another Component's
// Start, which is the only moment that proves it: the Actuator starts first,
// so it is already answering while the rest of the App is still coming up.
func TestReadyzIsUnavailableDuringStartup(t *testing.T) {
	var during int
	probe := &stub{name: "slow", tier: goboot.TierResource}
	f := setup(t, actuator.Config{}, probe)
	probe.start = func() { during = f.get("/readyz").Code }
	f.start(t)

	if during != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz during startup = %d, want 503", during)
	}
	if code := f.get("/readyz").Code; code != http.StatusOK {
		t.Errorf("GET /readyz after startup = %d, want 200", code)
	}
}

// TestChecksArePulledFromComponents: the Component is only added to the App.
// Nothing registers its Check with the Actuator.
func TestChecksArePulledFromComponents(t *testing.T) {
	var ran bool
	c := &checkStub{
		stub:  &stub{name: "db", tier: goboot.TierResource},
		check: func(ctx context.Context) error { ran = true; return nil },
	}
	f := setup(t, actuator.Config{}, c)
	f.start(t)

	if code := f.get("/readyz").Code; code != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want 200", code)
	}
	if !ran {
		t.Error("the Component's Check never ran")
	}
}

// TestCheckGetsTheRequestContext: the Actuator adds no deadline of its own,
// so a Check sees the probe's real one, which is the operator's number.
func TestCheckGetsTheRequestContext(t *testing.T) {
	var deadline time.Time
	var hasDeadline bool
	c := &checkStub{
		stub: &stub{name: "db", tier: goboot.TierResource},
		check: func(ctx context.Context) error {
			deadline, hasDeadline = ctx.Deadline()
			return nil
		},
	}
	f := setup(t, actuator.Config{}, c)
	f.start(t)

	f.get("/readyz")
	if hasDeadline {
		t.Errorf("a probe with no deadline gave the Check one, %v", deadline)
	}

	want := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), want)
	defer cancel()
	f.do(httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(ctx))
	if !hasDeadline || !deadline.Equal(want) {
		t.Errorf("Check deadline = %v %v, want %v", deadline, hasDeadline, want)
	}
}

// TestShowDetails pins both bodies, and the WARN line that is written either
// way, so an operator who leaves showDetails at never still has the full text.
func TestShowDetails(t *testing.T) {
	for _, tc := range []struct {
		showDetails string
		wantBody    string
	}{
		{"", `{"status":"DOWN"}`},
		{"never", `{"status":"DOWN"}`},
		{"alwyas", `{"status":"DOWN"}`}, // a typo leaves the body bare
		{"always", `{"status":"DOWN","checks":{"db":"DOWN: connection refused"}}`},
	} {
		t.Run("showDetails="+tc.showDetails, func(t *testing.T) {
			f := setup(t, actuator.Config{ShowDetails: tc.showDetails},
				failing("db", errors.New("connection refused")))
			f.start(t)

			rec := f.get("/readyz")
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("GET /readyz = %d, want 503", rec.Code)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.wantBody {
				t.Errorf("body = %s, want %s", got, tc.wantBody)
			}
			if log := f.log.String(); !strings.Contains(log, "check failed") || !strings.Contains(log, "connection refused") {
				t.Errorf("no WARN line with the full detail in:\n%s", log)
			}
		})
	}
}

// TestMetricsServesTheDefaultRegistry: promhttp reads the default gatherer,
// which already carries the runtime metrics with no registration code.
func TestMetricsServesTheDefaultRegistry(t *testing.T) {
	f := setup(t, actuator.Config{Expose: []string{"metrics"}})
	f.start(t)

	rec := f.get("/actuator/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /actuator/metrics = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Error("the default registry's go_goroutines is missing")
	}
}

// TestLogLevelRejectsWithItsOwnWords is docs/spec.md 4.0 applied to the one
// place in go-boot that answers a caller with an error body outside
// goboot/web: what the caller receives is text this package wrote. Before #38
// both branches answered with err.Error(), which handed the caller slog's
// wording or "http: request body too large" — neither of which says what to
// send instead.
func TestLogLevelRejectsWithItsOwnWords(t *testing.T) {
	f := setup(t, actuator.Config{Expose: []string{"loglevel"}})
	f.start(t)

	for name, tc := range map[string]struct{ body, want string }{
		"not json":     {`{`, `body must be {"level": "..."}`},
		"body too big": {`{"level":"` + strings.Repeat("x", 2<<10) + `"}`, `body must be {"level": "..."}`},
		"not a level":  {`{"level":"chatty"}`, "level must be one of DEBUG, INFO, WARN, ERROR"},
	} {
		t.Run(name, func(t *testing.T) {
			put := httptest.NewRequest(http.MethodPut, "/actuator/loglevel", strings.NewReader(tc.body))
			rec := f.do(put)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("PUT = %d, want 400", rec.Code)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
			// The words below the boundary must not be among the words above it.
			for _, leaked := range []string{"slog:", "http:", "json:"} {
				if strings.Contains(rec.Body.String(), leaked) {
					t.Errorf("the body carries text from below: %q", rec.Body.String())
				}
			}
		})
	}
}

// TestLogLevel: the change reaches the running logger, and the next line obeys
// it with no restart.
func TestLogLevel(t *testing.T) {
	f := setup(t, actuator.Config{Expose: []string{"loglevel"}})
	f.start(t)

	if got := level(t, f.get("/actuator/loglevel")); got != "INFO" {
		t.Errorf("level = %q, want INFO", got)
	}
	f.app.Log.Debug("before")
	if strings.Contains(f.log.String(), "before") {
		t.Fatal("a DEBUG line was logged at INFO")
	}

	put := httptest.NewRequest(http.MethodPut, "/actuator/loglevel", strings.NewReader(`{"level":"DEBUG"}`))
	rec := f.do(put)
	if rec.Code != http.StatusOK || level(t, rec) != "DEBUG" {
		t.Fatalf("PUT /actuator/loglevel = %d %q, want 200 DEBUG", rec.Code, level(t, rec))
	}
	f.app.Log.Debug("after")
	if !strings.Contains(f.log.String(), "after") {
		t.Error("the next log line did not obey the new level")
	}
}

func level(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Level string `json:"level"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return body.Level
}

// TestInfoReportsTheBuild: a test binary carries no VCS stamp, so the Go
// version is the part that can be asserted here.
func TestInfoReportsTheBuild(t *testing.T) {
	f := setup(t, actuator.Config{})
	f.start(t)

	var body map[string]string
	if err := json.Unmarshal(f.get("/actuator/info").Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body["go"], "go1.") {
		t.Errorf("go = %q, want a Go version", body["go"])
	}
}

// TestDrainFlipsReadinessFirst: the 503 has to land before anything is torn
// down, or it arrives after the Transports have already let go.
func TestDrainFlipsReadinessFirst(t *testing.T) {
	var inDrain, inStop int
	s := &drainStub{&stub{name: "transport", tier: goboot.TierTransport}}
	f := setup(t, actuator.Config{}, s)
	s.drain = func() { inDrain = f.get("/readyz").Code }
	s.stop = func() { inStop = f.get("/readyz").Code }
	f.start(t)

	if code := f.get("/readyz").Code; code != http.StatusOK {
		t.Fatalf("GET /readyz while running = %d, want 200", code)
	}
	if err := f.app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inDrain != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz during drain = %d, want 503", inDrain)
	}
	if inStop != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz during stop = %d, want 503", inStop)
	}
}

// TestPprofIsServedUnderTheActuatorPrefix: the profile name has to survive the
// prefix rewrite, or go tool pprof gets the index page instead of a profile.
func TestPprofIsServedUnderTheActuatorPrefix(t *testing.T) {
	f := setup(t, actuator.Config{Expose: []string{"pprof"}})
	f.start(t)

	if code := f.get("/actuator/pprof/").Code; code != http.StatusOK {
		t.Errorf("GET /actuator/pprof/ = %d, want 200", code)
	}
	rec := f.get("/actuator/pprof/heap?debug=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /actuator/pprof/heap = %d, want 200", rec.Code)
	}
	if body, _ := io.ReadAll(rec.Body); !bytes.Contains(body, []byte("heap profile")) {
		t.Error("the heap profile came back as something else")
	}
}
