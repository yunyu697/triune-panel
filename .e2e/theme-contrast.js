/* 主题可读性回归：14 个组合 × 4 个视图，量**真实渲染**的正文对比度。
 *
 * 为什么需要它：theme_contract_test.go 只能校验令牌两两之间的对比度，看不到
 * 「文字究竟坐在哪块面上」。v1.8.1 之前有两类只有浏览器里才暴露的问题——
 *   · `.todo-row` 是 <button>，color 被 UA 样式表钉成 buttontext（黑），暗色主题下
 *     待办标题成了黑字压深底（1.3:1），令牌测试完全碰不到；
 *   · 实心品牌面写死 `color:#fff`，而暗色方案的 --on-acc 是深色，白字压浅色
 *     渐变只有 1.7:1。
 * 两者都不是「令牌不对」，而是「用错令牌 / 没用令牌」，所以必须在渲染层量。
 *
 * 量化方式：从元素自身往上收集 background-color（渐变取实色停靠点平均），
 * 逐层 alpha 合成出实际底色，再与 color（SVG 文字取 fill）算 WCAG 对比度。
 * 正文按 4.5:1、大字按 3:1。
 */
const { launch } = require("./browser");
const path = require("path");
const URL = "file://" + path.resolve(__dirname, "..", "demo.html") + "#demo";

const SCHEMES = ["azure", "indigo", "forest", "rose", "solar", "steel", "ocean"];
const VIEWS = ["overview", "settings", "clients", "hostopt"];

let pass = 0, fail = 0;
const ck = (name, want, got) => {
  const ok = String(want) === String(got);
  ok ? pass++ : fail++;
  console.log(`${ok ? "PASS" : "FAIL"} ${name}${ok ? "" : `  期望=[${want}] 实际=[${got}]`}`);
};

// 在页面里跑的审计：返回低于 AA 的文字（最多 5 条，按对比度升序）
const AUDIT = () => {
  const parse = (s) => {
    const m = s && s.match(/rgba?\(([^)]+)\)/);
    if (!m) return null;
    const p = m[1].split(",").map((x) => parseFloat(x));
    return { r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1 };
  };
  const over = (f, b) => ({
    r: f.r * f.a + b.r * (1 - f.a),
    g: f.g * f.a + b.g * (1 - f.a),
    b: f.b * f.a + b.b * (1 - f.a),
    a: 1,
  });
  const lin = (c) => ((c /= 255) <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4));
  const lum = (c) => 0.2126 * lin(c.r) + 0.7152 * lin(c.g) + 0.0722 * lin(c.b);
  const ratio = (a, b) => {
    const la = lum(a), lb = lum(b);
    return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
  };

  const cs = getComputedStyle(document.documentElement);
  const bgTok = parse(cs.getPropertyValue("--bg")) || { r: 255, g: 255, b: 255, a: 1 };
  const base = bgTok.a >= 1 ? bgTok : over(bgTok, { r: 255, g: 255, b: 255, a: 1 });

  const layerOf = (el) => {
    const st = getComputedStyle(el);
    const bc = parse(st.backgroundColor);
    if (bc && bc.a > 0) return bc;
    if (st.backgroundImage && st.backgroundImage !== "none") {
      const stops = [...st.backgroundImage.matchAll(/rgba?\([^)]+\)/g)].map((m) => parse(m[0]));
      const solid = stops.filter((c) => c && c.a >= 0.5);
      if (solid.length) {
        return {
          r: solid.reduce((a, c) => a + c.r, 0) / solid.length,
          g: solid.reduce((a, c) => a + c.g, 0) / solid.length,
          b: solid.reduce((a, c) => a + c.b, 0) / solid.length,
          a: 1,
        };
      }
    }
    return null;
  };

  const label = (el) => {
    let s = el.tagName.toLowerCase();
    if (el.id) s += "#" + el.id;
    else if (el.classList && el.classList.length) s += "." + el.classList[0];
    return s;
  };

  const bad = [];
  for (const el of document.querySelectorAll("body *")) {
    let own = "";
    for (const n of el.childNodes) if (n.nodeType === 3) own += n.textContent;
    own = own.trim();
    if (!own) continue;
    // 不可见元素不算：弹窗未打开、折叠卡片里的文字量不到真底色。
    if (typeof el.checkVisibility === "function" &&
        !el.checkVisibility({ checkVisibilityCSS: true, opacityProperty: true })) continue;
    const rect = el.getBoundingClientRect();
    if (rect.width < 2 || rect.height < 2) continue;

    const st = getComputedStyle(el);
    // 渐变文字（background-clip:text）的 color 是 transparent，跳过以免假警报。
    if (st.webkitBackgroundClip === "text" || st.backgroundClip === "text") continue;
    const isSVG = el.namespaceURI === "http://www.w3.org/2000/svg";
    const col = parse(isSVG ? st.fill : st.color);
    if (!col) continue;

    // 从自身开始往上合成底色：按钮/标签的背景画在自己身上。
    const layers = [];
    let cur = el;
    while (cur && cur !== document.documentElement) {
      const l = layerOf(cur);
      if (l) layers.push(l);
      cur = cur.parentElement;
    }
    let bgc = base;
    for (let i = layers.length - 1; i >= 0; i--) bgc = over(layers[i], bgc);

    const fg = col.a >= 1 ? col : over(col, bgc);
    const fs = parseFloat(st.fontSize);
    const bold = parseInt(st.fontWeight, 10) >= 700;
    const need = fs >= 24 || (fs >= 18.66 && bold) ? 3 : 4.5;
    const cr = ratio(fg, bgc);
    if (cr < need) {
      bad.push({
        text: own.slice(0, 18),
        sel: label(el),
        ratio: Number(cr.toFixed(2)),
        need,
        fs,
      });
    }
  }
  return bad.sort((a, b) => a.ratio - b.ratio).slice(0, 5);
};

