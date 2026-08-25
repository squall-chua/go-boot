package goboot

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stub is a Component that records what happened to it. The base package and
// its tests import no Starter, so the lifecycle is exercised with this.
type stub struct {
	name      string
	tier      Tier
	stopOrder *[]string     // where Stop writes its name, if anywhere
	delay     time.Duration // how long Stop takes
	announce  bool          // say so on stdout, for a parent process to read
}

func (s *stub) Name() string { return s.name }
func (s *stub) Tier() Tier   { return s.tier }

func (s *stub) Start(ctx context.Context) (<-chan error, error) {
	if s.announce {
		fmt.Println("READY")
	}
	return nil, nil // this one cannot die once started
}

func (s *stub) Stop(ctx context.Context) error {
	time.Sleep(s.delay)
	if s.stopOrder != nil {
		*s.stopOrder = append(*s.stopOrder, s.name)
	}
	if s.announce {
		fmt.Println("STOPPED")
	}
	return nil
}

// TestStartStop pins that readiness follows the lifecycle and that Stop runs
// in reverse of Start.
func TestStartStop(t *testing.T) {
	var stops []string
	app, err := New(Config{Log: LogConfig{Level: "ERROR"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	app.Add(
		&stub{name: "observe", tier: TierObserve, stopOrder: &stops},
		&stub{name: "transport", tier: TierTransport, stopOrder: &stops},
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

	want := "transport observe"
	if got := strings.Join(stops, " "); got != want {
		t.Fatalf("stop order = %q, want %q", got, want)
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
	app, err := New(Config{Log: LogConfig{Level: "ERROR"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// announce puts READY and STOPPED on stdout, because the parent process
	// has no other way to see where the child has got to.
	app.Add(&stub{name: "ready", tier: TierTransport, delay: delay, announce: true})
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
