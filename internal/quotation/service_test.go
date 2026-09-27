package quotation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/cache"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/resilience"
)

var threePartners = []platform.Partner{
	{Name: "partner-slow", BaseURL: "http://slow"},
	{Name: "partner-flaky", BaseURL: "http://flaky"},
	{Name: "partner-degrading", BaseURL: "http://degrading"},
}

func validRequest() Request {
	r := Request{
		Driver:  Driver{Document: "12345678901", BirthYear: 1988},
		Vehicle: Vehicle{Plate: "ABC1D23", Model: "Gol 1.0", Year: 2020, ValueCents: 8500000},
	}
	if err := r.Normalize(); err != nil {
		panic(err)
	}
	return r
}

type fakeQuoter struct {
	delay      time.Duration
	premiums   map[string]int64
	failOn     map[string]bool
	calls      []string
	requests   []any
	concurrent atomic.Int32
	peak       atomic.Int32
}

func failing(partners ...string) map[string]bool {
	failOn := make(map[string]bool, len(partners))
	for _, p := range partners {
		failOn[p] = true
	}
	return failOn
}

func (f *fakeQuoter) Quote(_ context.Context, p platform.Partner, request any) (partner.Quote, error) {
	if now := f.concurrent.Add(1); now > f.peak.Load() {
		f.peak.Store(now)
	}
	defer f.concurrent.Add(-1)

	f.calls = append(f.calls, p.Name)
	f.requests = append(f.requests, request)
	time.Sleep(f.delay)

	if f.failOn[p.Name] {
		return partner.Quote{}, &partner.Error{Partner: p.Name, Status: 503, Reason: "partner answered 503"}
	}
	return partner.Quote{Partner: p.Name, PremiumCents: f.premiums[p.Name], Currency: "BRL"}, nil
}

func defaultPremiums() map[string]int64 {
	return map[string]int64{"partner-slow": 180000, "partner-flaky": 90000, "partner-degrading": 120000}
}

func TestQuoteAggregatesTheThreePartnersSortedByPremium(t *testing.T) {
	quoter := &fakeQuoter{premiums: defaultPremiums()}
	response, err := NewService(threePartners, quoter, nil, nil).Quote(context.Background(), "corretora-a", validRequest())
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}

	if len(response.Quotes) != 3 {
		t.Fatalf("%d quotes, expected 3", len(response.Quotes))
	}
	expected := []string{"partner-flaky", "partner-degrading", "partner-slow"}
	for i, name := range expected {
		if response.Quotes[i].Partner != name {
			t.Errorf("quote %d is from %q, expected from %q", i, response.Quotes[i].Partner, name)
		}
	}
	if response.TenantID != "corretora-a" {
		t.Errorf("tenant_id %q, expected corretora-a", response.TenantID)
	}
	if response.Degraded {
		t.Error("degraded is true with all three partners succeeding, expected false")
	}
	if len(response.MissingPartners) != 0 {
		t.Errorf("missing_partners %v, expected none", response.MissingPartners)
	}
	for _, quote := range response.Quotes {
		if quote.Source != "live" {
			t.Errorf("quote from %q has source %q, expected live", quote.Partner, quote.Source)
		}
		if quote.AgeSeconds != nil {
			t.Errorf("quote from %q has age_seconds %v, expected nil for a live quote", quote.Partner, *quote.AgeSeconds)
		}
	}

	body := serialize(t, response)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if _, ok := decoded["missing_partners"]; ok {
		t.Errorf("missing_partners present in JSON with no missing partners: %s", body)
	}
	if degraded, ok := decoded["degraded"]; !ok || degraded != false {
		t.Errorf("degraded absent or not false in JSON: %s", body)
	}
	quoteBodies, ok := decoded["quotes"].([]any)
	if !ok || len(quoteBodies) != 3 {
		t.Fatalf("quotes not decoded as expected: %s", body)
	}
	for _, q := range quoteBodies {
		quote, ok := q.(map[string]any)
		if !ok {
			t.Fatalf("quote is not an object: %v", q)
		}
		if _, ok := quote["age_seconds"]; ok {
			t.Errorf("age_seconds present in JSON for a live quote: %s", body)
		}
		if quote["source"] != "live" {
			t.Errorf("source %v in JSON, expected live: %s", quote["source"], body)
		}
	}
}

