# Exemplo: Vault de demonstração

`vault/` é um Vault do Obsidian pequeno para ver o leitor, o grafo e os vínculos
do painel funcionando. Os repositórios são por sua conta.

```sh
cp examples/config.example.json examples/config.json
# edite root e repos em examples/config.json
go build -o worktree-app . && ./worktree-app -config examples/config.json
```

O que olhar:

| onde | o que mostra |
|---|---|
| `docs/savepoints/2026-11-1*-checkout-*.md` | vínculo por `worktree:` no frontmatter; o mais novo vence |
| `docs/savepoints/2026-11-12-auditoria-login.md` | vínculo por `branches:` restrito a um repo |
| `docs/kanbans/` | frentes que agrupam os cards (`front`) |
| `docs/arquitetura/fluxo-de-worktrees.md` | leitor com imagem SVG, prévia de PDF, card de CSV e anexo inexistente |
| `Diario/` | aparece no grafo com o filtro de diário ligado |
| `Pessoal/` | fora de `vault.scope`: o painel nunca lê |

Para ver os vínculos nos cards, crie pelo painel a feature `checkout-novo` no repo
`web` e uma worktree da branch existente `dev.auditoria-login` no repo `api`. As
notas das worktrees são geradas em `vault/worktree-app/worktrees/` (não versionadas).

Convenção do grafo: notas em `docs/kanbans/` são frentes, em `docs/savepoints/`
são savepoints, em `Diario/` são diário e o resto do escopo é doc.
