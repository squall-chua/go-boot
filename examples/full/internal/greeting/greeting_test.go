package greeting

import (
	"context"
	"errors"
	"testing"

	"github.com/squall-chua/go-boot/examples/full/internal/greeting/entity"
)

// fakeRepo is the reason Repository is an interface: the Service Layer is
// tested with no database, no container and no fixtures. full_test.go needs a
// real PostgreSQL because it drives the whole wiring; this file needs none.
type fakeRepo struct {
	greeting entity.Greeting
	err      error
}

func (f fakeRepo) ByLang(context.Context, string) (entity.Greeting, error) {
	return f.greeting, f.err
}

func TestGreetUsesTheStoredMessage(t *testing.T) {
	s := New(fakeRepo{greeting: entity.Greeting{Message: "hei"}}, "hello")
	got, err := s.Greet(context.Background(), "world")
	if err != nil || got != "hei world" {
		t.Fatalf("Greet = %q, %v; want %q, nil", got, err, "hei world")
	}
}

func TestGreetFallsBackWhenThereIsNoRow(t *testing.T) {
	s := New(fakeRepo{err: entity.ErrNotFound}, "hello")
	got, err := s.Greet(context.Background(), "world")
	if err != nil || got != "hello world" {
		t.Fatalf("Greet = %q, %v; want %q, nil", got, err, "hello world")
	}
}

func TestGreetPassesAnyOtherErrorUp(t *testing.T) {
	want := errors.New("boom")
	_, err := New(fakeRepo{err: want}, "hello").Greet(context.Background(), "world")
	if !errors.Is(err, want) {
		t.Fatalf("Greet error = %v, want %v", err, want)
	}
}
