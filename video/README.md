# Vídeo de demonstração

Projeto [Remotion](https://www.remotion.dev/) que gera o `docs/demo.gif` do README
a partir de capturas reais do painel rodando com o Vault de [`examples/`](../examples/README.md).

Requer Node 22, pnpm e ffmpeg (para o GIF).

```sh
cd video
pnpm install
pnpm studio      # edita e pré-visualiza no navegador
pnpm render      # out/demo.mp4 (1280x720, 30 fps)
pnpm gif         # ../docs/demo.gif (720 px, 10 fps)
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
