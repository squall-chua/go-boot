// Package greeting is the DOMAIN of one feature: what it needs from storage,
// and the behaviour built on it. It imports no database driver and no HTTP
// package, and nothing here knows a request exists.
//
// The Entity and its persistence, and each Transport, are sub-packages, and
// they all point INWARD at this one:
//
//	greeting/          the domain — this package
//	greeting/entity/   the Entity and the SQL that loads it
//	greeting/rest/     HTTP: the DTOs, the bind, the handler, the routes
//
// Feature two is internal/orders with the same three, and two more lines in
// routes.go. Nothing here moves.
package greeting

import (
	"context"
	"errors"

	"github.com/squall-chua/go-boot/cmd/goboot/scaffold/grpc/internal/greeting/entity"
)

// Repository is what this feature needs from storage, and nothing more.
//
// It is declared HERE, beside the Service that CONSUMES it, and not in the
// entity package that implements it. That direction is the whole point: the
// domain says what it wants, and an adapter supplies it. greeting_test.go
// swaps in a fake in four lines.
type Repository interface {
	ByLang(ctx context.Context, lang string) (entity.Greeting, error)
}

// Service is the Service Layer: plain Go holding the behaviour. It knows
// nothing about HTTP, about gRPC, or about SQL.
type Service struct {
	repo     Repository
	fallback string
}

// New takes what this feature needs and nothing more. routes.go hands it the
// Repository and the config key, so the feature never reads config itself.
func New(repo Repository, fallback string) *Service {
	return &Service{repo: repo, fallback: fallback}
}

// Greet is the whole behaviour: the stored message for a language, or the
// configured fallback when no row has one.
func (s *Service) Greet(ctx context.Context, name string) (string, error) {
	g, err := s.repo.ByLang(ctx, "en")
	switch {
	case errors.Is(err, entity.ErrNotFound):
		return s.fallback + " " + name, nil // this service's own config key
	case err != nil:
		return "", err
	}
	return g.Message + " " + name, nil
}
