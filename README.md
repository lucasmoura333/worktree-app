<h1 align="center">worktree-app</h1>

<p align="center">
  <b>Branch out. Stay connected.</b><br>
  Várias tarefas em paralelo, uma tela para criar, acompanhar e remover git worktrees.
</p>

<p align="center">
  <img src="docs/demo.gif" width="720" alt="Demonstração do worktree-app: mural de worktrees, criação, savepoints, leitor de notas com anexos, grafo e modelos visuais">
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/licen%C3%A7a-MIT-d8ef5b?labelColor=20211f" alt="Licença MIT"></a>
  <img src="https://img.shields.io/badge/Go-1.25%2B-2548dc?labelColor=20211f" alt="Go 1.25+">
  <img src="https://img.shields.io/badge/s%C3%B3-127.0.0.1-ff5a2c?labelColor=20211f" alt="Só 127.0.0.1">
  <img src="https://img.shields.io/badge/Windows%20%C2%B7%20Linux%20%C2%B7%20WSL-ok-efebe1?labelColor=20211f" alt="Windows, Linux e WSL">
</p>

<p align="center">
  <a href="https://lucasmoura333.github.io/worktree-app/"><b>Prévia online</b></a> ·
  <a href="#começar">Começar</a> ·
  <a href="examples/README.md">Vault de exemplo</a> ·
  <a href="video/README.md">Como o vídeo é gerado</a> ·
  <a href="README.en.md">English</a>
</p>

---

Painel local para criar, acompanhar e remover **git worktrees** de vários
repositórios ao mesmo tempo, com notas opcionais no Obsidian.

É um binário Go com a página embutida (`go:embed`), sem dependências externas,
que escuta só em `127.0.0.1`. Funciona em Windows, Linux e WSL.

> Interface e documentação em português (BR). As telas do vídeo usam o Vault de
> [`examples/`](examples/README.md) e são animadas com Remotion em [`video/`](video/README.md).

## Por que

Quem toca várias tarefas em paralelo acaba com uma worktree por tarefa em cada
repo: `.env` para copiar, `node_modules` para instalar, porta do Docker para não
colidir, branch para lembrar de apagar. O painel faz esses passos de uma vez,
mostra o estado de tudo numa tela e só remove o que está seguro.

## Começar

Requer Go 1.25+ e `git` no PATH.

```sh
git clone https://github.com/lucasmoura333/worktree-app
cd worktree-app
cp config.example.json config.json   # ajuste root, repos e (opcional) vault
go build -o worktree-app .
./worktree-app                       # abre o navegador; Ctrl+C encerra
```

Opções:

| flag | uso |
|---|---|
| `-config caminho` | config a usar (padrão: `config.json` ao lado do executável) |
| `-no-open` | não abre o navegador |
| `-demo` | prévia com dados de exemplo em `127.0.0.1:7788`; não lê nem altera nada |
| `-export-static pasta` | gera a prévia como site estático (mural, mapa e leitor com o Vault de `examples/`) para o GitHub Pages; `-export-vault` troca o Vault de exemplo |

Rodar de novo com o app aberto só abre o navegador. Se for usar "instalar
dependências", abra pelo terminal em que `yarn`/`npm`/`composer` etc. estão no
PATH: a instalação herda esse ambiente.

### Windows + WSL

Para repos que vivem no WSL, compile o binário Linux e abra pelo PowerShell:

```powershell
go build -buildvcs=false -o wt-street.exe .
$env:GOOS = 'linux'; $env:GOARCH = 'amd64'
go build -buildvcs=false -o wt-street-linux .
Remove-Item Env:GOOS, Env:GOARCH

.\start-design.ps1 -Distro Ubuntu-24.04 -Config config.wsl.json
.\start-design.ps1 -Preview          # prévia com dados de exemplo
```

O editor usa o `code` do WSL e "Abrir pasta" converte o caminho com
`wslpath -w` para o Explorer. Em pastas `/mnt/c`, o build pede
`-buildvcs=false`.

### Testes

```sh
go test ./...    # usa repositórios git temporários, nunca os reais
```

## O que "Criar" faz, por repo marcado (em paralelo)

