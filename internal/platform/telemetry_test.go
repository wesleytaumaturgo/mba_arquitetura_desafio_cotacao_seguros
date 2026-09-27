// Package platform_test is an external test package (not platform) on purpose. PartnerBreakerGauge
// itself lives in internal/platform per the plan (docs/plano-resiliencia-parceiras.md, T09b), not to
// dodge an import cycle: internal/platform imports nothing under internal/, so there is no cycle in
// the production import graph either way. The reason this particular *test file* has to be external
// is narrower and Go-specific: it wires a real *resilience.Breaker into the gauge, and
// internal/resilience imports internal/partner, which imports internal/platform. An internal test
// file (package platform, not platform_test) becomes part of the platform package itself when the
// test binary is built, so importing resilience from an internal test file here would make Go try to
// build platform's own test-augmented variant as a dependency of itself through
// resilience->partner->platform, which the toolchain rejects as "import cycle not allowed in test".
// An external test package (platform_test) is a separate compilation unit and does not have that
// problem.
package platform_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/resilience"
)

// collectPartnerBreakerState collects the metrics registered on reader and returns the int64 value of
// partner_breaker_state{partner=name}, plus whether that data point was found at all.
func collectPartnerBreakerState(t *testing.T, reader metric.Reader, name string) (int64, bool) {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("reader.Collect: %v", err)
	}

	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "partner_breaker_state" {
				continue
			}
			gauge, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("partner_breaker_state is a %T, want metricdata.Gauge[int64]", m.Data)
			}
			for _, dp := range gauge.DataPoints {
				if v, ok := dp.Attributes.Value("partner"); ok && v.AsString() == name {
					return dp.Value, true
				}
			}
		}
	}
	return 0, false
}

// TestBreakerStateGaugeFollowsTransitions is the teste que prova of T09b's gauge (FDD seção 7): the
// gauge only knows what resilience.Config.OnStateChange tells it, so this test wires
// PartnerBreakerGauge.OnStateChange straight into a real *resilience.Breaker (built with the same
// deterministic httptest.Server script used by T05) instead of poking the gauge's internal map
// directly.
func TestBreakerStateGaugeFollowsTransitions(t *testing.T) {
	reader := metric.NewManualReader()
	meterProvider := metric.NewMeterProvider(metric.WithReader(reader))
	meter := meterProvider.Meter("test")

	gauge, err := platform.NewPartnerBreakerGauge(meter)
	if err != nil {
		t.Fatalf("NewPartnerBreakerGauge: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"partner unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	p := platform.Partner{Name: "partner-flaky", BaseURL: server.URL}
	client := partner.NewClient(200 * time.Millisecond)
	ctx := context.Background()
	fn := func() (partner.Quote, error) { return client.Quote(ctx, p, map[string]string{}) }

	const openTimeout = 30 * time.Millisecond
	breaker := resilience.NewBreaker("partner-flaky", resilience.Config{
		ConsecutiveFailures: 5,
		OpenTimeout:         openTimeout,
		HalfOpenMaxRequests: 2,
		OnStateChange:       gauge.OnStateChange,
	})

	if v, ok := collectPartnerBreakerState(t, reader, "partner-flaky"); !ok || v != 0 {
		t.Fatalf("partner_breaker_state right after NewBreaker = (%d, ok=%v), want (0, true)", v, ok)
	}

	for i := 1; i <= 5; i++ {
		if _, err := breaker.Execute(ctx, fn); err == nil {
			t.Fatalf("call %d: expected failure from the partner, got success", i)
		}
	}
	if state := breaker.State(); state != gobreaker.StateOpen {
		t.Fatalf("breaker.State() = %v, want StateOpen after 5 consecutive failures", state)
	}
	if v, ok := collectPartnerBreakerState(t, reader, "partner-flaky"); !ok || v != 2 {
		t.Fatalf("partner_breaker_state after 5 failures = (%d, ok=%v), want (2, true)", v, ok)
	}

	// gobreaker/v2 não expõe relógio injetável; time.Sleep é necessário aqui para atravessar
	// OpenTimeout e liberar as sondas de meio-aberto.
	time.Sleep(openTimeout + 20*time.Millisecond)

	// The server now always answers 200: the two half-open probes (HalfOpenMaxRequests: 2) succeed
	// and close the breaker again.
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"partner":"ignored","quote_id":"q-1","premium_cents":123456,` +
			`"currency":"BRL","coverage_cents":5000000,"valid_for_seconds":300}`))
	})

	if _, err := breaker.Execute(ctx, fn); err != nil {
		t.Fatalf("half-open probe 1: expected success, got %v", err)
	}
	if _, err := breaker.Execute(ctx, fn); err != nil {
		t.Fatalf("half-open probe 2: expected success, got %v", err)
	}
	if state := breaker.State(); state != gobreaker.StateClosed {
		t.Fatalf("breaker.State() = %v, want StateClosed after 2 successful half-open probes", state)
	}
	if v, ok := collectPartnerBreakerState(t, reader, "partner-flaky"); !ok || v != 0 {
		t.Fatalf("partner_breaker_state after closing = (%d, ok=%v), want (0, true)", v, ok)
	}
}
