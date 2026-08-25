package goboot

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stub is a Component that records every phase it goes through. The base
// package and its tests import no Starter, so the lifecycle is exercised with
// this.
type stub struct {
	name string
	tier Tier

	events *[]string          // where each phase writes "<phase> <name>", if anywhere
	hook   func(phase string) // runs inside each phase, for a test that inspects the App

	startDelay time.Duration // how long Start takes; cut short by ctx
	startErr   error
	stopDelay  time.Duration // how long Stop takes; cut short by ctx
	stopErr    error
	deathc     chan error // handed back by Start; nil means "cannot die"
	announce   bool       // say so on stdout, for a parent process to read
}

func (s *stub) Name() string { return s.name }
func (s *stub) Tier() Tier   { return s.tier }

func (s *stub) record(phase string) {
	if s.events != nil {
		*s.events = append(*s.events, phase+" "+s.name)
	}
	if s.hook != nil {
		s.hook(phase)
	}
}

func (s *stub) Start(ctx context.Context) (<-chan error, error) {
	if !wait(ctx, s.startDelay) {
		return nil, ctx.Err()
	}
	if s.startErr != nil {
		return nil, s.startErr
	}
	s.record("start")
	if s.announce {
		fmt.Println("READY")
	}
	return s.deathc, nil
}

func (s *stub) Stop(ctx context.Context) error {
	if !wait(ctx, s.stopDelay) {
		return ctx.Err()
	}
	s.record("stop")
	if s.announce {
		fmt.Println("STOPPED")
	}
	return s.stopErr
}

// drainStub is a stub that also takes part in the drain phase. Drainer is
// optional, so it cannot live on stub itself.
type drainStub struct{ *stub }

func (d *drainStub) Drain(ctx context.Context) { d.record("drain") }