1. `git fetch origin --prune` (opcional).
2. `git worktree add` na pasta do `folderPattern`:
   - **nova branch**: `branchPattern` a partir de `base` (ex.: `origin/dev`), sem upstream;
   - **branch existente**: local, ou `origin/x` com tracking (para review).
3. Copia do main os arquivos não versionados listados em `copy` (`.env` e afins;
   só o nome vai para o log, nunca o conteúdo).
4. Com `dockerEnv`, troca o nome do projeto do compose para `<o do main>-<feature>`
   e a porta para a primeira livre da faixa.
5. Dependências (`node_modules`, `vendor`...) nunca são copiadas: a worktree nasce
   sem elas. Com "instalar dependências" marcado, roda o `install` de cada uma.

Depois cria `tasks/<feature>/README.md` e `<feature>.code-workspace` (settings
copiados do `workspaceTemplate`), só se ainda não existirem. Caminhos dentro do
repo principal são recusados, para a worktree não virar arquivo não versionado
do main.

## Painel

Agrupa as worktrees por feature e mostra, para cada uma, alterações não
commitadas, commits fora de qualquer remoto, ↑/↓ do upstream, arquivos e deps
faltando, e deps que são link para outra pasta.

- **Copiar .env etc.**: refaz os passos 3–4 numa worktree em que falta algum arquivo.
- **Instalar deps**: roda o `install` das deps que faltam.
- **Remover**: confere tudo antes e só remove se nenhuma worktree tiver alteração
  ou commit local; senão exige "forçar". Deps que são link/junction saem antes da
  pasta, então o original nunca é tocado (e não contam como alteração). Apagar a
  branch local é opcional: `git branch -d`, ou `-D` com "forçar"; o hash fica no
  log para recriar com `git branch <nome> <hash>`. A branch do remoto nunca é
  tocada, e `tasks/` nunca é apagada.
- **Registros quebrados**: worktrees cuja pasta sumiu, com botão de `git worktree prune`.
- **Agentes**: a barra do topo conta quantos agentes (Claude Code, Codex, opencode,
  agy) estão trabalhando, esperando você ou concluídos, e cada card mostra o agente
  da sua pasta, o consumo da sessão (tokens de entrada/saída; custo quando a fonte
  informa) e um aviso **⚠ N vivos** quando há mais de uma sessão na mesma worktree.
  Atualiza a cada 3 s pelo `/api/agents`, que não roda `git status` nem toca o Vault.
  "🔕 Avisar" liga uma notificação do navegador quando um agente passa a esperar você.
  Os estados vêm de hooks; sem hook, os processos achados em `/proc` aparecem como
  "ativo (sem hook)". Veja [`examples/hooks/`](examples/hooks/README.md).
- **Terminal e agentes** (WSL): `>_` abre uma aba do Windows Terminal na pasta da
  worktree; no menu `···`, "▶ agente (novo)" abre o agente ali e "↺ agente (retomar)"
  retoma a última sessão registrada nessa worktree (fica desabilitado, com a dica do
  motivo, enquanto não houver sessão daquele agente ali; o poll de `/api/agents` religa).
  Se já houver agente vivo na worktree, o "▶ novo" pede confirmação antes de abrir
  outra sessão na mesma árvore.
- **Comparar…**: diff só leitura da worktree contra a base do repo (inclui o que não
  foi commitado e lista os arquivos novos) ou contra outra worktree do mesmo repo
  (só commits). Arquivos do `copy` (`.env` e afins) e pastas de deps ficam de fora;
  o patch é cortado em `diffMaxKB`.
- **Snapshot agora / Snapshots…**: retrato local da worktree em
  `stateDir/snapshots/<repo>/<pasta>/<data-hora>/`, com `meta.json` (branch, HEAD,
  upstream, dirty, sessões de agente para retomar e o savepoint vinculado),
  `changes.patch`, `untracked.tar` e, se houver commits fora do remoto,
  `commits.bundle`. Segredos, o `copy` do repo e as deps nunca entram; os nomes
  omitidos ficam no `meta.json`. Restaurar exige a worktree limpa: valida o patch e o
  tar antes de aplicar, recusa sobrescrever arquivos e não escreve refs do bundle
  (só mostra o comando `git fetch`). A retenção guarda os 10 mais novos por pasta por
  30 dias; snapshots feitos antes de remover ficam 90 dias. Na remoção forçada com
  risco, o snapshot é criado antes de apagar qualquer coisa e, se ele falhar, nada é
  removido.

