/* 首屏闪动回归（离线 demo）。
 *
 * 探针插在 app.js 内联脚本**之前**，读 app.js 接手前的真实 computedStyle。
 * 等 load 完再看没有意义：initTheme() 早把主题改对了、clearBootFlag() 早撤了
 * 属性，中间那一帧的浅色主题 / fadeUp 淡入就看不见了。
 * 另外还要查 app.js **接手之后**的状态：clearBootFlag() 会撤掉 data-boot，
 * 若 restoreView() 误把 animate 传成 true，goto() 补加 .enter 会让动画在
 * 首帧之后才播一次——只看探针会漏掉。
 *
 * 覆盖用户报的两个现象：
 *   1. 主题从默认 light/azure 渐变到用户偏好（body 还带 background/color transition）
 *   2. 刷新时整页 fadeUp 入场动画（停在概览页时 data-boot-view 不设置，
 *      首屏那条 animation:none 覆盖根本没命中）
 */
const { launch } = require("./browser");
const fs = require("fs");
const path = require("path");

const root = path.resolve(__dirname, "..");
const demo = fs.readFileSync(path.join(root, "demo.html"), "utf8");
const appTag = '<script>\n"use strict";';
const idx = demo.lastIndexOf(appTag);
if (idx < 0) throw new Error("demo.html 里找不到 app.js 的内联脚本");
const probe = `<script>
window.__firstFrame = (function () {
  const h = document.documentElement;
  const b = document.body, cs = b ? getComputedStyle(b) : null;
  const active = document.querySelector(".view.active");
  const acs = active ? getComputedStyle(active) : null;
  const meta = document.querySelector('meta[name="theme-color"]');
  return {
    mode: h.getAttribute("data-mode"),
    scheme: h.getAttribute("data-scheme"),
    bg: cs ? cs.backgroundColor : "",
    color: cs ? cs.color : "",
    themeColor: meta ? meta.getAttribute("content") : "",
    activeView: active ? active.id : "",
    activeAnimation: acs ? acs.animationName : "",
    activeOpacity: acs ? acs.opacity : ""
  };
})();
</script>`;
const probeFile = path.join(__dirname, "probe-flicker.html");
fs.writeFileSync(probeFile, demo.slice(0, idx) + probe + demo.slice(idx));
const URL = "file://" + probeFile + "#demo";

let pass = 0, fail = 0;
const ck = (n, want, got) => {
  const ok = String(want) === String(got);
  ok ? pass++ : fail++;
  console.log(`${ok ? "PASS" : "FAIL"} ${n}${ok ? "" : `  期望=[${want}] 实际=[${got}]`}`);
};
// 首帧之后也不能补播：撤掉 data-boot 时 .view.active.enter 若被加上，
// fadeUp 会在用户眼前再淡入一次。探针（app.js 之前）看不见，这里补上。
const ckNoReplay = (r) => {
  ck("app.js 接手后未补播入场动画", "none", r.now.activeAnimation);
  ck("恢复的页面不带 .enter", "false", String(r.now.activeEnter));
};

