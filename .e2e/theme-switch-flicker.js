/* 主题切换闪动量化和回归（离线 demo）。
 *
 * 用户报「切换主题时也会闪烁」。两种中间态，修复前实测都存在：
 *   1. applyTheme() 改 data-mode/scheme，而 body/.card 带 background/color
 *      transition —— 变量瞬变、过渡慢慢追，亮→暗实测出现 6 个中间色；
 *      同时没写 transition 的属性（边框、阴影、图标）却是瞬跳。
 *   2. renderSchemes() 用 innerHTML 重建 7 个预览卡（每卡 ~12 个节点），
 *      DOM 被销毁重建，弹窗里的网格闪一下（MutationObserver 记到 1 次）。
 *
 * 修复口径：切换瞬间给 <html> 挂 data-theme-switch 压掉全部 transition，
 * 两帧后撤掉；预览网格改成增量更新，不再 innerHTML 重建。
 * 采样：rAF 连续记 body 背景色；MutationObserver 盯 childList（重建）与
 * <html> 的 data-theme-switch 属性是否出现过（禁过渡确实生效）。
 */
const { launch } = require("./browser");
const path = require("path");

const URL = "file://" + path.resolve(__dirname, "..", "demo.html") + "#demo";

let pass = 0, fail = 0;
const ck = (n, want, got) => {
  const ok = String(want) === String(got);
  ok ? pass++ : fail++;
  console.log(`${ok ? "PASS" : "FAIL"} ${n}${ok ? "" : `  期望=[${want}] 实际=[${got}]`}`);
};
const transparent = c => c === "rgba(0, 0, 0, 0)" || c === "transparent";

(async () => {
  const browser = await launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  await page.goto(URL);
  await page.waitForTimeout(250);
  await page.evaluate(() => document.getElementById("themeBtn").click());
  await page.waitForTimeout(80);

  const startSample = () => page.evaluate(() => {
    window.__s = { colors: [], mut: 0, stop: false, switchSeen: false };
    const h = document.documentElement;
    const schemes = document.getElementById("schemes");
    window.__s.firstBtn = schemes.querySelector(".scheme");
    window.__s.obs = new MutationObserver(muts => { window.__s.mut += muts.length; });
    // 只盯 childList：增量更新只改 class，不该被算成「重建」
    window.__s.obs.observe(schemes, { childList: true, subtree: true });
    window.__s.htmlObs = new MutationObserver(() => {
      if (h.hasAttribute("data-theme-switch")) window.__s.switchSeen = true;
    });
    window.__s.htmlObs.observe(h, { attributes: true, attributeFilter: ["data-theme-switch"] });
    (function loop() {
      if (window.__s.stop) return;
      window.__s.colors.push(getComputedStyle(document.body).backgroundColor);
      requestAnimationFrame(loop);
    })();
  });
  const endSample = () => page.evaluate(() => {
    window.__s.stop = true;
    window.__s.obs.disconnect();
    window.__s.htmlObs.disconnect();
    const h = document.documentElement;
    return {
      colors: [...new Set(window.__s.colors)],
      mutations: window.__s.mut,
      rebuilt: window.__s.firstBtn !== document.getElementById("schemes").querySelector(".scheme"),
      switchSeen: window.__s.switchSeen,
      mode: h.getAttribute("data-mode"),
      scheme: h.getAttribute("data-scheme"),
      switching: h.hasAttribute("data-theme-switch"),
      onScheme: document.querySelector("#schemes .scheme.on")?.dataset.s || ""
    };
  });

  // ---- 场景 1：亮 → 暗 ----
  await startSample();
  await page.evaluate(() => document.querySelector('#modeSeg button[data-mode="dark"]').click());
  await page.waitForTimeout(500);
  const r1 = await endSample();
  console.log("\n[场景 1] 亮色 → 暗色");
  console.log("  背景色序列:", r1.colors.join(" → "));
  console.log("  #schemes childList 变更:", r1.mutations, " 预览卡被重建:", r1.rebuilt,
              " 禁过渡标记出现过:", r1.switchSeen);
  ck("最终是暗色", "dark", r1.mode);
  ck("切换瞬间挂过 data-theme-switch（禁掉过渡）", "true", String(r1.switchSeen));
  ck("切换后不留 data-theme-switch 标记", "false", String(r1.switching));
  ck("背景色没有渐变中间态（最多旧/新两色）", "true", String(r1.colors.length <= 2));
  ck("切换亮暗不重建预览网格 DOM", "false", String(r1.rebuilt));
  ck("#schemes 没有 childList 变更", "0", String(r1.mutations));
  ck("无 JS 异常", "", errors.join(" | "));

  // ---- 场景 2：换配色 forest（预览色只跟 mode 走，更不该重建）----
  await startSample();
  await page.evaluate(() => document.querySelector('#schemes .scheme[data-s="forest"]').click());
  await page.waitForTimeout(500);
  const r2 = await endSample();
  console.log("\n[场景 2] 暗色 azure → forest");
  console.log("  背景色序列:", r2.colors.join(" → "));
  console.log("  #schemes childList 变更:", r2.mutations, " 预览卡被重建:", r2.rebuilt,
              " 禁过渡标记出现过:", r2.switchSeen);
  ck("最终 scheme 是 forest", "forest", r2.scheme);
  ck("选中态落在 forest", "forest", r2.onScheme);
  ck("切换瞬间挂过 data-theme-switch", "true", String(r2.switchSeen));
  ck("背景色没有渐变中间态", "true", String(r2.colors.length <= 2));
  ck("配色切换不重建预览网格 DOM", "false", String(r2.rebuilt));
  ck("#schemes 没有 childList 变更", "0", String(r2.mutations));
  ck("切换后不留 data-theme-switch 标记", "false", String(r2.switching));

  // ---- 场景 3：刷新后首帧就是上次主题 ----
  await page.reload();
  await page.waitForTimeout(300);
  const r3 = await page.evaluate(() => ({
    mode: document.documentElement.getAttribute("data-mode"),
    scheme: document.documentElement.getAttribute("data-scheme"),
    bg: getComputedStyle(document.body).backgroundColor
  }));
  console.log("\n[场景 3] 刷新");
  ck("刷新后仍是 dark/forest", "dark/forest", r3.mode + "/" + r3.scheme);
  ck("刷新后没有残留 data-theme-switch", "false",
     String(await page.evaluate(() => document.documentElement.hasAttribute("data-theme-switch"))));
  console.log("  未使用 transparent():", transparent(r3.bg) === false ? "背景色不透明 OK" : "异常");

  await browser.close();
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
