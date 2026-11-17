/* 离线回归：排障三件套 —— 覆盖提示、规则体检脚本、规则漂移、诊断摘要。
   这四样解决的是同一类问题：改完规则之后，用户没法自己判断「到底生效了没有」。
   - 覆盖提示：勾了哪些规则集 → 哪一类关键服务现在只剩内置兜底
   - 体检脚本：在设备上跑，逐类看域名落到直连还是代理（内核 -t 只证明能加载）
   - 规则漂移：升级后老配置缺了哪几条内置规则
   - 诊断摘要：报障时一次贴全，省掉来回问版本与规则
   断言全部走界面，不直接读内部变量——回归要抓的正是「界面看起来对但没接上」。 */
const { launch } = require("./browser");
const path = require("path");
const URL = "file://" + path.resolve(__dirname, "..", "demo.html") + "#demo";

let pass = 0, fail = 0;
const ck = (n, want, got) => {
  const good = String(want) === String(got);
  good ? pass++ : fail++;
  console.log(`${good ? "PASS" : "FAIL"} ${n}${good ? "" : `  期望=[${want}] 实际=[${got}]`}`);
};
const ok = (n, cond) => ck(n, true, !!cond);
const has = (n, needle, hay) => {
  const good = String(hay).includes(needle);
  good ? pass++ : fail++;
  console.log(`${good ? "PASS" : "FAIL"} ${n}${good ? "" : `  缺少 [${needle}]，实际=[${hay}]`}`);
};
const nav = async (page, v) => {
  await page.evaluate(id => document.querySelector(`.nav-item[data-v="${id}"]`).click(), v);
  await page.waitForTimeout(400);
};

(async () => {
  const browser = await launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  await page.goto(URL);
  await page.waitForTimeout(700);

  // ---- 1. 覆盖提示：默认没勾，Apple 那一类必须显示「未勾」 ----
  await nav(page, "gen");
  await page.waitForTimeout(400);
  const chips = page.locator("#gen_cover .chip");
  ok("覆盖提示有标签", await chips.count() >= 8);
  const apple = page.locator("#gen_cover .chip", { hasText: "Apple 系统更新" }).first();
  has("默认未勾时标出「未勾」", "未勾", await apple.innerText());
  ok("未勾时是警示色", await page.locator("#gen_cover .chip.warn", { hasText: "Apple 系统更新" }).count() === 1);
  // GitHub 这一类没有对应规则集，永远由内置代理兜底，不该报「未勾」
  const gh = page.locator("#gen_cover .chip", { hasText: "GitHub" }).first();
  has("内置类标成「内置」", "内置", await gh.innerText());
  ok("内置类不用警示色", await page.locator("#gen_cover .chip.warn", { hasText: "GitHub" }).count() === 0);

  // ---- 2. 勾上 geosite-apple 后立刻变成「已覆盖」 ----
  await page.locator('input.gset[data-id="geosite-apple"]').check();
  await page.waitForTimeout(250);
  has("勾选后标出覆盖它的规则集", "geosite-apple", await apple.innerText());
  ok("勾选后转成通过色", await page.locator("#gen_cover .chip.ok", { hasText: "Apple 系统更新" }).count() === 1);
  ok("勾选后不再有 Apple 的警示", await page.locator("#gen_cover .chip.warn", { hasText: "Apple 系统更新" }).count() === 0);
  // 取消勾选要能退回去（选择集是内存里的，改漏了就会一直显示已覆盖）
  await page.locator('input.gset[data-id="geosite-apple"]').uncheck();
  await page.waitForTimeout(250);
  has("取消勾选后回到「未勾」", "未勾", await apple.innerText());

  // ---- 3. 体检脚本：两个入口都在，点击能真的下载 ----
  ok("生成页有体检脚本入口", await page.locator('#v-gen [data-act="downloadCheckScript"]').count() === 1);
  const dl = page.waitForEvent("download", { timeout: 4000 }).catch(() => null);
  await page.locator('#v-gen [data-act="downloadCheckScript"]').click();
  const file = await dl;
  ok("点击后触发下载", !!file);
  if (file) ck("下载文件名", "tp-check.sh", file.suggestedFilename());

  // ---- 4. 规则漂移：列表标出落后，点开能看到具体规则 ----
  await nav(page, "configs");
  const badge = page.locator("#cfgbody .chip.warn").first();
  has("列表标出落后条数", "落后 2 条", await badge.innerText());
  await badge.click();
  await page.waitForTimeout(350);
  ok("漂移弹窗打开", await page.locator("#mDrift").evaluate(el => el.classList.contains("show")));
  const driftTxt = await page.locator("#dr_body").innerText();
  has("弹窗说明生成版本", "v2.0.7", driftTxt);
  has("弹窗列出新增条数", "新增 2 条", driftTxt);
  has("弹窗列出具体规则", "ocsp.apple.com", driftTxt);
  await page.locator("#mDrift .foot button").click();
  await page.waitForTimeout(200);
  ok("漂移弹窗能关闭", await page.locator("#mDrift").evaluate(el => !el.classList.contains("show")));

  // ---- 5. 诊断摘要：设置页一键展开 ----
  await nav(page, "settings");
  ok("设置页有诊断摘要入口", await page.locator('[data-act="copyDiagSummary"]').count() === 1);
  await page.locator('[data-act="copyDiagSummary"]').click();
  await page.waitForTimeout(400);
  ok("摘要展开在页面上", await page.locator("#diag_out").evaluate(el => !el.classList.contains("hide")));
  const diag = await page.locator("#diag_out").innerText();
  has("摘要含标题", "triune-panel 诊断摘要", diag);
  has("摘要含规则统计", "内置系统规则", diag);
  has("摘要含内核段落", "内核", diag);
  ok("摘要给了反馈文案", (await page.locator("#diag_msg").innerText()).length > 0);

  ck("页面未抛异常", 0, errors.length);
  await browser.close();
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error("脚本异常:", e); process.exit(1); });
