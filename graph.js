"use strict";

// Visão Mapa: grafo local das notas, análogo ao do Obsidian.
// Sem centro: parte de todas as worktrees filtradas. Com ?center=<nota>: a nota
// no meio e os vizinhos em volta. Clique recentra; duplo clique (ou clique no
// centro) abre no leitor. Estado na URL: center, depth, mentions, diario.

const SVG_NS = "http://www.w3.org/2000/svg";
const graphUI = { seq: 0, timer: 0 };
const KIND_LABEL = {
  worktree: "Worktree",
  kanban: "Frente / Kanban",
  savepoint: "Savepoint",
  diario: "Diário",
  doc: "Outra nota",
};

function svg(tag, attrs, ...kids) {
  const el = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs || {})) if (v != null) el.setAttribute(k, String(v));
  for (const kid of kids.flat()) if (kid != null) el.append(kid instanceof Node ? kid : String(kid));
  return el;
}

function graphParams() {
  const p = new URLSearchParams(location.search);
  return {
    center: p.get("center") || "",
    depth: p.get("depth") === "2" ? 2 : 1,
    mentions: p.get("mentions") !== "0",
    diario: p.get("diario") === "1",
  };
}

function setGraphParams(patch, push) {
  const p = new URLSearchParams(location.search);
  p.set("view", "map");
  for (const [k, v] of Object.entries(patch)) {
    if (v === "" || v == null) p.delete(k);
    else p.set(k, v);
  }
  history[push ? "pushState" : "replaceState"](null, "", "?" + p + location.hash);
  renderFeatures();
}

// Abre o mapa centrado numa nota (usado pelo leitor).
function showInGraph(id) {
  view = "map";
  setGraphParams({ center: id }, true);
  $("#features")?.scrollIntoView({ block: "start" });
}

function renderGraph(box, items) {
  const opt = graphParams();
  if (!state?.vault || previewOnly()) {
    box.replaceChildren(h("p", { class: "empty" }, "O mapa precisa do Vault configurado."));
    return;
  }
  const seeds = opt.center
    ? [opt.center]
    : items.map(({ wt }) => wt.connection?.note).filter(Boolean);
  const label = (id) => id.split("/").pop();

  const toggle = (name, on, text) =>
    h(
      "label",
      { class: "graph-check" },
      h("input", {
        type: "checkbox",
        checked: on,
        onchange: (e) => setGraphParams({ [name]: e.target.checked ? "1" : "0" }),
      }),
      text,
    );
  const depthBtn = (d) =>
    h(
      "button",
      { "aria-pressed": String(opt.depth === d), onclick: () => setGraphParams({ depth: d === 2 ? "2" : "" }) },
      String(d),
    );
  const stage = h("div", { class: "graph-stage" }, h("p", { class: "graph-msg" }, "Montando o grafo…"));
  const status = h("span", { class: "graph-status muted" });
  box.replaceChildren(
    h(
      "div",
      { class: "graph" },
      h(
        "div",
        { class: "graph-bar" },
        h(
          "div",
          { class: "graph-title" },
          opt.center
            ? [
                h("button", { class: "icon-button", title: "Visão geral", "aria-label": "Visão geral", onclick: () => setGraphParams({ center: "" }, true) }, "←"),
                h("strong", { title: opt.center }, "◎ " + label(opt.center)),
                h("button", { class: "graph-read", onclick: () => openDoc(opt.center) }, "Ler nota"),
              ]
            : h("strong", {}, "Visão geral · " + plural(seeds.length, "worktree", "worktrees")),
        ),
        h(
          "div",
          { class: "graph-controls" },
          h("span", { class: "graph-depth", role: "group", "aria-label": "Profundidade" }, "Profundidade ", depthBtn(1), depthBtn(2)),
          toggle("mentions", opt.mentions, "Menções"),
          toggle("diario", opt.diario, "Diários"),
          h("button", { class: "icon-button", title: "Reler o Vault", "aria-label": "Reler o Vault", onclick: () => drawGraph(stage, status, seeds, opt, true) }, "↻"),
        ),
      ),
      stage,
      h(
        "div",
        { class: "graph-legend" },
        Object.entries(KIND_LABEL).map(([k, t]) => h("span", {}, h("i", { class: "dot k-" + k }), t)),
        h("span", {}, h("i", { class: "line" }), "link"),
        h("span", {}, h("i", { class: "line dashed" }), "menção no texto"),
        status,
      ),
    ),
  );
  if (!seeds.length) {
    stage.replaceChildren(h("p", { class: "graph-msg" }, "Nenhuma worktree com nota no Vault para este filtro."));
    return;
  }
  clearTimeout(graphUI.timer);
  // A busca re-renderiza a cada tecla: espera parar de digitar.
  graphUI.timer = setTimeout(() => drawGraph(stage, status, seeds, opt, false), 180);
}

