package kafka

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/squall-chua/go-boot"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
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

// batch builds one poll's worth of records on a single partition.
func batch(topic string, partition int32, offsets ...int64) kgo.Fetches {
	recs := make([]*kgo.Record, 0, len(offsets))
	for _, o := range offsets {
		recs = append(recs, &kgo.Record{
			Topic: topic, Partition: partition, Offset: o,
			Value: []byte("v"), Timestamp: time.Unix(0, 0),
		})
	}
	return kgo.Fetches{{Topics: []kgo.FetchTopic{{
		Topic:      topic,
		Partitions: []kgo.FetchPartition{{Partition: partition, Records: recs}},
	}}}}
}

// startFake drives the real fetch loop with a poller of the test's own, so
// the shutdown paths run exactly as they do in production without a cluster
// behind them. The client is left nil: Stop guards it, and Drain never
// touches it.
func startFake(t *testing.T, cfg Config, h Handler, poll func(context.Context) kgo.Fetches) *Component {
	t.Helper()
	if len(cfg.Brokers) == 0 {
		cfg.Brokers = []string{"localhost:9092"}
	}
	if cfg.Topic == "" {
		cfg.Topic = "t"
	}
	if cfg.Group == "" {
		cfg.Group = "g"
	}
	c, err := New(cfg, quiet(), h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.pollCtx, c.pollCancel = context.WithCancel(context.Background())
	t.Cleanup(func() { c.cancel(); c.pollCancel() })
	go c.consume(poll, func(context.Context, ...*kgo.Record) error { return nil })
	return c
}

// onceThenBlock hands the batch over on the first poll and then blocks until
// the poll context is cancelled, which is what a quiet topic looks like.
func onceThenBlock(f kgo.Fetches) func(context.Context) kgo.Fetches {
	var once sync.Once
	return func(ctx context.Context) kgo.Fetches {
		var out kgo.Fetches
		delivered := false
		once.Do(func() { out = f; delivered = true })
		if delivered {
			return out
		}
		<-ctx.Done()
		return nil
	}
}

// TestNewValidatesItsOwnConfig pins the 4.0 split: everything judgeable
// without the world is judged in New, and each message opens with the key.
func TestNewValidatesItsOwnConfig(t *testing.T) {
	ok := func(context.Context, Message) error { return nil }
	full := Config{Brokers: []string{"b:9092"}, Topic: "t", Group: "g"}
	withSASL := func(s SASLConfig) Config { c := full; c.SASL = s; return c }

	for _, tc := range []struct {
		name string
		cfg  Config
		h    Handler
		want string
	}{
		{"no brokers", Config{Topic: "t", Group: "g"}, ok, "kafka.brokers: required"},
		{"empty broker", Config{Brokers: []string{""}, Topic: "t", Group: "g"}, ok, "kafka.brokers: contains an empty"},
		{"no topic", Config{Brokers: []string{"b"}, Group: "g"}, ok, "kafka.topic: required"},
		{"no group", Config{Brokers: []string{"b"}, Topic: "t"}, ok, "kafka.group: required"},
		{"unknown mechanism", withSASL(SASLConfig{Mechanism: "kerberos", Username: "u", Password: "p"}), ok, "kafka.sasl.mechanism: unknown"},
		{"mechanism without password", withSASL(SASLConfig{Mechanism: saslPlain, Username: "u"}), ok, "kafka.sasl: mechanism is set"},
		{"no handler", full, nil, "kafka: New needs a Handler"},
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

// TestMechanismAcceptsTheThreeItDocuments pins the SASL set, including that
// an empty mechanism means SASL is off rather than an error.
func TestMechanismAcceptsTheThreeItDocuments(t *testing.T) {
	for _, name := range []string{saslPlain, saslSHA256, saslSHA512} {
		m, err := mechanism(SASLConfig{Mechanism: name, Username: "u", Password: "p"})
		if err != nil {
			t.Errorf("mechanism(%q): %v", name, err)
		}
		if m == nil {
			t.Errorf("mechanism(%q) = nil, want a mechanism", name)
		}
	}
	m, err := mechanism(SASLConfig{})
	if err != nil || m != nil {
		t.Errorf("mechanism(empty) = %v, %v; want nil, nil (SASL off)", m, err)
	}
}

// TestNewIdentity pins the name two Components on two topics rely on to not
// collide on the App's duplicate-name check.
func TestNewIdentity(t *testing.T) {
	c, err := New(Config{Brokers: []string{"b"}, Topic: "orders", Group: "g"}, quiet(),
		func(context.Context, Message) error { return nil })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := c.Name(), "kafka:orders"; got != want {
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
	c := startFake(t, Config{}, func(context.Context, Message) error {
		close(running)
		<-release
		return nil
	}, onceThenBlock(batch("t", 0, 1)))
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
	c := startFake(t, Config{}, func(context.Context, Message) error {
		close(running)
		<-release
		return nil
	}, onceThenBlock(batch("t", 0, 1)))
	<-running

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
}

// TestStopGivesUpAndCancelsTheHandler is the third, and the one not to skip:
// it is what stops a handler that will not return from hanging the shutdown.
func TestStopGivesUpAndCancelsTheHandler(t *testing.T) {
	running, cancelled := make(chan struct{}), make(chan struct{})
	c := startFake(t, Config{}, func(ctx context.Context, m Message) error {
		close(running)
		<-ctx.Done() // never returns on its own
		close(cancelled)
		return ctx.Err()
	}, onceThenBlock(batch("t", 0, 1)))
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

// TestHandlePartitionCommitsOnlyThePrefixThatSucceeded is the money path.
// Committing past a failed record would mark it done and lose it; skipping
// just that one would break the ordering the partition exists to provide.
func TestHandlePartitionCommitsOnlyThePrefixThatSucceeded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failAt   int // index that errors, or -1 for none
		wantOK   int
		wantSeen int
	}{
		{"all succeed", -1, 4, 4},
		{"first fails", 0, 0, 1},
		{"third fails", 2, 2, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen int
			c, err := New(Config{Brokers: []string{"b"}, Topic: "t", Group: "g"}, quiet(),
				func(context.Context, Message) error {
					seen++
					if tc.failAt >= 0 && seen-1 == tc.failAt {
						return errors.New("boom")
					}
					return nil
				})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			c.ctx, c.cancel = context.WithCancel(context.Background())
			defer c.cancel()

			recs := []*kgo.Record{{Offset: 0}, {Offset: 1}, {Offset: 2}, {Offset: 3}}
			ok := c.handlePartition(recs)
			if len(ok) != tc.wantOK {
				t.Errorf("committable = %d records, want %d", len(ok), tc.wantOK)
			}
			if seen != tc.wantSeen {
				t.Errorf("handler saw %d records, want %d (it must stop at the failure)", seen, tc.wantSeen)
			}
		})
	}
}

// TestHandlerPanicIsRecovered pins that one bad message does not take the
// process down, and that its offset is not committed either.
func TestHandlerPanicIsRecovered(t *testing.T) {
	c, err := New(Config{Brokers: []string{"b"}, Topic: "t", Group: "g"}, quiet(),
		func(context.Context, Message) error { panic("handler blew up") })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	defer c.cancel()

	ok := c.handlePartition([]*kgo.Record{{Offset: 0}, {Offset: 1}})
	if len(ok) != 0 {
		t.Errorf("committable = %d records after a panic, want 0", len(ok))
	}
}

// TestMessageCarriesTheRecordFields pins the mapping a handler reads,
// including the header slice becoming a map.
func TestMessageCarriesTheRecordFields(t *testing.T) {
	got := make(chan Message, 1)
	c, err := New(Config{Brokers: []string{"b"}, Topic: "t", Group: "g"}, quiet(),
		func(_ context.Context, m Message) error { got <- m; return nil })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	defer c.cancel()

	when := time.Unix(1700000000, 0)
	if err := c.call(&kgo.Record{
		Key: []byte("k"), Value: []byte("v"),
		Topic: "orders", Partition: 7, Offset: 42, Timestamp: when,
		Headers: []kgo.RecordHeader{{Key: "trace", Value: []byte("abc")}},
	}); err != nil {
		t.Fatalf("call: %v", err)
	}
	m := <-got
	if string(m.Key) != "k" || string(m.Value) != "v" || m.Topic != "orders" {
		t.Errorf("Message = %+v, want the record's fields", m)
	}
	if m.Partition != 7 || m.Offset != 42 || !m.Time.Equal(when) {
		t.Errorf("Message coordinates = %d/%d/%v, want 7/42/%v", m.Partition, m.Offset, m.Time, when)
	}
	if string(m.Headers["trace"]) != "abc" {
		t.Errorf("Headers = %v, want trace=abc", m.Headers)
	}
}

// TestRefusalTellsRefusalFromOutage pins which fetch errors end up on the
// death channel. A rebalance and a broker restart are retriable and franz-go
// handles them; revoked credentials are not and will not fix themselves.
func TestRefusalTellsRefusalFromOutage(t *testing.T) {
	c, err := New(Config{Brokers: []string{"b"}, Topic: "t", Group: "g"}, quiet(),
		func(context.Context, Message) error { return nil })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fetchWith := func(e error) kgo.Fetches {
		return kgo.Fetches{{Topics: []kgo.FetchTopic{{
			Topic:      "t",
			Partitions: []kgo.FetchPartition{{Partition: 0, Err: e}},
		}}}}
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool // want a death
	}{
		{"topic authorization failed", kerr.TopicAuthorizationFailed, true},
		{"group authorization failed", kerr.GroupAuthorizationFailed, true},
		{"sasl authentication failed", kerr.SaslAuthenticationFailed, true},
		{"unknown topic is retriable", kerr.UnknownTopicOrPartition, false},
		{"poll cancelled", context.Canceled, false},
		{"plain network error", errors.New("dial tcp: connection refused"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := c.refusal(fetchWith(tc.err)) != nil
			if got != tc.want {
				t.Errorf("refusal(%v) is a death = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestStopBeforeStartIsNotAnError pins that a Component the App never started
// — because an earlier Tier failed — can still be stopped.
func TestStopBeforeStartIsNotAnError(t *testing.T) {
	c, err := New(Config{Brokers: []string{"b"}, Topic: "t", Group: "g"}, quiet(),
		func(context.Context, Message) error { return nil })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Stop(t.Context()); err != nil {
		t.Errorf("Stop before Start = %v, want nil", err)
	}
}
