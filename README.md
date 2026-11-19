# worktree-app

Painel local para criar, acompanhar e remover **git worktrees** de vários
repositórios ao mesmo tempo, com notas opcionais no Obsidian.

É um binário Go com a página embutida (`go:embed`), sem dependências externas,
que escuta só em `127.0.0.1`. Funciona em Windows, Linux e WSL.

> Interface e documentação em português (BR).

![Demonstração do worktree-app: mural de worktrees, criação, savepoints, leitor de notas com anexos, grafo e modelos visuais](docs/demo.gif)

As telas usam o Vault de [`examples/`](examples/README.md).

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

## Créditos e licença

Criado por Vinicius; adaptado para WSL, Obsidian e leitor de notas por Lucas Moura.
Licença [MIT](LICENSE).
