#!/usr/bin/env python3
"""Gera os quadros do terminal do vídeo a partir de um tmux headless real.

Uso:
  python3 scripts/terminal-frames.py <worktree-de-exemplo> <pasta-de-saida> [titulo]

- Abre um bash limpo (sem rc do usuário) dentro de um tmux sem janela, na
  worktree de EXEMPLO, com HOME na pasta acima do root: o prompt mostra
  ~/projetos/... e nada da máquina real.
- Digita os comandos tecla a tecla, captura cada passo com
  `tmux capture-pane -e` (ANSI) e converte com rich para HTML com estilo
  inline. `capture-terminal.cjs` transforma os HTML em PNG.
- Não roda agente nenhum: a última linha só deixa o comando digitado.
"""
import html
import os
import subprocess
import sys
import time

from rich.console import Console
from rich.terminal_theme import MONOKAI
from rich.text import Text

COLS, ROWS = 92, 12
SESSION = "wt-video"

wt = os.path.abspath(sys.argv[1])
out = os.path.abspath(sys.argv[2])
title = sys.argv[3] if len(sys.argv) > 3 else "claude · checkout-novo"
home = os.path.dirname(os.path.dirname(os.path.dirname(wt)))  # .../demo (root = demo/projetos)
os.makedirs(out, exist_ok=True)


def tmux(*args, check=True):
    return subprocess.run(["tmux", *args], check=check, capture_output=True, text=True).stdout


tmux("kill-session", "-t", SESSION, check=False)
env = {
    "HOME": home,
    "PATH": "/usr/local/bin:/usr/bin:/bin",
    "TERM": "xterm-256color",
    "LANG": "C.UTF-8",
    "PS1": r"\[\e[1;32m\]dev@exemplo\[\e[0m\]:\[\e[1;34m\]\w\[\e[0m\]$ ",
    "GIT_CONFIG_GLOBAL": "/dev/null",
}
cmd = "env -i " + " ".join(f"{k}='{v}'" for k, v in env.items()) + " bash --norc --noprofile"
tmux("new-session", "-d", "-s", SESSION, "-x", str(COLS), "-y", str(ROWS), "-c", wt, cmd)
time.sleep(0.6)
tmux("send-keys", "-t", SESSION, "-l", "git config color.ui always; clear")
tmux("send-keys", "-t", SESSION, "Enter")
time.sleep(0.5)

frames = []


def grab(hold=1):
    time.sleep(0.25)
    frames.append((tmux("capture-pane", "-t", SESSION, "-p", "-e"), hold))


def typed(text, step=3, hold_end=1):
    for i in range(step, len(text) + step, step):
        tmux("send-keys", "-t", SESSION, "-l", "--", text[i - step : i])
        grab()
    if frames:
        frames[-1] = (frames[-1][0], hold_end)


def enter(hold=4):
    tmux("send-keys", "-t", SESSION, "Enter")
    time.sleep(0.4)
    grab(hold)


grab(3)
typed("git status -sb")
enter(5)
typed("git log --oneline -3")
enter(5)
typed("claude", step=2, hold_end=6)  # só digitado: nenhum agente roda

tmux("kill-session", "-t", SESSION, check=False)

CSS = """
html,body{margin:0;background:transparent}
.win{width:1180px;margin:20px auto;border-radius:12px;overflow:hidden;background:#1e1f1c;
 box-shadow:0 18px 50px rgba(0,0,0,.35);border:1px solid #3a3b36}
.bar{display:flex;align-items:center;gap:10px;height:38px;padding:0 14px;background:#2b2c28;
 color:#d8d6cc;font:600 14px 'Helvetica Neue',Arial,sans-serif}
.tab{background:#1e1f1c;padding:7px 14px;border-radius:8px 8px 0 0;margin-top:8px}
.plus{opacity:.5}
pre{margin:0;padding:14px 18px 18px;color:#e9e6da;font:19px/1.45 'DejaVu Sans Mono',monospace;
 white-space:pre;min-height:340px}
"""

console = Console(record=True, width=COLS, color_system="truecolor", force_terminal=True, file=open(os.devnull, "w"))
manifest = []
for n, (ansi, hold) in enumerate(frames):
    console.print(Text.from_ansi(ansi.rstrip("\n")), crop=False, soft_wrap=True)
    body = console.export_html(inline_styles=True, code_format="{code}", theme=MONOKAI)
    page = (
        f"<!doctype html><meta charset=utf-8><style>{CSS}</style>"
        f"<div class=win><div class=bar><span class=tab>&gt;_ {html.escape(title)}</span><span class=plus>+</span></div>"
        f"<pre>{body}</pre></div>"
    )
    name = f"term-{n:02d}"
    with open(os.path.join(out, name + ".html"), "w", encoding="utf-8") as f:
        f.write(page)
    manifest.append(f"{name}.png {hold}")

with open(os.path.join(out, "frames.txt"), "w") as f:
    f.write("\n".join(manifest) + "\n")
print(f"{len(frames)} quadros em {out}")