func TestQuoteCallsThePartnersSerially(t *testing.T) {
	quoter := &fakeQuoter{delay: 40 * time.Millisecond, premiums: defaultPremiums()}

	start := time.Now()
	if _, err := NewService(threePartners, quoter, nil, nil).Quote(context.Background(), "corretora-a", validRequest()); err != nil {
		t.Fatalf("Quote: %v", err)
	}
	elapsed := time.Since(start)

	if minimum := 3 * quoter.delay; elapsed < minimum {
		t.Fatalf("aggregation took %s; serially it should take at least %s", elapsed, minimum)
	}
	if peak := quoter.peak.Load(); peak != 1 {
		t.Fatalf("%d concurrent calls at the peak, expected 1 (serial calls)", peak)
	}
}

func TestOnePartnerDownReturnsAPartialResponse(t *testing.T) {
	quoter := &fakeQuoter{premiums: defaultPremiums(), failOn: failing("partner-flaky")}

	response, err := NewService(threePartners, quoter, nil, nil).Quote(context.Background(), "corretora-a", validRequest())
	if err != nil {
		t.Fatalf("Quote: %v, expected a partial response instead of an error", err)
	}

	if !response.Degraded {
		t.Error("degraded is false with one partner down, expected true")
	}
	if got := response.MissingPartners; len(got) != 1 || got[0] != "partner-flaky" {
		t.Errorf("missing_partners %v, expected [partner-flaky]", got)
	}
	if len(response.Quotes) != 2 {
		t.Fatalf("%d quotes, expected 2 (the two partners that answered)", len(response.Quotes))
	}
	expected := []string{"partner-degrading", "partner-slow"}
	for i, name := range expected {
		if response.Quotes[i].Partner != name {
			t.Errorf("quote %d is from %q, expected from %q", i, response.Quotes[i].Partner, name)
		}
	}

	if last := quoter.calls[len(quoter.calls)-1]; last != "partner-degrading" {
		t.Errorf("last partner called was %q, expected partner-degrading (the loop keeps going after a failure)", last)
	}
}

func TestAllPartnersDownReturnsNoQuotesButNoError(t *testing.T) {
	quoter := &fakeQuoter{premiums: defaultPremiums(), failOn: failing("partner-slow", "partner-flaky", "partner-degrading")}

	response, err := NewService(threePartners, quoter, nil, nil).Quote(context.Background(), "corretora-a", validRequest())
	if err != nil {
		t.Fatalf("Quote: %v, expected a response with no quotes instead of an error", err)
	}

	if len(response.Quotes) != 0 {
		t.Fatalf("%d quotes, expected 0", len(response.Quotes))
	}
	if !response.Degraded {
		t.Error("degraded is false with every partner down, expected true")
	}
	expected := []string{"partner-slow", "partner-flaky", "partner-degrading"}
	if len(response.MissingPartners) != len(expected) {
		t.Fatalf("missing_partners %v, expected %v", response.MissingPartners, expected)
	}
	for i, name := range expected {
		if response.MissingPartners[i] != name {
			t.Errorf("missing_partners[%d] %q, expected %q", i, response.MissingPartners[i], name)
		}
	}
}

