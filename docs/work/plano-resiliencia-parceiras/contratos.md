> Derivado de docs/plano-resiliencia-parceiras.md (tasks detalhadas). Fonte de verdade é o original; em
> conflito, o original vence.

# Tasks: arquivo, teste, comando de verificação

| Task | Fase | Arquivo(s) principal(is) | Teste que prova | Comando de verificação |
|---|---|---|---|---|
| T01 | 1 | `internal/platform/config.go` | `TestConfigReadsTheResilienceVariables`, `TestInvalidConfigFails` (6 casos novos) | `go test ./internal/platform/... -run 'Config' -v && make test` |
| T02 | 1 | `go.mod`, `go.sum` | nenhum (setup) | `go build ./... && go mod verify && make test` |
| T03 | 1 | `internal/quotation/request.go` | `TestFingerprintIsStableForTheSameNormalizedRequest`, `TestFingerprintChangesWhenAnyFieldChanges` | `go test ./internal/quotation/... -run Fingerprint -v && make test` |
| T04 | 1 | `internal/partner/client.go`, `cmd/quotation-api/main.go` | `TestQuoteFailsWhenThePartnerIsSlowerThanTheTimeout` | `go test ./internal/partner/... -v && go build ./... && make test` |
| T05 | 2 | `internal/resilience/breaker.go` (novo) | `TestBreakerOpensAfterFiveConsecutiveFailures` (teste determinístico exigido, breaker) | `go test ./internal/resilience/... -v && make test` |
| T06 | 2 | `internal/cache/quote_cache.go` (novo) | `TestKeyFormatIsStableAndVersioned`, `TestSetThenGetRoundTrips`, `TestGetMissesWhenTheKeyDoesNotExistOrExpired`, `TestGetIsBestEffortWhenRedisIsUnreachable` | `go test ./internal/cache/... -v && make test` |
| T07a | 3 | `internal/partner/client.go`, `internal/quotation/request.go`, `internal/quotation/service.go` | `TestQuoteAggregatesTheThreePartnersSortedByPremium` (ganha asserções novas) | `go test ./internal/quotation/... -run Aggregat -v && make test` |
| T07b | 3 | `internal/quotation/service.go`, `internal/quotation/handler.go` | reescreve os 2 testes invalidados + `TestQuotesResponds503WhenNoPartnerRespond` | `go test ./internal/quotation/... -v && make test` |
| T08 | 4 | `internal/quotation/service.go` | `TestQuoteUsesTheCacheEntryWithoutCallingThePartner`, `TestPartialResponseWithCacheAndFallback` (teste determinístico exigido, cache+fallback com `miniredis`) | `go test ./internal/quotation/... -v && make test` |
| T09 | 4 | `internal/quotation/service.go` | `TestQuoteSkipsThePartnerWhenItsBreakerIsOpen` | `go test ./internal/quotation/... -v && make test` |
| T09b | 4 | `internal/platform/telemetry.go`, `internal/resilience/breaker.go`, `internal/cache/quote_cache.go`, `internal/quotation/service.go` | `TestBreakerStateGaugeFollowsTransitions`, `TestCacheResultCounterIncrements`, `TestPartnerQuoteLogHasNoPersonalData` | `go test ./internal/... -v && make test` |
| T10 | 5 | `cmd/quotation-api/main.go` | nenhum automatizado; smoke manual | `go build ./... && go vet ./... && make test` + `curl` manual |
| T11 | 6 | `docs/evidencias/depois/*` (7 arquivos, um por evidência da tabela do enunciado) | evidência, não teste de código | `make down && make reproduce \| tee docs/evidencias/depois/reproduce.txt` |
| T12 | 7 | `README.md` (raiz do repositório, novo) | checklist de conteúdo + `grep` cruzado das métricas | `grep -rn "partner_breaker_state\|quotation_cache_result_total\|partner.quote\|partner.result" internal/` |

## Contrato de resposta, por estágio

**Depois de T07a** (só shape, sem fallback ainda):
```json
{"tenant_id":"corretora-a","quotes":[{"partner":"partner-slow","...":"...","source":"live"}],
 "elapsed_ms":1200,"degraded":false}
```

**Depois de T07b** (fallback parcial e falha total):
```json
{"tenant_id":"corretora-a","quotes":[{"partner":"partner-slow","...":"...","source":"live"}],
 "elapsed_ms":1873,"degraded":true,"missing_partners":["partner-flaky"]}
```
```json
{"error":"no partner quote available","tenant_id":"corretora-a",
 "missing_partners":["partner-slow","partner-flaky","partner-degrading"]}
```

