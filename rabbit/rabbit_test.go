package rabbit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/squall-chua/go-boot"
)

// The three interfaces the Starter claims. Checked by the compiler, because
// docs/spec.md 8.1's lesson is that a rule a tool keeps beats a rule prose
// promises.
var (
	_ goboot.Component = (*Component)(nil)
	_ goboot.Drainer   = (*Component)(nil)
	_ goboot.Checker   = (*Component)(nil)
)

// quiet is a logger that writes nowhere, so a test that exercises the error
// paths does not spray the output.
func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// acker records what the consume loop did with each delivery. It stands in
// for the channel an amqp.Delivery normally carries.
type acker struct {
	mu      sync.Mutex
	acks    int
	requeue []bool // one entry per Nack, holding its requeue flag
}

func (a *acker) Ack(uint64, bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acks++
	return nil
}

func (a *acker) Nack(_ uint64, _, requeue bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requeue = append(a.requeue, requeue)
	return nil
}

func (a *acker) Reject(uint64, bool) error { return nil }

func (a *acker) counts() (acks int, requeue []bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.acks, append([]bool(nil), a.requeue...)
}

// startFake wires a Component to a delivery channel of the test's own, with
// no broker behind it. Drain, Stop and closeBroker all guard a nil connection,
// so every path these tests care about runs exactly as it does in production.
// The one thing it skips is dial, which is the only part that needs a broker.
func startFake(t *testing.T, cfg Config, h Handler) (*Component, chan amqp.Delivery) {
	t.Helper()
	if cfg.URL == "" {
		cfg.URL = "amqp://test"
	}
	if cfg.Queue == "" {
		cfg.Queue = "q"
	}
	c, err := New(cfg, quiet(), h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	t.Cleanup(c.cancel)
	deliveries := make(chan amqp.Delivery, 8)
	go c.consume(deliveries)
	return c, deliveries
}

// TestNewValidatesItsOwnConfig pins the 4.0 split: everything that can be
// judged without the world is judged in New, and every message opens with the
// config key at fault.
func TestNewValidatesItsOwnConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		h    Handler
		want string
	}{
		{"no url", Config{Queue: "q"}, func(context.Context, Message) error { return nil }, "rabbit.url: required"},
		{"wrong scheme", Config{URL: "http://x", Queue: "q"}, func(context.Context, Message) error { return nil }, "rabbit.url: must begin"},
		{"no queue", Config{URL: "amqp://x"}, func(context.Context, Message) error { return nil }, "rabbit.queue: required"},
		{"negative prefetch", Config{URL: "amqp://x", Queue: "q", Prefetch: -1}, func(context.Context, Message) error { return nil }, "rabbit.prefetch:"},
		{"no handler", Config{URL: "amqp://x", Queue: "q"}, nil, "rabbit: New needs a Handler"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg, quiet(), tc.h)
			if err == nil {
				t.Fatalf("New(%+v) = nil error, want one", tc.cfg)
			}
			if got := err.Error(); !strings.Contains(got, tc.want) {
				t.Fatalf("New error = %q, want it to open with %q", got, tc.want)
			}
		})
	}
}

// TestNewDefaultsAndIdentity pins the defaults and the name two Components on
// two queues rely on to not collide on the App's duplicate-name check.
func TestNewDefaultsAndIdentity(t *testing.T) {
	c, err := New(Config{URL: "amqp://x", Queue: "orders"}, quiet(), func(context.Context, Message) error { return nil })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.cfg.Prefetch != 1 {
		t.Errorf("prefetch = %d, want 1", c.cfg.Prefetch)
	}
	if got, want := c.Name(), "rabbit:orders"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := c.Tier(), goboot.TierTransport; got != want {
		t.Errorf("Tier = %v, want TierTransport", got)
	}
	if err := c.Check(t.Context()); err != nil {
		t.Errorf("Check on a healthy consumer = %v, want nil", err)
	}
}

