// Package rabbit is the RabbitMQ consumer Starter. One Component consumes one
// queue: two queues is New called twice and Add called twice, which is what
// keeps the routing table, the per-queue handler map and the question of what
// one queue's panic does to another out of this package entirely.
//
// It is a heavy optional package. Nothing on a short path may import it, and
// a service that consumes from Kafka links none of it (docs/spec.md 8.1).
//
// Delivery is at-least-once. A handler still running when the stop budget
// expires has its context cancelled with its delivery unacknowledged, so the
// broker gives that message to somebody else. Handlers must be idempotent.
package rabbit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/squall-chua/go-boot"
)

// redialDelay is the pause between reconnection attempts. It is deliberately
// a constant rather than a backoff: a broker that is down for an hour costs
// 3600 dials, which is nothing, and a growing delay would mean a consumer
// that takes minutes to notice the broker came back.
const redialDelay = time.Second

// Config is the rabbit section.
type Config struct {
	URL   string `yaml:"url"`   // required, amqp:// or amqps://; from an environment variable
	Queue string `yaml:"queue"` // required

	// Prefetch is the AMQP QoS window: how many deliveries the broker may
	// have outstanding at once. One handler runs at a time whatever this is,
	// so a larger window buys pipelining, not concurrency.
	Prefetch int `yaml:"prefetch"` // 1

	// DiscardOnError decides what a handler error does to the delivery. The
	// default requeues it, because losing a message to a transient handler
	// failure is the worse mistake. Setting this Nacks without requeue, which
	// dead-letters the message where the queue has a dead-letter exchange and
	// discards it where it does not.
	//
	// Requeueing forever is how a poison message becomes a busy loop. This
	// Starter has no dead-letter policy of its own: configure one on the
	// queue, or set this and let the broker deal with it.
	DiscardOnError bool `yaml:"discardOnError"` // false
}

// Message is what a Handler is given. It is AMQP's shape, not one shared with
// the Kafka Starter: acknowledging a delivery tag and committing a partition
// offset are different acts, and a type covering both would leave half its
// fields zero on either broker.
type Message struct {
	Body        []byte
	Exchange    string
	RoutingKey  string
	Headers     map[string]any
	Redelivered bool
}

// Handler processes one message. Returning an error Nacks the delivery, so it
// is redelivered unless Config.DiscardOnError says otherwise. A panic is
// recovered and treated as an error, because one bad message must not take
// the process down with every other message in flight.
//
// ctx is NOT the ctx Start was given, which goboot.Component documents as
// unsafe to keep. It descends from a context this Component makes for itself
// in Start and cancels in Stop, so a handler still running when the stop
// budget expires sees its ctx cancelled and must return.
type Handler func(ctx context.Context, m Message) error

// Component consumes one queue. It is a goboot.Component, a goboot.Drainer
// and a goboot.Checker.
type Component struct {
	cfg Config
	log *slog.Logger
	h   Handler

	// ctx is the handlers' lifetime, made in Start and cancelled in Stop.
	ctx    context.Context
	cancel context.CancelFunc

	errc chan error    // the death channel handed back by Start
	done chan struct{} // closed when the consume loop has returned

	// closing is set by Drain or Stop and read by the consume loop, which is
	// how it tells "the broker went away, redial" from "we are shutting down,
	// go home".
	closing atomic.Bool

	mu    sync.Mutex
	conn  *amqp.Connection
	ch    *amqp.Channel
	fatal error // what Check reports; set once, by the consume loop
}

