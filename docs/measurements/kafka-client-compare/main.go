// Command compare answers the one unmeasured claim in docs/spec.md 14:
// franz-go against segmentio/kafka-go on consumer-group correctness. The
// recipe is the one that section names — a three-broker cluster, one consumer
// group of two members, a rolling restart, and a count of duplicated and
// dropped messages on each client.
//
// Both consumers keep goboot/kafka's discipline: auto-commit off, handle the
// record, then commit it. That is the only thing under test; everything else
// is held equal.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	segmentio "github.com/segmentio/kafka-go"
	"github.com/twmb/franz-go/pkg/kgo"
)

var (
	client  = flag.String("client", "franz", "franz or segmentio")
	topic   = flag.String("topic", "compare", "topic")
	group   = flag.String("group", "compare", "consumer group")
	count   = flag.Int("n", 30000, "records to produce")
	work    = flag.Duration("work", 20*time.Millisecond, "time each handler takes")
	budget  = flag.Duration("budget", 15*time.Minute, "give up after this")
	brokers = []string{"127.0.0.1:19092", "127.0.0.1:19093", "127.0.0.1:19094"}
	nodes   = []string{
		"goboot-kafka-compare-kafka1-1",
		"goboot-kafka-compare-kafka2-1",
		"goboot-kafka-compare-kafka3-1",
	}
)

// tally counts how often each key reached a handler, across both members.
type tally struct {
	mu   sync.Mutex
	seen map[string]int
	n    int // total deliveries
}

func newTally() *tally { return &tally{seen: map[string]int{}} }

func (t *tally) add(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seen[key]++
	t.n++
	return len(t.seen)
}

func (t *tally) report(want int) (distinct, deliveries, dup, missing int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.seen {
		if c > 1 {
			dup += c - 1
		}
	}
	return len(t.seen), t.n, dup, want - len(t.seen)
}

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime)

	produce(*count)

	t := newTally()
	full := make(chan struct{})
	var once sync.Once
	handled := func(key string) {
		time.Sleep(*work)
		if t.add(key) == *count {
			once.Do(func() { close(full) })
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *budget)
	defer cancel()
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(member int) {
			defer wg.Done()
			switch *client {
			case "franz":
				franzMember(ctx, member, handled)
			case "segmentio":
				segmentioMember(ctx, member, handled)
			default:
				log.Fatalf("unknown -client %q", *client)
			}
		}(i)
	}

	// Wait until both members are genuinely working before disturbing the
	// cluster, or the restart lands on an empty group and proves nothing.
	// The workload is sized so both clients are still mid-flight when the
	// roll finishes: a run where consumption ends first measures nothing,
	// which the first attempt at this proved by reporting a clean 0/0.
	waitFor(ctx, func() bool { d, _, _, _ := t.report(*count); return d > 200 })
	roll()

	select {
	case <-full:
		log.Printf("every record reached a handler")
	case <-ctx.Done():
		log.Printf("budget expired")
	}
	cancel()
	wg.Wait()

	distinct, deliveries, dup, missing := t.report(*count)
	fmt.Printf("\nclient=%s produced=%d distinct=%d deliveries=%d duplicated=%d dropped=%d\n",
		*client, *count, distinct, deliveries, dup, missing)
	if missing > 0 {
		fmt.Printf("dropped keys: %v\n", missingKeys(t, *count))
		os.Exit(1)
	}
}

func missingKeys(t *tally, want int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for i := range want {
		k := fmt.Sprintf("k%06d", i)
		if t.seen[k] == 0 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	if len(out) > 20 {
		return append(out[:20], "...")
	}
	return out
}

func waitFor(ctx context.Context, ok func() bool) {
	for !ok() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// produce fills the topic. One producer for both clients, so the only
// difference measured is the consumer group.
func produce(n int) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.AllowAutoTopicCreation(),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerBatchMaxBytes(1<<20),
	)
	if err != nil {
		log.Fatalf("producer: %v", err)
	}
	defer cl.Close()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range n {
		k := fmt.Sprintf("k%06d", i)
		wg.Add(1)
		cl.Produce(ctx, &kgo.Record{Topic: *topic, Key: []byte(k), Value: []byte(k)},
			func(_ *kgo.Record, err error) {
				defer wg.Done()
				if err != nil {
					log.Fatalf("produce %s: %v", k, err)
				}
			})
	}
	wg.Wait()
	log.Printf("produced %d records to %s", n, *topic)
}

// roll restarts each broker in turn, waiting between them, which is the
// rolling restart docs/spec.md 14 asks for.
func roll() {
	for _, n := range nodes {
		log.Printf("restarting %s", n)
		if out, err := exec.Command("docker", "restart", "-t", "10", n).CombinedOutput(); err != nil {
			log.Fatalf("restart %s: %v: %s", n, err, out)
		}
		time.Sleep(20 * time.Second)
	}
	log.Printf("rolling restart done")
}

// franzMember is goboot/kafka's loop: poll, run the handler per partition in
// order, commit what succeeded.
func franzMember(ctx context.Context, member int, handled func(string)) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(*group),
		kgo.ConsumeTopics(*topic),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		log.Fatalf("franz member %d: %v", member, err)
	}
	defer cl.Close()
	for ctx.Err() == nil {
		fetches := cl.PollFetches(ctx)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			return
		}
		for _, e := range fetches.Errors() {
			log.Printf("franz member %d fetch: %v", member, e.Err)
		}
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			done []*kgo.Record
		)
		fetches.EachPartition(func(p kgo.FetchTopicPartition) {
			if len(p.Records) == 0 {
				return
			}
			wg.Add(1)
			go func(recs []*kgo.Record) {
				defer wg.Done()
				for _, r := range recs {
					handled(string(r.Key))
				}
				mu.Lock()
				done = append(done, recs...)
				mu.Unlock()
			}(p.Records)
		})
		wg.Wait()
		if len(done) == 0 {
			continue
		}
		if err := cl.CommitRecords(ctx, done...); err != nil {
			log.Printf("franz member %d commit %d records: %v", member, len(done), err)
		}
	}
}

// segmentioMember is the same discipline over kafka-go's Reader: FetchMessage
// does not commit, CommitMessages does. The Reader owns the group, so this is
// one message at a time across whatever partitions it holds, which is the
// shape kafka-go offers.
func segmentioMember(ctx context.Context, member int, handled func(string)) {
	r := segmentio.NewReader(segmentio.ReaderConfig{
		Brokers:  brokers,
		GroupID:  *group,
		Topic:    *topic,
		MinBytes: 1,
		MaxBytes: 10 << 20,
	})
	defer func() { _ = r.Close() }()
	for ctx.Err() == nil {
		m, err := r.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("segmentio member %d fetch: %v", member, err)
			continue
		}
		handled(string(m.Key))
		if err := r.CommitMessages(ctx, m); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("segmentio member %d commit: %v", member, err)
		}
	}
}
