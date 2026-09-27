package cache

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
)

func newTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()

	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() failed: %v", err)
	}
	t.Cleanup(server.Close)

	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return server, client
}

func TestKeyFormatIsStableAndVersioned(t *testing.T) {
	fingerprint := "12345678900|1990|ABC1234|civic|2020|5000000|500000"

	key := Key("corretora-a", "partner-flaky", fingerprint)
	again := Key("corretora-a", "partner-flaky", fingerprint)

	if key != again {
		t.Fatalf("Key() is not stable for the same input: %q != %q", key, again)
	}

	prefix := "quote:v1:corretora-a:partner-flaky:"
	if !strings.HasPrefix(key, prefix) {
		t.Fatalf("Key() = %q, want prefix %q", key, prefix)
	}

	hash := strings.TrimPrefix(key, prefix)
	if len(hash) != 16 {
		t.Fatalf("Key() hash suffix has %d chars, want 16 (%q)", len(hash), hash)
	}
}

func TestSetThenGetRoundTrips(t *testing.T) {
	_, client := newTestRedis(t)
	c := NewQuoteCache(client, time.Minute)
	ctx := context.Background()

	quote := partner.Quote{
		Partner:         "partner-flaky",
		QuoteID:         "quote-123",
		PremiumCents:    84210,
		Currency:        "BRL",
		CoverageCents:   5000000,
		ValidForSeconds: 300,
	}
	key := Key("corretora-a", "partner-flaky", "some-fingerprint")

	before := time.Now()
	if err := c.Set(ctx, key, quote); err != nil {
		t.Fatalf("Set() returned error: %v", err)
	}
	after := time.Now()

	got, storedAt, ok := c.Get(ctx, key)
	if !ok {
		t.Fatalf("Get() ok = false, want true")
	}
	if got != quote {
		t.Fatalf("Get() quote = %+v, want %+v", got, quote)
	}
	if storedAt.Before(before.Add(-time.Second)) || storedAt.After(after.Add(time.Second)) {
		t.Fatalf("Get() storedAt = %v, want within 1s of [%v, %v]", storedAt, before, after)
	}
}

func TestGetMissesWhenTheKeyDoesNotExistOrExpired(t *testing.T) {
	server, client := newTestRedis(t)
	ctx := context.Background()

	t.Run("never written", func(t *testing.T) {
		c := NewQuoteCache(client, time.Minute)
		key := Key("corretora-a", "partner-flaky", "never-written")

		_, _, ok := c.Get(ctx, key)
		if ok {
			t.Fatalf("Get() ok = true, want false for a key never written")
		}
	})

	t.Run("expired after short TTL", func(t *testing.T) {
		ttl := 50 * time.Millisecond
		c := NewQuoteCache(client, ttl)
		key := Key("corretora-a", "partner-flaky", "expires-soon")
		quote := partner.Quote{Partner: "partner-flaky", QuoteID: "quote-999"}

		if err := c.Set(ctx, key, quote); err != nil {
			t.Fatalf("Set() returned error: %v", err)
		}

		// miniredis models TTL as a duration that is decremented explicitly (FastForward), not by
		// real wall-clock time, so we advance its fake clock past ttl instead of sleeping the test.
		server.FastForward(ttl + 10*time.Millisecond)

		_, _, ok := c.Get(ctx, key)
		if ok {
			t.Fatalf("Get() ok = true, want false after TTL expired")
		}
	})
}

func TestGetIsBestEffortWhenRedisIsUnreachable(t *testing.T) {
	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() failed: %v", err)
	}
	t.Cleanup(server.Close)

	// Tight timeouts and no retries so the "server is gone" assertions below run fast instead of
	// waiting out go-redis's default dial backoff.
	client := redis.NewClient(&redis.Options{
		Addr:          server.Addr(),
		MaxRetries:    0,
		DialTimeout:   50 * time.Millisecond,
		DialerRetries: 1,
	})
	t.Cleanup(func() { _ = client.Close() })

	c := NewQuoteCache(client, time.Minute)
	ctx := context.Background()

	key := Key("corretora-a", "partner-flaky", "unreachable")
	quote := partner.Quote{Partner: "partner-flaky", QuoteID: "quote-000"}

	server.Close()

	if err := c.Set(ctx, key, quote); err == nil {
		t.Fatalf("Set() error = nil, want a non-nil error when redis is unreachable")
	}

	_, _, ok := c.Get(ctx, key)
	if ok {
		t.Fatalf("Get() ok = true, want false when redis is unreachable")
	}
}

