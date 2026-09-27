> Derivado de docs/fdd-resiliencia-parceiras.md (seções 3-6, 9-10). Fonte de verdade é o original; em
> conflito, o original vence.

# Decisões do FDD: resiliência de parceiras

## Suposições de design (seção 1)

- S1: laço de consulta às três parceiras continua sequencial, não paralelo. Nenhuma mudança de
  concorrência é pedida por esta entrega.
- S2: `service.Quote` deixa de retornar `error` por falha de parceira individual; falha de parceira vira
  entrada em `MissingPartners`. Um `error` só volta em condição verdadeiramente inesperada (ex.: contexto
  cancelado antes de qualquer chamada).

## Assinaturas novas ou alteradas (seção 5)

- `internal/resilience.NewBreaker(name string, cfg Config) *Breaker`, `(*Breaker).Execute(ctx, fn) (partner.Quote, error)`,
  `(*Breaker).State() gobreaker.State`.
- `internal/quotation.Request.Fingerprint() string` (novo): concatenação por `|` dos campos já
  normalizados por `Normalize()`, sem hash. Existe para resolver o ciclo de import descrito abaixo.
- `internal/cache.NewQuoteCache(client *redis.Client, ttl time.Duration) *QuoteCache`,
  `cache.Key(tenantID, partnerName, fingerprint string) string` (recebe `string`, não
  `quotation.Request`), `(*QuoteCache).Get(ctx, key) (quote partner.Quote, storedAt time.Time, ok bool)`,
  `(*QuoteCache).Set(ctx, key, quote) error`.
- `internal/partner.NewClient(timeout time.Duration) *Client` (hoje `NewClient()`, sem parâmetro):
  mudança de assinatura, não aditiva, quebra todo chamador direto (só `cmd/quotation-api/main.go` hoje).

## Ciclo de import evitado (seção 1, 5)

- `cache.Key` originalmente receberia `quotation.Request`, o que faria `internal/cache` importar
  `internal/quotation`, criando um ciclo (já que `internal/quotation` precisa importar `internal/cache`
  para orquestrar o cache-aside).
- Resolução: `quotation.Request` ganha `Fingerprint() string` (fica em `internal/quotation`, sem
  dependência nova); `cache.Key` passa a receber `(tenantID, partnerName, fingerprint string)`, três
  strings, e calcula o SHA-256 internamente. `internal/cache` não importa `internal/quotation` em nenhum
  momento.

## Serialização de `source`/`age_seconds` (seção 5)

- `degraded`: sempre serializado (`true`/`false`, sem `omitempty`).
- `missing_partners`: `omitempty` (omitido quando as três parceiras respondem).
- `age_seconds`: ponteiro (`*int64`) com `omitempty`; omitido quando `source == "live"`; presente
  (inclusive quando igual a zero) quando `source == "cache"`.
- Motivo do ponteiro: um `int64` simples com `omitempty` omitiria incorretamente uma idade de cache igual
  a zero segundos.

## Destino do `502` existente (seção 4, 6)

- O `502 {"error":"partner insurer unavailable",...}` deixa de ser produzido para "uma parceira falhou,
  as outras responderam" (isso agora é `200` com `degraded: true`).
- `502` permanece só como branch remanescente de `respondPartnerFailure` para erro verdadeiramente
  inesperado fora do modelo de falha de parceira (S2), não removido do código.
- Falha total das três parceiras passa a ser `503`, não `502`.

## Estratégia de resiliência (seção 6)

- Sem retry automático dentro da mesma requisição: uma falha de parceira não tenta a mesma parceira de
  novo na mesma requisição.
- Cache-aside não conta como retry nem como degradação.

## Riscos e mitigação (seção 10, uma linha cada)

| Risco | Probabilidade | Mitigação principal | Contingência |
|---|---|---|---|
| Timeout de 2000 ms confunde parceira lenta com fora do ar | média | `PARTNER_TIMEOUT_MS` configurável; `partner.result` separa `timeout` de `http_error` | aumentar `PARTNER_TIMEOUT_MS` sem deploy |
| Breaker em memória do processo (limite 2, não resolvido) | alta com múltiplas réplicas | nenhuma nesta entrega, documentado | réplica única até fatia futura |
| TTL como única invalidação (limite 3, não resolvido) | baixa dentro de 15 min | TTL conservador (900s « 24h) | reduzir `CACHE_QUOTE_TTL_SECONDS` em incidente |
| Falha silenciosa do Redis mascara problema maior | baixa | métrica de miss + log distinto de miss por falha de conexão | painel dedicado de disponibilidade do Redis |
| Mudança de contrato quebra consumidor com parsing estrito | média | campos só aditivos; `degraded` sempre presente | aviso formal às corretoras antes do rollout |
| Script fixo do teste do breaker diverge do `partner-flaky` real | baixa | rajada real coberta separadamente por `feasibility_test.go` e `make reproduce`, não pelo teste do breaker | atualizar o script fixo no mesmo commit que mudar o mock |
