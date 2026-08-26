package kafka

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/squall-chua/go-boot/internal/brokertest"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// These are the tests #51 exists for. Everything in kafka_test.go drives the
// fetch loop with a poller of the test's own, which is the right test for
// what #49 settled and says nothing about a rebalance, a redelivery or a
// commit that lands after a partition moved. These run the same Component
// against a real broker in Docker, and skip unless GOBOOT_BROKER_TESTS=1.

// ping reports whether the broker is answering yet. brokertest calls it in a
// loop, because a container that is running and a broker that is listening
// are minutes apart on a cold JVM.
func ping(broker string) error {
	cl, err := kgo.NewClient(kgo.SeedBrokers(broker))
	if err != nil {
		return err
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cl.Ping(ctx)
}

// produce publishes one record per value and waits for every ack. The topic
// is auto-created at the container's partition count on the first call.
func produce(t *testing.T, broker, topic string, values ...string) {
	t.Helper()
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(broker),
		kgo.AllowAutoTopicCreation(),
	)
	if err != nil {
		t.Fatalf("produce: client: %v", err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, v := range values {
		r := &kgo.Record{Topic: topic, Key: []byte(v), Value: []byte(v)}
		if err := cl.ProduceSync(ctx, r).FirstErr(); err != nil {
			t.Fatalf("produce %q: %v", v, err)
		}
	}
}

// noCommit is what the broker reports for a partition a group has never
// committed.
const noCommit = int64(-1)

// committed reports the offset the group has committed for one partition, as
// the broker sees it.
//
// It builds the request out of kmsg rather than using franz-go's kadm,
// because kadm is a separate module and #51's own note says
// .github/module-counts.txt must not move. kmsg is already in goboot/kafka's
// linked set — docs/spec.md 7 has the row — so this costs nothing but the
// twelve lines.
func committed(t *testing.T, broker, group, topic string, partition int32) int64 {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(broker))
	if err != nil {
		t.Fatalf("committed: client: %v", err)
	}
	defer cl.Close()

	rt := kmsg.NewOffsetFetchRequestTopic()
	rt.Topic = topic
	rt.Partitions = []int32{partition}
	req := kmsg.NewPtrOffsetFetchRequest()
	req.Version = 7 // the last version that takes one group in Group/Topics
	req.Group = group
	req.Topics = []kmsg.OffsetFetchRequestTopic{rt}
	req.RequireStable = true

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := cl.Request(ctx, req)
	if err != nil {
		t.Fatalf("committed: OffsetFetch: %v", err)
	}
	resp, ok := raw.(*kmsg.OffsetFetchResponse)
	if !ok {
		t.Fatalf("committed: OffsetFetch answered %T", raw)
	}
	for _, ft := range resp.Topics {
		for _, fp := range ft.Partitions {
			if ft.Topic == topic && fp.Partition == partition {
				if fp.ErrorCode != 0 {
					t.Fatalf("committed: partition %d: error code %d", partition, fp.ErrorCode)
				}
				return fp.Offset
			}
		}
	}
	t.Fatalf("committed: OffsetFetch said nothing about %s/%d", topic, partition)
	return 0
}

// seen collects the keys a handler was given, in the order they arrived.
type seen struct {
	mu   sync.Mutex
	keys []string
}

func (s *seen) add(k string) {
	s.mu.Lock()
	s.keys = append(s.keys, k)
	s.mu.Unlock()
}

func (s *seen) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.keys...)
}

