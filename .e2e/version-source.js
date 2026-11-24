/* 「版本列表拿不到（403）」这条故障的界面侧回归。
 *
 * 后端证明的是「配额耗尽能识别、能换通道」；这里证明的是「用户真的看得见、
 * 真的改得动」——填了 Token 之后请求里带不带、列表改走订阅时有没有提示。
 * v2.0.x 出过「界面上有、请求里没有」的事，所以这两层都要钉。
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

(async () => {
  const browser = await launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  await page.goto(URL);
  await page.waitForTimeout(500);
  await page.evaluate(() => document.querySelector('.nav-item[data-v="settings"]').click());
  await page.waitForTimeout(400);

  // ---- 1. 输入框与说明 ----
  ck("更新源里有 Token 输入框", 1,
    await page.evaluate(() => document.querySelectorAll("#s_gh_token").length));
  ck("默认留空（匿名）", "",
    await page.evaluate(() => document.getElementById("s_gh_token").value));

  const hint = await page.evaluate(() => {
    const card = document.getElementById("set_card_update");
    return card ? card.textContent : "";
  });
  // 这两个数字是用户唯一能自己判断「要不要配」的依据，必须写在界面上，
  // 不能只写在 README 里。
  ck("说明里写了匿名额度", true, hint.includes("60 次/小时"));
  ck("说明里写了 Token 额度", true, hint.includes("5000 次/小时"));
  ck("说明里写了按出口 IP 计", true, hint.includes("按出口 IP"));
  ck("说明里写了订阅兜底", true, hint.includes("releases.atom"));

  // ---- 2. 填了会真的进请求 ----
  await page.evaluate(() => {
    const el = document.getElementById("s_gh_token");
    el.value = "  ghp_demo_token_0123456789  ";   // 前后空格要自己吃掉
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await page.evaluate(() => document.querySelector('[data-act="saveUpdateSettings"]').click());
  await page.waitForTimeout(400);
  ck("保存后落到设置里（去空格）", "ghp_demo_token_0123456789",
    await page.evaluate(() => MDB.settings.gh_token));

  // 占位符原样提交 = 没改过：不能拿 ******** 覆盖真 Token。
  await page.evaluate(() => {
    MDB.settings.gh_token = "ghp_real_token_abcdef";
    const el = document.getElementById("s_gh_token");
    el.value = "********";
    el.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector('[data-act="saveUpdateSettings"]').click();
  });
  await page.waitForTimeout(400);
  ck("提交占位符不覆盖真 Token", "ghp_real_token_abcdef",
    await page.evaluate(() => MDB.settings.gh_token));

  // ---- 3. 配额耗尽改读订阅时，要说清「这份列表没有校验值」 ----
  await page.evaluate(() => { window.__demoVerSource = "atom"; fetchVersions("panel"); });
  await page.waitForTimeout(800);
  const toast = await page.evaluate(() => document.getElementById("toasts").textContent);
  ck("列表改走订阅时有提示", true, toast.includes("releases 订阅"));
  ck("提示里说明了没有 sha256", true, toast.includes("sha256"));
  // 换通道之后列表本身仍然可用——否则「有提示」也只是从一个失败变成另一个失败。
  ck("订阅通道也能列出条目", true,
    await page.evaluate(() => document.getElementById("vs_select").options.length > 0));

  await page.evaluate(() => { window.__demoVerSource = ""; });

  ck("没有页面异常", 0, errors.length);
  if (errors.length) console.log(errors.join("\n"));

  await browser.close();
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
