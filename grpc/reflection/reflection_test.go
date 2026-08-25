package reflection_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/grpc/reflection"
	"github.com/squall-chua/go-boot/internal/gen/greet/v1/greetv1connect"
	"github.com/squall-chua/go-boot/web"
)

// recorder is the smallest thing that satisfies reflection.Handler. It keeps
// the patterns instead of serving them, which is how the two-handler claim is
// read directly rather than inferred from a client's behaviour.
type recorder struct{ patterns []string }

func (r *recorder) Handle(pattern string, _ http.Handler) {
	r.patterns = append(r.patterns, pattern)
}

// greetService is the mounted service the reflection client goes looking for.
type greetService struct {
	greetv1connect.UnimplementedGreetServiceHandler
}

// TestBothTheCurrentAndTheOlderHandlerAreMounted. grpcurl still asks for
// v1alpha, so mounting only v1 leaves the most common client with nothing.
func TestBothTheCurrentAndTheOlderHandlerAreMounted(t *testing.T) {
	t.Parallel()

	var rec recorder
	reflection.MountOn(&rec, greetv1connect.GreetServiceName)

	for _, want := range []string{
		"/grpc.reflection.v1.ServerReflection/",
		"/grpc.reflection.v1alpha.ServerReflection/",
	} {
		if !slices.Contains(rec.patterns, want) {
			t.Errorf("%q was not mounted; got %v", want, rec.patterns)
		}
	}
}

// TestAReflectionClientListsTheService is the end-to-end run: a real listener,
// a real bidirectional stream over cleartext HTTP/2, and the name back.
func TestAReflectionClientListsTheService(t *testing.T) {
	t.Parallel()

	app, err := goboot.New(goboot.Config{
		Lifecycle: goboot.LifecycleConfig{DrainDelay: time.Millisecond},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	app.Log = slog.New(slog.NewTextHandler(io.Discard, nil))

	srv, err := web.New(web.Config{Addr: "127.0.0.1:0"}, app.Log)
	if err != nil {
		t.Fatal(err)
	}
	srv.Handle(greetv1connect.NewGreetServiceHandler(&greetService{}))
	reflection.MountOn(srv, greetv1connect.GreetServiceName)
	app.Add(srv)

	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	// Reflection is a bidirectional stream, so it needs HTTP/2 and the gRPC
	// protocol. Since Go 1.24 cleartext HTTP/2 costs no extra module.
	tr := &http.Transport{Protocols: &http.Protocols{}}
	tr.Protocols.SetUnencryptedHTTP2(true)
	client := grpcreflect.NewClient(&http.Client{Transport: tr}, "http://"+srv.Addr(), connect.WithGRPC())

	stream := client.NewStream(t.Context())
	defer stream.Close() //nolint:errcheck // the test's assertions are on the list

	names, err := stream.ListServices()
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	var got []string
	for _, n := range names {
		got = append(got, string(n))
	}
	if !slices.Contains(got, greetv1connect.GreetServiceName) {
		t.Errorf("the mounted service is missing from %v", got)
	}

	// Listing the name is not enough for grpcurl: it then asks for the file
	// that defines the symbol, and that comes from the generated package's
	// own registration rather than from the names passed to MountOn.
	files, err := stream.FileContainingSymbol(protoreflect.FullName(greetv1connect.GreetServiceName))
	if err != nil {
		t.Fatalf("FileContainingSymbol: %v", err)
	}
	if len(files) == 0 {
		t.Error("no file descriptor came back for the mounted service")
	}
}