async function drawGraph(stage, status, seeds, opt, refresh) {
  const seq = ++graphUI.seq;
  const q = new URLSearchParams();
  for (const s of seeds) q.append("seed", s);
  q.set("depth", opt.depth);
  q.set("mentions", opt.mentions ? "1" : "0");
  q.set("diario", opt.diario ? "1" : "0");
  if (refresh) q.set("refresh", "1");
  let g;
  try {
    g = await getJSON("/api/graph?" + q);
  } catch (err) {
    if (seq === graphUI.seq) stage.replaceChildren(h("p", { class: "graph-msg err" }, "Não deu para montar o grafo: " + err.message));
    return;
  }
  if (seq !== graphUI.seq) return;
  if (!g.nodes.length) {
    stage.replaceChildren(h("p", { class: "graph-msg" }, "Essa nota não está no índice (fora de " + ((state && state.vaultScope) || "vault.scope") + "?)."));
    return;
  }
  status.textContent =
    plural(g.nodes.length, "nota", "notas") +
    " · " +
    plural(g.edges.length, "ligação", "ligações") +
    (g.truncated ? " · cortado para caber" : "") +
    " · índice de " +
    new Date(g.built).toLocaleTimeString("pt-BR");
  layoutGraph(g, opt.center);
  stage.replaceChildren(graphSVG(g, opt.center), graphList(g));
}

function hash(s) {
  let x = 2166136261;
  for (let i = 0; i < s.length; i++) x = Math.imul(x ^ s.charCodeAt(i), 16777619);
  return (x >>> 0) / 4294967296;
}

// Força simples, rodada uma vez (sem animação): repulsão entre todos, mola nas
// arestas e gravidade para o centro. Determinística pelo hash do id.
function layoutGraph(g, center) {
  const byId = new Map(g.nodes.map((n) => [n.id, n]));
  const seeds = g.nodes.filter((n) => n.level === 0);
  seeds.forEach((n, i) => {
    const r = seeds.length > 1 ? 70 * Math.sqrt(seeds.length) : 0;
    const a = (2 * Math.PI * i) / seeds.length;
    n.x = r * Math.cos(a);
    n.y = r * Math.sin(a);
  });
  const parent = new Map();
  for (const e of g.edges) {
    const a = byId.get(e.from), b = byId.get(e.to);
    if (a.level < b.level && !parent.has(b.id)) parent.set(b.id, a);
    if (b.level < a.level && !parent.has(a.id)) parent.set(a.id, b);
  }
  for (const level of [1, 2]) {
    for (const n of g.nodes.filter((m) => m.level === level)) {
      const p = parent.get(n.id) || { x: 0, y: 0 };
      const a = 2 * Math.PI * hash(n.id);
      n.x = p.x + 90 * Math.cos(a);
      n.y = p.y + 90 * Math.sin(a);
    }
  }
  const N = g.nodes;
  // Nó com rótulo sempre visível empurra mais, para os nomes não se encavalarem.
  for (const n of N) n.w = n.level === 0 || n.kind === "worktree" || n.kind === "kanban" ? 2.2 : 1;
  const iters = 280;
  for (let it = 0; it < iters; it++) {
    const alpha = 1 - it / iters;
    for (const n of N) n.dx = n.dy = 0;
    for (let i = 0; i < N.length; i++) {
      for (let j = i + 1; j < N.length; j++) {
        const a = N[i], b = N[j];
        let dx = a.x - b.x, dy = a.y - b.y;
        let d2 = dx * dx + dy * dy;
        if (d2 < 1) {
          dx = hash(a.id + b.id) - 0.5;
          dy = 0.5 - hash(b.id + a.id);
          d2 = 1;
        }
        const f = (2600 * a.w * b.w) / d2;
        const d = Math.sqrt(d2);
        a.dx += (dx / d) * f;
        a.dy += (dy / d) * f;
        b.dx -= (dx / d) * f;
        b.dy -= (dy / d) * f;
      }
    }
    for (const e of g.edges) {
      const a = byId.get(e.from), b = byId.get(e.to);
      const dx = b.x - a.x, dy = b.y - a.y;
      const d = Math.max(Math.hypot(dx, dy), 0.01);
      const f = (d - (e.kind === "mention" ? 120 : 85)) * 0.06;
      a.dx += (dx / d) * f;
      a.dy += (dy / d) * f;
      b.dx -= (dx / d) * f;
      b.dy -= (dy / d) * f;
    }
    const cap = 2 + 18 * alpha;
    for (const n of N) {
      n.dx -= n.x * 0.012;
      n.dy -= n.y * 0.012;
      if (center && n.id === center) continue; // centro fica fixo no meio
      const m = Math.hypot(n.dx, n.dy);
      const s = m > cap ? cap / m : 1;
      n.x += n.dx * s;
      n.y += n.dy * s;
    }
  }
}

