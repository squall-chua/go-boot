package rpc_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/squall-chua/go-boot/grpc"
	greetv1 "github.com/squall-chua/go-boot/internal/gen/greet/v1"
	"github.com/squall-chua/go-boot/internal/gen/greet/v1/greetv1connect"
	"github.com/squall-chua/go-boot/trace"
	"github.com/squall-chua/go-boot/trace/rpc"
	"github.com/squall-chua/go-boot/web"
)

// spans is global because the thing under test is: otelconnect and otelhttp
// both find their provider through otel.GetTracerProvider, a process-wide
// slot, so NOTHING in this file may call t.Parallel.
var spans *tracetest.SpanRecorder

func TestMain(m *testing.M) {
	spans = tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	os.Exit(m.Run())
}

// greeter is the Service Layer, and grpcGreeter is the thin gRPC Transport
// over it. Both are the shapes docs/spec.md 4.4 fixes; the tracing is what
// this file is about.
type greeter struct{}

func (g *greeter) Greet(_ context.Context, name string) (string, error) { return "hello " + name, nil }

type grpcGreeter struct{ svc *greeter }

func (g *grpcGreeter) Greet(ctx context.Context, req *connect.Request[greetv1.GreetRequest]) (*connect.Response[greetv1.GreetResponse], error) {
	out, err := g.svc.Greet(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&greetv1.GreetResponse{Greeting: out}), nil
}

func (g *grpcGreeter) GreetStream(context.Context, *connect.Request[greetv1.GreetStreamRequest], *connect.ServerStream[greetv1.GreetStreamResponse]) error {
	return nil
}

// TestAnRPCGetsOneSpanNotTwo is the whole reason trace.IsRPC exists. otelhttp
// and otelconnect mounted together give a redundant HTTP parent wrapping the
// real RPC span, and the last subtest measures that rather than asserting it:
// it mounts otelhttp with the filter removed and counts two.
//
// Three of connect's four protocols run here end to end, one per header rule
// trace.IsRPC applies: gRPC and gRPC-Web through the content type, Connect
// through the protocol header. Connect JSON shares Connect proto's header and
// is covered by the unit table in goboot/trace.
func TestAnRPCGetsOneSpanNotTwo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		filter  bool
		want    int
		h2c     bool // the gRPC protocol needs HTTP/2; the other two run on HTTP/1
		options []connect.ClientOption
	}{
		{"grpc protocol", true, 1, true, []connect.ClientOption{connect.WithGRPC()}},
		{"grpc-web protocol", true, 1, false, []connect.ClientOption{connect.WithGRPCWeb()}},
		{"connect protocol", true, 1, false, nil},
		{"otelhttp unfiltered is the bug this filter fixes", false, 2, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spans.Reset()
			base := serve(t, tc.filter)

			httpClient := http.DefaultClient
			if tc.h2c {
				httpClient = h2cClient()
			}
			client := greetv1connect.NewGreetServiceClient(httpClient, base, tc.options...)
			resp, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "bob"}))
			if err != nil {
				t.Fatalf("Greet: %v", err)
			}
			if got, want := resp.Msg.GetGreeting(), "hello bob"; got != want {
				t.Fatalf("Greet = %q, want %q", got, want)
			}
			ended := spans.Ended()
			if len(ended) != tc.want {
				var names []string
				for _, s := range ended {
					names = append(names, s.Name())
				}
				t.Fatalf("got %d spans %v, want %d", len(ended), names, tc.want)
			}
			// The one span that survives is the RPC's, named after the
			// procedure. An HTTP parent would be named by method instead.
			if tc.want == 1 && ended[0].Name() != greetv1connect.GreetServiceGreetProcedure[1:] {
				t.Errorf("span name = %q, want the procedure %q", ended[0].Name(), greetv1connect.GreetServiceGreetProcedure[1:])
			}
		})
	}
}

// h2cClient speaks HTTP/2 over cleartext, which is what the gRPC protocol
// needs. Since Go 1.24 net/http does this itself, so there is no
// golang.org/x/net/http2 here and no extra module in go.sum.
func h2cClient() *http.Client {
	t := &http.Transport{Protocols: &http.Protocols{}}
	t.Protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: t}
}

// serve mounts the greet service behind the tracing middleware. filter=false
// swaps trace.Middleware for a bare otelhttp, which is what a user gets by
// wrapping the mux themselves instead of using this Starter.
func serve(t *testing.T, filter bool) string {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := web.New(web.Config{Addr: "127.0.0.1:0"}, log)
	if filter {
		srv.Use(trace.DefaultMiddleware(log)...)
	} else {
		srv.Use(otelhttp.NewMiddleware(""))
	}

	opts, err := rpc.Options()
	if err != nil {
		t.Fatalf("rpc.Options: %v", err)
	}
	srv.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{&greeter{}},
		append(grpc.DefaultOptions(log), opts...)...))

	if _, err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})
	return "http://" + srv.Addr()
}