O app não faz commit, push nem nada que altere o remoto. A única conversa com o
remoto é `git fetch`.

Há quatro modelos visuais (Street-Art, Neo Brutalismo, Grunge e Frasurbane),
guardados no navegador, e as visões Cards, Mapa (grafo local das notas) e
Removidos. Busca e filtros ficam na URL.

## config.json

Aceita comentários `//`. Veja [`config.example.json`](config.example.json).

| campo | uso |
|---|---|
| `root` | pasta que contém os repositórios |
| `listen` | só `127.0.0.1`/`localhost` (o servidor executa git) |
| `folderPattern`, `branchPattern` | padrões com `{feature}` (e `{date}` na branch) |
| `dateFormat` | layout de data do Go para `{date}` (`020106` = ddmmaa) |
| `tasksDir`, `workspaceTemplate`, `editor` | pasta de tasks, modelo de `.code-workspace` e comando do editor |
| `vault` | opcional: notas no Obsidian (abaixo) |
| `reader` | opcional: leitor de notas (abaixo) |
| `stateDir` | registros dos agentes e snapshots (padrão `$WT_STATE_DIR` ou `~/.local/state/worktree-app`) |
| `distro` | distro do WSL para o terminal (padrão `$WSL_DISTRO_NAME`) |
| `terminal` | argv da aba nova com `{title}` `{path}` `{distro}` `{cmd}`; padrão no WSL: `wt.exe -w 0 nt --title {title} wsl.exe -d {distro} --cd {path} -e {cmd}`; vazio desliga |
| `agents` | `{"nome": {"new": [...], "resume": [..., "{session}"]}}`; padrão: `claude`, `codex`, `opencode`; ex. agy: `{"new":["agy"],"resume":["agy","--conversation","{session}"]}` |
| `diffMaxKB` | corte do patch em "Comparar" (padrão 2048) |
| `snapshots` | `{"keep":10,"maxAgeDays":30,"preRemoveDays":90,"maxMB":100,"fileMaxMB":20}`: retenção por pasta, limite total e por arquivo |

Por repo:

| campo | uso |
|---|---|
| `key`, `dir` | id curto e pasta do repo em `root` |
| `main` | worktree principal dentro de `dir` (`dev`, ou `.` se é a própria pasta) |
| `base` | origem padrão das branches novas |
| `worktreeRoot`, `folderPattern` | onde nascem as worktrees deste repo (relativo a `root`) |
| `copy` | arquivos/pastas não versionados copiados do main |
| `deps[]` | `dir` e `install` (argv) de cada pasta de dependência |
| `dockerEnv` | `.env` do compose, chaves de projeto/porta e faixa de portas |

## Notas no Obsidian (opcional)

Com `vault.path` configurado, o app só lê e escreve dentro de `vault.scope`
(padrão `worktree-app`). Notas fora dessa pasta nunca são lidas nem servidas.
Em `<scope>/worktrees/` ficam:

- `<feature>--<repo-key>.md`: só propriedades geradas, com `generated_by`.
  Caracteres especiais são codificados para não virarem caminhos.
- `worktrees.base`: criada uma vez (Cards por frente, ativos e removidos);
  pode ser personalizada no Obsidian.
- `worktrees.canvas`: regenerado com frente → worktree → último savepoint.

No menu `···` do card, **Editar conexões** aceita caminhos ou `[[wikilinks]]`
dentro do escopo. As propriedades `front` e `last_savepoint` são preservadas nas
atualizações, e os links abrem no Obsidian (`obsidian://`).

As notas são atualizadas ao consultar `/api/state` (botão Atualizar) e antes e
depois das operações. Uma remoção preserva a nota com `status: removido`, o SHA
anterior e os vínculos. Falha de leitura não é tratada como remoção, e notas com
conteúdo no corpo não são substituídas. Não há atualização em segundo plano.

Só entram nas notas estado, contagens, branch, SHA, upstream, porta e projeto
Docker. Nomes de arquivos alterados, conteúdo, logs e segredos não entram.

Campos opcionais de `vault`:

