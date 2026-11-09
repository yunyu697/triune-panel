/* 首屏/视图恢复 + TOTP 二维码的浏览器实测（离线 demo）。
 *
 * 关键：把一段探针插在 app.js 内联脚本**之前**，直接读 app.js 接手前的
 * computedStyle。只有这样才能证明「首帧画的是上次的页面」——等页面 load
 * 完再看，goto() 早就把 .active 切好了，看不出中途有没有闪过概览。
 */
const { launch } = require("./browser");
const fs = require("fs");
const path = require("path");

const root = path.resolve(__dirname, "..");
const demo = fs.readFileSync(path.join(root, "demo.html"), "utf8");
// 不能用 lastIndexOf("<script>")：app.js 的注释里就有这个字面量，会把探针
// 插进 JS 源码中间（整个脚本语法错误，且 window.__firstFrame 永远不存在）。
// app.js 以 "use strict"; 开头，这是它在 demo.html 里的唯一起点。
const appTag = '<script>\n"use strict";';
const idx = demo.lastIndexOf(appTag);
if (idx < 0) throw new Error("demo.html 里找不到 app.js 的内联脚本");
const probe = `<script>
window.__firstFrame = (function () {
  const h = document.documentElement;
  const g = id => document.getElementById(id);
  const d = id => { const e = g(id); return e ? getComputedStyle(e).display : "missing"; };
  return {
    boot: h.getAttribute("data-boot"), view: h.getAttribute("data-boot-view"),
    login: d("login"), app: d("app"),
    overview: d("v-overview"), gen: d("v-gen"), settings: d("v-settings"),
    overviewActive: g("v-overview").classList.contains("active")
  };
})();
</script>`;
const probeFile = path.join(__dirname, "probe.html");
fs.writeFileSync(probeFile, demo.slice(0, idx) + probe + demo.slice(idx));
const URL = "file://" + probeFile + "#demo";

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

  // 设偏好并刷新，读 app.js 接手前的首帧
  const reloadWith = async (token, view) => {
    await page.evaluate(({ token, view }) => {
      if (token === null) localStorage.removeItem("triune_token");
      else localStorage.setItem("triune_token", token);
      if (view === null) localStorage.removeItem("triune_view");
      else localStorage.setItem("triune_view", view);
    }, { token, view });
    await page.reload();
    await page.waitForTimeout(400);
    return page.evaluate(() => ({
      first: window.__firstFrame,
      now: (() => {
        const active = [...document.querySelectorAll(".view.active")].map(e => e.id);
        return {
          active: active.join(","), title: document.getElementById("pageTitle").textContent,
          boot: document.documentElement.getAttribute("data-boot"),
          view: document.documentElement.getAttribute("data-boot-view")
        };
      })()
    }));
  };

  // ---- 1. 上次停在「配置生成」，刷新首帧就应该是它 ----
  let r = await reloadWith("demo-token", "gen");
  ck("首帧 boot=app", "app", r.first.boot);
  ck("首帧 data-boot-view=gen", "gen", r.first.view);
  ck("首帧登录页已隐藏", "none", r.first.login);
  ck("首帧主界面已显示", "flex", r.first.app);
  ck("首帧概览页隐藏", "none", r.first.overview);
  ck("首帧配置生成页显示", "block", r.first.gen);
  ck("最终停在配置生成页", "v-gen", r.now.active);
  ck("最终标题正确", "配置生成", r.now.title);
  ck("data-boot 已撤", "null", String(r.now.boot));
  ck("data-boot-view 已撤", "null", String(r.now.view));

  // ---- 2. 换一页，同样成立 ----
  r = await reloadWith("demo-token", "settings");
  ck("首帧设置页显示", "block", r.first.settings);
  ck("首帧概览页仍隐藏", "none", r.first.overview);
  ck("最终停在设置页", "v-settings", r.now.active);

  // ---- 3. 没有偏好 / 非法值 → 安静退回概览，不能白屏 ----
  r = await reloadWith("demo-token", null);
  ck("无偏好时不打标记", "null", String(r.first.view));
  ck("无偏好时首帧概览可见", "block", r.first.overview);
  ck("无偏好时最终在概览", "v-overview", r.now.active);
  r = await reloadWith("demo-token", "not-a-view");
  ck("非法值不打标记", "null", String(r.first.view));
  ck("非法值首帧概览可见", "block", r.first.overview);
  ck("非法值最终在概览", "v-overview", r.now.active);

  // ---- 4. 未登录：首屏不纠偏，登录页照常显示 ----
  r = await reloadWith(null, "gen");
  ck("未登录时不打视图标记", "null", String(r.first.view));
  ck("未登录时首帧登录页可见", "flex", r.first.login);
  ck("未登录时首帧主界面隐藏", "none", r.first.app);

  // ---- 4b. 登录页两栏布局（与最终效果设计图同源）----
  // demo 启动会直接进主界面，showLogin() 是产品自己的入口，切回来量真实布局。
  const lg = await page.evaluate(() => {
    window.showLogin();
    const rect = s => {
      const e = document.querySelector(s);
      if (!e) return { x: 0, y: 0, w: 0, h: 0 };
      const b = e.getBoundingClientRect();
      return { x: Math.round(b.x), y: Math.round(b.y), w: Math.round(b.width), h: Math.round(b.height) };
    };
    return {
      display: getComputedStyle(document.getElementById("login")).display,
      card: rect(".login-card"), aside: rect(".login-aside"), orbit: rect(".la-orbit"),
      cardBlur: getComputedStyle(document.querySelector(".login-card")).backdropFilter,
      asideBlur: getComputedStyle(document.querySelector(".login-aside")).backdropFilter,
      features: document.querySelectorAll(".la-feature").length,
      formWide: ["lu", "lp"].every(id => document.getElementById(id).getBoundingClientRect().width > 100),
      overX: document.documentElement.scrollWidth - innerWidth,
      overY: document.documentElement.scrollHeight - innerHeight
    };
  });
  ck("登录页仍是 flex 两栏", "flex", lg.display);
  ck("登录卡片在左半区", true, lg.card.x + lg.card.w / 2 < 720);
  ck("登录卡片垂直居中", true, Math.abs(lg.card.y + lg.card.h / 2 - 450) < 20);
  ck("右侧品牌面板比卡片宽且填满高度", true, lg.aside.w > lg.card.w && lg.aside.h > 800);
  ck("左右两块不重叠", true, lg.aside.x >= lg.card.x + lg.card.w);
  ck("右侧三条特性都渲染", 3, lg.features);
  ck("右侧几何图占据剩余高度", true, lg.orbit.h > 150);
  ck("登录卡片与品牌面板都是磨砂玻璃", true,
    lg.cardBlur.includes("blur") && lg.asideBlur.includes("blur"));
  ck("登录表单完整可见", true, lg.formWide);
  ck("登录页无横向/纵向溢出", true, lg.overX <= 1 && lg.overY <= 1);

  // 手机端：品牌页头在上、表单在下，几何图与磨砂都收起
  const mp = await browser.newPage({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  mp.on("pageerror", e => errors.push(e.message));
  await mp.goto(URL);
  await mp.waitForTimeout(600);
  const lm = await mp.evaluate(() => {
    window.showLogin();
    const card = document.querySelector(".login-card").getBoundingClientRect();
    const aside = document.querySelector(".login-aside").getBoundingClientRect();
    return {
      asideTop: Math.round(aside.top), cardTop: Math.round(card.top),
      orbit: getComputedStyle(document.querySelector(".la-orbit")).display,
      blur: getComputedStyle(document.querySelector(".login-card")).backdropFilter,
      overX: document.documentElement.scrollWidth - innerWidth
    };
  });
  ck("手机端品牌页头在登录卡片上方", true, lm.asideTop < lm.cardTop);
  ck("手机端收起几何图", "none", lm.orbit);
  ck("手机端不上磨砂", "none", lm.blur);
  ck("手机端登录页无横向溢出", true, lm.overX <= 1);
  await mp.close();

  // ---- 5. 真实点击切页，偏好要落盘（刷新保留的前提） ----
  await reloadWith("demo-token", "overview");
  await page.evaluate(() => {
    const b = document.querySelector('.nav-item[data-v="traffic"]');
    b.click();
  });
  await page.waitForTimeout(300);
  const saved = await page.evaluate(() => localStorage.getItem("triune_view"));
  ck("点导航后偏好已保存", "traffic", saved);
  await page.reload();
  await page.waitForTimeout(400);
  const finalView = await page.evaluate(() => ({
    active: [...document.querySelectorAll(".view.active")].map(e => e.id).join(","),
    first: window.__firstFrame
  }));
  ck("切到流量页再刷新仍停在流量页", "v-traffic", finalView.active);
  ck("刷新首帧不是概览", "none", finalView.first.overview);

  // ---- 6. TOTP 绑定二维码：本地 canvas 真的画出来了 ----
  await page.evaluate(() => {
    document.querySelector('#fa_totp_start[data-act="faTotpSetup"]').click();
  });
  await page.waitForTimeout(400);
  const qr = await page.evaluate(() => {
    const cv = document.getElementById("fa_totp_qr");
    const d = cv.getContext("2d").getImageData(0, 0, cv.width, cv.height).data;
    let dark = 0;
    for (let i = 0; i < d.length; i += 4) if (d[i] < 100) dark++;
    return {
      dark, msg: document.getElementById("fa_totp_qr_msg").textContent,
      uri: document.getElementById("fa_uri").value,
      boxShown: !document.getElementById("fa_totp_box").classList.contains("hide")
    };
  });
  ck("二维码画出了深色模块", true, qr.dark > 500);
  ck("二维码提示文案", true, qr.msg.includes("二维码已生成"));
  ck("绑定 URI 已填", true, qr.uri.startsWith("otpauth://totp/"));
  ck("绑定区已展开", "true", qr.boxShown);

  // ---- 7. 启用后清掉密钥与二维码，别把凭据留在屏幕上 ----
  await page.evaluate(() => {
    document.getElementById("fa_totp_code").value = "123456";
    document.querySelector('[data-act="faTotpEnable"]').click();
  });
  await page.waitForTimeout(400);
  const cleared = await page.evaluate(() => {
    const cv = document.getElementById("fa_totp_qr");
    const d = cv.getContext("2d").getImageData(0, 0, cv.width, cv.height).data;
    let dark = 0;
    for (let i = 0; i < d.length; i += 4) if (d[i] < 100) dark++;
    return { dark, uri: document.getElementById("fa_uri").value,
      boxHidden: document.getElementById("fa_totp_box").classList.contains("hide") };
  });
  ck("启用后二维码已清除", 0, cleared.dark);
  ck("启用后 URI 已清除", "", cleared.uri);
  ck("启用后绑定区收起", "true", cleared.boxHidden);

  ck("页面无 JS 异常", 0, errors.length);
  if (errors.length) console.log("  JS 错误:", errors.slice(0, 5));
  await browser.close();
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
