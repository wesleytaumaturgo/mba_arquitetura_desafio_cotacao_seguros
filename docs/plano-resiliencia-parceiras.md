## Plano de Implementação: Resiliência de parceiras (circuit breaker, cache e fallback parcial)

**Fonte**: FDD v1.0 (`docs/fdd-resiliencia-parceiras.md`, seções 4, 5 e 9)
**Data**: 2026-09-27
**Estimativa total**: ~14,5 dias-dev
**Caminho crítico**: T07a → T07b → T08 → T09 → T09b → T10 → T11 → T12 (a cadeia do breaker, T01 → T04 →
T05 → T09, converge no mesmo ponto sem alongar o caminho)

**Pré-requisito, antes de tocar em qualquer código (Fase 1):** confirmar que `docs/evidencias/antes/`
já contém a captura de tela do p95 do Prometheus da execução "antes" (evidência 7 da tabela do
enunciado, seção "As evidências"). Hoje essa pasta só tem os três arquivos de texto
(`reproduce.txt`, `loadgen-antes.txt`, `smoke-factibilidade.txt`); se o gráfico não foi capturado,
**capturá-lo agora**, com `make down && make reproduce` de pé, é a última janela possível: depois da
Fase 1 o código já não reflete mais o "antes", e o enunciado proíbe copiar o número de outra execução
("Reprova: 'antes' copiado do roteiro ou de outro aluno").

---

### Visão geral das fases

| Fase | Descrição | Tasks | Estimativa |
|---|---|---|---|
| 1 | Fundação (config, dependências, timeout do cliente) | T01, T02, T03, T04 | 1,75 dia-dev |
| 2 | Componentes de resiliência isolados (breaker, cache) | T05, T06 | 3 dias-dev |
| 3 | Contrato de resposta e fallback parcial | T07a, T07b | 2 dias-dev |
| 4 | Integração no `Service` e telemetria de negócio | T08, T09, T09b | 4,5 dias-dev |
| 5 | Wiring final (`cmd/quotation-api/main.go`) | T10 | 1,25 dia-dev |
| 6 | Evidências do depois | T11 | 1,5 dia-dev |
| 7 | Fechamento (README do processo) | T12 | 0,5 dia-dev |

---

### Tasks detalhadas

#### Fase 1: Fundação

##### T01: Seis variáveis novas em `internal/platform/config.go`

- **Complexidade**: P
- **Estimativa**: 0,5 dia-dev
- **Depende de**: nenhuma
- **Desbloqueia**: T04, T10
- **Paralelizável com**: T02, T03
- **Arquivo(s)**: `internal/platform/config.go`, `internal/platform/config_test.go`
- **Teste que prova**: `TestConfigReadsTheResilienceVariables` (valores válidos, default e customizado) e
  `TestInvalidConfigFails` ganha casos para cada uma das seis variáveis com valor não numérico/inválido
- **Comando de verificação**: `go test ./internal/platform/... -run 'Config' -v && make test`

**Descrição técnica**
Adiciona ao `Config` os campos `CacheQuoteTTL time.Duration`, `PartnerTimeout time.Duration`,
`Breaker.ConsecutiveFailures uint32`, `Breaker.OpenTimeout time.Duration`,
`Breaker.HalfOpenMaxRequests uint32` e `RedisAddr string`, lidos de `CACHE_QUOTE_TTL_SECONDS`,
`PARTNER_TIMEOUT_MS`, `BREAKER_CONSECUTIVE_FAILURES`, `BREAKER_OPEN_SECONDS`,
`BREAKER_HALF_OPEN_MAX_REQUESTS` e `REDIS_ADDR` (FDD seção 5, tabela "Como se constrói").

**Abordagem sugerida**
Segue o padrão de `parsePartners`/`parseTenants`: uma função `parseResilience(env) (…, error)` que usa
`strconv.Atoi`/`strconv.ParseUint` e retorna erro no mesmo formato (`"CHAVE: %q não é ..."`). Defaults:
900, 2000, 5, 5, 2, `redis:6379`. `REDIS_ADDR` não precisa ser uma URL absoluta (é `host:porta` do
protocolo Redis, não HTTP); validar só que não é vazio.

**Critérios de aceite**
- [ ] `LoadConfig` sem nenhuma das seis variáveis produz exatamente os defaults acima.
- [ ] Cada uma das seis variáveis aceita um valor customizado e o valor aparece em `Config`.
- [ ] Valor não numérico em qualquer variável numérica falha `LoadConfig` com erro nomeando a variável.
- [ ] `REDIS_ADDR` vazio quando explicitamente setado como string vazia é tratado como "não setado" (usa
  o default), igual ao padrão de `text()` já existente.

**Observações**
Nenhuma dependência de `gobreaker`/`go-redis` aqui: são só `string`/`time.Duration`/`uint32` em
`platform.Config`, os tipos concretos das bibliotecas só aparecem em T05/T06/T10.

---

##### T02: Dependências novas em `go.mod`

- **Complexidade**: P
- **Estimativa**: 0,25 dia-dev
- **Depende de**: nenhuma
- **Desbloqueia**: T05, T06
- **Paralelizável com**: T01, T03, T04
- **Arquivo(s)**: `go.mod`, `go.sum`
- **Teste que prova**: nenhum teste de comportamento (task de setup); a prova é o build
- **Comando de verificação**: `go build ./... && go mod verify && make test`

**Descrição técnica**
Adiciona `github.com/sony/gobreaker/v2`, `github.com/redis/go-redis/v9` e
`github.com/alicebob/miniredis/v2` (esta última como dependência de teste) ao módulo (FDD seção 8).

**Abordagem sugerida**
`go get github.com/sony/gobreaker/v2@latest github.com/redis/go-redis/v9@latest
github.com/alicebob/miniredis/v2@latest`, seguido de `go mod tidy`. Registrar no plano a versão exata
resolvida (o FDD marca isso como hipótese em aberto).

**Critérios de aceite**
- [ ] `go build ./...` continua verde sem nenhum outro arquivo alterado.
- [ ] As três bibliotecas aparecem em `go.mod` com versão fixada (não `latest`).

**Observações**
Task de infraestrutura pura, sem lógica de negócio; existe só para que T05 e T06 não precisem tocar em
`go.mod` no meio de uma mudança de comportamento.

---

##### T03: `Request.Fingerprint()` em `internal/quotation/request.go`

- **Complexidade**: P
- **Estimativa**: 0,5 dia-dev
- **Depende de**: nenhuma
- **Desbloqueia**: T08
- **Paralelizável com**: T01, T02, T04
- **Arquivo(s)**: `internal/quotation/request.go`, `internal/quotation/request_test.go`
- **Teste que prova**: `TestFingerprintIsStableForTheSameNormalizedRequest`,
  `TestFingerprintChangesWhenAnyFieldChanges`
- **Comando de verificação**: `go test ./internal/quotation/... -run Fingerprint -v && make test`

