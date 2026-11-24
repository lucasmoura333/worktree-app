# Vídeo de demonstração

Projeto [Remotion](https://www.remotion.dev/) que gera o `docs/demo.gif` do README
a partir de capturas reais do painel rodando com o Vault de [`examples/`](../examples/README.md).

Requer Node 22, pnpm e ffmpeg (para o GIF).

```sh
cd video
pnpm install
pnpm studio      # edita e pré-visualiza no navegador
pnpm render      # out/demo.mp4 (1280x720, 30 fps)
pnpm gif         # ../docs/demo.gif (680 px, 8 fps)
```

Se o Remotion não achar um Chrome, passe `--browser-executable=/caminho/do/chrome`
no `render`.

## Regravar as telas

As imagens ficam em `public/` (2560x1440, viewport 1280x720 com escala 2).
Para refazer, suba o painel com `examples/config.json` apontando para repos que
tenham as worktrees `checkout-novo` (repo `web`), `auditoria-login` (repo `api`)
e mais uma qualquer, e rode:

```sh
WT_URL=http://127.0.0.1:7777/ pnpm capture
```

O script troca o caminho do `root` por `~/projetos` nas capturas
(`WT_ROOT_LABEL` muda o texto). Sem `CHROME_PATH`, ele usa o Chromium do
Playwright (`npx playwright-core install chromium`). Navegador headless não tem
visualizador de PDF, por isso a prévia do PDF fica fechada no vídeo.

Cenas, legendas e enquadramento estão em `src/Demo.tsx` (`PARTS`).

## Central de agentes, terminal, Comparar e snapshots

As cenas 03 a 06 usam três scripts. Grave **só com repos de exemplo**: nada de
código real nem a sessão de um agente de verdade, porque a tela inicial de um
agente mostra conta e organização.

1. **Sandbox.** Suba o painel com um config próprio (`stateDir` numa pasta só
   do vídeo), com as worktrees `checkout-novo` (repo `web`) e `auditoria-login`
   (repo `api`) e alguma alteração não commitada na primeira. Crie um snapshot
   (`POST /api/snapshot` ou pelo menu do card).
2. **Agentes sem agente.** Um processo qualquer chamado `claude` (uma cópia do
   `sleep`) com cwd na worktree faz o card aparecer vivo. O estado vem do
   próprio hook:

   ```sh
   printf '{"session_id":"demo-1","cwd":"<worktree>","hook_event_name":"UserPromptSubmit"}' \
     | WT_STATE_DIR=<state> worktree-app hook --agent claude
   ```

   Rode o hook **fora** de uma sessão de agente (por exemplo com `setsid` e o
   pai já encerrado). Se não, ele registra o pid do agente que o chamou como
   dono da sessão.
3. **Capturas do painel:**

   ```sh
   WT_URL=http://127.0.0.1:7795/ WT_ROOT_REAL=<root> WT_STATE_REAL=<state> \
   WT_WAITING_CMD="<hook com Notification/permission_prompt>" pnpm capture:agents
   ```

   O script troca `WT_ROOT_REAL` e `WT_STATE_REAL` por `~/projetos` em todo
   texto visível, e o `capture.cjs` faz o mesmo. Ele nunca clica em `>_` nem
   em "▶ agente": só abre o menu.
4. **Terminal:** um tmux headless com um bash limpo na worktree de exemplo,
   capturado em ANSI e convertido com [rich](https://github.com/Textualize/rich):

   ```sh
   python3 scripts/terminal-frames.py <worktree> /tmp/term
   node scripts/capture-terminal.cjs /tmp/term   # public/term/*.png + frames.txt
   ```

   Os tempos de `frames.txt` entram em `TERM_HOLDS` no `src/Demo.tsx`.

Sem fonte de emoji no sistema, o Chrome headless desenha □ no lugar de 🔔, ✋ e
⑂. Aponte `FONTCONFIG_FILE` para um `fonts.conf` que inclua o do sistema e uma
pasta com Noto Color Emoji, Noto Sans Symbols 2 e Noto Sans JP, e use a mesma
variável na captura e no `pnpm render`.