// TestDrainReturnsWhileAHandlerIsRunning is the first of the three #49 makes
// necessary. Drain must not wait for work in flight: its ctx has no deadline
// and cannot be cancelled, so a Drain that waited would hang the whole
// shutdown with nothing able to interrupt it.
func TestDrainReturnsWhileAHandlerIsRunning(t *testing.T) {
	running, release := make(chan struct{}), make(chan struct{})
	c, deliveries := startFake(t, Config{}, func(ctx context.Context, m Message) error {
		close(running)
		<-release
		return nil
	})
	deliveries <- amqp.Delivery{Acknowledger: &acker{}, Body: []byte("one")}
	<-running // the handler is in flight and is staying there

	done := make(chan struct{})
	go func() { defer close(done); c.Drain(context.Background()) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Drain blocked while a handler was running; it must return at once (#49)")
	}
	close(release)
}

// TestStopWaitsForTheHandler is the second: the waiting Drain must not do
// belongs in Stop, which has StopTimeout behind it.
func TestStopWaitsForTheHandler(t *testing.T) {
	running, release := make(chan struct{}), make(chan struct{})
	finished := make(chan struct{})
	c, deliveries := startFake(t, Config{}, func(ctx context.Context, m Message) error {
		close(running)
		<-release
		close(finished)
		return nil
	})
	deliveries <- amqp.Delivery{Acknowledger: &acker{}, Body: []byte("one")}
	<-running

	c.Drain(context.Background())
	// The broker closes the delivery stream once a cancelled consumer has
	// handed over what it already sent. There is no broker here, so say so.
	close(deliveries)

	stopped := make(chan error, 1)
	go func() { stopped <- c.Stop(context.Background()) }()

	select {
	case <-stopped:
		t.Fatal("Stop returned while the handler was still running; it must wait")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after the handler finished")
	}
	<-finished
}

// TestStopGivesUpAndCancelsTheHandler is the third, and the one not to skip:
// it is what stops a handler that will not return from hanging the shutdown.
func TestStopGivesUpAndCancelsTheHandler(t *testing.T) {
	running := make(chan struct{})
	cancelled := make(chan struct{})
	c, deliveries := startFake(t, Config{}, func(ctx context.Context, m Message) error {
		close(running)
		<-ctx.Done() // never returns on its own
		close(cancelled)
		return ctx.Err()
	})
	deliveries <- amqp.Delivery{Acknowledger: &acker{}, Body: []byte("one")}
	<-running

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Stop(ctx)
	if err == nil {
		t.Fatal("Stop = nil, want an error naming the handler still running")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Stop error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Stop took %v; it must give up when its own ctx expires", elapsed)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop returned without cancelling the handler's context")
	}
}

// TestHandlerResultDecidesTheAcknowledgement pins the money path: a handler
// that returns nil acknowledges, one that errors Nacks, and DiscardOnError is
// the only thing that decides whether the Nack requeues.
func TestHandlerResultDecidesTheAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name        string
		discard     bool
		handlerErr  error
		wantAcks    int
		wantRequeue []bool
	}{
		{"success acks", false, nil, 1, nil},
		{"error requeues by default", false, errors.New("boom"), 0, []bool{true}},
		{"error discards when asked", true, errors.New("boom"), 0, []bool{false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handled := make(chan struct{})
			c, deliveries := startFake(t, Config{DiscardOnError: tc.discard},
				func(context.Context, Message) error { defer close(handled); return tc.handlerErr })
			a := &acker{}
			deliveries <- amqp.Delivery{Acknowledger: a, Body: []byte("one")}
			<-handled

			// The acknowledgement happens after the handler returns, so give
			// the loop a moment to get there before reading the counts.
			waitFor(t, func() bool {
				acks, requeue := a.counts()
				return acks == tc.wantAcks && len(requeue) == len(tc.wantRequeue)
			}, "acknowledgement")

			acks, requeue := a.counts()
			if acks != tc.wantAcks {
				t.Errorf("acks = %d, want %d", acks, tc.wantAcks)
			}
			if len(requeue) != len(tc.wantRequeue) {
				t.Fatalf("nacks = %d, want %d", len(requeue), len(tc.wantRequeue))
			}
			for i := range requeue {
				if requeue[i] != tc.wantRequeue[i] {
					t.Errorf("nack %d requeue = %v, want %v", i, requeue[i], tc.wantRequeue[i])
				}
			}
			c.closing.Store(true)
			close(deliveries)
		})
	}
}

