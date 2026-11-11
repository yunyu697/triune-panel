/* 离线回归：配置生成新增的 OpenWrt-momo 目标。
   这里最容易「看起来没问题」的失败是：按钮加上了、内核却还是 mihomo，
   于是后端 400；或者演示数据没跟上，生成的 JSON 里没有 momo 按 tag 找的
   那几个入站。两种都要真点一遍才拦得住。 */
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
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  await page.goto(URL);
  await page.waitForTimeout(700);
  await page.evaluate(() => {
    document.querySelector('.nav-item[data-v="gen"]').click();
    const cbs = document.querySelectorAll("#gen_inbounds input[type=checkbox]");
    if (cbs.length) cbs[0].checked = true;
  });
  await page.waitForTimeout(300);

  // ---- 1. 目标段里真有第四个按钮 ----
  const targets = await page.evaluate(() =>
    [...document.querySelectorAll("#g_target_seg button")].map(b => b.dataset.v));
  ck("目标按钮数量", 4, targets.length);
  has("目标里有 openwrt-momo", "openwrt-momo", targets.join(","));

  // ---- 2. 选 momo：内核自动切 sing-box，mihomo 禁用，版本不低于 1.12 ----
  await page.evaluate(() => {
    const sel = document.getElementById("g_sbver");
    sel.value = "1.11";
    sel.dispatchEvent(new Event("change", { bubbles: true }));
    document.querySelector('#g_target_seg button[data-v="openwrt-momo"]').click();
  });
  await page.waitForTimeout(300);
  const state = await page.evaluate(() => ({
    core: genCore,
    target: genTarget,
    params: buildGenParams(),
    mihomoDisabled: document.querySelector('#g_core_seg button[data-v="mihomo"]').disabled,
    coreOn: [...document.querySelectorAll("#g_core_seg button")].filter(b => b.classList.contains("on")).map(b => b.dataset.v),
    sbverVisible: document.getElementById("g_sbver_wrap").style.display !== "none",
    note: document.getElementById("gen_target_note").textContent
  }));
  ck("切 momo 后内核变成 sing-box", "singbox", state.core);
  ck("界面上高亮的内核也只有 sing-box", "singbox", state.coreOn.join(","));
  ck("mihomo 按钮被禁用", "true", state.mihomoDisabled);
  ck("请求体 core 是 singbox", "singbox", state.params.core);
  ck("请求体 target 是 openwrt-momo", "openwrt-momo", state.params.target);
  has("请求体带 sing-box 版本", "core_version", JSON.stringify(state.params));
  has("1.11 被抬到 1.12+", "false", String(state.params.core_version === "1.11"));
  ck("版本下拉可见", "true", state.sbverVisible);
  has("目标提示里写明 momo 的 tag", "tun-in", state.note);
  has("目标提示里写明 auto_route", "auto_route:false", state.note);

  // ---- 3. 真生成一份，检查 profile 里 momo 要读的东西 ----
  await page.evaluate(() => document.querySelector('[data-act="genGenerate"]').click());
  await page.waitForTimeout(900);
  const out = await page.evaluate(() => document.getElementById("gen_out").textContent);
  let cfg = {};
  try { cfg = JSON.parse(out); } catch (e) { cfg = {}; }
  const tags = (cfg.inbounds || []).map(i => i.tag);
  for (const t of ["dns-in", "redirect-in", "tproxy-in", "tun-in", "mixed-in"]) {
    has(`生成的入站含 ${t}`, t, tags.join(","));
  }
  const tun = (cfg.inbounds || []).find(i => i.tag === "tun-in") || {};
  ck("tun-in 有 interface_name", "momo", tun.interface_name);
  ck("tun-in 的 auto_route 是 false", "false", String(tun.auto_route));
  const servers = (cfg.dns && cfg.dns.servers) || [];
  has("DNS 里有 fake-ip-dns-server", "fake-ip-dns-server", servers.map(s => s.tag).join(","));
  const rules = (cfg.route && cfg.route.rules) || [];
  ck("第一条路由规则是 sniff", "sniff", rules[0] && rules[0].action);
  has("路由里有 dns-in 的 DNS 劫持", "dns-in", JSON.stringify(rules.slice(0, 3)));
  ck("规则集直连下载", "direct", ((cfg.route || {}).rule_set || [])[0] && cfg.route.rule_set[0].download_detour);
  ck("生成结果可下载", "false", String(await page.evaluate(() => document.getElementById("gen_dl").disabled)));

  // ---- 4. 切目标要让旧结果失效，不能拿上一份配置冒充 ----
  await page.evaluate(() => document.querySelector('#g_target_seg button[data-v="client"]').click());
  await page.waitForTimeout(200);
  ck("切走后下载按钮禁用", "true", await page.evaluate(() => document.getElementById("gen_dl").disabled));
  ck("切走后 mihomo 恢复可用", "false", await page.evaluate(() =>
    document.querySelector('#g_core_seg button[data-v="mihomo"]').disabled));

  // ---- 5. 四个按钮在窄屏不能撑出横向滚动条 ----
  const narrow = await browser.newPage({ viewport: { width: 360, height: 760 } });
  await narrow.goto(URL);
  await narrow.waitForTimeout(700);
  await narrow.evaluate(() => document.querySelector('.nav-item[data-v="gen"]').click());
  await narrow.waitForTimeout(300);
  const ov = await narrow.evaluate(() =>
    document.documentElement.scrollWidth - document.documentElement.clientWidth);
  ck("360px 下无横向溢出", 0, ov);
  await narrow.close();

  ck("页面无 JS 异常", 0, errors.length);
  if (errors.length) console.log("  JS 错误:", errors.slice(0, 5));
  console.log(`\n${pass} PASS / ${fail} FAIL`);
  await browser.close();
  process.exit(fail ? 1 : 0);
})();