**Descrição técnica**
Implementa `func (r Request) Fingerprint() string`, que concatena por `|` os campos já normalizados por
`Normalize()` (documento, ano de nascimento, placa, modelo, ano do veículo, valor em centavos, cobertura),
sem calcular hash algum (FDD seção 5, decisão registrada em
`docs/work/fdd-resiliencia-parceiras/decisoes.md`, "Ciclo de import evitado").

**Abordagem sugerida**
`fmt.Sprintf("%s|%d|%s|%s|%d|%d|%s", r.Driver.Document, r.Driver.BirthYear, r.Vehicle.Plate,
r.Vehicle.Model, r.Vehicle.Year, r.Vehicle.ValueCents, r.Coverage)`, chamado sempre depois de `Normalize()`
já ter rodado (pré-condição, não validada de novo aqui).

**Critérios de aceite**
- [ ] Duas instâncias de `Request` com os mesmos dados normalizados produzem o mesmo `Fingerprint()`.
- [ ] Mudar qualquer um dos sete campos muda o `Fingerprint()`.
- [ ] `internal/quotation` continua sem importar `internal/cache` neste ponto (isso só acontece em T08).

**Observações**
`internal/cache` **nunca** importa `internal/quotation`: é essa task que existe para permitir isso. Não
tem relação de compilação com T06 (o pacote `cache` só recebe `string`), mas antecede T08, onde as duas
pontas se conectam.

---

##### T04: Timeout configurável em `internal/partner/client.go`

- **Complexidade**: P/M
- **Estimativa**: 0,5 dia-dev
- **Depende de**: T01 (usa `cfg.PartnerTimeout` no wiring de `main.go`)
- **Desbloqueia**: T05, T07a
- **Paralelizável com**: T02, T03
- **Arquivo(s)**: `internal/partner/client.go`, `internal/partner/client_test.go`,
  `cmd/quotation-api/main.go`
- **Teste que prova**: `TestQuoteFailsWhenThePartnerIsSlowerThanTheTimeout` (novo)
- **Comando de verificação**: `go test ./internal/partner/... -v && go build ./... && make test`

**Descrição técnica**
`NewClient` passa a receber `timeout time.Duration` e aplicá-lo a `http.Client.Timeout` (FDD seção 5,
"`internal/partner.Client` (estendido)"). É a mesma linha do "Como se testa" do SAD: "servidor HTTP de
teste que atrasa a resposta além do timeout configurado".

**Abordagem sugerida**
`func NewClient(timeout time.Duration) *Client { return &Client{http: &http.Client{Timeout: timeout,
Transport: platform.InstrumentTransport(http.DefaultTransport)}} }`. Atualizar `cmd/quotation-api/main.go`
para `partner.NewClient(cfg.PartnerTimeout)`. Atualizar as quatro chamadas `NewClient()` sem argumento em
`client_test.go` para um timeout de teste generoso (ex.: `time.Second`), exceto no novo teste, que usa um
timeout curto (ex.: `20 * time.Millisecond`) contra um `httptest.Server` que atrasa a resposta além disso.

**Critérios de aceite**
- [ ] `NewClient(timeout)` aplica o timeout ao `http.Client`.
- [ ] Uma resposta do parceiro mais lenta que o timeout configurado retorna erro (não trava, não
  atinge o timeout padrão do Go de "sem timeout").
- [ ] Os quatro testes existentes de `client_test.go` continuam verdes com o novo parâmetro.
- [ ] `cmd/quotation-api/main.go` compila usando `cfg.PartnerTimeout`.

**Observações**
Mudança de assinatura não aditiva (SAD, decisão 1, e `docs/work/fdd-resiliencia-parceiras/decisoes.md`).
Não há outro chamador de `partner.NewClient` fora de `main.go` e dos testes do próprio pacote.

---

#### Fase 2: Componentes de resiliência isolados

##### T05: `internal/resilience/breaker.go` (novo) + teste de abertura do breaker

- **Complexidade**: M/G
- **Estimativa**: 1,5 dia-dev
- **Depende de**: T02, T04
- **Desbloqueia**: T09
- **Paralelizável com**: T06
- **Arquivo(s)**: `internal/resilience/breaker.go` (novo), `internal/resilience/breaker_test.go` (novo)
- **Teste que prova**: `TestBreakerOpensAfterFiveConsecutiveFailures` (o teste determinístico do breaker
  exigido pelo enunciado, FDD seção 9)
- **Comando de verificação**: `go test ./internal/resilience/... -v && make test`

**Descrição técnica**
Implementa o wrapper de `gobreaker/v2` descrito na FDD, seção 5:
```go
type Config struct {
    ConsecutiveFailures uint32
    OpenTimeout         time.Duration
    HalfOpenMaxRequests uint32
}
func NewBreaker(name string, cfg Config) *Breaker
func (b *Breaker) Execute(ctx context.Context, fn func() (partner.Quote, error)) (partner.Quote, error)
func (b *Breaker) State() gobreaker.State
```

**Abordagem sugerida**
`gobreaker.NewCircuitBreaker[partner.Quote]` com `ReadyToTrip` checando
`counts.ConsecutiveFailures >= cfg.ConsecutiveFailures`, `Timeout: cfg.OpenTimeout`,
`MaxRequests: cfg.HalfOpenMaxRequests`. `Execute` delega para `cb.Execute(fn)`.

**Teste do breaker (script fixo, sem `cmd/partner-mock`)**
Um `httptest.Server` cujo handler consulta um contador atômico de requisições recebidas e responde de
acordo com uma tabela fixa codificada no teste (ex.: as 9 primeiras chamadas falham com `503`, espelhando
o formato da rajada real de 9 falhas consecutivas de `partner-flaky`, sequências 49-57, sem importar
`cmd/partner-mock`, conforme FDD seção 9 e o risco ajustado na seção 10). O teste usa
`partner.NewClient(timeout curto)` para chamar o servidor através de `Breaker.Execute`. Depois da 5ª
falha consecutiva, uma 6ª chamada ao breaker aberto **não deve** incrementar o contador do servidor de
teste.

**Critérios de aceite**
- [ ] Depois de 5 falhas consecutivas, `Execute` retorna erro sem que o `httptest.Server` receba a 6ª
  requisição (contador do servidor de teste não avança).
- [ ] `State()` reporta `StateOpen` imediatamente após a 5ª falha.
- [ ] Depois do `OpenTimeout` configurado no teste (curto, ex.: `50ms`), a próxima chamada é permitida
  (meio aberto) e, com sucesso, uma segunda chamada de sucesso fecha o circuito (`HalfOpenMaxRequests: 2`).
- [ ] Uma falha em meio aberto reabre o circuito imediatamente.

**Observações**
[PENDENTE: a assinatura exata de `gobreaker/v2` (nomes de campos de `gobreaker.Settings`, forma genérica
de `Execute`) depende da versão resolvida em T02; ajustar o wrapper se a API diferir do que o FDD assume.]
Risco de implementação: ver tabela de riscos, item sobre a versão da biblioteca ainda não fixada.

