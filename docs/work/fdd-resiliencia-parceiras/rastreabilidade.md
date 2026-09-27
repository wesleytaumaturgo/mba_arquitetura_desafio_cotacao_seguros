> Derivado de docs/fdd-resiliencia-parceiras.md (seções 1, 4, 5, 9). Fonte de verdade é o original; em
> conflito, o original vence.

# Rastreabilidade: item do FDD → seção → origem

| Item | Seção do FDD | Origem externa (arquivo:linha, evidência) |
|---|---|---|
| `NewClient` sem `Timeout` hoje | 1, 5 | `internal/partner/client.go:23-28` |
| Aborto da agregação em qualquer falha, hoje | 1, 4 | `internal/quotation/service.go:31-34` |
| Rajada de 9 falhas consecutivas nas sequências 49-57 (evidência real, coberta por `feasibility_test.go`/`make reproduce`, não pelo teste do breaker) | 1, 9, 10 | `docker-compose.yml:164-170`; travado por `cmd/partner-mock/feasibility_test.go` |
| Teste do breaker usa `httptest.Server` com script fixo próprio, sem importar `cmd/partner-mock` (evita acoplar o teste de unidade ao mock) | 9, 10 | decisão deste FDD, seção 9 e risco 6 |
| `Request.Fingerprint()` e `cache.Key(tenantID, partnerName, fingerprint)` evitam ciclo `internal/quotation` ↔ `internal/cache` | 1, 5 | decisão deste FDD, seção 5 |
| `partner-degrading`, soma 300ms acima de 5 simultâneas, teto 6000ms | 1 | `docker-compose.yml:177-191` |
| Redis já sobe, nenhum código fala com ele | 1 | `docker-compose.yml:133-145` |
| Chave de cache, TTL 900s, 6 variáveis novas, contrato de resposta, status HTTP | 3, 5, 6 | `docs/sad.md`, seção 4 e 5; `docs/work/sad/contratos.md`, `docs/work/sad/decisoes.md` |
| Parâmetros do breaker (5 falhas, 5s aberto, 2 sondas) e sua defesa numérica | 1, 5, 10 | `docs/sad.md:244-264` (decisão 1, seção 4) |
| Prêmio determinístico por hash da requisição no mock | 4 | `cmd/partner-mock/behavior.go:84-98` |
| Política "5 falhas consecutivas / 2 sucessos para fechar" simulada | 9, 10 | `cmd/partner-mock/feasibility_test.go` |
| Padrão de validação de configuração (`parsePartners`/`parseTenants`) | 2, 9 | `internal/platform/config.go` |
| `502` de hoje para qualquer falha de parceira | 1, 6 | `internal/quotation/handler.go:72-82` |
| Quatro limites conhecidos, não resolvidos nesta fatia | 1, 3, 10 | `docs/sad.md:402-409` (seção 4) |
| Decisão de hospedagem fora do escopo desta fatia | 1, 3 | `docs/sad.md:441-492` (seção 5) |

## Nota sobre este FDD

Este documento cobre só a fatia de código (circuit breaker, cache, fallback parcial); a decisão de
hospedagem da seção 5 do SAD não tem artefato de código correspondente e por isso não aparece nos
critérios de aceite deste FDD.
