/* 回归：入站弹窗里的两块 Reality UI —— spiderX 输入框 + 随机按钮，
   以及目标择优弹窗里的「取 SNI」面板。
   用离线 demo.html 跑（不连后端），SNI 面板灌假数据渲染。

   钉住的四件事：
     1) Reality 分支下 spiderX 控件在，默认值非空（空值导出的客户端会连不上）；
     2) 「随机」真的换了一个值，并且同步进了 ibGetSettings()；
     3) 「全部并入」把面板里验过的 SNI 都写进 serverNames；
     4) 换目标时只摘掉旧目标主机名那一项，用户自己配的其余名字不动，首位仍是最初那个。
   第 4 条是导出逻辑的命门：客户端拿 serverNames 首位当 SNI。 */
const { launch } = require("./browser");
const path = require("path");

let bad = 0;
function ok(cond, msg, extra) {
  if (cond) console.log("  ✓ " + msg);
  else { bad++; console.log("  ✗ " + msg + (extra === undefined ? "" : "  → " + JSON.stringify(extra))); }
}
const names = s => String(s || "").split(/[\s,]+/).map(x => x.trim()).filter(Boolean);

(async () => {
  const b = await launch();
  const p = await b.newPage({ viewport: { width: 1360, height: 950 } });
  const errs = [];
  p.on("pageerror", e => errs.push("pageerror: " + e.message));
  p.on("console", m => { if (m.type() === "error") errs.push("console: " + m.text()); });

  const file = "file://" + path.resolve(__dirname, "../demo.html") + "#demo";
  await p.goto(file);
  await p.waitForTimeout(900);

  // ---- 1. 新建入站 → Reality → spiderX ----
  await p.evaluate(() => openInboundModal());
  await p.waitForTimeout(500);
  await p.evaluate(() => {
    $("ib_protocol").value = "vless";
    $("ib_tls").value = "reality";
    ibOnTLS();
  });
  await p.waitForTimeout(300);

  const spider = await p.evaluate(() => {
    const el = $("ib_f_spider");
    return { exists: !!el, value: el && el.value, tag: el && el.tagName };
  });
  ok(spider.exists && spider.tag === "INPUT", "Reality 分支有 spiderX 输入框", spider);
  ok(String(spider.value || "").length > 0, "spiderX 默认值非空", spider.value);

  const before = spider.value;
  await p.evaluate(() => genSpider());
  await p.waitForTimeout(200);
  const after = await p.evaluate(() => $("ib_f_spider").value);
  ok(after && after !== before, "「随机」换了一个新值", { before, after });

  const inSettings = await p.evaluate(() => ibGetSettings().spider_x || "");
  ok(inSettings === after, "spiderX 同步进 ibGetSettings()", { after, inSettings });

  // ---- 2. 目标择优弹窗 + SNI 面板（假数据）----
  await p.evaluate(() => {
    closeModal("mInbound");
    $("ib_f_handshake").value = "www.amd.com";
    $("ib_f_snames").value = "www.apple.com";
    openDestPick();
    __destResults = [
      { host: "www.amd.com", port: 443, sni: "www.amd.com", ok: true, best: true, ms: 312,
        tls: "1.3", alpn: "h2", h2: true, cert_ok: true, issuer: "DigiCert Inc",
        not_after: "2027-05-12", days_left: 189, detail: "TLS 1.3 握手成功", warning: "" },
      { host: "www.apple.com", port: 443, sni: "www.apple.com", ok: true, best: false, ms: 148,
        tls: "1.3", alpn: "h2", h2: true, cert_ok: true, issuer: "Apple Public CA",
        not_after: "2027-01-01", days_left: 58, detail: "TLS 1.3 握手成功", warning: "" }
    ];
    renderDestResults();
    renderSNIPanel(__destResults[0], {
      host: "www.amd.com", port: 443, tried: 12,
      snis: [
        { sni: "www.amd.com", ok: true, ms: 407, source: "目标本身" },
        { sni: "amd.com", ok: true, ms: 340, source: "证书 SAN" },
        { sni: "connect.amd.com", ok: true, ms: 323, source: "证书 SAN" },
        { sni: "drivers.amd.com", ok: true, ms: 425, source: "证书 SAN" }
      ],
      rejected: [{ sni: "api.amd.com", source: "通配符展开", detail: "证书与 SNI 对不上（x509: certificate is valid for *.amd.com）" }],
      cert: { issuer: "DigiCert Inc", not_after: "2027-05-12", days_left: 189,
              names: ["www.amd.com", "amd.com"], wildcards: ["*.amd.com"] },
      note: "证书里一共 28 个可用名字，这里实测了前 12 个——填两三个就够轮换了，不必全要。"
    });
  });
  await p.waitForTimeout(300);

  // 面板渲染出来了吗
  const panel = await p.$$eval("#dest_sni [data-act]", bs => bs.length);
  ok(panel >= 5, "SNI 面板渲染出可用/被排除项", panel);

  // 点「全部并入」
  const merged = await p.evaluate(() => {
    const btns = [...document.querySelectorAll('#dest_sni [data-act="mergeSNI"]')];
    const all = btns.find(b => (b.textContent || "").includes("全部"));
    if (!all) return null;
    all.click();
    return $("ib_f_snames").value;
  });
  ok(merged !== null, "有「全部并入」按钮", merged);
  const mergedNames = names(merged);
  for (const n of ["www.amd.com", "amd.com", "connect.amd.com", "drivers.amd.com"]) {
    ok(mergedNames.includes(n), `全部并入后含 ${n}`, mergedNames);
  }

  // 换成 apple：只摘掉旧目标主机名那一项，其余保留，首位不动
  const afterUse = await p.evaluate(() => {
    useDest(1);
    return $("ib_f_snames").value;
  });
  const afterNames = names(afterUse);
  ok(!afterNames.includes("www.amd.com"), "换目标后旧目标主机名被摘掉", afterNames);
  for (const n of ["www.apple.com", "amd.com", "connect.amd.com", "drivers.amd.com"]) {
    ok(afterNames.includes(n), `换目标后保留 ${n}`, afterNames);
  }
  ok(afterNames[0] === "www.apple.com", "首位仍是用户自己配的名字", afterNames);

  // 离线跑 demo，缺图之类的 404 是预期的；真的 JS 报错才算挂
  const real = errs.filter(e => !/ERR_FILE_NOT_FOUND|Failed to load resource/.test(e));
  ok(real.length === 0, "没有 JS 报错", real);

  await b.close();
  console.log(bad === 0 ? "\n全部通过" : `\n${bad} 项失败`);
  process.exit(bad === 0 ? 0 : 1);
})();
