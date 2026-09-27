> Derivado de docs/fdd-resiliencia-parceiras.md (seção 5). Fonte de verdade é o original; em conflito, o
> original vence.

# Contratos, status HTTP e exemplos citados

## Status HTTP (seção 5, 6)

| Status | Quando | Situação |
|---|---|---|
| `400 {"error":"X-Tenant-Id is required"}` | falta o cabeçalho | inalterado |
| `400` | corpo inválido ou campo obrigatório ausente | inalterado |
| `200`, `degraded: true` | ao menos uma parceira sem prêmio, ao menos uma com | novo caminho feliz desta entrega |
| `503 {"error":"no partner quote available","tenant_id":...,"missing_partners":[...]}` | nenhuma parceira produz prêmio, nem ao vivo nem em cache | novo, substitui o 502 para este caso |
| `502` | erro inesperado fora do modelo de falha de parceira (S2) | branch remanescente, não mais o caminho comum |

## Exemplo de resposta 200 parcial

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

## Exemplo de resposta 503

```json
{
  "error": "no partner quote available",
  "tenant_id": "corretora-a",
  "missing_partners": ["partner-slow", "partner-flaky", "partner-degrading"]
}
```

## Assinaturas dos pacotes novos

```go
// internal/resilience
type Config struct {
    ConsecutiveFailures uint32
    OpenTimeout         time.Duration
    HalfOpenMaxRequests uint32
}
func NewBreaker(name string, cfg Config) *Breaker
func (b *Breaker) Execute(ctx context.Context, fn func() (partner.Quote, error)) (partner.Quote, error)
func (b *Breaker) State() gobreaker.State

// internal/quotation (novo método, resolve o ciclo de import)
func (r Request) Fingerprint() string

// internal/cache (recebe string, não quotation.Request; evita ciclo de import)
func NewQuoteCache(client *redis.Client, ttl time.Duration) *QuoteCache
func Key(tenantID, partnerName, fingerprint string) string
func (c *QuoteCache) Get(ctx context.Context, key string) (quote partner.Quote, storedAt time.Time, ok bool)
func (c *QuoteCache) Set(ctx context.Context, key string, quote partner.Quote) error

// internal/partner (assinatura alterada)
func NewClient(timeout time.Duration) *Client
```

## Métricas, logs e spans citados (seção 7)

- `partner_breaker_state{partner=...}` (proposta): gauge, `0` fechado, `1` meio aberto, `2` aberto.
- `quotation_cache_result_total{result="hit"|"miss"}` (proposta): contador; falha do Redis também soma a
  `miss`, com log de aviso separado.
- `http_server_request_duration_seconds`, `http_client_request_duration_seconds{server_address=...}`: já
  existentes, inalteradas.
- Span `partner.quote`, atributo `partner.result`: `cache_hit`, `live_success`, `circuit_open`,
  `too_many_requests`, `timeout`, `http_error`.
- Campos de log por parceira: `tenant_id`, `partner`, `source`, `breaker_state`, `elapsed_ms`,
  `cache_result`, `error`. Nunca CPF, placa, modelo ou ano do veículo.

## Dependências (seção 8)

| Componente | Versão mínima | Observação |
|---|---|---|
| `github.com/sony/gobreaker/v2` | v2.x, ainda não fixada em `go.mod` | confirmar na implementação |
| `github.com/redis/go-redis/v9` | v9.x compatível com Redis 7 | ainda não em `go.mod` |
| `github.com/alicebob/miniredis/v2` | v2.x | só em teste |
| Go | 1.25.0 | já fixado |
