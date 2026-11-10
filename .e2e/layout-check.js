/* 布局回归：矮窗口下侧栏能滚到底 + 手机竖屏无横向溢出、能滚到页面底部 */
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

  // ---- 1. 矮窗口：侧栏导航必须能滚，且滚到底后最后一个入口可见 ----
  for (const h of [900, 420, 320]) {
    const page = await browser.newPage({ viewport: { width: 1440, height: h } });
    await page.goto(URL);
    await page.waitForTimeout(800);
    const r = await page.evaluate(() => {
      const nav = document.getElementById("nav");
      const items = [...nav.querySelectorAll(".nav-item")];
      const last = items[items.length - 1];
      const before = last.getBoundingClientRect().bottom;
      const scrollable = nav.scrollHeight > nav.clientHeight + 1;
      // 常驻「应用」按钮：导航滚到底之后必须还在原位（它在 #nav 外面，只有 #nav 自己滚）
      const btn = document.getElementById("sideApply");
      const b0 = btn.getBoundingClientRect();
      nav.scrollTop = nav.scrollHeight;           // 滚到底
      const after = last.getBoundingClientRect().bottom;
      const b1 = btn.getBoundingClientRect();
      const foot = document.querySelector(".side-foot").getBoundingClientRect();
      return {
        scrollable,
        beforeOver: Math.round(before - innerHeight),
        afterVisible: after <= innerHeight + 1,
        footVisible: foot.bottom <= innerHeight + 1,
        navTop: Math.round(nav.getBoundingClientRect().top),
        applyShown: getComputedStyle(btn).display !== "none",
        applyPinned: Math.round(b0.top) === Math.round(b1.top),
        applyInView: b1.top >= 0 && b1.bottom <= innerHeight + 1,
        applyWide: Math.round(b1.width) >= 150,
        applyAboveFoot: b1.bottom <= foot.top + 1,
      };
    });
    console.log(`\n-- 1440x${h} --`);
    if (h === 900) {
      ck("正常高度不需要滚动", false, r.scrollable);
      ck("最后一个入口本就在视口内", true, r.beforeOver <= 1);
      // 点常驻按钮开弹窗，不切页
      const mv = await page.evaluate(() => {
        const before = document.body.dataset.view || "";
        document.getElementById("sideApply").click();
        const shown = document.getElementById("mApply").classList.contains("show");
        const after = document.body.dataset.view || "";
        closeModal("mApply");
        return { before, after, shown };
      });
      ck("点「应用配置」开弹窗而不是切页", true, mv.shown && mv.before === mv.after);
    } else {
      ck("窗口不够高时导航可滚动", true, r.scrollable);
      ck("未滚动时最后一个入口在视口外（确实需要滚）", true, r.beforeOver > 1);
      ck("滚到底后最后一个入口可见", true, r.afterVisible);
    }
    ck("底部账号/主题/退出区完整可见", true, r.footVisible);
    ck("「应用」按钮在 PC 上也常驻显示", true, r.applyShown);
    ck("「应用」按钮是侧栏里的一条整宽按钮", true, r.applyWide);
    ck("导航滚动时「应用」按钮不动（只有 #nav 自己滚）", true, r.applyPinned);
    ck("「应用」按钮在视口内、位于账号区上方", true, r.applyInView && r.applyAboveFoot);
    await page.close();
  }

  // ---- 1.5 设置页：命令条 / 分组条 / 内容卡片必须等宽对齐 ----
  // 分组条曾经是「左右 padding 为 0 的实色带」，玻璃层一开就成了一条通栏白带，
  // 里面再嵌一个灰底胶囊，看着像另一个系统的顶栏。这里量真实坐标钉死。
  {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await page.goto(URL);
    await page.waitForTimeout(800);
    await page.evaluate(() => window.goto("settings"));
    await page.waitForTimeout(500);
    const r = await page.evaluate(() => {
      const box = s => {
        const e = document.querySelector(s), b = e.getBoundingClientRect(), c = getComputedStyle(e);
        return {
          left: Math.round(b.left), right: Math.round(b.right), w: Math.round(b.width),
          radius: parseFloat(c.borderTopLeftRadius), padL: parseFloat(c.paddingLeft),
        };
      };
      return {
        bar: box(".cmdbar"), tabs: box(".set-tabs"),
        card: box("#v-settings .card:not([hidden])"), seg: box(".seg-lg"),
      };
    });
    console.log("\n-- 设置页 1440x900 --");
    ck("分组条与命令条等宽对齐", true,
      r.tabs.left === r.bar.left && r.tabs.right === r.bar.right);
    ck(`分组条与内容卡片等宽对齐（条 ${r.tabs.w}px / 卡 ${r.card.w}px）`, true,
      r.tabs.left === r.card.left && r.tabs.right === r.card.right);
    ck("分组条是圆角卡片且左右留内边距", true, r.tabs.radius > 0 && r.tabs.padL > 0);
    ck("标签组不再套灰底胶囊壳", true, r.seg.padL === 0 && r.seg.w < r.tabs.w);
    await page.close();
  }

  // ---- 2. 手机竖屏：无横向溢出，且能滚到内容底部 ----
  for (const [w, h] of [[390, 844], [360, 640], [320, 568]]) {
    const page = await browser.newPage({ viewport: { width: w, height: h }, isMobile: true, hasTouch: true });
    await page.goto(URL);
    await page.waitForTimeout(800);
    const views = await page.evaluate(() => [...document.querySelectorAll(".nav-item")].map(b => b.dataset.v));
    let worst = 0, worstView = ""; const bottomBad = [];
    for (const v of views) {
      await page.evaluate(v => document.querySelector(`.nav-item[data-v="${v}"]`)?.click(), v);
      await page.waitForTimeout(260);
      const o = await page.evaluate(() => {
        const main = document.querySelector(".main");
        const over = Math.max(document.documentElement.scrollWidth - innerWidth, main.scrollWidth - main.clientWidth);
        // 滚到底：最后一张卡片底边必须在视口内（否则就是「显示不全」）。
        // 窄屏是文档滚动、桌面是 .main 内部滚动，两个都滚——只滚其中一个，
        // 另一种模型下等于没滚，测出来的「看不全」是假红。
        window.scrollTo(0, document.documentElement.scrollHeight);
        main.scrollTop = main.scrollHeight;
        const cards = [...document.querySelector(".view.active").querySelectorAll(".card")];
        const last = cards[cards.length - 1];
        const bottomOver = last ? Math.round(last.getBoundingClientRect().bottom - innerHeight) : 0;
        return { over, bottomOver, n: cards.length };
      });
      if (o.over > worst) { worst = o.over; worstView = v; }
      if (o.bottomOver > 1) { bottomBad.push(`${v}:${o.bottomOver}px`); }
    }
    console.log(`\n-- ${w}x${h} --`);
    ck(`全部页面无横向溢出（最差 ${worst}px @${worstView || "-"}）`, true, worst <= 1);

    ck(`每页滚到底都能看全最后一张卡片${bottomBad.length ? "（" + bottomBad.join(",") + "）" : ""}`, true, bottomBad.length === 0);

    // 顶栏内容不许被裁：每个子元素右边界都在视口内
    const topOver = await page.evaluate(() =>
      Math.max(...[...document.querySelectorAll(".topbar > *")].map(e => e.getBoundingClientRect().right - innerWidth)));
    ck("顶栏无元素超出右边界", true, Math.round(topOver) <= 1);
    await page.close();
  }

  // ---- 2.5 组件更新表：长版本串不许把「操作」列顶出卡片 ----
  // mihomo `-v` 的第一行有 60 多个字符，而 td 默认 nowrap：版本号会把整张表撑宽，
  // 「操作」列被推到卡片右侧外面，看起来就像按钮被版本号遮住了。这里注入真实长度的
  // 版本串再量按钮右缘——只断言「元素存在」抓不到这类溢出。
  const LONG_VER = {
    singbox: "sing-box version 1.12.0-alpha.5 (go1.24.2 linux/amd64 CGO disabled)",
    mihomo: "Mihomo Meta v1.19.1 linux amd64 with go1.24.1 Sat Jun 21 2025",
  };
  for (const w of [1440, 1280, 1080, 390, 320]) {
    const page = await browser.newPage({ viewport: { width: w, height: 900 }, isMobile: w < 500, hasTouch: w < 500 });
    await page.goto(URL);
    await page.waitForTimeout(700);
    await page.evaluate(() => {
      document.querySelector('.nav-item[data-v="settings"]').click();
      document.querySelector('#set_sect_seg button[data-sect="maintain"]').click();
    });
    await page.waitForTimeout(400);
    const m = await page.evaluate((LONG) => {
      const rows = document.querySelectorAll("#updtbody tr");
      // 版本列是第 3 格（组件 / 路径 / 当前版本 / 可更新 / 状态 / 操作）
      if (rows[1]) rows[1].children[2].textContent = LONG.singbox;
      if (rows[2]) rows[2].children[2].textContent = LONG.mihomo;
      const card = document.getElementById("set_card_update");
      const wrap = card.querySelector(".tbl-wrap");
      const btns = [...card.querySelectorAll("#updtbody .btn")];
      const over = Math.max(...btns.map(b => b.getBoundingClientRect().right)) - card.getBoundingClientRect().right;
      return { over: Math.round(over), hscroll: wrap.scrollWidth - wrap.clientWidth, n: btns.length };
    }, LONG_VER);
    ck(`${w}px：长版本串下操作按钮仍在卡片内（${m.n} 个）`, true, m.over <= 1);
    ck(`${w}px：组件更新表不需要横向滑动`, true, m.hscroll <= 1);
    await page.close();
  }

  // ---- 2.5.1 组件更新表的列宽与对齐 ----
  // 三代坑都在这张表上：长版本串把「操作」顶出卡片 → 修好后「组件」列反过来
  // 吃掉全部剩余空间（573px，里面只有「singbox」四个字）→ 「操作」列的下拉比
  // 按钮高 2px，按钮不在一条基线上。这几条断言量的都是「看不见的布局」：
  // 只断言「元素存在」抓不到比例失调。
  {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await page.goto(URL);
    await page.waitForTimeout(700);
    await page.evaluate(() => {
      document.querySelector('.nav-item[data-v="settings"]').click();
      document.querySelector('#set_sect_seg button[data-sect="maintain"]').click();
    });
    await page.waitForTimeout(1400);
    const r = await page.evaluate(() => {
      const tbl = document.querySelector(".upd-tbl");
      const ths = [...tbl.querySelectorAll("thead th")];
      const w = ths.map(t => Math.round(t.getBoundingClientRect().width));
      // 「可更新」列的徽标起点要每行一致
      const badges = [...tbl.querySelectorAll("tbody tr")].map(tr => {
        const cell = tr.children[3];
        if (!cell) return null;
        const b = cell.querySelector(".badge");
        return b ? Math.round(b.getBoundingClientRect().left) : null;
      }).filter(v => v !== null);
      // 操作列：同一行里的按钮必须同一基线、同一高度
      const rows = [...tbl.querySelectorAll("tbody tr")].map(tr => {
        const acts = [...tr.querySelectorAll(".upd-acts > *")];
        if (acts.length < 2) return null;
        const ys = acts.map(a => Math.round(a.getBoundingClientRect().y));
        const hs = acts.map(a => Math.round(a.getBoundingClientRect().height));
        return { sameY: new Set(ys).size === 1, sameH: new Set(hs).size === 1, n: acts.length };
      }).filter(Boolean);
      return {
        cols: w,
        badgeLeft: badges,
        badgeAligned: new Set(badges).size === 1,
        rows,
        allSameY: rows.every(x => x.sameY),
        allSameH: rows.every(x => x.sameH),
      };
    });
    ck("组件更新表是六列（组件/路径/当前版本/可更新/状态/操作）", 6, r.cols.length);
    ck("组件列收窄到 220px 以内（原来会吃掉全部剩余空间）", true, r.cols[0] <= 220);
    ck("「可更新」列有独立宽度", true, r.cols[3] >= 90);
    ck("更新徽标每行起点一致", true, r.badgeAligned);
    ck("操作列按钮同一基线", true, r.allSameY);
    ck("操作列按钮同高（下拉不再比按钮高 2px）", true, r.allSameH);
    await page.close();
  }

  // ---- 2.6 表头形状：上面圆、下面直角 ----
  // 表头曾经四角全圆而下方的数据行是直角，接缝处多出两块缺口，整张表像两种形状
  // 拼起来的。CSS 里写成 shorthand 很难看出圆的是哪两个角，这里直接量 computed。
  {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await page.goto(URL);
    await page.waitForTimeout(700);
    await page.evaluate(() => {
      document.querySelector('.nav-item[data-v="settings"]').click();
      document.querySelector('#set_sect_seg button[data-sect="maintain"]').click();
    });
    await page.waitForTimeout(600);
    const r = await page.evaluate(() => {
      const th = document.querySelector(".upd-tbl thead th");
      if (!th) return null;
      const cs = getComputedStyle(th);
      return { top: cs.borderTopLeftRadius, bottom: cs.borderBottomLeftRadius };
    });
    ck("表头上角是圆的", true, !!r && r.top !== "0px");
    ck("表头下角是直角（与数据行齐平）", "0px", r ? r.bottom : "null");
    await page.close();
  }

  // ---- 3. 截图留档 ----
  for (const [w, h, name] of [[1440, 400, "short"], [390, 844, "390x844"], [320, 568, "320x568"]]) {
    const page = await browser.newPage({ viewport: { width: w, height: h }, isMobile: w < 500, hasTouch: w < 500 });
    await page.goto(URL);
    await page.waitForTimeout(800);
    await page.screenshot({ path: `../screenshots/layout-fixed-${name}.png` });
    await page.close();
  }

  console.log(`\n结果：${pass} 通过 / ${fail} 失败`);
  await browser.close();
  process.exit(fail ? 1 : 0);
})();
