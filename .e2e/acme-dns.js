/* 离线回归：ACME 的验证方式（HTTP-01 / DNS-01）。
   这条链路的失败方式很安静：凭据框回显了真 token、或者只改个邮箱保存
   就把凭据清空了——界面一切正常，直到下次续期失败才发现。
   所以这里逐个动作真点一遍：切服务商 → 提示与占位符跟着换 → 保存时提交的
   是打码占位符。 */
const { launch } = require("./browser");
const path = require("path");
const URL = "file://" + path.resolve(__dirname, "..", "demo.html") + "#demo";

let pass = 0, fail = 0;
const ck = (n, want, got) => {
  const ok = String(want) === String(got);
  ok ? pass++ : fail++;
  console.log(`${ok ? "PASS" : "FAIL"} ${n}${ok ? "" : `  期望=[${want}] 实际=[${got}]`}`);
};
const has = (n, needle, hay) => {
  const ok = String(hay).includes(needle);
  ok ? pass++ : fail++;
  console.log(`${ok ? "PASS" : "FAIL"} ${n}${ok ? "" : `  缺少 [${needle}]，实际=[${hay}]`}`);
};

(async () => {
  const browser = await launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await page.goto(URL);
  await page.waitForTimeout(900);

  await page.evaluate(() => document.querySelector('.nav-item[data-v="settings"]').click());
  await page.waitForTimeout(500);
  await page.evaluate(async () => { await loadSettings(); await loadCerts(); });
  await page.waitForTimeout(400);

  // ---- 1. 下拉里是「HTTP-01 + 编译进来的那几个服务商」 ----
  const opts = await page.evaluate(() =>
    [...document.querySelectorAll("#s_acme_dns_provider option")]
      .map(o => ({ v: o.value, t: o.textContent })));
  ck("下拉选项数量（1 种 HTTP-01 + 3 个演示服务商）", 4, opts.length);
  ck("第一项是 HTTP-01", "", opts[0].v);
  has("HTTP-01 选项写明要占 80", ":80", opts[0].t);
  ck("演示里选的是 cloudflare", "cloudflare", await page.evaluate(() => $("s_acme_dns_provider").value));

  // ---- 2. 凭据框只显示打码值 ----
  const dnsBox = await page.evaluate(() => ({
    hidden: document.getElementById("acme_dns_box").classList.contains("hide"),
    env: document.getElementById("s_acme_dns_env").value,
    hint: document.getElementById("acme_dns_hint").textContent,
    mode: document.getElementById("acme_mode_hint").textContent
  }));
  ck("选了 DNS 服务商时凭据框可见", false, dnsBox.hidden);
  ck("凭据框显示的是打码占位符", "********", dnsBox.env);
  has("提示里有该服务的必填变量名", "CLOUDFLARE_DNS_API_TOKEN", dnsBox.hint);
  has("说明 DNS-01 不占端口", "不占用任何端口", dnsBox.mode);

  // ---- 3. 换服务商：提示、占位符、必填项都要跟着换 ----
  await page.evaluate(() => { const s = $("s_acme_dns_provider"); s.value = "alidns"; s.dispatchEvent(new Event("change", { bubbles: true })); });
  await page.waitForTimeout(250);
  const ali = await page.evaluate(() => ({
    hint: document.getElementById("acme_dns_hint").textContent,
    ph: document.getElementById("s_acme_dns_env").placeholder
  }));
  has("阿里云的提示写到 SECRET_KEY", "ALICLOUD_SECRET_KEY", ali.hint);
  has("阿里云的示例带上 ACCESS_KEY", "ALICLOUD_ACCESS_KEY", ali.ph);

  // ---- 4. 切回 HTTP-01：凭据框收起，并说明代价 ----
  await page.evaluate(() => { const s = $("s_acme_dns_provider"); s.value = ""; s.dispatchEvent(new Event("change", { bubbles: true })); });
  await page.waitForTimeout(250);
  const http01 = await page.evaluate(() => ({
    hidden: document.getElementById("acme_dns_box").classList.contains("hide"),
    mode: document.getElementById("acme_mode_hint").textContent
  }));
  ck("HTTP-01 时凭据框收起", true, http01.hidden);
  has("HTTP-01 说明要占 :80", ":80", http01.mode);
  has("HTTP-01 说明泛域名用不了", "泛域名", http01.mode);

  // ---- 5. 域名框支持一份证书多个名字 ----
  has("域名占位符给出泛域名写法", "*.example.com",
    await page.evaluate(() => $("s_acme_domain").placeholder));

  // ---- 6. 证书卡片给出全部名字与两个文件的路径 ----
  const info = await page.evaluate(() => document.getElementById("certinfo").textContent);
  has("证书覆盖裸域名", "example.com", info);
  has("证书覆盖泛域名", "*.example.com", info);
  has("标出这次是 DNS-01 且写清服务商", "DNS-01 · cloudflare", info);
  has("给出证书路径", "fullchain.pem", info);
  has("给出私钥路径", "privkey.pem", info);

  // ---- 7. 保存时提交的是占位符（不能把真凭据冲掉）----
  await page.evaluate(() => {
    window.__saved = null;
    const orig = window.req;
    window.req = (p, o) => {
      if (p === "/api/settings" && o && o.method === "PUT") window.__saved = o.body;
      return orig(p, o);
    };
    const s = $("s_acme_dns_provider"); s.value = "cloudflare";
    s.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await page.waitForTimeout(200);
  await page.evaluate(() => saveSettings());
  await page.waitForTimeout(400);
  const saved = await page.evaluate(() => window.__saved || {});
  ck("保存时提交的服务商", "cloudflare", saved.acme_dns_provider);
  ck("保存时提交的凭据仍是打码占位符", "********", saved.acme_dns_env);
  ck("保存时带上域名列表", "example.com,*.example.com", saved.acme_domain);

  // ---- 8. 窄屏不能横向溢出 ----
  const narrow = await browser.newPage({ viewport: { width: 360, height: 780 } });
  await narrow.goto(URL);
  await narrow.waitForTimeout(800);
  await narrow.evaluate(() => document.querySelector('.nav-item[data-v="settings"]').click());
  await narrow.waitForTimeout(400);
  const ov = await narrow.evaluate(() =>
    document.documentElement.scrollWidth - document.documentElement.clientWidth);
  ck("360px 下无横向溢出", 0, ov);
  await narrow.close();

  console.log(`\n${pass} PASS / ${fail} FAIL`);
  await browser.close();
  process.exit(fail ? 1 : 0);
})();
