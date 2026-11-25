/* 回归：「点一次开关，动作跑好几遍、通知叠好几条」。
   用离线 demo.html（file:// 打开，自带演示数据）跑，不依赖后端。

   判定不看动画也不看像素，只数两件事：
     1) 这次点击对应的方法被调用了几次 —— 必须恰好 1 次；
     2) 屏幕上同时存在几条 .toast —— 最多 1 条。
   任何一条翻红都说明事件绑了不止一处（委托监听 + 行内 onchange/Binding 各一份）。 */
const { launch } = require("./browser");
const path = require("path");

let bad = 0, ran = 0;
function ok(cond, msg, extra) {
  if (cond) console.log("  ✓ " + msg);
  else { bad++; console.log("  ✗ " + msg + (extra === undefined ? "" : "  → " + JSON.stringify(extra))); }
}

(async () => {
  const b = await launch();
  const p = await b.newPage({ viewport: { width: 1440, height: 900 } });
  await p.goto("file://" + path.resolve(__dirname, "..", "demo.html") + "#demo");
  await p.waitForTimeout(1200);
  await p.evaluate(() => document.querySelector('.nav-item[data-v="settings"]')?.click());
  await p.waitForTimeout(600);

  // 统计每个动作被调用了几次
  await p.evaluate(() => {
    window.__calls = {};
    for (const n of ["saveCompatCore", "saveExtInSub", "saveAutoSwitch"]) {
      const orig = window[n];
      if (typeof orig !== "function") continue;
      window[n] = function (...a) { window.__calls[n] = (window.__calls[n] || 0) + 1; return orig.apply(this, a); };
    }
  });

  for (const [id, fn] of [["s_compat", "saveCompatCore"], ["s_autoswitch", "saveAutoSwitch"]]) {
    const el = await p.$("#" + id);
    // 兼容内核开关是设置页的固定项：它消失说明页面结构变了，不能静默跳过
    if (!el) { ok(false, `#${id} 存在`, "控件缺失"); continue; }
    // 依赖另一个开关的项（「自动切换」依赖兼容内核）会整行隐藏，点它会 30s 超时
    if (!(await el.isVisible())) { console.log(`  - #${id} 当前隐藏，跳过（依赖项未开）`); continue; }

    ran++;
    await el.click();
    await p.waitForTimeout(500);
    const n = await p.evaluate(i => window.__calls[i] || 0, fn);
    const toasts = await p.$$eval(".toast", ts => ts.length);
    ok(n === 1, `#${id} 只执行 1 次`, n);
    ok(toasts <= 1, `#${id} 最多留 1 条通知`, toasts);
    await p.evaluate(() => document.querySelectorAll(".toast").forEach(t => t.remove()));
    await el.click(); // 复原
    await p.waitForTimeout(300);
    await p.evaluate(() => document.querySelectorAll(".toast").forEach(t => t.remove()));
  }

  // select 也验一次：换内核只该触发一次重新加载
  await p.evaluate(() => document.querySelector('.nav-item[data-v="outbounds"]')?.click());
  await p.waitForTimeout(600);
  const sel = await p.$("#ob_core");
  if (!sel) {
    ok(false, "#ob_core 存在", "内核下拉缺失");
  } else {
    ran++;
    await p.evaluate(() => {
      window.__c = 0;
      const o = window.loadOutbounds;
      window.loadOutbounds = function (...a) { window.__c++; return o.apply(this, a); };
    });
    await sel.selectOption({ index: 1 }).catch(() => {});
    await p.waitForTimeout(500);
    const c = await p.evaluate(() => window.__c);
    ok(c === 1, "#ob_core 换选项只重新加载 1 次", c);
  }

  // 全都被跳过时不能算通过——那只是这一页没渲染出来
  ok(ran >= 2, "至少验到 2 个真实控件", ran);
  await b.close();
  console.log(bad === 0 ? "\n全部通过" : `\n${bad} 项失败`);
  process.exit(bad === 0 ? 0 : 1);
})();