(async () => {
  const browser = await launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  await page.goto(URL);
  await page.waitForTimeout(400);

  const reloadWith = async (prefs) => {
    await page.evaluate(p => {
      localStorage.setItem("triune_token", "demo-token");
      for (const [k, v] of Object.entries(p)) {
        if (v === null) localStorage.removeItem(k);
        else localStorage.setItem(k, v);
      }
    }, prefs);
    await page.reload();
    await page.waitForTimeout(400);
    return page.evaluate(() => {
      const h = document.documentElement;
      const cs = getComputedStyle(document.body);
      const active = document.querySelector(".view.active");
      const meta = document.querySelector('meta[name="theme-color"]');
      return {
        first: window.__firstFrame,
        now: {
          mode: h.getAttribute("data-mode"),
          scheme: h.getAttribute("data-scheme"),
          bg: cs.backgroundColor,
          color: cs.color,
          themeColor: meta ? meta.getAttribute("content") : "",
          activeView: active ? active.id : "",
          activeAnimation: active ? getComputedStyle(active).animationName : "",
          activeEnter: active ? active.classList.contains("enter") : false
        }
      };
    });
  };

  // ---- 场景 1：暗色 + forest，刷新停在概览页（最常见，也是最容易漏的一格）----
  let r = await reloadWith({ triune_mode: "dark", triune_scheme: "forest", triune_view: null });
  console.log("\n[场景 1] dark + forest，概览页");
  ck("首帧 mode 就是 dark", "dark", r.first.mode);
  ck("首帧 scheme 就是 forest", "forest", r.first.scheme);
  ck("首帧 body 背景 == 最终背景（无主题渐变）", r.now.bg, r.first.bg);
  ck("首帧 body 文字色 == 最终文字色", r.now.color, r.first.color);
  // theme-color 只影响 PWA 状态栏顶色，不参与页面绘制；由 app.js 的
  // applyTheme() 在首帧后按 --bg 补设。这里只确认它确实跟着主题变了。
  ck("最终 theme-color 已脱离默认浅色", "false", String(r.now.themeColor === "#f4f6fa"));
  ck("首帧概览页无 fadeUp 入场动画", "none", r.first.activeAnimation);
  ck("首帧概览页不是透明", "1", r.first.activeOpacity);
  ck("最终主题正确", "dark/forest", r.now.mode + "/" + r.now.scheme);
  ckNoReplay(r);

  // ---- 场景 2：浅色 + ocean，刷新停在「设置」页 ----
  r = await reloadWith({ triune_mode: "light", triune_scheme: "ocean", triune_view: "settings" });
  console.log("\n[场景 2] light + ocean，设置页");
  ck("首帧 mode 就是 light", "light", r.first.mode);
  ck("首帧 scheme 就是 ocean", "ocean", r.first.scheme);
  ck("首帧 body 背景 == 最终背景", r.now.bg, r.first.bg);
  ck("首帧无 fadeUp 入场动画", "none", r.first.activeAnimation);
  ck("最终主题正确", "light/ocean", r.now.mode + "/" + r.now.scheme);
  ckNoReplay(r);

  // ---- 场景 3：旧版 triune_theme 迁移（老用户升级后第一次刷新）----
  r = await reloadWith({
    triune_mode: null, triune_scheme: null, triune_theme: "midnight", triune_view: null
  });
  console.log("\n[场景 3] 旧版 triune_theme=midnight 迁移");
  ck("首帧已迁移为 dark", "dark", r.first.mode);
  ck("首帧已迁移为 indigo", "indigo", r.first.scheme);
  ck("首帧 body 背景 == 最终背景", r.now.bg, r.first.bg);
  ck("首帧无 fadeUp 入场动画", "none", r.first.activeAnimation);
  ck("最终迁移结果一致", "dark/indigo", r.now.mode + "/" + r.now.scheme);
  ckNoReplay(r);

  // ---- 场景 4：非法偏好值不能把页面画成没有样式 ----
  r = await reloadWith({ triune_mode: "bogus", triune_scheme: "bogus", triune_view: null });
  console.log("\n[场景 4] 非法偏好值兜底");
  ck("首帧 mode 回落到 light", "light", r.first.mode);
  ck("首帧 scheme 回落到 azure", "azure", r.first.scheme);
  ck("首帧 body 背景 == 最终背景", r.now.bg, r.first.bg);
  ckNoReplay(r);

  // ---- 场景 5：默认（没存过任何主题偏好）----
  r = await reloadWith({
    triune_mode: null, triune_scheme: null, triune_theme: null, triune_view: "gen"
  });
  console.log("\n[场景 5] 无主题偏好，配置生成页");
  ck("首帧 mode 是 light", "light", r.first.mode);
  ck("首帧 scheme 是 azure", "azure", r.first.scheme);
  ck("首帧 body 背景 == 最终背景", r.now.bg, r.first.bg);
  ck("首帧无 fadeUp 入场动画", "none", r.first.activeAnimation);
  ckNoReplay(r);

  // ---- 场景 6：主动切页仍要淡入（别把「刷新不闪」做成永远没动画）----
  console.log("\n[场景 6] 主动点导航切页");
  const nav = await page.evaluate(() => {
    const b = document.querySelector('.nav-item[data-v="settings"]');
    if (b) b.click();
    const el = document.getElementById("v-settings");
    return {
      clicked: !!b,
      active: el.classList.contains("active"),
      enter: el.classList.contains("enter"),
      anim: getComputedStyle(el).animationName
    };
  });
  ck("导航项存在", "true", String(nav.clicked));
  ck("切到了设置页", "true", String(nav.active));
  ck("主动切页会加 .enter", "true", String(nav.enter));
  ck("主动切页仍有 fadeUp", "fadeUp", nav.anim);

  ck("页面无 JS 异常", "", errors.join(" | "));
  await browser.close();
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
