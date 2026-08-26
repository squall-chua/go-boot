// Package kafka is the Kafka consumer Starter. One Component consumes one
// topic as one member of one consumer group: two topics is New called twice
// and Add called twice, which is what keeps the routing table and the
// per-topic handler map out of this package.
//
// It is a heavy optional package. Nothing on a short path may import it, and
// a service that consumes from RabbitMQ links none of it (docs/spec.md 8.1).
//
// Delivery is at-least-once. A handler still running when the stop budget
// expires has its context cancelled with its offset uncommitted, so the
// message goes to whoever picks up the partition. Handlers must be idempotent.
//
// A consumer group the broker has never seen starts at the NEWEST record, so
// nothing published before the group existed is delivered. That is the Java
// client's default rather than franz-go's, and it means a typo in kafka.group
// skips a window rather than replaying the whole topic. A group that has
// committed always resumes from its commit.
//
// That default has one cost, and it is the single exception to at-least-once
// above. Between a group's first join and its first commit on a partition,
// the group owns no offset for it. If the partition moves to another member
// in that window — a second pod starting seconds after the first, on a group
// nobody has run before — the new owner applies the same rule and starts at
// the newest record, so whatever the previous owner had in flight is never
// handled. Measured against a real broker at 3 records in 120 (#51). The
// window closes at the first commit and cannot reopen, and it is what
// "latest" means on any Kafka client rather than anything this package does.
// A service that cannot accept it wants its group's offsets seeded before it
// starts.
package kafka

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/squall-chua/go-boot"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

// The SASL mechanisms this Starter knows. Anything else is a config error
// naming the set, rather than a connection that fails later for no clear
// reason.
const (
	saslPlain  = "plain"
	saslSHA256 = "scram-sha-256"
	saslSHA512 = "scram-sha-512"
)

// Config is the kafka section.
type Config struct {
	Brokers []string `yaml:"brokers"` // required, at least one
	Topic   string   `yaml:"topic"`   // required
	Group   string   `yaml:"group"`   // required, the consumer group id

	TLS  bool       `yaml:"tls"`  // false
	SASL SASLConfig `yaml:"sasl"` // off when Mechanism is empty
}

// SASLConfig is the credentials, and nothing about them is optional once
// Mechanism is set.
type SASLConfig struct {
	Mechanism string `yaml:"mechanism"` // "plain", "scram-sha-256", "scram-sha-512"
	Username  string `yaml:"username"`
	Password  string `yaml:"password"` // from an environment variable
}

// Message is what a Handler is given. It is Kafka's shape, not one shared
// with the RabbitMQ Starter: committing a partition offset and acknowledging
// a delivery tag are different acts, and a type covering both would leave
// half its fields zero on either broker.
type Message struct {
	Key, Value []byte
	Topic      string
	Partition  int32
	Offset     int64
	Headers    map[string][]byte
	Time       time.Time
}

// Handler processes one message. Returning an error means the offset is NOT
// committed, so this message and everything after it on the same partition is
// delivered again. A panic is recovered and treated as an error, because one
// bad message must not take the process down with every other in flight.
//
// ctx is NOT the ctx Start was given, which goboot.Component documents as
// unsafe to keep. It descends from a context this Component makes for itself
// in Start and cancels in Stop, so a handler still running when the stop
// budget expires sees its ctx cancelled and must return.
type Handler func(ctx context.Context, m Message) error

// poller and committer are the two calls the fetch loop makes into the
// client. Naming them keeps consume's signature readable and is what lets the
// tests drive the loop without a cluster.
type (
	poller    func(context.Context) kgo.Fetches
	committer func(context.Context, ...*kgo.Record) error
)

// Component consumes one topic. It is a goboot.Component, a goboot.Drainer
// and a goboot.Checker.
type Component struct {
	cfg Config
	log *slog.Logger
	h   Handler
	cl  *kgo.Client

	// ctx is the handlers' lifetime, made in Start and cancelled in Stop.
	// pollCtx is only the fetch loop's, and Drain cancels it: that is how
	// "stop taking new work" is said to a client that is otherwise happy to
	// keep fetching.
	ctx        context.Context
	cancel     context.CancelFunc
	pollCtx    context.Context
	pollCancel context.CancelFunc

	errc chan error    // the death channel handed back by Start
	done chan struct{} // closed when the fetch loop has returned

	closing atomic.Bool

	mu    sync.Mutex
	fatal error // what Check reports
}

// New builds the Component. It validates its own config here and leaves
// anything needing the world to Start, which is the split docs/spec.md 4.0
// and ADR 0011 require of every Starter.
//
// A nil logger falls back to slog.Default() rather than panicking later.
func New(cfg Config, log *slog.Logger, h Handler) (*Component, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("kafka.brokers: required, at least one")
	}
	for _, b := range cfg.Brokers {
		if b == "" {
			return nil, errors.New("kafka.brokers: contains an empty entry")
		}
	}
	if cfg.Topic == "" {
		return nil, errors.New("kafka.topic: required")
	}
	if cfg.Group == "" {
		return nil, errors.New("kafka.group: required")
	}
	if _, err := mechanism(cfg.SASL); err != nil {
		return nil, err
	}
	if h == nil {
		return nil, errors.New("kafka: New needs a Handler")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Component{
		cfg:  cfg,
		log:  log,
		h:    h,
		errc: make(chan error, 1),
		done: make(chan struct{}),
	}, nil
}

