package resilience

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
)

// TestBreakerOpensAfterFiveConsecutiveFailures is the deterministic breaker test required by the
// assignment (FDD seção 9). It uses a fixed response script on an httptest.Server, mirroring the shape
// of the real partner-flaky burst (a run of consecutive 503s), without importing cmd/partner-mock.
func TestBreakerOpensAfterFiveConsecutiveFailures(t *testing.T) {
	var requests int32

	// Fixed script, indexed by the (1-based) request number actually received by the server:
	// 1-5 fail, tripping the breaker open; 6 fails again (the first half-open probe, reopening the
	// breaker immediately); 7-8 succeed (the half-open probes that close the breaker again).
	fails := map[int32]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		if fails[n] {
			http.Error(w, `{"error":"partner unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"partner":"ignored","quote_id":"q-1","premium_cents":123456,` +
			`"currency":"BRL","coverage_cents":5000000,"valid_for_seconds":300}`))
	}))
	defer server.Close()

	p := platform.Partner{Name: "partner-flaky", BaseURL: server.URL}
	client := partner.NewClient(200 * time.Millisecond)
	ctx := context.Background()
	fn := func() (partner.Quote, error) {
		return client.Quote(ctx, p, map[string]string{})
	}

	const openTimeout = 50 * time.Millisecond
	breaker := NewBreaker("partner-flaky", Config{
		ConsecutiveFailures: 5,
		OpenTimeout:         openTimeout,
		HalfOpenMaxRequests: 2,
	})

	// 5 consecutive failures trip the breaker open.
	for i := 1; i <= 5; i++ {
		if _, err := breaker.Execute(ctx, fn); err == nil {
			t.Fatalf("call %d: expected failure from the partner, got success", i)
		}
	}
	if got := atomic.LoadInt32(&requests); got != 5 {
		t.Fatalf("server received %d requests, want 5 before the breaker opens", got)
	}
	if state := breaker.State(); state != gobreaker.StateOpen {
		t.Fatalf("State() = %v, want StateOpen right after the 5th consecutive failure", state)
	}

	// A 6th call while the breaker is open must be rejected without reaching the server.
	if _, err := breaker.Execute(ctx, fn); err == nil {
		t.Fatal("call while breaker is open: expected an error")
	}
	if got := atomic.LoadInt32(&requests); got != 5 {
		t.Fatalf("server received %d requests, want 5: the 6th call must be short-circuited", got)
	}

	// After OpenTimeout, the breaker allows one probe (half-open). The script makes it fail, so the
	// breaker must reopen immediately.
	time.Sleep(openTimeout + 20*time.Millisecond)
	if _, err := breaker.Execute(ctx, fn); err == nil {
		t.Fatal("half-open probe: expected the scripted failure to be returned")
	}
	if got := atomic.LoadInt32(&requests); got != 6 {
		t.Fatalf("server received %d requests, want 6 after the half-open probe", got)
	}
	if state := breaker.State(); state != gobreaker.StateOpen {
		t.Fatalf("State() = %v, want StateOpen: a half-open failure must reopen the circuit immediately", state)
	}

	// After OpenTimeout again, two successful half-open probes (HalfOpenMaxRequests: 2) close the
	// circuit.
	time.Sleep(openTimeout + 20*time.Millisecond)
	if _, err := breaker.Execute(ctx, fn); err != nil {
		t.Fatalf("half-open probe 1: expected success, got %v", err)
	}
	if state := breaker.State(); state != gobreaker.StateHalfOpen {
		t.Fatalf("State() = %v, want StateHalfOpen after a single successful probe", state)
	}
	if _, err := breaker.Execute(ctx, fn); err != nil {
		t.Fatalf("half-open probe 2: expected success, got %v", err)
	}
	if state := breaker.State(); state != gobreaker.StateClosed {
		t.Fatalf("State() = %v, want StateClosed after 2 successful half-open probes", state)
	}
	if got := atomic.LoadInt32(&requests); got != 8 {
		t.Fatalf("server received %d requests, want 8 after the closing probes", got)
	}
}

// TestOnStateChangeFiresWithInitialStateThenOnEveryTransition proves the T09b contract:
// Config.OnStateChange is optional (nil is a no-op, exercised implicitly by every other test in this
// file), fires once right after NewBreaker with the initial StateClosed, and afterwards fires on every
// transition gobreaker itself reports — this is the synchronous, cheap hook that
// platform.PartnerBreakerGauge (T09b) hangs its last-known-state map from.
func TestOnStateChangeFiresWithInitialStateThenOnEveryTransition(t *testing.T) {
	type transition struct{ from, to gobreaker.State }
	var (
		mu   sync.Mutex
		name string
		seen []transition
	)

	onStateChange := func(n string, from, to gobreaker.State) {
		mu.Lock()
		defer mu.Unlock()
		name = n
		seen = append(seen, transition{from, to})
	}

	breaker := NewBreaker("partner-flaky", Config{
		ConsecutiveFailures: 1,
		OpenTimeout:         10 * time.Millisecond,
		HalfOpenMaxRequests: 1,
		OnStateChange:       onStateChange,
	})

	mu.Lock()
	if name != "partner-flaky" {
		t.Fatalf("OnStateChange name = %q right after NewBreaker, want partner-flaky", name)
	}
	if len(seen) != 1 || seen[0] != (transition{gobreaker.StateClosed, gobreaker.StateClosed}) {
		t.Fatalf("OnStateChange calls right after NewBreaker = %v, want exactly one Closed->Closed", seen)
	}
	mu.Unlock()

	ctx := context.Background()
	if _, err := breaker.Execute(ctx, alwaysFailsForTest); err == nil {
		t.Fatal("forcing the breaker open: expected an error")
	}

	mu.Lock()
	if len(seen) != 2 || seen[1] != (transition{gobreaker.StateClosed, gobreaker.StateOpen}) {
		t.Fatalf("OnStateChange calls after the tripping failure = %v, want a second Closed->Open", seen)
	}
	mu.Unlock()

	// gobreaker/v2 não expõe relógio injetável; time.Sleep é necessário aqui para atravessar
	// OpenTimeout e liberar a sonda de meio-aberto.
	time.Sleep(20 * time.Millisecond)
	if _, err := breaker.Execute(ctx, func() (partner.Quote, error) { return partner.Quote{}, nil }); err != nil {
		t.Fatalf("half-open probe: expected success, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 4 {
		t.Fatalf("OnStateChange calls after the successful probe = %v, want 4 (Closed->Open plus Open->HalfOpen plus HalfOpen->Closed)", seen)
	}
	if seen[2] != (transition{gobreaker.StateOpen, gobreaker.StateHalfOpen}) {
		t.Fatalf("OnStateChange calls[2] = %v, want Open->HalfOpen", seen[2])
	}
	if seen[3] != (transition{gobreaker.StateHalfOpen, gobreaker.StateClosed}) {
		t.Fatalf("OnStateChange calls[3] = %v, want HalfOpen->Closed", seen[3])
	}
}

func alwaysFailsForTest() (partner.Quote, error) {
	return partner.Quote{}, errors.New("boom")
}
