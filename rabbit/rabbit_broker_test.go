package rabbit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/squall-chua/go-boot/internal/brokertest"
)

// These are the tests #51 exists for. Everything in rabbit_test.go drives the
// consume loop over a delivery channel of the test's own, which is the right
// test for what #49 settled and says nothing about what a real broker
// redelivers or what redial does when the broker actually hangs up. These run
// the same Component against a real RabbitMQ in Docker, and skip unless
// GOBOOT_BROKER_TESTS=1.

// ping reports whether the broker is accepting connections yet.
func ping(url string) error {
	conn, err := amqp.Dial(url)
	if err != nil {
		return err
	}
	return conn.Close()
}

// declare creates the queue and publishes one message per body. The Starter
// declares nothing on purpose — QueueDeclarePassive fails if the queue is not
// there — so the test owns the topology, exactly as an operator would.
func declare(t *testing.T, url, queue string, bodies ...string) {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("declare: dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("declare: channel: %v", err)
	}
	if _, err := ch.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		t.Fatalf("declare %q: %v", queue, err)
	}
	for _, b := range bodies {
		err := ch.PublishWithContext(context.Background(), "", queue, false, false,
			amqp.Publishing{DeliveryMode: amqp.Persistent, Body: []byte(b)})
		if err != nil {
			t.Fatalf("publish %q: %v", b, err)
		}
	}
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

// start builds a Component against the real broker and starts it. Stop is
// left to the caller, because when and with what budget it is called is the
// thing under test.
func start(t *testing.T, url, queue string, h Handler) *Component {
	t.Helper()
	c, err := New(Config{URL: url, Queue: queue}, quiet(), h)
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

// TestARealBrokerRedeliversAfterAStopThatTimedOut is the second half of the
// at-least-once claim in this package's doc comment. rabbit_test.go proves
// Stop gives up on a handler that will not return; only a broker can say what
// happens to that handler's delivery afterwards, and the answer has to be
// that somebody else gets it — marked redelivered, because the broker knows.
func TestARealBrokerRedeliversAfterAStopThatTimedOut(t *testing.T) {
	url, _ := brokertest.Rabbit(t, ping)
	const queue = "redelivery"
	declare(t, url, queue, "a")

	// held is never closed until the test is over: this is the handler that
	// ignores its cancelled context, which is the case Stop's budget exists
	// for.
	held := make(chan struct{})
	t.Cleanup(func() { close(held) })
	arrived := make(chan Message, 4)

	stuck := start(t, url, queue, func(ctx context.Context, m Message) error {
		arrived <- m
		<-held
		return nil
	})

	select {
	case m := <-arrived:
		if string(m.Body) != "a" {
			t.Fatalf("first delivery = %q, want %q", m.Body, "a")
		}
		if m.Redelivered {
			t.Fatal("the first delivery reports Redelivered, want false")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no delivery from a real broker in 30s")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := stuck.Stop(ctx); err == nil {
		t.Fatal("Stop returned nil with a handler still running, want the budget to have expired")
	}

	// A fresh consumer on the same queue. Nothing was acknowledged, so the
	// broker must hand the delivery over again.
	again := make(chan Message, 4)
	fresh := start(t, url, queue, func(ctx context.Context, m Message) error {
		again <- m
		return nil
	})
	defer stop(t, fresh, "the fresh consumer")

	select {
	case m := <-again:
		if string(m.Body) != "a" {
			t.Fatalf("redelivery = %q, want %q", m.Body, "a")
		}
		if !m.Redelivered {
			t.Error("the redelivery reports Redelivered false, want true")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the message was not redelivered in 30s")
	}
}

// TestARealBrokerRedialsAfterTheBrokerHangsUp is the case rabbit_test.go
// cannot reach. Its fake closes the delivery channel, which is what a hang-up
// looks like from inside; this is the broker actually dropping the connection,
// with a live channel and a live connection to invalidate.
//
// The broker stays up, so redial must succeed and consuming must resume. A
// message published only after the drop is what proves it resumed rather than
// merely reconnected.
func TestARealBrokerRedialsAfterTheBrokerHangsUp(t *testing.T) {
	url, drop := brokertest.Rabbit(t, ping)
	const queue = "redial"
	declare(t, url, queue, "before")

	var mu sync.Mutex
	var bodies []string
	got := make(chan string, 8)
	c := start(t, url, queue, func(ctx context.Context, m Message) error {
		mu.Lock()
		bodies = append(bodies, string(m.Body))
		mu.Unlock()
		got <- string(m.Body)
		return nil
	})
	defer stop(t, c, "the consumer")

	awaitBody := func(want string) {
		t.Helper()
		deadline := time.After(60 * time.Second)
		for {
			select {
			case b := <-got:
				if b == want {
					return
				}
			case <-deadline:
				mu.Lock()
				defer mu.Unlock()
				t.Fatalf("waiting for %q, saw %v", want, bodies)
			}
		}
	}
	awaitBody("before")

	drop()

	// Published after the hang-up, so it can only arrive over a connection
	// redial made.
	declare(t, url, queue, "after")
	awaitBody("after")

	if err := c.Check(context.Background()); err != nil {
		t.Errorf("Check after a recovered hang-up = %v, want nil: a dropped connection is not a death", err)
	}
}

// TestARealBrokerFatalsOnAQueueThatIsGone is the other half of redial: a
// broker that is reachable and refusing. permanent() is what tells the two
// apart, and until now it was only ever handed an error a test built.
func TestARealBrokerFatalsOnAQueueThatIsGone(t *testing.T) {
	url, _ := brokertest.Rabbit(t, ping)
	const queue = "vanishing"
	declare(t, url, queue)

	var handled atomic.Int64
	c, err := New(Config{URL: url, Queue: queue}, quiet(), func(context.Context, Message) error {
		handled.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	errc, err := c.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.Stop(ctx) // it has already died; the error is the point of the test
	}()

	// Deleting the queue closes the consumer's channel, so the consume loop
	// redials and QueueDeclarePassive finds nothing. That is NOT_FOUND, which
	// permanent() calls fatal.
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	if _, err := ch.QueueDelete(queue, false, false, false); err != nil {
		t.Fatalf("delete %q: %v", queue, err)
	}
	_ = conn.Close()

	select {
	case got := <-errc:
		if got == nil {
			t.Fatal("the death channel carried nil")
		}
		if cerr := c.Check(context.Background()); cerr == nil {
			t.Error("Check is nil after a death, want the same error")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a deleted queue did not reach the death channel in 30s")
	}
}
