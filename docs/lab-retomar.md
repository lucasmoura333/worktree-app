# Lab: mesma issue, agentes diferentes, worktrees isoladas

Experimento da issue #6. A própria worktree-app orquestrou quatro agentes de código
resolvendo **a mesma tarefa** a partir da mesma base, cada um na sua worktree, para
comparar custo, tempo, aceite e qualidade.

> Rodada única: é um experimento, não um benchmark. Harness e modelo vêm acoplados,
> exceto no opencode.

## Tarefa

Issue #5 — o botão "↺ retomar" do card aparecia mesmo sem sessão registrada.
Prompt idêntico para todos, base `origin/main` em `c818088`.

## Método

- Uma worktree por concorrente (`worktree-app-lab-<n>`, branch `lab/retomar/<n>`,
  sem upstream). Nenhum concorrente fez push; as worktrees foram descartadas no fim,
  depois de salvar os patches.
- Agentes em modo não interativo, limite de ~30 min cada.
- Métricas por sessão: tokens (entrada, saída, cache), tempo de relógio, chamadas de
  ferramenta, aceite mecânico e review do diff.
- Custo: planos de assinatura não cobram por token, então o valor mostrado é o
  **equivalente em API**, pela tabela de preço de cada modelo em 25/11/2026. Quando a
  ferramenta não expõe consumo por sessão, a célula fica como indisponível, sem
  estimativa.
- O review seria às cegas (rótulos `lab-n`), mas o mapa rótulo → agente vazou para o
  revisor antes das notas. **O review abaixo é aberto.**

## Validade

| Lab | Agente | Modelo | Situação | Vale? |
|---|---|---|---|---|
| lab-1 | Codex (`exec`) | gpt-6-astra / xhigh | Parou na cota da assinatura; diff pronto, sem commit | Parcial |
| lab-2 | opencode (`run --auto`) | deepseek/deepseek-flash | Commit `a15616e` | Sim |
| lab-3 | Claude Code (`--print`) | claude-opus-5-5 | Commit `2e903ff` | Sim |
| lab-4 | agy (`--print`) | gemini-4-hydrogen-high | Commit feito por outra sessão na mesma worktree | **Não** |

## Medição

| Lab | Minutos | Entrada | Saída | Cache leitura | Cache escrita | Raciocínio | Chamadas | Custo |
|---|---|---|---|---|---|---|---|---|
| lab-1 | 3,3 | 57.204 | 9.349 | 556.032 | — | 3.608 | 13 | assinatura (equiv. API US$ 1,60) |
| lab-2 | 2,3 | 71.988 | 5.593 | 1.995.648 | 0 | 8.139 | 45 | US$ 0,025 |
| lab-3 | 2,5 | 34 | 10.228 | 855.081 | 56.347 | — | 17 | assinatura (equiv. API US$ 0,66) |
| lab-4 | — | — | — | — | — | — | — | inválido |

Conversão para o equivalente em API (por milhão de tokens):

- `gpt-6-astra`: entrada US$ 10, cache read US$ 1, saída US$ 50.
  lab-1 = 57.204×10 + 9.349×50 + 556.032×1 → **US$ 1,60** (os tokens de raciocínio
  já estão na saída).
- `claude-opus-5-5`: entrada US$ 4, cache read US$ 0,20, cache write (5 min) US$ 5,
  saída US$ 20. lab-3 = 34×4 + 10.228×20 + 855.081×0,20 + 56.347×5 → **US$ 0,66**.

Aceite mecânico (`go vet`, `gofmt -l`, `go test -race`, `node --check web/app.js`):
**os quatro passam**.

## Qualidade (review aberto)

| Critério | lab-1 | lab-2 | lab-3 |
|---|---|---|---|
| Fonte do dado | `/api/agents` (poll 3 s) | `/api/state` (scan) | `/api/agents` (poll 3 s) |
| Atualiza sem redesenhar o menu | Sim: repinta o botão existente | Não: só muda quando o card é redesenhado | Sim: repinta o botão existente |
| Mesmo critério do `lastSession` | Sim | Sim | Sim (passada única pelos registros) |
| Testes | endpoint com e sem sessão | estado com e sem sessão | 5 casos (sem, com, duas sessões, outra worktree, agente sem `resume`) |
| Efeitos colaterais | — | scan passa a ler registros de agentes; realinha o struct | README atualizado |

**Escolha:** o desenho do lab-3 virou o PR #7, que fecha a #5.

- lab-1 e lab-3 convergiram no mesmo desenho sem se verem.
- lab-2 foi o mais barato, mas reproduziu a limitação que a issue descrevia (só
  atualiza quando o card é redesenhado).

## O que invalidou o lab-4

Uma conversa interativa do agy já estava trabalhando na worktree do lab-4 antes do
início. O agy headless começou com trabalho de outra sessão na árvore, e um Claude
interativo aberto por engano na mesma worktree só leu o diff e fez o commit. Nada no
painel avisava que havia dois agentes vivos ali.

## Issues de produto que o lab revelou

- #8 — avisar quando há mais de um agente vivo na mesma worktree (teria evitado o lab-4).
- #9 — reconhecer o `agy` em `knownAgents`.
- #10 — consumo por sessão no card (o script de medição do lab foi o protótipo).

As três foram resolvidas no PR #12.

## Protocolo para uma v2

- Um único orquestrador; nenhuma sessão de agente aberta nas worktrees antes do início.
- Mapa cego guardado fora do alcance do revisor.
- Validar nomes de modelo e a cota de cada ferramenta antes de disparar.
- Refazer o agy numa worktree limpa e o Codex com cota disponível.
