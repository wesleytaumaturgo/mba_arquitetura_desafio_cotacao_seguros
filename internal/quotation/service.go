package quotation

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/sony/gobreaker/v2"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/cache"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/partner"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/platform"
	"github.com/GuilhermeOliveira591/mba_arquitetura_desafio_cotacao_seguros/internal/resilience"
)

// tracerName is this package's instrumentation scope for the partner.quote span (T09b, FDD seção 7),
// the same scope name ("quotation-api") used for the service's metrics (internal/cache,
// internal/platform).
const tracerName = "quotation-api"

type Quoter interface {
	Quote(ctx context.Context, p platform.Partner, request any) (partner.Quote, error)
}

type Service struct {
	partners []platform.Partner
	quoter   Quoter
	cache    *cache.QuoteCache
	breakers map[string]*resilience.Breaker
	tracer   trace.Tracer
}

// Option configures optional behaviour of a Service. See WithTracer.
type Option func(*Service)

// WithTracer overrides the trace.Tracer used to record the "partner.quote" span. Production code
// does not need it: NewService defaults to otel.Tracer("quotation-api"), exactly like
// cache.WithMeter defaults to otel.Meter("quotation-api"). Tests use it to inject a Tracer backed by
// a local, in-memory TracerProvider instead of mutating the process-wide global one — mutating the
// global provider does not work reliably across repeated test runs in the same binary, because the
// OTel SDK's global package/delegate mechanism only rewires an already-created Tracer once per
// process (see go.opentelemetry.io/otel/internal/global/state.go, delegateTraceOnce).
func WithTracer(t trace.Tracer) Option {
	return func(s *Service) { s.tracer = t }
}

// NewService builds a Service. quoteCache may be nil, in which case cache-aside is skipped
// entirely (always a miss, no call to Redis at all) so callers that don't exercise the cache can
// keep passing nil (FDD seção 4, T08 abordagem sugerida). breakers may also be nil, or simply not
// have an entry for a given partner: a nil map read is safe in Go and quoteForPartner falls back to
// calling the Quoter directly for that partner, exactly as in T08 (FDD seção 4, T09 abordagem
// sugerida).
func NewService(
	partners []platform.Partner, quoter Quoter, quoteCache *cache.QuoteCache, breakers map[string]*resilience.Breaker,
	opts ...Option,
) *Service {
	s := &Service{partners: partners, quoter: quoter, cache: quoteCache, breakers: breakers}
	for _, opt := range opts {
		opt(s)
	}
	if s.tracer == nil {
		s.tracer = otel.Tracer(tracerName)
	}
	return s
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
//
// It also produces the T09b business telemetry for this one partner (FDD seção 7): a "partner.quote"
// span carrying the "partner.result" attribute (so a circuit-shortened call says so in the trace,
// instead of looking like a mysteriously fast live quote) and a structured log line with tenant_id,
// partner, source, breaker_state, elapsed_ms and cache_result. Neither ever carries the driver's
// document, the vehicle's plate/model/year or a quote_id.
func (s *Service) quoteForPartner(
	ctx context.Context, tenant string, p platform.Partner, forPartner partnerRequest, fingerprint string,
) (partner.Quote, error) {
	start := time.Now()
	ctx, span := s.tracer.Start(ctx, "partner.quote")
	defer span.End()
	span.SetAttributes(attribute.String("partner.name", p.Name))

	key := s.cacheKey(tenant, p.Name, fingerprint)

	if quote, ok := s.fromCache(ctx, key); ok {
		span.SetAttributes(attribute.String("partner.result", "cache_hit"))
		s.logPartnerOutcome(tenant, p.Name, quote.Source, "hit", start, nil)
		return quote, nil
	}

	quote, err := s.callQuoter(ctx, p, forPartner)
	span.SetAttributes(attribute.String("partner.result", classifyPartnerResult(err)))
	if err != nil {
		s.logPartnerOutcome(tenant, p.Name, "", "miss", start, err)
		return partner.Quote{}, err
	}
	quote.Source = "live"

	s.store(ctx, key, quote)
	s.logPartnerOutcome(tenant, p.Name, quote.Source, "miss", start, nil)
	return quote, nil
}

// classifyPartnerResult maps the outcome of a live call (through callQuoter) to the partner.result
// span attribute (T09b, FDD seção 7): cache_hit is set by the caller before this is ever reached;
// this only distinguishes what happened when the cache did not answer.
func classifyPartnerResult(err error) string {
	switch {
	case err == nil:
		return "live_success"
	case errors.Is(err, gobreaker.ErrOpenState):
		return "circuit_open"
	case errors.Is(err, gobreaker.ErrTooManyRequests):
		return "too_many_requests"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "http_error"
	}
}

// breakerStateLabel reports the current state of the breaker configured for partnerName, or "none"
// when there isn't one (a nil map read and a missing key are both safe in Go, as in callQuoter).
func (s *Service) breakerStateLabel(partnerName string) string {
	breaker := s.breakers[partnerName]
	if breaker == nil {
		return "none"
	}
	switch breaker.State() {
	case gobreaker.StateOpen:
		return "open"
	case gobreaker.StateHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

// logPartnerOutcome emits the per-partner structured log line required by T09b (FDD seção 7):
// tenant_id, partner, source, breaker_state, elapsed_ms, cache_result (plus error, when present).
// err, when set, is either a *partner.Error (partner name and HTTP status/reason only) or a
// breaker/context error: never anything derived from the driver or the vehicle.
func (s *Service) logPartnerOutcome(tenant, partnerName, source, cacheResult string, start time.Time, err error) {
	attrs := []any{
		"tenant_id", tenant,
		"partner", partnerName,
		"source", source,
		"breaker_state", s.breakerStateLabel(partnerName),
		"elapsed_ms", time.Since(start).Milliseconds(),
		"cache_result", cacheResult,
	}
	if err != nil {
		slog.Default().Warn("partner quote failed", append(attrs, "error", err.Error())...)
		return
	}
	slog.Default().Info("partner quote processed", attrs...)
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
