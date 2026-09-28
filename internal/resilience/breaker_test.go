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

// scriptedBurstLength and scriptedBurstFailures describe a fixed 60-call response script for
// TestBreakerOpensAfterFiveConsecutiveFailures: calls 1-48 succeed, calls 49-57 fail (a run of 9
// consecutive 503s), and calls 58-60 succeed again. The 49-57 window is not arbitrary: replaying the
// real deterministic generator behind cmd/partner-mock (Behavior.Admit, Seed=20260729,
// FailureRate=0.4 — the same values as partner-flaky in docker-compose) for its first ~500 calls
// shows that positions 49-57 are, in fact, the longest run of consecutive failures it produces.
// The 1-48 prefix, however, is fabricated as "calm" (pure success) to isolate the trip/short-circuit
// proof below; it is NOT a faithful, contiguous replay of the real trace. Replaying the real
// generator over 1-60 in full shows a separate run of exactly 5 consecutive failures at 38-42, which
// would trip a ConsecutiveFailures:5 breaker before ever reaching the 49-57 window used here. The
// script only needs to be exercised up to the 54th call (the short-circuit assertion), but its full
// length is declared here so the failing window (49-57) reads the same way it would in a
// mostly-healthy 60-request trace.
const scriptedBurstLength = 60

// scriptedBurstFails reports whether the (1-based) call number n fails in the fixed 60-call script
// (see the comment above scriptedBurstLength for what is and isn't faithfully reproduced from the
// real partner-mock generator).
func scriptedBurstFails(n int32) bool {
	return n >= 49 && n <= 57
}

// TestBreakerOpensAfterFiveConsecutiveFailures is the deterministic breaker test required by the
// assignment (FDD seção 9). It replays a fixed 60-response script (a fabricated calm prefix at 1-48
// and 58-60, failures at 49-57 — the real longest failure run the partner-flaky mock generator
// produces; see the comment above scriptedBurstLength for exactly what is and isn't a faithful replay
// of the real trace) against an httptest.Server, without importing cmd/partner-mock, and proves the
// breaker opens exactly on the 53rd call (the 5th consecutive failure inside the 49-57 run) and
// short-circuits the 54th, never reaching the server. It stops at the short-circuit assertion, on
// purpose: this is the test the FDD/plano call "determinístico", so it never blocks on a timer (see
// TestBreakerHalfOpenProbesReopenAndClose for the half-open dance, which does).
func TestBreakerOpensAfterFiveConsecutiveFailures(t *testing.T) {
	var requests int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		if scriptedBurstFails(n) {
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

	breaker := NewBreaker("partner-flaky", Config{
		ConsecutiveFailures: 5,
		OpenTimeout:         50 * time.Millisecond,
		HalfOpenMaxRequests: 2,
	})

	// Calls 1-52: the fabricated calm prefix (1-48, pure success) plus the first four calls of the
	// 49-57 failure run (none reaching 5 consecutive yet), so the breaker stays closed and every call
	// reaches the server.
	for i := int32(1); i <= 52; i++ {
		_, err := breaker.Execute(ctx, fn)
		if wantErr := scriptedBurstFails(i); wantErr && err == nil {
			t.Fatalf("call %d: expected the scripted failure, got success", i)
		} else if !wantErr && err != nil {
			t.Fatalf("call %d: expected success, got %v", i, err)
		}
		if state := breaker.State(); state != gobreaker.StateClosed {
			t.Fatalf("State() after call %d = %v, want StateClosed (breaker must not open before the 5th consecutive failure)", i, state)
		}
	}
	if got := atomic.LoadInt32(&requests); got != 52 {
		t.Fatalf("server received %d requests, want 52 before the tripping call", got)
	}

	// Call 53 is the 5th consecutive failure inside the 49-57 burst (49, 50, 51, 52, 53): it trips the
	// breaker open.
	if _, err := breaker.Execute(ctx, fn); err == nil {
		t.Fatal("call 53: expected the scripted failure from the partner, got success")
	}
	if got := atomic.LoadInt32(&requests); got != 53 {
		t.Fatalf("server received %d requests, want 53 after the tripping call", got)
	}
	if state := breaker.State(); state != gobreaker.StateOpen {
		t.Fatalf("State() = %v, want StateOpen right after the 53rd call (5th consecutive failure)", state)
	}

	// Call 54 must be short-circuited: rejected by the open breaker without ever reaching the server,
	// even though the script would have failed it anyway.
	if _, err := breaker.Execute(ctx, fn); err == nil {
		t.Fatal("call 54: expected an error from the open breaker")
	}
	if got := atomic.LoadInt32(&requests); got != 53 {
		t.Fatalf("server received %d requests, want 53: the 54th call must be short-circuited", got)
	}
}

// TestBreakerHalfOpenProbesReopenAndClose covers the half-open dance: a failed probe reopens the circuit
// immediately, and HalfOpenMaxRequests consecutive successful probes close it again.
//
// gobreaker/v2 não expõe relógio injetável (as transições de half-open correm sobre time.Now()
// internamente), então time.Sleep é a única forma de atravessar OpenTimeout aqui de forma determinística;
// as durações usadas são as menores que não flutuam (dezenas de ms), isoladas nesta função para que o
// teste "determinístico" acima (TestBreakerOpensAfterFiveConsecutiveFailures) nunca dependa de tempo real.
func TestBreakerHalfOpenProbesReopenAndClose(t *testing.T) {
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

	// 5 consecutive failures trip the breaker open (setup for the half-open dance below, not itself the
	// assertion under test in this function).
	for i := 1; i <= 5; i++ {
		if _, err := breaker.Execute(ctx, fn); err == nil {
			t.Fatalf("call %d: expected failure from the partner, got success", i)
		}
	}
	if state := breaker.State(); state != gobreaker.StateOpen {
		t.Fatalf("State() = %v, want StateOpen right after the 5th consecutive failure", state)
	}

	// gobreaker/v2 has no injectable clock: after OpenTimeout, the breaker allows one probe (half-open).
	// The script makes it fail, so the breaker must reopen immediately.
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

	// gobreaker/v2 has no injectable clock: after OpenTimeout again, two successful half-open probes
	// (HalfOpenMaxRequests: 2) close the circuit.
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
