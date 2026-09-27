> Derivado de docs/plano-resiliencia-parceiras.md (tasks detalhadas). Fonte de verdade é o original; em
> conflito, o original vence.

# Rastreabilidade: task do plano → seção do FDD/SAD → origem

| Task | Seção do FDD | Origem externa (arquivo:linha, evidência) |
|---|---|---|
| T01 | FDD seção 5, `docs/work/fdd-resiliencia-parceiras/contratos.md` | `internal/platform/config.go` (padrão `parsePartners`/`parseTenants`) |
| T02 | FDD seção 8 | `go.mod` (bibliotecas ainda não fixadas) |
| T03 | FDD seção 5, "Ciclo de import evitado" | `docs/work/fdd-resiliencia-parceiras/decisoes.md` |
| T04 | FDD seção 5, "`internal/partner.Client` (estendido)"; SAD decisão 1 | `internal/partner/client.go:23-28` (`NewClient` sem `Timeout` hoje) |
| T05 | FDD seção 5, 9; SAD decisão 1 | `docs/sad.md:244-264`; risco ajustado em `docs/fdd-resiliencia-parceiras.md`, seção 10 (script fixo, não `cmd/partner-mock`) |
| T06 | FDD seção 5; SAD decisão 2 | `docker-compose.yml:133-145` (Redis já sobe, nenhum código fala com ele) |
| T07a | FDD seção 5, exemplo de resposta | `internal/quotation/request.go` (`Response` hoje sem estes campos) |
| T07b | FDD seção 4, 6; SAD decisão 3 | `internal/quotation/service.go:31-34` (aborto hoje); `internal/quotation/handler.go:72-82` (`502` hoje) |
| T08 | FDD seção 4 (fluxo principal, passos 2.1-2.4), seção 9 (teste determinístico 2) | `docs/work/fdd-resiliencia-parceiras/contratos.md` (assinaturas de `cache`) |
| T09 | FDD seção 4 (passo 2.3) | `docs/work/fdd-resiliencia-parceiras/contratos.md` (assinatura de `resilience.Breaker`) |
| T09b | FDD seção 7; `docs/enunciado.md`, seção "Instrumentação" | `docs/fdd-resiliencia-parceiras.md`, seção 7 (métricas/spans/logs propostos); `docs/enunciado.md:366-391` |
| T10 | FDD seção 3, "Incluído" | `cmd/quotation-api/main.go` (wiring atual, sem Redis/breaker) |
| T11 | `docs/enunciado.md`, seção "As evidências" (tabela de 7 linhas, não uma seção do FDD) | `docs/enunciado.md:414-451`; `docs/roteiro-cenario-de-falha.md` (metodologia do "antes"); `docs/evidencias/antes/loadgen-antes.txt`, `docs/evidencias/antes/reproduce.txt` |
| T12 | `docs/enunciado.md`, seção "O README do processo" | `docs/enunciado.md:453-464` |

## Testes existentes que este plano reescreve (não cria do zero)

| Teste existente | Task que reescreve | Motivo |
|---|---|---|
| `TestOnePartnerDownBringsDownTheWholeRequest` (`internal/quotation/service_test.go:100`) | T07b | comportamento invertido: agora é resposta parcial, não erro |
| `TestQuotesResponds502WithTheNameOfThePartnerThatWentDown` (`internal/quotation/handler_test.go:95`) | T07b | `502` deixa de ser a resposta para uma única parceira fora |

## Restrições respeitadas em todas as tasks

- `cmd/partner-mock`, `cmd/loadgen` e os perfis de `docker-compose.yml` não aparecem na coluna
  "Arquivo(s)" de nenhuma task (ver `docs/plano-resiliencia-parceiras.md`, observações finais).
- `make test` é comando de verificação em todas as tasks de código (T01-T10); T11 usa `make reproduce`.
