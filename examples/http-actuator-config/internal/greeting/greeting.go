// Package greeting is the DOMAIN of one feature: the behaviour, and nothing
// else. It imports no HTTP package, and nothing here knows a request exists.
//
// There is no Repository and no entity sub-package, because this service has no
// database. examples/full is the same shape with storage in it.
package greeting

import (
	"context"

	"github.com/squall-chua/go-boot"
)

// Service is the Service Layer: plain Go holding the behaviour.
type Service struct{ greeting string }

// New takes what this feature needs and nothing more. routes.go hands it the
// config key, so the feature never reads config itself.
func New(greeting string) *Service { return &Service{greeting: greeting} }

// Greet is the whole behaviour: the configured greeting, and the name.
func (s *Service) Greet(ctx context.Context, name string) string {
	// Visible after PUT /actuator/loglevel, with no restart.
	goboot.LoggerFrom(ctx).Debug("greeting", "name", name)
	return s.greeting + " " + name
}
