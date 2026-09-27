// Package cache wraps go-redis/v9 with the narrow contract the quotation service needs to store and
// retrieve a partner.Quote by an already-computed key (FDD seção 5). It never imports
// internal/quotation: the key is built by the caller via Key, not by this package.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
)

// meterName is the instrumentation scope used for this package's metrics, the same one used
// elsewhere in the service (FDD seção 7).
const meterName = "quotation-api"

// QuoteCache stores partner.Quote values in Redis with a fixed TTL. Get is best-effort: any error
// (missing key, invalid JSON, network failure) is logged and reported to the caller as ok == false,
// never as an error, per FDD seção 5. Internally, every Get/Set outcome is also counted by
// quotation_cache_result_total{result="hit"|"miss"|"redis_error"} (T09b, FDD seção 7); that
// three-way distinction only exists inside this package, it never leaks into Get's return value.
type QuoteCache struct {
	client        *redis.Client
	ttl           time.Duration
	meter         metric.Meter
	resultCounter metric.Int64Counter
}

// Option configures optional behaviour of a QuoteCache. See WithMeter.
type Option func(*QuoteCache)

// WithMeter overrides the otel.Meter used to record quotation_cache_result_total. Production code
// does not need it: NewQuoteCache defaults to otel.Meter("quotation-api"). Tests use it to inject a
// Meter backed by a local, in-memory MeterProvider (e.g. one with a ManualReader), so they can read
// the counter's value without touching the process-wide global provider.
func WithMeter(m metric.Meter) Option {
	return func(c *QuoteCache) { c.meter = m }
}

// NewQuoteCache builds a QuoteCache backed by client, storing entries with the given ttl.
func NewQuoteCache(client *redis.Client, ttl time.Duration, opts ...Option) *QuoteCache {
	c := &QuoteCache{client: client, ttl: ttl}
	for _, opt := range opts {
		opt(c)
	}
	if c.meter == nil {
		c.meter = otel.Meter(meterName)
	}

	counter, err := c.meter.Int64Counter(
		"quotation_cache_result_total",
		metric.WithDescription(`Cache-aside outcomes for partner quotes: "hit", "miss" or "redis_error".`),
	)
	if err != nil {
		log.Printf("cache: creating quotation_cache_result_total counter failed: %v", err)
	}
	c.resultCounter = counter

	return c
}

// recordResult increments quotation_cache_result_total{result=result}. result is the metric's only
// attribute besides the fixed name, low cardinality by construction (hit, miss, redis_error): never
// tenant_id, a partner name or a quote_id.
func (c *QuoteCache) recordResult(ctx context.Context, result string) {
	if c.resultCounter == nil {
		return
	}
	c.resultCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// Key derives the cache key for a (tenant, partner, fingerprint) triple. The fingerprint is hashed
// (SHA-256, first 16 hex chars) so the key never carries the raw request fields (FDD seção 5).
func Key(tenantID, partnerName, fingerprint string) string {
	sum := sha256.Sum256([]byte(fingerprint))
	hash := hex.EncodeToString(sum[:])[:16]
	return fmt.Sprintf("quote:v1:%s:%s:%s", tenantID, partnerName, hash)
}

// entry is the JSON payload stored in Redis: only partner.Quote fields plus the timestamp, never
// Driver/Vehicle data (FDD seção 5, critério de aceite de T06).
type entry struct {
	Quote    partner.Quote `json:"quote"`
	StoredAt time.Time     `json:"stored_at"`
}

// Set serializes quote with the current time and writes it under key with the configured TTL.
func (c *QuoteCache) Set(ctx context.Context, key string, quote partner.Quote) error {
	payload, err := json.Marshal(entry{Quote: quote, StoredAt: time.Now()})
	if err != nil {
		return fmt.Errorf("cache: marshal entry for key %q: %w", key, err)
	}

	if err := c.client.Set(ctx, key, payload, c.ttl).Err(); err != nil {
		c.recordResult(ctx, "redis_error")
		return fmt.Errorf("cache: set key %q: %w", key, err)
	}
	return nil
}

// Get reads and deserializes the entry stored under key. Any failure (missing key, invalid JSON,
// Redis unreachable) is logged and reported as ok == false; the caller never sees the error.
func (c *QuoteCache) Get(ctx context.Context, key string) (quote partner.Quote, storedAt time.Time, ok bool) {
	raw, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			c.recordResult(ctx, "miss")
			return partner.Quote{}, time.Time{}, false
		}
		log.Printf("cache: get key %q failed: %v", key, err)
		c.recordResult(ctx, "redis_error")
		return partner.Quote{}, time.Time{}, false
	}

	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		log.Printf("cache: unmarshal key %q failed: %v", key, err)
		c.recordResult(ctx, "redis_error")
		return partner.Quote{}, time.Time{}, false
	}

	c.recordResult(ctx, "hit")
	return e.Quote, e.StoredAt, true
}