// unique reports the distinct keys, sorted.
func (s *seen) unique() []string {
	set := map[string]bool{}
	for _, k := range s.list() {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// has reports whether key ever reached a handler.
func (s *seen) has(key string) bool {
	for _, k := range s.list() {
		if k == key {
			return true
		}
	}
	return false
}

// start builds a Component against the real broker and starts it, failing the
// test if either step does. Stop is left to the caller, because when and with
// what budget it is called is the thing under test.
func start(t *testing.T, broker, topic, group string, h Handler) *Component {
	t.Helper()
	c, err := New(Config{Brokers: []string{broker}, Topic: topic, Group: group}, quiet(), h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return c
}

// stop shuts a Component down with a budget it is expected to finish inside,
// and fails the test if it does not.
func stop(t *testing.T, c *Component, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Stop(ctx); err != nil {
		t.Errorf("Stop %s: %v", name, err)
	}
}

// awaitAssigned publishes probe records until one comes back, and reports the
// probe key that did.
//
// It exists because the group starts at the END of the topic: a record
// published before this member was assigned its partitions is behind the
// reset point and will never be delivered. Sleeping instead would be a guess,
// and the guess is what makes a broker test flaky.
func awaitAssigned(t *testing.T, broker, topic string, s *seen) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		probe := fmt.Sprintf("probe%d", i)
		produce(t, broker, topic, probe)
		for wait := time.Now().Add(2 * time.Second); time.Now().Before(wait); {
			if s.has(probe) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	t.Fatal("no member was assigned a partition in 60s")
}

// TestARealBrokerStartsANewGroupAtTheEndOfTheTopic pins the one default #51
// moved. franz-go resets a group with no committed offset to the OLDEST
// record on the topic; kafka.Component.Start overrides that to the newest, so
// a typo in kafka.group cannot replay a month of retention.
//
// docs/spec.md 12 freezes defaults for the life of v1, which is what makes
// this worth a test of its own rather than a line of prose: a client upgrade
// that changed it, or an option dropped in a refactor, fails here.
func TestARealBrokerStartsANewGroupAtTheEndOfTheTopic(t *testing.T) {
	broker := brokertest.Kafka(t, 1, ping)
	const topic, group = "reset", "g"

	// Older than the group, and therefore never its business.
	produce(t, broker, topic, "before")

	var got seen
	c := start(t, broker, topic, group, func(_ context.Context, m Message) error {
		got.add(string(m.Key))
		return nil
	})
	defer stop(t, c, "the consumer")

	// A probe published after Start is the proof the member is assigned and
	// fetching. If "before" were going to arrive, it would have arrived by
	// now: it sits at a lower offset on the same partition.
	awaitAssigned(t, broker, topic, &got)
	if got.has("before") {
		t.Errorf("a new group was given %q, which predates it: the reset offset is the start of the topic, want the end", "before")
	}
}

// TestARealBrokerRedeliversAfterAStopThatTimedOut is the second half of the
// at-least-once claim in this package's doc comment. kafka_test.go proves
// Stop gives up on a handler that will not return; only a broker can say what
// happens to that handler's message afterwards, and the answer has to be that
// the offset is still uncommitted and somebody else gets it.
func TestARealBrokerRedeliversAfterAStopThatTimedOut(t *testing.T) {
	broker := brokertest.Kafka(t, 1, ping)
	const topic, group = "redelivery", "g"

	// The group has to own a committed offset before the interesting part,
	// or "uncommitted" has nothing to be measured against. A warm-up member
	// with a handler that returns gets one: its Stop is clean, so the fetch
	// loop finishes its commit before Stop returns.
	var warmed seen
	warm := start(t, broker, topic, group, func(_ context.Context, m Message) error {
		warmed.add(string(m.Key))
		return nil
	})
	awaitAssigned(t, broker, topic, &warmed)
	stop(t, warm, "the warm-up member")

	base := committed(t, broker, group, topic, 0)
	if base == noCommit {
		t.Fatalf("the warm-up member committed nothing, so there is no baseline")
	}

	// held is never closed until the test is over: this is the handler that
	// ignores its cancelled context, which is the case Stop's budget exists
	// for.
	held := make(chan struct{})
	t.Cleanup(func() { close(held) })
	arrived := make(chan string, 8)

	stuck := start(t, broker, topic, group, func(_ context.Context, m Message) error {
		arrived <- string(m.Key)
		<-held
		return nil
	})
	produce(t, broker, topic, "a", "b", "c")

	select {
	case k := <-arrived:
		if k != "a" {
			t.Fatalf("first delivery = %q, want %q", k, "a")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("no delivery from a real broker in 60s")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := stuck.Stop(ctx); err == nil {
		t.Fatal("Stop returned nil with a handler still running, want the budget to have expired")
	}

	// The offset itself, read back from the broker. This is what the ticket
	// asks for in as many words, and it is stronger than watching the record
	// arrive again: it says the group never claimed the record as done.
	if after := committed(t, broker, group, topic, 0); after != base {
		t.Errorf("committed offset moved from %d to %d across a Stop that timed out, want it to stand still", base, after)
	}

	// And the consequence: a fresh member of the same group is given all
	// three again.
	var again seen
	all := make(chan struct{})
	var once sync.Once
	fresh := start(t, broker, topic, group, func(_ context.Context, m Message) error {
		again.add(string(m.Key))
		if len(again.unique()) >= 3 {
			once.Do(func() { close(all) })
		}
		return nil
	})
	defer stop(t, fresh, "the fresh member")

	select {
	case <-all:
	case <-time.After(60 * time.Second):
		t.Fatalf("the fresh member saw %v, want all three redelivered", again.unique())
	}
}

// TestARealBrokerRebalancesWhileAHandlerIsInFlight moves a partition to a
// second member while the first is mid-message. That is the case #51 names
// first and the fake fetch loop cannot reach at all: the fake never revokes
// anything, so it never asks what a commit does to a partition this member no
// longer owns.
//
// What it asserts is the guarantee the package claims and not more: every
// record reaches a handler at least once, and the partition genuinely moved.
// Duplicates are legal — that is what at-least-once means — so the count is
// logged rather than pinned, because it is a property of the timing rather
// than of the code.
func TestARealBrokerRebalancesWhileAHandlerIsInFlight(t *testing.T) {
	const (
		partitions = 4
		// Enough work per partition that the first member is still busy when
		// the second one has finished joining: a rebalance takes a second or
		// two, and forty records at 150 ms across four partitions were gone
		// before it landed.
		records = 120
		topic   = "rebalance"
		group   = "g"
	)
	broker := brokertest.Kafka(t, partitions, ping)

	var all seen
	var where partitions4 // which partition each key arrived on
	var firstCount, secondCount atomic.Int64
	done := make(chan struct{})
	var once sync.Once
	// Slow enough that a handler is always in flight when the second member
	// joins, fast enough that all of them still finish.
	handler := func(n *atomic.Int64) Handler {
		return func(_ context.Context, m Message) error {
			time.Sleep(150 * time.Millisecond)
			all.add(string(m.Key))
			where.note(string(m.Key), m.Partition)
			n.Add(1)
			// >= and not ==: handlers run concurrently across partitions, so
			// the goroutine that adds the last key is not necessarily the one
			// that reads the count at exactly the target.
			if len(all.unique())-probes(all.unique()) >= records {
				once.Do(func() { close(done) })
			}
			return nil
		}
	}

	// Give the group a committed offset before the rebalance, so this test
	// measures the rebalance rather than the reset offset.
	//
	// This is not decoration. Without it the test fails about one run in
	// three, losing a handful of records — measured, and always the same
	// ones. A partition moving to a member that the group has never
	// committed for makes that member apply the reset offset, which is the
	// END of the topic, so whatever the previous owner had in flight is
	// skipped. That window is real, it is what `latest` means on any Kafka
	// client, and docs/spec.md 9 carries it as a known gap. It is a
	// different fact from the one this test exists to pin, and leaving the
	// two tangled would mean neither is pinned.
	var warmed seen
	warm := start(t, broker, topic, group, func(_ context.Context, m Message) error {
		warmed.add(string(m.Key))
		return nil
	})
	awaitAssigned(t, broker, topic, &warmed)
	seed := make([]string, 40)
	for i := range seed {
		seed[i] = fmt.Sprintf("s%03d", i)
	}
	produce(t, broker, topic, seed...)
	for deadline := time.Now().Add(60 * time.Second); len(warmed.unique())-probes(warmed.unique()) < len(seed); {
		if time.Now().After(deadline) {
			t.Fatalf("warm-up saw only %d of %d", len(warmed.unique())-probes(warmed.unique()), len(seed))
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop(t, warm, "the warm-up member")

	first := start(t, broker, topic, group, handler(&firstCount))
	defer stop(t, first, "the first member")
	awaitAssigned(t, broker, topic, &all)

	values := make([]string, records)
	for i := range values {
		values[i] = fmt.Sprintf("m%03d", i)
	}
	produce(t, broker, topic, values...)

	// Wait until the first member is genuinely mid-flight before disturbing
	// it. Joining before it owns anything would rebalance nothing.
	deadline := time.Now().Add(60 * time.Second)
	for !all.has("m000") {
		if time.Now().After(deadline) {
			t.Fatalf("the first member handled none of the %d in 60s", records)
		}
		time.Sleep(50 * time.Millisecond)
	}

	second := start(t, broker, topic, group, handler(&secondCount))
	defer stop(t, second, "the second member")

	select {
	case <-done:
	case <-time.After(90 * time.Second):
		var missing []string
		for i := range records {
			if k := fmt.Sprintf("m%03d", i); !all.has(k) {
				missing = append(missing, k)
			}
		}
		t.Fatalf("only %d of %d reached a handler (first=%d second=%d); lost %d: %v\nwhere each key went: %v",
			len(all.unique())-probes(all.unique()), records,
			firstCount.Load(), secondCount.Load(), len(missing), missing, where.dump())
	}
	// Without this the test passes when the first member simply finished
	// everything before the second one joined, which proves nothing about a
	// partition moving.
	if secondCount.Load() == 0 {
		t.Fatalf("the second member handled nothing, so no partition moved: first=%d", firstCount.Load())
	}
	t.Logf("rebalance: %d records, %d deliveries (first=%d second=%d)",
		records, len(all.list()), firstCount.Load(), secondCount.Load())
}

// probes counts the awaitAssigned keys mixed into a key set, so a record
// count does not have to know how many probes it took to see the assignment.
func probes(keys []string) int {
	n := 0
	for _, k := range keys {
		if len(k) > 5 && k[:5] == "probe" {
			n++
		}
	}
	return n
}

// partitions4 records which partition each key arrived on, so a failure can
// say whether the lost records cluster on one partition — which is what a
// partition moving to a member that reset past them would look like.
type partitions4 struct {
	mu sync.Mutex
	at map[string]int32
}

func (p *partitions4) note(key string, partition int32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.at == nil {
		p.at = map[string]int32{}
	}
	p.at[key] = partition
}

func (p *partitions4) dump() map[string]int32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]int32, len(p.at))
	for k, v := range p.at {
		out[k] = v
	}
	return out
}
