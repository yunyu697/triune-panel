/* 「局域网 DNS」在离线演示里的真实交互回归。
 *
 * 单测证明的是产物里那几行字对不对；这里证明的是「用户填了，值真的进了请求」。
 * 两者缺一不可——v2.0.x 出过「界面上有、请求里没有」的事，产物一侧全绿。
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
  await page.evaluate(() => {
    document.querySelector('.nav-item[data-v="gen"]').click();
    const cbs = document.querySelectorAll("#gen_inbounds input[type=checkbox]");
    if (cbs.length) cbs[0].checked = true;
  });
  await page.waitForTimeout(300);

  ck("输入框存在", 1, await page.evaluate(() => document.querySelectorAll("#gen_lan_dns").length));
  ck("默认留空", "", await page.evaluate(() => document.getElementById("gen_lan_dns").value));
  ck("留空时请求里也留空", "", await page.evaluate(() => buildGenParams().lan_dns));

  // 用户填一个地址
  await page.evaluate(() => {
    const el = document.getElementById("gen_lan_dns");
    el.value = "  10.0.0.1  ";   // 前后空格要自己吃掉，别指望用户
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await page.waitForTimeout(200);
  ck("填了之后请求里带上（去空格）", "10.0.0.1",
    await page.evaluate(() => buildGenParams().lan_dns));

  // 生成一次，确认回显
  await page.evaluate(() => document.querySelector('[data-act="genGenerate"]').click());
  await page.waitForTimeout(900);
  ck("结果页回显填的值", true, await page.evaluate(() =>
    document.getElementById("gen_vres").textContent.includes("局域网 DNS：10.0.0.1")));

  // 改回去：留空时不该多出一行「未设置」——那是输入框自己的事，
  // 挤进结果摘要只会让「这一行是有意义的」变成噪音。
  await page.evaluate(() => {
    const el = document.getElementById("gen_lan_dns");
    el.value = "";
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await page.waitForTimeout(200);
  await page.evaluate(() => document.querySelector('[data-act="genGenerate"]').click());
  await page.waitForTimeout(900);
  ck("留空时结果页不出现这一行", false, await page.evaluate(() =>
    document.getElementById("gen_vres").textContent.includes("局域网 DNS")));

  ck("没有页面异常", 0, errors.length);
  if (errors.length) console.log(errors.join("\n"));

  await browser.close();
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
