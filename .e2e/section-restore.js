/* 设置页分组刷新回归（离线 demo）。
 *
 * 用户报「在设置界面选了运维标签刷新，会先跳到常用、再跳回运维」。
 * 根因有两段：index.html 里 common 按钮写死 class="on"、所有 [data-sect]
 * 卡片默认都可见；而 app.js 的 setSectSaved() 被放在 setTimeout(...,30)
 * 里，首帧到那 30ms 之间浏览器有充足时间画一帧「常用」。
 *
 * 探针插在 app.js 内联脚本之前读首帧 computedStyle，另外在 +15ms（早于
 * 那个 30ms 定时器）再采一次，确认没有「先常用后运维」的中间帧。
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
(function () {
  const snap = function () {
    const h = document.documentElement;
    const card = s => {
      const el = document.querySelector('#v-settings .card[data-sect="' + s + '"]');
      return el ? getComputedStyle(el).display : "missing";
    };
    const btn = s => {
      const el = document.querySelector('#set_sect_seg button[data-sect="' + s + '"]');
      return el ? { bg: getComputedStyle(el).backgroundColor,
                    on: el.classList.contains("on"),
                    aria: el.getAttribute("aria-selected") } : null;
    };
    return {
      bootSect: h.getAttribute("data-boot-sect"),
      view: h.getAttribute("data-boot-view"),
      commonCard: card("common"), maintainCard: card("maintain"),
      commonBtn: btn("common"), maintainBtn: btn("maintain")
    };
  };
  window.__firstFrame = snap();
  setTimeout(function () { window.__at15ms = snap(); }, 15);
})();
</script>`;
const probeFile = path.join(__dirname, "probe-section.html");
fs.writeFileSync(probeFile, demo.slice(0, idx) + probe + demo.slice(idx));
const URL = "file://" + probeFile + "#demo";

const transparent = c => c === "rgba(0, 0, 0, 0)" || c === "transparent";
let pass = 0, fail = 0;
const ck = (n, want, got) => {
  const ok = String(want) === String(got);
  ok ? pass++ : fail++;
  console.log(`${ok ? "PASS" : "FAIL"} ${n}${ok ? "" : `  期望=[${want}] 实际=[${got}]`}`);
};

(async () => {
  const browser = await launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  await page.goto(URL);
  await page.waitForTimeout(300);

  const reloadWith = async (prefs) => {
    await page.evaluate(p => {
      localStorage.setItem("triune_token", "demo-token");
      for (const [k, v] of Object.entries(p)) {
        if (v === null) localStorage.removeItem(k); else localStorage.setItem(k, v);
      }
    }, prefs);
    await page.reload();
    await page.waitForTimeout(400);
    return page.evaluate(() => ({
      first: window.__firstFrame, mid: window.__at15ms,
      now: (function () {
        const h = document.documentElement;
        const card = s => {
          const el = document.querySelector('#v-settings .card[data-sect="' + s + '"]');
          return el ? getComputedStyle(el).display : "missing";
        };
        const btn = s => {
          const el = document.querySelector('#set_sect_seg button[data-sect="' + s + '"]');
          return el ? { bg: getComputedStyle(el).backgroundColor,
                        on: el.classList.contains("on"),
                        aria: el.getAttribute("aria-selected") } : null;
        };
        return {
          bootSect: h.getAttribute("data-boot-sect"),
          commonCard: card("common"), maintainCard: card("maintain"),
          commonBtn: btn("common"), maintainBtn: btn("maintain")
        };
      })()
    }));
  };

  // ---- 场景 1：停在「设置 / 运维」刷新 ----
  let r = await reloadWith({ triune_view: "settings", triune_settings_sect: "maintain" });
  console.log("\n[场景 1] 设置 / 运维 刷新");
  console.log("  首帧:", JSON.stringify(r.first));
  console.log("  +15ms:", JSON.stringify(r.mid));
  ck("首帧打了 data-boot-sect=maintain", "maintain", r.first.bootSect);
  ck("首帧就显示了设置页", "settings", String(r.first.view));
  ck("首帧隐藏「常用」卡片", "none", r.first.commonCard);
  ck("首帧显示「运维」卡片", "block", r.first.maintainCard);
  // 不能只比「两个按钮颜色不同」——修复前 common 高亮、maintain 透明，
  // 颜色也不同，那条断言会假绿。要分别盯住两边自己的状态。
  ck("首帧「运维」按钮是高亮态", "true",
     String(!transparent(r.first.maintainBtn.bg)));
  ck("首帧「常用」按钮不是高亮态", "true",
     String(transparent(r.first.commonBtn.bg)));
  ck("+15ms 没有跳回常用（常用卡片仍隐藏）", "none", r.mid.commonCard);
  ck("+15ms 运维卡片仍显示", "block", r.mid.maintainCard);
  ck("+15ms「运维」按钮仍是高亮态", "true", String(!transparent(r.mid.maintainBtn.bg)));
  ck("最终由 JS 接手，运维按钮带 .on", "true", String(r.now.maintainBtn.on));
  ck("最终 aria-selected 正确", "true", r.now.maintainBtn.aria);
  ck("最终 common 按钮不是选中态", "false", String(r.now.commonBtn.on));
  ck("最终已撤掉 data-boot-sect", "null", String(r.now.bootSect));

  // ---- 场景 2：停在「设置 / 安全」刷新 ----
  r = await reloadWith({ triune_view: "settings", triune_settings_sect: "security" });
  console.log("\n[场景 2] 设置 / 安全 刷新");
  ck("首帧 data-boot-sect=security", "security", r.first.bootSect);
  ck("首帧隐藏「常用」卡片", "none", r.first.commonCard);
  ck("首帧隐藏「运维」卡片", "none", r.first.maintainCard);
  ck("首帧「常用」按钮不是高亮态", "true", String(transparent(r.first.commonBtn.bg)));

  // ---- 场景 3：不在设置页时不打标记 ----
  r = await reloadWith({ triune_view: "overview", triune_settings_sect: "maintain" });
  console.log("\n[场景 3] 停在概览页刷新");
  ck("不在设置页不打 data-boot-sect", "null", String(r.first.bootSect));

  // ---- 场景 4：非法分组值兜底 ----
  // 非法值回落 common，而不是「不打标记」：不打标记时三组卡片在首帧全可见，
  // app.js 收起来又会跳一下。回落成 common 才能和最终态逐帧一致。
  r = await reloadWith({ triune_view: "settings", triune_settings_sect: "bogus" });
  console.log("\n[场景 4] 非法分组值");
  ck("非法值回落到 data-boot-sect=common", "common", r.first.bootSect);
  ck("首帧只显示「常用」卡片", "block", r.first.commonCard);
  ck("首帧隐藏「运维」卡片", "none", r.first.maintainCard);
  ck("最终退回常用", "true", String(r.now.commonBtn.on));

  ck("页面无 JS 异常", "", errors.join(" | "));
  await browser.close();
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
