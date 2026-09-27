> Derivado de docs/sad.md (seção 4 e 5). Fonte de verdade é o original; em conflito, o original vence.

# Decisões do SAD

## Decisão 1: circuit breaker (seção 4)

- **Escopo:** 1 breaker por parceira (3 no total).
- **Biblioteca:** `github.com/sony/gobreaker/v2` (proposta, ainda não em `go.mod`).
- **Timeout do `http.Client`:** 2000 ms (`internal/partner/client.go`, campo hoje ausente; `NewClient`
  cria `&http.Client{Transport: platform.InstrumentTransport(...)}` sem `Timeout`, linhas 23-28).
- **Timeout conta como falha para o breaker:** sim.
- **Falhas consecutivas para abrir (`ReadyToTrip`):** 5.
- **Tempo de circuito aberto antes de meio aberto:** 5 s.
- **Chamadas em meio aberto / o que fecha:** 2 (`MaxRequests: 2`); as duas precisam ter sucesso para
  fechar, qualquer falha reabre imediatamente.
- **Alternativas descartadas (uma linha cada):** breaker global único descartado por derrubar as duas
  parceiras saudáveis junto com a instável; breaker por (parceira, corretora) descartado por multiplicar
  estado por tenant sem ganho, já que a instabilidade de uma parceira não depende de qual corretora pergunta.
- **Limites conhecidos:** estado vive na memória do processo, não atravessa réplicas (limite 2, seção 4);
  timeout de 2000 ms pode, em rede real mais lenta que o mock, cortar uma resposta legítima de
  `partner-slow` e contá-la como falha.

## Decisão 2: cache (seção 4)

- **Escopo da chave:** por parceira, `quote:v1:<tenant_id>:<partner_name>:<hash>`.
- **TTL:** 900 s (15 min).
- **Invalidação:** só o TTL (`EXPIRE` nativo do Redis); sem invalidação ativa, sem endpoint de purga.
- **Ordem cache × breaker:** cache-aside, cache é consultado antes de qualquer decisão do breaker; acerto
  de cache não é degradação.
- **Falha do Redis:** best effort, contada como miss, não aborta a requisição.
- **Alternativa descartada (uma linha):** cache agregado por (corretora, risco), cobrindo as três
  parceiras numa única chave, descartado porque invalida o cache inteiro se qualquer uma das três
  parceiras estiver fora, o que não compõe com o fallback parcial (decisão 3).
- **Limites conhecidos:** TTL como única forma de invalidação (limite 3, seção 4) — uma parceira pode
  corrigir um preço fora de banda e a corretora ainda vê o preço antigo por até 15 min; 3x mais chaves e
  round-trips ao Redis por cotação do que a alternativa agregada.

## Decisão 3: fallback (seção 4)

- **Estratégia primária:** resposta parcial (parceiras que responderam, ao vivo ou de cache, com
  `degraded`/`missing_partners` quando falta alguma).
- **Estratégia para "cotação anterior":** já coberta pelo cache-aside da decisão 2, não é uma segunda
  tentativa após falha.
- **Caso degenerado (nenhuma parceira produz prêmio):** `503 {"error":"no partner quote available", ...}`,
  substitui o `502 {"error":"partner insurer unavailable", ...}` de hoje para falha de parceira única.
- **Alternativas descartadas (uma linha cada):** recusa explícita como estratégia única, descartada por
  desperdiçar cotações que já tiveram sucesso; cotação anterior "pura" como estratégia primária,
  descartada por reduzir a atualidade do preço sem necessidade quando a parceira está saudável.
- **Limites conhecidos:** contrato de resposta muda (quebra consumidor que assume sempre 3 `quotes`);
  resposta com uma parceira só reduz o poder de comparação da corretora.

## Decisão de hospedagem (seção 5)

- **Escolha:** cloud pública, região única no Brasil para app e cache; armazenamento de auditoria em modo
  WORM replicado entre duas regiões dentro do Brasil.
- **Alternativas descartadas (uma linha cada):** on-premise descartado por exigir capacidade própria para
  absorver picos sem ganho de compliance adicional; híbrido (app/cache em cloud, auditoria on-premise)
  descartado por dobrar a superfície operacional sem exigência regulatória que force isso.
- **Limites conhecidos:** região única para a aplicação = indisponibilidade total em caso de perda de
  região (RTO de horas, cenário 7c); vendor lock-in da aplicação, mitigado parcialmente pela portabilidade
  do formato WORM da auditoria.

## Os quatro limites conhecidos (seção 4, não implementados)

1. Sem limite de chamadas simultâneas por parceira.
2. Breaker em memória do processo, não distribuído entre réplicas.
3. TTL como única forma de invalidação do cache.
4. Sem isolamento de capacidade por tenant (uma corretora pode consumir a capacidade das outras).