func TestBrokerGoesInThePartnerRequest(t *testing.T) {
	quoter := &fakeQuoter{premiums: defaultPremiums()}
	service := NewService(threePartners[:1], quoter, nil, nil)

	if _, err := service.Quote(context.Background(), "corretora-a", validRequest()); err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if _, err := service.Quote(context.Background(), "corretora-b", validRequest()); err != nil {
		t.Fatalf("Quote: %v", err)
	}

	toA, toB := serialize(t, quoter.requests[0]), serialize(t, quoter.requests[1])
	if toA == toB {
		t.Fatalf("different brokers produced the same request to the partner: %s", toA)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(toA), &body); err != nil {
		t.Fatalf("request to the partner is not JSON: %v", err)
	}
	if body["broker"] != "corretora-a" {
		t.Errorf("broker %v, expected corretora-a", body["broker"])
	}
	if _, ok := body["vehicle"]; !ok {
		t.Errorf("request to the partner does not carry the vehicle: %s", toA)
	}
}

func serialize(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}

// newTestCache starts a real miniredis server and wraps it in a real cache.QuoteCache (FDD seção 9:
// the determinístico test must not use a fake). The returned *miniredis.Miniredis lets a test close
// the server mid-flight to prove Redis failures are best effort (T08 critério de aceite 5).
func newTestCache(t *testing.T, ttl time.Duration) (*miniredis.Miniredis, *cache.QuoteCache) {
	t.Helper()

	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run(): %v", err)
	}
	t.Cleanup(server.Close)

	client := redis.NewClient(&redis.Options{
		Addr:          server.Addr(),
		MaxRetries:    0,
		DialTimeout:   50 * time.Millisecond,
		DialerRetries: 1,
	})
	t.Cleanup(func() { _ = client.Close() })

	return server, cache.NewQuoteCache(client, ttl)
}

func TestQuoteUsesTheCacheEntryWithoutCallingThePartner(t *testing.T) {
	_, quoteCache := newTestCache(t, time.Minute)
	request := validRequest()

	cached := partner.Quote{Partner: "partner-flaky", QuoteID: "cached-quote", PremiumCents: 84210, Currency: "BRL"}
	key := cache.Key("corretora-a", "partner-flaky", request.Fingerprint())
	if err := quoteCache.Set(context.Background(), key, cached); err != nil {
		t.Fatalf("Set: %v", err)
	}

	quoter := &fakeQuoter{premiums: defaultPremiums()}
	response, err := NewService(threePartners, quoter, quoteCache, nil).Quote(context.Background(), "corretora-a", request)
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}

	for _, called := range quoter.calls {
		if called == "partner-flaky" {
			t.Fatalf("the quoter was called for partner-flaky even though it had a cache entry: %v", quoter.calls)
		}
	}

	var flakyQuote *partner.Quote
	for i := range response.Quotes {
		if response.Quotes[i].Partner == "partner-flaky" {
			flakyQuote = &response.Quotes[i]
		}
	}
	if flakyQuote == nil {
		t.Fatalf("no quote from partner-flaky in the response: %+v", response.Quotes)
	}
	if flakyQuote.Source != "cache" {
		t.Errorf("source %q, expected cache", flakyQuote.Source)
	}
	if flakyQuote.AgeSeconds == nil {
		t.Error("age_seconds is nil for a cache hit, expected non-nil")
	}
	if response.Degraded {
		t.Error("degraded is true with a cache hit and two live successes, expected false (a cache hit is not degradation)")
	}
	if len(response.MissingPartners) != 0 {
		t.Errorf("missing_partners %v, expected none", response.MissingPartners)
	}
}

func TestQuoteFallsBackToLiveOnCacheMiss(t *testing.T) {
	_, quoteCache := newTestCache(t, time.Minute)
	request := validRequest()

	quoter := &fakeQuoter{premiums: defaultPremiums()}
	response, err := NewService(threePartners, quoter, quoteCache, nil).Quote(context.Background(), "corretora-a", request)
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if len(quoter.calls) != 3 {
		t.Fatalf("quoter called %d times, expected 3 on a full cache miss: %v", len(quoter.calls), quoter.calls)
	}
	for _, quote := range response.Quotes {
		if quote.Source != "live" {
			t.Errorf("quote from %q has source %q, expected live", quote.Partner, quote.Source)
		}
	}

	for _, p := range threePartners {
		key := cache.Key("corretora-a", p.Name, request.Fingerprint())
		if _, _, ok := quoteCache.Get(context.Background(), key); !ok {
			t.Errorf("no cache entry written for %q after a live success, expected Set to have run", p.Name)
		}
	}
}