// mechanism turns the config into a franz-go SASL mechanism, or nil when SASL
// is off. It is called by New so a bad mechanism name is a startup error
// rather than a puzzling handshake failure.
func mechanism(c SASLConfig) (sasl.Mechanism, error) {
	if c.Mechanism == "" {
		return nil, nil
	}
	if c.Username == "" || c.Password == "" {
		return nil, errors.New("kafka.sasl: mechanism is set, so username and password are required")
	}
	switch c.Mechanism {
	case saslPlain:
		return plain.Auth{User: c.Username, Pass: c.Password}.AsMechanism(), nil
	case saslSHA256:
		return scram.Auth{User: c.Username, Pass: c.Password}.AsSha256Mechanism(), nil
	case saslSHA512:
		return scram.Auth{User: c.Username, Pass: c.Password}.AsSha512Mechanism(), nil
	}
	return nil, fmt.Errorf("kafka.sasl.mechanism: unknown %q, want one of %q, %q, %q",
		c.Mechanism, saslPlain, saslSHA256, saslSHA512)
}

// Name is the Component name, and the key the Actuator files the Check under.
// The topic is part of it so that two Components on two topics do not collide
// on the App's duplicate-name check.
func (c *Component) Name() string { return "kafka:" + c.cfg.Topic }

// Tier puts the consumer last to start and first to stop, alongside the HTTP
// and gRPC Transports.
func (c *Component) Tier() goboot.Tier { return goboot.TierTransport }

// Start builds the client, checks the brokers answer and begins fetching. The
// ping is what makes an unreachable cluster a startup error rather than a pod
// that reports ready and silently consumes nothing.
func (c *Component) Start(ctx context.Context) (<-chan error, error) {
	m, err := mechanism(c.cfg.SASL) // already validated by New
	if err != nil {
		return nil, err
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(c.cfg.Brokers...),
		kgo.ConsumerGroup(c.cfg.Group),
		kgo.ConsumeTopics(c.cfg.Topic),
		// The whole delivery guarantee rests on committing after the handler
		// returns, so the client must never commit on its own.
		kgo.DisableAutoCommit(),
		// Where a group with no usable committed offset begins: the next
		// record, not the oldest on the topic. franz-go's own default is the
		// oldest, which turns a typo in kafka.group into a replay of the
		// whole retention window, and differs from the Java client every
		// operator's expectation is built on. It decides nothing about a
		// group that has committed — that one always resumes from its commit
		// — so at-least-once is untouched. Measured against a real broker and
		// chosen deliberately before the surface froze (#51); docs/spec.md 12
		// means this default cannot move again inside v1.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()),
	}
	if c.cfg.TLS {
		opts = append(opts, kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	}
	if m != nil {
		opts = append(opts, kgo.SASL(m))
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka: %w", err)
	}
	if err := cl.Ping(ctx); err != nil {
		cl.Close()
		return nil, fmt.Errorf("kafka.brokers: %w", err)
	}
	c.cl = cl
	// Not derived from ctx: goboot.Component documents Start's ctx as
	// cancelled once the start sequence is over, and handlers outlive that.
	base := context.WithoutCancel(ctx)
	c.ctx, c.cancel = context.WithCancel(base)
	c.pollCtx, c.pollCancel = context.WithCancel(base)
	go c.consume(cl.PollFetches, cl.CommitRecords)
	c.log.Info("consuming", "component", c.Name(), "topic", c.cfg.Topic, "group", c.cfg.Group)
	return c.errc, nil
}

// consume fetches batches until Drain stops it or the client goes away.
//
// poll and commit are the client's PollFetches and CommitRecords in
// production. They are parameters because they are the only two things this
// loop asks of the cluster, so passing them in lets the shutdown tests drive
// the real loop with no cluster behind it: what #35 needs proving is when
// this loop lets go, not what Kafka puts in a batch.
func (c *Component) consume(poll poller, commit committer) {
	defer close(c.done)
	for {
		if c.closing.Load() {
			return
		}
		fetches := poll(c.pollCtx)
		if fetches.IsClientClosed() {
			return
		}
		// Drain cancels pollCtx. Anything fetched but not yet handed to a
		// handler is not work in hand, and it is uncommitted, so leaving it
		// is how the next member of the group gets it.
		if c.pollCtx.Err() != nil {
			return
		}
		if err := c.refusal(fetches); err != nil {
			c.setFatal(err)
			c.errc <- err
			return
		}
		c.handleBatch(fetches, commit)
	}
}

// refusal reports the first error in a fetch that will not fix itself. A
// broker restart, a coordinator move and a rebalance all arrive here as
// retriable errors and are left to franz-go, which reconnects on its own; a
// death would turn one cluster hiccup into every consumer in the cluster
// crash-looping, which is the failure mode CONTEXT.md keeps liveness away
// from for the same reason.
func (c *Component) refusal(fetches kgo.Fetches) error {
	for _, fe := range fetches.Errors() {
		if errors.Is(fe.Err, context.Canceled) || kerr.IsRetriable(fe.Err) {
			continue
		}
		var ke *kerr.Error
		if !errors.As(fe.Err, &ke) {
			// Not the broker refusing: a transport problem franz-go retries.
			c.log.Warn("fetch", "component", c.Name(), "partition", fe.Partition, "err", fe.Err)
			continue
		}
		return fmt.Errorf("%s: partition %d: %w", c.Name(), fe.Partition, fe.Err)
	}
	return nil
}

// handleBatch runs one poll's records: partitions concurrently, and each
// partition's records in order on one goroutine.
//
// That split is not a tuning knob, it is Kafka's own model. Partitions are
// the unit of parallelism, and running one partition's records in order is
// what preserves the ordering the broker guarantees. There is no workers key
// because there is nothing left for one to mean.
func (c *Component) handleBatch(fetches kgo.Fetches, commit committer) {
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
			ok := c.handlePartition(recs)
			if len(ok) == 0 {
				return
			}
			mu.Lock()
			done = append(done, ok...)
			mu.Unlock()
		}(p.Records)
	})
	wg.Wait()
	if len(done) == 0 {
		return
	}
	if err := commit(c.ctx, done...); err != nil {
		// Nothing was lost: an uncommitted offset is redelivered, which is
		// what at-least-once means.
		c.log.Error("commit", "component", c.Name(), "records", len(done), "err", err)
	}
}

