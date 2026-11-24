// Converte os quadros HTML de terminal-frames.py em PNG (public/term/*.png).
// Uso: node scripts/capture-terminal.cjs <pasta-dos-html>   (CHROME_PATH opcional)
const fs = require("fs");
const path = require("path");
const { chromium } = require("playwright-core");

const SRC = path.resolve(process.argv[2] || "out/term");
const OUT = path.join(__dirname, "..", "public", "term");

(async () => {
  fs.rmSync(OUT, { recursive: true, force: true });
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch(process.env.CHROME_PATH ? { executablePath: process.env.CHROME_PATH } : {});
  const page = await browser.newPage({ viewport: { width: 1220, height: 640 }, deviceScaleFactor: 2 });
  const files = fs.readdirSync(SRC).filter((f) => f.endsWith(".html")).sort();
  for (const f of files) {
    await page.goto("file://" + path.join(SRC, f));
    await page.locator(".win").screenshot({ path: path.join(OUT, f.replace(/\.html$/, ".png")), omitBackground: true });
  }
  fs.copyFileSync(path.join(SRC, "frames.txt"), path.join(OUT, "frames.txt"));
  console.log("ok", files.length, "quadros");
  await browser.close();
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
