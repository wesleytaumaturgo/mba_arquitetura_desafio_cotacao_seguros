package quotation

import (
	"context"
	"sort"
	"time"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/cache"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/resilience"
)

type Quoter interface {
	Quote(ctx context.Context, p platform.Partner, request any) (partner.Quote, error)
}

type Service struct {
	partners []platform.Partner
	quoter   Quoter
	cache    *cache.QuoteCache
	breakers map[string]*resilience.Breaker
}

// NewService builds a Service. quoteCache may be nil, in which case cache-aside is skipped
// entirely (always a miss, no call to Redis at all) so callers that don't exercise the cache can
// keep passing nil (FDD seção 4, T08 abordagem sugerida). breakers may also be nil, or simply not
// have an entry for a given partner: a nil map read is safe in Go and quoteForPartner falls back to
// calling the Quoter directly for that partner, exactly as in T08 (FDD seção 4, T09 abordagem
// sugerida).
func NewService(
	partners []platform.Partner, quoter Quoter, quoteCache *cache.QuoteCache, breakers map[string]*resilience.Breaker,
) *Service {
	return &Service{partners: partners, quoter: quoter, cache: quoteCache, breakers: breakers}
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
// any Redis failure, which counts as a miss) falls back to calling the Quoter, wrapped in that
// partner's breaker when one is configured (FDD seção 4, passo 2.3; T09). A live success is written
// back to the cache on a best-effort basis: a Set error is ignored, it never fails the request.
func (s *Service) quoteForPartner(
	ctx context.Context, tenant string, p platform.Partner, forPartner partnerRequest, fingerprint string,
) (partner.Quote, error) {
	key := s.cacheKey(tenant, p.Name, fingerprint)

	if quote, ok := s.fromCache(ctx, key); ok {
		return quote, nil
	}

	quote, err := s.callQuoter(ctx, p, forPartner)
	if err != nil {
		return partner.Quote{}, err
	}
	quote.Source = "live"

	s.store(ctx, key, quote)
	return quote, nil
}

// callQuoter calls the Quoter directly, or through the partner's breaker when one is configured
// (a nil map read and a missing key are both safe in Go). Any error the breaker returns instead of
// calling the Quoter — gobreaker.ErrOpenState, gobreaker.ErrTooManyRequests or anything else — is
// handled by the caller exactly like any other partner failure: it becomes a missing_partners entry
// (FDD seção 6).
func (s *Service) callQuoter(ctx context.Context, p platform.Partner, forPartner partnerRequest) (partner.Quote, error) {
	breaker := s.breakers[p.Name]
	if breaker == nil {
		return s.quoter.Quote(ctx, p, forPartner)
	}
	return breaker.Execute(ctx, func() (partner.Quote, error) {
		return s.quoter.Quote(ctx, p, forPartner)
	})
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
