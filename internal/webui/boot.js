/* 首屏引导：在 <head> 里同步决定「第一帧画登录页还是主界面」、
 * 「主界面里画哪一页」，以及「用哪套主题」。
 *
 * 原来 index.html 里登录页默认可见、#app 默认 .hide，而 app.js 要等
 * /api/status 一个网络往返回来才 showApp()——刷新时这一来一回就是用户
 * 看到的「先闪一下登录页，然后跳回主页」。第二个问题是 HTML 里
 * #v-overview 写死带 .active，刷新一律回概览。第三个是主题：
 * data-mode/data-scheme 写死 light/azure，等 body 末尾的 app.js 跑
 * initTheme() 才改成用户偏好，而 body 带 background/color transition，
 * 于是深色用户每次刷新都看着整页从浅色渐变到深色。
 *
 * 这里只做一件事：按本地存下来的会话、页面偏好与主题偏好，在**首次绘制
 * 之前**给 <html> 打上属性，index.html 的 CSS 据此决定显隐与配色。之后
 * app.js 调 showApp() / showLogin() 时会清掉 data-boot*，显隐重新由
 * .hide / .active 全权决定；主题属性保留（applyTheme() 会写成同一个值，
 * 不会触发过渡）。
 *
 * 这不是权限判断：token 可能已过期，真正的校验仍在服务端（/api/status 与
 * 每个 API 的 401）。伪造 localStorage 只能让本机先看到一层没有数据的空壳。
 * 只读账号的页面纠正由 app.js 的 goto() 在同一个 JS 任务里完成，浏览器
 * 不会绘制中间态，所以这里不需要复制一份角色白名单。
 *
 * 必须外置：CSP 是 script-src 'self'，内联 <script> 会被浏览器直接忽略。
 * localStorage 在隐私模式 / file:// 下可能抛异常，那时退回登录页 / 概览 /
 * 默认主题。
 */

/* 与 app.js 的 VIEWS 保持一致，由 embed_test.go 的 TestViewRestoreOnReload 钉住。
 * 只认白名单里的值：localStorage 是同源可控数据，但手改/旧版本残留一个
 * 不存在的页面名时，CSS 的「先藏概览」会没有对应显示规则 → 整页空白。
 * 非法值干脆不设属性，退回 index.html 默认的概览页。 */
var BOOT_VIEWS = ["overview", "inbounds", "outbounds", "clients", "traffic",
  "import", "gen", "configs", "apply", "settings", "audit"];

/* 主题白名单与旧版迁移表必须与 app.js 的 MODE_NAME / SCHEMES / LEGACY_THEME
 * 一致，由 TestFirstPaintTheme 逐项比对。不认的值回落 light/azure：写进
 * <html> 一个不存在的 scheme，CSS 变量就没了，首帧会画成没有样式的一页。
 * 旧版把亮暗与配色揉成一个 triune_theme，升级后第一次刷新也要直接画对，
 * 不能等 app.js 迁移完再跳一次。 */
var BOOT_MODES = ["light", "dark"];
var BOOT_SCHEMES = ["azure", "indigo", "forest", "rose", "solar", "steel", "ocean"];
var BOOT_LEGACY_THEME = {
  light: ["light", "azure"], dark: ["dark", "azure"], midnight: ["dark", "indigo"],
  forest: ["dark", "forest"], rose: ["dark", "rose"],
  solar: ["light", "solar"], steel: ["light", "steel"], ocean: ["light", "ocean"]
};

(function () {
  var boot = "guest", view = "", mode = "light", scheme = "azure";
  try {
    if (window.localStorage) {
      if (localStorage.getItem("triune_token")) boot = "app";
      view = localStorage.getItem("triune_view") || "";

      var m = localStorage.getItem("triune_mode");
      var s = localStorage.getItem("triune_scheme");
      if (!m && !s) {
        var legacy = BOOT_LEGACY_THEME[localStorage.getItem("triune_theme") || ""];
        // 挡掉 "__proto__" 之类命中原型链的键：那会拿到一个对象而不是数组。
        if (legacy && legacy.length === 2) { m = legacy[0]; s = legacy[1]; }
      }
      if (BOOT_MODES.indexOf(m) >= 0) mode = m;
      if (BOOT_SCHEMES.indexOf(s) >= 0) scheme = s;
    }
  } catch (e) { /* 存储不可用：保持登录页 / 概览页 / 默认主题 */ }

  var root = document.documentElement;
  root.setAttribute("data-boot", boot);
  // 主题与登录态无关：登录页也用同一套配色，深色偏好下不该先亮一帧。
  root.setAttribute("data-mode", mode);
  root.setAttribute("data-scheme", scheme);
  // 概览是 HTML 默认页，不需要标记；只有停在别处时才需要首帧纠偏，
  // 这样「属性存在但值没匹配上」的风险面只剩这一种情况，且已被白名单挡住。
  if (boot === "app" && view !== "overview" && BOOT_VIEWS.indexOf(view) >= 0) {
    root.setAttribute("data-boot-view", view);
  }
})();
