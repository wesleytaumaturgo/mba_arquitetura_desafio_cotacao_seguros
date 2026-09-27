# Comparação antes/depois

> Evidência de fechamento da T11 (`docs/plano-resiliencia-parceiras.md`), citando os artefatos brutos em
> `docs/evidencias/antes/` e `docs/evidencias/depois/`. Cada número abaixo tem o arquivo de origem entre
> parênteses; nenhum valor foi recalculado ou arredondado além do que os próprios arquivos já mostram.

## Três execuções medidas

1. **Antes** — baseline pré-resiliência, carga padrão (5 cotações distintas) — `docs/evidencias/antes/loadgen-antes.txt`.
2. **Depois, carga padrão** — mesma carga (5 cotações distintas, o suficiente para o cache saturar rápido) — `docs/evidencias/depois/loadgen-depois.txt`.
3. **Depois, sem cache** — 500 cotações distintas (`-distinct 500`), para medir o pior caso quando o cache quase não acerta — `docs/evidencias/depois/loadgen-depois-sem-cache.txt`.

## Tabela comparativa

| Métrica | Antes | Depois (carga padrão) | Depois (sem cache, 500 distintas) |
|---|---|---|---|
| Sucesso, baseline (10 req, 1 em voo) | 5 de 10 (50%) (`loadgen-antes.txt:4`) | 10 de 10 (100%) (`loadgen-depois.txt:4`) | 10 de 10 (100%) (`loadgen-depois-sem-cache.txt:198`) |
| Sucesso, load (200 req, 50 em voo) | 120 de 200 (60%) (`loadgen-antes.txt:10`) | 200 de 200 (100%) (`loadgen-depois.txt:9`) | 200 de 200 (100%) (`loadgen-depois-sem-cache.txt:203`) |
| p95, baseline | 2,04 s (`loadgen-antes.txt:5`) | 2,04 s (`loadgen-depois.txt:5`) | 2,01 s (`loadgen-depois-sem-cache.txt:199`) |
| p95, load | **8,01 s** (`loadgen-antes.txt:11`) | **204 ms** (`loadgen-depois.txt:10`) | **3,68 s** (`loadgen-depois-sem-cache.txt:204`) |
| Vazão, load | 8,0 req/s (`loadgen-antes.txt:12`) | 744,2 req/s (`loadgen-depois.txt:11`) | 22,9 req/s (`loadgen-depois-sem-cache.txt:205`) |
| Falhas | `HTTP 502 from partner-flaky`: 5 (baseline) + 80 (load) (`loadgen-antes.txt:7,13`) | nenhuma | nenhuma |
| Hit rate ao final da janela | 0% por construção (sem cliente Redis) | ~100% (últimos pontos não-`NaN` da série) (`prometheus-cache-hit-rate.json`) | ~6,5% (dilatado pelas 500 cotações distintas na mesma janela combinada) (`prometheus-cache-hit-rate-duas-cargas.json`) |
| Estado do breaker `partner-flaky` observado | não instrumentado nesta versão | fechado(0) → aberto(2) → meio-aberto(1) → aberto(2) de novo, na mesma captura de 30 min que cobre as duas cargas (`prometheus-breaker-state.json`) | mesma captura combinada acima |

## Prova de que o breaker e o cache evitam trabalho de verdade (traces reais, não inferência)

- **Breaker aberto** (`jaeger-trace-breaker-aberto.json`): requisição de 1,613 s no total; a parceira ao
  vivo (`partner.result=live_success`) sozinha consome 1,613 s, enquanto as duas parceiras com
  `partner.result=circuit_open` levam **~222 µs cada** — não fazem chamada HTTP de saída, confirmado pela
  ausência de spans `HTTP POST` associados a elas (só a parceira `live_success` tem um span `HTTP POST` de
  1,612 s). Nenhum trace do depois mostra `502` para uma única parceira fora — a busca por `502` nos
  artefatos desta pasta só encontra falsos positivos numéricos (ex.: um valor de latência `4.183...` ou um
  tamanho de payload `50272`), nunca um status HTTP.
- **Cache hit** (`jaeger-trace-cache.json`): requisição de 5,981 ms no total; as três parceiras têm
  `partner.result=cache_hit` e levam entre 736 µs e 3,257 µs cada — nenhuma chamada HTTP de saída para
  nenhuma parceira.

## RNF por RNF (metas de `docs/sad.md`, seção 3)

| RNF | Meta | Antes | Depois (carga padrão) | Depois (sem cache) | Atingido? |
|---|---|---|---|---|---|
| RNF-01 — p95 de `POST /quotes` | ≤ 3 s | 8,01 s (`loadgen-antes.txt:11`) | 204 ms (`loadgen-depois.txt:10`) | 3,68 s (`loadgen-depois-sem-cache.txt:204`) | **Sim** na carga padrão (39x melhor que a meta); **não** no cenário sem cache — ver limitação abaixo |
| RNF-02 — taxa de respostas 2xx | ≥ 99,5% | 60% (`loadgen-antes.txt:10`) | 100% (`loadgen-depois.txt:9`) | 100% (`loadgen-depois-sem-cache.txt:203`) | **Sim**, nos dois cenários do depois |
| RNF-03 — hit rate do cache ao final da carga padrão | ≥ 90% | 0%, por construção (sem cliente Redis) | ~100% ao final da janela (`prometheus-cache-hit-rate.json`) | ~6,5% (`prometheus-cache-hit-rate-duas-cargas.json`) — não é o cenário que o RNF mede | **Sim**, no cenário que o RNF-03 de fato descreve (carga padrão, 5 cotações distintas); o cenário sem cache é deliberadamente fora da definição do RNF, existe só para expor o pior caso |

## Limitação explícita: p95 de 3,68 s sem cache fica acima do alvo de 3 s

No cenário sem cache (500 cotações distintas, onde o cache quase não acerta e a maior parte das chamadas
é ao vivo), o p95 medido foi **3,68 s** (`loadgen-depois-sem-cache.txt:204`), acima da meta de RNF-01
(≤ 3 s, `docs/sad.md`, seção 3). A causa é conhecida e documentada desde o desenho, não uma regressão
desta implementação: o laço de consulta às três parceiras em `internal/quotation/service.go` continua
**sequencial**, por decisão explícita (suposição S1 do FDD, `docs/fdd-resiliencia-parceiras.md`: "o laço
de consulta às três parceiras continua sequencial (como hoje), não paralelo; nada nesta entrega pede
paralelismo, e introduzi-lo mudaria o comportamento de concorrência simultânea contra `partner-degrading`
de forma não solicitada"). A paralelização da agregação é o item de bônus do enunciado, explicitamente
fora do escopo desta entrega (`docs/sad.md`, seção 1: "Não cobre a paralelização da agregação (item de
bônus do enunciado, não implementado nesta entrega e citado apenas como trabalho futuro"). Sem cache para
absorver repetições, o pior caso por requisição soma, em série, o tempo de `partner-slow`, o
timeout/breaker de `partner-degrading` e a folga de `partner-flaky` — o que a carga padrão do RNF-01
(5 cotações distintas, cache satura em poucas requisições) mascara.