const setTheme = async (page, mode, scheme) => {
  await page.evaluate(([m, s]) => {
    document.documentElement.dataset.mode = m;
    document.documentElement.dataset.scheme = s;
  }, [mode, scheme]);
  // applyTheme 用 data-theme-switch 压过渡，属性落定后再等一次样式重算。
  await page.waitForTimeout(260);
};

(async () => {
  const browser = await launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await page.goto(URL);
  await page.waitForTimeout(1000);
  // 关掉所有过渡/动画：否则切主题或切视图时 getComputedStyle 会量到过渡中的
  // 中间色（慢 runner 上尤其明显），造成「稳定态其实达标、却被判低对比度」的假红。
  // 我们只关心最终落定的配色，不关心切换动画，所以量之前先把动画取消。
  await page.addStyleTag({
    content: "*,*::before,*::after{transition:none!important;animation:none!important}",
  });

  // ---- 1. 待办行：<button> 的 color 不能落回 UA 的 buttontext ----
  await setTheme(page, "dark", "azure");
  const todo = await page.evaluate(() => {
    const row = document.querySelector(".todo-row");
    const b = row && row.querySelector("b");
    const cs = getComputedStyle(document.documentElement);
    return {
      hasRow: !!row,
      color: row ? getComputedStyle(row).color : "",
      bColor: b ? getComputedStyle(b).color : "",
      text: cs.getPropertyValue("--text").trim(),
    };
  });
  ck("待办行存在", true, todo.hasRow);
  ck("待办行文字色跟随 --text（不落回黑色 buttontext）", true,
    todo.color !== "rgb(0, 0, 0)" && todo.color !== "");
  ck("待办标题也跟着 --text", todo.bColor, todo.color);

  // ---- 2. 实心品牌面上的文字必须用 --on-acc ----
  await setTheme(page, "dark", "forest");
  const fills = await page.evaluate(() => {
    const cs = getComputedStyle(document.documentElement);
    const btn = document.querySelector(".btn.primary");
    const idx = document.querySelector(".rank-row .idx");
    const norm = (c) => {
      const n = document.createElement("div");
      n.style.color = c;
      document.body.appendChild(n);
      const v = getComputedStyle(n).color;
      n.remove();
      return v;
    };
    return {
      onAcc: norm(cs.getPropertyValue("--on-acc").trim()),
      btn: btn ? getComputedStyle(btn).color : "",
      idx: idx ? getComputedStyle(idx).color : "",
    };
  });
  ck("主按钮文字用 --on-acc（暗色下不再是白字压浅底）", fills.onAcc, fills.btn);
  ck("榜首序号用 --on-acc", fills.onAcc, fills.idx);

  // ---- 3. 全组合 × 全视图：没有低于 WCAG AA 的文字 ----
  for (const mode of ["light", "dark"]) {
    for (const scheme of SCHEMES) {
      await setTheme(page, mode, scheme);
      for (const v of VIEWS) {
        await page.evaluate((view) => {
          const item = document.querySelector(`.nav-item[data-v="${view}"]`);
          if (item) item.click();
        }, v);
        await page.waitForTimeout(200);
        const rows = await page.evaluate(AUDIT);
        ck(`${mode}/${scheme} ${v} 无低对比度文字`, 0, rows.length);
        for (const r of rows) {
          console.log(
            `     ↳ ${r.ratio} (<${r.need}) ${r.fs}px «${r.text}» ${r.sel}`
          );
        }
      }
    }
  }

  console.log(`\n结果：${pass} 通过 / ${fail} 失败`);
  await browser.close();
  process.exit(fail ? 1 : 0);
})();