// handlePartition runs the Handler over one partition's records in order and
// reports the leading run that succeeded.
//
// It stops at the first failure and commits nothing after it. Committing past
// a failed record would mark it done and lose it, and skipping just that one
// would break the ordering the partition exists to provide.
func (c *Component) handlePartition(recs []*kgo.Record) []*kgo.Record {
	for i, r := range recs {
		if err := c.call(r); err != nil {
			c.log.Error("handler",
				"component", c.Name(), "partition", r.Partition,
				"offset", r.Offset, "err", err)
			return recs[:i]
		}
		if c.ctx.Err() != nil {
			// Stop gave up on us. Commit what is genuinely finished and
			// leave the rest for whoever picks up the partition.
			return recs[:i+1]
		}
	}
	return recs
}

// call invokes the Handler and turns a panic into an error. Without this one
// bad message takes the process down and every other message in flight with
// it, which is the same reason both Transports recover.
func (c *Component) call(r *kgo.Record) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("handler panicked: %v", p)
		}
	}()
	headers := make(map[string][]byte, len(r.Headers))
	for _, h := range r.Headers {
		headers[h.Key] = h.Value
	}
	return c.h(c.ctx, Message{
		Key:       r.Key,
		Value:     r.Value,
		Topic:     r.Topic,
		Partition: r.Partition,
		Offset:    r.Offset,
		Headers:   headers,
		Time:      r.Timestamp,
	})
}

// Drain stops the client fetching and returns at once. It does NOT wait for
// the handlers in flight, and must not: goboot.Drainer's ctx has no deadline
// and cannot be cancelled, so a Drain that waited would block the whole
// shutdown with nothing able to interrupt it (#49). The waiting is in Stop,
// which has StopTimeout behind it.
func (c *Component) Drain(context.Context) {
	c.closing.Store(true)
	if c.pollCancel != nil {
		c.pollCancel()
	}
	c.log.Info("draining", "component", c.Name())
}

// Stop waits for the handlers in flight to finish, then leaves the group and
// closes the client. It gives up when ctx expires — which carries StopTimeout
// — cancels the handlers' context and closes anyway, so a handler that will
// not return cannot hang the shutdown. Whatever it was working on is
// uncommitted, so the group delivers it again.
func (c *Component) Stop(ctx context.Context) error {
	if c.cancel == nil {
		return nil // never started
	}
	c.Drain(ctx) // idempotent; covers a Stop with no Drain before it
	var err error
	select {
	case <-c.done:
	case <-ctx.Done():
		err = fmt.Errorf("handler still running: %w", ctx.Err())
	}
	c.cancel()
	if c.cl != nil {
		// Close commits nothing on its own — DisableAutoCommit is set — but
		// it does leave the group, so the partitions move without waiting
		// for the session timeout to expire.
		c.cl.Close()
	}
	return err
}

// Check reports the error the fetch loop gave up on, and nothing else. It
// touches no network: CONTEXT.md requires a Check to finish inside the probe's
// own deadline, and one that reached the cluster would turn a broker blip into
// an unready pod. An idle topic is not a fault, so silence reports healthy.
func (c *Component) Check(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fatal
}

// setFatal records what Check reports.
func (c *Component) setFatal(err error) {
	c.mu.Lock()
	c.fatal = err
	c.mu.Unlock()
}