---

##### T06: `internal/cache/quote_cache.go` (novo) + testes com `miniredis`

- **Complexidade**: M
- **Estimativa**: 1,5 dia-dev
- **Depende de**: T02
- **Desbloqueia**: T08
- **Paralelizável com**: T05
- **Arquivo(s)**: `internal/cache/quote_cache.go` (novo), `internal/cache/quote_cache_test.go` (novo)
- **Teste que prova**: `TestKeyFormatIsStableAndVersioned`, `TestSetThenGetRoundTrips`,
  `TestGetMissesWhenTheKeyDoesNotExistOrExpired`, `TestGetIsBestEffortWhenRedisIsUnreachable`
- **Comando de verificação**: `go test ./internal/cache/... -v && make test`

**Descrição técnica**
Implementa `QuoteCache` sobre `go-redis/v9`, testado com `miniredis/v2` (FDD seção 5):
```go
func NewQuoteCache(client *redis.Client, ttl time.Duration) *QuoteCache
func Key(tenantID, partnerName, fingerprint string) string
func (c *QuoteCache) Get(ctx context.Context, key string) (quote partner.Quote, storedAt time.Time, ok bool)
func (c *QuoteCache) Set(ctx context.Context, key string, quote partner.Quote) error
```

**Abordagem sugerida**
`Key` calcula SHA-256 de `fingerprint`, usa os 16 primeiros caracteres hex, e monta
`fmt.Sprintf("quote:v1:%s:%s:%s", tenantID, partnerName, hash)`. `Set` serializa `{quote, stored_at}` em
JSON e grava com `client.Set(ctx, key, payload, ttl)`. `Get` lê, desserializa e retorna `ok=false` em
qualquer erro (chave ausente, JSON inválido, erro de rede), sem propagar o erro ao chamador (só loga).
Para o teste de expiração, usar um TTL curto passado ao `NewQuoteCache` do teste (ex.: `50ms`) e
`time.Sleep` só o suficiente para passar do TTL, nunca dependendo do TTL de produção (900s).

**Critérios de aceite**
- [ ] `Key("corretora-a", "partner-flaky", fingerprint)` tem o formato
  `quote:v1:corretora-a:partner-flaky:<16 hex chars>`, estável para o mesmo `fingerprint`.
- [ ] `Set` seguido de `Get` com a mesma chave retorna a quote idêntica e `storedAt` dentro de uma
  tolerância pequena (ex.: 1s) do instante do `Set`.
- [ ] `Get` numa chave nunca escrita retorna `ok == false`, sem erro visível ao chamador.
- [ ] `Get` numa chave escrita com um TTL de teste curto, depois de expirado, retorna `ok == false`.
- [ ] `Get`/`Set` contra um `miniredis` fechado (`server.Close()` antes da chamada) retornam
  `ok == false`/erro tratável, sem panic e sem propagar o erro de rede como se fosse do chamador.
- [ ] Nenhum campo de `Driver`/`Vehicle` (CPF, placa) aparece no payload serializado gravado no Redis
  (só os campos de `partner.Quote` mais o timestamp).

**Observações**
`internal/cache` não importa `internal/quotation` (ver T03). `Get`/`Set` recebem a `key` já pronta; quem
chama `Key()` é `internal/quotation/service.go` (T08), não o próprio pacote `cache`.

---

#### Fase 3: Contrato de resposta e fallback parcial

##### T07a: Campos novos do contrato (`Degraded`, `MissingPartners`, `Source`, `AgeSeconds`)

- **Complexidade**: P
- **Estimativa**: 0,5 dia-dev
- **Depende de**: T04 (mesmo arquivo `client.go`)
- **Desbloqueia**: T07b
- **Paralelizável com**: nenhuma (é pré-requisito direto de T07b)
- **Arquivo(s)**: `internal/partner/client.go`, `internal/quotation/request.go`,
  `internal/quotation/service.go`, `internal/quotation/service_test.go`
- **Teste que prova**: `TestQuoteAggregatesTheThreePartnersSortedByPremium` ganha asserções dos campos
  novos
- **Comando de verificação**: `go test ./internal/quotation/... -run Aggregat -v && make test`

**Descrição técnica**
Adiciona `Source string` e `AgeSeconds *int64` (com `json:"age_seconds,omitempty"`) a `partner.Quote`, e
`Degraded bool` (sem `omitempty`) e `MissingPartners []string` (com `omitempty`) a `quotation.Response`
(FDD seção 5). Ainda **não** muda o comportamento de erro: o laço em `service.Quote` continua abortando
no primeiro erro (isso é T07b); esta task só garante que todo sucesso ao vivo grava `Source: "live"` e que
`Degraded`/`MissingPartners` existem no tipo, ambos zerados no caminho feliz.

**Contrato de API** *(campos novos do response, ainda sem o caminho de erro)*

| | |
|---|---|
| **Método/Rota** | `POST /quotes` (inalterada) |
| **Response `200`** | `{ tenant_id, quotes: [{ partner, quote_id, premium_cents, currency, coverage_cents, valid_for_seconds, source: "live" }], elapsed_ms, degraded: false }` |
| **Serialização** | `degraded` sempre presente; `missing_partners` omitido quando vazio; `age_seconds` (ponteiro) omitido quando `source == "live"` |

**Critérios de aceite**
- [ ] Toda quote bem-sucedida ao vivo tem `source: "live"` no JSON serializado.
- [ ] `age_seconds` não aparece no JSON de uma quote com `source: "live"` (omitido, não `null`).
- [ ] Uma resposta com as três parceiras bem-sucedidas tem `"degraded":false` e nenhum campo
  `missing_partners` no JSON serializado.
- [ ] O struct `partner.Quote` continua satisfazendo o mesmo contrato JSON de hoje para os campos
  existentes (`partner`, `quote_id`, `premium_cents`, `currency`, `coverage_cents`, `valid_for_seconds`).

**Observações**
Esta task é deliberadamente pequena e não muda nenhum teste que hoje espera erro; isso fica isolado em
T07b para que uma regressão de comportamento não se misture com uma mudança de shape.

---

##### T07b: Fallback como resposta parcial, `503` na falha total

- **Complexidade**: M/G
- **Estimativa**: 1,5 dia-dev
- **Depende de**: T07a
- **Desbloqueia**: T08
- **Paralelizável com**: nenhuma
- **Arquivo(s)**: `internal/quotation/service.go`, `internal/quotation/handler.go`,
  `internal/quotation/service_test.go`, `internal/quotation/handler_test.go`
- **Teste que prova**: reescreve `TestOnePartnerDownBringsDownTheWholeRequest` e
  `TestQuotesResponds502WithTheNameOfThePartnerThatWentDown`; adiciona
  `TestQuotesResponds503WhenNoPartnerRespond`
- **Comando de verificação**: `go test ./internal/quotation/... -v && make test`

