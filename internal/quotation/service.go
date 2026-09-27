package quotation

import (
	"context"
	"sort"
	"time"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/cache"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
)

type Quoter interface {
	Quote(ctx context.Context, p platform.Partner, request any) (partner.Quote, error)
}

type Service struct {
	partners []platform.Partner
	quoter   Quoter
	cache    *cache.QuoteCache
}

// NewService builds a Service. quoteCache may be nil, in which case cache-aside is skipped
// entirely (always a miss, no call to Redis at all) so callers that don't exercise the cache can
// keep passing nil (FDD seção 4, T08 abordagem sugerida).
func NewService(partners []platform.Partner, quoter Quoter, quoteCache *cache.QuoteCache) *Service {
	return &Service{partners: partners, quoter: quoter, cache: quoteCache}
}

func (s *Service) Quote(ctx context.Context, tenant string, request Request) (Response, error) {
	start := time.Now()
	forPartner := partnerRequest{Broker: tenant, Request: request}
	fingerprint := request.Fingerprint()

	quotes := make([]partner.Quote, 0, len(s.partners))
	missingPartners := make([]string, 0, len(s.partners))
	for _, p := range s.partners {
		quote, err := s.quoteForPartner(ctx, tenant, p, forPartner, fingerprint)
		if err != nil {
			missingPartners = append(missingPartners, p.Name)
			continue
		}
		quotes = append(quotes, quote)
	}

	sort.Slice(quotes, func(i, j int) bool {
		return quotes[i].PremiumCents < quotes[j].PremiumCents
	})

	return Response{
		TenantID:        tenant,
		Quotes:          quotes,
		ElapsedMs:       time.Since(start).Milliseconds(),
		Degraded:        len(missingPartners) > 0,
		MissingPartners: missingPartners,
	}, nil
}

// quoteForPartner implements the cache-aside read path for a single partner (FDD seção 4, passos
// 2.1-2.4): a cache hit skips the Quoter entirely and is never treated as degradation; a miss (or
// any Redis failure, which counts as a miss) falls back to calling the Quoter directly, exactly as
// before T08 (the breaker only wraps this call starting at T09). A live success is written back to
// the cache on a best-effort basis: a Set error is ignored, it never fails the request.
func (s *Service) quoteForPartner(
	ctx context.Context, tenant string, p platform.Partner, forPartner partnerRequest, fingerprint string,
) (partner.Quote, error) {
	key := s.cacheKey(tenant, p.Name, fingerprint)

	if quote, ok := s.fromCache(ctx, key); ok {
		return quote, nil
	}

	quote, err := s.quoter.Quote(ctx, p, forPartner)
	if err != nil {
		return partner.Quote{}, err
	}
	quote.Source = "live"

	s.store(ctx, key, quote)
	return quote, nil
}

// cacheKey returns "" when there is no cache configured; fromCache/store treat that as an
// unconditional miss/no-op without ever touching Redis.
func (s *Service) cacheKey(tenant, partnerName, fingerprint string) string {
	if s.cache == nil {
		return ""
	}
	return cache.Key(tenant, partnerName, fingerprint)
}

func (s *Service) fromCache(ctx context.Context, key string) (partner.Quote, bool) {
	if s.cache == nil || key == "" {
		return partner.Quote{}, false
	}

	quote, storedAt, ok := s.cache.Get(ctx, key)
	if !ok {
		return partner.Quote{}, false
	}

	age := int64(time.Since(storedAt).Seconds())
	quote.Source = "cache"
	quote.AgeSeconds = &age
	return quote, true
}

func (s *Service) store(ctx context.Context, key string, quote partner.Quote) {
	if s.cache == nil || key == "" {
		return
	}
	_ = s.cache.Set(ctx, key, quote)
}
