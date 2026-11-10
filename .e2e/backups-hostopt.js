/* 离线回归：备份管理与主机优化两个新页面在 demo 里能否真的走通。
   重点不是「元素存在」，而是事件委托链路：导航切页 → 数据渲染 → 预览 →
   确认弹窗 → 删除/应用/撤销 → 重新渲染。任何一个动作没挂上 data-act，
   点击都会静默无反应，Go 侧的字符串测试看不出来。 */
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

  // ---- 1. 两个入口都在导航里，且不在概览页 ----
  const nav = await page.evaluate(() => {
    const keys = [...document.querySelectorAll(".nav-item")].map(b => b.dataset.v);
    return { backups: keys.includes("backups"), hostopt: keys.includes("hostopt"), total: keys.length };
  });
  ck("导航含「备份管理」入口", true, nav.backups);
  ck("导航含「主机优化」入口", true, nav.hostopt);
  ck("导航项总数随之增加", true, nav.total >= 12);

  // ---- 2. 点导航切到备份管理，清单与总览必须渲染出来 ----
  await page.evaluate(() => document.querySelector('.nav-item[data-v="backups"]').click());
  await page.waitForTimeout(500);
  const bk = await page.evaluate(() => ({
    view: document.body.dataset.view,
    active: document.querySelector(".view.active")?.id,
    rows: document.querySelectorAll("#bk_body tr").length,
    hasHeader: document.querySelectorAll("#bk_body tr th").length === 0,
    summary: document.getElementById("bk_summary").textContent,
    hint: document.getElementById("bk_hint").textContent,
    keep: document.getElementById("bk_keep").value,
    auto: document.getElementById("bk_auto").checked
  }));
  ck("切到了备份管理页", "backups", bk.view);
  ck("active 视图是 v-backups", "v-backups", bk.active);
  ck("演示清单渲染出 9 条", 9, bk.rows);
  ck("总览含容量单位", true, /MB|GB/.test(bk.summary));
  ck("提示说明归档含内核二进制", true, bk.hint.includes("内核二进制"));
  ck("策略默认不限制", "0", bk.keep);
  ck("自动清理默认关闭", false, bk.auto);

  // ---- 3. 保存策略 → 预览 → 确认删除，完整走一遍 ----
  await page.evaluate(() => {
    document.getElementById("bk_maxdays").value = "3";
    document.querySelector('[data-act="saveBackupPolicy"]').click();
  });
  await page.waitForTimeout(600);
  const saved = await page.evaluate(() => ({
    value: document.getElementById("bk_maxdays").value,
    hint: document.getElementById("bk_policy_hint").textContent
  }));
  ck("保存后策略回填没被冲掉", "3", saved.value);
  ck("策略提示仍是自动清理关闭", true, saved.hint.includes("关闭"));

  await page.evaluate(() => document.querySelector('[data-act="previewBackupPrune"]').click());
  await page.waitForTimeout(500);
  const preview = await page.evaluate(() => {
    const t = document.getElementById("bk_preview").textContent;
    return { text: t, n: (t.match(/将清理 (\d+) 份/) || [])[1] || "0" };
  });
  ck("预览列出了待清理条目", true, +preview.n > 0);
  ck("预览写明释放空间", true, preview.text.includes("约"));

  const beforeRows = bk.rows;
  await page.evaluate(() => {
    document.querySelector("#bk_body .bk-sel").checked = true;
    document.querySelector('[data-act="deleteSelectedBackups"]').click();
  });
  await page.waitForTimeout(150);
  const confirmShown = await page.evaluate(() => document.getElementById("mConfirm").classList.contains("show"));
  ck("删除前弹出二次确认", true, confirmShown);
  await page.evaluate(() => document.getElementById("cf_ok").click());
  await page.waitForTimeout(700);
  const afterRows = await page.evaluate(() => document.querySelectorAll("#bk_body tr").length);
  ck("确认后清单少了一份", beforeRows - 1, afterRows);

  // ---- 4. 切到主机优化，体检项与命令都渲染 ----
  await page.evaluate(() => document.querySelector('.nav-item[data-v="hostopt"]').click());
  await page.waitForTimeout(500);
  const ho = await page.evaluate(() => ({
    view: document.body.dataset.view,
    active: document.querySelector(".view.active")?.id,
    checks: document.querySelectorAll("#ho_checks > div").length,
    cmds: document.getElementById("ho_cmds").textContent,
    meta: document.getElementById("ho_meta").textContent,
    applyShown: !document.getElementById("ho_apply").classList.contains("hide"),
    revertShown: !document.getElementById("ho_revert").classList.contains("hide")
  }));
  ck("切到了主机优化页", "hostopt", ho.view);
  ck("active 视图是 v-hostopt", "v-hostopt", ho.active);
  ck("十一项体检都渲染了", 11, ho.checks);
  ck("给了手动执行命令", true, ho.cmds.includes("sysctl"));
  ck("元信息显示未应用", true, ho.meta.includes("未应用"));
  ck("可应用时显示应用按钮", true, ho.applyShown);
  ck("未应用时不显示撤销按钮", false, ho.revertShown);

  // ---- 5. 改参数 → 差异预览 → 应用 → 撤销，按钮状态必须跟着变 ----
  // 与当前值一致时不该弹确认：没有变更可确认，直接提示「无需应用」。
  const diffIdle = await page.evaluate(() => document.getElementById("ho_diff").textContent);
  ck("未改动时差异预览写明一致", true, diffIdle.includes("一致"));
  ck("未改动时点应用不弹确认", false, await page.evaluate(async () => {
    document.getElementById("ho_apply").click();
    await new Promise(r => setTimeout(r, 150));
    return document.getElementById("mConfirm").classList.contains("show");
  }));

  const diffAfter = await page.evaluate(() => {
    const set = (id, v) => {
      const el = document.getElementById(id);
      el.value = v;
      el.dispatchEvent(new Event("change", { bubbles: true }));
    };
    set("ho_cc", "bbr");
    set("ho_qdisc", "fq");
    return document.getElementById("ho_diff").textContent;
  });
  ck("改动两项后差异预览计数", true, diffAfter.includes("2 项将被修改"));
  ck("差异预览列出拥塞控制", true, diffAfter.includes("拥塞控制"));
  ck("有变更后应用按钮可点且写明项数", "应用 2 项变更", await page.evaluate(() => {
    const b = document.getElementById("ho_apply");
    return b.disabled ? "(disabled)" : b.textContent.trim();
  }));

  await page.evaluate(() => document.getElementById("ho_apply").click());
  await page.waitForTimeout(150);
  ck("应用前有二次确认", true,
    await page.evaluate(() => document.getElementById("mConfirm").classList.contains("show")));
  ck("确认弹窗列出待改项", true,
    await page.evaluate(() => document.getElementById("cf_text").textContent.includes("项变更")));
  await page.evaluate(() => document.getElementById("cf_ok").click());
  await page.waitForTimeout(600);
  const applied = await page.evaluate(() => ({
    meta: document.getElementById("ho_meta").textContent,
    applyShown: !document.getElementById("ho_apply").classList.contains("hide"),
    applyDisabled: document.getElementById("ho_apply").disabled,
    revertShown: !document.getElementById("ho_revert").classList.contains("hide")
  }));
  ck("应用后显示已应用", true, applied.meta.includes("已应用"));
  ck("应用后应用按钮保留但不可点", true, applied.applyShown && applied.applyDisabled);
  ck("应用后出现撤销按钮", true, applied.revertShown);

  await page.evaluate(() => document.getElementById("ho_revert").click());
  await page.waitForTimeout(150);
  await page.evaluate(() => document.getElementById("cf_ok").click());
  await page.waitForTimeout(600);
  const reverted = await page.evaluate(() => ({
    meta: document.getElementById("ho_meta").textContent,
    revertShown: !document.getElementById("ho_revert").classList.contains("hide")
  }));
  ck("撤销后回到未应用", true, reverted.meta.includes("未应用"));
  ck("撤销后隐藏撤销按钮", false, reverted.revertShown);

  // ---- 6. 体检项的一键修复与回退 ----
  // 体检只报状态时用户还得自己查「该改哪个键」。能代劳的项带修复按钮；
  // 不能代劳的项按钮禁用并写明原因（藏起来会被当成页面漏了）。
  // 这里的断言全部走事件委托链路：data-act 没挂上时点击会静默无反应。
  const fixUI = await page.evaluate(() => {
    const rows = [...document.querySelectorAll("#ho_checks > div")];
    const row = t => rows.find(x => x.textContent.includes(t));
    const fdRow = row("文件描述符上限"), ckRow = row("时钟同步");
    const fdBtn = fdRow && fdRow.querySelector('button[data-act="hostOptFix"]');
    const ckBtn = ckRow && ckRow.querySelector('button[data-act="hostOptFix"]');
    return {
      fdBtn: !!fdBtn, fdLabel: fdBtn ? fdBtn.textContent.trim() : "", fdDisabled: fdBtn ? fdBtn.disabled : null,
      ckBtn: !!ckBtn, ckDisabled: ckBtn ? ckBtn.disabled : null,
      ckReason: ckRow ? ckRow.textContent.includes("NTP=yes") : false,
      fixedArea: (document.getElementById("ho_fixes").textContent || "").trim(),
    };
  });
  ck("失败的体检项带修复按钮", true, fixUI.fdBtn);
  ck("修复按钮写明目标值", true, fixUI.fdLabel.includes("65535"));
  ck("可修复项按钮可点", false, fixUI.fdDisabled);
  ck("不可修复项仍显示按钮", true, fixUI.ckBtn);
  ck("不可修复项按钮禁用", true, fixUI.ckDisabled);
  ck("不可修复项写明原因", true, fixUI.ckReason);
  ck("未修复前没有回退区", "", fixUI.fixedArea);

  await page.evaluate(() => {
    const rows = [...document.querySelectorAll("#ho_checks > div")];
    rows.find(x => x.textContent.includes("文件描述符上限"))
      .querySelector('button[data-act="hostOptFix"]').click();
  });
  await page.waitForTimeout(150);
  ck("修复前有二次确认", true,
    await page.evaluate(() => document.getElementById("mConfirm").classList.contains("show")));
  ck("确认弹窗说明会记原值并可回退", true,
    await page.evaluate(() => document.getElementById("cf_text").textContent.includes("回退")));
  await page.evaluate(() => document.getElementById("cf_ok").click());
  await page.waitForTimeout(800);

  const afterFix = await page.evaluate(() => {
    const rows = [...document.querySelectorAll("#ho_checks > div")];
    const fdRow = rows.find(x => x.textContent.includes("文件描述符上限"));
    const area = document.getElementById("ho_fixes");
    return {
      rowOk: fdRow ? fdRow.textContent.includes("✓") : false,
      stillBtn: !!(fdRow && fdRow.querySelector('button[data-act="hostOptFix"]')),
      areaText: (area.textContent || "").trim(),
      revertBtn: !!(area.querySelector('button[data-act="hostOptRevertFix"]')),
      changes: area.textContent.includes("65535"),
    };
  });
  ck("修复后体检项转 OK", true, afterFix.rowOk);
  ck("修复后行内按钮消失", false, afterFix.stillBtn);
  ck("修复后出现「已修复」区", true, afterFix.areaText.includes("面板已修复 1 项"));
  ck("已修复区写明改了什么", true, afterFix.changes);
  ck("已修复区提供回退按钮", true, afterFix.revertBtn);

  await page.evaluate(() => document.querySelector('#ho_fixes button[data-act="hostOptRevertFix"]').click());
  await page.waitForTimeout(150);
  ck("回退前有二次确认", true,
    await page.evaluate(() => document.getElementById("mConfirm").classList.contains("show")));
  const revertText = await page.evaluate(() => document.getElementById("cf_text").textContent);
  ck("回退确认写明恢复原值", true, revertText.includes("恢复"));
  await page.evaluate(() => document.getElementById("cf_ok").click());
  await page.waitForTimeout(800);

  const afterRevert = await page.evaluate(() => {
    const rows = [...document.querySelectorAll("#ho_checks > div")];
    const fdRow = rows.find(x => x.textContent.includes("文件描述符上限"));
    return {
      rowFail: fdRow ? fdRow.textContent.includes("✕") : false,
      btnBack: !!(fdRow && fdRow.querySelector('button[data-act="hostOptFix"]')),
      areaText: (document.getElementById("ho_fixes").textContent || "").trim(),
    };
  });
  ck("回退后体检项重新失败", true, afterRevert.rowFail);
  ck("回退后修复按钮回来", true, afterRevert.btnBack);
  ck("回退后清空已修复区", "", afterRevert.areaText);

  // ---- 9. 组件更新：xray 版本列表标出「与 mihomo 不兼容」的版本 ----
  // 这条的价值在于**装之前**就看见：xray ≥ 26.7.11 的 REALITY 默认只接受
  // 26.3.27 以上的客户端，mihomo 把客户端版本写死在 1.8.2，握手报
  // REALITY authentication failed 而节点参数全对。所以既要看选项里的标记，
  // 也要看选中后的说明，以及边界（26.7.10 不该误伤）。
  await page.evaluate(() => document.querySelector('.nav-item[data-v="settings"]').click());
  await page.waitForTimeout(700);
  // 组件表的「可更新」徽标也要带提醒：只写「↑ v26.x」等于没说清楚代价。
  // 先清掉 6 小时缓存，否则用的是上一次会话里存下的老结论。
  await page.evaluate(async () => {
    try { localStorage.removeItem("tp_comp_upd"); } catch(e) {}
    await refreshComponentUpdates();
  });
  await page.waitForTimeout(400);
  const badge = await page.evaluate(() => {
    const el = document.getElementById("upd_xray");
    const b = el && el.querySelector("[title]");
    return { text: el ? el.textContent : "", title: b ? b.getAttribute("title") : "" };
  });
  ck("可更新徽标带不兼容提醒 ⚠", true, badge.text.includes("⚠"));
  ck("徽标 title 点名 REALITY 限制", true, badge.title.includes("REALITY"));
  await page.evaluate(() => fetchVersions("xray"));
  await page.waitForTimeout(500);
  const vers = await page.evaluate(() => {
    const opts = [...document.querySelectorAll("#vs_select option")].map(o => o.textContent.trim());
    const box = document.getElementById("vs_compat");
    return {
      open: document.getElementById("mVersions").classList.contains("show"),
      opts,
      marked: opts.filter(t => t.includes("与 mihomo 不兼容")).length,
      boundary: (opts.find(t => t.startsWith("v26.7.10")) || "").includes("mihomo"),
      oldest: (opts.find(t => t.startsWith("v26.3.27")) || "").includes("mihomo"),
      shown: box.style.display !== "none",
      text: box.textContent || "",
    };
  });
  ck("版本列表弹窗打开", true, vers.open);
  ck("xray 列表共 4 条", 4, vers.opts.length);
  ck("不兼容版本恰好标了 2 条", 2, vers.marked);
  ck("边界 26.7.10 不带标记", false, vers.boundary);
  ck("26.3.27 不带标记", false, vers.oldest);
  ck("默认选中最新（不兼容）时展开说明", true, vers.shown);
  ck("说明点名 REALITY 与客户端版本", true,
    vers.text.includes("REALITY") && vers.text.includes("mihomo"));

  // 切到兼容版本：说明必须消失（不能留着上一个版本的结论）
  await page.evaluate(() => {
    const sel = document.getElementById("vs_select");
    sel.selectedIndex = [...sel.options].findIndex(o => o.textContent.includes("26.3.27"));
    pickVersion();
  });
  await page.waitForTimeout(150);
  ck("选兼容版本后说明隐藏", false,
    await page.evaluate(() => document.getElementById("vs_compat").style.display !== "none"));
  // 再切回 26.7.11：起始版本本身就要报
  await page.evaluate(() => {
    const sel = document.getElementById("vs_select");
    sel.selectedIndex = [...sel.options].findIndex(o => o.textContent.includes("26.7.11"));
    pickVersion();
  });
  await page.waitForTimeout(150);
  ck("起始版本 26.7.11 也带说明", true,
    await page.evaluate(() => document.getElementById("vs_compat").textContent.includes("26.7.11")));
  await page.evaluate(() => closeModal("mVersions"));

  // ---- 10. 新增入站时自动建一个用户（8 位随机名） ----
  // 不建的话：入站保存成功、应用成功、界面一片绿，但导不出链接、订阅是空的。
  await page.evaluate(() => document.querySelector('.nav-item[data-v="inbounds"]').click());
  await page.waitForTimeout(400);
  const beforeUsers = await page.evaluate(() => req("/api/clients").then(x => x.length));
  await page.evaluate(() => openInboundModal(null));
  await page.waitForTimeout(400);
  await page.evaluate(async () => {
    document.getElementById("ib_name").value = "e2e-auto";
    document.getElementById("ib_port").value = "26301";
    await saveInbound();
  });
  await page.waitForTimeout(700);
  const autoRes = await page.evaluate(async () => {
    const cs = await req("/api/clients");
    return {
      n: cs.length,
      names: cs.map(x => x.client.name),
      toast: [...document.querySelectorAll(".toast")].map(t => t.textContent).join(" | "),
      modalClosed: !document.getElementById("mInbound").classList.contains("show"),
    };
  });
  ck("保存成功后弹窗关闭", true, autoRes.modalClosed);
  ck("自动建了 1 个用户", beforeUsers + 1, autoRes.n);
  ck("提示里写出自动用户名", true, /已自动创建用户/.test(autoRes.toast));
  const autoName = autoRes.names.find(n => /^[abcdefghijkmnpqrstuvwxyz23456789]{8}$/.test(n));
  ck("用户名是 8 位、不含 l/1/o/0", true, !!autoName);

  console.log(`\n${pass} PASS / ${fail} FAIL`);
  await browser.close();
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
