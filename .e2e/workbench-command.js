/* 离线回归：B 方案「命令工作台」在 demo 里是否真的能操作。
   重点不是元素存在，而是「键盘 / 点击 → 状态变化 → 落库 → 刷新恢复」这条链；
   还有 14 组主题、窄屏无溢出、DOM 顺序与视觉顺序一致。 */
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
  await page.waitForTimeout(950);

  // ---- 1. 命令条与三段导航 ----
  const shell = await page.evaluate(() => ({
    cmdbar: !!document.getElementById("cmdbar"),
    acts: [...document.querySelectorAll(".cmd-quick .btn")].map(b => b.dataset.act),
    secs: [...document.querySelectorAll(".nav-sec")].map(e => e.textContent.trim()),
    labels: [...document.querySelectorAll(".nav-item span")].map(e => e.textContent),
    moreSecs: [...document.querySelectorAll(".more-sec")].map(e => e.textContent.trim())
  }));
  ck("命令条常驻", true, shell.cmdbar);
  ck("三个高频动作都在", "openInboundModal,goto,openApply", shell.acts.join(","));
  ck("导航三段分组", "查看,配置,维护", shell.secs.join(","));
  ck("导航标签不含斜杠", false, shell.labels.concat(shell.moreSecs).some(s => s.includes("/")));
  ck("更多抽屉同样按三段分组", "查看,配置,维护", shell.moreSecs.join(","));

  // ---- 2. Ctrl+K 搜索并执行 ----
  await page.keyboard.press("Control+k");
  await page.waitForTimeout(250);
  ck("Ctrl K 打开命令面板", true,
    await page.evaluate(() => document.getElementById("cmdOverlay").classList.contains("show")));
  await page.fill("#cmdInput", "备份管理");
  await page.waitForTimeout(200);
  const found = await page.evaluate(() => [...document.querySelectorAll("#cmdList .cmd-item .ct")].map(e => e.textContent));
  ck("搜索能命中目标页面", true, found.some(s => s.includes("备份管理")));
  await page.keyboard.press("Enter");
  await page.waitForTimeout(350);
  ck("回车执行后切到目标页", "backups",
    await page.evaluate(() => document.body.dataset.view));
  ck("执行后面板关闭", false,
    await page.evaluate(() => document.getElementById("cmdOverlay").classList.contains("show")));
  // 「审计」只存在于视图索引、不在动作表里：必须单独覆盖 c.view 分支。
  // 只测动作项时，把 goto(c.view) 删掉也不会红，是典型的假绿。
  await page.keyboard.press("Control+k");
  await page.waitForTimeout(180);
  await page.fill("#cmdInput", "审计");
  await page.waitForTimeout(180);
  await page.keyboard.press("Enter");
  await page.waitForTimeout(320);
  ck("命令面板的视图项也能跳转", "audit",
    await page.evaluate(() => document.body.dataset.view));

  await page.keyboard.press("Control+k");
  await page.waitForTimeout(180);
  await page.keyboard.press("Escape");
  await page.waitForTimeout(150);
  ck("Esc 关闭命令面板", false,
    await page.evaluate(() => document.getElementById("cmdOverlay").classList.contains("show")));

  // ---- 3. 回到工作台：卡片与工具 ----
  await page.evaluate(() => goto("overview"));
  await page.waitForTimeout(700);
  const wb = await page.evaluate(() => ({
    cards: [...document.querySelectorAll("#workbench .wb-card")].map(c => c.dataset.wb),
    tools: [...document.querySelectorAll('#workbench .wb-card[data-wb="todo"] .wb-tool')].map(b => b.dataset.act || "drag"),
    todos: document.querySelectorAll("#wbTodo .todo-row").length,
    spans: [...document.querySelectorAll("#workbench .wb-card")].filter(c => c.style.gridRowEnd).length
  }));
  ck("工作台七张卡片齐全", "todo,kpis,trend,cores,users,rank,quick", wb.cards.join(","));
  ck("卡片头有折叠置顶上移下移拖拽", "wbToggle,wbPin,wbUp,wbDown,drag", wb.tools.join(","));
  ck("今天要处理有内容", true, wb.todos >= 1);
  ck("桌面瀑布流已算出 grid-row span", 7, wb.spans);

  // ---- 4. DOM 顺序 = 视觉顺序（Tab 顺序的前提） ----
  const orderOK = await page.evaluate(() => {
    const cs = [...document.querySelectorAll("#workbench .wb-card")];
    const tops = cs.map(c => c.getBoundingClientRect().top);
    // 两列布局允许同行两张卡 top 相同，但绝不允许后出现的前一张更靠上
    for (let i = 1; i < tops.length; i++) if (tops[i] < tops[i - 1] - 2) return false;
    return true;
  });
  ck("视觉顺序没有倒挂", true, orderOK);

  // ---- 5. 折叠 / 置顶 / 排序，并确认写进设置 ----
  await page.evaluate(() => wbToggle("rank"));
  await page.waitForTimeout(150);
  ck("折叠生效", true,
    await page.evaluate(() => document.querySelector('[data-wb="rank"]').classList.contains("collapsed")));
  await page.evaluate(() => wbPin("todo"));
  await page.waitForTimeout(200);
  const pinned = await page.evaluate(() => ({
    first: document.querySelector("#workbench .wb-card").dataset.wb,
    cls: document.querySelector('[data-wb="todo"]').classList.contains("pinned")
  }));
  ck("置顶后待办排第一", "todo", pinned.first);
  ck("置顶标记存在", true, pinned.cls);
  const before = await page.evaluate(() => [...document.querySelectorAll("#workbench .wb-card")].map(c => c.dataset.wb).join(","));
  await page.evaluate(() => wbDown("trend"));
  await page.waitForTimeout(200);
  const after = await page.evaluate(() => [...document.querySelectorAll("#workbench .wb-card")].map(c => c.dataset.wb).join(","));
  ck("下移改变卡片顺序", true, before !== after);
  const saved = await page.evaluate(() => ({
    ls: localStorage.getItem("triune_wb_layout") || "",
    srv: (typeof MDB !== "undefined" ? MDB.settings.wb_layout : "") || ""
  }));
  ck("布局写入本机缓存", true, saved.ls.includes("pinned"));
  ck("布局同步写入服务端设置", true, saved.srv.includes("pinned"));

  // ---- 6. 刷新后恢复布局 ----
  await page.reload();
  await page.waitForTimeout(900);
  const restored = await page.evaluate(() => ({
    folded: document.querySelector('[data-wb="rank"]').classList.contains("collapsed"),
    pinned: document.querySelector('[data-wb="todo"]').classList.contains("pinned")
  }));
  ck("刷新后折叠状态保留", true, restored.folded);
  ck("刷新后置顶状态保留", true, restored.pinned);

  // ---- 7. 14 组主题：每组的四层令牌都齐全且真的跟着配色变 ----
  const themes = await page.evaluate(() => {
    const r = document.documentElement;
    const keys = ["--bg", "--card", "--text", "--muted", "--acc", "--acc-btn",
      "--ok-soft", "--err-soft", "--chart-down", "--shadow", "--elev-2", "--code-bg"];
    const schemes = ["azure", "indigo", "forest", "rose", "solar", "steel", "ocean"];
    const out = [];
    for (const mode of ["light", "dark"]) {
      for (const s of schemes) {
        r.dataset.mode = mode; r.dataset.scheme = s;
        const cs = getComputedStyle(r);
        out.push({mode, s, vals: keys.map(k => (cs.getPropertyValue(k) || "").trim())});
      }
    }
    return out;
  });
  ck("主题组合数", 14, themes.length);
  ck("每组四层令牌都非空", true, themes.every(t => t.vals.every(v => v.length > 0)));
  const okSofts = new Set(themes.filter(t => t.mode === "light").map(t => t.vals[6]));
  ck("浅色下成功底色随方案变化", true, okSofts.size >= 6);
  const charts = new Set(themes.filter(t => t.mode === "dark").map(t => t.vals[8]));
  ck("深色下图表色随方案变化", true, charts.size >= 5);

  // ---- 8. 密度与圆角两根独立轴 ----
  const axes = await page.evaluate(() => {
    setDensity("compact"); setRadius("soft");
    const r = document.documentElement;
    const a = {d: r.dataset.density, rad: r.dataset.radius,
      pad: getComputedStyle(r).getPropertyValue("--dens-pad").trim(),
      ls: localStorage.getItem("triune_density")};
    setDensity("comfortable"); setRadius("normal");
    const b = {d: r.dataset.density, rad: r.getAttribute("data-radius"),
      pad: getComputedStyle(r).getPropertyValue("--dens-pad").trim()};
    return {a, b};
  });
  ck("紧凑档生效", "compact", axes.a.d);
  ck("柔和圆角档生效", "soft", axes.a.rad);
  ck("密度偏好写入本机", "compact", axes.a.ls);
  ck("默认档撤掉 data-radius", null, axes.b.rad);
  ck("密度令牌真的变了", true, axes.a.pad !== axes.b.pad);

  // ---- 9. 手机端：五个入口 + 更多抽屉 + 单列无溢出 ----
  for (const [w, h] of [[390, 844], [360, 640], [320, 568]]) {
    const p = await browser.newPage({ viewport: {width: w, height: h}, isMobile: true, hasTouch: true });
    await p.goto(URL);
    await p.waitForTimeout(850);
    const m = await p.evaluate(() => {
      const nav = [...document.querySelectorAll("#navMobile .mnav-item")].map(b => b.textContent.trim());
      const cards = [...document.querySelectorAll("#workbench .wb-card")];
      window.scrollTo(0, document.documentElement.scrollHeight);
      const last = cards[cards.length - 1].getBoundingClientRect();
      return {
        nav, navShown: getComputedStyle(document.getElementById("navMobile")).display !== "none",
        deskNavHidden: getComputedStyle(document.getElementById("nav")).display === "none",
        spans: cards.filter(c => c.style.gridRowEnd).length,
        over: document.documentElement.scrollWidth - innerWidth,
        lastBottom: Math.round(last.bottom - innerHeight)
      };
    });
    ck(`${w}px 底部五个入口`, "概览,入站,应用,设置,更多", m.nav.join(","));
    ck(`${w}px 底部条可见且桌面导航隐藏`, true, m.navShown && m.deskNavHidden);
    ck(`${w}px 工作台退回单列`, 0, m.spans);
    ck(`${w}px 无横向溢出`, true, m.over <= 1);
    ck(`${w}px 能滚到最后一张卡片`, true, m.lastBottom <= 1);
    await p.evaluate(() => openMoreSheet());
    await p.waitForTimeout(180);
    const sheet = await p.evaluate(() => ({
      shown: document.getElementById("moreSheet").classList.contains("show"),
      items: document.querySelectorAll("#moreBody .more-item").length
    }));
    ck(`${w}px 更多抽屉列出全部页面`, true, sheet.shown && sheet.items >= 11);
    await p.close();
  }

  console.log(`\n结果：${pass} 通过 / ${fail} 失败`);
  await browser.close();
  process.exit(fail ? 1 : 0);
})();
