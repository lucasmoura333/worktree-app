# worktree-app

[Português](README.md) · **English**

**Live preview:** https://lucasmoura333.github.io/worktree-app/ (sample data; nothing is changed)

A local dashboard to create, track and remove **git worktrees** across several
repositories at once, with optional notes in Obsidian.

It is a single Go binary with the web UI embedded (`go:embed`), no external
dependencies, listening only on `127.0.0.1`. Runs on Windows, Linux and WSL.

> The UI itself is in Brazilian Portuguese.

![worktree-app demo: worktree board, creation, savepoints, note reader with attachments, graph and visual themes](docs/demo.gif)

The screens use the sample Vault in [`examples/`](examples/README.md).

## Why

If you work on several tasks in parallel, you end up with one worktree per task
in each repo: an `.env` to copy, `node_modules` to install, a Docker port that
must not collide, a branch to remember to delete. The dashboard does those steps
in one go, shows the state of everything on one screen and only removes what is
safe to remove.

## Getting started

Requires Go 1.25+ and `git` on the PATH.

```sh
git clone https://github.com/lucasmoura333/worktree-app
cd worktree-app
cp config.example.json config.json   # set root, repos and (optionally) vault
go build -o worktree-app .
./worktree-app                       # opens the browser; Ctrl+C stops it
```

| flag | use |
|---|---|
| `-config path` | config file (default: `config.json` next to the binary) |
| `-no-open` | don't open the browser |
| `-demo` | preview with sample data on `127.0.0.1:7788`; reads and changes nothing |
| `-export-static dir` | builds the preview as a static site (board, map and note reader with the `examples/` Vault) for GitHub Pages; `-export-vault` picks another sample Vault |

Running it again while it is open just opens the browser. For "install
dependencies", start it from a terminal where `yarn`/`npm`/`composer` etc. are on
the PATH: installs inherit that environment.

### Windows + WSL

For repos living inside WSL, build the Linux binary and start it from PowerShell:

```powershell
go build -buildvcs=false -o wt-street.exe .
$env:GOOS = 'linux'; $env:GOARCH = 'amd64'
go build -buildvcs=false -o wt-street-linux .
Remove-Item Env:GOOS, Env:GOARCH

.\start-design.ps1 -Distro Ubuntu-24.04 -Config config.wsl.json
.\start-design.ps1 -Preview          # preview with sample data
```

The editor uses WSL's `code`, and "Open folder" converts the path with
`wslpath -w` for Explorer. Under `/mnt/c`, builds need `-buildvcs=false`.

### Tests

```sh
go test ./...    # uses temporary git repositories, never your real ones
```

## What "Create" does, per selected repo (in parallel)

1. `git fetch origin --prune` (optional).
2. `git worktree add` in the folder given by `folderPattern`:
   - **new branch**: `branchPattern` from `base` (e.g. `origin/dev`), with no upstream;
   - **existing branch**: local, or `origin/x` with tracking (for reviews).
3. Copies the untracked files listed in `copy` from the main worktree (`.env` and
   the like; only the name goes to the log, never the content).
4. With `dockerEnv`, renames the compose project to `<main's name>-<feature>` and
   picks the first free port in the range.
5. Dependencies (`node_modules`, `vendor`...) are never copied: the worktree starts
   without them. With "install dependencies" checked, it runs each `install`.

It then creates `tasks/<feature>/README.md` and `<feature>.code-workspace`
(settings copied from `workspaceTemplate`), only if they don't exist yet. Paths
inside the main repo are refused, so a worktree never becomes an untracked
folder of main.

## Dashboard

Worktrees are grouped by feature. For each one it shows uncommitted changes,
commits not on any remote, ↑/↓ against upstream, missing files and dependencies,
and dependencies that are links to another folder.

- **Copy .env etc.**: redoes steps 3–4 on a worktree that is missing something.
- **Install deps**: runs `install` for the missing dependencies.
- **Remove**: checks everything first and only removes when no worktree has
  changes or local commits; otherwise it requires "force". Dependencies that are
  links/junctions are detached before the folder is deleted, so the original is
  never touched. Deleting the local branch is optional (`git branch -d`, or `-D`
  with "force"); the hash is logged so it can be recreated. Remote branches and
  `tasks/` are never deleted.
- **Broken entries**: worktrees whose folder is gone, with a `git worktree prune` button.
- **Diagnostics**: read-only report of inconsistent worktrees, orphan branches
  (merged, missing a remote or with a gone upstream), duplicated or out-of-range
  Docker ports, corrupted snapshots, orphan/duplicate/hookless agent sessions and
  the latest `ops.jsonl` failures. The menu badge shows what needs action.