func TestSetPayloadHasNoDriverOrVehicleFields(t *testing.T) {
	_, client := newTestRedis(t)
	c := NewQuoteCache(client, time.Minute)
	ctx := context.Background()

	quote := partner.Quote{
		Partner:         "partner-flaky",
		QuoteID:         "quote-123",
		PremiumCents:    84210,
		Currency:        "BRL",
		CoverageCents:   5000000,
		ValidForSeconds: 300,
	}
	key := Key("corretora-a", "partner-flaky", "no-pii")

	if err := c.Set(ctx, key, quote); err != nil {
		t.Fatalf("Set() returned error: %v", err)
	}

	raw, err := client.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("client.Get() returned error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("json.Unmarshal() returned error: %v", err)
	}

	for _, forbidden := range []string{"driver", "vehicle", "cpf", "plate", "document"} {
		if _, present := payload[forbidden]; present {
			t.Fatalf("serialized payload has forbidden field %q: %v", forbidden, payload)
		}
	}
}

// cacheResultCounts collects the counter registered on reader and returns, for
// quotation_cache_result_total, the cumulative count observed for each "result" attribute value.
func cacheResultCounts(t *testing.T, reader sdkmetric.Reader) map[string]int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("reader.Collect: %v", err)
	}

	counts := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "quotation_cache_result_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("quotation_cache_result_total is a %T, want metricdata.Sum[int64]", m.Data)
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value("result"); ok {
					counts[v.AsString()] += dp.Value
				}
			}
		}
	}
	return counts
}

// TestCacheResultCounterIncrements is the teste que prova of T09b's counter (FDD seção 7):
// quotation_cache_result_total{result="hit"|"miss"|"redis_error"}, incremented internally by Get/Set
// without changing their public signature (Get still only returns ok bool to the caller).
func TestCacheResultCounterIncrements(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := meterProvider.Meter("test")

	server, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() failed: %v", err)
	}
	t.Cleanup(server.Close)

	// Tight timeouts and no retries, as in TestGetIsBestEffortWhenRedisIsUnreachable, so the
	// redis_error assertions below run fast instead of waiting out go-redis's default dial backoff.
	client := redis.NewClient(&redis.Options{
		Addr:          server.Addr(),
		MaxRetries:    0,
		DialTimeout:   50 * time.Millisecond,
		DialerRetries: 1,
	})
	t.Cleanup(func() { _ = client.Close() })

	c := NewQuoteCache(client, time.Minute, WithMeter(meter))
	ctx := context.Background()

	quote := partner.Quote{Partner: "partner-flaky", QuoteID: "quote-123", PremiumCents: 84210}
	key := Key("corretora-a", "partner-flaky", "some-fingerprint")

	if err := c.Set(ctx, key, quote); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, _, ok := c.Get(ctx, key); !ok {
		t.Fatalf("Get() ok = false, want true right after Set")
	}
	if _, _, ok := c.Get(ctx, Key("corretora-a", "partner-flaky", "never-written")); ok {
		t.Fatalf("Get() ok = true, want false for a key never written")
	}

	counts := cacheResultCounts(t, reader)
	if counts["hit"] != 1 {
		t.Errorf("result=hit count = %d, want 1", counts["hit"])
	}
	if counts["miss"] != 1 {
		t.Errorf("result=miss count = %d, want 1", counts["miss"])
	}
	if counts["redis_error"] != 0 {
		t.Errorf("result=redis_error count = %d, want 0 before redis is closed", counts["redis_error"])
	}

	server.Close() // redis unreachable from here on: Get/Set failures must count as redis_error.

	if err := c.Set(ctx, key, quote); err == nil {
		t.Fatal("Set() error = nil, want a non-nil error once redis is closed")
	}
	if _, _, ok := c.Get(ctx, key); ok {
		t.Fatalf("Get() ok = true, want false once redis is closed")
	}

	counts = cacheResultCounts(t, reader)
	if counts["redis_error"] != 2 {
		t.Errorf("result=redis_error count = %d, want 2 (one Set, one Get) after redis is closed", counts["redis_error"])
	}
	if counts["hit"] != 1 {
		t.Errorf("result=hit count = %d, want unchanged at 1 after redis is closed", counts["hit"])
	}
	if counts["miss"] != 1 {
		t.Errorf("result=miss count = %d, want unchanged at 1 after redis is closed", counts["miss"])
	}
}
