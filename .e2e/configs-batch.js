/* 离线回归：配置管理的多选与批量操作。
   以前删几十份配置只能一条一条点，所以这里钉住的是「选择集」这条新链路：
   勾选 → 计数 → 全选只作用于当前筛选结果 → 批量删除真的删掉。
   最后顺手钉一下生成页的区块顺序：自定义规则必须在规则集之前，
   否则界面暗示的优先级与生成出来的配置正好相反。 */
const { launch } = require("./browser");
const path = require("path");
const URL = "file://" + path.resolve(__dirname, "..", "demo.html") + "#demo";

let pass = 0, fail = 0;
const ck = (n, want, got) => {
  const ok = String(want) === String(got);
  ok ? pass++ : fail++;
  console.log(`${ok ? "PASS" : "FAIL"} ${n}${ok ? "" : `  期望=[${want}] 实际=[${got}]`}`);
};
const ok = (n, cond) => ck(n, true, !!cond);
const has = (n, needle, hay) => {
  const good = String(hay).includes(needle);
  good ? pass++ : fail++;
  console.log(`${good ? "PASS" : "FAIL"} ${n}${good ? "" : `  缺少 [${needle}]，实际=[${hay}]`}`);
};

(async () => {
  const browser = await launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  await page.goto(URL);
  await page.waitForTimeout(700);
  await page.evaluate(() => document.querySelector('.nav-item[data-v="configs"]').click());
  await page.waitForTimeout(350);

  const bulkHidden = () => page.locator("#cfg_bulk").evaluate(el => el.classList.contains("hide"));
  const bulkText = async () => (await page.locator("#cfg_bulk_n").textContent()).trim();
  const rowBoxes = () => page.locator("#cfgbody tr input[type=checkbox]");

  // ---- 1. 初始状态：有数据，但批量条不占位 ----
  ck("演示数据行数", 2, await page.locator("#cfgbody tr").count());
  ok("未选择时批量条隐藏", await bulkHidden());

  // ---- 2. 勾一条：计数与操作条都要跟上 ----
  await rowBoxes().first().check();
  await page.waitForTimeout(150);
  ck("勾一条后的计数", "已选 1 份", await bulkText());
  ok("勾选后批量条出现", !(await bulkHidden()));

  // ---- 3. 表头全选：行内全勾、表头也勾 ----
  await page.locator("#cfg_all").check();
  await page.waitForTimeout(150);
  ck("全选后的计数", "已选 2 份", await bulkText());
  ck("行内被勾选的数量", 2, await page.locator("#cfgbody tr input[type=checkbox]:checked").count());

  // ---- 4. 取消全选：选择集清空，操作条收起 ----
  await page.locator("#cfg_all").uncheck();
  await page.waitForTimeout(150);
  ok("取消全选后批量条隐藏", await bulkHidden());

  // ---- 5. 搜索态全选只作用于可见行（否则会删掉看不见的配置）----
  await page.locator("#cfg_search").fill("电视");
  await page.waitForTimeout(200);
  ck("搜索后行数", 1, await page.locator("#cfgbody tr").count());
  await page.locator("#cfg_all").check();
  await page.waitForTimeout(150);
  ck("搜索态全选只勾可见行", "已选 1 份", await bulkText());
  await page.locator("#cfg_all").uncheck();
  await page.locator("#cfg_search").fill("");
  await page.waitForTimeout(200);

  // ---- 6. 批量删除：确认框要报出名字，确认后真的少一行 ----
  await rowBoxes().first().check();
  await page.waitForTimeout(150);
  await page.locator('[data-act="cfgBatchDelete"]').click();
  await page.waitForTimeout(300);
  ok("弹出确认框", await page.locator("#mConfirm").evaluate(el => el.classList.contains("show")));
  has("确认框列出被删的名字", "主路由-singbox1.14", await page.locator("#cf_text").textContent());
  await page.locator("#cf_ok").click();
  await page.waitForTimeout(450);
  ck("删除后剩余行数", 1, await page.locator("#cfgbody tr").count());
  ok("删除后批量条收起", await bulkHidden());

  // ---- 7. 生成页：自定义规则必须排在规则集之前 ----
  await page.evaluate(() => document.querySelector('.nav-item[data-v="gen"]').click());
  await page.waitForTimeout(350);
  const order = await page.evaluate(() =>
    [...document.querySelectorAll("#v-gen .card h3")].map(h => (h.childNodes[0].textContent || "").trim()));
  const iCustom = order.indexOf("自定义规则");
  const iSets = order.findIndex(t => t.startsWith("规则集"));
  ok("生成页有「自定义规则」卡片", iCustom >= 0);
  ok("生成页有「规则集」卡片", iSets >= 0);
  ck("自定义规则排在规则集之前", "true", String(iCustom >= 0 && iSets > iCustom));

  ck("页面未抛异常", 0, errors.length);
  await browser.close();
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error("脚本异常:", e); process.exit(1); });