**Descrição técnica**
`service.Quote` para de retornar `(Response{}, err)` no primeiro erro de parceira: acumula o nome da
parceira em `MissingPartners` e continua o laço (FDD seção 4, fluxo principal, passo 2.5, e seção 1,
suposição S2). `service.Quote` passa a retornar sempre `(Response, nil)` neste caminho.
`handler.quote` responde `503 {"error":"no partner quote available","tenant_id":...,
"missing_partners":[...]}` quando `len(response.Quotes) == 0`, e `200` com `Degraded`/`MissingPartners`
preenchidos caso contrário. `respondPartnerFailure` deixa de ser alcançado no caminho comum de falha de
parceira (fica como branch remanescente para erro inesperado, FDD seção 6).

**Contrato de API**

| | |
|---|---|
| **Método/Rota** | `POST /quotes` |
| **Response `200`, parcial** | `{"tenant_id":"corretora-a","quotes":[{"partner":"partner-slow",...,"source":"live"}],"elapsed_ms":1873,"degraded":true,"missing_partners":["partner-flaky"]}` |
| **Response `503`** | `{"error":"no partner quote available","tenant_id":"corretora-a","missing_partners":["partner-slow","partner-flaky","partner-degrading"]}` — mensagem literal do FDD seção 5 |
| **Response `400`** | inalterada: `{"error":"X-Tenant-Id is required"}` |

**Abordagem sugerida**
`fakeQuoter.failOn string` (usado hoje) precisa virar algo como `failOn map[string]bool` para simular
mais de uma parceira falhando ao mesmo tempo (necessário para o teste de falha total). Manter
`failOn` como caso especial de conveniência (uma única parceira) é opcional; o importante é o teste de
503 conseguir fazer as três falharem.

**Critérios de aceite**
- [ ] Uma parceira falhando: resposta `200`, `degraded: true`, `missing_partners: ["<parceira>"]`, as
  outras duas quotes presentes e ordenadas por `premium_cents`.
- [ ] As três parceiras falhando: resposta `503`, corpo exatamente
  `{"error":"no partner quote available","tenant_id":"<tenant>","missing_partners":[...]}` com as três
  parceiras listadas.
- [ ] `TestOnePartnerDownBringsDownTheWholeRequest` (nome antigo, comportamento invertido) deixa de
  existir como estava; o teste equivalente agora afirma resposta parcial, não erro.
- [ ] `TestQuotesResponds502WithTheNameOfThePartnerThatWentDown` deixa de existir como estava; nenhum
  teste espera `502` para uma única parceira fora.
- [ ] Nenhum teste remanescente espera que `service.Quote` retorne `error` por falha de parceira comum.

**Observações**
Esta é a task que mais risco de regressão carrega: dois testes que hoje passam vão falhar assim que o
código mudar, e **precisam** ser reescritos na mesma task, não depois, para que `make test` não fique
vermelho entre commits.

---

#### Fase 4: Integração no `Service`

##### T08: Cache-aside em `service.Quote`

- **Complexidade**: G
- **Estimativa**: 2 dias-dev
- **Depende de**: T03, T06, T07b
- **Desbloqueia**: T09
- **Paralelizável com**: nenhuma
- **Arquivo(s)**: `internal/quotation/service.go`, `internal/quotation/service_test.go`,
  `internal/quotation/handler_test.go`
- **Teste que prova**: `TestQuoteUsesTheCacheEntryWithoutCallingThePartner`,
  `TestQuoteFallsBackToLiveOnCacheMiss`, e o segundo teste determinístico exigido pelo enunciado,
  `TestPartialResponseWithCacheAndFallback` (miniredis real, cobre cache + fallback + 503 juntos, FDD
  seção 9)
- **Comando de verificação**: `go test ./internal/quotation/... -v && make test`

**Descrição técnica**
Antes de chamar o `Quoter`, `service.Quote` calcula `fingerprint := request.Fingerprint()` (T03) e
`key := cache.Key(tenant, p.Name, fingerprint)` (T06), consulta `QuoteCache.Get`; em acerto, marca
`Source: "cache"`, calcula `AgeSeconds` a partir de `storedAt` e pula a chamada ao parceiro; em miss
(incluindo falha do Redis), chama o `Quoter` diretamente como hoje (o breaker entra só em T09) e, em
sucesso, grava no cache com `QuoteCache.Set` (best effort, erro de `Set` é ignorado) (FDD seção 4, passos
2.1-2.4).

**Contrato de API** *(shape final do `200` parcial, com `source: "cache"`)*

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

**Abordagem sugerida**
`NewService(partners []platform.Partner, quoter Quoter, cache *cache.QuoteCache) *Service`, com
`cache` aceitando `nil` (tratado como "sempre miss", sem qualquer chamada ao Redis) para que os testes
que não exercitam cache continuem passando com `NewService(threePartners, quoter, nil)`.
[PENDENTE: o FDD não define a assinatura exata de `NewService`; esta é uma decisão deste plano, alinhar
antes de codar caso o time prefira uma interface `Cache` em vez do tipo concreto `*cache.QuoteCache`.]

**Teste determinístico exigido (cache + fallback, `miniredis`)**
Usando um `*miniredis.Miniredis` real e `cache.NewQuoteCache` real (não um fake): pré-carrega a chave de
uma parceira (vira `source: "cache"` com `age_seconds` presente), deixa outra parceira sem cache e
falhando no `Quoter` (vira `missing_partners`, `degraded: true`), e a terceira sem cache e bem-sucedida
(`source: "live"`). Um segundo subteste zera o cache e faz as três falharem no `Quoter`: resposta `503`.

**Critérios de aceite**
- [ ] Uma parceira com entrada de cache válida não aciona o `Quoter` (contagem de chamadas não inclui
  essa parceira) e aparece com `source: "cache"` e `age_seconds` no JSON.
- [ ] Uma parceira sem cache, bem-sucedida ao vivo, grava uma entrada no cache (verificável relendo via
  `QuoteCache.Get` depois da chamada).
- [ ] Uma parceira sem cache e falhando no `Quoter` aparece em `missing_partners`.
- [ ] Com as três parceiras sem cache e falhando no `Quoter`, a resposta é `503` com o corpo exato de
  T07b.
- [ ] `Get`/`Set` contra Redis indisponível (miniredis fechado no meio do teste) não impede uma resposta
  bem-sucedida ao vivo (falha do Redis é best effort, FDD decisão 2).
- [ ] Um acerto de cache sozinho, com as outras duas ao vivo, produz `degraded: false` (acerto de cache
  nunca é degradação).

**Observações**
Esta é a task mais pesada do plano: junta os três pacotes novos/alterados (`cache`, `quotation.Request`,
`quotation.Service`) pela primeira vez. É aqui que mora o segundo teste determinístico exigido pelo
enunciado.

---

##### T09: Breaker por parceira em `service.Quote`