func TestQuoteIgnoresARedisFailureAndStillAnswersLive(t *testing.T) {
	server, quoteCache := newTestCache(t, time.Minute)
	server.Close() // Redis unreachable before the very first call: Get and Set must be best effort.

	quoter := &fakeQuoter{premiums: defaultPremiums()}
	response, err := NewService(threePartners, quoter, quoteCache, nil).Quote(context.Background(), "corretora-a", validRequest())
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if len(response.Quotes) != 3 {
		t.Fatalf("%d quotes, expected 3 even with Redis unreachable (Get/Set failures must not block a live answer)", len(response.Quotes))
	}
	if response.Degraded {
		t.Error("degraded is true with Redis unreachable but every partner live, expected false")
	}
}

// TestPartialResponseWithCacheAndFallback is the second teste determinístico exigido pelo enunciado
// (FDD seção 9): a real miniredis + cache.QuoteCache, not a fake, covering cache hit, live success,
// live failure and total failure together.
func TestPartialResponseWithCacheAndFallback(t *testing.T) {
	t.Run("cache hit, live success and live failure combine into a partial response", func(t *testing.T) {
		_, quoteCache := newTestCache(t, time.Minute)
		request := validRequest()

		cached := partner.Quote{Partner: "partner-slow", QuoteID: "cached-slow", PremiumCents: 91234, Currency: "BRL"}
		key := cache.Key("corretora-a", "partner-slow", request.Fingerprint())
		if err := quoteCache.Set(context.Background(), key, cached); err != nil {
			t.Fatalf("Set: %v", err)
		}

		quoter := &fakeQuoter{premiums: defaultPremiums(), failOn: failing("partner-flaky")}
		response, err := NewService(threePartners, quoter, quoteCache, nil).Quote(context.Background(), "corretora-a", request)
		if err != nil {
			t.Fatalf("Quote: %v", err)
		}

		for _, called := range quoter.calls {
			if called == "partner-slow" {
				t.Fatalf("quoter called for partner-slow even though it had a cache entry: %v", quoter.calls)
			}
		}

		if !response.Degraded {
			t.Error("degraded is false with partner-flaky failing, expected true")
		}
		if got := response.MissingPartners; len(got) != 1 || got[0] != "partner-flaky" {
			t.Errorf("missing_partners %v, expected [partner-flaky]", got)
		}
		if len(response.Quotes) != 2 {
			t.Fatalf("%d quotes, expected 2 (cache hit + live success)", len(response.Quotes))
		}

		bySource := map[string]partner.Quote{}
		for _, q := range response.Quotes {
			bySource[q.Partner] = q
		}
		slow, ok := bySource["partner-slow"]
		if !ok {
			t.Fatalf("no quote from partner-slow: %+v", response.Quotes)
		}
		if slow.Source != "cache" || slow.AgeSeconds == nil {
			t.Errorf("partner-slow quote %+v, expected source cache with age_seconds set", slow)
		}
		degrading, ok := bySource["partner-degrading"]
		if !ok {
			t.Fatalf("no quote from partner-degrading: %+v", response.Quotes)
		}
		if degrading.Source != "live" {
			t.Errorf("partner-degrading source %q, expected live", degrading.Source)
		}
	})

	t.Run("empty cache and every partner failing live produces no quotes and every partner missing", func(t *testing.T) {
		_, quoteCache := newTestCache(t, time.Minute)

		quoter := &fakeQuoter{premiums: defaultPremiums(), failOn: failing("partner-slow", "partner-flaky", "partner-degrading")}
		response, err := NewService(threePartners, quoter, quoteCache, nil).Quote(context.Background(), "corretora-a", validRequest())
		if err != nil {
			t.Fatalf("Quote: %v, expected a response with no quotes instead of an error", err)
		}

		if len(response.Quotes) != 0 {
			t.Fatalf("%d quotes, expected 0 (the handler turns this into the T07b 503)", len(response.Quotes))
		}
		expected := []string{"partner-slow", "partner-flaky", "partner-degrading"}
		if len(response.MissingPartners) != len(expected) {
			t.Fatalf("missing_partners %v, expected %v", response.MissingPartners, expected)
		}
		for i, name := range expected {
			if response.MissingPartners[i] != name {
				t.Errorf("missing_partners[%d] %q, expected %q", i, response.MissingPartners[i], name)
			}
		}
	})
}

