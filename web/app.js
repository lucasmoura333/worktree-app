"use strict";

const $ = (s, el = document) => el.querySelector(s);
const PROPS = new Set(["checked", "value", "disabled", "selected", "hidden"]);

// h monta elementos sempre por textContent/propriedades: nomes de branch e
// caminhos nunca viram HTML.
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (PROPS.has(k)) el[k] = v;
    else el.setAttribute(k, v === true ? "" : String(v));
  }
  for (const kid of kids.flat()) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : String(kid));
  }
  return el;
}

const HEADERS = { "Content-Type": "application/json", "X-WT": "1" };

async function getJSON(url) {
  const r = await fetch(url);
  if (!r.ok) throw new Error((await r.text()) || r.statusText);
  return r.json();
}

async function postJSON(url, body) {
  const r = await fetch(url, {
    method: "POST",
    headers: HEADERS,
    body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error((await r.text()) || r.statusText);
  return r.json();
}

let state = null;
// Pasta do Vault que o servidor lê e escreve (vault.scope no config).
// Prévia sem leitor/mapa (a prévia estática do GitHub Pages lê as notas de exemplo).
const previewOnly = () => !!(state && state.demo && !state.docsPreview);
const vaultScope = () => (state && state.vaultScope) || "worktree-app";
let busy = false;
const sel = {}; // repo -> { checked, mode, base, branch }
const branches = {}; // repo -> [{ name, remote, date }]

// ---------- log ----------

function logLine(ev) {
  const box = $("#log-body");
  const cmd = /^git /.test(ev.msg || "") ? " cmd" : "";
  const line = h(
    "div",
    { class: "l-" + (ev.level || "info") + cmd },
    h("span", { class: "t" }, (ev.t || new Date().toLocaleTimeString()) + " "),
    ev.repo ? h("span", { class: "r" }, "[" + ev.repo + "] ") : null,
    ev.level === "done" ? (ev.ok ? "✔ " : "✖ ") : null,
    h("span", { class: "m" }, ev.msg),
  );
  const atBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 8;
  box.append(line);
  if (atBottom) box.scrollTop = box.scrollHeight;
}

function setBusy(v) {
  busy = v;
  document.body.classList.toggle("busy", v);
  updateCreate();
}

// stream lê a resposta NDJSON de uma operação e joga cada evento no log.
async function stream(url, body) {
  if (busy || state?.demo) return false;
  setBusy(true);
  let ok = false;
  try {
    const r = await fetch(url, {
      method: "POST",
      headers: HEADERS,
      body: JSON.stringify(body),
    });
    if (!r.ok) {
      logLine({ level: "error", msg: (await r.text()) || r.statusText });
      return false;
    }
    const reader = r.body.pipeThrough(new TextDecoderStream()).getReader();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += value;
      let i;
      while ((i = buf.indexOf("\n")) >= 0) {
        const raw = buf.slice(0, i).trim();
        buf = buf.slice(i + 1);
        if (!raw) continue;
        const ev = JSON.parse(raw);
        if (ev.level === "done") ok = !!ev.ok;
        logLine(ev);
      }
    }
  } catch (e) {
    logLine({ level: "error", msg: e.message });
  } finally {
    setBusy(false);
    await refresh();
  }
  return ok;
}

// ---------- estado ----------

async function refresh() {
  try {
    state = await getJSON("/api/state");
  } catch (e) {
    logLine({ level: "error", msg: "estado: " + e.message });
    $("#updated").textContent = "Atualização falhou";
    if (!state)
      $("#features").replaceChildren(
        h(
          "div",
          { class: "empty" },
          h("h3", {}, "Não foi possível ler as worktrees."),
          h(
            "p",
            {},
            "Confira se o servidor está em execução e tente atualizar.",
          ),
          h("button", { onclick: refresh }, "Tentar novamente"),
        ),
      );
    return;
  }
  $("#root").textContent = state.root;
  if (state.demo) {
    $("#root").textContent =
      "Prévia visual · dados de exemplo · repositórios e Vault não são alterados";
    $("#vault-status").textContent = "Prévia das conexões com o Obsidian";
  }
  $("#btn-fetch").disabled = !!state.demo;
  $("#updated").textContent =
    "Atualizado " +
    new Date().toLocaleTimeString("pt-BR", {
      hour: "2-digit",
      minute: "2-digit",
    });
  $("#vault-status").textContent = state.demo
    ? "Prévia · conexões de exemplo com o Obsidian"
    : state.vaultError
      ? "Vault: " + state.vaultError
      : state.vault
        ? "Conectado ao Obsidian · notas geradas em " + vaultScope() + "/worktrees/"
        : "Vault não configurado. Defina vault.path no config.json para conectar.";
  const repoFilter = $("#repo-filter");
  const selectedRepo =
    repoFilter.value || new URLSearchParams(location.search).get("repo") || "";
  repoFilter.replaceChildren(
    h("option", { value: "" }, "Todos os repos"),
    ...state.repos.map((r) => h("option", { value: r.key }, r.dir)),
  );
  repoFilter.value = selectedRepo;

  renderRepoRows();
  renderFeatures();
  renderStale();
  schedulePlan();
}

async function loadBranches(key, force) {
  if (state?.demo) {
    branches[key] = [];
    return;
  }
  if (branches[key] && !force) return;
  try {
    branches[key] = await getJSON(
      "/api/branches?repo=" + encodeURIComponent(key),
    );
  } catch (e) {
    logLine({ level: "error", repo: key, msg: "branches: " + e.message });
    branches[key] = [];
  }
  fillDatalist(key);
}

function fillDatalist(key) {
  const dl = document.getElementById("dl-" + key);
  if (!dl) return;
  const s = sel[key];
  const list = (branches[key] || []).filter(
    (b) => s.mode === "existing" || b.remote,
  );
  dl.replaceChildren(
    ...list.map((b) => h("option", { value: b.name }, b.date)),
  );
}

function plural(n, one, many) {
  return n + " " + (n === 1 ? one : many);
}

function repoDir(key) {
  const r = state && state.repos.find((x) => x.key === key);
  return r ? r.dir : key;
}

// ---------- nova feature ----------

function renderRepoRows() {
  const tbody = $("#repo-rows");
  tbody.replaceChildren();
  for (const r of state.repos) {
    const s =
      sel[r.key] ||
      (sel[r.key] = { checked: false, mode: "new", base: r.base, branch: "" });
    const input = h("input", {
      "aria-label": "Origem ou branch de " + r.dir,
      name: "branch-" + r.key,
      list: "dl-" + r.key,
      spellcheck: "false",
      autocomplete: "off",
      value: s.mode === "new" ? s.base : s.branch,
      placeholder: s.mode === "new" ? r.base : "branch local ou origin/…",
      disabled: !s.checked,
      oninput: (e) => {
        if (s.mode === "new") s.base = e.target.value;
        else s.branch = e.target.value;
        schedulePlan();
      },
      onfocus: () => loadBranches(r.key),
    });
    const mode = h(
      "select",
      {
        "aria-label": "Modo de " + r.dir,
        name: "mode-" + r.key,
        disabled: !s.checked,
        onchange: (e) => {
          s.mode = e.target.value;
          renderRepoRows();
          schedulePlan();
        },
      },
      h(
        "option",
        { value: "new", selected: s.mode === "new" },
        "nova branch a partir de",
      ),
      h(
        "option",
        { value: "existing", selected: s.mode === "existing" },
        "branch existente",
      ),
    );
    const cb = h("input", {
      type: "checkbox",
      "aria-label": "Selecionar " + r.dir,
      checked: s.checked,
      disabled: !!r.error,
      onchange: (e) => {
        s.checked = e.target.checked;
        if (s.checked) loadBranches(r.key);
        renderRepoRows();
        schedulePlan();
      },
    });
    tbody.append(
      h(
        "tr",
        { class: s.checked ? "" : "off" },
        h("td", {}, cb),
        h(
          "td",
          {},
          h("div", {}, h("strong", {}, r.dir)),
          r.error
            ? h("div", { class: "err" }, r.error)
            : h(
                "div",
                { class: "muted mono" },
                r.main + (r.mainBranch ? " · " + r.mainBranch : ""),
              ),
        ),
        h("td", {}, mode),
        h("td", {}, input, h("datalist", { id: "dl-" + r.key })),
      ),
    );
    fillDatalist(r.key);
  }
}

function buildReq() {
  return {
    feature: $("#feature").value.trim(),
    repos: state
      ? state.repos
          .filter((r) => sel[r.key] && sel[r.key].checked)
          .map((r) => {
            const s = sel[r.key];
            return {
              key: r.key,
              mode: s.mode,
              base: s.mode === "new" ? s.base : "",
              branch: s.mode === "existing" ? s.branch : "",
            };
          })
      : [],
    install: $("#opt-install").checked,
    fetch: $("#opt-fetch").checked,
    tasks: $("#opt-tasks").checked,
    workspace: $("#opt-ws").checked,
  };
}

let planTimer = 0;
let planSeq = 0;
let lastPlan = null;

function schedulePlan() {
  ++planSeq;
  lastPlan = null;
  updateCreate();
  clearTimeout(planTimer);
  planTimer = setTimeout(runPlan, 250);
}

async function runPlan() {
  if (state?.demo) {
    $("#plan").replaceChildren(
      h(
        "p",
        { class: "help" },
        "Prévia visual: criação desativada. No modo normal, a prévia mostra pastas, branches e portas antes de criar.",
      ),
    );
    return;
  }
  const req = buildReq();
  const box = $("#plan");
  if (!req.feature || !req.repos.length) {
    lastPlan = null;
    box.replaceChildren(
      h(
        "p",
        { class: "muted" },
        "Dê um nome e marque ao menos um repo para ver a prévia.",
      ),
    );
    updateCreate();
    return;
  }
  const seq = ++planSeq;
  let plan;
  try {
    plan = await postJSON("/api/plan", req);
  } catch (e) {
    if (seq === planSeq) {
      lastPlan = null;
      box.replaceChildren(h("p", { class: "err" }, e.message));
      updateCreate();
    }
    return;
  }
  if (seq !== planSeq) return;
  lastPlan = plan;
  renderPlan(plan, req);
  updateCreate();
}

function renderPlan(plan, req) {
  const rows = plan.items.map((it) =>
    h(
      "tr",
      {},
      h("td", {}, repoDir(it.repo)),
      h("td", { class: "mono" }, it.folder || ""),
      h(
        "td",
        { class: "mono" },
        it.branch || "",
        it.newBranch && it.mode === "new"
          ? h("span", { class: "muted" }, " (nova)")
          : null,
      ),
      h(
        "td",
        { class: "mono muted" },
        it.startPoint || (it.mode === "existing" ? "local" : ""),
      ),
      h("td", { class: "mono" }, it.port || ""),
      h(
        "td",
        {},
        it.problems.length
          ? it.problems.map((p) => h("div", { class: "err" }, p))
          : h("span", { class: "okc" }, "ok"),
      ),
    ),
  );
  const meta = h(
    "div",
    { class: "plan-meta muted" },
    req.tasks
      ? h(
          "div",
          {},
          "tasks: ",
          h("span", { class: "mono" }, plan.tasksDir),
          plan.tasksExists ? " (já existe, mantida)" : " (nova)",
        )
      : null,
    req.workspace
      ? h(
          "div",
          {},
          "workspace: ",
          h("span", { class: "mono" }, plan.workspace),
          plan.workspaceExists
            ? h("span", { class: "warnc" }, " (já existe, não será alterado)")
            : " (novo)",
        )
      : null,
    plan.problems.map((p) => h("div", { class: "err" }, p)),
  );
  $("#plan").replaceChildren(
    h(
      "table",
      { class: "grid" },
      h(
        "thead",
        {},
        h(
          "tr",
          {},
          ["Repo", "Pasta", "Branch", "Origem", "Porta", "Status"].map((t) =>
            h("th", {}, t),
          ),
        ),
      ),
      h("tbody", {}, rows),
    ),
    meta,
  );
}

function updateCreate() {
  $("#btn-create").disabled = busy || state?.demo || !lastPlan || !lastPlan.ok;
}

async function create() {
  const req = buildReq();
  logLine({ level: "info", msg: "criando " + req.feature + "…" });
  const ok = await stream("/api/create", req);
  if (ok) {
    $("#create-dlg").close();
    $("#feature").value = "";
    schedulePlan();
  }
}

// ---------- features ----------

function badges(wt, f) {
  const b = [];
  const tag = (cls, text, title) =>
    h("span", { class: "tag " + cls, title }, text);
  if (wt.error) b.push(tag("err", "erro", wt.error));
  if (wt.dirty > 0)
    b.push(tag("warn", plural(wt.dirty, "alteração", "alterações")));
  if (wt.unpushed > 0)
    b.push(
      tag(
        "warn",
        wt.unpushed + " fora do remoto",
        "commits que não estão em nenhum remoto",
      ),
    );
  if (wt.upstream) {
    if (wt.ahead || wt.behind)
      b.push(tag("", "↑" + wt.ahead + " ↓" + wt.behind, wt.upstream));
  } else if (!wt.error) {
    b.push(tag("", "sem upstream"));
  }
  if (wt.missingFiles.length)
    b.push(tag("warn", "faltam: " + wt.missingFiles.join(", ")));
  if (wt.missingDeps.length)
    b.push(tag("", "sem " + wt.missingDeps.join(", ")));
  if (wt.linkedDeps.length)
    b.push(tag("", "link: " + wt.linkedDeps.join(", ")));
  if (wt.branch && !wt.branch.includes(f.name))
    b.push(tag("", "branch ≠ pasta"));
  if (!b.some((x) => /warn|err/.test(x.className))) b.unshift(tag("ok", "ok"));
  return h("div", { class: "badges" }, b);
}

// One visual card per worktree; the original feature remains the unit of removal.
let view = new URLSearchParams(location.search).get("view") || "cards";
if (!["cards", "map", "archived"].includes(view)) view = "cards";
function notePath(value) {
  return (value || "").replace(/^\[\[|\]\]$/g, "").split("|")[0];
}
function noteLabel(value) {
  const path = notePath(value);
  return path.split("/").pop() || "";
}
function noteHref(value) {
  return (
    "obsidian://open?vault=" +
    encodeURIComponent(state.vault || "") +
    "&file=" +
    encodeURIComponent(notePath(value))
  );
}
function docLink(value, label, icon = "↗") {
  if (previewOnly() && value)
    return h(
      "span",
      { class: "doc-link", title: notePath(value) },
      h("span", { "aria-hidden": "true" }, icon),
      label || noteLabel(value),
    );
  return value && state.vault
    ? h(
        "a",
        {
          class: "doc-link",
          href: noteHref(value),
          title: notePath(value) + " · clique lê aqui, Ctrl+clique abre no Obsidian",
          "data-note": value,
        },
        h("span", { "aria-hidden": "true" }, icon),
        label || noteLabel(value),
      )
    : null;
}
function updatedLabel(value) {
  const date = new Date(value);
  return value && !Number.isNaN(+date)
    ? date.toLocaleString("pt-BR", {
        day: "2-digit",
        month: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "Estado desta leitura";
}
function worktreeCard(wt, f, removed = false) {
  const attention =
    wt.error ||
    wt.dirty > 0 ||
    wt.unpushed !== 0 ||
    !wt.upstream ||
    wt.missingFiles.length > 0 ||
    wt.missingDeps.length > 0;
  const connection = wt.connection || {};
  const menu = h(
    "div",
    { class: "action-menu" },
    h(
      "button",
      {
        disabled: !state.vault || busy || !!state.vaultError,
        onclick: () => editConnections(wt),
      },
      "Editar conexões",
    ),
    !removed
      ? h(
          "button",
          { class: "act", onclick: () => openPath(wt.path, "explorer") },
          "Abrir pasta",
        )
      : null,
    !removed
      ? f.workspaces.map((ws) =>
          h(
            "button",
            { class: "act", onclick: () => openPath(ws, "editor") },
            "Abrir workspace",
          ),
        )
      : null,
    !removed && wt.missingFiles.length
      ? h(
          "button",
          {
            class: "act",
            onclick: () =>
              stream("/api/setup", {
                repo: wt.repo,
                path: wt.path,
                install: false,
              }),
          },
          "Copiar arquivos locais",
        )
      : null,
    !removed && wt.missingDeps.length
      ? h(
          "button",
          {
            class: "act",
            onclick: () =>
              stream("/api/setup", {
                repo: wt.repo,
                path: wt.path,
                install: true,
              }),
          },
          "Instalar dependências",
        )
      : null,
    !removed
      ? h("button", { onclick: () => openDiff(wt) }, "Comparar…")
      : null,
    !removed
      ? h("button", { onclick: () => takeSnapshot(wt) }, "Snapshot agora")
      : null,
    !removed
      ? h("button", { onclick: () => openSnapshots(wt) }, "Snapshots…")
      : null,
    !removed && state.terminal
      ? (state.agentNames || []).flatMap((ag) => [
          h("button", { onclick: () => launchTerminal(wt.path, ag, false) }, "▶ " + ag + " (novo)"),
          paintResume(
            h(
              "button",
              {
                "data-resume-path": wt.path,
                "data-resume-agent": ag,
                onclick: () => launchTerminal(wt.path, ag, true),
              },
              "↺ " + ag + " (retomar)",
            ),
          ),
        ])
      : null,
    !removed
      ? h(
          "button",
          { class: "danger act", onclick: () => openRemove(f) },
          "Remover feature…",
        )
      : null,
  );
  return h(
    "article",
    { class: "worktree-card" },
    h(
      "div",
      { class: "card-top" },
      h("span", { class: "repo-chip" }, wt.repoDir),
      !removed
        ? h(
            "span",
            { class: "agent-slot", "data-agent-path": wt.path },
            liveAgentsIn(wt.path).map(agentChip),
            liveCount(wt.path) > 1 ? concurrentChip(liveCount(wt.path), liveAgentsIn(wt.path)) : null,
          )
        : null,
      h(
        "span",
        {
          class:
            "state-chip " + (removed ? "removed" : attention ? "warn" : ""),
        },
        removed ? "Removido" : attention ? "Atenção" : "Em dia",
      ),
    ),
    h(
      "div",
      { class: "card-content" },
      h("h3", {}, wt.feature || f.name),
      h(
        "div",
        { class: "branch-line" },
        h("span", { "aria-hidden": "true" }, "⑂"),
        wt.branch || "detached " + wt.head,
      ),
      h(
        "div",
        { class: "git-metrics" },
        h(
          "span",
          { class: wt.dirty ? "dirty" : "" },
          "● " + (wt.dirty || 0) + " alter.",
        ),
        h(
          "span",
          { title: wt.upstream || "Sem upstream" },
          "↑ " + (wt.ahead || 0) + "  ↓ " + (wt.behind || 0),
        ),
        wt.port ? h("span", {}, ":" + wt.port) : null,
      ),
      !removed
        ? badges(wt, f)
        : h("span", { class: "mono" }, "SHA preservado: " + wt.head),
      h(
        "div",
        { class: "document-links" },
        docLink(connection.front, null, "⌁"),
        docLink(
          connection.lastSavepoint,
          "Savepoint · " + noteLabel(connection.lastSavepoint),
          "↶",
        ),
        docLink(connection.note, "Nota da worktree", "◇"),
        !connection.front
          ? h("span", { class: "unlinked" }, "Frente ainda não vinculada")
          : null,
      ),
    ),
    h(
      "div",
      { class: "card-bottom" },
      h("span", { class: "card-time" }, updatedLabel(connection.updatedAt)),
      h(
        "div",
        { class: "row" },
        !removed && state.terminal
          ? h(
              "button",
              {
                class: "open-term",
                title: "Abrir um terminal nesta worktree",
                onclick: () => launchTerminal(wt.path, "", false),
              },
              ">_",
            )
          : null,
        !removed
          ? h(
              "button",
              {
                class: "open-code act",
                onclick: () => openPath(wt.path, "editor"),
              },
              "↗ Code",
            )
          : null,
        h(
          "details",
          { class: "more-actions" },
          h("summary", { "aria-label": "Ações de " + wt.feature }, "···"),
          menu,
        ),
      ),
    ),
  );
}
function renderFeatures() {
  if (!state) return;
  const box = $("#features");
  const query = $("#search").value.trim().toLocaleLowerCase("pt-BR");
  const repo = $("#repo-filter").value;
  const active = state.features.flatMap((f) =>
    f.worktrees.map((wt) => ({ wt, f })),
  );
  const items = (
    view === "archived"
      ? (state.archived || []).map((wt) => ({
          wt,
          f: { name: wt.feature, workspaces: [] },
        }))
      : active
  ).filter(
    ({ wt }) =>
      (!repo || wt.repo === repo) &&
      [
        wt.feature,
        wt.branch,
        wt.repoDir,
        wt.connection?.front,
        wt.connection?.lastSavepoint,
      ]
        .join(" ")
        .toLocaleLowerCase("pt-BR")
        .includes(query),
  );
  $("#feature-count").textContent = active.length;
  document
    .querySelectorAll("[data-view]")
    .forEach((button) =>
      button.setAttribute("aria-pressed", String(button.dataset.view === view)),
    );
  // Mapa: grafo local das notas (graph.js). Com ?center= não depende do filtro.
  if (view === "map") {
    renderGraph(box, items);
    return;
  }
  if (!items.length) {
    box.replaceChildren(
      h(
        "div",
        { class: "empty" },
        h(
          "h3",
          {},
          query || repo
            ? "Nenhum resultado por aqui."
            : view === "archived"
              ? "O histórico começa no próximo ciclo."
              : "Espaço livre para a próxima ideia.",
        ),
        h(
          "p",
          {},
          query || repo
            ? "Ajuste a busca ou o filtro de repositório."
            : view === "archived"
              ? "Notas geradas de worktrees removidas ficam preservadas no Vault."
              : "Crie uma worktree para trabalhar sem misturar contextos.",
        ),
        !query && !repo && view !== "archived"
          ? h(
              "button",
              { class: "primary", onclick: openCreate },
              "＋ Criar primeira worktree",
            )
          : null,
      ),
    );
    return;
  }
  const groups = new Map();
  for (const item of items) {
    const front = item.wt.connection?.front || "";
    if (!groups.has(front)) groups.set(front, []);
    groups.get(front).push(item);
  }
  box.replaceChildren(
    ...Array.from(groups, ([front, entries]) =>
      h(
        "section",
        { class: "front-group" },
        h(
          "div",
          { class: "group-heading" },
          h("span", { class: "group-dot", "aria-hidden": "true" }),
          front && state.vault && !previewOnly()
            ? h("a", { href: noteHref(front), "data-note": front }, noteLabel(front))
            : front
              ? noteLabel(front)
              : "Sem frente vinculada",
          h(
            "span",
            { class: "group-count" },
            plural(entries.length, "worktree", "worktrees"),
          ),
        ),
        h(
          "div",
          { class: "card-grid" },
          entries.map(({ wt, f }) => worktreeCard(wt, f, view === "archived")),
        ),
      ),
    ),
  );
}
function openCreate() {
  $("#create-dlg").showModal();
  $("#feature").focus();
}
function editConnections(wt) {
  if (state?.demo) {
    logLine({
      level: "info",
      msg: "Prévia visual: as conexões de exemplo não são gravadas no Vault.",
    });
    return;
  }
  const dlg = $("#dlg");
  const c = wt.connection || {};
  const front = h("input", {
    value: notePath(c.front),
    name: "front",
    autocomplete: "off",
    spellcheck: "false",
    placeholder: vaultScope() + "/docs/kanbans/minha-frente…",
  });
  const savepoint = h("input", {
    value: notePath(c.lastSavepoint),
    name: "savepoint",
    autocomplete: "off",
    spellcheck: "false",
    placeholder: vaultScope() + "/docs/savepoints/2026-11-10-…",
  });
  const error = h("p", { class: "err", role: "status" });
  const save = h(
    "button",
    {
      class: "primary",
      onclick: async () => {
        save.disabled = true;
        try {
          await postJSON("/api/connections", {
            repo: wt.repo,
            feature: wt.feature,
            front: front.value.trim(),
            lastSavepoint: savepoint.value.trim(),
          });
          dlg.close();
          await refresh();
        } catch (e) {
          error.textContent = e.message;
        } finally {
          save.disabled = false;
        }
      },
    },
    "Salvar conexões",
  );
  dlg.replaceChildren(
    h("h3", {}, "Conectar " + wt.feature),
    h(
      "p",
      { class: "help" },
      "Cole o caminho da nota ou um [[wikilink]]. As conexões são salvas nas propriedades da nota gerada. Kanban, diário e savepoint continuam com seu próprio conteúdo.",
    ),
    h("label", { class: "field" }, "Frente / Kanban", front),
    h("label", { class: "field" }, "Último savepoint", savepoint),
    error,
    h(
      "div",
      { class: "row end" },
      h("button", { onclick: () => dlg.close() }, "Cancelar"),
      save,
    ),
  );
  dlg.showModal();
}

function renderStale() {
  const card = $("#stale-card");
  card.hidden = !state.stale.length;
  const byRepo = {};
  for (const wt of state.stale)
    (byRepo[wt.repo] = byRepo[wt.repo] || []).push(wt);
  $("#stale").replaceChildren(
    ...Object.entries(byRepo).map(([repo, list]) =>
      h(
        "div",
        { class: "feature" },
        h(
          "div",
          { class: "fhead" },
          h("h3", {}, repoDir(repo)),
          h("div", { class: "spacer" }),
          h(
            "button",
            {
              class: "small act",
              onclick: () => stream("/api/prune", { repos: [repo] }),
            },
            "git worktree prune",
          ),
        ),
        list.map((wt) =>
          h(
            "div",
            { class: "mono muted" },
            wt.folder + " · " + (wt.branch || wt.head),
          ),
        ),
      ),
    ),
  );
}

async function openPath(path, how) {
  if (state?.demo) {
    logLine({
      level: "info",
      msg: "Prévia visual: abrir pastas e editores fica disponível com os seus repositórios.",
    });
    return;
  }
  try {
    await postJSON("/api/open", { path, with: how });
  } catch (e) {
    logLine({ level: "error", msg: "abrir: " + e.message });
  }
}

function openRemove(f) {
  const dlg = $("#dlg");
  const risky = (wt) =>
    wt.dirty > 0 || wt.unpushed !== 0 || !!wt.error || liveCount(wt.path) > 0;
  const btn = h(
    "button",
    { class: "danger solid", type: "button" },
    state.demo ? "Prévia · remoção desativada" : "Remover",
  );
  const force = h("input", { type: "checkbox" });
  const delBranch = h("input", { type: "checkbox" });
  const forceBranch = h("input", { type: "checkbox", disabled: true });
  const rmWs = h("input", { type: "checkbox", checked: true });
  const keepSnapshot = h("input", { type: "checkbox", checked: true });
  const checks = f.worktrees.map((wt) => ({
    wt,
    cb: h("input", { type: "checkbox", checked: true }),
  }));
  const update = () => {
    const chosen = checks.filter((c) => c.cb.checked);
    btn.disabled =
      state.demo ||
      !chosen.length ||
      chosen.some((c) => c.wt.locked) ||
      (chosen.some((c) => risky(c.wt)) && !force.checked);
    forceBranch.disabled = !delBranch.checked;
    if (!delBranch.checked) forceBranch.checked = false;
  };
  for (const c of checks) c.cb.addEventListener("change", update);
  force.addEventListener("change", update);
  delBranch.addEventListener("change", update);

  const list = h(
    "ul",
    {},
    checks.map(({ wt, cb }) =>
      h(
        "li",
        {},
        h(
          "label",
          {},
          cb,
          h("strong", {}, wt.repoDir),
          h("span", { class: "mono" }, wt.folder),
        ),
        risky(wt) || wt.locked
          ? h(
              "div",
              { class: "err" },
              [
                wt.locked
                  ? "trancada (git worktree unlock antes)"
                  : null,
                liveCount(wt.path) > 0
                  ? plural(liveCount(wt.path), "agente rodando", "agentes rodando")
                  : null,
                wt.error ? "status indisponível" : null,
                wt.dirty > 0
                  ? plural(
                      wt.dirty,
                      "alteração não commitada",
                      "alterações não commitadas",
                    )
                  : null,
                wt.unpushed > 0
                  ? plural(
                      wt.unpushed,
                      "commit fora de qualquer remoto",
                      "commits fora de qualquer remoto",
                    )
                  : null,
                wt.unpushed < 0 && !wt.error
                  ? "não deu para conferir commits fora do remoto"
                  : null,
              ]
                .filter(Boolean)
                .join(" · "),
            )
          : null,
      ),
    ),
  );

  btn.addEventListener("click", async () => {
    dlg.close();
    await stream("/api/remove", {
      feature: f.name,
      repos: checks.filter((c) => c.cb.checked).map((c) => c.wt.repo),
      force: force.checked,
      deleteBranch: delBranch.checked,
      forceBranch: forceBranch.checked,
      workspace: rmWs.checked,
      skipSnapshot: !keepSnapshot.checked,
    });
  });

  dlg.replaceChildren(
    ...[
      h("h3", {}, "Remover " + f.name),
      list,
      h(
        "label",
        {},
        force,
        "forçar (descarta alterações e commits locais listados acima)",
      ),
      h(
        "label",
        { class: "sub" },
        keepSnapshot,
        "salvar um snapshot antes de remover (só vale com “forçar”)",
      ),
      h(
        "label",
        {},
        delBranch,
        "apagar a branch local (git branch -d, só se já estiver mesclada)",
      ),
      h(
        "label",
        { class: "sub" },
        forceBranch,
        "forçar (git branch -D, apaga mesmo sem merge; o remoto não é tocado)",
      ),
      f.workspaces.length
        ? h(
            "label",
            {},
            rmWs,
            "apagar o .code-workspace se ele só apontar para esta feature",
          )
        : null,
      f.tasksDir
        ? h("p", { class: "muted" }, f.tasksDir + " não é apagada.")
        : null,
      h(
        "div",
        { class: "row end" },
        h("button", { type: "button", onclick: () => dlg.close() }, "Cancelar"),
        btn,
      ),
    ].filter(Boolean),
  );
  update();
  dlg.showModal();
}

// ---------- eventos ----------

$("#feature").addEventListener("input", schedulePlan);
for (const el of document.querySelectorAll("#opt-fetch, #opt-tasks, #opt-ws")) {
  el.addEventListener("change", schedulePlan);
}
$("#btn-create").addEventListener("click", create);
$("#btn-refresh").addEventListener("click", refresh);
$("#btn-fetch").addEventListener("click", async () => {
  await stream("/api/fetch", { repos: [] });
  for (const k of Object.keys(branches)) loadBranches(k, true);
});
$("#log-clear").addEventListener("click", () =>
  $("#log-body").replaceChildren(),
);

$("#btn-new").addEventListener("click", openCreate);
$("#close-create").addEventListener("click", () => $("#create-dlg").close());
const params = new URLSearchParams(location.search);
$("#search").value = params.get("q") || "";
function saveFilters() {
  const p = new URLSearchParams(location.search);
  p.set("view", view);
  if ($("#search").value) p.set("q", $("#search").value);
  else p.delete("q");
  if ($("#repo-filter").value) p.set("repo", $("#repo-filter").value);
  else p.delete("repo");
  history.replaceState(null, "", "?" + p + location.hash);
}
$("#search").addEventListener("input", () => {
  saveFilters();
  renderFeatures();
});
$("#repo-filter").addEventListener("change", () => {
  saveFilters();
  renderFeatures();
});
document.querySelectorAll("[data-view]").forEach((button) =>
  button.addEventListener("click", () => {
    view = button.dataset.view;
    saveFilters();
    renderFeatures();
  }),
);
const themeKeys = ["street", "neo", "grunge", "frasurbane"];
function applyTheme(theme) {
  const key = themeKeys.includes(theme) ? theme : "street";
  document.body.dataset.theme = key;
  $("#theme").value = key;
  document.querySelector('meta[name="theme-color"]').content = getComputedStyle(
    document.body,
  )
    .getPropertyValue("--paper")
    .trim();
}
try {
  applyTheme(localStorage.getItem("wt-theme"));
} catch {
  applyTheme("street");
}
$("#theme").addEventListener("change", (e) => {
  applyTheme(e.target.value);
  try {
    localStorage.setItem("wt-theme", e.target.value);
  } catch {}
});
window.addEventListener("popstate", () => {
  const p = new URLSearchParams(location.search);
  view = ["cards", "map", "archived"].includes(p.get("view"))
    ? p.get("view")
    : "cards";
  $("#search").value = p.get("q") || "";
  $("#repo-filter").value = p.get("repo") || "";
  renderFeatures();
});
// ---------- leitor de notas ----------
// Links com data-note abrem a nota aqui dentro; Ctrl/Cmd/Shift+clique ou botão
// do meio seguem o obsidian:// normal.

const reader = { history: [], pos: -1, seq: 0 };

function readerLink(target, embed) {
  const [path, alias] = target.split("|");
  const label = alias || path.split("#")[0].split("/").pop() || path;
  return h(
    "a",
    {
      class: embed ? "md-embed md-wiki" : "md-wiki",
      href: state?.vault ? noteHref(path) : "#",
      "data-note": path,
      title: path,
    },
    embed ? "📎 " + label : label,
  );
}

function scrollToHeading(anchor) {
  const body = $("#reader-body");
  if (!anchor) {
    $("#reader").scrollTop = 0;
    return;
  }
  const want = anchor.trim().toLowerCase();
  for (const hd of body.querySelectorAll(".md-h")) {
    if (hd.textContent.trim().toLowerCase() === want) {
      hd.scrollIntoView({ block: "start" });
      hd.classList.add("md-target");
      setTimeout(() => hd.classList.remove("md-target"), 1600);
      return;
    }
  }
}

// Nota de worktree só tem propriedades: abaixo delas entram os vínculos e os
// savepoints que falam dela (vinculados pelo frontmatter ou só citados).
function worktreeExtras(doc) {
  const props = Object.fromEntries(
    MD.splitFrontmatter(doc.text.replace(/\r\n?/g, "\n")).props.map(([k, v]) => [
      k,
      (v[0] || "").replace(/^(["'])(.*)\1$/, "$2"),
    ]),
  );
  const link = (value, empty) =>
    value ? readerLink(notePath(value), false) : h("span", { class: "unlinked" }, empty);
  const out = [
    h("h2", { class: "md-h" }, "Vínculos"),
    h(
      "dl",
      { class: "md-props-list md-links" },
      h("dt", {}, "Frente"),
      h("dd", {}, link(props.front, "não vinculada")),
      h("dt", {}, "Último savepoint"),
      h("dd", {}, link(props.last_savepoint, "nenhum savepoint declara esta branch")),
      h("dt", {}, "Origem do vínculo"),
      h("dd", {}, { auto: "automática (frontmatter dos savepoints)", manual: "manual (tela)" }[props.link_source] || "—"),
    ),
  ];
  const ms = doc.mentions || [];
  out.push(
    h("h2", { class: "md-h" }, "Citado em"),
    ms.length
      ? h(
          "ul",
          { class: "md-list md-mentions" },
          ms.map((m) =>
            h(
              "li",
              {},
              h("span", { class: "md-mention-date" }, m.date || ""),
              " ",
              readerLink(m.path + "|" + m.title, false),
              " ",
              h(
                "span",
                {
                  class: m.linked ? "md-tag" : "md-mention-kind",
                  title: m.linked
                    ? "declara branch/worktree no frontmatter"
                    : "só cita no texto; não vincula",
                },
                m.linked ? "vinculado" : "menção",
              ),
            ),
          ),
        )
      : h(
          "p",
          { class: "unlinked" },
          props.branch
            ? "Nenhum savepoint cita " + props.branch + "."
            : "Worktree em detached: só aparece aqui se um savepoint declarar worktree: no frontmatter.",
        ),
  );
  return out;
}

async function openDoc(link, push = true) {
  const target = notePath(link);
  const [path, anchor] = target.split("#");
  const dlg = $("#reader");
  const seq = ++reader.seq;
  $("#reader-title").textContent = path.split("/").pop();
  $("#reader-path").textContent = path;
  $("#reader-meta").textContent = "carregando…";
  $("#reader-obsidian").href = noteHref(path);
  if (!dlg.open) dlg.showModal();
  if (push) {
    reader.history = reader.history.slice(0, reader.pos + 1);
    reader.history.push(target);
    reader.pos = reader.history.length - 1;
  }
  $("#reader-back").disabled = reader.pos <= 0;
  $("#reader-fwd").disabled = reader.pos >= reader.history.length - 1;
  const body = $("#reader-body");
  try {
    const doc = await getJSON("/api/doc?path=" + encodeURIComponent(path));
    if (seq !== reader.seq) return;
    const readerCfg = (state && state.reader) || {};
    const asset = (url) => "/api/asset?doc=" + encodeURIComponent(doc.path) + "&path=" + encodeURIComponent(url);
    body.replaceChildren(MD.render(doc.text, { wiki: readerLink, assets: doc.assets || {}, asset, reader: readerCfg }));
    if (doc.path.startsWith(vaultScope() + "/worktrees/")) {
      // Vínculos e citações primeiro; as 19 propriedades geradas descem fechadas.
      body.prepend(...worktreeExtras(doc));
      const props = body.querySelector(".md-props");
      if (props) {
        props.open = false;
        body.append(props);
      }
    }
    $("#reader-path").textContent = doc.path;
    $("#reader-obsidian").href = noteHref(doc.path);
    const when = doc.modified ? new Date(doc.modified).toLocaleString("pt-BR") : "";
    $("#reader-meta").textContent =
      (doc.source === "docs" ? "~/projects/docs (canônico)" : "Vault") + (when ? " · editado " + when : "");
    scrollToHeading(anchor);
  } catch (err) {
    if (seq !== reader.seq) return;
    body.replaceChildren(h("p", { class: "err" }, "Não deu para abrir: " + err.message));
    $("#reader-meta").textContent = "";
  }
}

document.addEventListener("click", (e) => {
  const a = e.target.closest("a[data-note]");
  if (!a || state?.demo || !state?.vault) return;
  if (e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
  e.preventDefault();
  openDoc(a.dataset.note);
});
$("#reader-back").addEventListener("click", () => {
  if (reader.pos > 0) openDoc(reader.history[--reader.pos], false);
});
$("#reader-fwd").addEventListener("click", () => {
  if (reader.pos < reader.history.length - 1) openDoc(reader.history[++reader.pos], false);
});
$("#reader-close").addEventListener("click", () => $("#reader").close());
$("#reader-map").addEventListener("click", () => {
  const id = $("#reader-path").textContent;
  $("#reader").close();
  if (id) showInGraph(id);
});
$("#reader").addEventListener("close", () => {
  reader.history = [];
  reader.pos = -1;
});

// ---- Agentes: estado vindo de /api/agents (hooks + /proc), sem git nem Vault.

const AGENT_LABEL = {
  working: "trabalhando",
  waiting: "esperando você",
  done: "concluído",
  running: "ativo (sem hook)",
  ended: "encerrado",
};
let agentsState = { agents: [], counts: {} };
const agentSeen = new Map(); // agente/sessão -> último estado (para avisar)
let agentTimer = 0;

function agentKey(a) {
  return a.agent + "/" + (a.session || "pid" + a.pid);
}

function liveAgentsIn(path) {
  return agentsState.agents.filter((a) => a.alive && a.path === path);
}

function liveCount(path) {
  const n = agentsState.liveByPath?.[path];
  return n == null ? liveAgentsIn(path).length : n;
}

function fmtTokens(n) {
  if (!n) return "0";
  if (n >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, "") + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1).replace(/\.0$/, "") + "k";
  return String(n);
}

// Consumo vindo de /api/agents. Para quem não tem fonte numérica (agy) ou
// sessão ainda sem consumo (opencode), o card mostra "—" em vez de esconder.
function usageLabel(a) {
  const u = a.usage;
  if (!u) return a.agent === "agy" || a.agent === "opencode" ? "—" : "";
  const parts = [];
  if (u.in) parts.push("↑" + fmtTokens(u.in));
  if (u.out) parts.push("↓" + fmtTokens(u.out));
  if (u.hasCost) parts.push("US$" + Number(u.costUsd || 0).toFixed(2));
  return parts.join(" ") || "—";
}

function usageTitle(a) {
  const u = a.usage;
  if (u) {
    const parts = ["tokens: " + (u.in || 0) + " entrada, " + (u.out || 0) + " saída"];
    if (u.cacheRead) parts.push("cache lido " + u.cacheRead);
    if (u.cacheWrite) parts.push("cache escrito " + u.cacheWrite);
    if (u.reasoning) parts.push("raciocínio " + u.reasoning);
    if (u.hasCost) parts.push("custo estimado US$ " + Number(u.costUsd || 0).toFixed(4));
    if (u.source) parts.push("fonte " + u.source);
    return parts.join(" · ");
  }
  if (a.agent === "agy") return "tokens indisponíveis: o agy guarda a sessão em blob protobuf";
  if (a.agent === "opencode") return "consumo indisponível: sessão sem mensagens ou opencode CLI ausente";
  return "sem consumo registrado para esta sessão";
}

function concurrentChip(n, live) {
  return h(
    "span",
    {
      class: "agent-chip concurrent",
      title:
        live.map((a) => a.agent + "/" + (a.session || a.pid)).join("\n") +
        "\nDuas sessões na mesma worktree podem sobrescrever o trabalho uma da outra.",
    },
    "⚠ " + n + " vivos",
  );
}

function agentChip(a) {
  const label = AGENT_LABEL[a.state] || a.state;
  const u = usageLabel(a);
  return h(
    "span",
    {
      class: "agent-chip " + a.state,
      title:
        a.agent +
        " · " +
        label +
        (a.since ? " desde " + new Date(a.since).toLocaleTimeString("pt-BR") : "") +
        (a.session ? "\nsessão " + a.session : "") +
        "\n" +
        usageTitle(a),
    },
    a.agent + " · " + label,
    u ? h("span", { class: "agent-usage" }, " · " + u) : null,
  );
}

// ---- Comparar: diff só leitura contra a base ou contra outra worktree.

function patchLines(text) {
  const box = h("div", { class: "diff-lines" });
  for (const line of text.split("\n")) {
    const cls = line.startsWith("@@")
      ? "hunk"
      : line.startsWith("+") && !line.startsWith("+++")
        ? "add"
        : line.startsWith("-") && !line.startsWith("---")
          ? "del"
          : /^(diff --git|index |--- |\+\+\+ |new file|deleted file|similarity|rename |Binary)/.test(line)
            ? "meta"
            : "";
    box.append(h("div", { class: cls }, line || " "));
  }
  return box;
}

function splitPatch(patch) {
  const byPath = new Map();
  for (const chunk of patch.split(/^(?=diff --git )/m)) {
    const m = /^diff --git a\/.* b\/(.*)$/m.exec(chunk);
    if (m) byPath.set(m[1], chunk);
  }
  return byPath;
}

async function openDiff(wt) {
  const dlg = $("#dlg");
  const others = state.features
    .flatMap((f) => f.worktrees)
    .filter((o) => o.repo === wt.repo && o.path !== wt.path);
  const target = h(
    "select",
    {},
    h("option", { value: "base" }, "a base do repo (com o que não foi commitado)"),
    others.map((o) => h("option", { value: o.path }, o.folder + " · " + (o.branch || o.head))),
  );
  const out = h("div", { class: "diff-out" }, h("p", { class: "muted" }, "Carregando…"));
  const load = async () => {
    out.replaceChildren(h("p", { class: "muted" }, "Carregando…"));
    try {
      const q = new URLSearchParams({ repo: wt.repo, path: wt.path, against: target.value });
      const d = await getJSON("/api/diff?" + q);
      const chunks = splitPatch(d.patch);
      const add = d.files.reduce((n, f) => n + f.add, 0);
      const del = d.files.reduce((n, f) => n + f.del, 0);
      out.replaceChildren(h("div", {},
        h(
          "p",
          { class: "sub" },
          d.fromLabel + " → contra " + d.toLabel + " · ",
          plural(d.files.length, "arquivo", "arquivos"),
          " · ",
          h("span", { class: "diff-add" }, "+" + add),
          " ",
          h("span", { class: "diff-del" }, "−" + del),
        ),
        d.dirtyTo
          ? h("p", { class: "warn" }, "A outra worktree tem " + d.dirtyTo + " alterações sem commit, que não entram nesta comparação.")
          : null,
        d.truncated || d.filesCut
          ? h("p", { class: "warn" }, "Diff grande: a lista ou o patch foram cortados. Abra no editor para ver tudo.")
          : null,
        d.files.length || d.untracked.length ? null : h("p", { class: "muted" }, "Sem diferenças."),
        d.files.map((f) => {
          const det = h(
            "details",
            { class: "diff-file" },
            h(
              "summary",
              {},
              h("span", { class: "mono" }, f.oldPath ? f.oldPath + " → " + f.path : f.path),
              " ",
              f.binary
                ? h("span", { class: "muted" }, "binário")
                : [h("span", { class: "diff-add" }, "+" + f.add), " ", h("span", { class: "diff-del" }, "−" + f.del)],
            ),
          );
          det.addEventListener(
            "toggle",
            () => {
              if (det.open && det.children.length === 1)
                det.append(
                  chunks.has(f.path)
                    ? patchLines(chunks.get(f.path))
                    : h("p", { class: "muted" }, "Patch fora do corte."),
                );
            },
          );
          return det;
        }),
        d.untracked.length
          ? h(
              "details",
              { class: "diff-file" },
              h("summary", {}, plural(d.untracked.length, "arquivo novo sem git add", "arquivos novos sem git add")),
              h("ul", {}, d.untracked.map((n) => h("li", { class: "mono" }, n))),
            )
          : null,
      ));
    } catch (e) {
      out.replaceChildren(h("p", { class: "warn" }, "Não deu para comparar: " + e.message));
    }
  };
  target.addEventListener("change", load);
  dlg.replaceChildren(
    h("h3", {}, "Comparar " + (wt.feature || wt.folder)),
    h("label", {}, "Contra ", target),
    out,
    h("div", { class: "row" }, h("button", { onclick: () => dlg.close() }, "Fechar")),
  );
  dlg.classList.add("wide");
  dlg.addEventListener("close", () => dlg.classList.remove("wide"), { once: true });
  dlg.showModal();
  load();
}

// ---- Snapshots: retrato local do patch, dos untracked e dos commits fora do remoto.

function snapBytes(n) {
  if (!n) return "0 B";
  if (n < 1024) return n + " B";
  if (n < 1048576) return Math.round(n / 1024) + " KB";
  return (n / 1048576).toFixed(1) + " MB";
}

async function takeSnapshot(wt) {
  await stream("/api/snapshot", { repo: wt.repo, path: wt.path, reason: "manual" });
}

function snapName(s) {
  return String(s.dir || "").split(/[\\/]/).filter(Boolean).pop() || "";
}

async function restoreSnapshot(wt, s) {
  const when = updatedLabel(s.createdAt);
  if (!window.confirm("Restaurar o snapshot de " + when + "? A worktree precisa estar limpa."))
    return;
  $("#dlg").close();
  await stream("/api/snapshot/restore", {
    repo: wt.repo,
    path: wt.path,
    snapshot: snapName(s),
  });
}

async function openSnapshots(wt) {
  const dlg = $("#dlg");
  const out = h("div", { class: "snap-list" }, h("p", { class: "muted" }, "Carregando…"));
  const load = async () => {
    try {
      const q = new URLSearchParams({ repo: wt.repo, path: wt.path });
      const list = await getJSON("/api/snapshots?" + q);
      out.replaceChildren(
        list.length
          ? h(
              "ul",
              {},
              list.map((s) =>
                h(
                  "li",
                  {},
                  h(
                    "div",
                    {},
                    h("strong", {}, updatedLabel(s.createdAt)),
                    h(
                      "span",
                      { class: "muted" },
                      " · " + (s.reason === "pre-remove" ? "antes de remover" : "manual"),
                    ),
                  ),
                  h(
                    "div",
                    { class: "muted" },
                    (s.branch || s.head) + " · " + snapBytes(s.bytes),
                    s.omitted && s.omitted.length
                      ? " · " + plural(s.omitted.length, "arquivo omitido", "arquivos omitidos")
                      : "",
                    s.hasBundle ? " · bundle de commits" : "",
                  ),
                  s.agents && s.agents.length
                    ? h(
                        "div",
                        { class: "muted" },
                        "sessões: " + s.agents.map((a) => a.agent + "/" + a.session).join(", "),
                      )
                    : null,
                  h(
                    "button",
                    { class: "act", onclick: () => restoreSnapshot(wt, s) },
                    "Restaurar",
                  ),
                ),
              ),
            )
          : h("p", { class: "muted" }, "Nenhum snapshot para esta worktree."),
      );
    } catch (e) {
      out.replaceChildren(h("p", { class: "warn" }, "Não deu para listar: " + e.message));
    }
  };
  dlg.replaceChildren(
    h("h3", {}, "Snapshots " + (wt.feature || wt.folder)),
    h(
      "p",
      { class: "muted" },
      "Restaurar exige a worktree limpa. Segredos, deps e o copy do repo ficam de fora. Commits do bundle só aparecem como comando.",
    ),
    out,
    h(
      "div",
      { class: "row end" },
      h("button", { onclick: () => takeSnapshot(wt) }, "Snapshot agora"),
      h("button", { onclick: () => dlg.close() }, "Fechar"),
    ),
  );
  dlg.classList.add("wide");
  dlg.addEventListener("close", () => dlg.classList.remove("wide"), { once: true });
  dlg.showModal();
  load();
}

async function launchTerminal(path, agent, resume) {
  if (!resume) {
    const live = liveAgentsIn(path);
    if (live.length) {
      const names = [...new Set(live.map((a) => a.agent))].join(", ");
      const ok = window.confirm(
        "Já há " +
          plural(live.length, "agente vivo", "agentes vivos") +
          " nesta worktree (" +
          names +
          "). Duas sessões na mesma árvore podem sobrescrever o trabalho uma da outra. Abrir outra mesmo assim?",
      );
      if (!ok) return;
    }
  }
  try {
    await postJSON("/api/terminal", { path, agent, resume });
    logLine({
      level: "ok",
      msg: (agent ? agent + (resume ? " (retomar)" : "") : "terminal") + " aberto em " + path,
    });
  } catch (e) {
    logLine({ level: "error", msg: "terminal: " + e.message });
  }
}

// O "↺ retomar" só vale com sessão registrada do agente na worktree
// (agentsState.resumable, mesmo critério do servidor). O menu não é
// redesenhado a cada poll: paintAgents só religa/desliga o botão existente.
function paintResume(btn) {
  const { resumePath: path, resumeAgent: ag } = btn.dataset;
  const ok = (agentsState.resumable?.[path] || []).includes(ag);
  btn.disabled = !ok;
  btn.title = ok
    ? "Retomar a última sessão de " + ag + " nesta worktree"
    : "Nenhuma sessão de " + ag + " registrada nesta worktree";
  return btn;
}

function paintAgents() {
  const live = agentsState.agents.filter((a) => a.alive);
  const c = agentsState.counts || {};
  const bar = $("#agents-bar");
  const parts = [
    ["waiting", c.waiting, "✋"],
    ["working", c.working, "⚙"],
    ["done", c.done, "✓"],
    ["running", c.running, "●"],
  ].filter(([, n]) => n > 0);
  bar.replaceChildren(
    ...(parts.length
      ? parts.map(([st, n, icon]) =>
          h(
            "button",
            {
              class: "agent-chip small " + st,
              title: live
                .filter((a) => a.state === st)
                .map((a) => a.agent + " em " + (a.feature || a.folder))
                .join("\n"),
              onclick: () => {
                const a = live.find((x) => x.state === st);
                const slot =
                  a && document.querySelector('[data-agent-path="' + CSS.escape(a.path) + '"]');
                slot?.closest(".worktree-card")?.scrollIntoView({ block: "center" });
              },
            },
            icon + " " + n + " " + AGENT_LABEL[st],
          ),
        )
      : [h("span", { class: "muted" }, "Nenhum agente ativo")]),
  );
  bar.hidden = false;
  for (const slot of document.querySelectorAll("[data-agent-path]")) {
    const path = slot.dataset.agentPath;
    const mine = live.filter((a) => a.path === path);
    const n = liveCount(path);
    const nodes = mine.map(agentChip);
    if (n > 1) nodes.push(concurrentChip(n, mine));
    slot.replaceChildren(...nodes);
    const card = slot.closest(".worktree-card");
    card?.classList.toggle("agent-waiting", mine.some((a) => a.state === "waiting"));
    card?.classList.toggle("agent-concurrent", n > 1);
  }
  for (const btn of document.querySelectorAll("[data-resume-agent]")) paintResume(btn);
}

function notifyTransitions() {
  const on =
    localStorage.getItem("wt-notify") === "1" && window.Notification?.permission === "granted";
  for (const a of agentsState.agents) {
    const k = agentKey(a);
    const prev = agentSeen.get(k);
    agentSeen.set(k, a.state);
    if (on && prev && prev !== "waiting" && a.state === "waiting" && a.alive) {
      new Notification(a.agent + " esperando você", { body: a.feature || a.folder, tag: k });
    }
  }
}

async function pollAgents() {
  clearTimeout(agentTimer);
  if (state?.demo) return;
  if (!document.hidden) {
    try {
      agentsState = await getJSON("/api/agents");
      notifyTransitions();
      paintAgents();
    } catch {
      /* sem a barra o painel continua funcionando */
    }
  }
  agentTimer = setTimeout(pollAgents, 3000);
}

function setupNotify() {
  const b = $("#btn-notify");
  if (!window.Notification) return;
  const paint = () => {
    const on = localStorage.getItem("wt-notify") === "1" && Notification.permission === "granted";
    b.textContent = on ? "🔔 Avisando" : "🔕 Avisar";
    b.classList.toggle("on", on);
  };
  b.hidden = false;
  b.addEventListener("click", async () => {
    if (localStorage.getItem("wt-notify") === "1") localStorage.removeItem("wt-notify");
    else if ((await Notification.requestPermission()) === "granted")
      localStorage.setItem("wt-notify", "1");
    paint();
  });
  paint();
}

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) pollAgents();
});

setupNotify();
refresh().then(pollAgents);
