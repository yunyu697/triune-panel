/* 回归：「对话框一编辑就退出」
   用离线 demo.html（file:// 打开，自带演示数据）跑，不依赖后端。
   逐个弹窗：强制 show → 在第一个文本输入框里敲字 → 弹窗必须还开着。

   根因回顾：遮罩层挂着 data-act="closeModal" data-self="1"，而 data-self 的
   判断原先只写在 click 里。input 事件从输入框冒泡到遮罩，被当成「点了空白区」，
   于是每敲一个字关一次弹窗。 */
const { launch } = require("./browser");
const path = require("path");

(async () => {
  const browser = await launch();
  const page = await browser.newPage();
  const errs = [];
  page.on("console", m => { if (m.type() === "error") errs.push(m.text()); });
  page.on("pageerror", e => errs.push("pageerror: " + e.message));

  await page.goto("file://" + path.resolve(__dirname, "..", "demo.html") + "#demo");
  await page.waitForTimeout(1500);

  // 顺带覆盖三个真实入口（弹窗内容由业务代码填充）
  const entries = [
    ["openInboundModal(null)", "mInbound"],
    ["openClientModal(null)", "mClient"],
    ["EXT.length ? editExternal(EXT[0].id) : null", "mExt"],
  ];
  let opened = 0;
  for (const [expr, id] of entries) {
    const ok = await page.evaluate(expr).then(() => page.evaluate(i => document.getElementById(i).classList.contains("show"), id)).catch(() => false);
    if (ok) opened++;
    console.log(`入口 ${expr} → ${ok ? "已打开" : "未打开（演示数据可能为空）"}`);
    await page.evaluate(() => document.querySelectorAll(".overlay.show").forEach(e => e.classList.remove("show")));
  }

  // 全量扫：每个带输入框的弹窗都敲一遍
  const ids = await page.evaluate(() =>
    [...document.querySelectorAll(".overlay")].map(o => o.id).filter(id => {
      const o = document.getElementById(id);
      return o && o.querySelector("input[type=text], input:not([type]), textarea");
    }));
  let closed = 0, fail = 0;
  for (const id of ids) {
    await page.evaluate(i => document.getElementById(i).classList.add("show"), id);
    await page.waitForTimeout(260); // 等 opacity/transform 过渡结束
    const sel = `#${id} input[type=text], #${id} input:not([type]), #${id} textarea`;
    // 不用 click：部分弹窗里的输入框藏在 display:none 的分支里，playwright 会一直等
    const focused = await page.evaluate(s => {
      const el = document.querySelector(s);
      if (!el) return false;
      el.focus();
      return document.activeElement === el;
    }, sel);
    if (!focused) { console.log(`[skip] #${id} 输入框不可聚焦`); continue; }
    await page.keyboard.type("abc");
    await page.waitForTimeout(150);
    const still = await page.evaluate(i => document.getElementById(i).classList.contains("show"), id);
    const val = await page.evaluate(s => (document.querySelector(s) || {}).value, sel);
    if (!still) closed++;
    console.log(`${still ? "PASS" : "FAIL"}  #${id}  仍打开=${still}  值=${JSON.stringify(val)}`);
    await page.evaluate(() => document.querySelectorAll(".overlay.show").forEach(e => e.classList.remove("show")));
  }

  // 三个真实入口至少要开得出来两个；一个都开不出来说明弹窗体系坏了，
  // 下面「强制 show 再敲字」那一轮照样会过——那是假绿。
  if (opened < 2) fail++;

  // 控制台一声不响地报错，用户是看不到的（这里以前只打印、不判定）
  const realErrs = errs.filter(e => !/ERR_FILE_NOT_FOUND/.test(e));
  if (realErrs.length) fail++;

  console.log(`\n结果：${ids.length - closed}/${ids.length} 个弹窗可编辑`);
  console.log("真实入口:", `${opened}/${entries.length} 已打开`);
  console.log("console.error:", realErrs.length ? realErrs : "无");
  await browser.close();
  process.exit(fail || closed ? 1 : 0);
})();