// wait sleeps for d, or reports false if ctx runs out first. A zero d never
// looks at ctx, so an already-expired one still gets its turn.
func wait(ctx context.Context, d time.Duration) bool {
	if d == 0 {
		return true
	}
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// newApp builds a quiet App. Tests that do not care about the drain delay
// pass a tiny one, because the real default is five seconds.
func newApp(t *testing.T, life LifecycleConfig) *App {
	t.Helper()
	app, err := New(Config{Log: LogConfig{Level: "ERROR"}, Lifecycle: life})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

// quick is a lifecycle with no waiting in it.
var quick = LifecycleConfig{DrainDelay: time.Nanosecond}

// TestStartStop pins that readiness follows the lifecycle and that Stop runs
// in reverse of Start.
func TestStartStop(t *testing.T) {
	var events []string
	app := newApp(t, quick)
	app.Add(
		&stub{name: "observe", tier: TierObserve, events: &events},
		&stub{name: "transport", tier: TierTransport, events: &events},
	)

	if app.Ready() {
		t.Fatal("Ready() is true before Start")
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !app.Ready() {
		t.Fatal("Ready() is false after Start")
	}
	if err := app.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if app.Ready() {
		t.Fatal("Ready() is true after Stop")
	}

	want := "start observe start transport stop transport stop observe"
	if got := strings.Join(events, " "); got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// TestStartOrderIsTierOrder pins the core move: the order passed to Add is
// ignored, so wiring the Components wrongly is not a mistake a developer can
// make.
func TestStartOrderIsTierOrder(t *testing.T) {
	var events []string
	app := newApp(t, quick)
	app.Add(
		&stub{name: "transport", tier: TierTransport, events: &events},
		&stub{name: "observe", tier: TierObserve, events: &events},
		&stub{name: "resource", tier: TierResource, events: &events},
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	want := "start observe start resource start transport"
	if got := strings.Join(events, " "); got != want {
		t.Fatalf("start order = %q, want %q", got, want)
	}
}

// TestDuplicateNamesRejected pins that two Components cannot share a name,
// and that the clash is caught before anything starts.
func TestDuplicateNamesRejected(t *testing.T) {
	var events []string
	app := newApp(t, quick)
	app.Add(
		&stub{name: "web", tier: TierObserve, events: &events},
		&stub{name: "web", tier: TierTransport, events: &events},
	)

	err := app.Start(t.Context())
	if err == nil {
		t.Fatal("Start accepted two Components named web")
	}
	if !strings.Contains(err.Error(), "web") {
		t.Fatalf("error %q does not name the duplicate", err)
	}
	if len(events) != 0 {
		t.Fatalf("Components started despite the clash: %v", events)
	}
	if app.Ready() {
		t.Fatal("Ready() is true after a rejected Start")
	}
}

// TestStartTimeoutCoversTheWholeSequence pins that the start timeout is one
// budget for every Component together, not one budget each. Two Components
// each take most of the budget, so the second one runs out of it.
func TestStartTimeoutCoversTheWholeSequence(t *testing.T) {
	var events []string
	app := newApp(t, LifecycleConfig{StartTimeout: 150 * time.Millisecond, DrainDelay: time.Nanosecond})
	app.Add(
		&stub{name: "first", tier: TierObserve, events: &events, startDelay: 100 * time.Millisecond},
		&stub{name: "second", tier: TierResource, events: &events, startDelay: 100 * time.Millisecond},
	)

	err := app.Start(t.Context())
	if err == nil {
		t.Fatal("Start ran past its own timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want a deadline", err)
	}
	if !strings.Contains(err.Error(), "second") {
		t.Fatalf("error %q does not name the Component that ran out of time", err)
	}
}

// TestStartFailureStopsInReverseWithoutDrain pins step 1 of Run: the started
// Components are stopped in reverse with no drain and no drain delay, and the
// start error comes back joined with any stop errors.
func TestStartFailureStopsInReverseWithoutDrain(t *testing.T) {
	var events []string
	first := &stub{name: "first", tier: TierObserve, events: &events}
	second := &stub{name: "second", tier: TierResource, events: &events, stopErr: errors.New("stop broke")}
	third := &stub{name: "third", tier: TierTransport, events: &events, startErr: errors.New("start broke")}
	app := newApp(t, LifecycleConfig{DrainDelay: 5 * time.Second})
	app.Add(&drainStub{first}, &drainStub{second}, third)

	start := time.Now()
	err := app.Start(t.Context())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Start hid the failure")
	}
	for _, want := range []string{"start broke", "stop broke", "third", "second"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q is missing %q", err, want)
		}
	}
	want := "start first start second stop second stop first"
	if got := strings.Join(events, " "); got != want {
		t.Fatalf("events = %q, want %q (no drain, reverse stop)", got, want)
	}
	if elapsed > time.Second {
		t.Fatalf("start failure took %v; it waited the drain delay", elapsed)
	}
	if app.Ready() {
		t.Fatal("Ready() is true after a failed Start")
	}
}

// TestReadyOnlyWhenEveryComponentHasStarted pins that readiness is false
// while the last Component is still starting, and false again the moment
// shutdown begins — before any Drain or Stop runs.
func TestReadyOnlyWhenEveryComponentHasStarted(t *testing.T) {
	var app *App
	readyAt := map[string]bool{}
	watch := func(phase string) { readyAt[phase] = app.Ready() }

	app = newApp(t, quick)
	app.Add(&drainStub{&stub{name: "only", tier: TierTransport, hook: watch}})

	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if readyAt["start"] {
		t.Fatal("Ready() was true while a Component was still starting")
	}
	if !app.Ready() {
		t.Fatal("Ready() is false once every Component has started")
	}
	if err := app.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if readyAt["drain"] || readyAt["stop"] {
		t.Fatalf("Ready() was still true during shutdown: %v", readyAt)
	}
}

// TestDrainRunsInStartOrderBeforeAnyStop pins the phase the prototype had
// backwards: the Actuator announces the 503 first, and the Transports let go
// afterwards.
func TestDrainRunsInStartOrderBeforeAnyStop(t *testing.T) {
	var events []string
	app := newApp(t, quick)
	app.Add(
		&drainStub{&stub{name: "transport", tier: TierTransport, events: &events}},
		&drainStub{&stub{name: "observe", tier: TierObserve, events: &events}},
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := app.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	want := "start observe start transport drain observe drain transport stop transport stop observe"
	if got := strings.Join(events, " "); got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// TestDrainDelayIsWaited pins the pause between Drain and Stop, which is what
// gives a load balancer time to see the 503.
func TestDrainDelayIsWaited(t *testing.T) {
	app := newApp(t, LifecycleConfig{DrainDelay: 300 * time.Millisecond})
	app.Add(&drainStub{&stub{name: "only", tier: TierTransport}})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := time.Now()
	if err := app.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("Stop took %v; it did not wait the drain delay", elapsed)
	}
}

// TestStopTimeoutCoversTheWholeSequence pins that the stop timeout is one
// budget for every Component together, and that a Component which overruns it
// gets a cancelled context rather than being waited on forever.
func TestStopTimeoutCoversTheWholeSequence(t *testing.T) {
	app := newApp(t, LifecycleConfig{DrainDelay: time.Nanosecond, StopTimeout: 100 * time.Millisecond})
	app.Add(
		&stub{name: "slow", tier: TierObserve, stopDelay: 10 * time.Second},
		&stub{name: "alsoslow", tier: TierTransport, stopDelay: 10 * time.Second},
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := time.Now()
	err := app.Stop(t.Context())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Stop hid the overrun")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want a deadline", err)
	}
	for _, want := range []string{"slow", "alsoslow"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q is missing %q", err, want)
		}
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Stop took %v; the timeout did not cover the sequence", elapsed)
	}
}

// TestStopJoinsErrors pins that one bad Stop does not hide the rest.
func TestStopJoinsErrors(t *testing.T) {
	app := newApp(t, quick)
	app.Add(
		&stub{name: "first", tier: TierObserve, stopErr: errors.New("first broke")},
		&stub{name: "second", tier: TierTransport, stopErr: errors.New("second broke")},
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	err := app.Stop(t.Context())
	if err == nil {
		t.Fatal("Stop hid both failures")
	}
	for _, want := range []string{"first broke", "second broke"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q is missing %q", err, want)
		}
	}
}

// TestDeathIsFatal pins that a failure arriving after startup shuts the App
// down and names the Component that died.
func TestDeathIsFatal(t *testing.T) {
	var events []string
	deathc := make(chan error, 1)
	app := newApp(t, quick)
	app.Add(
		&stub{name: "observe", tier: TierObserve, events: &events},
		&stub{name: "transport", tier: TierTransport, events: &events, deathc: deathc},
	)

	done := make(chan error, 1)
	go func() { done <- app.Run(t.Context()) }()

	// Wait for readiness, then kill the Transport from underneath the App.
	for !app.Ready() {
		time.Sleep(time.Millisecond)
	}
	deathc <- errors.New("listener broke")

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil after a Component died")
		}
		if !strings.Contains(err.Error(), "transport") || !strings.Contains(err.Error(), "listener broke") {
			t.Fatalf("error %q does not name the Component that died", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a death did not begin shutdown")
	}

	want := "start observe start transport stop transport stop observe"
	if got := strings.Join(events, " "); got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// TestNilDeathChannelDoesNotBlockShutdown pins the common case: a Component
// that cannot die once started returns nil. A nil channel blocks forever, so
// it must never be read as a death, and it must not hold shutdown up.
func TestNilDeathChannelDoesNotBlockShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	app := newApp(t, quick)
	app.Add(
		&stub{name: "cannot-die", tier: TierObserve},                             // nil channel
		&stub{name: "can-die", tier: TierTransport, deathc: make(chan error, 1)}, // live, never used
	)

	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	for !app.Ready() {
		time.Sleep(time.Millisecond)
	}

	// Nobody has died. Reading a nil channel as a death would end Run here.
	select {
	case err := <-done:
		t.Fatalf("Run returned on its own (%v); a nil channel was read as a death", err)
	case <-time.After(100 * time.Millisecond):
	}
	if !app.Ready() {
		t.Fatal("Ready() went false on its own")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown blocked on a nil death channel")
	}
}

// TestLifecycleDefaults pins the three published defaults, which are what an
// operator gets when the config says nothing.
func TestLifecycleDefaults(t *testing.T) {
	app, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := LifecycleConfig{
		StartTimeout: 30 * time.Second,
		DrainDelay:   5 * time.Second,
		StopTimeout:  10 * time.Second,
	}
	if app.life != want {
		t.Fatalf("defaults = %+v, want %+v", app.life, want)
	}
}

// TestBadLogLevel pins that a config mistake is an error, not a panic.
func TestBadLogLevel(t *testing.T) {
	if _, err := New(Config{Log: LogConfig{Level: "LOUD"}}); err == nil {
		t.Fatal("New accepted an unparsable log level")
	}
}

// TestHelperApp is not a test. It is the child process the signal tests
// spawn: an App with one Component, run to completion under Run.
func TestHelperApp(t *testing.T) {
	if os.Getenv("GOBOOT_HELPER") == "" {
		t.Skip("helper process, driven by the signal tests")
	}
	delay, err := time.ParseDuration(os.Getenv("GOBOOT_HELPER_STOP"))
	if err != nil {
		t.Fatalf("GOBOOT_HELPER_STOP: %v", err)
	}
	// The stop timeout is a minute so that the slow-stop case really is slow;
	// the drain delay is out of the way because these tests are about signals.
	app := newApp(t, LifecycleConfig{DrainDelay: time.Nanosecond, StopTimeout: time.Minute})
	// announce puts READY and STOPPED on stdout, because the parent process
	// has no other way to see where the child has got to.
	app.Add(&stub{name: "ready", tier: TierTransport, stopDelay: delay, announce: true})
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// startHelper spawns the child and returns once it has printed READY.
func startHelper(t *testing.T, stopDelay time.Duration) (*exec.Cmd, chan string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperApp$")
	cmd.Env = append(os.Environ(),
		"GOBOOT_HELPER=1",
		"GOBOOT_HELPER_STOP="+stopDelay.String(),
	)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	await(t, lines, "READY")
	return cmd, lines
}

// await blocks until the child prints want, or fails the test.
func await(t *testing.T, lines chan string, want string) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("helper exited before printing %q", want)
			}
			if line == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q from the helper", want)
		}
	}
}

