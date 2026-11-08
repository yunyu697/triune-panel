/* 「绕过节点服务器」在离线演示里的真实点击回归。
 * 单测只能证明字符串拼对了；这里真的点开关、切内核、生成、读结果。 */
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
    if (cbs.length) cbs[0].checked = true;   // 至少要选一个入站
  });
  await page.waitForTimeout(300);

  ck("开关默认勾选", "true", await page.evaluate(() => document.getElementById("gen_bypass").checked));
  ck("buildGenParams 带出 bypass_server", "true",
    await page.evaluate(() => buildGenParams().bypass_server));

  const generate = async () => {
    await page.evaluate(() => document.querySelector('[data-act="genGenerate"]').click());
    await page.waitForTimeout(900);
    return page.evaluate(() => ({
      out: document.getElementById("gen_out").textContent,
      info: document.getElementById("gen_vres").textContent,
      infoShown: !document.getElementById("gen_vres").classList.contains("hide"),
      dlEnabled: !document.getElementById("gen_dl").disabled
    }));
  };

  // ---- 1. mihomo：默认开启，规则排在最前 ----
  let r = await generate();
  // 只看 rules: 之后的列表：前面 dns-hijack 也有 "- any:53"，整份 YAML 里
  // 第一条 "- " 并不是规则。
  const all = r.out.split("\n");
  const ri = all.findIndex(x => x.trim() === "rules:");
  const lines = all.slice(ri + 1).filter(x => /^\s*- /.test(x)).map(x => x.trim());
  ck("mihomo 第一条规则是节点直连", "- DOMAIN,demo.example.com,DIRECT", lines[0]);
  ck("生成结果已可下载", "true", r.dlEnabled);
  ck("页面回显了直连地址", true, r.info.includes("demo.example.com"));

  // ---- 2. 关掉开关：旧结果立刻失效，重新生成不再有直连规则 ----
  await page.evaluate(() => document.getElementById("gen_bypass").click());
  await page.waitForTimeout(200);
  ck("切换开关后旧结果失效", "true", await page.evaluate(() => document.getElementById("gen_dl").disabled));
  ck("buildGenParams 反映关闭状态", "false",
    await page.evaluate(() => buildGenParams().bypass_server));
  r = await generate();
  ck("关闭后没有节点直连规则", "false", r.out.includes("DOMAIN,demo.example.com,DIRECT"));
  ck("关闭后不显示直连地址", "false", r.infoShown && r.info.includes("节点服务器直连"));

  // ---- 3. 重新打开 + 切到 sing-box：直连规则紧跟 DNS 劫持 ----
  await page.evaluate(() => {
    document.getElementById("gen_bypass").click();
    document.querySelector('#g_core_seg button[data-v="singbox"]').click();
  });
  await page.waitForTimeout(200);
  r = await generate();
  let rules;
  try { rules = JSON.parse(r.out).route.rules; } catch (e) { rules = []; }
  ck("sing-box 第一条仍是 DNS 劫持", "hijack-dns", rules[0] && rules[0].action);
  ck("sing-box 第二条是节点域名直连", "direct", rules[1] && rules[1].outbound);
  ck("sing-box 直连目标正确", "demo.example.com", rules[1] && rules[1].domain && rules[1].domain[0]);

  // ---- 4. 保存的参数里必须有这个字段，重新生成才不会丢 ----
  const saved = await page.evaluate(() => JSON.stringify(buildGenParams()));
  ck("保存参数含 bypass_server:true", true, saved.includes('"bypass_server":true'));

  ck("页面无 JS 异常", 0, errors.length);
  if (errors.length) console.log("  JS 错误:", errors.slice(0, 5));
  await browser.close();
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