// New builds the Component. It validates its own config here and leaves
// anything needing the world to Start, which is the split docs/spec.md 4.0
// and ADR 0011 require of every Starter.
//
// A nil logger falls back to slog.Default() rather than panicking later.
func New(cfg Config, log *slog.Logger, h Handler) (*Component, error) {
	if cfg.URL == "" {
		return nil, errors.New("rabbit.url: required")
	}
	if !strings.HasPrefix(cfg.URL, "amqp://") && !strings.HasPrefix(cfg.URL, "amqps://") {
		return nil, fmt.Errorf("rabbit.url: must begin amqp:// or amqps://, got %q", schemeOf(cfg.URL))
	}
	if cfg.Queue == "" {
		return nil, errors.New("rabbit.queue: required")
	}
	if cfg.Prefetch < 0 {
		return nil, fmt.Errorf("rabbit.prefetch: must not be negative, got %d", cfg.Prefetch)
	}
	if cfg.Prefetch == 0 {
		cfg.Prefetch = 1
	}
	if h == nil {
		return nil, errors.New("rabbit: New needs a Handler")
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

// schemeOf reports the scheme of a URL for an error message, without the
// credentials that follow it. The whole URL must never reach a log line.
func schemeOf(url string) string {
	if i := strings.Index(url, "://"); i >= 0 {
		return url[:i+3]
	}
	return url
}

// Name is the Component name, and the key the Actuator files the Check under.
// The queue is part of it so that two Components on two queues do not collide
// on the App's duplicate-name check.
func (c *Component) Name() string { return "rabbit:" + c.cfg.Queue }

// Tier puts the consumer last to start and first to stop, alongside the HTTP
// and gRPC Transports.
func (c *Component) Tier() goboot.Tier { return goboot.TierTransport }

// Start dials the broker, checks the queue is there and begins consuming. It
// returns once the first delivery could arrive, so a failure to reach the
// broker at boot is a startup error rather than a pod that reports ready and
// silently consumes nothing.
func (c *Component) Start(ctx context.Context) (<-chan error, error) {
	deliveries, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	// Not derived from ctx: goboot.Component documents Start's ctx as
	// cancelled once the start sequence is over, and handlers outlive that.
	c.ctx, c.cancel = context.WithCancel(context.WithoutCancel(ctx))
	go c.consume(deliveries)
	c.log.Info("consuming", "component", c.Name(), "queue", c.cfg.Queue, "prefetch", c.cfg.Prefetch)
	return c.errc, nil
}

// dial opens a connection, a channel and a consumer, and reports the delivery
// stream. It declares nothing: QueueDeclarePassive asks whether the queue is
// there and fails if it is not. Creating it here would turn a typo in
// rabbit.queue into a new empty queue that nothing ever publishes to, and
// topology belongs to whoever owns the broker.
func (c *Component) dial(ctx context.Context) (<-chan amqp.Delivery, error) {
	conn, err := amqp.Dial(c.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", schemeOf(c.cfg.URL), err)
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("channel: %w", err)
	}
	if _, err := ch.QueueDeclarePassive(c.cfg.Queue, true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("rabbit.queue: %q is not on the broker: %w", c.cfg.Queue, err)
	}
	if err := ch.Qos(c.cfg.Prefetch, 0, false); err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("rabbit.prefetch: %w", err)
	}
	// autoAck is false and stays false: this Starter's whole delivery
	// guarantee rests on acknowledging after the handler returns.
	deliveries, err := ch.Consume(c.cfg.Queue, c.consumerTag(), false, false, false, false, nil)
	if err != nil {
		ch.Close()
		conn.Close()
		return nil, fmt.Errorf("consume %q: %w", c.cfg.Queue, err)
	}
	c.mu.Lock()
	c.conn, c.ch = conn, ch
	c.mu.Unlock()
	return deliveries, nil
}

// consumerTag is the name this consumer registers under, which is what
// Drain cancels and what shows up in the broker's management UI.
func (c *Component) consumerTag() string { return "goboot-" + c.cfg.Queue }

// consume runs one handler at a time until the stream closes, then decides
// whether that was a shutdown or a broker that went away.
func (c *Component) consume(deliveries <-chan amqp.Delivery) {
	defer close(c.done)
	for {
		for d := range deliveries {
			c.handle(d)
		}
		// The stream is closed. Drain and Stop both close it on purpose.
		if c.closing.Load() {
			return
		}
		next, err := c.redial()
		if err != nil {
			c.setFatal(err)
			c.errc <- err
			return
		}
		if next == nil { // shutdown overtook the redial
			return
		}
		deliveries = next
	}
}

// redial reconnects after the broker went away, and keeps trying until it
// works, this Component is shut down, or the broker says something that will
// not fix itself.
//
// A dropped connection is not a death: a broker restart or a failover is
// ordinary, and killing the pod for one would turn an outage into every
// consumer in the cluster crash-looping, which is the failure mode CONTEXT.md
// keeps liveness away from for the same reason.
func (c *Component) redial() (<-chan amqp.Delivery, error) {
	c.closeBroker()
	for attempt := 1; ; attempt++ {
		select {
		case <-c.ctx.Done():
			return nil, nil
		case <-time.After(redialDelay):
		}
		if c.closing.Load() {
			return nil, nil
		}
		deliveries, err := c.dial(c.ctx)
		if err == nil {
			c.log.Info("reconnected", "component", c.Name(), "attempts", attempt)
			return deliveries, nil
		}
		if permanent(err) {
			return nil, fmt.Errorf("%s: %w", c.Name(), err)
		}
		c.log.Warn("reconnecting", "component", c.Name(), "attempt", attempt, "err", err)
	}
}

// permanent reports whether err is the broker refusing rather than the
// network failing. A queue that has been deleted and credentials that have
// been revoked do not fix themselves, so retrying only hides them; those go
// on the death channel, and a death is fatal.
func permanent(err error) bool {
	var ae *amqp.Error
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.Code {
	case amqp.AccessRefused, amqp.NotFound, amqp.NotAllowed:
		return true
	}
	return false
}

// handle runs the Handler over one delivery and acknowledges the result.
func (c *Component) handle(d amqp.Delivery) {
	err := c.call(d)
	if err != nil {
		c.log.Error("handler",
			"component", c.Name(), "routingKey", d.RoutingKey,
			"redelivered", d.Redelivered, "err", err)
		if nerr := d.Nack(false, !c.cfg.DiscardOnError); nerr != nil {
			c.log.Error("nack", "component", c.Name(), "err", nerr)
		}
		return
	}
	if aerr := d.Ack(false); aerr != nil {
		c.log.Error("ack", "component", c.Name(), "err", aerr)
	}
}

// call invokes the Handler and turns a panic into an error. Without this one
// bad message takes the process down and every other message in flight with
// it, which is the same reason both Transports recover.
func (c *Component) call(d amqp.Delivery) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panicked: %v", r)
		}
	}()
	return c.h(c.ctx, Message{
		Body:        d.Body,
		Exchange:    d.Exchange,
		RoutingKey:  d.RoutingKey,
		Headers:     d.Headers,
		Redelivered: d.Redelivered,
	})
}

