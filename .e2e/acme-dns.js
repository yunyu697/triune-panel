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

  // ---- 8. 证书卡必须能自己保存（它跟「基础设置」不在同一个分组里）----
  // 这条是回归用例：证书卡曾经没有保存按钮，也不在常驻条与脏点那两张表里，
  // 于是「切成 DNS-01 + 贴凭据」之后没有任何按钮可按，切组也不提醒，
  // 用户以为存上了，实际库里仍是 HTTP-01，直到签发报「泛域名只能用 DNS-01」。
  const wiring = await page.evaluate(() => {
    const card = document.getElementById("set_card_cert");
    return {
      btn: !!(card && card.querySelector('[data-act="saveCertSettings"]')),
      bar: SET_BAR_SECTIONS.some(s => s.id === "set_card_cert" && s.label === "保存证书设置"),
      dirty: SET_SAVE_CARDS.includes("set_card_cert"),
      fn: typeof window.saveCertSettings,
    };
  });
  ck("证书卡有自己的保存按钮", true, wiring.btn);
  ck("常驻条认得证书卡", true, wiring.bar);
  ck("证书卡参与「未保存」脏点", true, wiring.dirty);
  ck("保存函数是可派发的全局动作", "function", wiring.fn);

  // 改凭据要立刻点亮「运维」分组标签上的脏点
  await page.evaluate(() => {
    const ta = $("s_acme_dns_env");
    ta.value = "CLOUDFLARE_DNS_API_TOKEN=real-token";
    ta.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await page.waitForTimeout(150);
  ck("改凭据后运维标签点亮脏点", true,
    await page.evaluate(() => !!document.querySelector('#set_sect_seg button[data-sect="maintain"].dirty')));

  // 保存：提交的必须是界面上的真值（而不是只提交域名/邮箱），存完回显打码、脏点消失
  await page.evaluate(() => {
    window.__certSaved = null;
    const orig = window.req;
    window.req = (p, o) => {
      if (p === "/api/settings" && o && o.method === "PUT") window.__certSaved = o.body;
      return orig(p, o);
    };
  });
  await page.evaluate(() => saveCertSettings());
  await page.waitForTimeout(400);
  const certSaved = await page.evaluate(() => window.__certSaved || {});
  ck("保存提交了验证方式", "cloudflare", certSaved.acme_dns_provider);
  ck("保存提交了刚填的凭据", "CLOUDFLARE_DNS_API_TOKEN=real-token", certSaved.acme_dns_env);
  ck("保存提交了域名", "example.com,*.example.com", certSaved.acme_domain);
  ck("保存后凭据框回显打码", "********",
    await page.evaluate(() => document.getElementById("s_acme_dns_env").value));
  ck("保存后脏点消失", false,
    await page.evaluate(() => !!document.querySelector('#set_sect_seg button[data-sect="maintain"].dirty')));

  // 常驻条：停在证书卡时应显示「保存证书设置」。把组件更新卡临时藏起来，
  // 判定结果就不依赖视口高度与两张卡的相对位置。
  const bar = await page.evaluate(() => {
    document.querySelector('#set_sect_seg button[data-sect="maintain"]').click();
    const main = document.querySelector(".main"), upd = $("set_card_update");
    const prev = upd.style.display;
    upd.style.display = "none";
    main.scrollTop = main.scrollHeight;
    updateSetBar();
    const out = { label: $("set_bar_btn").textContent,
                  vis: getComputedStyle($("set_bar")).visibility };
    upd.style.display = prev;
    return out;
  });
  ck("常驻条在证书卡显示「保存证书设置」", "保存证书设置", bar.label);
  ck("常驻条在证书卡可见", "visible", bar.vis);

  // ---- 9. 窄屏不能横向溢出 ----
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