- **Complexidade**: M
- **Estimativa**: 1 dia-dev
- **Depende de**: T05, T08
- **Desbloqueia**: T10
- **Paralelizável com**: nenhuma
- **Arquivo(s)**: `internal/quotation/service.go`, `internal/quotation/service_test.go`
- **Teste que prova**: `TestQuoteSkipsThePartnerWhenItsBreakerIsOpen`
- **Comando de verificação**: `go test ./internal/quotation/... -v && make test`

**Descrição técnica**
No ramo de cache miss (T08), a chamada direta ao `Quoter` passa a ser envolvida por
`breakers[p.Name].Execute(ctx, func() (partner.Quote, error) { return s.quoter.Quote(ctx, p, forPartner)
})` quando existe um breaker para aquela parceira (FDD seção 4, passo 2.3).

**Abordagem sugerida**
`NewService` ganha `breakers map[string]*resilience.Breaker`. Se `breakers[p.Name]` não existir (mapa
`nil` ou chave ausente, leitura seguro em Go), chama o `Quoter` direto, sem breaker — isso mantém todos
os testes de T01-T08 compilando com `NewService(..., nil)` sem precisar construir breakers fictícios só
para não quebrar.

**Critérios de aceite**
- [ ] Um breaker pré-aberto (forçado no setup do teste com falhas suficientes via `Execute`) para uma
  parceira faz `service.Quote` marcar essa parceira em `missing_partners` **sem** chamar o `Quoter` para
  ela (contagem de chamadas não muda).
- [ ] Uma parceira sem breaker configurado continua sendo chamada normalmente (comportamento de T08
  preservado).
- [ ] `ErrTooManyRequests` (meio aberto saturado) é tratado exatamente como qualquer outra falha de
  parceira para fins de `missing_partners` (FDD seção 6).

**Observações**
O teste de unidade do próprio `Breaker` (abertura determinística na rajada) já existe em T05; este teste
é sobre a integração, não sobre o breaker isolado.

---

##### T09b: Telemetria de negócio (gauge do breaker, contador de cache, span e logs)

- **Complexidade**: M/G
- **Estimativa**: 1,5 dia-dev
- **Depende de**: T05, T06, T08, T09
- **Desbloqueia**: T10
- **Paralelizável com**: nenhuma
- **Arquivo(s)**: `internal/platform/telemetry.go`, `internal/resilience/breaker.go` (`Config` ganha um
  campo `OnStateChange`, assinatura de `NewBreaker` não muda), `internal/cache/quote_cache.go` (ganha
  uma opção `WithMeter`, assinatura de `Get`/`Set` não muda), `internal/quotation/service.go`
- **Teste que prova**: `TestBreakerStateGaugeFollowsTransitions`, `TestCacheResultCounterIncrements`
  (ambos com o `ManualReader` em memória do SDK do OTel), mais `TestPartnerQuoteLogHasNoPersonalData`
  (adicional aos dois pedidos, para verificar objetivamente a exigência de "sem dado pessoal")
- **Comando de verificação**: `go test ./internal/... -v && make test`

**Descrição técnica**
Implementa as três métricas de negócio e as duas marcações de trace exigidas pelo enunciado (seção
"Instrumentação", e FDD seção 7):

1. **Gauge `partner_breaker_state{partner}`** (`0` fechado, `1` meio aberto, `2` aberto). Vive em
   `internal/platform/telemetry.go` como um `Int64ObservableGauge`: mantém o último estado conhecido por
   parceira num mapa protegido por mutex, atualizado de forma síncrona e barata pelo hook
   `resilience.Config.OnStateChange func(name string, from, to gobreaker.State)` (campo novo, opcional,
   `nil` é no-op), e relatado ao coletor só no momento da coleta, via `RegisterCallback`. `NewBreaker`
   dispara o hook uma vez com o estado inicial (`StateClosed`) logo após a criação, para que o gauge
   tenha um valor mesmo antes da primeira falha.
2. **Contador `quotation_cache_result_total{result="hit"|"miss"|"redis_error"}`**. Vive dentro de
   `internal/cache/quote_cache.go`, incrementado internamente por `Get`/`Set` conforme o desfecho real
   (chave encontrada, chave ausente/expirada, ou erro de conexão com o Redis) — a distinção de
   `redis_error` só existe dentro do pacote `cache`, porque `Get` continua retornando só `ok bool` para
   quem chama (T06, FDD seção 5: "nunca retorna erro para o chamador"). `NewQuoteCache` ganha uma opção
   variádica `WithMeter(m metric.Meter)`; sem ela, usa `otel.Meter("quotation-api")` (padrão de produção),
   e o teste injeta um `Meter` de um `MeterProvider` local para ler os valores sem depender do provider
   global do processo.
3. **Span `partner.quote` e atributo `partner.result`**, em `internal/quotation/service.go`, um por
   parceira processada dentro do laço de `Quote`. Valores: `cache_hit`, `live_success`, `circuit_open`,
   `too_many_requests`, `timeout` (`errors.Is(err, context.DeadlineExceeded)`), `http_error` (demais
   erros de `*partner.Error`). É esse atributo que responde à cobrança do enunciado: "a requisição
   curto-circuitada tem que dizer isso no trace, senão ela aparece como uma cotação misteriosamente
   rápida" — o span existe mesmo quando o breaker impede a chamada de sair.
4. **Campos de log estruturado** por parceira processada: `tenant_id`, `partner`, `source`,
   `breaker_state`, `elapsed_ms`, `cache_result`. Nunca CPF, placa, modelo, ano do veículo ou `quote_id`
   (o enunciado, seção "Instrumentação", proíbe `quote_id` em atributo de span ou métrica; por
   segurança, o plano estende a mesma proibição ao log de negócio).

**Critérios de aceite**
- [ ] Depois de 5 falhas consecutivas contra um `httptest.Server` com o `Config.OnStateChange` ligado ao
  gauge, o `ManualReader` do teste coleta `partner_breaker_state{partner="..."}` igual a `2`.
- [ ] Depois do `OpenTimeout` e de duas sondas bem-sucedidas em meio aberto, a mesma coleta mostra `0`.
- [ ] Um `Get` com acerto incrementa `quotation_cache_result_total{result="hit"}`; um `Get` numa chave
  ausente incrementa `result="miss"`; um `Get`/`Set` contra um `miniredis` fechado incrementa
  `result="redis_error"`, sem alterar o retorno público de `Get` (continua só `ok bool`).
- [ ] Uma parceira servida de cache tem span `partner.quote` com `partner.result="cache_hit"`; uma
  parceira com o breaker aberto tem `partner.result="circuit_open"`, mesmo sem nenhuma chamada HTTP de
  saída para ela.
- [ ] Nenhum campo de log ou atributo de span/métrica contém CPF, placa, modelo, ano do veículo ou
  `quote_id`, verificado por um teste que captura a saída do logger num buffer e busca por essas
  substrings a partir de uma requisição com dados reais.

