/* 浏览器启动口径统一放在这里，本地与 CI 才不会各写一套。
 *
 * 本地：用已经装好的 Google Chrome（Playwright 自带 Chromium 下载经常卡住）。
 * CI：E2E_CHANNEL=bundled → 不传 channel，用 `playwright install chromium`
 *     装下来的那一份。也可以给 E2E_CHANNEL 传别的 channel 名。
 */
const { chromium } = require("playwright");

async function launch(opts = {}) {
  const ch = process.env.E2E_CHANNEL || "chrome";
  const o = Object.assign({}, opts);
  if (ch !== "bundled") o.channel = ch;
  return chromium.launch(o);
}

module.exports = { launch };