// TestSigtermShutsDownCleanly pins step 3 of Run: one SIGTERM stops every
// Component and the process exits zero.
func TestSigtermShutsDownCleanly(t *testing.T) {
	cmd, lines := startHelper(t, 0)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	await(t, lines, "STOPPED")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper exited badly: %v", err)
	}
}

// TestSecondSignalKills pins the other half: once shutdown has begun, Go's
// default handling is back, so a second signal kills the process outright
// instead of queueing behind a Stop that takes ten seconds.
func TestSecondSignalKills(t *testing.T) {
	cmd, _ := startHelper(t, 10*time.Second)
	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("first signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Repeat the second signal until it lands, so the test does not depend on
	// hitting the window between the first signal arriving and Run handing
	// the handler back. If the handler were never handed back, every one of
	// these would be swallowed and the deadline would fire.
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	var err error
	for waiting := true; waiting; {
		select {
		case err = <-done:
			waiting = false
		case <-tick.C:
			_ = cmd.Process.Signal(syscall.SIGTERM)
		case <-deadline:
			t.Fatal("helper survived repeated SIGTERMs")
		}
	}

	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("helper took %v to die; the second signal did not kill it", elapsed)
	}
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("helper exited cleanly (%v); it should have been killed by the signal", err)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("helper exit = %v, want killed by SIGTERM", exit)
	}
}