// Drain stops the broker sending new deliveries and returns at once. It does
// NOT wait for the handler in flight, and must not: goboot.Drainer's ctx has
// no deadline and cannot be cancelled, so a Drain that waited would block the
// whole shutdown with nothing able to interrupt it (#49). The waiting is in
// Stop, which has StopTimeout behind it.
func (c *Component) Drain(context.Context) {
	c.closing.Store(true)
	c.mu.Lock()
	ch := c.ch
	c.mu.Unlock()
	if ch != nil {
		// Cancelling the consumer tells the broker to stop, and closes the
		// delivery stream once what it already sent has been handed over.
		if err := ch.Cancel(c.consumerTag(), false); err != nil {
			c.log.Warn("cancel consumer", "component", c.Name(), "err", err)
		}
	}
	c.log.Info("draining", "component", c.Name())
}

// Stop waits for the handler in flight to finish, then closes the connection.
// It gives up when ctx expires — which carries StopTimeout — cancels the
// handler's context and closes anyway, so a handler that will not return
// cannot hang the shutdown. Whatever it was working on is unacknowledged, so
// the broker redelivers it.
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
	c.closeBroker()
	return err
}

// closeBroker drops the channel and the connection if they are open. It is
// safe to call more than once, which redial and Stop both rely on.
func (c *Component) closeBroker() {
	c.mu.Lock()
	ch, conn := c.ch, c.conn
	c.ch, c.conn = nil, nil
	c.mu.Unlock()
	if ch != nil {
		_ = ch.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
}

// Check reports the error the consume loop gave up on, and nothing else. It
// touches no network: CONTEXT.md requires a Check to finish inside the probe's
// own deadline, and one that dialled the broker would turn a broker blip into
// an unready pod. An idle queue is not a fault, so silence reports healthy.
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
