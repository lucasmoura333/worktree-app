# Hooks de agentes

O painel mostra, em cada worktree, se o Claude Code, o Codex ou o opencode estão
**trabalhando**, **esperando você** ou **concluídos**. Cada agente avisa o
worktree-app por um hook que roda o próprio binário:

```sh
wt-street-linux hook --agent claude|codex|opencode   # lê o JSON do evento no stdin
```

O subcomando não imprime nada, sempre sai com 0 e grava só
`{agent, session, cwd, state, since, ts, pid}` em
`$WT_STATE_DIR/agents` (padrão `~/.local/state/worktree-app/agents`, com arquivos 0600).
Prompt, transcript e saída de ferramenta não são gravados.

Cada um instala nos seus agentes. Os comandos abaixo supõem o binário em
`~/.local/bin/wt-street-linux`.

| arquivo | onde vai |
|---|---|
| `claude-settings.json` | mesclar em `~/.claude/settings.json` (chave `hooks`) |
| `codex-hooks.json` | mesclar em `~/.codex/hooks.json` e aprovar em `/hooks` no Codex |
| `worktree-status.js` | copiar para `~/.config/opencode/plugins/` |

Mesclar sem perder os hooks que você já tem (faça backup antes):

```sh
jq -s '.[0] * {hooks: (reduce (.[1].hooks | to_entries[]) as $e (.[0].hooks // {};
  .[$e.key] = ((.[$e.key] // []) + $e.value)))}' \
  ~/.claude/settings.json examples/hooks/claude-settings.json > /tmp/settings.json \
  && mv /tmp/settings.json ~/.claude/settings.json
```

Sem hook, o painel ainda acha os processos dos agentes em `/proc` (Linux/WSL) e
mostra "ativo (sem hook)", sem saber se está esperando você.

| evento | Claude | Codex | opencode |
|---|---|---|---|
| trabalhando | UserPromptSubmit, PostToolUse | UserPromptSubmit, PostToolUse | `session.status` busy |
| esperando você | Notification (permissão/pergunta) | PermissionRequest | `permission.asked`, `question.asked` |
| concluído | Stop, Notification `idle_prompt` | Stop | `session.idle` |
| encerrado | SessionEnd | SessionEnd | `session.deleted` |
