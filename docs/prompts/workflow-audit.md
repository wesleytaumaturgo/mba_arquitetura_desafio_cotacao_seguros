# Auditoria do workflow de desenvolvimento com IA

Você é um auditor externo. Você NÃO conhece as intenções de quem montou este workflow; só o que está nos arquivos. Toda afirmação sua precisa de evidência citável (arquivo:linha, hash de commit, trecho de documento). Afirmação sem evidência não entra no relatório.

## Fontes (leia todas antes de escrever)

1. `CLAUDE.md`, `AGENTS.md`, `.claude/rules/*.md`, `.claude/settings.json`
2. Peças autorais: `.claude/skills/domain-context/`, `.claude/commands/implement-task.md`, `.claude/commands/implement-plan.md`, `.claude/commands/reviewer-mutation-check.md`, `.claude/agents/task-implementer.md`, `.claude/agents/task-reviewer.md`
3. Skills de documentação usadas: `.claude/skills/hld-creator/SKILL.md`, `fdd-creator/SKILL.md`, `implementation-plan-creator/SKILL.md` (só o bloco final "Passo final obrigatório" e "Regra adicional", que são autorais)
4. Artefatos produzidos: `docs/sad.md`, `docs/fdd-resiliencia-parceiras.md`, `docs/plano-resiliencia-parceiras.md`, `docs/work/**`, `docs/evidencias/antes/**`, `docs/workflow-metrics.md`
5. Histórico: `git log --oneline --stat` (todos os commits com `Tnn` na mensagem) e o código deles: `internal/resilience/`, `internal/cache/`, `internal/quotation/`, `internal/platform/config.go`
6. Enunciado e critérios: `docs/enunciado.md` (seções "Critérios de aceite", "O que reprova sozinho", "As evidências") e `docs/guia-sad.md`

Não leia nem cite: `cmd/partner-mock`, `cmd/loadgen`, skills de terceiros não listadas acima.

## Perguntas que o relatório responde

A. **Rastreabilidade**: dá para reconstruir a cadeia enunciado → SAD → FDD → plano → task → commit → teste para 3 requisitos escolhidos por você (um funcional, um não funcional, uma restrição)? Onde ela quebra?
B. **Portões**: quais verificações são mecânicas (comando, teste, grep) e quais são prosa que depende de o modelo obedecer? Liste cada regra de `task-reviewer.md` e classifique.
C. **Escapes**: compare os critérios de aceite do FDD (seção 9) com os testes commitados. O que o FDD exige e nenhum teste prova? O que o revisor aprovou que viola um critério? (Confira `time.Sleep` em `*_test.go`, dados pessoais em chave/valor/log, status HTTP, ordenação.)
D. **Escopo**: alguma task tocou arquivo fora da sua lista no plano? Alguma dependência ficou fora de sintonia com o código (`go.mod` vs imports)?
E. **Custo**: pelo `git log` (timestamps) e por `docs/workflow-metrics.md`, quanto tempo por task e por documento; onde o tempo humano foi gasto em ferramenta e não em decisão?
F. **Riscos do próprio workflow**: o que acontece se (1) o revisor aprovar cedo demais, (2) o plano tiver uma task ambígua, (3) o orquestrador perder o contexto no meio, (4) o humano aceitar toda parada sem ler? Para cada um: existe mitigação hoje? Onde?
G. **Comparação com os repositórios de referência** (só se os conhecer): o que o VideoMax (`implement-and-evaluate`), o greenfield (`plan-*` + readers) e os labs (`granularidade`, `eval-specs`) fazem que este workflow não faz, e vice-versa.

## Formato de saída (obrigatório, para permitir comparação entre auditores)

```
# Auditoria do workflow — <modelo/ferramenta> — <data>

## Pontos fortes
| # | Ponto | Evidência (arquivo:linha / commit) | Impacto (alto/médio/baixo) |

## Pontos fracos
| # | Ponto | Evidência | Risco (alto/médio/baixo) | Já mitigado? (sim/parcial/não, onde) |

## Escapes encontrados (pergunta C)
| # | Critério (FDD §9 ou enunciado) | O que falta / o que viola | Evidência |

## Melhorias propostas
| # | Melhoria | Mudança concreta (arquivo e o que escrever) | Custo (h) | Retorno |

## Respostas A–G (uma seção curta por letra, com evidência)

## O que eu NÃO consegui verificar e por quê
```

Regras: no máximo 10 itens por tabela; ordene por impacto/risco; sem adjetivos sem evidência; se dois itens são o mesmo problema, junte. Você não edita nenhum arquivo: só escreve o relatório em `docs/workflow-audit/<ferramenta>-<data>.md`.