**Observações**
[Refinamento sobre o FDD: `docs/fdd-resiliencia-parceiras.md`, seção 7, descreve
`quotation_cache_result_total{result="hit"|"miss"}` com a falha do Redis somando a `miss`; esta task usa
um terceiro rótulo, `redis_error`, por instrução explícita desta rodada de planejamento. Se o time
confirmar que é a intenção definitiva, vale atualizar o FDD (e o extrato
`docs/work/fdd-resiliencia-parceiras/contratos.md`) para não ficar divergente do código, já que o
enunciado reprova "métrica citada na documentação que o código não emite" e o inverso, código emitindo
o que a documentação não descreve, é a mesma falta de coerência.]
Cardinalidade: `partner` e `result` são os únicos rótulos, ambos de baixa cardinalidade (3 parceiras, 2-3
resultados); nenhum rótulo carrega `tenant_id`, placa ou `quote_id` (o enunciado trata isso como
"cardinalidade sem teto" e proíbe diretamente).

---

#### Fase 5: Wiring final

##### T10: `cmd/quotation-api/main.go` monta Redis, cache e os três breakers

- **Complexidade**: M
- **Estimativa**: 1,25 dia-dev
- **Depende de**: T01, T02, T06, T09, T09b
- **Desbloqueia**: T11
- **Paralelizável com**: nenhuma
- **Arquivo(s)**: `cmd/quotation-api/main.go`
- **Teste que prova**: nenhum teste automatizado dedicado (ver Observações); verificação por build,
  vet e smoke manual
- **Comando de verificação**: `go build ./... && go vet ./... && make test`, seguido de smoke manual:
  `docker compose up -d --build --wait && curl -s -X POST http://localhost:8080/quotes -H 'X-Tenant-Id:
  corretora-a' -d '{"driver":{"document":"1","birth_year":1990},"vehicle":{"plate":"ABC1D23","model":"Onix","year":2022,"value_cents":6000000}}' | jq .`

**Descrição técnica**
Constrói `redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})`, um `platform.BreakerStateGauge` (T09b)
via `otel.Meter("quotation-api")`, `cache.NewQuoteCache(redisClient, cfg.CacheQuoteTTL,
cache.WithMeter(otel.Meter("quotation-api")))`, um `map[string]*resilience.Breaker` com uma entrada por
`cfg.Partners` usando `resilience.Config{cfg.Breaker.ConsecutiveFailures, cfg.Breaker.OpenTimeout,
cfg.Breaker.HalfOpenMaxRequests, OnStateChange: gauge.Update}`, e chama `quotation.NewService(cfg.Partners,
partner.NewClient(cfg.PartnerTimeout), quoteCache, breakers)` (FDD seção 3, "Incluído"; T09b para o
wiring de telemetria).

**Critérios de aceite**
- [ ] `go build ./...` e `go vet ./...` passam.
- [ ] Com o ambiente do `docker-compose.yml` (não alterado) de pé, uma cotação bem-sucedida retorna
  `source: "live"` para todas as parceiras na primeira chamada.
- [ ] A mesma requisição repetida logo em seguida retorna ao menos uma parceira com `source: "cache"`.
- [ ] `curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=partner_breaker_state'` (ou a
  UI do Prometheus) mostra as três séries, uma por parceira, com valor `0` no repouso.
- [ ] `curl -s http://localhost:9090/api/v1/query --data-urlencode 'query=quotation_cache_result_total'`
  mostra pelo menos os rótulos `result="hit"` e `result="miss"` depois de algumas cotações repetidas.
- [ ] Nenhuma mudança em `docker-compose.yml`, `cmd/partner-mock` ou `cmd/loadgen`.

**Observações**
Não há teste automatizado de `main.go` no padrão deste repositório (só `cmd/partner-mock` expõe um
`routes()` testável; `cmd/quotation-api/main.go` nunca teve `main_test.go`). A prova real desta task é o
smoke manual acima, e a confirmação definitiva vem da Fase 6 (`make reproduce`).

---

#### Fase 6: Evidências do depois

##### T11: As 7 evidências da tabela do enunciado, em `docs/evidencias/depois/`

- **Complexidade**: M
- **Estimativa**: 1,5 dia-dev
- **Depende de**: T10
- **Desbloqueia**: T12
- **Paralelizável com**: nenhuma
- **Arquivo(s)**: `docs/evidencias/depois/reproduce.txt`, `docs/evidencias/depois/make-test.txt`,
  `docs/evidencias/depois/jaeger-trace-breaker-aberto.json`,
  `docs/evidencias/depois/jaeger-trace-cache.json`, `docs/evidencias/depois/prometheus-breaker-state.png`,
  `docs/evidencias/depois/prometheus-cache-hit-rate.png`,
  `docs/evidencias/depois/prometheus-p95-depois.png`, `docs/evidencias/antes/prometheus-p95-antes.png`
  (só se ainda faltar, ver pré-requisito no topo do plano), `docs/evidencias/depois/comparacao.md`
- **Teste que prova**: não é uma task de código; a prova é a evidência em si, cada linha com o comando
  ou a consulta que a gerou, colados, e uma legenda de uma linha (enunciado, seção "As evidências")
- **Comando de verificação**: `make down && make reproduce | tee docs/evidencias/depois/reproduce.txt`

**Descrição técnica**
Gera exatamente as 7 linhas da tabela de evidências do enunciado (seção "As evidências"), cada uma com
formato, comando/consulta e janela de tempo próprios. `make down` antes de **cada** `make reproduce`
desta task (inclusive se precisar repetir por ruído, ver tabela de riscos) para o ambiente começar
zerado: Jaeger guarda traces em memória, Prometheus não tem volume e retém só a última hora, e o
`make down` some com os dois (enunciado, "Comece por aqui", passo 1).

**As 7 evidências**

| # | O que mostra | Comando/consulta (colar como texto) | Janela de tempo | Arquivo |
|---|---|---|---|---|
| 1 | Diferença de p95, taxa de sucesso e vazão, antes/depois | `make down && make reproduce \| tee docs/evidencias/depois/reproduce.txt` | a execução inteira (10 baseline + 200 com 50 em voo) | `reproduce.txt` |
| 2 | Toda a suíte (a que já vinha e a nova) passando | `make test \| tee docs/evidencias/depois/make-test.txt` | não se aplica (não é gráfico) | `make-test.txt` |
| 3 | Requisição que não chamou a parceira com o breaker aberto, respondeu rápido | Jaeger UI `http://localhost:16686`, service `quotation-api`, operation `POST /quotes`, Tags `partner.result=circuit_open`; export: `curl -s 'http://localhost:16686/api/traces?service=quotation-api&tags=%7B%22partner.result%22%3A%22circuit_open%22%7D&lookback=1h&limit=5' -o docs/evidencias/depois/jaeger-trace-breaker-aberto.json` | `Last Hour`, capturado durante a carga | `jaeger-trace-breaker-aberto.json` |
| 4 | Cotação sem os spans de saída para a parceira servida de cache | Jaeger UI, mesma tela, Tags `partner.result=cache_hit`; export: `curl -s 'http://localhost:16686/api/traces?service=quotation-api&tags=%7B%22partner.result%22%3A%22cache_hit%22%7D&lookback=1h&limit=5' -o docs/evidencias/depois/jaeger-trace-cache.json` | `Last Hour`, capturado durante a carga | `jaeger-trace-cache.json` |
| 5 | Degrau fechado → aberto → meio aberto → fechado no tempo | Prometheus UI `http://localhost:9090`, aba Graph, PromQL `partner_breaker_state{partner="partner-flaky"}` | `Last Hour`, capturado **durante** a execução (Prometheus não persiste) | `prometheus-breaker-state.png` |
| 6 | Curva do hit rate subindo conforme o cache aquece | PromQL `sum(rate(quotation_cache_result_total{result="hit"}[5m])) / sum(rate(quotation_cache_result_total{result=~"hit\|miss"}[5m]))` | `Last Hour`, passo de rate `[5m]`, capturado durante a execução | `prometheus-cache-hit-rate.png` |
| 7 | p95 do `POST /quotes`, antes e depois, mesma escala | PromQL `histogram_quantile(0.95, sum by (le) (rate(http_server_request_duration_seconds_bucket[5m])))`, uma captura por execução | `Last Hour`, `[5m]`, uma captura na sessão do antes e outra na do depois | `prometheus-p95-antes.png` (se faltar) e `prometheus-p95-depois.png` |

