/* 离线回归：新建入站的「节点地址」选择器（IP / 域名 / 跟随面板设置）。
   这条链路的失败方式很安静：选完保存、界面一切正常，只是导出的链接指向了
   另一个地址，要到客户端连不上才发现。所以这里逐个动作真点一遍：
   切换模式 → 自动带出候选 → 保存 → 列表回显 → 再打开时模式回填。 */
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
  await page.goto(URL);
  await page.waitForTimeout(900);

  // ---- 1. 列表里能看出每条入站连的是哪个地址 ----
  const rows = await page.evaluate(() => {
    const trs = [...document.querySelectorAll("#ibtbody tr")];
    return trs.map(tr => ({
      name: tr.querySelector("b")?.textContent || "",
      addr: tr.querySelector(".ib-addr")?.textContent.trim() || ""
    }));
  });
  ck("列表有 3 条演示入站", 3, rows.length);
  ck("REALITY 入站标出 IP", "→ 203.0.113.10", rows[0].addr);
  ck("CDN 入站标出域名", "→ cdn.example.com", rows[1].addr);
  ck("未指定的入站不显示地址", "", rows[2].addr);

  // ---- 2. 新增入站：默认「跟随面板设置」 ----
  await page.evaluate(() => openInboundModal());
  await page.waitForTimeout(900);
  const d0 = await page.evaluate(() => ({
    open: document.getElementById("mInbound").classList.contains("show"),
    on: [...document.querySelectorAll("#ib_addr_seg button")].filter(b => b.classList.contains("on")).map(b => b.dataset.mode),
    inputHidden: document.getElementById("ib_nodehost").classList.contains("hide"),
    hint: document.getElementById("ib_addr_hint").textContent,
    options: [...document.querySelectorAll("#ib_addr_domains option")].map(o => o.value)
  }));
  ck("弹窗打开", true, d0.open);
  ck("默认只选中「跟随面板设置」", "inherit", (d0.on || []).join(","));
  ck("跟随时输入框隐藏", true, d0.inputHidden);
  ck("提示写出跟随的地址", true, d0.hint.includes("vps.example.com"));
  ck("域名候选来自后端", "vps.example.com,cdn.example.com", d0.options.join(","));

  // ---- 3. 切到「用 IP」：自动带出候选并给出版本场景说明 ----
  await page.evaluate(() => document.querySelector('#ib_addr_seg button[data-mode="ip"]').click());
  await page.waitForTimeout(300);
  const d1 = await page.evaluate(() => ({
    on: [...document.querySelectorAll("#ib_addr_seg button")].filter(b => b.classList.contains("on")).map(b => b.dataset.mode),
    value: document.getElementById("ib_nodehost").value,
    hidden: document.getElementById("ib_nodehost").classList.contains("hide"),
    hint: document.getElementById("ib_addr_hint").textContent
  }));
  ck("选中「用 IP」", "ip", (d1.on || []).join(","));
  ck("输入框显示出来", false, d1.hidden);
  ck("自动带出公网 IP", "203.0.113.10", d1.value);
  ck("提示说明 CDN 场景", true, d1.hint.includes("CDN"));

  // ---- 4. 切到「用域名」：换成域名候选 ----
  await page.evaluate(() => document.querySelector('#ib_addr_seg button[data-mode="domain"]').click());
  await page.waitForTimeout(300);
  const d2 = await page.evaluate(() => document.getElementById("ib_nodehost").value);
  ck("切到域名时替换成域名候选", "vps.example.com", d2);

  // ---- 5. 选「用域名」但清空 → 必须拦住，不能静默退回跟随 ----
  await page.evaluate(() => {
    document.getElementById("ib_nodehost").value = "";
    saveInbound();
  });
  await page.waitForTimeout(500);
  const blocked = await page.evaluate(() => ({
    stillOpen: document.getElementById("mInbound").classList.contains("show"),
    toasts: [...document.querySelectorAll("#toasts .toast")].map(t => t.textContent)
  }));
  ck("空地址不放行（弹窗还开着）", true, blocked.stillOpen);
  ck("提示说清怎么改", true, blocked.toasts.some(t => t.includes("没填值")));

  // ---- 6. 填回 IP 并保存 → 列表出现新行且带地址 ----
  await page.evaluate(() => {
    document.getElementById("ib_nodehost").value = "198.51.100.42";
    document.getElementById("ib_name").value = "new-node";
    saveInbound();
  });
  await page.waitForTimeout(900);
  const after = await page.evaluate(() => {
    const trs = [...document.querySelectorAll("#ibtbody tr")];
    return {
      closed: !document.getElementById("mInbound").classList.contains("show"),
      count: trs.length,
      last: trs.length ? {
        name: trs[trs.length - 1].querySelector("b")?.textContent || "",
        addr: trs[trs.length - 1].querySelector(".ib-addr")?.textContent.trim() || ""
      } : null
    };
  });
  ck("保存后弹窗关闭", true, after.closed);
  ck("列表多了一条", 4, after.count);
  ck("新行名字正确", "new-node", after.last && after.last.name);
  ck("新行标出手填的地址", "→ 198.51.100.42", after.last && after.last.addr);

  // ---- 7. 再打开编辑：模式与值必须回填（否则一编辑就被重置） ----
  await page.evaluate(() => {
    const btn = [...document.querySelectorAll("#ibtbody tr")].pop().querySelector('[data-act="openInboundModal"]');
    btn.click();
  });
  await page.waitForTimeout(900);
  const edit = await page.evaluate(() => ({
    on: [...document.querySelectorAll("#ib_addr_seg button")].filter(b => b.classList.contains("on")).map(b => b.dataset.mode),
    value: document.getElementById("ib_nodehost").value
  }));
  ck("编辑时回填成「用 IP」", "ip", (edit.on || []).join(","));
  ck("编辑时回填地址", "198.51.100.42", edit.value);

  // ---- 8. 已指定域名的入站，编辑时应回填成「用域名」 ----
  await page.evaluate(() => closeModal("mInbound"));
  await page.waitForTimeout(300);
  await page.evaluate(() => {
    document.querySelectorAll("#ibtbody tr")[1].querySelector('[data-act="openInboundModal"]').click();
  });
  await page.waitForTimeout(900);
  const edit2 = await page.evaluate(() => ({
    on: [...document.querySelectorAll("#ib_addr_seg button")].filter(b => b.classList.contains("on")).map(b => b.dataset.mode),
    value: document.getElementById("ib_nodehost").value
  }));
  ck("域名入站回填成「用域名」", "domain", (edit2.on || []).join(","));
  ck("域名入站回填地址", "cdn.example.com", edit2.value);

  console.log(`\n${pass} PASS / ${fail} FAIL`);
  await browser.close();
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