- **Agents**: the top bar counts agents (Claude Code, Codex, opencode, agy) that are
  working, waiting for you or done, and each card shows the agent in its folder, the
  session usage (input/output tokens; cost when the source reports it) and a
  **⚠ N live** warning when more than one session runs in the same worktree.
  It refreshes every 3 s from `/api/agents`, which runs no `git status` and does
  not touch the Vault. "🔕 Notify" enables a browser notification when an agent
  starts waiting for you. States come from hooks; without a hook, processes found
  in `/proc` show as "active (no hook)". See [`examples/hooks/`](examples/hooks/README.md).
- **Terminal and agents** (WSL): `>_` opens a Windows Terminal tab in the worktree
  folder; in the `···` menu, "▶ agent (new)" opens the agent there and
  "↺ agent (resume)" resumes the last session recorded in that worktree (it stays
  disabled, with the reason, until that agent has a session there; the
  `/api/agents` poll re-enables it). If an agent is already live in the worktree,
  "▶ new" asks for confirmation before opening another session in the same tree.
- **Compare…**: read-only diff of the worktree against the repo base (includes
  uncommitted work and lists new files) or against another worktree of the same
  repo (commits only). Files from `copy` (`.env` and the like) and dependency
  folders are left out; the patch is cut at `diffMaxKB`.
- **Snapshot now / Snapshots…**: local snapshot of the worktree under
  `stateDir/snapshots/<repo>/<folder>/<date-time>/`, with `meta.json` (branch, HEAD,
  upstream, dirty, agent sessions to resume and the linked savepoint),
  `changes.patch`, `untracked.tar` and, when there are commits not on a remote,
  `commits.bundle`. Secrets, the repo `copy` and dependencies never go in; omitted
  names are recorded in `meta.json`. Restoring requires a clean worktree: it
  validates the patch and tar before applying, refuses to overwrite files and does
  not write bundle refs (it only prints the `git fetch` command). Retention keeps
  the 10 newest per folder for 30 days; snapshots taken before a removal are kept
  for 90 days. On a forced removal with risk, the snapshot is taken before anything
  is deleted, and if it fails nothing is removed.

The app never commits, pushes or changes the remote. Its only remote operation
is `git fetch`.

There are four visual themes (Street-Art, Neo Brutalism, Grunge and Frasurbane),
stored in the browser, plus Cards, Map (local graph of notes) and Removed views.
Search and filters live in the URL.

## Obsidian notes (optional)

With `vault.path` set, the app only reads and writes inside `vault.scope`
(default `worktree-app`); notes outside it are never read or served. It keeps a
generated note per worktree, a `worktrees.base` view and a `worktrees.canvas`
linking front → worktree → last savepoint. Only state, counts, branch, SHA,
upstream, port and Docker project go into notes: never file names, contents,
logs or secrets. A sample Vault with kanbans, linked savepoints, a journal and a
note with attachments lives in [`examples/`](examples/README.md).

The built-in Markdown reader renders notes with `createElement`/`textContent`
(no `innerHTML`). Images become captioned figures, PDF/spreadsheet links become
cards, and a lone PDF gets an embedded preview. Attachments are served through
`GET /api/asset` with an extension allowlist, size limit, symlink and `..`
protection, and a script-free CSP for SVG.

See [`config.example.json`](config.example.json) and the Portuguese
[README](README.md) for every config field.

## Security

- Listens on loopback only. State-changing routes require the `X-WT: 1` header
  and a local `Host`, which blocks requests from other pages in the browser.
- No command changes the remote; removals check for changes and local commits.
- The Vault is restricted to `vault.scope`.
- The agent hook prints nothing except the JSON reply agy requires (`{}`, or
  `{"decision":"allow"}` on Stop); it always exits 0 and stores only agent,
  session, folder, state, timestamps and pid (0600). Prompts and transcripts are
  never stored.
- Session usage reads only numeric fields (tokens, cache, reasoning and cost)
  from Claude Code and Codex transcripts, incrementally, and from opencode via
  `opencode db` (a numeric-only SQL query); prompts, replies and tool output are
  never kept or returned. agy (protobuf) shows "—" until the binary has a
  numeric source for it.

## Credits and license

Created by Vinicius; adapted for WSL, Obsidian and the note reader by Lucas Moura.
[MIT](LICENSE) license.
