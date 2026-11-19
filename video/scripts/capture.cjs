// Captura as telas usadas no vídeo (public/*.png) de um painel já rodando com
// o Vault de examples/. Uso:
//   WT_URL=http://127.0.0.1:7777/ CHROME_PATH=/caminho/do/chrome pnpm capture
// Sem CHROME_PATH, usa o Chromium do Playwright (npx playwright-core install chromium).
const path = require("path");
const { chromium } = require("playwright-core");

const OUT = path.join(__dirname, "..", "public");
const U = process.env.WT_URL || "http://127.0.0.1:7777/";
// Caminho exibido no lugar do root real, para não vazar o da sua máquina.
const ROOT_LABEL = process.env.WT_ROOT_LABEL || "~/projetos";

(async () => {
  const browser = await chromium.launch(process.env.CHROME_PATH ? { executablePath: process.env.CHROME_PATH } : {});
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 720 }, deviceScaleFactor: 2, locale: "pt-BR" });
  const page = await ctx.newPage();
  page.on("pageerror", (e) => console.log("pageerror", e.message));
  const mask = () =>
    page.evaluate((label) => {
      const r = document.querySelector("#root");
      if (r) r.textContent = label;
    }, ROOT_LABEL);
  const shot = async (name) => {
    await page.waitForTimeout(900);
    await mask();
    await page.screenshot({ path: path.join(OUT, name + ".png") });
    console.log("ok", name);
  };
  const toBoard = () => page.evaluate(() => document.querySelector("#board").scrollIntoView());

  for (const theme of ["street", "neo", "grunge", "frasurbane"]) {
    await page.goto(U);
    await page.evaluate((t) => localStorage.setItem("wt-theme", t), theme);
    await page.goto(U + "?view=cards");
    await page.waitForSelector("#features *");
    if (theme === "street") await shot("hero");
    await toBoard();
    await shot("cards-" + theme);
    if (theme === "street") {
      await page.evaluate(() => window.scrollBy(0, 520));
      await shot("cards-street-2");
    }
  }

  await page.evaluate(() => localStorage.setItem("wt-theme", "street"));
  await page.goto(U + "?view=cards");
  await page.waitForSelector("#features *");
  await page.click("#btn-new");
  await page.fill("#feature", "busca-rapida");
  for (const cb of await page.locator("#repo-rows input[type=checkbox]").all()) await cb.check();
  await page.waitForTimeout(1500);
  await shot("create");
  await page.keyboard.press("Escape");

  // openDoc é global no app.js; os caminhos são do Vault de examples/.
  const open = async (doc) => {
    await page.evaluate((d) => openDoc(d), doc);
    await page.waitForTimeout(1200);
  };
  await open("worktree-app/docs/savepoints/2026-11-14-checkout-pix");
  await shot("reader-savepoint");
  await open("worktree-app/docs/arquitetura/fluxo-de-worktrees");
  await shot("reader-fluxo");
  await page.evaluate(() => {
    const b = document.querySelector("#reader-body");
    b.scrollTop = b.scrollHeight;
  });
  await shot("reader-fluxo-anexos");

  await page.goto(U + "?view=map");
  await toBoard();
  await page.waitForTimeout(2500);
  await shot("map");
  await page.goto(U + "?view=map&center=" + encodeURIComponent("worktree-app/docs/kanbans/checkout") + "&depth=2");
  await toBoard();
  await page.waitForTimeout(2500);
  await shot("map-kanban");

  await browser.close();
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