| campo | uso |
|---|---|
| `scope` | pasta do Vault que o app pode ler e escrever |
| `savepoints`, `savepointsLink` | pasta de notas de retomada fora do Vault e onde ela aparece no Vault; o frontmatter (`branch`, `repo`, `worktree`, `front`, `related`) liga a nota à worktree |
| `docs`, `docsLink` | árvore de docs fora do Vault e onde ela é espelhada; o leitor prefere a cópia de fora |

Convenção do grafo e dos cards: notas em `docs/kanbans/` são frentes, em
`docs/savepoints/` são savepoints, em `Diario/` são diário e o resto do escopo é doc.

Para ver tudo funcionando, há um Vault de exemplo em [`examples/`](examples/README.md)
com kanbans, savepoints com vínculo, diário e uma nota com anexos.

`POST /api/connections` (header `X-WT: 1`) atualiza vínculos por script:

```json
{"repo":"web","feature":"checkout-novo","front":"worktree-app/docs/kanbans/checkout","lastSavepoint":"worktree-app/docs/savepoints/2026-11-10-checkout-novo"}
```

## Leitor de notas e anexos

O painel abre as notas do escopo num leitor de Markdown próprio (montado com
`createElement`/`textContent`, sem `innerHTML`). Imagens viram figura com legenda,
links para PDF/planilha/arquivo viram card com tipo e tamanho, e um PDF sozinho no
parágrafo ganha prévia embutida.

Os anexos passam por `GET /api/asset?doc=<nota>&path=<link>`: só dentro das raízes
do leitor, com allowlist de extensão, limite de tamanho, proteção contra symlink e
`..`, e `Content-Disposition` (inline para imagem/PDF/texto, download para o resto).
SVG é servido com CSP sem script.

| `reader` | padrão | uso |
|---|---|---|
| `images` | `true` | renderizar imagens locais |
| `maxImageWidth` | `720` | largura máxima em px |
| `attachments` | `card` | `card` ou `inline` |
| `pdf` | `preview` | `preview` (embed) ou `link` |
| `externalImages` | `false` | carregar imagem de URL externa |
| `maxAssetMB` | `50` | limite por anexo |

## Segurança

- Escuta só em loopback. Rotas que alteram estado exigem o header `X-WT: 1` e
  `Host` local, o que bloqueia requisições de outras páginas do navegador.
- Nenhum comando altera o remoto; remoções conferem alterações e commits locais.
- O Vault fica restrito a `vault.scope`.
- O hook dos agentes (`wt-street-linux hook`) não imprime nada, exceto a resposta
  JSON que o agy exige (`{}` ou `{"decision":"allow"}` no Stop); sempre sai com 0 e
  grava só agente, sessão, pasta, estado, horários e pid (0600). Prompt e transcript
  não são gravados. Nada disso vai para o Vault.
- O consumo por sessão lê apenas campos numéricos (tokens, cache, raciocínio e custo)
  dos transcripts do Claude Code e do Codex, de forma incremental, e do opencode via
  `opencode db` (consulta SQL só com números). Prompt, resposta e saída de ferramenta
  não são guardados nem devolvidos. O agy (protobuf) aparece como "—" até existir uma
  fonte numérica no binário.
- O terminal só aceita worktrees conhecidas e monta tudo como argv, sem `cmd.exe`;
  argumentos com espaço, aspas, `;` ou `&` são recusados. A sessão a retomar vem do
  registro do hook, nunca do navegador.
- O diff roda com `GIT_OPTIONAL_LOCKS=0` (não cria `index.lock`) e só lê.
- Snapshot é local, em pasta 0700 com arquivos 0600. O `copy` do repo, as deps e uma
  lista fixa de segredos (`.env*`, `*.pem`, `*.key`, `*.p12`, `*.pfx`, `id_rsa*`,
  `.npmrc`, `credentials*.json`, `.claude/`, `secrets/`) ficam de fora; só os nomes
  omitidos vão para o `meta.json`. Nada disso vai para o Vault. O restore recusa
  worktree suja, caminho absoluto ou com `..`, links e sobrescrita; o bundle de
  commits é só informativo (nenhuma ref é escrita).

## Créditos e licença

Criado por Vinicius; adaptado para WSL, Obsidian e leitor de notas por Lucas Moura.
Licença [MIT](LICENSE).
