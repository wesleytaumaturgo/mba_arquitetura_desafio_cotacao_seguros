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

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
)

// QuoteCache stores partner.Quote values in Redis with a fixed TTL. Get is best-effort: any error
// (missing key, invalid JSON, network failure) is logged and reported to the caller as ok == false,
// never as an error, per FDD seção 5.
type QuoteCache struct {
	client *redis.Client
	ttl    time.Duration
}

// NewQuoteCache builds a QuoteCache backed by client, storing entries with the given ttl.
func NewQuoteCache(client *redis.Client, ttl time.Duration) *QuoteCache {
	return &QuoteCache{client: client, ttl: ttl}
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
		return fmt.Errorf("cache: set key %q: %w", key, err)
	}
	return nil
}

// Get reads and deserializes the entry stored under key. Any failure (missing key, invalid JSON,
// Redis unreachable) is logged and reported as ok == false; the caller never sees the error.
func (c *QuoteCache) Get(ctx context.Context, key string) (quote partner.Quote, storedAt time.Time, ok bool) {
	raw, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		if err != redis.Nil {
			log.Printf("cache: get key %q failed: %v", key, err)
		}
		return partner.Quote{}, time.Time{}, false
	}

	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		log.Printf("cache: unmarshal key %q failed: %v", key, err)
		return partner.Quote{}, time.Time{}, false
	}

	return e.Quote, e.StoredAt, true
}