// alwaysFails is a breaker fn used only to force a *resilience.Breaker into a known state during test
// setup; it never reaches the fakeQuoter (T09 critério de aceite 1: the Quoter must not be called for
// a partner whose breaker is already open).
func alwaysFails() (partner.Quote, error) {
	return partner.Quote{}, errors.New("boom")
}

// TestQuoteSkipsThePartnerWhenItsBreakerIsOpen is the teste que prova of T09 (FDD seção 4, passo 2.3):
// service.Quote must wrap the live call in breakers[p.Name].Execute when a breaker is configured for
// that partner, and call the Quoter directly (as in T08) otherwise.
func TestQuoteSkipsThePartnerWhenItsBreakerIsOpen(t *testing.T) {
	t.Run("an open breaker skips the Quoter and marks the partner missing, other partners unaffected", func(t *testing.T) {
		breaker := resilience.NewBreaker("partner-flaky", resilience.Config{
			ConsecutiveFailures: 1,
			OpenTimeout:         time.Hour,
			HalfOpenMaxRequests: 1,
		})
		if _, err := breaker.Execute(context.Background(), alwaysFails); err == nil {
			t.Fatal("forcing the breaker open: expected an error from alwaysFails")
		}
		if state := breaker.State(); state != gobreaker.StateOpen {
			t.Fatalf("breaker state %v after 1 consecutive failure (ConsecutiveFailures: 1), expected StateOpen", state)
		}

		breakers := map[string]*resilience.Breaker{"partner-flaky": breaker}
		quoter := &fakeQuoter{premiums: defaultPremiums()}
		response, err := NewService(threePartners, quoter, nil, breakers).
			Quote(context.Background(), "corretora-a", validRequest())
		if err != nil {
			t.Fatalf("Quote: %v, expected a partial response instead of an error", err)
		}

		for _, called := range quoter.calls {
			if called == "partner-flaky" {
				t.Fatalf("the quoter was called for partner-flaky even though its breaker is open: %v", quoter.calls)
			}
		}
		if got := len(quoter.calls); got != 2 {
			t.Fatalf("quoter called %d times, expected 2 (the two partners without a configured breaker): %v", got, quoter.calls)
		}
		var sawSlow, sawDegrading bool
		for _, called := range quoter.calls {
			switch called {
			case "partner-slow":
				sawSlow = true
			case "partner-degrading":
				sawDegrading = true
			}
		}
		if !sawSlow || !sawDegrading {
			t.Fatalf("quoter.calls %v, expected partner-slow and partner-degrading (no breaker configured for them)", quoter.calls)
		}

		if !response.Degraded {
			t.Error("degraded is false with partner-flaky's breaker open, expected true")
		}
		if got := response.MissingPartners; len(got) != 1 || got[0] != "partner-flaky" {
			t.Errorf("missing_partners %v, expected [partner-flaky]", got)
		}
		if len(response.Quotes) != 2 {
			t.Fatalf("%d quotes, expected 2 (the two partners without an open breaker)", len(response.Quotes))
		}
	})

	t.Run("ErrTooManyRequests from a saturated half-open breaker is treated like any other partner failure", func(t *testing.T) {
		breaker := resilience.NewBreaker("partner-flaky", resilience.Config{
			ConsecutiveFailures: 1,
			OpenTimeout:         20 * time.Millisecond,
			HalfOpenMaxRequests: 1,
		})
		if _, err := breaker.Execute(context.Background(), alwaysFails); err == nil {
			t.Fatal("forcing the breaker open: expected an error from alwaysFails")
		}
		time.Sleep(30 * time.Millisecond) // past OpenTimeout: the breaker now allows exactly 1 half-open probe.

		started := make(chan struct{})
		release := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = breaker.Execute(context.Background(), func() (partner.Quote, error) {
				close(started)
				<-release
				return partner.Quote{}, nil
			})
		}()
		<-started // the single half-open slot (HalfOpenMaxRequests: 1) is now occupied.
		defer func() {
			close(release)
			wg.Wait()
		}()

		breakers := map[string]*resilience.Breaker{"partner-flaky": breaker}
		quoter := &fakeQuoter{premiums: defaultPremiums()}
		response, err := NewService(threePartners, quoter, nil, breakers).
			Quote(context.Background(), "corretora-a", validRequest())
		if err != nil {
			t.Fatalf("Quote: %v, expected a partial response instead of an error", err)
		}

		for _, called := range quoter.calls {
			if called == "partner-flaky" {
				t.Fatalf("the quoter was called for partner-flaky even though its breaker rejected with ErrTooManyRequests: %v", quoter.calls)
			}
		}
		if got := response.MissingPartners; len(got) != 1 || got[0] != "partner-flaky" {
			t.Errorf("missing_partners %v, expected [partner-flaky] (ErrTooManyRequests counts as a failure, FDD seção 6)", got)
		}
		if !response.Degraded {
			t.Error("degraded is false with partner-flaky rejected by ErrTooManyRequests, expected true")
		}
	})
}

