/* 手机端滚动模型 + 底部导航 + 常驻「应用」按钮的回归。
   背景：窄屏原来是「.shell 固定 100dvh + .main 内部滚动」，Android 地址栏伸缩会让
   .shell 反复改高度，滚动又在内层容器，合成器中途丢 tile——整页从某一行开始变白、
   底部滑不到。现在窄屏改成文档滚动，导航条固定在底部。
     node android-scroll.js
   任何一条挂掉都意味着手机端退回了「内部滚动 / 顶部导航」。 */
const { launch } = require("./browser");
const path = require("path");

const URL = "file://" + path.resolve(__dirname, "..", "demo.html") + "#demo";
const VIEWS = ["overview", "inbounds", "outbounds", "clients", "gen", "apply", "configs", "settings"];

let bad = 0;
function ok(cond, msg, extra) {
  if (cond) console.log("  ✓ " + msg);
  else { bad++; console.log("  ✗ " + msg + (extra === undefined ? "" : "  → " + JSON.stringify(extra))); }
}

(async () => {
  const browser = await launch();

  for (const vp of [{ w: 390, h: 844 }, { w: 360, h: 640 }, { w: 320, h: 568 }]) {
    console.log(`\n==== ${vp.w}×${vp.h} ====`);
    const page = await browser.newPage({ viewport: { width: vp.w, height: vp.h }, isMobile: true, hasTouch: true });
    await page.goto(URL);
    await page.waitForTimeout(900);

    const m = await page.evaluate(() => {
      const shell = document.querySelector(".shell");
      const main = document.querySelector(".main");
      const side = document.querySelector(".side");
      const tb = document.querySelector(".cmdbar");
      const sb = side.getBoundingClientRect();
      return {
        shellH: Math.round(shell.getBoundingClientRect().height),
        shellScroll: shell.scrollHeight,
        shellOverflow: getComputedStyle(shell).overflowY,
        mainScroll: main.scrollHeight,
        mainClient: main.clientHeight,
        mainOverflow: getComputedStyle(main).overflowY,
        sidePos: getComputedStyle(side).position,
        navH: Math.round(sb.height),
        navGapBottom: Math.round(innerHeight - sb.bottom),
        navHVar: getComputedStyle(document.documentElement).getPropertyValue("--nav-h").trim(),
        tbPos: getComputedStyle(tb).position,
        docScroll: document.documentElement.scrollHeight,
        innerH: window.innerHeight,
        bodyOverflowY: getComputedStyle(document.body).overflowY,
        contentPadBottom: parseFloat(getComputedStyle(document.querySelector(".content")).paddingBottom),
      };
    });

    // 1. 外层不再是固定高度：内容多高外壳就多高，没有裁剪
    ok(m.shellOverflow === "visible", ".shell 不裁剪溢出", m.shellOverflow);
    ok(m.shellScroll <= m.shellH + 1, ".shell 高度容纳全部内容", { shellH: m.shellH, shellScroll: m.shellScroll });
    // 2. 内层不再是滚动容器
    ok(m.mainOverflow === "visible", ".main 不是滚动容器", m.mainOverflow);
    ok(m.mainScroll <= m.mainClient + 1, ".main 自身不溢出", { mainScroll: m.mainScroll, mainClient: m.mainClient });
    // 3. 页面由文档滚动
    ok(m.docScroll > m.innerH, "文档可滚动（内容比视口长）", { docScroll: m.docScroll, innerH: m.innerH });
    ok(m.bodyOverflowY !== "hidden", "body 没被锁住", m.bodyOverflowY);
    // 4. 导航条固定在底部，顶栏不 sticky
    ok(m.sidePos === "fixed", "导航条 fixed", m.sidePos);
    ok(m.navGapBottom === 0, "导航条贴住屏幕底边", m.navGapBottom);
    ok(m.navHVar === m.navH + "px", "--nav-h 与导航条实际高度一致", { var: m.navHVar, real: m.navH });
    ok(m.tbPos === "static", "顶栏不 sticky（滚动时不和内容抢空间）", m.tbPos);
    // 5. 内容底部留白足够：不能被固定导航条压住
    ok(m.contentPadBottom >= m.navH + 20, "内容底部留白盖住导航条", m.contentPadBottom);

    // 6. 滚到底：末张卡片必须完整露在导航条上方，不能被压住
    const bottom = await page.evaluate(() => {
      window.scrollTo(0, document.documentElement.scrollHeight);
      const last = document.querySelector(".view.active").lastElementChild;
      const r = last.getBoundingClientRect();
      const side = document.querySelector(".side").getBoundingClientRect();
      return { lastBottom: Math.round(r.bottom), lastH: Math.round(r.height),
               navTop: Math.round(side.top), vh: window.innerHeight,
               docBottom: Math.round(document.documentElement.getBoundingClientRect().bottom) };
    });
    ok(bottom.lastH > 0, "末张卡片有实际高度", bottom.lastH);
    ok(bottom.lastBottom <= bottom.navTop + 2, "滚到底后末张卡片完整露在导航条上方", bottom);
    ok(Math.abs(bottom.docBottom - bottom.vh) <= 2, "底部没有多余空白", bottom);

    // 7. 滚动后导航条仍在原位（fixed 生效）
    await page.evaluate(() => window.scrollTo(0, 600));
    await page.waitForTimeout(150);
    const navTop2 = await page.evaluate(() => Math.round(document.querySelector(".side").getBoundingClientRect().top));
    ok(Math.abs(navTop2 - (vp.h - m.navH)) <= 1, "滚动 600px 后导航条仍在底部", { navTop2, want: vp.h - m.navH });

    // 8. 每个页面：内容都渲染出来了、无横向溢出、切页回顶
    for (const v of VIEWS) {
      await page.evaluate(v => document.querySelector(`.nav-item[data-v="${v}"]`)?.click(), v);
      await page.waitForTimeout(320);
      const r = await page.evaluate(() => {
        const view = document.querySelector(".view.active");
        const kids = [...view.children];
        // 设置页一次只显示一组，[hidden] / display:none 的子块高度本来就是 0
        const shown = kids.filter(k => !k.hasAttribute("hidden") && getComputedStyle(k).display !== "none");
        return {
          id: view.id,
          kids: shown.length,
          zero: shown.filter(k => k.getBoundingClientRect().height < 2).length,
          overflowX: document.documentElement.scrollWidth - document.documentElement.clientWidth,
          scrollY: Math.round(window.scrollY),
        };
      });
      ok(r.kids > 0 && r.zero === 0, `${v}：${r.kids} 个子块都有高度`, r);
      ok(r.overflowX <= 0, `${v}：无横向溢出`, r.overflowX);
      ok(r.scrollY === 0, `${v}：切页回到顶部`, r.scrollY);
    }

    // 9. 图表不许比卡片宽：画布一旦被钉成固定像素宽，右侧就被裁掉（手机上的「显示不全」）
    await page.evaluate(() => document.querySelector('.nav-item[data-v="overview"]')?.click());
    await page.waitForTimeout(500);
    const charts = await page.evaluate(() => {
      const out = [];
      document.querySelectorAll("#v-overview canvas").forEach(cv => {
        const cr = cv.getBoundingClientRect();
        // 卡片的类名换过代（现在图表在 .chart-wrap / .wb-body 里），
        // 所以按「谁给的宽度」取容器，而不是列举一堆类名：拿不到卡片时
        // 退到直接父元素，至少不会因为选择器过期而量出 0 宽。
        const card = cv.closest(".card, .kpi, .ring-card, .chart-wrap") || cv.parentElement;
        out.push({
          id: cv.id,
          w: Math.round(cr.width),
          attr: cv.width,
          inlineW: cv.style.width || "",
          cardW: card ? Math.round(card.getBoundingClientRect().width) : 0,
        });
      });
      return out;
    });
    const tooWide = charts.filter(c => c.inlineW !== "" || c.w > c.cardW + 1 || c.attr === 0);
    ok(tooWide.length === 0, `图表宽度都跟着容器走（${charts.length} 个画布）`, tooWide);

    // 10. 设置页分组条钉在视口最上方（顶栏不 sticky，导航条在底部）
    await page.evaluate(() => document.querySelector('.nav-item[data-v="settings"]')?.click());
    await page.waitForTimeout(400);
    await page.evaluate(() => window.scrollTo(0, 700));
    await page.waitForTimeout(200);
    const tabs = await page.evaluate(() => {
      const t = document.querySelector(".set-tabs:not([hidden])");
      return t ? { top: Math.round(t.getBoundingClientRect().top) } : null;
    });
    if (tabs) ok(tabs.top === 0, "设置分组条钉在视口顶部", tabs);

    // 11. 「应用配置」在窄屏仍然可达。
    //     方案 B 之后侧栏底部那个常驻按钮在窄屏被隐藏，入口移到命令条
    //     （.cmd-quick 里那个图标按钮）。所以要验的是「够得着、不切页」，
    //     不是「某个固定元素在某个固定位置」——后者一改版就失效，而它失效
    //     时没有任何人会知道。
    const applyEntry = await page.evaluate(() => {
      const btns = [...document.querySelectorAll('[data-act="openApply"]')]
        .filter(b => b.offsetParent !== null);
      if (!btns.length) return { count: 0 };
      const b = btns[0], r = b.getBoundingClientRect();
      return { count: btns.length, w: Math.round(r.width), h: Math.round(r.height),
               inViewport: r.left >= 0 && r.right <= innerWidth + 1 };
    });
    ok(applyEntry.count > 0, "窄屏有可见的「应用配置」入口", applyEntry);
    ok(applyEntry.inViewport !== false, "入口在视口内（不需要横向找）", applyEntry);
    ok(applyEntry.h >= 28, "入口触控高度 ≥28px", applyEntry.h);

    // 点它开弹窗、当前页不变——应用是动作，不该把页面换走
    const beforeView = await page.evaluate(() => document.body.dataset.view || "");
    await page.evaluate(() => [...document.querySelectorAll('[data-act="openApply"]')]
      .filter(b => b.offsetParent !== null)[0].click());
    await page.waitForTimeout(350);
    const ap = await page.evaluate(() => ({
      shown: document.getElementById("mApply").classList.contains("show"),
      view: document.body.dataset.view || "",
      core: (document.getElementById("ap_core") || {}).value,
      hasRev: !!document.getElementById("rev_core"),
      navHasApply: !!document.querySelector('.nav-item[data-v="apply"]'),
    }));
    ok(ap.shown, "点「应用」入口打开应用弹窗", ap.shown);
    ok(ap.view === beforeView, "打开应用弹窗不切页（仍停在原页面）", `${beforeView} → ${ap.view}`);
    ok(!!ap.core, "弹窗里有内核选择", ap.core);
    ok(ap.hasRev, "修订历史有自己的内核选择（不与弹窗抢 ap_core）", ap.hasRev);
    ok(!ap.navHasApply, "导航里不再有「应用 / 修订」入口", ap.navHasApply);
    await page.evaluate(() => closeModal("mApply"));
    await page.waitForTimeout(200);

    // 12. 弹窗打开时锁住页面滚动，关掉后恢复。
    //     必须点**可见**的入口：命令条里那个在当前页可能是 display:none，
    //     对隐藏元素 click() 不报错但也不开弹窗，于是这条会测成「锁没生效」。
    await page.evaluate(() => document.querySelector('.nav-item[data-v="overview"]')?.click());
    await page.waitForTimeout(300);
    const opened = await page.evaluate(() => {
      const btns = [...document.querySelectorAll('[data-act="openInboundModal"]')]
        .filter(b => b.offsetParent !== null);
      if (!btns.length) return false;
      btns[0].click();
      return true;
    });
    // 等状态而不是等固定毫秒：弹窗要先把演示数据读出来再 show，
    // 400ms 在慢机器上不够——检查时还没开、关的时候才刚开，锁就被当成没生效。
    let shown = true;
    try {
      await page.waitForFunction(
        () => document.getElementById("mInbound").classList.contains("show"), null, { timeout: 3000 });
    } catch { shown = false; }
    ok(opened, "概览页有可见的「新增入站」入口", opened);
    ok(shown, "弹窗真的打开了", shown);
    const locked = await page.evaluate(() => getComputedStyle(document.body).overflowY);
    ok(locked === "hidden", "弹窗打开时锁住页面滚动", locked);
    await page.evaluate(() => closeModal("mInbound"));
    await page.waitForFunction(
      () => !document.getElementById("mInbound").classList.contains("show"), null, { timeout: 3000 })
      .catch(() => {});
    const unlocked = await page.evaluate(() => getComputedStyle(document.body).overflowY);
    ok(unlocked !== "hidden", "关掉弹窗后解除锁定", unlocked);

    await page.close();
  }

  await browser.close();
  console.log(bad === 0 ? "\n全部通过" : `\n${bad} 项失败`);
  process.exit(bad === 0 ? 0 : 1);
})();
