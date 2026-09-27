> Derivado de docs/plano-resiliencia-parceiras.md (tasks e observações finais). Fonte de verdade é o
> original; em conflito, o original vence.

# Decisões de planejamento (não estão no FDD)

## Assinatura de `NewService`, introduzida em duas etapas

- FDD não define a assinatura exata de `NewService` com `cache`/`breakers` (gap sinalizado como
  `[PENDENTE]` nas tasks T08, T09 e nas observações finais do plano).
- Decisão deste plano: `NewService` evolui incrementalmente.
  - T08: `NewService(partners []platform.Partner, quoter Quoter, cache *cache.QuoteCache) *Service`,
    com `cache == nil` tratado como "sempre miss" (nenhuma chamada ao Redis).
  - T09: ganha `breakers map[string]*resilience.Breaker`; `breakers[p.Name]` ausente ou mapa `nil`
    significa "chama o `Quoter` direto, sem proteção de breaker" (leitura de mapa `nil` é segura em Go).
- Motivo: permite que testes de T01-T08 continuem passando com `NewService(..., nil)`/`NewService(...,
  nil, nil)` sem precisar construir caches/breakers fictícios só para não quebrar a compilação.
- Alternativa não escolhida: introduzir `cache` e `breakers` numa única mudança de assinatura em T08.
  Descartada para manter cada task menor; o time pode preferir a mudança única se achar a evolução em
  duas etapas mais confusa.

## Split de T07 em T07a/T07b

- T07a (campos novos do contrato: `Degraded`, `MissingPartners`, `Source`, `AgeSeconds`) e T07b (fallback
  como resposta parcial, `503` na falha total) são duas tasks, não uma.
- Motivo: T07a é puramente aditivo (não quebra nenhum teste existente); T07b inverte o comportamento de
  dois testes hoje passando. Separar os dois evita misturar "mudança de shape" com "mudança de
  comportamento de erro" na mesma revisão.

## `go test ./...` como gate por task

- `make test` == `go test ./...`, que compila todos os pacotes de `./...`, inclusive `cmd/quotation-api`
  (mesmo sem testes próprios). Qualquer mudança de assinatura que quebre um chamador (`main.go` ou um
  `_test.go`) precisa atualizar esse chamador **na mesma task**, nunca depois.
- Consequência direta: T04 (muda `partner.NewClient`) já atualiza `cmd/quotation-api/main.go`; T07b já
  reescreve os dois testes invalidados no mesmo commit; T08/T09 já atualizam todos os call sites de
  `NewService` nos testes existentes.

## Ordem: contrato/fallback (T07) antes de cache/breaker (T08/T09)

- Decisão de sequenciamento: o fallback parcial é testável e válido usando só o `Quoter` que já existe
  hoje, sem esperar por `gobreaker`/Redis. Isso reduz o tamanho de T07 e evita que a task de maior risco
  de regressão (mudar o comportamento de erro) dependa de duas bibliotecas externas ainda não integradas.

## Teste de T10 (`main.go`) não é automatizado

- Decisão: `cmd/quotation-api/main.go` não ganha `main_test.go` nesta entrega. O padrão do repositório só
  testa `main` em `cmd/partner-mock` porque esse pacote expõe um `routes()` testável; `quotation-api`
  nunca teve isso.
- Critério de aceite alternativo: build + vet + smoke manual (`docker compose up --wait` + `curl`), com a
  prova definitiva vindo da Fase 6 (`make reproduce`).

## Riscos de implementação específicos deste plano (não são os riscos do FDD)

| Risco | Task afetada | Mitigação |
|---|---|---|
| API de `gobreaker/v2` difere do assumido no FDD | T05 | fixar versão em T02 antes de escrever o wrapper |
| `go-redis/v9`/`miniredis/v2` divergem em algum comando RESP | T06 | usar só `SET`/`GET`/`EXPIRE` |
| Reescrita dos testes invalidados feita fora da task que muda o comportamento | T07b | reescrita é parte da mesma task, não uma task separada |
| `AgeSeconds` calculado com relógio real torna teste flutuante | T06, T08 | tolerância nos asserts, nunca igualdade exata |
| Mapa de breakers não populado para as três parceiras em `main.go` | T10 | construir o mapa iterando `cfg.Partners`, nunca lista hardcoded |
| Números de `make reproduce`/Jaeger variam entre execuções | T11 | rodar duas vezes (sempre com `make down` antes), usar a execução representativa |
| Captura de p95 "antes" nunca foi feita e não dá para refazer depois que o código mudar | T11 | verificar isso antes da Fase 1 (pré-requisito no topo do plano), não em T11 |
| Contador de cache com rótulo `redis_error` refina o FDD sem atualizar o documento | T09b | atualizar `docs/fdd-resiliencia-parceiras.md` seção 7 se o time confirmar o terceiro rótulo |

## Telemetria de negócio (T09b), decisões que não estão no FDD

- **Gauge do breaker sem mudar a assinatura de `NewBreaker`**: `resilience.Config` ganha um campo
  `OnStateChange func(name string, from, to gobreaker.State)`, opcional (`nil` é no-op). O gauge em si
  (`platform.BreakerStateGauge`, um `Int64ObservableGauge`) mantém o último estado por parceira num mapa
  protegido por mutex, atualizado de forma síncrona no hook; o OTel só lê esse mapa no momento da coleta,
  via `RegisterCallback`. `NewBreaker` dispara o hook uma vez com `StateClosed` na criação, para o gauge
  não ficar "sem dado" antes da primeira falha.
- **Contador de cache com três rótulos sem mudar a assinatura de `Get`**: `quotation_cache_result_total`
  ganha `result="redis_error"` além de `hit`/`miss`. A distinção só existe dentro de
  `internal/cache/quote_cache.go` (que sabe se foi miss real ou falha de conexão); `Get` continua
  retornando só `ok bool` para `service.go`, preservando o contrato normativo do FDD ("nunca retorna erro
  para o chamador"). `NewQuoteCache` ganha uma opção variádica `WithMeter(m metric.Meter)`, não um
  parâmetro posicional novo, para não quebrar as chamadas já planejadas em T06/T10.
- **Refinamento sobre o FDD, sinalizado, não silencioso**: `docs/fdd-resiliencia-parceiras.md`, seção 7,
  descreve o contador com só dois rótulos (`hit`/`miss`, falha do Redis soma a `miss`). O terceiro rótulo
  é uma instrução explícita desta rodada de planejamento; se confirmado como definitivo, o FDD e seu
  extrato em `docs/work/fdd-resiliencia-parceiras/contratos.md` precisam ser atualizados para não
  divergir do código (ver tabela de riscos).
- **Span `partner.quote` cobre o caso do enunciado que "não conta"**: a exigência de que "a requisição
  curto-circuitada tem que dizer isso no trace, senão ela aparece como uma cotação misteriosamente
  rápida" é resolvida pelo atributo `partner.result="circuit_open"` no span de negócio, que existe mesmo
  quando nenhuma chamada HTTP sai para a parceira.
