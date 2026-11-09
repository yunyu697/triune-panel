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