function graphSVG(g, center) {
  const pad = 70;
  const xs = g.nodes.map((n) => n.x), ys = g.nodes.map((n) => n.y);
  const vb = {
    x: Math.min(...xs) - pad,
    y: Math.min(...ys) - pad,
    w: Math.max(...xs) - Math.min(...xs) + 2 * pad,
    h: Math.max(...ys) - Math.min(...ys) + 2 * pad,
  };
  const minSide = 420;
  if (vb.w < minSide) { vb.x -= (minSide - vb.w) / 2; vb.w = minSide; }
  if (vb.h < minSide * 0.6) { vb.y -= (minSide * 0.6 - vb.h) / 2; vb.h = minSide * 0.6; }
  const root = svg("svg", { class: "graph-svg", viewBox: `${vb.x} ${vb.y} ${vb.w} ${vb.h}`, role: "img", "aria-label": "Grafo de notas" });
  const byId = new Map(g.nodes.map((n) => [n.id, n]));
  const adj = new Map(g.nodes.map((n) => [n.id, new Set()]));
  const edgeEls = [];
  const edgesG = svg("g", { class: "g-edges" });
  for (const e of g.edges) {
    const a = byId.get(e.from), b = byId.get(e.to);
    adj.get(a.id).add(b.id);
    adj.get(b.id).add(a.id);
    const line = svg("line", { x1: a.x, y1: a.y, x2: b.x, y2: b.y, class: "g-edge " + e.kind });
    line.dataset.a = a.id;
    line.dataset.b = b.id;
    edgeEls.push(line);
    edgesG.append(line);
  }
  const few = g.nodes.length <= 24;
  const nodesG = svg("g", { class: "g-nodes" });
  const nodeEls = new Map();
  for (const n of g.nodes) {
    const isCenter = n.id === center;
    const r = isCenter ? 11 : n.kind === "worktree" ? 8 : n.level === 0 ? 8 : 5.5;
    const always = isCenter || n.level === 0 || n.kind === "worktree" || n.kind === "kanban" || few;
    const text = n.label.length > 34 ? n.label.slice(0, 32) + "…" : n.label;
    const el = svg(
      "g",
      {
        class: `g-node k-${n.kind}${isCenter ? " center" : ""}${always ? " show-label" : ""}`,
        transform: `translate(${n.x},${n.y})`,
        tabindex: 0,
        role: "button",
        "aria-label": (KIND_LABEL[n.kind] || "Nota") + ": " + (n.title || n.label) + (isCenter ? " (abrir)" : " (centralizar)"),
      },
      svg("title", {}, (n.title ? n.title + "\n" : "") + n.id + (isCenter ? "\nclique: ler" : "\nclique: centralizar · duplo clique: ler")),
      svg("circle", { r }),
      svg("text", { x: r + 4, y: 4 }, text),
    );
    const act = (read) => (read || isCenter ? openDoc(n.id) : setGraphParams({ center: n.id }, true));
    let clickTimer = 0;
    el.addEventListener("click", () => {
      if (root.dataset.dragged === "1") return;
      clearTimeout(clickTimer);
      clickTimer = setTimeout(() => act(false), 220);
    });
    el.addEventListener("dblclick", () => {
      clearTimeout(clickTimer);
      act(true);
    });
    el.addEventListener("keydown", (e) => {
      if (e.key === "Enter") act(e.shiftKey);
    });
    const hl = (on) => {
      root.classList.toggle("focus", on);
      el.classList.toggle("hl", on);
      for (const id of adj.get(n.id)) nodeEls.get(id).classList.toggle("hl", on);
      for (const l of edgeEls) if (l.dataset.a === n.id || l.dataset.b === n.id) l.classList.toggle("hl", on);
    };
    el.addEventListener("mouseenter", () => hl(true));
    el.addEventListener("mouseleave", () => hl(false));
    el.addEventListener("focus", () => hl(true));
    el.addEventListener("blur", () => hl(false));
    nodeEls.set(n.id, el);
    nodesG.append(el);
  }
  root.append(edgesG, nodesG);
  panZoom(root, vb);
  return root;
}

