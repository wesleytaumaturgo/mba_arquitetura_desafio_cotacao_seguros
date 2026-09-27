package quotation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const validBody = `{
  "driver": {"document": "12345678901", "birth_year": 1988},
  "vehicle": {"plate": "abc1d23", "model": "Gol 1.0", "year": 2020, "value_cents": 8500000},
  "coverage": "comprehensive"
}`

func testAPI(quoter Quoter) http.Handler {
	return NewAPI(NewService(threePartners, quoter), []string{"corretora-a", "corretora-b"}).Routes()
}

func postQuotes(h http.Handler, tenant, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/quotes", strings.NewReader(body))
	if tenant != "" {
		req.Header.Set(TenantHeader, tenant)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	return response
}

func TestQuotesReturnsAggregatedQuotes(t *testing.T) {
	response := postQuotes(testAPI(&fakeQuoter{premiums: defaultPremiums()}), "corretora-a", validBody)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d, expected 200: %s", response.Code, response.Body)
	}

	var body Response
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	if len(body.Quotes) != 3 {
		t.Fatalf("%d quotes, expected 3", len(body.Quotes))
	}
	if body.TenantID != "corretora-a" {
		t.Errorf("tenant_id %q, expected corretora-a", body.TenantID)
	}
}

func TestQuotesRejectsRequestWithoutTenant(t *testing.T) {
	quoter := &fakeQuoter{premiums: defaultPremiums()}
	response := postQuotes(testAPI(quoter), "", validBody)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status %d, expected 400", response.Code)
	}
	if len(quoter.calls) != 0 {
		t.Fatalf("the partners were called (%v) even without a tenant", quoter.calls)
	}
}

func TestQuotesRejectsUnknownBroker(t *testing.T) {
	quoter := &fakeQuoter{premiums: defaultPremiums()}
	response := postQuotes(testAPI(quoter), "corretora-pirata", validBody)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status %d, expected 403", response.Code)
	}
	if len(quoter.calls) != 0 {
		t.Fatalf("the partners were called (%v) for a broker that is not enabled", quoter.calls)
	}
}

func TestQuotesRejectsInvalidBody(t *testing.T) {
	cases := map[string]string{
		"broken json":      `{"driver":`,
		"missing document": `{"driver":{"birth_year":1988},"vehicle":{"plate":"ABC1D23","year":2020,"value_cents":100}}`,
		"missing plate":    `{"driver":{"document":"1","birth_year":1988},"vehicle":{"year":2020,"value_cents":100}}`,
		"zero value":       `{"driver":{"document":"1","birth_year":1988},"vehicle":{"plate":"A","year":2020,"value_cents":0}}`,
		"invalid coverage": `{"driver":{"document":"1","birth_year":1988},"vehicle":{"plate":"A","year":2020,"value_cents":1},"coverage":"vip"}`,
		"unknown field":    `{"driver":{"document":"1","birth_year":1988},"vehicle":{"plate":"A","year":2020,"value_cents":1},"discount":true}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			response := postQuotes(testAPI(&fakeQuoter{premiums: defaultPremiums()}), "corretora-a", body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status %d, expected 400: %s", response.Code, response.Body)
			}
		})
	}
}

func TestQuotesRespondsWithAPartialResponseWhenOnePartnerIsDown(t *testing.T) {
	quoter := &fakeQuoter{premiums: defaultPremiums(), failOn: failing("partner-flaky")}
	response := postQuotes(testAPI(quoter), "corretora-a", validBody)

	if response.Code != http.StatusOK {
		t.Fatalf("status %d, expected 200: %s", response.Code, response.Body)
	}

	var body Response
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("unreadable response: %v", err)
	}
	if !body.Degraded {
		t.Error("degraded is false with one partner down, expected true")
	}
	if got := body.MissingPartners; len(got) != 1 || got[0] != "partner-flaky" {
		t.Errorf("missing_partners %v, expected [partner-flaky]", got)
	}
	if len(body.Quotes) != 2 {
		t.Fatalf("%d quotes, expected 2", len(body.Quotes))
	}
	for i := 1; i < len(body.Quotes); i++ {
		if body.Quotes[i-1].PremiumCents > body.Quotes[i].PremiumCents {
			t.Errorf("quotes not sorted by premium_cents: %+v", body.Quotes)
		}
	}
}

func TestQuotesResponds503WhenNoPartnerRespond(t *testing.T) {
	quoter := &fakeQuoter{premiums: defaultPremiums(), failOn: failing("partner-slow", "partner-flaky", "partner-degrading")}
	response := postQuotes(testAPI(quoter), "corretora-a", validBody)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, expected 503: %s", response.Code, response.Body)
	}

	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("unreadable error: %v", err)
	}
	if decoded["error"] != "no partner quote available" {
		t.Errorf("error %v, expected %q", decoded["error"], "no partner quote available")
	}
	if decoded["tenant_id"] != "corretora-a" {
		t.Errorf("tenant_id %v, expected corretora-a", decoded["tenant_id"])
	}
	missing, ok := decoded["missing_partners"].([]any)
	if !ok || len(missing) != 3 {
		t.Fatalf("missing_partners %v, expected the three partners", decoded["missing_partners"])
	}
	expected := []string{"partner-slow", "partner-flaky", "partner-degrading"}
	for i, name := range expected {
		if missing[i] != name {
			t.Errorf("missing_partners[%d] %v, expected %q", i, missing[i], name)
		}
	}
}

func TestHealthz(t *testing.T) {
	response := httptest.NewRecorder()
	testAPI(&fakeQuoter{premiums: defaultPremiums()}).
		ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status %d, expected 200", response.Code)
	}
}
