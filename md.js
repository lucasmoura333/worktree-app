"use strict";

// Markdown do Obsidian -> DOM. Tudo é montado com createElement/textContent:
// conteúdo das notas (logs, e-mails colados, payloads) nunca vira HTML.
// Cobre o que o Vault usa: frontmatter, títulos, listas aninhadas, tarefas,
// tabelas, código, citações/callouts, [[wikilinks]], embeds, #tags, ==marca==.
const MD = (() => {
  const el = (tag, cls, ...kids) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    for (const k of kids.flat()) if (k != null) n.append(k instanceof Node ? k : String(k));
    return n;
  };

  // ---------- inline ----------
  const INLINE = new RegExp(
    [
      "(?<code>`+)(?<codeText>[\\s\\S]*?[^`])\\k<code>(?!`)",
      "(?<embed>!\\[\\[(?<embedT>[^\\]\\n]+)\\]\\])",
      "(?<wiki>\\[\\[(?<wikiT>[^\\]\\n]+)\\]\\])",
      "(?<img>!\\[(?<imgAlt>[^\\]\\n]*)\\]\\((?<imgUrl>[^)\\s]+)\\))",
      "(?<link>\\[(?<linkText>[^\\]\\n]+)\\]\\((?<linkUrl>[^)\\s]+)\\))",
      "(?<bold>\\*\\*(?<boldT>[^*\\n]+?)\\*\\*|__(?<boldU>[^_\\n]+?)__)",
      "(?<strike>~~(?<strikeT>[^~\\n]+?)~~)",
      "(?<mark>==(?<markT>[^=\\n]+?)==)",
      "(?<em>\\*(?<emT>[^*\\s][^*\\n]*?)\\*|(?<![\\p{L}\\p{N}])_(?<emU>[^_\\n]+?)_(?![\\p{L}\\p{N}]))",
      "(?<url>https?:\\/\\/[^\\s<>()\\]]+[^\\s<>()\\].,;:!?'\"])",
      "(?<tag>(?<![\\p{L}\\p{N}#&/])#(?<tagT>[\\p{L}_][\\p{L}\\p{N}_/-]*))",
    ].join("|"),
    "gu",
  );

  function safeHref(url) {
    return /^(https?:|mailto:|obsidian:)/i.test(url) ? url : null;
  }

  // ---------- anexos ----------
  const FILE_EXT = /\.(pdf|xlsx?|csv|docx?|txt|zip|7z|rar|png|jpe?g|gif|webp|svg)$/i;

  const humanSize = (bytes) => {
    if (!bytes && bytes !== 0) return "";
    if (bytes < 1024) return bytes + " B";
    const units = ["KB", "MB", "GB"];
    let v = bytes / 1024;
    let i = 0;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    return v.toLocaleString("pt-BR", { maximumFractionDigits: 1 }) + " " + units[i];
  };

  const extOf = (url) => {
    const m = /\.([a-z0-9]+)(?:[?#]|$)/i.exec(url.split(/[?#]/)[0]);
    return m ? m[1].toUpperCase() : "ARQ";
  };

  const isLocalLink = (ctx, url) => !!ctx.asset && !/^(https?:|mailto:|obsidian:|#|\/\/)/i.test(url);

  function imgNode(src, alt, info, ctx) {
    const img = document.createElement("img");
    img.className = "md-img";
    img.src = src;
    img.alt = alt || "";
    img.loading = "lazy";
    img.decoding = "async";
    if (info && info.size) img.dataset.size = humanSize(info.size);
    const w = ctx.reader && ctx.reader.maxImageWidth;
    if (w) img.style.maxWidth = w + "px";
    return img;
  }

  function attNode(text, url, info, ctx) {
    const kind = info && info.kind ? info.kind : /\.pdf(?:[?#]|$)/i.test(url) ? "pdf" : "file";
    let name = text;
    if (!name) {
      try {
        name = decodeURIComponent(url.split(/[?#]/)[0].split("/").pop() || url);
      } catch {
        name = url;
      }
    }
    if (info && info.ok === false) {
      return el(
        "span",
        "md-att md-att-missing",
        el("span", "md-att-badge", extOf(url)),
        el("span", "md-att-body", el("strong", null, name), el("small", null, "não encontrado")),
      );
    }
    const a = el("a", "md-att");
    a.href = ctx.asset(url);
    a.target = "_blank";
    a.rel = "noopener";
    a.dataset.kind = kind;
    if (kind === "pdf") a.dataset.pdf = "1";
    const label = { pdf: "PDF", image: "IMAGEM", file: "ARQUIVO" }[kind] || "ARQUIVO";
    const detail = label + (info && info.size ? " · " + humanSize(info.size) : "");
    a.append(
      el("span", "md-att-badge", extOf(url)),
      el("span", "md-att-body", el("strong", null, name), el("small", null, detail)),
      el("span", "md-att-open", "abrir"),
    );
    return a;
  }

  function inline(text, ctx) {
    const out = [];
    let last = 0;
    INLINE.lastIndex = 0;
    for (const m of text.matchAll(INLINE)) {
      if (m.index > last) out.push(text.slice(last, m.index));
      last = m.index + m[0].length;
      const g = m.groups;
      if (g.code) out.push(el("code", null, g.codeText.trim()));
      else if (g.embed) out.push(ctx.wiki(g.embedT, true));
      else if (g.wiki) out.push(ctx.wiki(g.wikiT, false));
      else if (g.img) {
        const info = ctx.assets ? ctx.assets[g.imgUrl] : null;
        if (/^https?:/i.test(g.imgUrl)) {
          if (ctx.reader && ctx.reader.externalImages) out.push(imgNode(g.imgUrl, g.imgAlt, null, ctx));
          else out.push(el("span", "md-embed", "imagem externa: " + (g.imgAlt || g.imgUrl)));
        } else if (ctx.reader && ctx.reader.images === false) {
          out.push(el("span", "md-embed", g.imgAlt || g.imgUrl));
        } else if (info && info.ok === false) {
          out.push(el("span", "md-embed md-embed-missing", "imagem indisponível: " + (g.imgAlt || g.imgUrl)));
        } else if (isLocalLink(ctx, g.imgUrl) && (!info || info.ok)) {
          out.push(imgNode(ctx.asset(g.imgUrl), g.imgAlt, info, ctx));
        } else {
          out.push(el("span", "md-embed", g.imgAlt || g.imgUrl));
        }
      }
      else if (g.link) {
        const href = safeHref(g.linkUrl);
        if (href) {
          const a = el("a", "md-ext", inline(g.linkText, ctx));
          a.href = href;
          a.target = "_blank";
          a.rel = "noopener noreferrer";
          out.push(a);
        } else if (isLocalLink(ctx, g.linkUrl) && FILE_EXT.test(g.linkUrl.split(/[?#]/)[0])) {
          out.push(attNode(g.linkText, g.linkUrl, ctx.assets ? ctx.assets[g.linkUrl] : null, ctx));
        } else {
          // Link relativo do markdown: tenta como nota do Vault.
          let target = g.linkUrl;
          try {
            target = decodeURIComponent(target);
          } catch {}
          out.push(ctx.wiki(target.replace(/\.md$/, "") + "|" + g.linkText, false));
        }
      } else if (g.bold) out.push(el("strong", null, inline(g.boldT ?? g.boldU, ctx)));
      else if (g.strike) out.push(el("del", null, inline(g.strikeT, ctx)));
      else if (g.mark) out.push(el("mark", null, inline(g.markT, ctx)));
      else if (g.em) out.push(el("em", null, inline(g.emT ?? g.emU, ctx)));
      else if (g.url) {
        const a = el("a", "md-ext", g.url);
        a.href = g.url;
        a.target = "_blank";
        a.rel = "noopener noreferrer";
        out.push(a);
      } else if (g.tag) out.push(el("span", "md-tag", "#" + g.tagT));
    }
    if (last < text.length) out.push(text.slice(last));
    return out;
  }

  // ---------- frontmatter ----------
  function splitFrontmatter(text) {
    const m = /^---\n([\s\S]*?)\n---[ \t]*(?:\n|$)/.exec(text);
    if (!m) return { props: [], body: text };
    const props = [];
    for (const line of m[1].split("\n")) {
      const kv = /^([A-Za-z_][\w-]*):\s*(.*)$/.exec(line);
      if (kv) props.push([kv[1], kv[2] ? [kv[2]] : []]);
      else if (props.length && /^\s+-\s+/.test(line)) props[props.length - 1][1].push(line.replace(/^\s+-\s+/, ""));
      else if (props.length && /^\s+\S/.test(line)) props[props.length - 1][1].push(line.trim());
    }
    return { props, body: text.slice(m[0].length) };
  }

  const unquote = (v) => v.replace(/^(["'])(.*)\1$/, "$2");

  function renderProps(props, ctx) {
    const dl = el("dl", "md-props-list");
    for (const [k, vals] of props) {
      const dd = el("dd");
      vals.forEach((v, i) => {
        if (i) dd.append(el("br"));
        for (const item of v.startsWith("[") && !v.startsWith("[[") ? v.slice(1, -1).split(/,\s*(?![^[]*\]\])/) : [v])
          dd.append(...inline(unquote(item.trim()), ctx), " ");
      });
      dl.append(el("dt", null, k), dd);
    }
    const d = el("details", "md-props", el("summary", null, "Propriedades (" + props.length + ")"), dl);
    return d;
  }

  // ---------- blocos ----------
  const RE = {
    fence: /^\s*(```|~~~)\s*([\w+-]*)/,
    heading: /^(#{1,6})\s+(.*?)\s*#*\s*$/,
    hr: /^\s{0,3}([-*_])(\s*\1){2,}\s*$/,
    quote: /^\s{0,3}>\s?/,
    item: /^(\s*)([-*+]|\d+[.)])\s+(.*)$/,
    tableSep: /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/,
  };
  const indentOf = (s) => s.replace(/\t/g, "    ").length;
  const isBlockStart = (l) =>
    RE.fence.test(l) || RE.heading.test(l) || RE.hr.test(l) || RE.quote.test(l) || RE.item.test(l) || /^\s*\|/.test(l);

  function cells(line) {
    return line.trim().replace(/^\|/, "").replace(/\|$/, "").split(/(?<!\\)\|/).map((c) => c.trim());
  }

  function listBlock(lines, ctx) {
    const root = el("div");
    const stack = []; // { indent, list }
    for (const { indent, marker, text } of lines) {
      while (stack.length && indent < stack[stack.length - 1].indent) stack.pop();
      if (!stack.length || indent > stack[stack.length - 1].indent) {
        const list = el(/\d/.test(marker) ? "ol" : "ul", "md-list");
        const parentLi = stack.length ? stack[stack.length - 1].list.lastElementChild : null;
        (parentLi || root).append(list);
        stack.push({ indent, list });
      }
      const li = el("li");
      const task = /^\[([ xX/-])\]\s*(.*)$/.exec(text);
      if (task) {
        const box = document.createElement("input");
        box.type = "checkbox";
        box.disabled = true;
        box.checked = task[1] !== " ";
        li.className = "md-task" + (box.checked ? " done" : "");
        li.append(box, " ", ...inline(task[2], ctx));
      } else li.append(...inline(text, ctx));
      stack[stack.length - 1].list.append(li);
    }
    return [...root.childNodes];
  }

  // Parágrafo com só uma imagem/anexo vira figura/card de bloco (sem <p> em volta).
  function finishParagraph(p, ctx) {
    const kids = [...p.childNodes].filter((n) => n.nodeType !== 3 || n.textContent.trim());
    if (kids.length !== 1) return p;
    const only = kids[0];
    if (only.nodeType !== 1) return p;

    if (only.tagName === "IMG" && only.classList.contains("md-img")) {
      const cap = el("figcaption", null, only.alt || "");
      if (only.dataset.size) cap.append(" · " + only.dataset.size);
      return el("figure", "md-figure", only, cap);
    }
    if (only.tagName === "A" && only.classList.contains("md-att")) {
      const wrap = el("div", "md-att-row", only);
      const mode = (ctx.reader && ctx.reader.pdf) || "preview";
      if (only.dataset.pdf === "1" && mode === "preview") {
        const d = el("details", "md-att-preview", el("summary", null, "Prévia do PDF"));
        const em = document.createElement("embed");
        em.type = "application/pdf";
        em.src = only.href;
        d.append(em);
        wrap.append(d);
      }
      return wrap;
    }
    return p;
  }

  function blocks(lines, ctx) {
    const out = [];
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];
      if (!line.trim()) {
        i++;
        continue;
      }
      let m;
      if ((m = RE.fence.exec(line))) {
        const close = m[1];
        const body = [];
        for (i++; i < lines.length && !lines[i].trim().startsWith(close); i++) body.push(lines[i]);
        i++;
        const pre = el("pre", "md-code", el("code", null, body.join("\n")));
        if (m[2]) pre.dataset.lang = m[2];
        out.push(pre);
        continue;
      }
      if ((m = RE.heading.exec(line))) {
        out.push(el("h" + m[1].length, "md-h", inline(m[2], ctx)));
        i++;
        continue;
      }
      if (RE.hr.test(line)) {
        out.push(el("hr"));
        i++;
        continue;
      }
      if (/^\s*\|/.test(line) && i + 1 < lines.length && RE.tableSep.test(lines[i + 1])) {
        const head = cells(line);
        const table = el("table", "md-table");
        const tr = el("tr", null, head.map((c) => el("th", null, inline(c, ctx))));
        table.append(el("thead", null, tr));
        const tb = el("tbody");
        for (i += 2; i < lines.length && /^\s*\|/.test(lines[i]); i++)
          tb.append(el("tr", null, cells(lines[i]).map((c) => el("td", null, inline(c, ctx)))));
        table.append(tb);
        out.push(el("div", "md-table-wrap", table));
        continue;
      }
      if (RE.quote.test(line)) {
        const body = [];
        for (; i < lines.length && RE.quote.test(lines[i]); i++) body.push(lines[i].replace(RE.quote, ""));
        const callout = /^\[!(\w+)\][-+]?\s*(.*)$/.exec(body[0] || "");
        const q = el("blockquote", callout ? "md-callout md-callout-" + callout[1].toLowerCase() : "md-quote");
        if (callout) {
          q.append(el("div", "md-callout-title", inline(callout[2] || callout[1], ctx)));
          body.shift();
        }
        q.append(...blocks(body, ctx));
        out.push(q);
        continue;
      }
      if (RE.item.test(line)) {
        const items = [];
        while (i < lines.length) {
          const it = RE.item.exec(lines[i]);
          if (it) {
            items.push({ indent: indentOf(it[1]), marker: it[2], text: it[3] });
            i++;
          } else if (lines[i].trim() && /^\s+/.test(lines[i]) && items.length) {
            items[items.length - 1].text += " " + lines[i].trim();
            i++;
          } else if (!lines[i].trim() && i + 1 < lines.length && RE.item.test(lines[i + 1])) {
            i++;
          } else break;
        }
        out.push(...listBlock(items, ctx));
        continue;
      }
      const para = [];
      for (; i < lines.length && lines[i].trim() && (!para.length || !isBlockStart(lines[i])); i++) para.push(lines[i].trim());
      const p = el("p");
      para.forEach((l, j) => {
        if (j) p.append(el("br"));
        p.append(...inline(l, ctx));
      });
      out.push(finishParagraph(p, ctx));
    }
    return out;
  }

  // ctx.wiki(target, embed) devolve o nó de um [[wikilink]].
  function render(text, ctx) {
    text = text.replace(/\r\n?/g, "\n").replace(/%%[\s\S]*?%%/g, "");
    const { props, body } = splitFrontmatter(text);
    const frag = document.createDocumentFragment();
    if (props.length) {
      const d = renderProps(props, ctx);
      // Nota só de propriedades (as geradas do wt): nada a esconder.
      if (!body.trim()) d.open = true;
      frag.append(d);
    }
    frag.append(...blocks(body.split("\n"), ctx));
    return frag;
  }

  return { render, splitFrontmatter };
})();