**Abordagem sugerida**
1. `make down && make reproduce | tee docs/evidencias/depois/reproduce.txt` (linha 1; mesmo comando do
   enunciado, seção "Comece por aqui", passo 2, e de `docs/roteiro-cenario-de-falha.md`).
2. Com o ambiente ainda de pé (não rodar `make down` entre os passos 2 e 7), `make test | tee
   docs/evidencias/depois/make-test.txt` (linha 2; não depende do ambiente, mas fica na mesma sessão por
   organização).
3. Repetir `make load` uma ou duas vezes para ter tráfego suficiente na janela, e capturar as linhas 3-7
   no Jaeger e no Prometheus enquanto a carga ainda está na retenção de 1 hora de ambos.
4. Escrever `docs/evidencias/depois/comparacao.md`: tabela antes/depois de p95, taxa de sucesso (RNF-01,
   RNF-02 do SAD), taxa de acerto de cache (RNF-03, antes 0% por construção), citando
   `docs/evidencias/antes/loadgen-antes.txt` lado a lado com os novos números.

**Critérios de aceite**
- [ ] As 7 linhas da tabela existem, cada uma com o comando/consulta colado como texto (não só a
  descrição) e uma legenda de uma linha.
- [ ] Toda evidência de gráfico traz a consulta PromQL como texto ao lado e declara a janela de tempo
  (regra de formato do enunciado).
- [ ] O relatório da linha 1 e a saída da linha 2 estão como texto colado, não como imagem.
- [ ] Nenhum trace do depois mostra `502` para uma única parceira fora (só `503` na falha total, se
  ocorrer, ou `200 degraded:true`).
- [ ] A evidência de p95 (linha 7) tem uma captura do antes e uma do depois, na mesma escala.
- [ ] `docker-compose.yml`, `cmd/partner-mock` e `cmd/loadgen` permanecem byte-a-byte iguais ao início do
  plano (`git diff --stat` não lista nenhum dos três).

