// Captura as cenas da central de agentes (public/agents-*.png, diff-*.png,
// snapshots.png) de um painel já rodando com repos de EXEMPLO. Uso:
//   WT_URL=http://127.0.0.1:7795/ WT_ROOT_REAL=/caminho/do/root \
//   WT_WAITING_CMD="hook.sh claude s1 /caminho/wt Notification permission_prompt" \
//   CHROME_PATH=/usr/bin/google-chrome pnpm capture:agents
//
// - WT_ROOT_REAL (e WT_STATE_REAL, opcional) são trocados por WT_ROOT_LABEL
//   (padrão ~/projetos) em todo texto da página, para não vazar caminhos.
// - WT_WAITING_CMD, se definido, roda entre as capturas "trabalhando" e
//   "esperando você" (ex.: um hook com Notification). Sem ele, a cena de espera
//   é pulada.
// - Nunca clica em ">_" nem em "▶ agente": só abre o menu para a captura.
const path = require("path");
const { execSync } = require("child_process");
const { chromium } = require("playwright-core");

const OUT = path.join(__dirname, "..", "public");
const U = process.env.WT_URL || "http://127.0.0.1:7777/";
const ROOT_LABEL = process.env.WT_ROOT_LABEL || "~/projetos";
const REAL = [process.env.WT_ROOT_REAL, process.env.WT_STATE_REAL].filter(Boolean);
const CARD = process.env.WT_CARD || "dev.checkout-novo"; // pasta da worktree em destaque

(async () => {
  const browser = await chromium.launch(process.env.CHROME_PATH ? { executablePath: process.env.CHROME_PATH } : {});
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 720 }, deviceScaleFactor: 2, locale: "pt-BR" });
  const page = await ctx.newPage();
  page.on("pageerror", (e) => console.log("pageerror", e.message));

  const mask = () =>
    page.evaluate(
      ({ label, real }) => {
        const r = document.querySelector("#root");
        if (r) r.textContent = label;
        if (!real.length) return;
        const fix = (s) => real.reduce((acc, p) => acc.split(p).join(label), s);
        const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
        for (let n = walk.nextNode(); n; n = walk.nextNode()) {
          if (real.some((p) => n.nodeValue.includes(p))) n.nodeValue = fix(n.nodeValue);
        }
        for (const el of document.querySelectorAll("[title]")) el.title = fix(el.title);
      },
      { label: ROOT_LABEL, real: REAL },
    );
  const shot = async (name, wait = 900) => {
    await page.waitForTimeout(wait);
    await mask();
    await page.screenshot({ path: path.join(OUT, name + ".png") });
    console.log("ok", name);
  };
  const card = () =>
    page.locator("article.worktree-card", { has: page.locator(`[data-agent-path$="/${CARD}"]`) }).first();
  const closeMenus = () =>
    page.evaluate(() => document.querySelectorAll("details.more-actions[open]").forEach((d) => (d.open = false)));
  // Topo do card a ~110 px do alto da tela, para a legenda do vídeo não cobrir.
  const toCard = () => card().evaluate((el) => window.scrollBy(0, el.getBoundingClientRect().top - 110));
  const menu = async (label) => {
    await closeMenus(); // o Escape não fecha <details>
    await card().locator("details.more-actions > summary").click();
    if (label) await card().locator("details.more-actions button", { hasText: label }).click();
  };

  await page.goto(U);
  await page.evaluate(() => localStorage.setItem("wt-theme", "street"));
  await page.goto(U + "?view=cards");
  await page.waitForSelector("#features *");
  await page.waitForSelector("#agents-bar:not([hidden])", { timeout: 10000 });

  await page.waitForTimeout(1500);

  // 2. menu do card com terminal / agente novo / retomar (sem clicar).
  // O menu abre para cima: o rodapé do card fica embaixo da tela.
  await card()
    .locator("details.more-actions")
    .evaluate((el) => window.scrollBy(0, el.getBoundingClientRect().top - 600));
  await menu();
  await shot("agents-menu");
  await closeMenus();

  // 3. o agente passa a esperar você (o polling é de 3 s)
  if (process.env.WT_WAITING_CMD) {
    execSync(process.env.WT_WAITING_CMD, { stdio: "inherit", shell: "/bin/sh" });
    await page.waitForFunction(() => /esperando/i.test(document.querySelector("#agents-bar")?.textContent || ""), null, {
      timeout: 15000,
    });
    await page.evaluate(() => window.scrollTo(0, 0));
    await shot("agents-bar", 600);
    await toCard();
    await shot("agents-waiting", 600);
  }

  // 4. Comparar contra a base e contra outra worktree
  await menu("Comparar");
  await page.waitForSelector(".diff-file", { timeout: 10000 });
  await shot("diff-base", 1200);
  const firstFile = page.locator(".diff-file").first();
  await firstFile.click().catch(() => {});
  await shot("diff-file", 1000);
  await page.keyboard.press("Escape");

  // 5. Snapshots da worktree (crie um antes via API ou pelo menu)
  await menu("Snapshots");
  await page.waitForSelector("dialog[open]");
  await shot("snapshots", 1200);
  await page.keyboard.press("Escape");

  await browser.close();
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
