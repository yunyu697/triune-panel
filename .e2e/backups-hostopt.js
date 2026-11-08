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

  console.log(`\n${pass} PASS / ${fail} FAIL`);
  await browser.close();
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
