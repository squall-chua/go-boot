// Package brokertest brings up a real Kafka or a real RabbitMQ in Docker for
// a test. It is internal because it is not part of the v1 promise: it exists
// so goboot/kafka and goboot/rabbit can be proven against the thing they
// abstract (#51), not so a user can call it.
//
// It shells out to the docker CLI and links nothing. That is deliberate.
// goboot/db/dbtest chose embedded-postgres over testcontainers-go on 3 linked
// modules against 45 (#13), and testcontainers here would cost the same 45 in
// the test column of .github/module-counts.txt for both packages. There is no
// embedded Kafka and no embedded RabbitMQ, so the daemon is unavoidable; the
// modules are not.
//
// A test that calls any of this skips unless GOBOOT_BROKER_TESTS=1 and a
// docker daemon answers. The switch is off by default because these pull
// about 950 MB of broker images: the same argument the PostgreSQL cache step
// in .github/workflows/ci.yml makes about putting a third-party host in the
// path of the whole test job. CI sets it, so the tests are a gate there and
// an opt-in everywhere else.
package brokertest

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// enable is the switch that turns these tests on. It is set in
// .github/workflows/ci.yml.
const enable = "GOBOOT_BROKER_TESTS"

// startTimeout is how long a broker gets to answer before the test gives up.
// A cold JVM under a loaded CI runner is slow; a broker that has not spoken
// in a minute is broken rather than slow.
const startTimeout = 60 * time.Second

// requireDocker skips the test unless the switch is on and a docker daemon
// answers. `docker info` and not `docker version`: the client alone reports a
// version with no daemon behind it, which is exactly the case this has to
// catch.
func requireDocker(tb testing.TB) {
	tb.Helper()
	if os.Getenv(enable) != "1" {
		tb.Skipf("brokertest: %s is not 1, skipping", enable)
	}
	cmd := exec.Command("docker", "info", "--format", "{{.ServerVersion}}")
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("brokertest: %s is 1 but no docker daemon answered: %v: %s",
			enable, err, strings.TrimSpace(string(out)))
	}
}

