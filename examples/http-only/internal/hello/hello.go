// Package hello is the DOMAIN of one feature: the behaviour, and nothing else.
// It imports no HTTP package, and nothing here knows a request exists.
//
// There is no Repository and no entity sub-package, because this service has no
// database. A feature that grows one adds them then — see examples/full, which
// is the same shape with storage in it.
package hello

import (
	"context"

	"github.com/squall-chua/go-boot"
)

// Service is the Service Layer: plain Go holding the behaviour.
type Service struct{}

// New takes what this feature needs, which here is nothing.
func New() *Service { return &Service{} }

// Hello is the whole behaviour. It returns no error because it cannot fail; a
// feature that can fail returns one, and the handler passes it up untouched.
func (s *Service) Hello(ctx context.Context, name string) string {
	// The logger is already tagged with this request's ID, so this line and
	// the access-log line for the same request join up.
	goboot.LoggerFrom(ctx).Info("greeting", "name", name)
	return name
}