// Roda do mouse aproxima no ponteiro; arrastar o fundo move.
function panZoom(root, vb) {
  const apply = () => root.setAttribute("viewBox", `${vb.x} ${vb.y} ${vb.w} ${vb.h}`);
  root.addEventListener(
    "wheel",
    (e) => {
      e.preventDefault();
      const rect = root.getBoundingClientRect();
      const fx = (e.clientX - rect.left) / rect.width, fy = (e.clientY - rect.top) / rect.height;
      const k = e.deltaY > 0 ? 1.15 : 1 / 1.15;
      const nw = Math.min(Math.max(vb.w * k, 120), 12000), nh = (nw / vb.w) * vb.h;
      vb.x += (vb.w - nw) * fx;
      vb.y += (vb.h - nh) * fy;
      vb.w = nw;
      vb.h = nh;
      apply();
    },
    { passive: false },
  );
  let drag = null;
  root.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    drag = { x: e.clientX, y: e.clientY, vx: vb.x, vy: vb.y, moved: false };
    root.dataset.dragged = "0";
  });
  root.addEventListener("pointermove", (e) => {
    if (!drag) return;
    const rect = root.getBoundingClientRect();
    const dx = e.clientX - drag.x, dy = e.clientY - drag.y;
    if (!drag.moved && Math.hypot(dx, dy) < 4) return;
    if (!drag.moved) root.setPointerCapture(e.pointerId);
    drag.moved = true;
    root.dataset.dragged = "1";
    vb.x = drag.vx - (dx * vb.w) / rect.width;
    vb.y = drag.vy - (dy * vb.h) / rect.height;
    apply();
  });
  const end = () => {
    drag = null;
    setTimeout(() => (root.dataset.dragged = "0"), 0);
  };
  root.addEventListener("pointerup", end);
  root.addEventListener("pointercancel", end);
}

// Alternativa em texto (teclado, leitor de tela, celular).
function graphList(g) {
  const groups = {};
  for (const n of g.nodes) (groups[n.kind] ||= []).push(n);
  return h(
    "details",
    { class: "graph-list" },
    h("summary", {}, "Lista das " + plural(g.nodes.length, "nota", "notas")),
    Object.keys(KIND_LABEL)
      .filter((k) => groups[k])
      .map((k) =>
        h(
          "div",
          {},
          h("h4", {}, KIND_LABEL[k] + " (" + groups[k].length + ")"),
          h(
            "ul",
            {},
            groups[k].map((n) =>
              h(
                "li",
                {},
                readerLink(n.id + "|" + (n.title || n.label), false),
                " ",
                h("button", { class: "graph-center-btn", title: "Centralizar no mapa", onclick: () => setGraphParams({ center: n.id }, true) }, "◎"),
              ),
            ),
          ),
        ),
      ),
  );
}
