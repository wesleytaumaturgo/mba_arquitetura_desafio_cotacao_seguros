package quotation

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
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
	failOn     string
	calls      []string
	requests   []any
	concurrent atomic.Int32
	peak       atomic.Int32
}

func (f *fakeQuoter) Quote(_ context.Context, p platform.Partner, request any) (partner.Quote, error) {
	if now := f.concurrent.Add(1); now > f.peak.Load() {
		f.peak.Store(now)
	}
	defer f.concurrent.Add(-1)

	f.calls = append(f.calls, p.Name)
	f.requests = append(f.requests, request)
	time.Sleep(f.delay)

	if p.Name == f.failOn {
		return partner.Quote{}, &partner.Error{Partner: p.Name, Status: 503, Reason: "partner answered 503"}
	}
	return partner.Quote{Partner: p.Name, PremiumCents: f.premiums[p.Name], Currency: "BRL"}, nil
}

func defaultPremiums() map[string]int64 {
	return map[string]int64{"partner-slow": 180000, "partner-flaky": 90000, "partner-degrading": 120000}
}

func TestQuoteAggregatesTheThreePartnersSortedByPremium(t *testing.T) {
	quoter := &fakeQuoter{premiums: defaultPremiums()}
	response, err := NewService(threePartners, quoter).Quote(context.Background(), "corretora-a", validRequest())
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
	if _, err := NewService(threePartners, quoter).Quote(context.Background(), "corretora-a", validRequest()); err != nil {
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

func TestOnePartnerDownBringsDownTheWholeRequest(t *testing.T) {
	quoter := &fakeQuoter{premiums: defaultPremiums(), failOn: "partner-flaky"}

	_, err := NewService(threePartners, quoter).Quote(context.Background(), "corretora-a", validRequest())
	if err == nil {
		t.Fatal("one partner down and the request answered success — there is a fallback where there should be none")
	}

	var failure *partner.Error
	if !errors.As(err, &failure) || failure.Partner != "partner-flaky" {
		t.Fatalf("error %v does not identify the partner that failed", err)
	}

	if last := quoter.calls[len(quoter.calls)-1]; last != "partner-flaky" {
		t.Errorf("last partner called was %q, expected partner-flaky", last)
	}
}

func TestBrokerGoesInThePartnerRequest(t *testing.T) {
	quoter := &fakeQuoter{premiums: defaultPremiums()}
	service := NewService(threePartners[:1], quoter)

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