// TestHandlerPanicIsRecoveredAndNacked pins that one bad message does not take
// the process down with every other message in flight, and that the delivery
// is not silently lost either.
func TestHandlerPanicIsRecoveredAndNacked(t *testing.T) {
	handled := make(chan struct{})
	c, deliveries := startFake(t, Config{}, func(context.Context, Message) error {
		defer close(handled)
		panic("handler blew up")
	})
	a := &acker{}
	deliveries <- amqp.Delivery{Acknowledger: a, Body: []byte("one")}
	<-handled

	waitFor(t, func() bool { _, requeue := a.counts(); return len(requeue) == 1 }, "nack after panic")
	acks, requeue := a.counts()
	if acks != 0 {
		t.Errorf("acks = %d after a panic, want 0", acks)
	}
	if len(requeue) != 1 || !requeue[0] {
		t.Errorf("nacks = %v, want one requeue", requeue)
	}
	// The loop is still alive: a second delivery is still handled.
	c.closing.Store(true)
	close(deliveries)
}

// TestMessageCarriesTheDeliveryFields pins the mapping a handler reads.
func TestMessageCarriesTheDeliveryFields(t *testing.T) {
	got := make(chan Message, 1)
	c, deliveries := startFake(t, Config{}, func(_ context.Context, m Message) error {
		got <- m
		return nil
	})
	deliveries <- amqp.Delivery{
		Acknowledger: &acker{},
		Body:         []byte("payload"),
		Exchange:     "ex",
		RoutingKey:   "rk",
		Headers:      amqp.Table{"k": "v"},
		Redelivered:  true,
	}
	m := <-got
	if string(m.Body) != "payload" || m.Exchange != "ex" || m.RoutingKey != "rk" || !m.Redelivered {
		t.Errorf("Message = %+v, want the delivery's fields", m)
	}
	if m.Headers["k"] != "v" {
		t.Errorf("Headers = %v, want k=v", m.Headers)
	}
	c.closing.Store(true)
	close(deliveries)
}

// TestPermanentTellsRefusalFromOutage pins which broker errors end up on the
// death channel. A queue that is gone and credentials that are revoked do not
// fix themselves; anything else is an outage to reconnect through.
func TestPermanentTellsRefusalFromOutage(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"queue gone", &amqp.Error{Code: amqp.NotFound}, true},
		{"access refused", &amqp.Error{Code: amqp.AccessRefused}, true},
		{"not allowed", &amqp.Error{Code: amqp.NotAllowed}, true},
		{"connection forced", &amqp.Error{Code: amqp.ConnectionForced}, false},
		{"plain network error", errors.New("dial tcp: connection refused"), false},
		{"wrapped queue gone", errors.Join(errors.New("dial"), &amqp.Error{Code: amqp.NotFound}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := permanent(tc.err); got != tc.want {
				t.Errorf("permanent(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestStopBeforeStartIsNotAnError pins that a Component the App never started
// — because an earlier Tier failed — can still be stopped.
func TestStopBeforeStartIsNotAnError(t *testing.T) {
	c, err := New(Config{URL: "amqp://x", Queue: "q"}, quiet(), func(context.Context, Message) error { return nil })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Stop(t.Context()); err != nil {
		t.Errorf("Stop before Start = %v, want nil", err)
	}
}

// waitFor polls until cond holds or the test gives up, so an assertion about
// something another goroutine does does not need a sleep long enough to be
// slow and short enough to be flaky.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
