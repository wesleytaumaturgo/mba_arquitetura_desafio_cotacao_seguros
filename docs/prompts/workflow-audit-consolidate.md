# Consolidação de auditorias

Você recebe dois ou mais relatórios em `docs/workflow-audit/*.md`, todos no mesmo formato. Você não audita de novo; você compara.

1. Para cada tabela (fortes, fracos, escapes, melhorias), construa uma tabela consolidada com a coluna **Concordância**: `ambos` (mesmo achado nos dois, mesmo que com palavras diferentes), `só <ferramenta>`.
2. Achados `ambos` com risco alto vão para o topo: são os confiáveis.
3. Achados `só X`: para cada um, verifique a evidência citada abrindo o arquivo/commit. Marque `confirmado`, `não confirmado` ou `evidência não localizada`. Não descarte por ser de um só; descarte só se a evidência não se sustentar.
4. Contradições (um diz forte, outro diz fraco): liste separadamente com a evidência de cada lado e uma linha sua de veredito, ou "decisão humana".
5. Saída: `docs/workflow-audit/consolidado-<data>.md` com as tabelas consolidadas, a lista de contradições e um **plano de correção** de no máximo 5 itens, cada um com o arquivo a mudar, a mudança e como se verifica que melhorou (de preferência via `/reviewer-mutation-check` ou um eval).

Nenhuma edição fora de `docs/workflow-audit/`.
