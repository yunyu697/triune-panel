/* 回归：页面上每个动作都必须真的存在。
   app.js 的委托只认 data-act / data-enter 两条属性：名字找不到函数，runAction 就
   console.error 一声什么也不做——按钮在、样式在、点了没反应，用户只能当它是坏的。
   以前「确认无误，应用」的 data-args 就是一段坏 JSON，同一类问题还在别处出现过
   （指向从未定义过的函数）。所以这里不盯某一个按钮，扫整页：
     1) 每个 data-act / data-enter 的名字，window 上得有同名函数；
     2) 每条 data-args 得是能 JSON.parse 出来的数组。
   遍历所有视图，因为动作大多挂在渲染出来的片段上，首屏看不到。 */
const { launch } = require("./browser");
const path = require("path");

let bad = 0;
function ok(cond, msg, extra) {
  if (cond) console.log("  ✓ " + msg);
  else { bad++; console.log("  ✗ " + msg + (extra === undefined ? "" : "  → " + JSON.stringify(extra))); }
}

(async () => {
  const b = await launch();
  const p = await b.newPage({ viewport: { width: 1440, height: 950 } });
  const errs = [];
  p.on("pageerror", e => errs.push("pageerror: " + e.message));
  p.on("console", m => { const t = m.text(); if (m.type() === "error" && !/ERR_FILE_NOT_FOUND|Failed to load resource/.test(t)) errs.push("console: " + t); });

  await p.goto("file://" + path.resolve(__dirname, "..", "demo.html") + "#demo");
  await p.waitForTimeout(1200);

  // 逐个视图过一遍，让动态片段都渲染出来再扫
  const views = await p.evaluate(() => [...document.querySelectorAll(".nav-item[data-v]")].map(e => e.dataset.v));
  for (const v of views) {
    await p.evaluate(x => document.querySelector(`.nav-item[data-v="${x}"]`)?.click(), v);
    await p.waitForTimeout(250);
  }

  const found = await p.evaluate(() => {
    const out = { acts: [], enters: [], badArgs: [], count: 0 };
    document.querySelectorAll("[data-act]").forEach(el => {
      out.count++;
      out.acts.push(el.dataset.act);
      const a = el.dataset.args;
      if (a !== undefined) {
        try {
          const v = JSON.parse(a);
          if (!Array.isArray(v)) out.badArgs.push({ act: el.dataset.act, args: a, why: "不是数组" });
        } catch (e) { out.badArgs.push({ act: el.dataset.act, args: a, why: "不是合法 JSON" }); }
      }
    });
    document.querySelectorAll("[data-enter]").forEach(el => out.enters.push(el.dataset.enter));
    return out;
  });

  // 名字 → 是否有同名函数（在页面里判，window 上挂着 app.js 的全部动作）
  const check = await p.evaluate(list => {
    const miss = [];
    for (const n of list) if (typeof window[n] !== "function") miss.push(n);
    return [...new Set(miss)];
  }, found.acts.concat(found.enters));

  ok(found.count > 50, "扫到足量动作（视图都渲染了）", found.count);
  ok(check.length === 0, "每个 data-act / data-enter 都有同名函数", check);
  ok(found.badArgs.length === 0, "每条 data-args 都是合法 JSON 数组", found.badArgs);
  ok(errs.length === 0, "遍历视图期间没有报错", errs);

  await b.close();
  console.log(bad === 0 ? "\n全部通过" : `\n${bad} 项失败`);
  process.exit(bad === 0 ? 0 : 1);
})();