// docker runs one docker command and reports its combined output, failing the
// test if it does not exit cleanly.
func docker(tb testing.TB, args ...string) string {
	tb.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		tb.Fatalf("brokertest: docker %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// runPublished starts a detached container publishing containerPort on a free
// host port, and registers its removal. The container is killed rather than
// stopped: nothing in it holds state a test wants back, and a graceful broker
// shutdown costs seconds per test.
//
// It retries because freePort cannot hold the port it finds. Between the
// kernel handing one over and the daemon binding it, something else can take
// it — measured once in fifteen runs, as `failed to bind host port ...:
// address already in use`. The broker needs its host port up front, since the
// address it advertises has to be the one clients dial, so `--publish 0:` and
// reading the answer back is not open to us.
//
// args is given the host port because of that same requirement.
func runPublished(tb testing.TB, containerPort int, args func(hostPort int) []string) (id string, hostPort int) {
	tb.Helper()
	var last string
	for range 5 {
		hostPort = freePort(tb)
		full := append([]string{
			"run", "--detach", "--rm",
			"--publish", fmt.Sprintf("%d:%d", hostPort, containerPort),
		}, args(hostPort)...)
		out, err := exec.Command("docker", full...).CombinedOutput()
		if err == nil {
			id = strings.TrimSpace(string(out))
			tb.Cleanup(func() { _ = exec.Command("docker", "kill", id).Run() })
			return id, hostPort
		}
		last = strings.TrimSpace(string(out))
		if !strings.Contains(last, "address already in use") &&
			!strings.Contains(last, "port is already allocated") {
			tb.Fatalf("brokertest: docker run: %v: %s", err, last)
		}
	}
	tb.Fatalf("brokertest: no host port stayed free across 5 tries: %s", last)
	return "", 0
}

// freePort asks the kernel for a port and gives it straight back, the same
// trick goboot/db/dbtest uses. Two processes asking at the same moment get
// different answers, so a second `go test` run does not collide with this one.
func freePort(tb testing.TB) int {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("brokertest: find a free port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// await calls probe until it stops returning an error or startTimeout runs
// out, and reports the last error it saw.
func await(tb testing.TB, what string, probe func() error) {
	tb.Helper()
	deadline := time.Now().Add(startTimeout)
	var err error
	for time.Now().Before(deadline) {
		if err = probe(); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	tb.Fatalf("brokertest: %s did not come up in %s: %v", what, startTimeout, err)
}

// kafkaImage and rabbitImage are pinned. An unpinned tag would make a test
// that passes today fail on a Tuesday for a reason nobody changed.
const (
	kafkaImage  = "apache/kafka:4.1.0"
	rabbitImage = "rabbitmq:4-alpine"
)

// Kafka starts a single-broker Kafka in KRaft mode and reports the bootstrap
// address to put in kafka.Config.Brokers.
//
// Topics are auto-created with partitions partitions on first use, so a test
// publishes and the topic appears. That is what keeps this package free of
// franz-go's kadm: an admin client would be a second module in
// .github/module-counts.txt's test column for goboot/kafka, and #51 says that
// number must not move.
//
// ready is called once the container is up; pass a probe that dials the
// broker, which is the only thing that knows the difference between a
// container that is running and a broker that is listening.
func Kafka(tb testing.TB, partitions int, ready func(broker string) error) string {
	tb.Helper()
	requireDocker(tb)
	// The advertised listener must be the address the client dials, not the
	// one the broker binds: the container binds 9092 and the host reaches it
	// on the published port. A broker that advertises its own container name
	// sends the client somewhere it cannot go, and the failure looks like a
	// hang.
	_, port := runPublished(tb, 9092, func(hostPort int) []string {
		return []string{
			"--env", "KAFKA_NODE_ID=1",
			"--env", "KAFKA_PROCESS_ROLES=broker,controller",
			"--env", "KAFKA_LISTENERS=PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093",
			"--env", fmt.Sprintf("KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://127.0.0.1:%d", hostPort),
			"--env", "KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER",
			"--env", "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
			"--env", "KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093",
			"--env", "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1",
			"--env", "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1",
			"--env", "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1",
			// Without this a group's first join waits three seconds for
			// members that are not coming, on every test.
			"--env", "KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS=0",
			"--env", fmt.Sprintf("KAFKA_NUM_PARTITIONS=%d", partitions),
			kafkaImage,
		}
	})
	broker := fmt.Sprintf("127.0.0.1:%d", port)
	await(tb, "kafka", func() error { return ready(broker) })
	return broker
}

// Rabbit starts a RabbitMQ and reports the amqp:// URL to put in
// rabbit.Config.URL, along with a function that drops every connection the
// broker holds.
//
// drop is what exercises rabbit.redial against something real: it is the
// broker hanging up, not the process closing its own socket, and the two are
// not the same event. The broker stays up, so the reconnect succeeds and the
// test can watch it happen rather than only watching it be attempted.
func Rabbit(tb testing.TB, ready func(url string) error) (url string, drop func()) {
	tb.Helper()
	requireDocker(tb)
	id, port := runPublished(tb, 5672, func(int) []string { return []string{rabbitImage} })
	url = fmt.Sprintf("amqp://guest:guest@127.0.0.1:%d/", port)
	// `await_startup` and not only a successful dial. RabbitMQ accepts AMQP
	// connections part-way through boot, and a consumer registered inside
	// that window is never sent the cancel when its queue is deleted — the
	// delivery stream just stays open and silent. Measured at roughly one run
	// in two; waiting for the node to say it has finished starting is what
	// makes these tests deterministic.
	await(tb, "rabbitmq to finish starting", func() error {
		out, err := exec.Command("docker", ctlArgs(id, "await_startup")...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	})
	await(tb, "rabbitmq", func() error { return ready(url) })
	return url, func() {
		tb.Helper()
		docker(tb, ctlArgs(id, "close_all_connections", "brokertest")...)
	}
}

// ctlArgs builds the docker arguments that run rabbitmqctl inside the
// container.
//
// --user rabbitmq is not optional. docker exec runs as root, and root's first
// rabbitmqctl during boot leaves /var/lib/rabbitmq/.erlang.cookie unreadable
// by the node itself, which kills the container outright — measured, with the
// crash dump to show for it.
func ctlArgs(id string, args ...string) []string {
	return append([]string{"exec", "--user", "rabbitmq", id, "rabbitmqctl"}, args...)
}
