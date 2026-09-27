> Derivado de docs/sad.md (seções 2-5). Fonte de verdade é o original; em conflito, o original vence.

# Contratos, chaves, payloads e configuração citados

## Chave de cache (decisão 2, seção 4)

```
quote:v1:<tenant_id>:<partner_name>:<hash>
```

`<hash>` = 16 primeiros caracteres hex do SHA-256 de:
`documento normalizado | ano de nascimento | placa normalizada | modelo | ano do veículo | valor em centavos | cobertura`
(campos normalizados por `Request.Normalize()`, `internal/quotation/request.go:32-57`).

Exemplo: `quote:v1:corretora-a:partner-flaky:3f9a2c1b8e7d4a10`.

**Valor armazenado:** `partner.Quote` (`Partner`, `QuoteID`, `PremiumCents`, `Currency`, `CoverageCents`,
`ValidForSeconds`) mais o instante em que foi gravado. Nenhum CPF, placa ou dado de `Driver`/`Vehicle`.

## Contrato de resposta (decisão 3, seção 4)

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

`degraded` é verdadeiro sempre e só quando `missing_partners` não está vazio.

## Status HTTP

| Status | Quando | Situação |
|---|---|---|
| `400 {"error":"X-Tenant-Id is required"}` | falta o cabeçalho `X-Tenant-Id` | já existe, `internal/quotation/handler.go:39-43` (RF-01) |
| `502 {"error":"partner insurer unavailable","partner":"..."}` | qualquer falha de parceira | comportamento de hoje, `internal/quotation/handler.go:72-82`; aposentado do caminho feliz nesta entrega |
| `503 {"error":"no partner quote available","tenant_id":"...","missing_partners":[...]}` | nenhuma das três parceiras produz prêmio, nem ao vivo nem em cache | novo, decisão 3, RF-06 |

## Variáveis de configuração novas (seção 5, `internal/platform/config.go`)

| Variável | Valor |
|---|---|
| `CACHE_QUOTE_TTL_SECONDS` | 900 |
| `PARTNER_TIMEOUT_MS` | 2000 |
| `BREAKER_CONSECUTIVE_FAILURES` | 5 |
| `BREAKER_OPEN_SECONDS` | 5 |
| `BREAKER_HALF_OPEN_MAX_REQUESTS` | 2 |
| `REDIS_ADDR` | `redis:6379` (nome do serviço em `docker-compose.yml:133-145`) |

Segue o padrão já existente de `parsePartners`/`parseTenants`.

## Arquivos e funções citados

| Arquivo | Estado | Papel |
|---|---|---|
| `internal/partner/client.go` | existente, estendido | `NewClient` (linhas 23-28, sem `Timeout`), `Quote()`, ganha `Timeout: 2000ms` |
| `internal/quotation/service.go` | existente, estendido | orquestra por parceira; hoje aborta tudo em `:31-34`; ganha resposta parcial |
| `internal/quotation/handler.go` | existente, estendido | valida `X-Tenant-Id` (:39-43); `respondPartnerFailure` reescrito (:72-82) |
| `internal/quotation/request.go` | existente, estendido | `Request.Normalize()` (:32-57); `Response`/`partner.Quote` ganham `Degraded`, `MissingPartners`, `source`, `age_seconds` |
| `internal/platform/config.go` | existente, estendido | cinco variáveis novas acima |
| `internal/platform/telemetry.go` | existente, estendido | span `partner.quote`, atributo `partner.result`, métricas de negócio |
| `internal/resilience/breaker.go` | **(proposto)** | wrapper de `gobreaker/v2`, um por `platform.Partner` |
| `internal/cache/quote_cache.go` | **(proposto)** | `QuoteCache`, chave/TTL acima |
| `cmd/quotation-api/main.go` | existente, estendido | instancia os três breakers |
| `docker-compose.yml` | existente | Redis já sobe (:133-145), sem `--save`/`--appendonly` (:136-138); perfis de `partner-mock` (:164-170 flaky, :177-191 degrading) |
| `cmd/partner-mock/behavior.go` | existente | prêmio determinístico por hash da requisição (:84-98) |
| `cmd/partner-mock/feasibility_test.go` | existente | trava a política "5 falhas consecutivas / 2 sucessos para fechar" |
| `cmd/partner-mock/config.go` | existente | `PARTNER_QUOTE_TTL_SECONDS` (:62-66), TTL técnico do mock, não o TTL de negócio do cache |
| `cmd/loadgen/config.go` | existente | `defaultDistinct = 5` |

## Bibliotecas propostas

- `github.com/sony/gobreaker/v2` — circuit breaker.
- `github.com/redis/go-redis/v9` — cliente Redis.
- `github.com/alicebob/miniredis/v2` — Redis em memória para teste.

## Métricas e spans citados

- `partner_breaker_state{partner=...}` **(proposta)** — estado do breaker.
- `quotation_cache_result_total{result="hit"|"miss"}` **(proposta)** — resultado do cache.
- `http_server_request_duration_seconds` — latência de `POST /quotes`.
- `http_client_request_duration_seconds{server_address=...}` — latência por parceira.
- Span de negócio `partner.quote`, atributo `partner.result` (valores incluem `circuit_open`).
