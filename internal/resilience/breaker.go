// Package resilience wraps gobreaker/v2 with the narrow contract the quotation service needs: one
// circuit breaker per partner, keyed on partner.Quote as the successful result type.
package resilience

import (
	"context"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
)

// Config holds the parameters of a single partner's circuit breaker (FDD seção 5).
type Config struct {
	// ConsecutiveFailures is how many consecutive failures in the closed state trip the breaker open.
	ConsecutiveFailures uint32
	// OpenTimeout is how long the breaker stays open before allowing a half-open probe.
	OpenTimeout time.Duration
	// HalfOpenMaxRequests is how many consecutive successful probes in the half-open state are
	// required to close the breaker again.
	HalfOpenMaxRequests uint32
}

// Breaker wraps a gobreaker.CircuitBreaker[partner.Quote] for a single partner.
type Breaker struct {
	cb *gobreaker.CircuitBreaker[partner.Quote]
}

// NewBreaker builds a Breaker named after the partner it guards, configured per cfg.
func NewBreaker(name string, cfg Config) *Breaker {
	settings := gobreaker.Settings{
		Name:        name,
		MaxRequests: cfg.HalfOpenMaxRequests,
		Timeout:     cfg.OpenTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= cfg.ConsecutiveFailures
		},
	}
	return &Breaker{cb: gobreaker.NewCircuitBreaker[partner.Quote](settings)}
}

// Execute runs fn through the breaker. It returns gobreaker.ErrOpenState (or ErrTooManyRequests) without
// calling fn when the breaker rejects the call.
func (b *Breaker) Execute(_ context.Context, fn func() (partner.Quote, error)) (partner.Quote, error) {
	return b.cb.Execute(fn)
}

// State reports the breaker's current state (closed, half-open or open).
func (b *Breaker) State() gobreaker.State {
	return b.cb.State()
}