**Depois de T08** (shape final, com `source: "cache"`):
```json
{
  "tenant_id": "corretora-a",
  "quotes": [
    {"partner": "partner-slow", "quote_id": "...", "premium_cents": 91234, "currency": "BRL",
     "coverage_cents": 5000000, "valid_for_seconds": 300, "source": "live"},
    {"partner": "partner-flaky", "quote_id": "...", "premium_cents": 84210, "currency": "BRL",
     "coverage_cents": 5000000, "valid_for_seconds": 300, "source": "cache", "age_seconds": 47}
  ],
  "elapsed_ms": 1873,
  "degraded": true,
  "missing_partners": ["partner-degrading"]
}
```

## Assinaturas por task

```go
// T04
func NewClient(timeout time.Duration) *Client

// T05
func NewBreaker(name string, cfg Config) *Breaker
func (b *Breaker) Execute(ctx context.Context, fn func() (partner.Quote, error)) (partner.Quote, error)
func (b *Breaker) State() gobreaker.State

// T06
func NewQuoteCache(client *redis.Client, ttl time.Duration) *QuoteCache
func Key(tenantID, partnerName, fingerprint string) string
func (c *QuoteCache) Get(ctx context.Context, key string) (quote partner.Quote, storedAt time.Time, ok bool)
func (c *QuoteCache) Set(ctx context.Context, key string, quote partner.Quote) error

// T08 (NewService, primeira evolução)
func NewService(partners []platform.Partner, quoter Quoter, cache *cache.QuoteCache) *Service

// T09 (NewService, segunda evolução)
func NewService(partners []platform.Partner, quoter Quoter, cache *cache.QuoteCache,
    breakers map[string]*resilience.Breaker) *Service

// T09b (extensões aditivas, sem quebrar T05/T06)
type Config struct { // internal/resilience, campo novo
    ConsecutiveFailures uint32
    OpenTimeout         time.Duration
    HalfOpenMaxRequests uint32
    OnStateChange       func(name string, from, to gobreaker.State) // novo, opcional
}
func NewQuoteCache(client *redis.Client, ttl time.Duration, opts ...Option) *QuoteCache // opts novo, opcional
func WithMeter(m metric.Meter) Option // internal/cache, novo
```

## Evidências (T11), tabela completa

| # | Arquivo | Comando/consulta | Janela |
|---|---|---|---|
| 1 | `reproduce.txt` | `make down && make reproduce \| tee docs/evidencias/depois/reproduce.txt` | execução inteira |
| 2 | `make-test.txt` | `make test \| tee docs/evidencias/depois/make-test.txt` | não se aplica |
| 3 | `jaeger-trace-breaker-aberto.json` | Jaeger, tags `partner.result=circuit_open` | Last Hour |
| 4 | `jaeger-trace-cache.json` | Jaeger, tags `partner.result=cache_hit` | Last Hour |
| 5 | `prometheus-breaker-state.png` | `partner_breaker_state{partner="partner-flaky"}` | Last Hour |
| 6 | `prometheus-cache-hit-rate.png` | `sum(rate(quotation_cache_result_total{result="hit"}[5m])) / sum(rate(quotation_cache_result_total{result=~"hit\|miss"}[5m]))` | Last Hour, `[5m]` |
| 7 | `prometheus-p95-antes.png` + `prometheus-p95-depois.png` | `histogram_quantile(0.95, sum by (le) (rate(http_server_request_duration_seconds_bucket[5m])))` | Last Hour, `[5m]`, uma captura por execução |

## README do processo (T12), as cinco coisas + chave por extenso

1. Link para `docs/sad.md` e `docs/evidencias/`.
2. Como subir e reproduzir: `make up`, `make ps`, `make down && make reproduce`.
3. O que foi implementado (breaker, cache, fallback) vs. proposta (os 4 limites conhecidos + paralelização).
4. Métricas/atributos: `partner_breaker_state{partner}`, `quotation_cache_result_total{result=...}`, span `partner.quote`/`partner.result`.
5. O que faria diferente: os 4 limites conhecidos do SAD.

Chave por extenso: `quote:v1:<tenant_id>:<partner_name>:<hash>`, `tenant_id` obrigatório sem exceção.
