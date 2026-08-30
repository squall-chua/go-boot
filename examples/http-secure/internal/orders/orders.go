// Package orders is the DOMAIN of the guarded feature. Like hello, it imports
// no security package: it is handed the subject the token became, and it never
// asks how that was checked.
package orders

import (
	"context"

	"github.com/squall-chua/go-boot"
)

// Service is the Service Layer: plain Go holding the behaviour.
type Service struct{}

// New takes what this feature needs, which here is nothing.
func New() *Service { return &Service{} }

// Accept records the order against the caller it was placed for.
func (s *Service) Accept(ctx context.Context, subject string) {
	goboot.LoggerFrom(ctx).Info("order accepted", "sub", subject)
}
