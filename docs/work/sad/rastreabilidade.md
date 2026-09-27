> Derivado de docs/sad.md (seções 3-8). Fonte de verdade é o original; em conflito, o original vence.

# Rastreabilidade: item → seção → origem externa

| Item | Seção do SAD | Origem externa (arquivo:linha, evidência) |
|---|---|---|
| RNF-01, p95 8,01 s sob carga padrão | 3 | `docs/evidencias/antes/loadgen-antes.txt:9-13` |
| RNF-02, 60% (120 de 200) de respostas 2xx | 3 | `docs/evidencias/antes/loadgen-antes.txt:9-13` |
| RNF-03, hit rate hoje 0% por construção | 3 | `internal/quotation/service.go` (não importa cliente Redis) |
| RNF-03, carga padrão repete 5 cotações distintas | 3 | `cmd/loadgen/config.go`, `defaultDistinct = 5` |
| RNF-05, `partner-slow` ≈1569 ms, `partner-flaky` ≈167 ms, `partner-degrading` ≈6160 ms | 3 | `docs/roteiro-cenario-de-falha.md:100-105` (trace), consulta em `:149` |
| RF-01, `X-Tenant-Id` obrigatório | 3 | `internal/quotation/handler.go:39-43` |
| RF-02, ordenação por `premium_cents` no caso completo | 3 | `internal/quotation/service.go:38-40` |
| Decisão 1, `NewClient` sem `Timeout` | 4 | `internal/partner/client.go:23-28` |
| Decisão 1, aborto da agregação em qualquer falha | 4 | `internal/quotation/service.go:31-34` |
| `partner-flaky`, rajada de 9 falhas consecutivas nas sequências 49-57 | 4 | `docker-compose.yml:164-170`; confirmado em `docs/smoke-test-factibilidade.md`; travado por `cmd/partner-mock/feasibility_test.go` |
| `partner-degrading`, soma 300 ms por chamada extra acima de 5 simultâneas, teto 6000 ms | 4 | `docker-compose.yml:177-191` |
| Breaker abre na chamada 42 de 210, 7 aberturas e 2 recuperações | 4 | `docs/evidencias/antes/smoke-factibilidade.txt:5-6` |
| 2 sondas consecutivas bem-sucedidas fecham o breaker | 4 | `docs/evidencias/antes/smoke-factibilidade.txt:6` |
| Pico de até 34 chamadas simultâneas à `partner-flaky` | 4, 6 (A1 runbook) | `docs/smoke-test-factibilidade.md`, seção 2.3 |
| Redis sobe e nenhum código fala com ele | 4 (decisão 2) | `docker-compose.yml:133-145` |
| `valid_for_seconds` do mock, 300 s por padrão (TTL técnico, não o TTL de negócio) | 4 (decisão 2) | `cmd/partner-mock/config.go:62-66`, `PARTNER_QUOTE_TTL_SECONDS` |
| Campos normalizados usados no hash da chave de cache | 4 (decisão 2) | `internal/quotation/request.go:32-57`, `Request.Normalize()` |
| Prêmio do mock é determinístico por hash da requisição | 4 (decisão 2) | `cmd/partner-mock/behavior.go:84-98` |
| Comportamento hoje: `502` para qualquer falha de parceira | 4 (decisão 3) | `internal/quotation/handler.go:72-82` |
| Padrão de configuração (`parsePartners`/`parseTenants`) | 5 | `internal/platform/config.go` |
| `REDIS_ADDR` = nome do serviço `redis` | 5 | `docker-compose.yml:133-145` |
| p95 sobe de 2,04 s para 8,01 s só com 50 requisições em voo (funda a escolha de hospedagem) | 5 | `docs/evidencias/antes/loadgen-antes.txt:9-13` |
| Redis local sem `--save` nem `--appendonly` (funda RTO/RPO do cache) | 7 | `docker-compose.yml:136-138` |
| RNF-01 e RNF-02 também fundamentam o custo do modo degradado no cenário 7c | 3, 7c | `docs/evidencias/antes/loadgen-antes.txt:9-13` |

## Nota sobre a fonte anteriormente citada

O relatório completo de `make reproduce` (`docs/evidencias/antes/reproduce.txt`) contém o mesmo bloco de
saída do `loadgen` nas linhas 495-519 (baseline + load); `docs/evidencias/antes/loadgen-antes.txt:1-24` é
esse mesmo bloco isolado, e é a fonte usada nas linhas numeradas acima porque a numeração é estável e não
depende do restante do log de `make reproduce`.
