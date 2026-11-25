// Prévia estática (GitHub Pages): troca as chamadas /api/* do worktree-app
// pelos arquivos gerados por `worktree-app -export-static`. Leituras vêm dos
// JSON exportados; operações continuam desativadas, como na prévia local.
(() => {
  const base = new URL(".", location.href);
  const realFetch = window.fetch.bind(window);
  const OFF = "Prévia online: operação desativada. Rode o app localmente para usar.";
  const cache = {};
  const load = (name) => (cache[name] ||= realFetch(new URL(name, base)).then((r) => r.json()));

  const text = (body, status) => new Response(body, { status, headers: { "Content-Type": "text/plain; charset=utf-8" } });
  const json = (v) => new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });

  // Mesmo alvo do cleanDocLink/wikiTarget: tira [[ ]], alias, âncora e .md.
  const target = (link) => {
    let t = String(link || "").trim();
    if (t.startsWith("[[") && t.endsWith("]]")) t = t.slice(2, -2);
    t = t.split("|")[0].split("#")[0].trim();
    return t.replace(/\.md$/i, "");
  };
  const baseName = (p) => p.split("/").pop();

  async function docIndex() {
    const docs = await load("api/docs.json");
    if (!docIndex.map) {
      const byId = {}, byName = {};
      for (const [id, file] of Object.entries(docs)) {
        const k = target(id);
        byId[k] = file;
        (byName[baseName(k)] ||= []).push(file);
      }
      docIndex.map = { byId, byName };
    }
    return docIndex.map;
  }

  async function doc(link) {
    const t = target(link);
    const { byId, byName } = await docIndex();
    const file = byId[t] || (!t.includes("/") && byName[t] && byName[t].length === 1 ? byName[t][0] : null);
    return file ? json(await load("api/docs/" + file)) : text("nota não encontrada", 404);
  }

  async function graph(q) {
    const [g, st] = await Promise.all([load("api/graph.json"), load("api/state.json")]);
    const scope = (st.vaultScope || "worktree-app") + "/";
    const depth = q.get("depth") === "2" ? 2 : 1;
    const withMentions = q.get("mentions") !== "0";
    const withDiario = q.get("diario") === "1";
    const ids = Object.keys(g.nodes);
    const byName = (name) => {
      const hit = ids.filter((id) => target(baseName(id)) === name);
      return hit.length === 1 ? hit[0] : name;
    };
    const resp = { nodes: [], edges: [], built: g.built };
    const level = {}, order = [];
    let frontier = [];
    for (const raw of q.getAll("seed")) {
      let id = target(raw);
      if (!id.includes("/")) id = byName(id);
      if (!g.nodes[id]) id = ids.find((x) => target(x) === id) || id;
      if (!g.nodes[id] || !id.startsWith(scope)) {
        (resp.missing ||= []).push(raw);
        continue;
      }
      if (!(id in level)) {
        level[id] = 0;
        order.push(id);
        frontier.push(id);
      }
    }
    const allowed = (id) => {
      const n = g.nodes[id];
      return n && (withDiario || n.kind !== "diario") && !n.label.startsWith("_");
    };
    for (let d = 1; d <= depth && frontier.length; d++) {
      const limit = d > 1 ? 12 : 60;
      const next = [];
      for (const id of frontier) {
        const nbs = (g.nb[id] || [[], []])[withMentions ? 1 : 0];
        for (let i = 0; i < nbs.length; i++) {
          const nb = nbs[i];
          if (i >= limit || order.length >= 160) {
            resp.truncated = true;
            break;
          }
          if (nb in level || !allowed(nb)) continue;
          level[nb] = d;
          order.push(nb);
          next.push(nb);
        }
      }
      frontier = next;
    }
    for (const id of order) {
      const n = g.nodes[id];
      resp.nodes.push({ id, label: n.label, kind: n.kind, title: n.title || undefined, level: level[id] });
    }
    const out = (a) => g.out[a] || [];
    for (const a of order) {
      for (const b of out(a)) if (b in level) resp.edges.push({ from: a, to: b, kind: "link" });
      if (withMentions) {
        for (const b of g.mentions[a] || []) {
          if (b in level && a < b && !out(a).includes(b) && !out(b).includes(a)) resp.edges.push({ from: a, to: b, kind: "mention" });
        }
      }
    }
    resp.edges.sort((x, y) => (x.from !== y.from ? (x.from < y.from ? -1 : 1) : x.to < y.to ? -1 : x.to > y.to ? 1 : 0));
    return json(resp);
  }

  window.fetch = async (input, init) => {
    const u = new URL(typeof input === "string" ? input : input.url, location.origin);
    if (!u.pathname.startsWith("/api/")) return realFetch(input, init);
    const method = ((init && init.method) || "GET").toUpperCase();
    if (method !== "GET") return text(OFF, 403);
    switch (u.pathname) {
      case "/api/state":
        return json(await load("api/state.json"));
      case "/api/doc":
        return doc(u.searchParams.get("path"));
      case "/api/graph":
        return graph(u.searchParams);
      default:
        return text(OFF, 403);
    }
  };

  // Anexos do leitor: /api/asset?doc=<nota>&path=<link> -> vault/<caminho resolvido>.
  const assetURL = (raw) => {
    const u = new URL(raw, location.origin);
    const docPath = u.searchParams.get("doc") || "";
    const link = decodeURIComponent((u.searchParams.get("path") || "").split("#")[0]);
    const parts = link.startsWith("/") ? [] : docPath.split("/").slice(0, -1);
    for (const p of link.split("/")) {
      if (p === "..") parts.pop();
      else if (p && p !== ".") parts.push(p);
    }
    return new URL("vault/" + parts.map(encodeURIComponent).join("/"), base).href;
  };
  const fix = (el) => {
    for (const attr of ["src", "href", "data"]) {
      const v = el.getAttribute && el.getAttribute(attr);
      if (v && v.includes("/api/asset?")) el.setAttribute(attr, assetURL(v));
      else if (attr === "href" && v === "/" && el.classList && el.classList.contains("brand")) el.setAttribute("href", base.href);
    }
  };
  new MutationObserver((muts) => {
    for (const m of muts) {
      if (m.type === "attributes") fix(m.target);
      for (const n of m.addedNodes || []) {
        if (n.nodeType !== 1) continue;
        fix(n);
        n.querySelectorAll && n.querySelectorAll("[src],[href],[data]").forEach(fix);
      }
    }
  }).observe(document.documentElement, { subtree: true, childList: true, attributes: true, attributeFilter: ["src", "href", "data"] });
  document.addEventListener("DOMContentLoaded", () => document.querySelectorAll("[src],[href],[data]").forEach(fix));
})();