// newInMemoryTracer builds a *sdktrace.TracerProvider backed by an in-memory exporter, local to the
// test, and returns both the exporter (to read the spans back) and a trace.Tracer to pass to
// quotation.WithTracer. It deliberately does NOT call otel.SetTracerProvider: mutating the global
// provider is not safe to rely on across repeated runs of the same test binary, because the OTel
// SDK's global package only rewires an already-created Tracer to a new delegate once per process
// (go.opentelemetry.io/otel/internal/global/state.go, delegateTraceOnce) — a second
// otel.SetTracerProvider call in the same binary would silently leave any package-level
// otel.Tracer(...) var pointed at the first provider. Injecting the Tracer via WithTracer avoids the
// global entirely.
func newInMemoryTracer(t *testing.T) (*tracetest.InMemoryExporter, trace.Tracer) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return exporter, tp.Tracer("test")
}

// partnerQuoteResult finds the "partner.quote" span for partnerName (identified by the "partner.name"
// attribute) and returns its "partner.result" attribute.
func partnerQuoteResult(spans tracetest.SpanStubs, partnerName string) (string, bool) {
	for _, span := range spans {
		if span.Name != "partner.quote" {
			continue
		}
		var gotPartner, result string
		for _, kv := range span.Attributes {
			switch kv.Key {
			case "partner.name":
				gotPartner = kv.Value.AsString()
			case "partner.result":
				result = kv.Value.AsString()
			}
		}
		if gotPartner == partnerName {
			return result, true
		}
	}
	return "", false
}