**Observações**
Se qualquer captura de gráfico sair ruidosa ou vazia, repetir com `make down && make reproduce` antes,
nunca reaproveitar uma captura antiga sem repetir a carga (o enunciado trata "métrica existindo mas
nunca mudando de valor" como evidência que não conta). Esta task não introduz nenhum teste Go novo; ela
fecha o ciclo pedido pelo enunciado ("a evidência que você mediu"), não um resultado a maquiar se os
números do depois não melhorarem.

---

#### Fase 7: Fechamento

##### T12: README do processo

- **Complexidade**: P
- **Estimativa**: 0,5 dia-dev
- **Depende de**: T11 (precisa dos links de evidência e da lista final de métricas/atributos)
- **Desbloqueia**: nenhuma (última task do plano)
- **Paralelizável com**: nenhuma
- **Arquivo(s)**: `README.md` (novo, raiz do repositório; hoje não existe, ver Observações)
- **Teste que prova**: não é código; critério de aceite é uma checklist de conteúdo, mais uma
  verificação cruzada de que cada métrica citada existe de fato no código
- **Comando de verificação**: `grep -rn "partner_breaker_state\|quotation_cache_result_total\|partner.quote\|partner.result" internal/` (toda métrica/atributo citado no README precisa aparecer aqui)

**Descrição técnica**
Escreve o README do processo exigido pelo enunciado (seção "O README do processo"), na raiz do
repositório, na branch `main`, com as cinco coisas exigidas, o parágrafo de prosa e a chave de cache por
extenso.

**As cinco coisas exigidas**
1. Link para `docs/sad.md` e para `docs/evidencias/` (antes e depois).
2. Como subir o ambiente e reproduzir **esta** versão: `make up`, `make ps`, `make down && make
   reproduce`, exatamente como em `docs/enunciado.md`, "Comece por aqui".
3. O que foi implementado e o que ficou como proposta: circuit breaker, cache e fallback parcial
   implementados nesta fatia; os quatro limites conhecidos do SAD (limite de chamadas simultâneas por
   parceira, breaker não distribuído entre réplicas, TTL como única invalidação, sem isolamento de
   capacidade por tenant) e a paralelização da agregação (bônus) permanecem como proposta, não
   implementados.
4. Nomes das métricas e atributos criados (T09b): gauge `partner_breaker_state{partner}`, contador
   `quotation_cache_result_total{result="hit"|"miss"|"redis_error"}`, span `partner.quote` com atributo
   `partner.result` (`cache_hit`, `live_success`, `circuit_open`, `too_many_requests`, `timeout`,
   `http_error`).
5. O que seria diferente com mais tempo: candidatos naturais são os próprios quatro limites conhecidos do
   SAD, não itens novos inventados para a ocasião.

**A chave de cache por extenso** (repetida do SAD e do FDD, para satisfazer a exigência do enunciado de
que ela apareça "no SAD e no README do processo"):
```
quote:v1:<tenant_id>:<partner_name>:<hash>
```
onde `<hash>` são os 16 primeiros caracteres hexadecimais do SHA-256 de `documento normalizado | ano de
nascimento | placa normalizada | modelo | ano do veículo | valor em centavos | cobertura`. O `tenant_id`
está presente sem exceção: é o componente que o enunciado marca como reprovação automática se faltar
("Chave de cache sem `tenant_id` reprova").

**O parágrafo de prosa** (exigido à parte das cinco coisas): um parágrafo curto descrevendo o coração da
solução — o que foi protegido (as três parceiras instáveis), com o quê (breaker por parceira, cache por
parceira, fallback como resposta parcial), e o que isso custou (mudança de contrato de resposta, 3x mais
chaves no Redis que a alternativa agregada, breaker em memória por réplica).

**Critérios de aceite**
- [ ] As cinco coisas exigidas aparecem, cada uma identificável (não misturadas em prosa contínua).
- [ ] A chave de cache aparece por extenso, idêntica à do SAD e do FDD, com `tenant_id` explicitamente
  marcado como obrigatório.
- [ ] Toda métrica/atributo citado no README aparece no `grep` do comando de verificação.
- [ ] O parágrafo de prosa sobre o coração da solução existe e é distinto das cinco coisas listadas.
- [ ] `README.md` está na raiz do repositório, na branch `main` (não em `docs/`).

**Observações**
Hoje não existe `README.md` na raiz: o arquivo que ocupava esse lugar era o próprio enunciado, já movido
para `docs/enunciado.md` (enunciado, "O resto, na ordem", passo 5). Esta task cria o README do processo
do zero, não edita um placeholder.

---

### Checkpoints de validação

| Após fase | Validação | Como verificar |
|---|---|---|
| 1 | Config valida as seis variáveis; cliente HTTP tem timeout; nenhum comportamento de negócio mudou ainda | `make test`; resposta a uma parceira fora continua `502` (ainda não mudou) |
| 2 | `resilience` e `cache` existem e passam isolados, mas nada em `quotation` os usa ainda | `go test ./internal/resilience/... ./internal/cache/... -v && make test` |
| 3 | Contrato de resposta mudou (`degraded`, `missing_partners`, `source`, `age_seconds`); fallback parcial funciona sem cache nem breaker; `502` só sobra para erro inesperado | `go test ./internal/quotation/... -v`; `curl` manual com um fake mostrando `200 degraded:true` e `503` na falha total |
| 4 | Cache-aside, breaker e telemetria de negócio totalmente integrados; os dois testes determinísticos exigidos pelo enunciado passam; gauge, contador, span e logs existem | `go test ./... -v \| grep -E 'Breaker\|Cache\|Gauge\|Counter'`; `make test` |
| 5 | Aplicação sobe via `docker compose up` com Redis, os três breakers reais e telemetria de negócio; `source`/`age_seconds`/`degraded` aparecem de verdade; métricas aparecem no Prometheus | smoke manual de T10 |
| 6 | As 7 evidências da tabela do enunciado existem em `docs/evidencias/depois/`, cada uma com comando/consulta e janela de tempo | leitura de `comparacao.md` e conferência linha a linha da tabela de T11 |
| 7 | `README.md` na raiz, branch `main`, com as cinco coisas exigidas, o parágrafo de prosa e a chave de cache por extenso | leitura do `README.md`; `grep` das métricas citadas contra `internal/` |

---

### Riscos de implementação

| Risco | Probabilidade | Impacto | Mitigação | Task afetada |
|---|---|---|---|---|
| API de `gobreaker/v2` difere do que o FDD assume (nomes de campos, forma genérica de `Execute`) | média | retrabalho no wrapper de `internal/resilience` | fixar a versão em T02 antes de escrever T05; ajustar a assinatura interna do wrapper, não o contrato público já definido no FDD | T05 |
| `go-redis/v9` e `miniredis/v2` divergem em algum comando RESP usado (ex.: `SET` com `EX`) | baixa | teste de T06 falha de forma confusa, não por bug de lógica | usar só comandos básicos (`SET`/`GET`/`EXPIRE`), já bem suportados pelo `miniredis` | T06 |
| Reescrever os dois testes existentes (`TestOnePartnerDown...`, `TestQuotesResponds502...`) fora da mesma task que muda o comportamento | alta se não seguido à risca | `make test` fica vermelho entre commits, violando a restrição do plano | T07b inclui a reescrita dos dois testes como parte da mesma task, não depois | T07b |
| `AgeSeconds` calculado com o relógio real torna o teste de T06/T08 flutuante (diferença de milissegundos) | média | teste ocasionalmente falha por margem apertada | usar tolerância (`>= 0` e `< N segundos`, nunca igualdade exata) nos asserts de `age_seconds`/`storedAt` | T06, T08 |
| Esquecer de popular o mapa de breakers para alguma das três parceiras em `main.go`, deixando-a sem proteção silenciosamente | baixa | uma parceira roda sem circuit breaker em produção sem erro visível | T10 constrói o mapa iterando `cfg.Partners`, nunca uma lista hardcoded separada; T11 (evidência) expõe isso se o breaker nunca abrir sob carga | T10 |
| Números de `make reproduce`/Jaeger variam entre execuções por causa do jitter dos mocks | baixa | comparação antes/depois com ruído | rodar duas vezes (sempre com `make down` antes) e usar a execução representativa, mesma prática de `docs/evidencias/antes/` | T11 |
| Captura de p95 "antes" (evidência 7) nunca foi feita, e não dá para refazer depois que o código mudar | média | a evidência 7 fica incompleta, sem comparação válida na mesma escala | verificar isso **antes** da Fase 1 (pré-requisito no topo do plano), não em T11 | T11 |

---

### Observações finais

- Nenhuma task altera `cmd/partner-mock`, `cmd/loadgen` ou os perfis de `docker-compose.yml`; a coluna
  "Arquivo(s)" de cada task acima comprova isso por omissão.
- `make test` (== `go test ./...`) é usado como gate de toda task porque `go test ./...` compila todos os
  pacotes de `./...`, inclusive `cmd/quotation-api`; qualquer mudança de assinatura que quebre um chamador
  precisa atualizar esse chamador na mesma task (é por isso que T04, T07a, T08 e T09 tocam `main.go` ou os
  testes de chamada em vez de deixar isso para depois).
- [PENDENTE: a assinatura final de `NewService` (ordem e tipos de `cache`/`breakers`) é uma decisão deste
  plano, não do FDD; ela é introduzida incrementalmente (T08 adiciona `cache`, T09 adiciona `breakers`)
  para manter cada task pequena, mas o time pode preferir uma única mudança de assinatura em T08 se achar
  a evolução em duas etapas mais confusa que direta.]
- A ordem T07 (contrato/fallback) antes de T08/T09 (cache/breaker) é deliberada: o fallback parcial é
  testável e válido usando só o `Quoter` que já existe, sem esperar por Redis ou pelo `gobreaker`, o que
  reduz o tamanho de cada task individual.
- T09b introduz um terceiro rótulo (`redis_error`) no contador de cache que o FDD, seção 7, não previa
  (lá o contador tinha só `hit`/`miss`, com falha do Redis somando a `miss`). É uma decisão desta rodada
  de planejamento, não uma leitura livre do FDD; fica sinalizada na tabela de riscos para não virar uma
  divergência silenciosa entre o SAD/FDD e o código, o que o enunciado trata como falta de coerência.
- T11 e T12 seguem a tabela de evidências e a lista de cinco itens do README exatamente como o enunciado
  as descreve (`docs/enunciado.md`, seções "As evidências" e "O README do processo"), porque a correção é
  estática: citar uma evidência ou um item do README fora do formato pedido lá conta contra a entrega
  tanto quanto não tê-lo.
