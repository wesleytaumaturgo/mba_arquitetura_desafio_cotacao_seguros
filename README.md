# Cotação de seguros: resiliência contra parceiras instáveis

Este é o README do processo, exigido pelo enunciado ao final da entrega (`docs/enunciado.md`, "O README
do processo"). Ele não substitui o enunciado original, que está em `docs/enunciado.md`.

- Documento de arquitetura (SAD): [`docs/sad.md`](docs/sad.md)
- Evidências de antes da entrega: [`docs/evidencias/antes/`](docs/evidencias/antes/)
- Evidências de depois da entrega: [`docs/evidencias/depois/`](docs/evidencias/depois/)

| Métrica | Antes | Depois |
|---|---|---|
| p95, carga de load (200 req, 50 em voo) | 8,01 s | 204 ms |
| Sucesso, carga de load | 60% (120/200) | 100% (200/200) |
| Hit rate do cache ao final da janela | 0% (sem cliente Redis) | ~100% |

Comparação completa, com origem de cada número: [`docs/evidencias/depois/comparacao.md`](docs/evidencias/depois/comparacao.md)

## Como subir o ambiente e reproduzir esta versão

Igual ao passo a passo de `docs/enunciado.md`, seção "Comece por aqui":

```bash
make up     # docker compose up -d --build
make ps     # lista os oito serviços do ambiente e o estado de cada um
```

Para reproduzir a execução completa desta versão (o mesmo comando usado para gerar as evidências de
"depois"):

```bash
make down && make reproduce
```

## O que foi implementado vs. o que ficou como proposta

**Implementado nesta fatia:**

- **Circuit breaker por parceira** (`internal/resilience/breaker.go`, `gobreaker/v2`): 5 falhas
  consecutivas abrem o breaker, que fica aberto por 5 s e depois libera 2 sondas em half-open.
- **Cache por parceira** (`internal/cache/quote_cache.go`, Redis, TTL de 900 s): cada cotação de cada
  parceira é armazenada em uma chave própria (ver "A chave de cache", abaixo).
- **Fallback parcial** (decisão 3 do SAD, cache-aside): o cache é consultado **antes** de qualquer chamada
  ao vivo; um acerto dispensa a parceira (`source: "cache"`, não é degradação, decisão 2 do SAD), e só em
  cache miss a chamada ao vivo é tentada. Quando essa chamada ao vivo também falha, a parceira entra em
  `missing_partners` em vez de derrubar a cotação inteira. A resposta volta com `degraded: true` e a lista
  de `missing_partners` quando alguma parceira não produz prêmio nem ao vivo nem em cache.

Os dois testes determinísticos exigidos pelo enunciado (um do breaker abrindo, um do cache/fallback) são
`TestBreakerOpensAfterFiveConsecutiveFailures` (`internal/resilience/breaker_test.go`) e
`TestPartialResponseWithCacheAndFallback` (`internal/quotation/service_test.go:369`); ambos rodam com
`make test` (`go test ./...`), evidência em
[`docs/evidencias/depois/make-test.txt`](docs/evidencias/depois/make-test.txt).

**Ficou como proposta, não implementado nesta fatia** (detalhado em `docs/sad.md`, seção "Os quatro
limites conhecidos"):

1. Limite de chamadas simultâneas por parceira (não há; uma onda de requisições pode saturar uma
   parceira antes que o breaker veja falhas suficientes para abrir).
2. Breaker não distribuído entre réplicas (vive na memória do processo; cada réplica aprende sozinha que
   uma parceira caiu).
3. TTL como única forma de invalidação do cache (sem invalidação ativa quando uma parceira corrige um
   preço fora de banda).
4. Sem isolamento de capacidade por tenant (uma corretora de alto volume pode consumir a capacidade das
   parceiras compartilhadas por todas as outras).

Também ficou como proposta, como item de bônus do enunciado: a **paralelização da agregação** entre as
três parceiras.

## Métricas e atributos criados

- Gauge `partner_breaker_state{partner}` (`internal/platform/telemetry.go`): `0` fechado, `1` meio
  aberto, `2` aberto.
- Contador `quotation_cache_result_total{result="hit"|"miss"|"redis_error"}`
  (`internal/cache/quote_cache.go`): `redis_error` distingue falha de conexão de um miss real.
- Span `partner.quote` (`internal/quotation/service.go`), com o atributo `partner.result`, que pode ser
  `cache_hit`, `live_success`, `circuit_open`, `too_many_requests`, `timeout` ou `http_error`.

## O que seria diferente com mais tempo

O primeiro candidato é a **paralelização da agregação** entre as três parceiras (item de bônus do
enunciado, não implementado nesta fatia): no cenário sem cache (500 cotações distintas, pior caso), o p95
medido foi **3,68 s**, acima do alvo de RNF-01 (≤ 3 s), porque o laço de consulta às três parceiras em
`internal/quotation/service.go` continua sequencial por decisão explícita desta entrega (ver
[`docs/evidencias/depois/comparacao.md`](docs/evidencias/depois/comparacao.md), "Limitação explícita").
Paralelizar essa chamada é o caminho mais direto para trazer esse pior caso para dentro da meta.

Depois vêm os próprios quatro limites conhecidos listados acima (não itens novos inventados para a
ocasião): limite de concorrência por parceira, breaker distribuído entre réplicas, invalidação de cache
além do TTL, e isolamento de capacidade por tenant.

Uma lição de teste também ficaria diferente com mais tempo: `TestBreakerHalfOpenProbesReopenAndClose`
(`internal/resilience/breaker_test.go`) usa `time.Sleep` porque `gobreaker/v2` não expõe um relógio
injetável para atravessar o `OpenTimeout` e liberar a sonda de meio-aberto — uma dependência de tempo real
aceita só por ser documentada no próprio teste, isolada em uma função separada do teste determinístico, e
por ter a menor duração que não flutua. Com mais tempo, valeria investigar um wrapper de relógio injetável
para eliminar até essa exceção.

## A chave de cache, por extenso

```
quote:v1:<tenant_id>:<partner_name>:<hash>
```

onde `<hash>` são os 16 primeiros caracteres hexadecimais do SHA-256 de `documento normalizado | ano de
nascimento | placa normalizada | modelo | ano do veículo | valor em centavos | cobertura`. O `tenant_id`
está presente na chave sem exceção: é o componente que o enunciado marca como reprovação automática se
faltar ("Chave de cache sem `tenant_id` reprova").

## O coração da solução

O que esta entrega protege são as três parceiras instáveis (`partner-slow`, `partner-flaky`,
`partner-degrading`) contra o efeito cascata de suas próprias falhas sobre a corretora: um circuit
breaker por parceira evita insistir em chamar quem já está falhando, um cache por parceira responde sem
sequer chamar a parceira enquanto a cotação em cache ainda é recente (dentro do TTL), e um fallback parcial
devolve o que deu certo em vez de derrubar a cotação inteira por causa de uma parceira só. Isso custou uma mudança no
contrato de resposta da API (campos `source`, `age_seconds`, `degraded` e `missing_partners`, novos),
3x mais chaves no Redis do que a alternativa de cache agregado por cotação, e um breaker que vive na
memória de cada réplica do processo, não compartilhado entre elas.

## Como o trabalho foi feito

O processo seguiu uma cascata de documentos, cada um fonte de verdade do seguinte: primeiro o
[SAD](docs/sad.md) (decisões de arquitetura), depois o FDD (`docs/fdd-resiliencia-parceiras.md`, detalhe de
implementação), depois o plano de tasks (`docs/plano-resiliencia-parceiras.md`), executado task a task pelo
comando `/implement-plan`, com um laço implementador ↔ revisor por task (no máximo três rodadas antes de
escalar para decisão humana). Extratos derivados de cada documento longo (chaves, contratos, decisões,
rastreabilidade) ficam em [`docs/work/`](docs/work/), para que quem consome um documento não precise reler