// TestPartnerQuoteSpanCarriesResult is a critério de aceite of T09b (FDD seção 7): a cache hit must
// carry partner.result="cache_hit" and an open breaker must carry partner.result="circuit_open" in
// the partner.quote span, even though neither ever reaches the Quoter — otherwise a
// circuit-shortened call looks like a mysteriously fast live quote in the trace.
func TestPartnerQuoteSpanCarriesResult(t *testing.T) {
	exporter, tracer := newInMemoryTracer(t)

	_, quoteCache := newTestCache(t, time.Minute)
	request := validRequest()
	cached := partner.Quote{Partner: "partner-slow", QuoteID: "cached-slow", PremiumCents: 91234, Currency: "BRL"}
	key := cache.Key("corretora-a", "partner-slow", request.Fingerprint())
	if err := quoteCache.Set(context.Background(), key, cached); err != nil {
		t.Fatalf("Set: %v", err)
	}

	breaker := resilience.NewBreaker("partner-flaky", resilience.Config{
		ConsecutiveFailures: 1,
		OpenTimeout:         time.Hour,
		HalfOpenMaxRequests: 1,
	})
	if _, err := breaker.Execute(context.Background(), alwaysFails); err == nil {
		t.Fatal("forcing the breaker open: expected an error from alwaysFails")
	}
	breakers := map[string]*resilience.Breaker{"partner-flaky": breaker}

	quoter := &fakeQuoter{premiums: defaultPremiums()}
	if _, err := NewService(threePartners, quoter, quoteCache, breakers, WithTracer(tracer)).
		Quote(context.Background(), "corretora-a", request); err != nil {
		t.Fatalf("Quote: %v", err)
	}

	spans := exporter.GetSpans()
	if result, ok := partnerQuoteResult(spans, "partner-slow"); !ok || result != "cache_hit" {
		t.Fatalf("partner.result for partner-slow = (%q, ok=%v), want (cache_hit, true)", result, ok)
	}
	if result, ok := partnerQuoteResult(spans, "partner-flaky"); !ok || result != "circuit_open" {
		t.Fatalf("partner.result for partner-flaky = (%q, ok=%v), want (circuit_open, true)", result, ok)
	}
	if result, ok := partnerQuoteResult(spans, "partner-degrading"); !ok || result != "live_success" {
		t.Fatalf("partner.result for partner-degrading = (%q, ok=%v), want (live_success, true)", result, ok)
	}
}

// TestPartnerQuoteLogHasNoPersonalData is the teste que prova required by T09b (FDD seção 7): the
// structured, per-partner log line must never carry the driver's document (CPF), the vehicle's
// plate/model/year or a quote_id — this test deliberately sends real-looking personal data and a
// breaker-open partner, then greps the captured log output for every forbidden value.
func TestPartnerQuoteLogHasNoPersonalData(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	request := Request{
		Driver:  Driver{Document: "39053344705", BirthYear: 1988},
		Vehicle: Vehicle{Plate: "BRA2E19", Model: "Civic Turbo", Year: 2021, ValueCents: 9200000},
	}
	if err := request.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	breaker := resilience.NewBreaker("partner-degrading", resilience.Config{
		ConsecutiveFailures: 1,
		OpenTimeout:         time.Hour,
		HalfOpenMaxRequests: 1,
	})
	if _, err := breaker.Execute(context.Background(), alwaysFails); err == nil {
		t.Fatal("forcing the breaker open: expected an error from alwaysFails")
	}
	breakers := map[string]*resilience.Breaker{"partner-degrading": breaker}

	quoter := &fakeQuoter{premiums: defaultPremiums(), failOn: failing("partner-flaky")}
	if _, err := NewService(threePartners, quoter, nil, breakers).
		Quote(context.Background(), "corretora-a", request); err != nil {
		t.Fatalf("Quote: %v", err)
	}

	logOutput := buf.String()
	if logOutput == "" {
		t.Fatal("no log output captured; the per-partner log line is not being written")
	}

	forbidden := []string{
		request.Driver.Document,
		request.Vehicle.Plate,
		request.Vehicle.Model,
		strconv.Itoa(request.Vehicle.Year),
		"quote_id",
	}
	for _, needle := range forbidden {
		if strings.Contains(logOutput, needle) {
			t.Fatalf("log output contains forbidden value %q:\n%s", needle, logOutput)
		}
	}
}
