/* 首屏引导：在 <head> 里同步决定「第一帧画登录页还是主界面」，
 * 以及「主界面里画哪一页」。
 *
 * 原来 index.html 里登录页默认可见、#app 默认 .hide，而 app.js 要等
 * /api/status 一个网络往返回来才 showApp()——刷新时这一来一回就是用户
 * 看到的「先闪一下登录页，然后跳回主页」。网络慢或面板刚重启时更明显。
 * 第二个问题是 HTML 里 #v-overview 写死带 .active，刷新一律回概览：
 * 上次停在「配置生成」按一下 F5，先画概览，等 app.js 跑完才切回去。
 *
 * 这里只做一件事：按本地存下来的会话与页面偏好，给 <html> 打上
 * data-boot / data-boot-view，index.html 的 CSS 据此在**首次绘制之前**
 * 把登录页藏掉、把主界面和上次那一页显示出来。之后 app.js 调
 * showApp() / showLogin() 时会清掉这两个属性，显隐重新由 .hide / .active
 * 全权决定，CSS 的 !important 不会和它打架。
 *
 * 这不是权限判断：token 可能已过期，真正的校验仍在服务端（/api/status 与
 * 每个 API 的 401）。伪造 localStorage 只能让本机先看到一层没有数据的空壳。
 * 只读账号的页面纠正由 app.js 的 goto() 在同一个 JS 任务里完成，浏览器
 * 不会绘制中间态，所以这里不需要复制一份角色白名单。
 *
 * 必须外置：CSP 是 script-src 'self'，内联 <script> 会被浏览器直接忽略。
 * localStorage 在隐私模式 / file:// 下可能抛异常，那时退回「登录页优先」。
 */

/* 与 app.js 的 VIEWS 保持一致，由 embed_test.go 的 TestViewRestoreOnReload 钉住。
 * 只认白名单里的值：localStorage 是同源可控数据，但手改/旧版本残留一个
 * 不存在的页面名时，CSS 的「先藏概览」会没有对应显示规则 → 整页空白。
 * 非法值干脆不设属性，退回 index.html 默认的概览页。 */
var BOOT_VIEWS = ["overview", "inbounds", "outbounds", "clients", "traffic",
  "import", "gen", "configs", "apply", "settings", "audit"];

(function () {
  var boot = "guest", view = "";
  try {
    if (window.localStorage) {
      if (localStorage.getItem("triune_token")) boot = "app";
      view = localStorage.getItem("triune_view") || "";
    }
  } catch (e) { /* 存储不可用：保持登录页 / 概览页 */ }

  var root = document.documentElement;
  root.setAttribute("data-boot", boot);
  // 概览是 HTML 默认页，不需要标记；只有停在别处时才需要首帧纠偏，
  // 这样「属性存在但值没匹配上」的风险面只剩这一种情况，且已被白名单挡住。
  if (boot === "app" && view !== "overview" && BOOT_VIEWS.indexOf(view) >= 0) {
    root.setAttribute("data-boot-view", view);
  }
})();
