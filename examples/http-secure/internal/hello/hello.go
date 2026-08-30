// Package hello is the DOMAIN of the open feature: the behaviour, and nothing
// else. It imports no HTTP package and no security package, because whether a
// route is guarded is a fact about the mount, not about the behaviour.
package hello

import "context"

// Service is the Service Layer: plain Go holding the behaviour.
type Service struct{}

// New takes what this feature needs, which here is nothing.
func New() *Service { return &Service{} }

// Hello is the whole behaviour.
func (s *Service) Hello(ctx context.Context, name string) string { return name }
