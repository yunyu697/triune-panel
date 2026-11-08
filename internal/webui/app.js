"use strict";
/* ================= 基础设施 ================= */
// 演示模式：URL 带 #demo，或文件名本身是 demo.html（离线演示双保险）
const DEMO = location.hash.includes("demo") || /demo\.html?$/i.test(location.pathname);
const $ = id => document.getElementById(id);

/* ================= 视口高度兜底 =================
   微信 X5 这类 Chromium 86 的内核不认 dvh，.shell 会落到 CSS 里的 100vh——
   那是「地址栏收起时」的高度，底部一截永远被地址栏盖着；而 .main 是内部滚动、
   页面本身不滚，滑也滑不到，表现就是「显示不全」。
   这里量一次实际可视高度写进 --app-h，CSS 侧用 @supports 只在老内核上取它。
   只在方向变化时重量：跟着地址栏伸缩实时改高度，本身就是「整页在闪」的成因。 */
(function syncAppHeight() {
  if (window.CSS && CSS.supports && CSS.supports("height", "100dvh")) return;  // 现代内核走 dvh
  const apply = () => {
    const vv = window.visualViewport;
    const h = Math.round((vv && vv.height) || window.innerHeight || 0);
    if (h > 0) document.documentElement.style.setProperty("--app-h", h + "px");
  };
  apply();
  addEventListener("orientationchange", () => setTimeout(apply, 300));
})();

/* ================= 事件委托 =================
   以前界面到处是 onclick="fn(...)" 这类内联事件属性。内联属性只能靠 CSP 的
   'unsafe-inline' 放行，而那一条同时也放行了注入的 <script>——等于把 XSS 的
   执行环节白送出去。现在改成：
     - HTML 只写 data-act="fn"（参数放 data-args，JSON 数组）
     - 键盘回车写 data-enter="fn"
     - 需要「元素本身」时写 data-el（1 / checked / value / num）
   这里在 document 上一次性委托 click/change/input/keydown，动作按名字从
   window 取——**不用 eval**，所以收紧 CSP 之后照样能跑。
   动态参数必须用 escJson() 生成：名字/令牌里带引号时，直接拼进属性会把
   HTML 结构写坏（这也是以前 onclick 里带用户名会失效的原因）。 */
function escJson(v) {
  return JSON.stringify(v)
    .replace(/&/g, "&amp;").replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function runAction(el, name, ev) {
  const fn = window[name];
  if (typeof fn !== "function") { console.error("[ui] 未知动作:", name); return; }
  let args = [];
  if (el.dataset.args) {
    try { args = JSON.parse(el.dataset.args); }
    catch (e) { console.error("[ui] 参数不是合法 JSON:", name, el.dataset.args); }
  }
  switch (el.dataset.el) {
    case "1": args.push(el); break;
    case "checked": args.push(!!el.checked); break;
    case "value": args.push(el.value); break;
    case "num": args.push(+el.value); break;
  }
  fn.apply(el, args);
}

function closestAct(t) {
  return (t && t.closest) ? t.closest("[data-act]") : null;
}

/* 事件 → 动作元素。data-self 的含义：遮罩类元素只有事件发生在**它自己身上**
   （点在空白区）才算，内部控件冒泡上来的一律忽略。

   这条判断必须 click/change/input 三个事件共用。以前只写在 click 里，于是
   input 事件从输入框冒泡到遮罩（遮罩挂着 data-act="closeModal"），在弹窗里
   每敲一个字就 closeModal 一次——所有对话框都「一编辑就退出」，而且控制台
   一声不响。change 同理（下拉框一选就关）。

   【回归测试】internal/webui/ui_delegation_test.go 盯着这三个注册都走这里。 */
function actFrom(e) {
  const el = closestAct(e.target);
  if (!el) return null;
  if (el.dataset.self === "1" && e.target !== el) return null;
  return el;
}
/* 同一个控件会连发多个事件：select 与 checkbox 是 click → input → change，
   text 输入框是 input →（失焦时）change。三个监听器都派发，动作就会执行三次
   ——「通知连弹三次」，写库也写三次。所以按事件源控件只认一种事件：

     select / checkbox / radio / file / range / color → change
     其它 input、textarea                            → input（边打边算，搜索框要这个）
     其余（按钮、div、svg 之类）                      → click

   判据用 e.target 而不是取到的动作元素：data-act 可能挂在包着控件的容器上，
   那种情况也要按里面控件的事件来派发，否则一次都不会触发。 */
const CHANGE_TYPES = ["checkbox", "radio", "file", "range", "color"];
function eventOf(target) {
  const tag = target.tagName;
  if (tag === "SELECT") return "change";
  if (tag === "TEXTAREA") return "input";
  if (tag === "INPUT") {
    const t = (target.type || "text").toLowerCase();
    return CHANGE_TYPES.includes(t) ? "change" : "input";
  }
  return "click";
}

/* 三个事件都必须走 actFrom()，不能各写一份 closestAct ——漏一个就是
   「一编辑就退出」。新增委托事件时照抄下面三行即可。 */
function onDelegated(e) {
  const el = actFrom(e);
  if (!el) return;
  if (e.type !== eventOf(e.target)) return;   // 同一控件只认一个事件，避免重复派发
  runAction(el, el.dataset.act, e);
}
document.addEventListener("click", onDelegated);
document.addEventListener("change", onDelegated);
document.addEventListener("input", onDelegated);
document.addEventListener("keydown", e => {
  if (e.key !== "Enter") return;
  const el = e.target && e.target.closest ? e.target.closest("[data-enter]") : null;
  if (!el) return;
  e.preventDefault();
  runAction(el, el.dataset.enter, e);
});

/* 两个只有「内联表达式」才能表达的动作，收成具名函数 */
function __dirty(el) { el.dataset.dirty = "1"; }
function genPolicySet(id, v) { genPolicies[id] = v; }
function reloadPage() { location.reload(); }
function removeSelf(el) { if (el && el.parentNode) el.parentNode.remove(); }

/* 「查看历史备份」：列 data/backups/panel 下的版本备份。
   这个按钮以前指向一个从未定义过的函数（点了没反应），顺手补上。 */
async function loadPanelBackups() {
  openModal("mView");
  $("vw_title").textContent = "面板历史备份";
  $("vw_body").textContent = "读取中…";
  try {
    const j = await req("/api/panel/backups");
    const lines = [
      "当前版本：" + (j.current_version || "—"),
      "可执行文件：" + (j.executable || "—"),
      "回滚备份 .bak：" + (j.bak_exists ? "有" : "无"),
      ""
    ];
    const bs = j.backups || [];
    lines.push(bs.length ? "版本备份（新 → 旧）：" : "版本备份：暂无");
    bs.forEach(n => lines.push("  " + n));
    lines.push("", "回滚入口：设置 → 运维 → 面板自更新（或在命令行跑 tp update --rollback）");
    $("vw_body").textContent = lines.join("\n");
  } catch (e) {
    $("vw_body").textContent = "读取失败：" + e.message;
  }
}

let TOKEN = localStorage.getItem("triune_token") || "";
/* 当前账号身份。只用于「体验层」显隐与提前拦截——
   真正的权限判定全在服务端，改前端改不掉 403。 */
let ROLE = localStorage.getItem("triune_role") || "";
let ME = { user: localStorage.getItem("triune_user") || "", role: ROLE };
const isOwner = () => ROLE === "owner";
const isViewer = () => ROLE === "viewer";
const ROLE_LABEL = { owner:"超级管理员", admin:"管理员", viewer:"只读" };
// 仪表盘数据状态（需在演示 mock 初始化之前声明）
const OV = { daily: [], hist: [], traffic: [], cores: [] };
let INB_N = 0;

const ICONS = {
  gen:'<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6"/>',
  overview:'<path d="M3 3h7v9H3zM14 3h7v5h-7zM14 12h7v9h-7zM3 16h7v5H3z"/>',
  inbounds:'<rect x="2" y="2" width="20" height="8" rx="2"/><rect x="2" y="14" width="20" height="8" rx="2"/><path d="M6 6h.01M6 18h.01"/>',
  // 出站：门框 + 向外的箭头。别用「托盘+上箭头」——那和「配置管理」的
  // 下载图标只差箭头方向，侧栏里一眼看过去会认错。
  outbounds:'<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="M16 17l5-5-5-5"/><path d="M21 12H9"/>',
  clients:'<path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75"/>',
  traffic:'<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>',
  import:'<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>',
  apply:'<path d="M9 11l3 3L22 4"/><path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11"/>',
  configs:'<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><path d="M7 10l5 5 5-5"/><path d="M12 15V3"/>',
  settings:'<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>',
  audit:'<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/><path d="M9 13h6M9 17h4"/>',
  // 备份管理：叠放的磁盘 + 时钟，和「配置管理」的下载托盘区分开
  backups:'<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v6c0 1.7 4 3 9 3s9-1.3 9-3V5"/><path d="M3 11v6c0 1.7 4 3 9 3s9-1.3 9-3v-6"/><path d="M12 12v4l2.5 1.5"/>',
  // 主机优化：主板 + 提速折线
  hostopt:'<rect x="2" y="3" width="20" height="14" rx="2"/><path d="M8 21h8M12 17v4"/><path d="M6 12l3-4 3 3 4-5"/>',
  // 更多：九宫格
  more:'<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/>'
};
/* 导航按「要做什么」分三段，不按对象堆列表：查看（看状态）、配置（改东西）、
   维护（管面板自己）。分组名一律是完整双字词，不用斜杠拼名字——手机上换行时
   斜杠会被误读成分隔符。顺序必须与 index.html 的 section、boot.js 的
   BOOT_VIEWS 完全一致，TestViewRestoreOnReload 会逐项比。 */
const VIEWS = [
  ["overview","概览","查看","一屏掌握入站、用户、流量与内核运行状态"],
  ["traffic","流量","查看","每日趋势、用量明细与配额消耗"],
  ["audit","审计","查看","登录、配置变更与关键操作留痕"],
  ["inbounds","入站","配置","管理监听端口、协议、传输与 TLS"],
  ["outbounds","出站","配置","查看当前内核渲染出的出站、路由规则与 DNS（只读）"],
  ["clients","用户","配置","账号、凭据、配额与到期时间"],
  ["import","外部节点","配置","把外部订阅与分享链接聚合进订阅输出"],
  ["gen","配置生成","配置","按目标设备与场景一键生成完整配置"],
  ["configs","配置管理","配置","保存的配置随时下载、重新生成与对比"],
  // 第 5 位 true = 不占导航位。应用是动作，入口是常驻命令条与弹窗（#mApply）；
  // 这一页只剩修订历史与内核日志。
  ["apply","修订与日志","配置","配置修订历史与内核运行日志", true],
  ["settings","设置","维护","内核路径、证书、版本管理与主题"],
  ["backups","备份管理","维护","四类旧版本文件统一盘点、保留策略与容量清理"],
  ["hostopt","主机优化","维护","BBR 与拥塞控制只读体检，默认不改系统设置"]
];
const TITLES = Object.fromEntries(VIEWS.map(v => [v[0], v[1]]));
const SUBS = Object.fromEntries(VIEWS.map(v => [v[0], v[3]]));

/* 只读账号的可见页面：其余页面依赖的接口（导出、应用、审计、设置）
   都会 403，与其让人点进去看一堆红字，不如不给入口。
   「用户」保留：服务端返回脱敏副本（凭据与订阅令牌为空），能看到用量与配额，
   但改不了。「设置」保留，里面只显示「我的账号」「登录设备」两张卡。 */
const VIEWER_VIEWS = ["overview", "inbounds", "outbounds", "clients", "traffic", "settings"];
function visibleViews() {
  return VIEWS.filter(([k, , , , hidden]) =>
    !hidden && (!isViewer() || VIEWER_VIEWS.includes(k)));
}

let CURRENT = "overview";
/* 手机端底部只放五个高频入口，其余收进「更多」抽屉，抽屉里仍按查看、
   配置、维护三段排列。抽屉里的标签和桌面一样不带斜杠。 */
const MOBILE_NAV = [
  ["overview", "概览"], ["inbounds", "入站"], ["apply", "应用"],
  ["settings", "设置"], ["more", "更多"]
];
const svgIcon = (k, size) =>
  `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${ICONS[k] || ICONS.overview}</svg>`;
/* 分组标题与导航项都用 VIEWS 里的原名，不造简写。 */
function renderNav() {
  const list = visibleViews();
  // 用 VIEWS 判断而不是 list：「修订与日志」不占导航位（hidden），
  // 但停在那页时不该被当成「页面不存在」踢回概览。
  if (!VIEWS.some(([k]) => k === CURRENT)) CURRENT = "overview";
  let html = "", last = null;
  list.forEach(([v, label, sec]) => {
    if (sec !== last) {
      html += `<div class="nav-sec">${escHtml(sec)}</div>`;
      last = sec;
    }
    html += `<button class="nav-item${v===CURRENT?" on":""}" data-v="${v}" title="${escHtml(label)}">
      ${svgIcon(v, 18)}<span>${escHtml(label)}</span></button>`;
  });
  $("nav").innerHTML = html;

  // 三条是切页，两条是动作（应用开弹窗、更多开抽屉），动作直接走事件委托。
  $("navMobile").innerHTML = MOBILE_NAV.map(([k, label]) => {
    const attr = k === "apply" ? 'data-act="openApply"'
      : k === "more" ? 'data-act="openMoreSheet"' : `data-v="${k}"`;
    const on = k !== "apply" && k !== "more" && k === CURRENT ? " on" : "";
    return `<button class="mnav-item${on}" ${attr} aria-label="${label}">
      ${svgIcon(k, 18)}<span>${label}</span></button>`;
  }).join("");

  let mh = "", mlast = null;
  list.forEach(([v, label, sec]) => {
    if (sec !== mlast) {
      if (mlast !== null) mh += "</div>";
      mh += `<div class="more-sec">${sec}</div><div class="more-grid">`;
      mlast = sec;
    }
    mh += `<button class="more-item${v===CURRENT?" on":""}" data-v="${v}">
      ${svgIcon(v, 15)}<span>${label}</span></button>`;
  });
  if (mlast !== null) mh += "</div>";
  $("moreBody").innerHTML = mh;
}
renderNav();
$("navMobile").addEventListener("click", e => {
  const b = e.target.closest("[data-v]");
  if (b) goto(b.dataset.v);
});
$("moreBody").addEventListener("click", e => {
  const b = e.target.closest("[data-v]");
  if (b) { closeMoreSheet(); goto(b.dataset.v); }
});
function openMoreSheet() { $("moreSheet").classList.add("show"); syncBodyLock(); }
function closeMoreSheet() { $("moreSheet").classList.remove("show"); syncBodyLock(); }

/* ---- 窄屏可用性 ---- */
/* 导航条在 960px 以下变成横向可滑的一行。切到靠后的页面时，选中的入口可能
   整个在视口外：用户既看不到「我在哪一页」，也找不到回去的路。
   只动导航条自己的 scrollLeft，不用 scrollIntoView——那会连 .main 一起滚，
   页面会莫名跳一下。 */
function scrollNavToActive() {
  const nav = $("nav");
  if (!nav || nav.scrollWidth <= nav.clientWidth + 1) return;
  const on = nav.querySelector(".nav-item.on");
  if (!on) return;
  const nb = nav.getBoundingClientRect(), ob = on.getBoundingClientRect();
  const left = ob.left - nb.left + nav.scrollLeft;
  nav.scrollLeft = Math.max(0, left - (nb.width - ob.width) / 2);
}
/* 表格列多时 .tbl-wrap 会横向截断，但界面上没有任何「还能往右滑」的迹象，
   手机上常以为缺的那一列根本不存在。提示必须放在滚动容器**外面**（兄弟节点），
   放里面会被横向滚动一起带走。只处理当前可见的表格：display:none 的 view
   量不出 scrollWidth，会被误判成「不用滑」。 */
function markScrollableTables() {
  document.querySelectorAll(".view.active .tbl-wrap, .overlay.show .tbl-wrap").forEach(w => {
    const can = w.scrollWidth > w.clientWidth + 2;
    let hint = w.previousElementSibling;
    if (hint && !hint.classList.contains("tbl-hint")) hint = null;
    if (can && !hint) {
      const el = document.createElement("div");
      el.className = "tbl-hint";
      el.textContent = "← 左右滑动查看全部列 →";
      w.parentNode.insertBefore(el, w);
    } else if (!can && hint) hint.remove();
  });
}
let __navResizeTimer;
addEventListener("resize", () => {
  clearTimeout(__navResizeTimer);
  __navResizeTimer = setTimeout(() => { markScrollableTables(); scrollNavToActive(); }, 120);
});
requestAnimationFrame(scrollNavToActive);

$("nav").addEventListener("click", e => {
  const b = e.target.closest(".nav-item");
  if (b) goto(b.dataset.v);
});
/* sect 可选：设置页分了三组，从别处「去改某一项」跳过来时要直接落到那一组，
   否则用户按提示进去只看到「常用」，找不到「组件更新」。 */
/* animate=false 只给刷新恢复用（restoreView）：首屏不播入场动画，
   见 index.html 的 .view.active.enter 与 html[data-boot] 规则。 */
function goto(v, sect, animate = true) {
  // 只读账号直接跳走隐藏页时，退回概览——避免「页面空白 + 一串 403」
  if (isViewer() && !VIEWER_VIEWS.includes(v)) v = "overview";
  CURRENT = v;
  // 记住当前页：刷新后 boot.js 会在首帧直接画这一页，不用先过一遍概览。
  // 存的是**纠正后**的 v——只读账号点进无权页面时 goto 会退回概览，不能把
  // 那个页面写进偏好，否则下次刷新又白闪一次再被踢回来。
  // 这是界面偏好而非会话身份，所以登出时不清：换账号回来仍停在熟悉的页面。
  try { localStorage.setItem("triune_view", v); } catch (_) {}
  // 给 CSS 一个「现在是哪一页」的钩子：手机端常驻的「应用」按钮在设置页（有自己的
  // 常驻保存条）和应用页（本来就在那儿）要藏起来，否则两套按钮叠在一起。
  document.body.dataset.view = v;
  document.querySelectorAll(".nav-item,.more-item,.mnav-item[data-v]").forEach(b =>
    b.classList.toggle("on", b.dataset.v === v));
  // 入场动画只给真正切页：刷新恢复（restoreView 传 animate=false）不播，
  // 否则首帧从 opacity:0 淡入，又是那下整页闪。
  VIEWS.forEach(([k]) => {
    const el = $("v-" + k), on = k === v;
    el.classList.toggle("active", on);
    el.classList.toggle("enter", !!animate && on);
  });
  // 切页回到顶部：窄屏是文档滚动、桌面是 .main 内部滚动，两个都归零。
  // 不归零的话从长页切到短页会停在半空，看着像「上面缺了一块」。
  window.scrollTo(0, 0);
  const mn = document.querySelector(".main");
  if (mn) mn.scrollTop = 0;
  scrollNavToActive();
  markScrollableTables();
  $("pageTitle").textContent = TITLES[v];
  $("pageSub").textContent = SUBS[v] || "";
  loadAll();
  if (v === "overview") {
    setTimeout(loadOverview, 30);
    wbEnsure();                 // 幂等：只加载一次布局并建好卡片头
    setTimeout(loadTodos, 60);  // 待办各来源独立请求，失败互不影响
  }
  if (v === "outbounds") loadOutbounds();
  if (v === "audit") loadAudit();
  if (v === "backups") loadBackupInventory();
  if (v === "hostopt") loadHostOpt();
  if (v === "configs") loadSavedConfigs();
  if (v === "settings") {
    /* 分组必须在**当前这个 JS 任务**里恢复。原来 setSectSaved() 排在下面的
       30ms 定时器里，浏览器有充足时间先画一帧 HTML 默认的「常用」再跳回
       「运维」——用户看到的就是「刷新先跳常用」。boot.js 的 data-boot-sect
       只兜住 app.js 执行之前的那段首帧，这里接手时 setSect() 会撤掉标记，
       两边必须得出同一个结果，否则仍会跳一下。
       同步调用是安全的：setSect() 只依赖已在 DOM 里的卡片与角色，
       .active 上面已经摆好，offsetParent 量得到东西。 */
    if (sect && SET_SECTS[sect]) setSect(sect, true);   // 深链接：用户点的，记住它
    else setSectSaved();
    setTimeout(() => {
      syncTopbarH();     // 进页面时补量一次：登录前量到的是 0
      loadPanelTLS();
      updateSetBar();    // 顶栏高度补量完再判一次常驻保存条
    }, 30);
  }
}

/* ================= 命令中枢 =================
   把「找页面」变成一步：全部视图 + 高频动作进同一个索引，Ctrl K 直达。
   动作项只存名字和参数，执行时按名字从 window 取——和事件委托同一套协议，
   不用 eval，CSP 仍是 script-src 'self'。只读账号看不到无权页面（visibleViews
   与侧栏共用一份过滤），隐藏的「修订与日志」也不会凭空冒出来。 */
const CMD_ACTIONS = [
  {act:"openInboundModal", title:"新增入站", group:"常用操作", hint:"入站 端口 协议", icon:"inbounds"},
  {act:"openClientModal", title:"新增用户", group:"常用操作", hint:"用户 账号 配额", icon:"clients"},
  {act:"openApply", title:"应用配置", group:"常用操作", hint:"应用 渲染 生效", icon:"apply"},
  {act:"goto", args:["gen"], title:"生成配置", group:"常用操作", hint:"生成 订阅 客户端", icon:"gen"},
  {act:"loadStatus", title:"刷新状态", group:"常用操作", hint:"刷新 内核 速率", icon:"overview"},
  {act:"goto", args:["backups"], title:"备份管理", group:"常用操作", hint:"备份 容量 清理", icon:"backups"},
  {act:"goto", args:["hostopt"], title:"主机优化", group:"常用操作", hint:"BBR 拥塞控制 体检", icon:"hostopt"}
];
function cmdItems() {
  const views = visibleViews().map(([k, label, sec, sub]) =>
    ({view:k, title:label, group:sec, hint:sub || "", icon:k}));
  return CMD_ACTIONS.concat(views);
}
let CMD_FILTERED = [], CMD_SEL = 0;
function cmdPaint(q) {
  const s = (q || "").trim().toLowerCase();
  CMD_FILTERED = cmdItems().filter(c =>
    !s || (c.title + " " + c.group + " " + (c.hint || "")).toLowerCase().includes(s)).slice(0, 16);
  CMD_SEL = 0;
  const host = $("cmdList");
  if (!CMD_FILTERED.length) {
    host.innerHTML = '<div class="cmd-empty">没有匹配的页面或操作</div>';
    return;
  }
  host.innerHTML = CMD_FILTERED.map((c, i) =>
    `<button class="cmd-item${i === 0 ? " on" : ""}" data-act="cmdRun" data-args='${escJson([i])}'>
      <span class="ci">${svgIcon(c.icon, 18)}</span>
      <span class="ct">${escHtml(c.title)}</span>
      <span class="cs">${escHtml(c.group)}</span></button>`).join("");
}
function cmdMove(d) {
  if (!CMD_FILTERED.length) return;
  CMD_SEL = (CMD_SEL + d + CMD_FILTERED.length) % CMD_FILTERED.length;
  document.querySelectorAll("#cmdList .cmd-item").forEach((b, i) =>
    b.classList.toggle("on", i === CMD_SEL));
  const on = document.querySelector("#cmdList .cmd-item.on");
  if (on && on.scrollIntoView) on.scrollIntoView({block:"nearest"});
}
function cmdExec(i) {
  const c = CMD_FILTERED[i];
  if (!c) return;
  closeCommand();
  if (c.view) { goto(c.view); return; }
  const fn = window[c.act];
  if (typeof fn === "function") fn.call(document.body, ...(c.args || []));
}
function cmdRun(i) { cmdExec(+i); }
function cmdQuery(v) { cmdPaint(v); }
function openCommand() {
  $("cmdOverlay").classList.add("show");
  $("cmdInput").value = "";
  cmdPaint("");
  syncBodyLock();
  // 下一任务再聚焦：面板刚 show 时部分 Android WebView 还量不到输入框。
  setTimeout(() => $("cmdInput").focus(), 0);
}
function closeCommand() {
  $("cmdOverlay").classList.remove("show");
  syncBodyLock();
}
document.addEventListener("keydown", e => {
  const k = (e.key || "").toLowerCase();
  if ((e.ctrlKey || e.metaKey) && (k === "k" || k === "p")) {
    e.preventDefault();
    $("cmdOverlay").classList.contains("show") ? closeCommand() : openCommand();
    return;
  }
  if (!$("cmdOverlay").classList.contains("show")) return;
  if (e.key === "Escape") { e.preventDefault(); closeCommand(); }
  else if (e.key === "ArrowDown") { e.preventDefault(); cmdMove(1); }
  else if (e.key === "ArrowUp") { e.preventDefault(); cmdMove(-1); }
  else if (e.key === "Enter") { e.preventDefault(); cmdExec(CMD_SEL); }
});

/* ================= 命令工作台 =================
   布局存服务端（跨设备一致），localStorage 只作只读账号 / 接口失败时的本机
   降级。卡片可折叠、置顶、排序；拖拽有上移下移按钮作为键盘与触摸的等价
   替代路径——不能只有一种操作方式。 */
/* 默认顺序按方案 B 设计稿：先待办、再看趋势与内核、然后快捷动作，
   KPI 大数字与占比/排行退到第二屏（用户仍可自行置顶/排序，布局存后端）。 */
const WB_KEYS = ["todo", "trend", "cores", "quick", "kpis", "users", "rank"];
const WB_SETTING = "wb_layout";
let WB = {order: WB_KEYS.slice(), collapsed: [], pinned: []};
let WB_READY = false, WB_RO = null, WB_DRAG = "";

function wbNormalize(raw) {
  const base = {order: WB_KEYS.slice(), collapsed: [], pinned: []};
  try {
    const o = typeof raw === "string" ? JSON.parse(raw) : (raw || {});
    const order = [];
    (Array.isArray(o.order) ? o.order : []).forEach(k => {
      if (WB_KEYS.includes(k) && !order.includes(k)) order.push(k);
    });
    // 新增卡片在旧布局里不存在时补到末尾，不能让它凭空消失。
    WB_KEYS.forEach(k => { if (!order.includes(k)) order.push(k); });
    base.order = order;
    ["collapsed", "pinned"].forEach(f => {
      base[f] = (Array.isArray(o[f]) ? o[f] : []).filter(k => WB_KEYS.includes(k));
    });
  } catch (_) { /* 脏值一律回落默认布局 */ }
  return base;
}
function wbOrder() {
  const p = WB.pinned.filter(k => WB.order.includes(k));
  return p.concat(WB.order.filter(k => !p.includes(k)));
}
/* 卡片头只建一次。之后只改按钮状态与文字，不重建 DOM——否则键盘用户按一次
   「上移」焦点就掉了，等于没有键盘可访问性。 */
function wbInitHeads() {
  document.querySelectorAll("#workbench .wb-card").forEach(card => {
    const k = card.dataset.wb;
    const head = card.querySelector(".wb-head");
    if (!head) return;
    const tools = [
      ["wbToggle", "▾", "折叠或展开"],
      ["wbPin", "★", "置顶"],
      ["wbUp", "↑", "上移"],
      ["wbDown", "↓", "下移"]
    ];
    head.innerHTML = `<h3>${escHtml(card.dataset.title)}<span class="hint">${escHtml(card.dataset.hint || "")}</span></h3>
      <div class="wb-tools">${tools.map(([act, label, tip]) =>
        `<button class="wb-tool" data-act="${act}" data-args='${escJson([k])}'
           aria-label="${tip}" title="${tip}">${label}</button>`).join("")}
        <button class="wb-tool" data-drag="1" data-key="${k}" draggable="true"
           aria-label="拖动排序" title="拖动排序">⠿</button></div>`;
  });
}
function wbPaintHeads() {
  document.querySelectorAll("#workbench .wb-card").forEach(card => {
    const k = card.dataset.wb;
    const folded = WB.collapsed.includes(k), pinned = WB.pinned.includes(k);
    card.classList.toggle("collapsed", folded);
    card.classList.toggle("pinned", pinned);
    const t = card.querySelector('[data-act="wbToggle"]');
    if (t) { t.setAttribute("aria-pressed", folded ? "true" : "false"); t.firstChild.nodeValue = folded ? "▸" : "▾"; }
    const p = card.querySelector('[data-act="wbPin"]');
    if (p) { p.setAttribute("aria-pressed", pinned ? "true" : "false"); }
  });
}
function wbApply() {
  const host = $("workbench");
  if (!host) return;
  wbOrder().forEach(k => {
    const c = host.querySelector(`[data-wb="${k}"]`);
    if (c) host.appendChild(c);
  });
  wbPaintHeads();
  wbMasonry();
}
function wbSave() {
  const val = JSON.stringify(WB);
  try { localStorage.setItem("triune_wb_layout", val); } catch (_) {}
  // 只读账号 / 演示模式写不进服务端也不该报错：本机布局照样可用。
  req("/api/settings", {method:"PUT", body:{[WB_SETTING]: val}}).catch(() => {});
}
async function wbEnsure() {
  if (WB_READY) return;
  WB_READY = true;
  wbInitHeads();
  wbInitDrag();
  wbInitMasonry();
  let raw = "";
  try { raw = (await req("/api/settings"))[WB_SETTING] || ""; } catch (_) {}
  if (!raw) { try { raw = localStorage.getItem("triune_wb_layout") || ""; } catch (_) {} }
  WB = wbNormalize(raw);
  wbApply();
}
function wbToggle(k) {
  const i = WB.collapsed.indexOf(k);
  if (i >= 0) WB.collapsed.splice(i, 1); else WB.collapsed.push(k);
  wbPaintHeads(); wbMasonry(); wbSave();
}
function wbPin(k) {
  const i = WB.pinned.indexOf(k);
  if (i >= 0) WB.pinned.splice(i, 1); else WB.pinned.push(k);
  wbApply(); wbSave();
}
function wbStep(k, d) {
  // 置顶段固定，只在非置顶卡片之间排序；否则「置顶」会被悄悄绕过。
  if (WB.pinned.includes(k)) { toast("取消置顶后才能调整顺序", "info"); return; }
  const i = WB.order.indexOf(k), j = i + d;
  if (i < 0 || j < 0 || j >= WB.order.length || WB.pinned.includes(WB.order[j])) return;
  [WB.order[i], WB.order[j]] = [WB.order[j], WB.order[i]];
  wbApply(); wbSave();
}
function wbUp(k) { wbStep(k, -1); }
function wbDown(k) { wbStep(k, 1); }
function wbMoveBefore(from, to) {
  if (from === to || WB.pinned.includes(from) || WB.pinned.includes(to)) return;
  const arr = WB.order.filter(k => k !== from);
  const i = arr.indexOf(to);
  arr.splice(i < 0 ? arr.length : i, 0, from);
  WB.order = arr;
}
function wbInitDrag() {
  const host = $("workbench");
  if (!host) return;
  host.addEventListener("dragstart", e => {
    const h = e.target.closest("[data-drag]");
    if (!h) return;
    WB_DRAG = h.dataset.key;
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = "move";
      e.dataTransfer.setData("text/plain", WB_DRAG);
    }
    const c = host.querySelector(`[data-wb="${WB_DRAG}"]`);
    if (c) c.classList.add("dragging");
  });
  host.addEventListener("dragend", () => {
    WB_DRAG = "";
    host.querySelectorAll(".dragging").forEach(c => c.classList.remove("dragging"));
  });
  host.addEventListener("dragover", e => {
    if (!WB_DRAG || !e.target.closest(".wb-card")) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
  });
  host.addEventListener("drop", e => {
    const c = e.target.closest(".wb-card");
    if (!c || !WB_DRAG) return;
    e.preventDefault();
    wbMoveBefore(WB_DRAG, c.dataset.wb);
    WB_DRAG = "";
    host.querySelectorAll(".dragging").forEach(x => x.classList.remove("dragging"));
    wbApply(); wbSave();
  });
}
/* 瀑布流：用 grid-row span 做不等高错落，DOM 顺序 = 视觉顺序 = Tab 顺序。
   不用 column-count（顺序会被按列拆开），也不用 dense（会打乱顺序）。
   手机上 CSS 直接退回单列，这里把 inline span 清干净。 */
function wbMasonry() {
  const host = $("workbench");
  if (!host) return;
  const cards = host.querySelectorAll(".wb-card");
  if (window.matchMedia("(max-width:960px)").matches) {
    cards.forEach(c => { c.style.gridRowEnd = ""; });
    return;
  }
  const cs = getComputedStyle(host);
  const row = parseFloat(cs.gridAutoRows) || 8;
  const gap = parseFloat(cs.rowGap) || 18;
  cards.forEach(c => {
    c.style.gridRowEnd = "";                 // 先回到 CSS 的占位 span，再量真实高度
    const h = c.getBoundingClientRect().height;
    if (h > 0) c.style.gridRowEnd = "span " + Math.max(2, Math.ceil((h + gap) / (row + gap)));
  });
}
function wbInitMasonry() {
  const host = $("workbench");
  if (!host) return;
  if (window.ResizeObserver) {
    WB_RO = new ResizeObserver(() => wbMasonry());
    host.querySelectorAll(".wb-card").forEach(c => WB_RO.observe(c));
  }
  addEventListener("resize", () => setTimeout(wbMasonry, 120));
}

/* 「今天要处理」是注册表：每个 provider 独立请求、独立失败，一个接口挂了
   不影响其它待办。以后要给主机优化加时钟 / 文件描述符 / conntrack 体检，
   在这里挂一个 provider 就行，工作台骨架不用改。 */
const TODO_PROVIDERS = [
  {key:"inbounds", async run() {
    const j = await req("/api/inbounds");
    const n = Array.isArray(j) ? j.length : (j.items || []).length;
    if (!n) return {level:"warn", title:"还没有入站", text:"先新增一个入站，才能生成客户端配置", view:"inbounds"};
    return null;
  }},
  {key:"backups", async run() {
    const j = await req("/api/backups/inventory");
    const s = j.summary || {}, p = j.policy || {};
    const total = s.total_bytes || 0;
    const limitMB = +(p.max_mb || 0);
    if (total > 200 * 1048576 || (limitMB > 0 && total > limitMB * 1048576)) {
      return {level:"warn", title:"备份占用 " + fmtBytes(total),
        text:"旧归档里含内核二进制，建议到备份管理设上限并清理", view:"backups"};
    }
    return null;
  }},
  {key:"hostopt", async run() {
    const j = await req("/api/host-opt");
    if (j.apply_enabled && !j.applied) {
      return {level:"info", title:"主机网络可以优化",
        text:"检测到 BBR 可用但当前未启用，面板默认不会改系统设置", view:"hostopt"};
    }
    return null;
  }}
];
async function loadTodos() {
  const host = $("wbTodo");
  if (!host) return;
  const marks = {warn:"!", err:"✕", info:"i", ok:"✓"};
  const out = await Promise.all(TODO_PROVIDERS.map(async p => {
    try { return await p.run(); } catch (_) { return null; }
  }));
  let items = out.filter(Boolean);
  if (!items.length) items = [{level:"ok", title:"一切正常",
    text:"入站、备份占用与主机优化都没有待处理项", view:"overview"}];
  host.innerHTML = items.map(it =>
    `<button class="todo-row" data-act="todoGo" data-args='${escJson([it.view || "overview"])}' title="${escHtml(it.text || it.title)}">
      <span class="todo-ic ${escHtml(it.level || "info")}">${marks[it.level] || "i"}</span>
      <span class="todo-tx"><b>${escHtml(it.title)}</b><span>${escHtml(it.text || "")}</span></span>
      <span class="todo-go">→</span></button>`).join("");
  wbMasonry();
}
function todoGo(view) { goto(view); }

/* ================= Toast / Modal / Confirm ================= */
function toast(msg, type = "ok") {
  const icons = {ok:"✓", err:"✕", warn:"!", info:"i"};
  const el = document.createElement("div");
  el.className = "toast " + type;
  el.innerHTML = `<b style="color:var(--${type==="info"?"acc-text":type})">${icons[type]||"i"}</b><span>${msg}</span>`;
  $("toasts").appendChild(el);
  setTimeout(() => { el.classList.add("out"); setTimeout(() => el.remove(), 320); }, 3000);
}
function openModal(id){
  const ov = $(id);
  ov.classList.add("show");
  // 上次关掉时停在中段的弹窗，再打开应该从头看；顺带重算表格横滑提示
  const m = ov.querySelector(".modal"); if (m) m.scrollTop = 0;
  markScrollableTables();
  syncBodyLock();
}
function closeModal(id){ $(id).classList.remove("show"); syncBodyLock(); }
/* 弹窗开着时锁住页面滚动。窄屏是文档滚动，不锁的话手指在遮罩上一拖，
   背后的页面跟着滑，看着像整页在抖。按 DOM 现状重算而不做计数——
   计数一旦漏减就会把页面永久锁死，那是比不锁严重得多的故障。 */
function syncBodyLock(){
  // 按 DOM 现状重算，绝不计数：计数漏减会把页面永久锁死。
  const modal = document.querySelector(".overlay.show") ? "hidden" : "";
  // 命令面板与「更多」抽屉不是 .overlay（定位方式不同），但同样是铺满视口的
  // 浮层，漏掉任一个窄屏上手指一拖背后的文档就跟着滚。
  const layer = document.querySelector(".cmd-overlay.show,.more-sheet.show") ? "hidden" : "";
  document.body.style.overflow = modal || layer;
}
function ask(title, text) {
  return new Promise(resolve => {
    $("cf_title").textContent = title; $("cf_text").textContent = text;
    openModal("mConfirm");
    $("cf_ok").onclick = () => { closeModal("mConfirm"); resolve(true); };
    $("mConfirm").onclick = e => { if (e.target === $("mConfirm")) resolve(false); };
  });
}
function viewContent(title, body) { $("vw_title").textContent = title; $("vw_body").textContent = body; openModal("mView"); }

/* ================= 主题：亮暗 × 色彩方案 =================
   以前 8 个主题是一维列表，色块上只画了一个主色：浅色和曜石看着都是蓝，
   点下去一个是白底一个是黑底。现在拆成两层，色块画的是**真实配色组合**。 */
const SCHEMES = [
  ["azure", "蔚蓝"], ["indigo", "靛夜"], ["forest", "森野"], ["rose", "玫瑰"],
  ["solar", "暖阳"], ["steel", "雾蓝"], ["ocean", "海盐"]
];
const MODE_NAME = { light: "浅色", dark: "暗色" };
// 旧版把亮暗和配色揉进一个 theme id。升级后要把 localStorage 里的旧值
// 翻译成「模式 + 方案」，否则老用户打开面板会掉回默认配色。
const LEGACY_THEME = {
  light: ["light", "azure"], dark: ["dark", "azure"], midnight: ["dark", "indigo"],
  forest: ["dark", "forest"], rose: ["dark", "rose"],
  solar: ["light", "solar"], steel: ["light", "steel"], ocean: ["light", "ocean"]
};
let themeMode = "light", themeScheme = "azure";

// 预览色直接从 CSS 变量读：预览和真实主题共用同一份定义，
// 不会出现「色块是这种蓝、应用后是那种蓝」。读不到（老浏览器）时给个兜底。
function schemeVars(mode, scheme) {
  const probe = document.createElement("div");
  probe.setAttribute("data-mode", mode);
  probe.setAttribute("data-scheme", scheme);
  probe.style.cssText = "position:absolute;left:-9999px;top:0;width:0;height:0;pointer-events:none";
  document.body.appendChild(probe);
  const cs = getComputedStyle(probe);
  const g = k => (cs.getPropertyValue(k) || "").trim();
  const out = {
    bg: g("--bg") || "#ffffff", side: g("--side") || "#ffffff", card: g("--card") || "#ffffff",
    text: g("--text") || "#111111", line: g("--line") || "#dddddd",
    acc: g("--acc") || "#3b82f6", acc2: g("--acc2") || "#8b5cf6",
    muted: g("--muted") || "#888888", ok: g("--ok") || "#16a34a",
    err: g("--err") || "#dc2626", chart: g("--chart-down") || "#22c55e",
    code: g("--code-bg") || "#f1f4f9"
  };
  probe.remove();
  return out;
}
/* 预览卡上每个色块对应哪个 CSS 变量、悬停时显示什么名字。
   数量比旧版多一倍：颜色方案现在同时决定语义色、图表色与代码块底色，
   缩略图必须把这些层都画出来，用户才能一眼看出「这是一整套配色」。
   renderSchemes() 第一次建 DOM，之后只改已有节点的 style/title/class。 */
const SCHEME_DOTS = [
  ["bg", "背景"], ["side", "侧栏"], ["card", "卡片"], ["text", "文字"],
  ["acc", "主色"], ["acc2", "辅色"], ["ok", "成功"], ["chart", "图表"]
];
/* 大预览卡是一张缩略页面：侧栏、顶栏、正文卡片、按钮、状态点、图表条。
   节点只在这里创建一次，paintScheme() 之后只改 style，不重建 DOM。 */
function schemeMarkup([id, name]) {
  return `<button class="scheme" data-s="${id}">
      <span class="prev">
        <i class="p-side"></i><i class="p-nav1"></i><i class="p-nav2"></i><i class="p-nav3"></i>
        <i class="p-top"></i><i class="p-card"></i><i class="p-ln"></i><i class="p-ln2"></i>
        <i class="p-btn"></i><i class="p-ok"></i><i class="p-chart"></i>
      </span>
      <span class="nm"><b>${name}</b><span class="chk">✓</span></span>
      <span class="dots">${SCHEME_DOTS.map(() => "<i></i>").join("")}</span>
      <span class="hex">—</span>
    </button>`;
}
/* 增量刷新一张预览卡。原来每次切主题都用 innerHTML 重建 7 张（每张 12 个
   节点），弹窗里的网格会整块闪一下。这里一个节点都不换：文字用
   textNode.nodeValue 改（characterData，不是 childList），颜色只写 style，
   选中态只切 class——MutationObserver 盯 childList 应当一条都记不到。 */
function paintScheme(btn, id, name) {
  const c = schemeVars(themeMode, id);
  btn.classList.toggle("on", id === themeScheme);
  btn.title = MODE_NAME[themeMode] + " · " + name;
  btn.querySelector(".prev").style.background = c.bg;
  const side = btn.querySelector(".p-side");
  side.style.background = c.side;
  side.style.borderRightColor = c.line;
  const card = btn.querySelector(".p-card");
  card.style.background = c.card;
  card.style.borderColor = c.line;
  btn.querySelector(".p-ln").style.background = c.text;
  btn.querySelector(".p-ln2").style.background = c.muted;
  [".p-nav1", ".p-nav2", ".p-nav3"].forEach((sel, i) => {
    const n = btn.querySelector(sel);
    n.style.background = i === 0 ? c.acc : c.muted;
    n.style.opacity = i === 0 ? "1" : ".42";
  });
  btn.querySelector(".p-top").style.background = c.card;
  btn.querySelector(".p-top").style.borderBottomColor = c.line;
  btn.querySelector(".p-btn").style.background = `linear-gradient(135deg,${c.acc},${c.acc2})`;
  btn.querySelector(".p-ok").style.background = c.ok;
  btn.querySelector(".p-chart").style.background =
    `linear-gradient(90deg,${c.acc} 45%,${c.chart} 45%)`;
  const dots = btn.querySelectorAll(".dots i");
  SCHEME_DOTS.forEach(([k, label], i) => {
    dots[i].style.background = c[k];
    dots[i].title = `${label} ${c[k]}`;
  });
  btn.querySelector(".hex").firstChild.nodeValue = `${c.acc} · ${c.acc2}`;
}
function renderSchemes() {
  const cur = SCHEMES.find(s => s[0] === themeScheme);
  $("themeNow").textContent = MODE_NAME[themeMode] + " · " + (cur ? cur[1] : "蔚蓝");
  $("schemeN").textContent = SCHEMES.length + " 套";
  document.querySelectorAll("#modeSeg button").forEach(b => b.classList.toggle("on", b.dataset.mode === themeMode));
  const host = $("schemes");
  if (!host.children.length) host.innerHTML = SCHEMES.map(schemeMarkup).join("");
  [...host.querySelectorAll(".scheme")].forEach(btn => {
    const s = SCHEMES.find(x => x[0] === btn.dataset.s);
    if (s) paintScheme(btn, s[0], s[1]);
  });
}
let themeSwitchSeq = 0;
/* 切换瞬间给 <html> 挂 data-theme-switch：index.html 用它把整页的
   transition/animation 压成 none，CSS 变量一次换到位，不再插值出一串
   中间色。两帧后撤——一帧让浏览器按「无过渡」算出新样式，第二帧兜住
   样式重算与合成。序号挡住连点：前一次的定时器不能撤掉后一次的标记。 */
function markThemeSwitch() {
  const r = document.documentElement;
  const seq = ++themeSwitchSeq;
  r.setAttribute("data-theme-switch", "");
  requestAnimationFrame(() => requestAnimationFrame(() => {
    if (seq === themeSwitchSeq) r.removeAttribute("data-theme-switch");
  }));
}
function applyTheme() {
  markThemeSwitch();   // 必须排在改 data-mode/scheme 之前，同一个 JS 任务里
  const r = document.documentElement;
  r.dataset.mode = themeMode;
  r.dataset.scheme = themeScheme;
  localStorage.setItem("triune_mode", themeMode);
  localStorage.setItem("triune_scheme", themeScheme);
  localStorage.removeItem("triune_theme");
  // 主屏图标启动后的状态栏 / 任务切换器顶色跟着主题走。
  // manifest 里的 theme_color 是静态的（只能是浅色），深色主题下会露出
  // 一条浅色顶栏；这里按当前 --bg 覆盖一次，装到主屏后才不会突兀。
  const tc = document.querySelector('meta[name="theme-color"]');
  if (tc) {
    const bg = getComputedStyle(r).getPropertyValue("--bg").trim();
    if (bg) tc.setAttribute("content", bg);
  }
  setTimeout(() => { if (typeof redrawCharts === "function") redrawCharts(); }, 60);
}
/* 密度与圆角是独立于明暗、配色的两根轴：外观偏好只存本机，和服务端配置无关。
   改它们同样要压掉一帧过渡（圆角/内距都在 transition 里），所以复用
   markThemeSwitch()。boot.js 会在首帧就恢复属性，避免刷新后先画默认档。 */
const DENSITIES = ["compact", "comfortable"];
const RADII = ["sharp", "normal", "soft"];
let themeDensity = "comfortable", themeRadius = "normal";
function paintAxes() {
  markThemeSwitch();
  const r = document.documentElement;
  r.dataset.density = themeDensity;
  if (themeRadius === "normal") r.removeAttribute("data-radius");
  else r.dataset.radius = themeRadius;
  localStorage.setItem("triune_density", themeDensity);
  localStorage.setItem("triune_radius", themeRadius);
  document.querySelectorAll("#densitySeg button").forEach(b =>
    b.classList.toggle("on", b.dataset.v === themeDensity));
  document.querySelectorAll("#radiusSeg button").forEach(b =>
    b.classList.toggle("on", b.dataset.v === themeRadius));
  setTimeout(() => { if (typeof redrawCharts === "function") redrawCharts(); }, 60);
}
function setDensity(v) {
  if (!DENSITIES.includes(v) || v === themeDensity) return;
  themeDensity = v; paintAxes();
}
function setRadius(v) {
  if (!RADII.includes(v) || v === themeRadius) return;
  themeRadius = v; paintAxes();
}
function setMode(m) { if (!MODE_NAME[m] || m === themeMode) return; themeMode = m; applyTheme(); renderSchemes(); }
function setScheme(s) { if (s === themeScheme) return; themeScheme = s; applyTheme(); renderSchemes(); }
$("schemes").addEventListener("click", e => {
  const b = e.target.closest(".scheme");
  if (b) setScheme(b.dataset.s);
});
function toggleThemePop() { $("themePop").classList.toggle("show"); }
document.addEventListener("click", e => {
  if (!e.target.closest("#themePop") && !e.target.closest("#themeBtn")) $("themePop").classList.remove("show");
});
(function initTheme() {
  let mode = localStorage.getItem("triune_mode");
  let scheme = localStorage.getItem("triune_scheme");
  if (!mode && !scheme) {
    const legacy = LEGACY_THEME[localStorage.getItem("triune_theme") || ""];
    if (legacy) { mode = legacy[0]; scheme = legacy[1]; }
  }
  if (!MODE_NAME[mode]) mode = "light";
  if (!SCHEMES.some(s => s[0] === scheme)) scheme = "azure";
  themeMode = mode; themeScheme = scheme;
  // 首帧属性已由 boot.js 写过，这里只把 JS 状态与分段按钮对齐，不再挂
  // data-theme-switch（刷新时 CSS 本来就没有过渡可压）。
  let d = localStorage.getItem("triune_density");
  let rad = localStorage.getItem("triune_radius");
  if (!DENSITIES.includes(d)) d = "comfortable";
  if (!RADII.includes(rad)) rad = "normal";
  themeDensity = d; themeRadius = rad;
  const r0 = document.documentElement;
  r0.dataset.density = d;
  if (rad === "normal") r0.removeAttribute("data-radius");
  else r0.dataset.radius = rad;
  applyTheme();
  renderSchemes();
  document.querySelectorAll("#densitySeg button").forEach(b =>
    b.classList.toggle("on", b.dataset.v === d));
  document.querySelectorAll("#radiusSeg button").forEach(b =>
    b.classList.toggle("on", b.dataset.v === rad));
})();

/* ================= API（含演示 mock） ================= */
async function req(path, opts = {}) {
  const m = opts.method || "GET";
  // 只读账号的写请求在本地就拦掉：服务端会回 403，但等一个往返再弹错误
  // 不如直接说清楚「你是只读账号」。绕过前端也没用——服务端照样 403。
  // 例外：会话吊销属于「管自己的登录」，不是改配置，服务端对每条会话单独判权
  //（只能踢自己或权限更低的），只读账号也该能把自己的设备退掉。
  if (isViewer() && m !== "GET" && !path.startsWith("/api/sessions")) throw new Error("当前账号为只读角色，无权执行此操作");
  if (DEMO) return mockRoute(path, opts);
  const r = await fetch(path, {
    method: m,
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + TOKEN },
    body: opts.body ? JSON.stringify(opts.body) : undefined
  });
  const j = await r.json().catch(() => ({}));
  if (r.status === 401) { dropSession(); showLogin(); throw new Error(j.error || "unauthorized"); }
  if (!r.ok) throw new Error(j.error || r.status);
  return j;
}

/* -------- 演示模式 mock 数据 -------- */
let MDB, REV_N = 3;
if (DEMO) {
  const seedDaily = n => Array.from({length:n},(_,i)=>{
    const d = new Date(Date.now()-(n-1-i)*864e5).toISOString().slice(0,10);
    // 双周期叠加 + 周末低谷，曲线更接近真实用量
    const week = 1 + 0.22*Math.sin(i/2.6) + 0.12*Math.sin(i/1.3 + 0.7);
    const dow = [0.82,0.86,0.9,0.95,1.12,1.28,1.18][new Date(d).getDay()];
    const base = 0.55 + 0.45*Math.abs(Math.sin(i/5.1 + 0.4));
    return {day:d, up:Math.round(2.4e9*base*week*dow*(0.92+0.16*Math.cos(i/2))),
                 down:Math.round(9.2e9*base*week*dow*(0.9+0.22*Math.sin(i/3.1)))};
  });
  // 演示数据：预置一段实时速率采样历史，首屏即可看到实时速率曲线
  OV.hist = Array.from({length: 30}, (_, i) => ({
    up: Math.round(1.6e5 + 9e4 * Math.sin(i / 3.1) + Math.random() * 5e4),
    down: Math.round(1.7e6 + 8e5 * Math.sin(i / 4.7 + 1) + Math.random() * 4e5)
  }));
  MDB = {
    version:"v0.8.9-demo",
    // 演示用的规则标签与已保存配置：让「点标签套用」「保存后下载」这条链路
    // 在离线演示页里也能完整走一遍
    ruleTags:[
      {id:1,name:"公司内网",count:3,created_at:"2026-10-28 14:12:00",
       rules:"DOMAIN-SUFFIX,corp.example.com,DIRECT\nIP-CIDR,10.0.0.0/8,DIRECT\nIP-CIDR,172.16.0.0/12,DIRECT"},
      {id:2,name:"广告拦截",count:3,created_at:"2026-10-28 14:20:00",
       rules:"DOMAIN-KEYWORD,doubleclick,REJECT\nDOMAIN-SUFFIX,googlesyndication.com,REJECT\nDOMAIN-SUFFIX,adservice.google.com,REJECT"},
      {id:3,name:"国内直连",count:2,created_at:"2026-10-29 09:05:00",
       rules:"DOMAIN-SUFFIX,cn,DIRECT\nGEOSITE,bilibili,DIRECT"}
    ],
    savedConfigs:[
      {id:2,name:"主路由-singbox1.14",note:"改端口前的备份",core:"singbox",core_version:"1.14",
       target:"main-router",scenario:"balanced",size:3184,hash:"d3m0b2",created_by:"admin",
       params:'{"core":"singbox","core_version":"1.14","target":"main-router","inbound_ids":[1,2],"scenario":"balanced"}',
       created_at:"2026-10-30 21:40:00",updated_at:"2026-10-30 21:40:00"},
      {id:1,name:"家里电视-mihomo",note:"",core:"mihomo",core_version:"",
       target:"side-router",scenario:"media",size:2015,hash:"d3m0b1",created_by:"admin",
       params:'{"core":"mihomo","target":"side-router","inbound_ids":[1],"scenario":"media"}',
       created_at:"2026-10-27 18:02:00",updated_at:"2026-10-27 18:02:00"}
    ],
    inbounds:[
      {id:1,name:"hk-01-reality",core:"singbox",protocol:"vless",port:443,transport:"tcp",tls:"reality",listen:"",enabled:true,draft:false,settings:"{}"},
      {id:2,name:"jp-01-ws",core:"xray",protocol:"vless",port:8443,transport:"ws",tls:"tls",listen:"",enabled:true,draft:false,settings:"{}"},
      {id:3,name:"sg-01-hy2",core:"singbox",protocol:"hysteria2",port:36712,transport:"udp",tls:"tls",listen:"",enabled:false,draft:false,settings:"{}"}
    ],
    clients:[
      {client:{id:1,name:"alice",credential:"8f2b1c3d-9e4a-4f5b-8c6d-7e8f9a0b1c2d",sub_token:"demo1234abcd",quota_gb:50,expires_at:null,enabled:true},inbound_ids:[1,2]},
      {client:{id:2,name:"bob",credential:"p@ssw0rd-bob",sub_token:"demo5678efgh",quota_gb:0,expires_at:"2026-12-31T00:00:00Z",enabled:true},inbound_ids:[1]},
      {client:{id:3,name:"carol",credential:"2a7c-41dd-9b0e",sub_token:"demo9012ijkl",quota_gb:100,expires_at:"2026-10-15T00:00:00Z",enabled:false},inbound_ids:[2]}
    ],
    counters:{1:{up:3.6e9,down:41.6e9},2:{up:0.9e9,down:6.2e9},3:{up:0,down:0}},
    daily:seedDaily(30),
    external:[
      {id:1,name:"香港 IEPL-01",raw_link:"vless://8f2b1c3d@hk-iepl.example.net:443?security=tls#香港 IEPL-01",created_at:"2026-10-28T09:12:44",
       parsed:{ok:true,protocol:"vless",host:"hk-iepl.example.net",port:443,network:"tcp",security:"tls"}},
      {id:2,name:"日本 BGP-02",raw_link:"vmess://eyJhZGQiOiJqcC0wMi5leGFtcGxlLmNvbSIsInBvcnQiOjQ0MywiaWQiOiJkZGVhZGVhZCJ9",created_at:"2026-10-29T21:03:17",
       parsed:{ok:true,protocol:"vmess",host:"jp-02.example.com",port:443,network:"ws",security:"tls",sni:"jp-02.example.com"}},
      {id:3,name:"美国 中转-01",raw_link:"trojan://p@ss@us-relay.example.org:8443#美国 中转-01",created_at:"2026-10-30T13:45:02",
       parsed:{ok:true,protocol:"trojan",host:"us-relay.example.org",port:8443,network:"tcp",security:"tls"}},
      {id:4,name:"旧订阅-ss-01",raw_link:"ss://旧格式链接#旧订阅-ss-01",created_at:"2026-10-31T02:08:51",
       parsed:{ok:false,error:"ss: base64 解码失败"}}
    ],
    revisions:[
      {id:3,core:"singbox",status:"applied",note:"web apply",created_at:"2026-10-31T03:40:11",content:"(修订内容示例)"},
      {id:2,core:"singbox",status:"applied",note:"auto: 用户停用后重新应用",created_at:"2026-10-31T02:10:05",content:"(修订内容示例)"},
      {id:1,core:"singbox",status:"failed_validation",note:"导入预检",created_at:"2026-10-30T22:51:33",detail:"校验失败示例: inbound/sg-01: port 36712 already in use"}
    ],
    settings:{bin_singbox:"/usr/local/bin/sing-box",bin_xray:"/usr/local/bin/xray",bin_mihomo:"",public_host:"vps.example.com",stats_enabled:"1",stats_secret:"",metrics_token:"demo-metrics-token-0123456789abcdef",quota_warn_pct:"80",acme_enabled:"0",acme_domain:"",acme_email:"",auth_email:"admin@example.com",smtp_host:"smtp.example.com",smtp_port:"465",smtp_user:"demo",smtp_from:"panel@example.com",notify_email:"ops@example.com",notify_webhook:"https://hooks.example.com/triune",notify_events:"settings,inbound,client",external_in_sub:"0",show_compat_core:"0",
      backup_keep_count:"0",backup_max_mb:"0",backup_max_days:"0",backup_auto_prune:"0"},
    // 备份容量治理与主机体检的演示数据：让离线 demo 也能完整走一遍预览→清理、
    // 检测→应用→撤销，而不是点进去一片空白。
    backupInventory:{
      entries:[
        {key:"data/data/triune-data-20261101-030000.tar.gz",name:"triune-data-20261101-030000.tar.gz",category:"data",group:"data",size:48234496,mod_time:"2026-11-01T03:00:00Z",age_days:7},
        {key:"data/data/triune-data-20261104-030000.tar.gz",name:"triune-data-20261104-030000.tar.gz",category:"data",group:"data",size:48951296,mod_time:"2026-11-04T03:00:00Z",age_days:4},
        {key:"data/data/triune-data-20261108-030000.tar.gz",name:"triune-data-20261108-030000.tar.gz",category:"data",group:"data",size:49123328,mod_time:"2026-11-08T03:00:00Z",age_days:0},
        {key:"panel/panel/v1.3.1-20261104-030000.bak",name:"v1.3.1-20261104-030000.bak",category:"panel",group:"panel",size:15683746,mod_time:"2026-11-04T03:00:00Z",age_days:4},
        {key:"panel/panel/v1.3.2-20261108-030000.bak",name:"v1.3.2-20261108-030000.bak",category:"panel",group:"panel",size:15719152,mod_time:"2026-11-08T03:00:00Z",age_days:0},
        {key:"core/xray/20261104-030000.bak",name:"20261104-030000.bak",category:"core",group:"xray",size:32833408,mod_time:"2026-11-04T03:00:00Z",age_days:4},
        {key:"core/xray/20261108-030000.bak",name:"20261108-030000.bak",category:"core",group:"xray",size:32901120,mod_time:"2026-11-08T03:00:00Z",age_days:0},
        {key:"revision/xray/20261104-030000.json.bak",name:"20261104-030000.json.bak",category:"revision",group:"xray",size:8192,mod_time:"2026-11-04T03:00:00Z",age_days:4},
        {key:"revision/xray/20261108-030000.json.bak",name:"20261108-030000.json.bak",category:"revision",group:"xray",size:8448,mod_time:"2026-11-08T03:00:00Z",age_days:0}
      ],
      summary:{total_bytes:243451886,count:9,
        by_category:{data:146309120,panel:31402898,core:65734528,revision:16640},
        disk_free:12884901888,disk_total:21474836480,
        hint:"共 9 份、占用 232.2 MB；数据归档 3 份占 139.5 MB。归档会把 cores/ 下的内核二进制一起打包，份数越多越占空间。每个分类至少保留最新一份，清理不会把回滚能力清空。",
        roots:["/opt/triune-panel/data/backups","/opt/triune-panel/backups"]},
      policy:{keep_count:0,max_mb:0,max_days:0,auto_prune:false},planned:[]
    },
    hostOpt:{
      platform:"linux",kernel:"6.8.0-45-generic",current_cc:"cubic",
      available_cc:["reno","cubic","bbr"],bbr_module:true,default_qdisc:"pfifo_fast",
      qdisc_detail:"qdisc pfifo_fast 0: dev eth0 root refcnt 2 bands 3 priomap 1 2 2 2 1 2 0 0 1 1 1 1 1 1 1 1",
      in_container:false,is_root:true,
      checks:[
        {name:"内核版本 ≥ 4.9",ok:true,warn:false,detail:"内核 6.8.0-45-generic，支持 BBR"},
        {name:"BBR 可用",ok:true,warn:false,detail:"tcp_bbr 模块已加载，available_congestion_control 含 bbr"},
        {name:"当前拥塞控制已是 bbr",ok:false,warn:false,detail:"当前 tcp_congestion_control = cubic"},
        {name:"默认 qdisc 为 fq/fq_codel",ok:false,warn:false,detail:"net.core.default_qdisc = pfifo_fast"},
        {name:"tc 实测 qdisc",ok:false,warn:false,detail:"eth0 当前为 pfifo_fast"},
        {name:"当前不在容器里",ok:true,warn:false,detail:"未检测到 docker / k8s / lxc / OpenVZ 特征"},
        {name:"面板具备 root 权限",ok:true,warn:false,detail:"当前以 root 运行，可写 /etc/sysctl.d"},
        {name:"时钟同步（NTP）",ok:true,warn:false,detail:"时区 Asia/Shanghai，NTPSynchronized=yes"},
        {name:"文件描述符上限",ok:false,warn:false,detail:"面板进程 soft=1024 hard=1048576，系统 fs.file-max=9223372036854775807，已分配 1280 个；soft 上限低于建议值 65535，高并发时会拒新连接（systemd 服务可设 LimitNOFILE=65535）"},
        {name:"conntrack 连接跟踪水位",ok:false,warn:true,detail:"未启用 nf_conntrack（没有 NAT / 端口转发时属正常；若做转发请确认模块已加载）"},
        {name:"sysctl 配置覆盖检查",ok:true,warn:false,detail:"没有更靠后的配置文件覆盖面板管理的 sysctl 键"}
      ],
      suggested:[
        "# 手动应用（等价面板的「应用」）",
        "sudo tee /etc/sysctl.d/99-triune-bbr.conf >/dev/null <<'EOF'",
        "net.ipv4.tcp_congestion_control=bbr",
        "net.core.default_qdisc=fq",
        "net.core.rmem_max=16777216",
        "net.core.wmem_max=16777216",
        "net.core.somaxconn=8192",
        "net.ipv4.ip_local_port_range=10240 60999",
        "EOF",
        "sudo sysctl -p /etc/sysctl.d/99-triune-bbr.conf",
        "# 验证",
        "sysctl net.ipv4.tcp_congestion_control net.core.default_qdisc net.core.somaxconn",
        "sudo tc qdisc show dev eth0"
      ],
      apply_enabled:true,reason:"",applied:false,config_path:"/etc/sysctl.d/99-triune-bbr.conf",
      tune:{cc:"cubic",qdisc:"pfifo_fast",rmem_max:212992,wmem_max:212992,somaxconn:4096,port_start:32768,port_end:60999},
      options:{cc:["reno","cubic","bbr"],qdisc:["fq","fq_codel","cake"]},
      system:{load:[0.42,0.31,0.28],cpu_count:4,mem_total:8589934592,mem_avail:5368709120,
        disk_total:21474836480,disk_free:12884901888,ipv6:true,mtu:1500,mtu_if:"eth0",warnings:[]},
      expected:null
    },
    admins:[
      {id:1,username:"admin",role:"owner",enabled:true,has_totp:true,last_ip:"203.0.113.7",last_login:"2026-10-31 09:24:17",created_at:"2026-09-01 10:00:00"},
      {id:2,username:"ops-lin",role:"admin",enabled:true,has_totp:false,last_ip:"198.51.100.23",last_login:"2026-10-30 22:41:05",created_at:"2026-10-12 14:20:00"},
      {id:3,username:"noc-watch",role:"viewer",enabled:false,has_totp:false,last_ip:"",last_login:"",created_at:"2026-10-20 09:00:00"}
    ],
    // 登录设备：三条会话，其中一条是「本机」，一条是同事，一条是可疑 IP
    sessions:[
      {id:"9f3c1a7b2d4e5f60",user:"admin",role:"owner",ip:"203.0.113.7",
       device:"Chrome · macOS",ua:"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/141.0 Safari/537.36",
       created:"2026-10-31 09:24:17",last_seen:"2026-10-31 09:41:52",expires:"2026-11-01 09:24:17",current:true,revocable:true},
      {id:"4b8e2c9d1f7a3e05",user:"admin",role:"owner",ip:"198.51.100.77",
       device:"Safari · iOS",ua:"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 Safari/604.1",
       created:"2026-10-30 21:02:44",last_seen:"2026-10-30 23:18:09",expires:"2026-10-31 21:02:44",current:false,revocable:true},
      {id:"c1d4a6e8b03f5927",user:"ops-lin",role:"admin",ip:"45.9.9.9",
       device:"脚本 · Python",ua:"python-requests/2.32.3",
       created:"2026-10-31 04:07:11",last_seen:"2026-10-31 04:07:11",expires:"2026-11-01 04:07:11",current:false,revocable:true}
    ]
  };
}
async function mockRoute(path, opts) {
  await new Promise(r => setTimeout(r, 220));
  const m = opts.method || "GET";
  // 注意 (.*)? 在没匹配到内容时给的是 null 而不是 ""：
  // 旧写法 `const [,api,rest] = ...` 会让 /api/inbounds 这类无子路径的请求
  // 拿到 rest === null，接着 rest.includes(...) 抛 TypeError。
  // 因为调用方几乎都 `catch(e){}`，症状是「演示页某块永远空白」，很难查。
  const mt = path.match(/\/api\/([^/?]*)(.*)?/) || [];
  const api = mt[1] || "", rest = mt[2] || "";
  const q = Object.fromEntries(new URLSearchParams(path.split("?")[1] || ""));
  if (api === "login") {
    // 演示 TOTP 两步登录：任意账密 → 返回 need_2fa，输入 123456 放行
    if (!opts.body?.code) return { need_2fa: "totp" };
    if (opts.body.code !== "123456") throw new Error("动态口令不正确（演示模式请输入 123456）");
    return { token: "demo", user:"admin", role:"owner", last_ip: "203.0.113.7", last_time: "2026-10-31 09:24:17" };
  }
  if (api === "me") {
    if (rest.includes("/password")) {
      if (!opts.body?.new || opts.body.new.length < 8) throw new Error("密码至少 8 位");
      return { ok: 1, relogin: true };
    }
    return { user:"admin", role:"owner", last_ip:"203.0.113.7", last_login:"2026-10-31 09:24:17", must_change:false, has_totp:true };
  }
  if (api === "admins") {
    if (m === "GET") return MDB.admins;
    if (m === "POST") {
      const b = opts.body || {};
      if (!b.username) throw new Error("用户名不能为空");
      if (MDB.admins.some(a => a.username === b.username)) throw new Error("用户名已存在");
      const a = { id: Math.max(0, ...MDB.admins.map(x => x.id)) + 1, username: b.username,
        role: b.role || "admin", enabled: true, has_totp: false, last_ip: "", last_login: "",
        created_at: new Date().toISOString().slice(0, 19).replace("T", " ") };
      MDB.admins.push(a);
      return { ok: 1, admin: a, password: b.password || "DemoGen3ratedPass" };
    }
    const id = +rest.split("/")[1];
    const a = MDB.admins.find(x => x.id === id);
    if (m === "PUT") {
      if (!a) throw new Error("管理员不存在");
      if (a.role === "owner" && (opts.body.role || opts.body.enabled === false)) throw new Error("超级管理员不能降级或停用");
      Object.assign(a, opts.body || {});
      return { ok: 1, admin: a };
    }
    if (m === "DELETE") {
      if (!a) throw new Error("管理员不存在");
      if (a.role === "owner") throw new Error("超级管理员不能删除");
      MDB.admins = MDB.admins.filter(x => x.id !== id);
      return { ok: 1 };
    }
  }
  if (api === "auth2fa") {
    if (m === "GET") return { mode:"off", totp_ready:false, email:"admin@example.com", smtp_ready:true };
    if (rest.includes("totp/setup")) return { secret:"JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP", uri:"otpauth://totp/TriunePanel:admin?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=TriunePanel&algorithm=SHA1&digits=6&period=30" };
    if (rest.includes("totp/enable")) { if (opts.body?.code !== "123456") throw new Error("动态口令不正确（演示模式请输入 123456）"); return { ok:"1" }; }
    if (rest.includes("email/test")) return { ok:"1" };
    if (rest.includes("email/enable")) { if (!opts.body?.email?.includes("@")) throw new Error("收件邮箱格式无效"); return { ok:"1" }; }
    if (rest.includes("disable")) return { ok:"1" };
    return {};
  }
  if (api === "login-history") {
    const t = n => new Date(Date.now() - n * 60000).toISOString().slice(0, 19).replace("T", " ");
    return { days: +q.days || 7, scanned: 11, truncated: false, items: [
      { user:"admin", total:4, ok:3, fail:1, ips:["203.0.113.7","198.51.100.77"],
        last_ok:t(12), last_fail:t(320), events:[
        {time:t(12), ip:"203.0.113.7", result:"ok", detail:"登录成功（已通过 totp 二次验证）"},
        {time:t(95), ip:"198.51.100.77", result:"ok", detail:"登录成功（已通过 totp 二次验证）"},
        {time:t(320), ip:"45.9.9.9", result:"fail", detail:"用户名或密码错误"},
        {time:t(1440), ip:"203.0.113.7", result:"ok", detail:"登录成功"}] },
      { user:"ops-lin", total:2, ok:2, fail:0, ips:["198.51.100.23"],
        last_ok:t(600), last_fail:"", events:[
        {time:t(600), ip:"198.51.100.23", result:"ok", detail:"登录成功"},
        {time:t(1500), ip:"198.51.100.23", result:"ok", detail:"登录成功"}] },
      { user:"root", total:5, ok:0, fail:5, ips:[],
        last_ok:"", last_fail:t(3), events:[
        {time:t(3), ip:"45.9.9.9", result:"fail", detail:"用户名或密码错误"},
        {time:t(4), ip:"45.9.9.9", result:"fail", detail:"用户名或密码错误"},
        {time:t(5), ip:"45.9.9.9", result:"fail", detail:"来源处于锁定期，拒绝登录（剩余约 10 分钟）"}] }
    ]};
  }
  if (api === "sessions") {
    if (m === "GET") return MDB.sessions;
    const sid = (rest || "").split("/")[1];
    if (m === "DELETE" && sid) {
      const s = MDB.sessions.find(x => x.id === sid);
      if (!s) throw new Error("会话不存在或已失效");
      if (s.current) throw new Error("演示模式不踢自己（会直接掉线）");
      MDB.sessions = MDB.sessions.filter(x => x.id !== sid);
      return { ok: 1 };
    }
    if (m === "DELETE") {
      const before = MDB.sessions.length;
      MDB.sessions = MDB.sessions.filter(x => x.current || x.user !== "admin");
      return { ok: 1, revoked: before - MDB.sessions.length };
    }
  }
  if (api === "version") return { version: MDB.version, binary_version: MDB.version, restart_failed: "" };
  if (api === "status") return { cores: ["singbox","xray","mihomo"].map(c => ({
    core:c, running:c!=="mihomo", config_exists:c!=="mihomo", bin_set:!!MDB.settings["bin_"+c], bin_found:!!MDB.settings["bin_"+c] })) };
  if (api === "inbounds" && rest.includes("/check")) {
    const i = MDB.inbounds.find(x => x.id === +rest.split("/")[1]);
    const checks = [
      { name:"配置状态", ok: i.enabled && !i.draft, detail: i.draft ? "仍是导入草稿，未参与配置渲染" : i.enabled ? "已启用并参与配置渲染" : "入站已停用" },
      { name:"内核进程", ok: i.core !== "mihomo", detail: i.core === "mihomo" ? "mihomo 未运行 —— 请先应用配置或安装内核" : i.core + " 正在运行" },
      { name:"端口监听", ok: i.enabled, detail: (i.transport === "udp" ? "UDP :" : "TCP 127.0.0.1:") + i.port + (i.enabled ? " 可连接" : " 无监听 —— 内核未运行或未启用") },
      { name:"公网可达", ok: i.enabled && i.core !== "mihomo", detail: i.enabled ? "demo.example.com:" + i.port + " 可达（演示）" : "请检查防火墙/安全组是否放行 " + i.port }
    ];
    return { inbound: i, checks, all_ok: checks.every(c => c.ok) };
  }
  // 演示模式：JSON → 表单解析（真机由后端 /api/inbounds/parse-json 完成）
  if (api === "inbounds" && rest === "/parse-json") return demoFillInbound(String(opts.body?.text || ""), opts.body?.core || "xray");
  // 演示模式：出站 / 路由 / DNS 的只读视图（真机由后端渲染当前配置得到）
  if (api === "outbounds" && m === "GET") return demoOutbounds(q.core || "xray");
  if (api === "inbounds" && m === "GET") return MDB.inbounds;
  if (api === "inbounds" && m === "POST") { const b = Object.assign({}, opts.body); b.id = Math.max(0,...MDB.inbounds.map(x=>x.id))+1; b.enabled=false; b.draft=false; MDB.inbounds.push(b); return b; }
  if (api === "inbounds" && m === "PUT") { const id=+rest.split("/")[1]; const i=MDB.inbounds.findIndex(x=>x.id===id); MDB.inbounds[i]=Object.assign({},MDB.inbounds[i],opts.body,{id}); return MDB.inbounds[i]; }
  if (api === "inbounds" && m === "DELETE") { MDB.inbounds = MDB.inbounds.filter(x=>x.id!==+rest.split("/")[1]); return {ok:1}; }
  if (api === "clients" && m === "GET") return MDB.clients;
  if (api === "clients" && m === "POST" && rest.includes("/reset-token")) {
    const e = MDB.clients.find(x => x.client.id === +rest.split("/")[1]);
    e.client.sub_token = "demo" + Math.random().toString(16).slice(2, 10);
    return { ok: 1, sub_token: e.client.sub_token, msg: "订阅令牌已重置（演示）" };
  }
  if (api === "clients" && m === "POST") { const b=opts.body; const c={id:Math.max(0,...MDB.clients.map(x=>x.client.id))+1,name:b.name,credential:b.credential||crypto.randomUUID(),sub_token:Math.random().toString(16).slice(2,14),quota_gb:b.quota_gb||0,expires_at:b.expires_at||null,enabled:true}; MDB.clients.push({client:c,inbound_ids:b.inbound_ids||[]}); return c; }
  if (api === "clients" && m === "PUT") { const id=+rest.split("/")[1]; const e=MDB.clients.find(x=>x.client.id===id); if(rest.includes("/inbounds")){e.inbound_ids=opts.body.inbound_ids;return{ok:1}} e.client=Object.assign({},e.client,opts.body,{id}); return e.client; }
  if (api === "clients" && m === "DELETE") { MDB.clients=MDB.clients.filter(x=>x.client.id!==+rest.split("/")[1]); return {ok:1}; }
  if (api === "traffic") return MDB.clients.map(({client:c}) => { const t=MDB.counters[c.id]||{up:0,down:0};
    const total=t.up+t.down, quota=c.quota_gb*1073741824, pct=quota>0?total/quota*100:0;
    return {id:c.id,name:c.name,up:t.up,down:t.down,total,quota_gb:c.quota_gb,enabled:c.enabled,
      exceeded:quota>0&&total>=quota, warn:quota>0&&total<quota&&pct>=80, pct}; });
  if (api === "traffic/daily") { const n=+q.days||30; return MDB.daily.slice(-n); }
  if (api === "traffic/live") return {up_bps:Math.round(2.1e5+Math.random()*9e4), down_bps:Math.round(1.9e6+Math.random()*8e5), interval:30, updated_at:new Date().toISOString().slice(0,19)};
  if (api === "revisions") return MDB.revisions;
  if (api === "logs") return {logs:["2026-10-31 03:40:11 INFO sing-box started (1.9.7)","2026-10-31 03:40:11 INFO inbound/hk-01-reality: tcp://:443 started","2026-10-31 03:40:12 INFO inbound/jp-01-ws: ws://:8443 started"]};
  if (api === "notify" && rest.includes("/test")) {
    const ch = [];
    if ((MDB.settings.notify_email || "").trim()) ch.push("邮件 → " + MDB.settings.notify_email);
    if ((MDB.settings.notify_webhook || "").trim()) ch.push("Webhook → " + MDB.settings.notify_webhook);
    if (!ch.length) return { ok: false, errors: ["还没配置任何通道：填收件邮箱或 Webhook 地址后先保存"] };
    return { ok: true, channels: ch };
  }
  if (api === "settings" && m === "GET") return MDB.settings;
  if (api === "settings" && m === "PUT") { Object.assign(MDB.settings, opts.body); return {ok:1}; }
  if (api === "backups" && rest === "/inventory") {
    const inv = MDB.backupInventory, st = MDB.settings;
    inv.policy = { keep_count:+st.backup_keep_count || 0, max_mb:+st.backup_max_mb || 0,
      max_days:+st.backup_max_days || 0, auto_prune:st.backup_auto_prune === "1" };
    // 演示版按天数预演，并守住「每组最新一份」这条硬底线
    const newest = {};
    inv.entries.forEach(e => {
      const g = e.category + "/" + e.group;
      if (!newest[g] || e.mod_time > newest[g].mod_time) newest[g] = e;
    });
    const p = inv.policy;
    inv.planned = inv.entries.filter(e => newest[e.category + "/" + e.group] !== e &&
      ((p.max_days > 0 && e.age_days > p.max_days) || p.keep_count > 0 || p.max_mb > 0));
    return inv;
  }
  if (api === "backups" && rest === "/prune") {
    const inv = MDB.backupInventory, b = opts.body || {}, keys = b.keys || [];
    const planned = keys.length ? inv.entries.filter(e => keys.includes(e.key)) : (inv.planned || []);
    if (b.dry_run || !b.confirm) return { dry_run:true, planned, failed:[] };
    const gone = new Set(planned.map(e => e.key));
    const removed = inv.entries.filter(e => gone.has(e.key));
    inv.entries = inv.entries.filter(e => !gone.has(e.key));
    inv.summary.count = inv.entries.length;
    inv.summary.total_bytes = inv.entries.reduce((s, e) => s + e.size, 0);
    inv.planned = [];
    return { dry_run:false, removed, failed:[],
      freed:removed.reduce((s, e) => s + e.size, 0),
      summary:inv.summary, planned:[], policy:inv.policy };
  }
  if (api === "host-opt" && m === "GET") return MDB.hostOpt;
  if (api === "host-opt" && rest === "/apply") {
    if (opts.body?.confirm !== "APPLY NET") throw new Error("需要显式确认");
    const t = opts.body?.tune || MDB.hostOpt.tune;
    const h = MDB.hostOpt;
    h.applied = true;
    h.tune = { ...h.tune, ...t };
    h.current_cc = t.cc;
    h.default_qdisc = t.qdisc;
    h.expected = { ...t };
    h.checks = h.checks.filter(c => c.name !== "面板写入的参数已生效").concat(
      [{ name:"面板写入的参数已生效", ok:true, warn:false, detail:"六个参数的实测值与面板写入的一致" }]);
    return h;
  }
  if (api === "host-opt" && rest === "/revert") {
    if (opts.body?.confirm !== "REVERT NET") throw new Error("需要显式确认");
    const h = MDB.hostOpt;
    h.applied = false;
    h.expected = null;
    h.tune = { cc:"cubic", qdisc:"pfifo_fast", rmem_max:212992, wmem_max:212992, somaxconn:4096, port_start:32768, port_end:60999 };
    h.current_cc = "cubic";
    h.default_qdisc = "pfifo_fast";
    h.checks = h.checks.filter(c => c.name !== "面板写入的参数已生效");
    return h;
  }
  // 预检必须排在 apply 前面：api 只取到 "apply"，rest 才是 "/dry-run"，
  // 先命中下面那条就会返回「应用成功」的结构，弹窗里显示 undefined。
  if (api === "apply" && rest === "/dry-run") return {content:'{\n  "log": { "level": "info", "timestamp": true },\n  "inbounds": [\n    { "type": "vless", "tag": "hk-01-reality", "listen": "::", "listen_port": 443, "users": [ ... ] }\n  ],\n  "outbounds": [ { "type": "direct", "tag": "direct" } ],\n  "experimental": { "clash_api": { "external_controller": "127.0.0.1:9090" } }\n}',result:{validated:true}};
  if (api === "apply") { MDB.revisions.unshift({id:++REV_N,core:opts.body.core,status:"applied",note:opts.body.note||"demo apply",created_at:new Date().toISOString().slice(0,19),content:"(修订内容示例)"}); return {revision_id:REV_N,core:opts.body.core,validated:true,restarted:true}; }
  if (api === "rollback") return {restored_from:"revisions/singbox/config.r3.bak"};
  if (api === "switch-core") {
    const b = opts.body;
    if (b.dry_run) return {to:b.to, validated:true, hash:"d3m0abcd", content:'{\n  "log": { "level": "info", "timestamp": true },\n  "inbounds": [\n    { "type": "vless", "tag": "hk-01-reality", "listen": "::", "listen_port": 443, "users": [ ... ] }\n  ],\n  "outbounds": [ { "type": "direct", "tag": "direct" } ]\n}'};
    const moved = [];
    (b.inbound_ids||[]).forEach(id => { const i = MDB.inbounds.find(x=>x.id===id); if (i) { moved.push(i.name); i.core = b.to; } });
    MDB.revisions.unshift({id:++REV_N,core:b.to,status:"applied",note:"switch → "+b.to,created_at:new Date().toISOString().slice(0,19),content:"(修订内容示例)"});
    return {ok:1, to:b.to, from:["(原内核)"], inbounds:moved, validated:true, hash:"d3m0abcd", revision_id:REV_N, warnings:[]};
  }
  if (api === "tools" && rest === "/reality-keypair") return {private_key:"demo-priv-"+Math.random().toString(16).slice(2,10),public_key:"demo-pub-"+Math.random().toString(16).slice(2,10)};
  if (api === "tools" && rest === "/reality-dest") {
    // 演示数据里特意留一个「证书不符」和一个「连不上」，好让人看见三种结论的样子
    await new Promise(r => setTimeout(r, 600));
    const cs = (opts.body && opts.body.candidates) || [];
    const results = cs.map((c, i) => i === 3
      ? { host:c.host, port:c.port, sni:c.host, ok:false, ms:0, tls:"", alpn:"", h2:false,
          cert_ok:false, issuer:"", not_after:"", days_left:0,
          detail:"连接超时（演示数据）", warning:"换一个目标试试" }
      : { host:c.host, port:c.port, sni:c.host, ok:true, ms:38 + i*27, tls:"1.3", alpn:"h2",
          h2:true, cert_ok:i !== 2, issuer:"R11", not_after:"2027-02-01", days_left:88,
          detail:"TLS 1.3 握手成功（演示数据）",
          warning: i === 2 ? "证书与 SNI 对不上（演示数据）" : "" });
    results.sort((a,b) => (b.ok-a.ok) || (b.cert_ok-a.cert_ok) || (a.ms-b.ms));
    const best = results.find(r => r.ok && r.cert_ok);
    if (best) best.best = true;
    return {results, best: best ? best.host + ":" + best.port : "", timeout: 14000};
  }
  if (api === "tools" && rest === "/uuid") return {uuid:crypto.randomUUID()};
  if (api === "import") {
    if ((opts.body.mode||"external") === "external") {
      MDB.external.push({id:Math.max(0,...MDB.external.map(x=>x.id))+1, name:"新导入节点", raw_link:"vless://demo@node"+Math.ceil(Math.random()*99)+".example.com:443#新导入节点", created_at:new Date().toISOString().slice(0,19)});
      return {created:["新导入节点 → 外部节点（仅订阅聚合）"],failed:[],parse_errors:[]};
    }
    return {created:["新导入节点 → 入站草稿 id=99（未生效，需 apply）"],failed:[],parse_errors:[]};
  }
  if (api === "config" && rest === "/diff") {
    const d = (opts.body || {}).core || "singbox";
    return { core: d, old_missing: false, changed: 6, new_hash: "d3m0abcd", diff: [
      { op: " ", text: '{ "log": { "level": "info", "timestamp": true },' },
      { op: " ", text: '  "inbounds": [' },
      { op: "-", text: '    { "type": "vless", "tag": "hk-01-reality", "listen_port": 443, "users": [ { "name": "alice" } ] }' },
      { op: "+", text: '    { "type": "vless", "tag": "hk-01-reality", "listen_port": 443, "users": [ { "name": "alice" }, { "name": "bob" } ] }' },
      { op: "+", text: '    { "type": "hysteria2", "tag": "sg-01-hy2", "listen_port": 36712, "users": [ { "name": "carol" } ] }' },
      { op: " ", text: '  ],' },
      { op: "-", text: '  "outbounds": [ { "type": "direct", "tag": "direct" } ]' },
      { op: "+", text: '  "outbounds": [ { "type": "direct", "tag": "direct" }, { "type": "block", "tag": "block" } ]' }
    ] };
  }
  if (api === "external-nodes" && rest === "/import-url") {
    const p = opts.body?.prefix ? opts.body.prefix + "-" : "";
    const now = new Date().toISOString().slice(0, 19);
    const raw = "vless://demo@node01.example.org:443#" + p + "远端订阅节点-01";
    MDB.external.push({ id: Math.max(0, ...MDB.external.map(x => x.id)) + 1, name: p + "远端订阅节点-01", raw_link: raw, created_at: now, parsed: demoExtParsed(raw) });
    return { created: [p + "远端订阅节点-01（vless）"], skipped: [p + "远端订阅节点-01（这条链接已存在）"], failed: [], parse_errors: [] };
  }
  if (api === "external-nodes" && m === "GET") return MDB.external;
  // 演示模式：改名 / 换链接（真机由后端解析并要求链接合法、不与别的节点重复）
  if (api === "external-nodes" && m === "PUT") {
    const id = +rest.split("/")[1];
    const it = MDB.external.find(x => x.id === id);
    if (!it) throw new Error("外部节点不存在");
    if (opts.body?.name !== undefined) {
      if (!String(opts.body.name).trim()) throw new Error("名称不能为空");
      it.name = String(opts.body.name).trim();
    }
    if (opts.body?.raw_link !== undefined) {
      const raw = String(opts.body.raw_link).trim();
      const p = demoExtParsed(raw);
      if (!p.ok) throw new Error("链接无法解析: " + p.error);
      it.raw_link = raw; it.parsed = p;
    }
    return it;
  }
  if (api === "external-nodes" && m === "DELETE") { MDB.external = MDB.external.filter(x=>x.id!==+rest.split("/")[1]); return {ok:1}; }
  if (api === "certs") {
    // 演示两条：一条健康、一条仅剩 9 天，便于看到到期标红效果
    const day = n => new Date(Date.now() + n * 86400000).toISOString();
    return [
      { domain:"panel.example.com", issuer:"R11", not_before:day(-30), not_after:day(62),
        file:"data/certs/panel.example.com/fullchain.pem" },
      { domain:"node.example.com", issuer:"R11", not_before:day(-81), not_after:day(9),
        file:"data/certs/node.example.com/fullchain.pem" }
    ];
  }
  if (api === "certs/issue") return {ok:1, msg:"已触发签发检查（演示）"};
  if (api === "audit" && m === "DELETE") return {ok:1};
  if (api === "audit") {
    const t = n => new Date(Date.now() - n * 60000).toISOString().slice(0, 19).replace("T", " ");
    const ip = "203.0.113.24";
    return [
      { id:9, time:t(1),   actor:"admin", ip, action:"login",             target:"admin",           result:"ok",   detail:"登录成功（已通过 totp 二次验证）" },
      { id:8, time:t(8),   actor:"admin", ip, action:"apply",             target:"singbox",         result:"ok",   detail:"应用成功，修订 #37" },
      { id:7, time:t(26),  actor:"admin", ip, action:"inbound.update",    target:"香港-01 · VLESS", result:"ok",   detail:"vless/reality 端口 443" },
      { id:6, time:t(41),  actor:"admin", ip, action:"client.create",     target:"张三",            result:"ok",   detail:"新建用户 #5，绑定 2 个入站" },
      { id:5, time:t(95),  actor:"admin", ip, action:"settings.update",   target:"quota_warn_pct,stats_enabled", result:"ok", detail:"修改设置" },
      { id:4, time:t(140), actor:"admin", ip, action:"client.reset-token",target:"李四",            result:"ok",   detail:"重置用户 #4 的订阅令牌（旧链接失效）" },
      { id:3, time:t(210), actor:"admin", ip, action:"rollback",          target:"xray",            result:"ok",   detail:"回滚到备份 20261031-101500.bak" },
      { id:2, time:t(320), actor:"admin", ip:"45.9.9.9", action:"login",  target:"admin",           result:"fail", detail:"用户名或密码错误" },
      { id:1, time:t(325), actor:"admin", ip:"45.9.9.9", action:"login",  target:"",                result:"fail", detail:"来源处于锁定期，拒绝登录（剩余约 10 分钟）" }
    ];
  }
  if (api === "components") return {
    panel:{component:"panel",version:"v0.5.0-demo"},
    cores:[
      {component:"singbox",bin:"/usr/local/bin/sing-box",bin_set:true,version:"sing-box version 1.9.7",running:true,config_exists:true,backups:["1.9.5-20261001-120000.bak"]},
      {component:"xray",bin:"/usr/local/bin/xray",bin_set:true,version:"Xray 24.12.18 (Xray, Penetrates Everything.)",running:false,config_exists:true,backups:[]},
      {component:"mihomo",bin:"",bin_set:false,running:false,backups:[]}
    ]};
  if (api === "mirrors") {
    const ms = [
      {id:"official",name:"官方地址",base:"",note:"github.com 直连，默认选项"},
      {id:"ghfast",name:"ghfast.top",base:"https://ghfast.top",note:"老牌，速度稳定"},
      {id:"ghproxy-net",name:"ghproxy.net",base:"https://ghproxy.net"},
      {id:"gh-proxy",name:"gh-proxy.com",base:"https://gh-proxy.com"},
      {id:"llkk",name:"gh.llkk.cc",base:"https://gh.llkk.cc"},
      {id:"moeyy",name:"github.moeyy.xyz",base:"https://github.moeyy.xyz"}
    ];
    if (rest.startsWith("/test")) {
      await new Promise(r=>setTimeout(r,700)); // 演示里也模拟一下测速耗时
      const lat = {"":1280,"https://ghfast.top":186,"https://ghproxy.net":412,
                   "https://gh-proxy.com":0,"https://gh.llkk.cc":523,"https://github.moeyy.xyz":0};
      const results = ms.map(m => lat[m.base]
        ? {...m, ok:true, ms:lat[m.base]}
        : {...m, ok:false, ms:0, error:"timeout"}).sort((a,b)=>(b.ok-a.ok)||(a.ms-b.ms));
      return {results, best:results[0].base, ok_count:results.filter(r=>r.ok).length};
    }
    return {mirrors:ms, default:"", timeout_ms:6000};
  }
  if (api === "updates" && rest.startsWith("/versions")) {
    // 演示数据里特意放一条预发布，好让人看见「稳定版 / 预发布」是怎么分的
    const releases = [
      {tag:"v1.11.0-alpha.2",prerelease:true,notes:"## 预发布\n- 实验性 **Hysteria3** 支持\n- 接口可能有变动，不建议生产使用"},
      {tag:"v1.10.0",notes:"## 新功能\n- 支持 **Hysteria2** 混淆参数 `obfs`\n- 修复高并发下的内存占用\n\n### 修复\n- WS 路径含查询串时握手失败"},
      {tag:"v1.9.7",notes:"稳定性修复版本。\n\n- 修复 TLS 证书热加载\n- 提升日志可读性"},
      {tag:"v1.9.5"},{tag:"v1.8.9"}];
    return {component:q.component,source:"github",channel:"all",
      counts:{all:releases.length,stable:releases.filter(r=>!r.prerelease).length,prerelease:releases.filter(r=>r.prerelease).length},
      releases};
  }
  if (api.startsWith("cores/") && api.endsWith("/update")) { await new Promise(r=>setTimeout(r,900)); const c=api.split("/")[1];
    const installed = !MDB.settings["bin_"+c];
    if (installed) MDB.settings["bin_"+c] = "/opt/triune-panel/data/bin/"+c+"/"+(c==="singbox"?"sing-box":c);
    return {ok:1,component:c,version:c+" version 9.9.9 (demo)",sha256:"demo",backup:"20261031-053500.bak",target:MDB.settings["bin_"+c],installed,restarted:!installed,was_running:!installed}; }
  if (api.startsWith("cores/") && api.endsWith("/rollback")) { const c=api.split("/")[1]; return {ok:1,restored:"1.9.5-20261001-120000.bak",component:c}; }
  if (api === "panel/backups") return {current_version:"v0.5.0-demo",executable:"/opt/triune-panel/triune-panel",backups:["v0.4.0-20261015-090000.bak","v0.3.0-20261002-180000.bak"],bak_exists:true};
  if (api === "panel/rollback") { await new Promise(r=>setTimeout(r,600)); return {ok:1,msg:"回滚完成，正在重启..."}; }
  return {};
}

/* 演示模式下的出站视图数据（仅 DEMO 生效）。
   真机上这些字段来自后端把当前入站渲染成配置后抽出来的结果
   （internal/api/outbounds.go），演示页没有后端，就用一份结构一致的样例。 */
function demoOutbounds(core) {
  const common = "服务端出站固定为 direct（freedom）：入站流量只能从本机出去，面板不提供上游代理 / 链式代理。";
  if (core === "mihomo") return {
    core, inbound_count: 3,
    outbounds: [{ tag: "DIRECT", type: "direct（隐式）", source: "builtin",
      note: "服务端模式不写 proxies，未匹配规则的流量直接走本机网络" }],
    route_rules: [], dns: null, dns_imported: false, route_imported: false,
    notes: ["mihomo 用 listeners 做服务端，不写 proxies：所有入站流量直接走本机网络出口。",
      "没有任何路由规则：全部流量直接出网。",
      "没有导入 DNS 段，域名解析走内核默认（系统 DNS）。"],
  };
  if (core === "singbox") return {
    core, inbound_count: 3,
    outbounds: [{ tag: "direct", type: "direct", source: "builtin" }],
    route_rules: [{ index: 1, source: "imported", outbound: "action=reject",
      summary: "domain_suffix=ads.example.com → action=reject",
      raw: { domain_suffix: ["ads.example.com"], action: "reject" } }],
    dns: { servers: [{ tag: "dns-remote", address: "https://1.1.1.1/dns-query" }] },
    dns_imported: true, route_imported: true,
    notes: [common,
      "路由规则来自导入的 3x-ui / xray 配置，追加在面板统计规则之后——统计规则必须最前，否则读不到流量计数。"],
  };
  return {
    core, inbound_count: 3,
    outbounds: [
      { tag: "direct", type: "freedom", source: "builtin" },
      { tag: "block", type: "blackhole", source: "builtin" },
    ],
    route_rules: [
      { index: 1, source: "panel", outbound: "api", summary: "inboundTag=api → api",
        raw: { type: "field", inboundTag: ["api"], outboundTag: "api" } },
      { index: 2, source: "imported", outbound: "block",
        summary: "domain=ads.example.com,tracker.example.net → block",
        raw: { type: "field", domain: ["ads.example.com", "tracker.example.net"], outboundTag: "block" } },
    ],
    dns: { servers: ["1.1.1.1", "8.8.8.8"] },
    dns_imported: true, route_imported: true,
    notes: [common,
      "路由规则来自导入的 3x-ui / xray 配置，追加在面板统计规则之后——统计规则必须最前，否则读不到流量计数。",
      "出站多于一条，说明导入的路由里有 block 规则，面板为此补了 blackhole 出站。"],
  };
}

/* 演示模式下的「JSON → 入站表单」解析（仅 DEMO 生效）。
   实际部署时这一步由后端 /api/inbounds/parse-json 完成（internal/importer）；
   演示页没有后端，这里放一份精简实现，保证 demo.html 里「粘贴 → 填入」
   这条路也能走通。两边字段名保持一致，别单独改其中一边。 */
function demoFillInbound(text, core) {
  const asObj = v => {
    if (!v) return {};
    if (typeof v === "object") return v;
    try { return JSON.parse(v) || {}; } catch(e) { return {}; }
  };
  const pick = (...vs) => { for (const v of vs) { const o = asObj(v); if (Object.keys(o).length) return o; } return {}; };
  const rnd = n => Array.from({length:n}, () => Math.floor(Math.random()*16).toString(16)).join("");
  const uuid = () => (crypto && crypto.randomUUID ? crypto.randomUUID() : "demo-" + rnd(8) + "-" + rnd(12));

  let root;
  try { root = JSON.parse(text); } catch(e) { throw new Error("不是合法 JSON：" + e.message); }
  const arr = Array.isArray(root) ? root
    : (root && Array.isArray(root.inbounds)) ? root.inbounds
    : (root && Array.isArray(root.data)) ? root.data
    : (root && root.protocol) ? [root] : null;
  if (!arr || !arr.length) throw new Error("没有解析到入站（根节点既不是数组，也没有 inbounds 字段）");

  let globalIgnored = "";
  if (root && (root.dns || root.routing)) {
    const dn = root.dns && Array.isArray(root.dns.servers) ? root.dns.servers.length : 0;
    const rt = root.routing && Array.isArray(root.routing.rules) ? root.routing.rules.length : 0;
    const bits = [];
    if (dn) bits.push(dn + " 个 DNS 服务器");
    if (rt) bits.push(rt + " 条路由规则");
    if (bits.length) globalIgnored = "JSON 里还含 " + bits.join("、") + "，这里只填单条入站，全局段未导入（需要的话用批量导入接口）";
  }

  const KNOWN = ["vless","vmess","trojan","shadowsocks","socks","http","dokodemo-door","tunnel","wireguard"];
  const items = arr.map(m => {
    const st = asObj(m.streamSettings), se = asObj(m.settings);
    const warns = [];
    const proto0 = String(m.protocol || "").toLowerCase();
    const proto = proto0 === "tunnel" ? "dokodemo-door" : proto0;
    if (proto0 === "tunnel") warns.push("tunnel 协议已按 dokodemo-door（端口转发）填入");
    const name = String(m.remark || m.tag || (proto0 + "-" + m.port));
    const base = { name, protocol: proto, listen: String(m.listen || ""), port: +m.port || 0,
      transport: "tcp", tls: "none", settings: {}, clients: [], warnings: warns, skip: false };
    if (!KNOWN.includes(proto0)) { base.skip = true; warns.push("协议 " + proto0 + " 面板不支持"); return base; }

    const trMap = {tcp:"tcp", raw:"tcp", ws:"ws", grpc:"grpc", h2:"h2", http:"h2",
      httpupgrade:"httpupgrade", xhttp:"xhttp", splithttp:"xhttp", kcp:"kcp", mkcp:"kcp", quic:"quic"};
    let transport = trMap[String(st.network || "tcp").toLowerCase()] || "tcp";
    const sec = String(st.security || "none").toLowerCase();
    const tls = sec === "reality" ? "reality" : (sec === "tls" || sec === "xtls") ? "tls" : "none";
    const settings = {};

    if (["ws","h2","httpupgrade","xhttp"].includes(transport)) {
      const blk = transport === "ws" ? asObj(st.wsSettings)
        : transport === "httpupgrade" ? asObj(st.httpupgradeSettings)
        : transport === "xhttp" ? pick(st.xhttpSettings, st.splithttpSettings)
        : pick(st.httpSettings, st.h2Settings);
      if (blk.path) settings.path = blk.path;
      const host = Array.isArray(blk.host) ? blk.host[0] : blk.host;
      if (host) settings.ws_host = host;
      if (transport === "xhttp" && blk.mode) settings.xhttp_mode = blk.mode;
    }
    if (transport === "grpc") { const g = asObj(st.grpcSettings); if (g.serviceName) settings.service_name = g.serviceName; }
    if (transport === "kcp") {
      const k = asObj(st.kcpSettings);
      if (k.seed) settings.kcp_seed = k.seed;
      const ht = asObj(k.header).type; if (ht) settings.kcp_header = ht;
    }
    if (transport === "quic") {
      const q = asObj(st.quicSettings);
      if (q.security) settings.quic_security = q.security;
      if (q.key) settings.quic_key = q.key;
      const ht = asObj(q.header).type; if (ht) settings.quic_header = ht;
    }
    // 目标内核适配（与后端 adaptToCore 同规则）
    if (core === "xray" && (transport === "h2" || transport === "quic")) {
      warns.push(transport === "h2"
        ? "Xray 26 起移除了 h2 传输，已转成 xhttp（mode=stream-one）；客户端需同步改，否则连不上"
        : "Xray 26 起移除了裸 QUIC 传输，已转成 xhttp（mode=stream-one）；要走 H3 请把 ALPN 设为 h3 并配好证书");
      transport = "xhttp"; settings.xhttp_mode = "stream-one";
    }
    if (core === "singbox" && (transport === "kcp" || transport === "xhttp")) {
      warns.push("sing-box 不支持 " + transport + " 传输，已按 tcp 填入；客户端需同步改传输");
      transport = "tcp";
    }

    if (tls === "tls") {
      const t = asObj(st.tlsSettings);
      if (t.serverName) settings.server_name = t.serverName;
      if (Array.isArray(t.alpn) && t.alpn.length) settings.alpn = t.alpn.join(",");
      const c0 = Array.isArray(t.certificates) ? t.certificates[0] : null;
      if (c0) { if (c0.certificateFile) settings.certificate_path = c0.certificateFile; if (c0.keyFile) settings.key_path = c0.keyFile; }
    }
    if (tls === "reality") {
      const r = asObj(st.realitySettings);
      const dest = String(r.dest || r.target || "");
      if (dest) {
        const p = dest.lastIndexOf(":");
        if (p > 0 && !dest.slice(p).includes("]")) {
          settings.handshake_server = dest.slice(0, p);
          const hp = +dest.slice(p+1); if (hp && hp !== 443) settings.handshake_port = hp;
        } else settings.handshake_server = dest;
      }
      const names = Array.isArray(r.serverNames) ? r.serverNames : (r.serverName ? [r.serverName] : []);
      if (names.length) settings.server_names = names.join(",");
      if (r.privateKey) settings.private_key = r.privateKey;
      if (Array.isArray(r.shortIds) && r.shortIds.length) settings.short_ids = r.shortIds.slice();
      if (r.spiderX) settings.spider_x = r.spiderX;
      if (+r.maxTimeDiff > 0) settings.max_time_diff = +r.maxTimeDiff;
      const pub = asObj(r.settings).publicKey; if (pub) settings.pbk = pub;
    }

    if (proto === "shadowsocks") {
      settings.method = se.method || "aes-256-gcm";
      if (se.network) settings.ss_network = se.network;
    }
    if (proto === "vless") {
      if (Array.isArray(se.fallbacks) && se.fallbacks.length) settings.fallbacks = se.fallbacks;
      const flows = Array.from(new Set((Array.isArray(se.clients) ? se.clients : []).map(c => c.flow).filter(Boolean)));
      if (flows.length === 1) settings.flow = flows[0];
    }
    if (proto === "socks" || proto === "http") {
      const a = String(se.auth || "").toLowerCase();
      if (a) settings.account_auth = (a === "none" || a === "noauth") ? "noauth" : "password";
      if (se.udp) settings.udp = true;
    }
    if (proto === "dokodemo-door") {
      if (se.address) settings.dd_address = se.address;
      if (+se.port > 0) settings.dd_port = +se.port;
      if (se.network) settings.dd_network = se.network;
      if (se.followRedirect) settings.dd_follow_redirect = true;
    }
    if (proto === "wireguard") {
      const raw = {}; Object.keys(se).forEach(k => { if (k !== "clients" && k !== "accounts") raw[k] = se[k]; });
      if (Object.keys(raw).length) settings.xray_raw_settings = raw;
    }

    // 用户：JSON 里有就照搬，没有就现生成一个
    let clients = [];
    const acc = (proto === "socks" || proto === "http") && Array.isArray(se.accounts) ? se.accounts : [];
    if (acc.length) {
      clients = acc.filter(a => a.user || a.pass).map(a => ({ name: a.user || "user", credential: (a.user || "") + ":" + (a.pass || ""), kind: "用户名:密码" }));
    } else if (Array.isArray(se.clients) && se.clients.some(c => c.id || c.password || c.user)) {
      clients = se.clients.filter(c => c.id || c.password || c.user).map((c, i) => ({
        name: c.email || c.name || (proto + "-user" + (i + 1)),
        credential: c.id || c.password || ((c.user || "") + ":" + (c.pass || "")),
        kind: c.id ? "UUID" : "密码",
        quota_gb: +c.totalGB > 0 ? +(+c.totalGB / 1073741824).toFixed(2) : 0
      }));
    } else if (se.password) {
      clients = [{ name: "imported", credential: se.password, kind: "密码", quota_gb: 0 }];
    }
    if (!clients.length) {
      if (proto === "vless" || proto === "vmess" || proto === "tuic")
        clients = [{ name: name + "-user1", credential: uuid(), kind: "UUID", auto: true, quota_gb: 0 }];
      else if (["trojan","shadowsocks","hysteria2","anytls"].includes(proto))
        clients = [{ name: name + "-user1", credential: rnd(24), kind: "密码", auto: true, quota_gb: 0 }];
      else if (settings.account_auth === "password")
        clients = [{ name: name + "-user1", credential: "u" + rnd(6) + ":" + rnd(24), kind: "用户名:密码", auto: true, quota_gb: 0 }];
    }

    return Object.assign(base, { transport, tls, settings, clients });
  });

  return { core, source: "3x-ui", items, global_ignored: globalIgnored };
}

/* ================= 登录 ================= */
if (DEMO) $("demoTag").classList.remove("hide");
/* boot.js 在 <head> 里按本地 token 决定了首屏画谁（见 index.html 的
   html[data-boot] 规则）。这里一旦真正接手显隐，就撤掉那个标记：否则 CSS 的
   !important 会一直压着 .hide——会话过期想回登录页都回不去。 */
function clearBootFlag() {
  const h = document.documentElement;
  h.removeAttribute("data-boot");
  h.removeAttribute("data-boot-view");
  // 首屏分组规则是 !important 的，留着会一直压住用户之后切组。
  h.removeAttribute("data-boot-sect");
}
function showLogin(){ clearBootFlag(); $("login").classList.remove("hide"); $("app").classList.add("hide"); }
/* 恢复上次停留的页面。boot.js 已在首帧用 data-boot-view 把目标页画出来了，
   这里补齐标题、导航高亮和该页的数据加载，最后才撤标记。
   顺序很关键：先 clearBootFlag() 就会露出一帧 HTML 默认激活的概览页，
   「刷新后跳回主页」等于没修。goto() 内部已经 loadAll()，showApp() 不再
   单独调一次，避免同一批接口拉两遍。 */
function restoreView() {
  let v = "";
  try { v = localStorage.getItem("triune_view") || ""; } catch (_) {}
  // 旧版本残留 / 手改的非法值退回概览，别让 TITLES[v] 变成 undefined
  if (!VIEWS.some(([k]) => k === v)) v = "overview";
  // 先把 .active 摆到与首屏一致的位置：撤掉 data-boot-view 时不会露出
  // HTML 默认激活的概览页。animate=false 让 goto() 不加 .enter，首屏与
  // 撤标记之后都不会播 fadeUp（那会让整页从 opacity:0 淡入一下）。
  VIEWS.forEach(([k]) => $("v-" + k).classList.toggle("active", k === v));
  goto(v, undefined, false);
}
function showApp(){
  $("login").classList.add("hide");
  $("app").classList.remove("hide");
  restoreView();
  clearBootFlag();
}
/* 会话作废：清干净本地身份，否则每次刷新都会「先按 token 画主界面，再被 401
   打回登录页」，把一次性的过期变成每天都能看见的闪屏。 */
function dropSession() {
  TOKEN = "";
  try {
    localStorage.removeItem("triune_token");
    localStorage.removeItem("triune_role");
    localStorage.removeItem("triune_user");
  } catch (_) {}
  ROLE = ""; ME = { user: "", role: "" };
}

/* 二次验证：第一步提交账密，服务端返回 need_2fa 后展示验证码输入行 */
let LOGIN_2FA = ""; // "totp" | "email" | ""
function resetLoginUI() {
  LOGIN_2FA = "";
  $("l2fa_row").classList.add("hide");
  $("l2fa_resend").classList.add("hide");
  $("lcode").value = "";
  $("l2fa_hint").textContent = "";
}
async function login() {
  try {
    const body = { user:$("lu").value, pass:$("lp").value };
    if (LOGIN_2FA) body.code = $("lcode").value.trim();
    const j = await req("/api/login", { method:"POST", body });
    if (j.need_2fa) {
      LOGIN_2FA = j.need_2fa;
      $("l2fa_row").classList.remove("hide");
      $("l2fa_resend").classList.toggle("hide", j.need_2fa !== "email");
      $("l2fa_label").textContent = j.need_2fa === "email" ? "邮箱验证码" : "动态口令（验证器 App）";
      $("l2fa_hint").textContent = j.need_2fa === "email"
        ? (j.email ? `验证码已发送至 ${j.email}，5 分钟内有效` : "验证码已发送")
        : (DEMO ? "演示模式：输入 123456" : "请输入验证器 App 中的 6 位动态口令");
      $("lcode").focus();
      return;
    }
    TOKEN = j.token; localStorage.setItem("triune_token", TOKEN); resetLoginUI();
    // 记住身份：后续所有显隐与按钮可用性都靠它（真正的权限判定在服务端）
    ME = { user: j.user || "", role: j.role || "" };
    ROLE = ME.role;
    localStorage.setItem("triune_user", ME.user);
    localStorage.setItem("triune_role", ROLE);
    applyRole();
    // 上次登录来源提示（本次之前的那次），便于发现异常访问
    if (j.last_ip) {
      toast(`上次登录：${j.last_ip}${j.last_time ? " · " + j.last_time : ""}`);
      $("login_last").classList.remove("hide");
      $("login_last").textContent = ` · 上次登录 ${j.last_ip}`;
    }
    // 默认密码未改：强制改密后才能使用
    if (j.must_change) { $("mMustChange").classList.add("show"); syncBodyLock(); $("mc_pass").focus(); return; }
    showApp();
  } catch (e) {
    toast(String(e.message).includes("fetch")
      ? "无法连接面板服务：请确认面板已启动；离线演示请打开 demo.html"
      : e.message, "err");
  }
}
/* 首次登录强制改密 */
async function submitMustChange() {
  const p1 = $("mc_pass").value, p2 = $("mc_pass2").value;
  if (p1.length < 8) { toast("新密码至少 8 位", "err"); return; }
  if (p1 !== p2) { toast("两次输入的密码不一致", "err"); return; }
  try {
    // 首次设密码时原密码为空（服务端对「从未设过密码」的账号跳过原密码校验）
    const r = await req("/api/me/password", { method:"PUT", body:{ old:"", new:p1 } });
    $("mc_pass").value = ""; $("mc_pass2").value = "";
    closeModal("mMustChange");
    // 改密会让所有会话失效（含当前这个），必须回登录页重新登录
    if (r && r.relogin) { toast("密码已更新，请用新密码重新登录"); logout(); return; }
    toast("密码已更新，欢迎使用 Triune Panel");
    showApp();
  } catch (e) { toast("修改失败：" + e.message, "err"); }
}
/* 登录页版本号（/api/public/info 免认证，仅返回版本字符串） */
(async function loadLoginVer() {
  const el = $("login_ver");
  if (DEMO) { el.textContent = "Triune Panel · 演示版"; return; }
  try {
    const j = await req("/api/public/info");
    if (j.version) el.textContent = `Triune Panel ${j.version}`;
  } catch (e) {}
})();
/* 重发邮箱验证码：重新提交账密即可触发服务端重发 */
async function loginResend() {
  $("lcode").value = "";
  await login();
}
function logout(){
  // 先让服务端销毁会话，再清本地——否则 token 在剩余 24h 内仍然有效。
  // 演示模式没有后端，跳过。
  const t = TOKEN;
  if (t && !DEMO) fetch("api/logout", { method: "POST", headers: { Authorization: "Bearer " + t } }).catch(() => {});
  dropSession();
  resetLoginUI(); showLogin();
}

/* ================= 数据加载 ================= */
async function loadAll() {
  // 按角色决定拉什么：只读账号拉设置 / 配置生成会被 403，
  // 与其刷一串错误提示，不如根本不请求。用户列表例外——服务端会给脱敏副本。
  const jobs = [loadStatus(), loadInbounds(), loadClients(), loadTraffic(), loadRevisions(),
                loadVersion(), loadCerts(), loadComponents(), loadLive(), loadExternal(),
                loadOverview(), loadMe(), loadSessions(), loadLoginHistory()];
  if (!isViewer()) jobs.push(loadSettings(), loadGen(), loadAuth2FA());
  if (isOwner()) jobs.push(loadAdmins());
  await Promise.allSettled(jobs);
  markScrollableTables();   // 表格内容填进去之后才知道要不要横滑
  scrollNavToActive();      // 角色过滤后导航项可能变少，重新对齐
}
function fmtBytes(b) {
  if (!b) return "0";
  const u = ["B","KB","MB","GB","TB"]; let i = 0;
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return b.toFixed(i ? 2 : 0) + " " + u[i];
}

/* ================= 备份管理 =================
   四类旧版本文件（数据归档 / 面板 .bak / 内核 .bak / 配置修订 .bak）统一成一个
   视图。删除一律走后端的白名单复核：这里只回传 key，不拼路径、不自行判断
   哪些文件能删。策略输入框只在首次进入时回填，避免「刷新一下把没保存的编辑冲掉」。 */
const BK_CAT_LABEL = { data:"数据归档", panel:"面板版本", core:"内核版本", revision:"配置修订" };
let BK_POLICY_LOADED = false;

async function loadBackupInventory() {
  try {
    renderBackupInventory(await req("/api/backups/inventory"));
  } catch (e) {
    const el = $("bk_summary");
    if (el) el.textContent = "加载失败：" + e.message;
  }
}

function renderBackupInventory(j) {
  const s = j.summary || {}, p = j.policy || {}, cat = s.by_category || {};
  const catText = Object.keys(BK_CAT_LABEL)
    .map(k => `${BK_CAT_LABEL[k]} ${fmtBytes(cat[k] || 0)}`).join(" · ");
  $("bk_summary").innerHTML = `<b style="font-size:20px">${fmtBytes(s.total_bytes || 0)}</b>` +
    `<span class="muted"> / ${s.count || 0} 份</span>` +
    `<div style="margin-top:6px">${escHtml(catText)}</div>` +
    `<div class="muted" style="margin-top:6px">磁盘可用 ` +
    `${s.disk_free ? fmtBytes(s.disk_free) : "未知"}${s.disk_total ? " / 共 " + fmtBytes(s.disk_total) : ""}</div>`;
  $("bk_hint").textContent = s.hint || "";
  if (!BK_POLICY_LOADED) {
    $("bk_keep").value = p.keep_count ?? 0;
    $("bk_maxmb").value = p.max_mb ?? 0;
    $("bk_maxdays").value = p.max_days ?? 0;
    $("bk_auto").checked = !!p.auto_prune;
    BK_POLICY_LOADED = true;
  }
  $("bk_policy_hint").textContent = p.auto_prune
    ? "后台自动清理已开启（启动 10 分钟后首次检查，之后每 6 小时一次）"
    : "后台自动清理关闭：只在你点「按策略清理」时才删";
  renderBackupTable(j.entries || []);
  renderBackupPreview(j.planned || [], []);
}

function renderBackupTable(entries) {
  $("bk_body").innerHTML = entries.map(e => `<tr>
    <td><input type="checkbox" class="bk-sel" value="${escHtml(e.key)}"></td>
    <td>${escHtml(BK_CAT_LABEL[e.category] || e.category)}</td>
    <td class="mono">${escHtml(e.group)}</td>
    <td class="mono">${escHtml(e.name)}</td>
    <td>${fmtBytes(e.size)}</td>
    <td class="muted">${escHtml(String(e.mod_time || "").replace("T", " ").slice(0, 19))}</td>
    <td>${Number(e.age_days) || 0}</td>
    <td class="ta-r"><button class="btn sm danger" data-act="deleteOneBackup" data-args="${escJson([e.key])}">删除</button></td>
  </tr>`).join("") || `<tr><td colspan="8" class="muted">还没有面板生成的备份文件。</td></tr>`;
  markScrollableTables();
}

function renderBackupPreview(planned, failed) {
  const box = $("bk_preview");
  if (!box) return;
  planned = planned || []; failed = failed || [];
  if (!planned.length && !failed.length) {
    box.innerHTML = `<p class="muted">按当前策略没有需要清理的条目。</p>`;
    return;
  }
  const freed = planned.reduce((sum, e) => sum + (Number(e.size) || 0), 0);
  const shown = planned.slice(0, 8).map(e => escHtml(e.name)).join("、");
  box.innerHTML = `<div style="padding:10px 12px;border-radius:9px;font-size:13px;line-height:1.7;
      background:color-mix(in srgb,var(--warn) 10%,transparent);
      border:1px solid color-mix(in srgb,var(--warn) 35%,transparent)">
      <b>将清理 ${planned.length} 份，约 ${fmtBytes(freed)}</b>
      <div class="muted">${shown}${planned.length > 8 ? " …" : ""}</div></div>` +
    (failed.length ? `<div class="muted" style="margin-top:8px">${failed.map(x => escHtml(x)).join("；")}</div>` : "");
}

async function saveBackupPolicy() {
  const body = {
    backup_keep_count: String(Math.max(0, +$("bk_keep").value || 0)),
    backup_max_mb: String(Math.max(0, +$("bk_maxmb").value || 0)),
    backup_max_days: String(Math.max(0, +$("bk_maxdays").value || 0)),
    backup_auto_prune: $("bk_auto").checked ? "1" : "0"
  };
  try {
    await req("/api/settings", { method:"PUT", body });
    toast("保留策略已保存");
    BK_POLICY_LOADED = false;   // 回填成刚提交的服务器真值
    await loadBackupInventory();
  } catch (e) { toast(e.message, "err"); }
}

async function previewBackupPrune() {
  try {
    const j = await req("/api/backups/prune", { method:"POST", body:{ dry_run:true } });
    renderBackupPreview(j.planned || [], j.failed || []);
    toast(`按当前策略将清理 ${(j.planned || []).length} 份`, "info");
  } catch (e) { toast(e.message, "err"); }
}

async function runBackupPrune() {
  let pre;
  try { pre = await req("/api/backups/prune", { method:"POST", body:{ dry_run:true } }); }
  catch (e) { return toast(e.message, "err"); }
  const list = pre.planned || [];
  renderBackupPreview(list, pre.failed || []);
  if (!list.length) return toast("按当前策略没有要清理的条目", "info");
  const freed = list.reduce((sum, e) => sum + (Number(e.size) || 0), 0);
  if (!await ask("按策略清理备份",
    `将删除 ${list.length} 份旧备份，释放约 ${fmtBytes(freed)}。每个分类至少保留最新一份，删除后无法恢复。确定继续？`)) return;
  await doBackupPrune([]);
}

async function deleteSelectedBackups() {
  const keys = Array.from(document.querySelectorAll("#bk_body .bk-sel:checked")).map(x => x.value);
  if (!keys.length) return toast("请先勾选要删除的备份", "warn");
  if (!await ask("删除备份", `确定删除选中的 ${keys.length} 份备份？删除后无法恢复；每个分类的最新一份会被保留。`)) return;
  await doBackupPrune(keys);
}

async function deleteOneBackup(key) {
  if (!await ask("删除备份", `确定删除这一份备份？删除后无法恢复。\n${key}`)) return;
  await doBackupPrune([key]);
}

async function doBackupPrune(keys) {
  try {
    const j = await req("/api/backups/prune", { method:"POST", body:{ confirm:true, keys } });
    toast(`已清理 ${(j.removed || []).length} 份，释放 ${fmtBytes(j.freed || 0)}`);
    await loadBackupInventory();
    if ((j.failed || []).length) viewContent("部分条目未清理", (j.failed || []).join("\n"));
  } catch (e) { toast(e.message, "err"); }
}

/* ================= 主机优化（网络参数） =================
   体检只读；两个写动作必须回传后端约定的确认字面量，并且只给超级管理员看按钮。
   这里不做任何「自动优化」——用户不点，系统设置永远不会被改。
   参数表由后端定义（六个键），前端只负责填值、预览差异、把选择原样回传。 */
let HO_REPORT = null;
let HO_TUNE = null;    // 后端当前的生效值
const HO_CONFIRM = "APPLY NET";
const HO_REVERT_CONFIRM = "REVERT NET";

async function loadHostOpt() {
  try {
    HO_REPORT = await req("/api/host-opt");
    renderHostOpt(HO_REPORT);
  } catch (e) {
    const el = $("ho_meta");
    if (el) el.textContent = "检测失败：" + e.message;
  }
}

/* 系统概览：负载 / 内存 / 磁盘 / IPv6 / MTU，纯展示，不参与任何写操作。 */
function renderHoSys(s) {
  const host = $("ho_sys");
  if (!host) return;
  if (!s) { host.innerHTML = '<p class="muted">当前平台不支持系统概览</p>'; return; }
  const pctText = (used, total) => total ? Math.round(used / total * 100) + "%" : "—";
  const rows = [
    ["负载", (s.load || []).length ? s.load.map(x => x.toFixed(2)).join(" / ") : "—"],
    ["CPU", s.cpu_count ? s.cpu_count + " 核" : "—"],
    ["内存", s.mem_total ? `${fmtBytes(s.mem_total - s.mem_avail)} / ${fmtBytes(s.mem_total)}（${pctText(s.mem_total - s.mem_avail, s.mem_total)}）` : "—"],
    ["数据盘", s.disk_total ? `${fmtBytes(s.disk_total - s.disk_free)} / ${fmtBytes(s.disk_total)}（${pctText(s.disk_total - s.disk_free, s.disk_total)}）` : "—"],
    ["IPv6", s.ipv6 ? "已启用" : "已禁用"],
    ["默认网卡 MTU", s.mtu ? `${s.mtu}${s.mtu_if ? "（" + s.mtu_if + "）" : ""}` : "—"],
  ];
  host.innerHTML = rows.map(([k, v]) =>
    `<div style="display:flex;flex-wrap:wrap;gap:2px 12px;padding:7px 0;border-bottom:1px solid var(--line)">
       <span class="muted" style="flex:0 0 96px">${escHtml(k)}</span>
       <span class="mono" style="flex:1 1 140px;min-width:0;word-break:break-word">${escHtml(v)}</span>
     </div>`).join("");
  const warn = $("ho_sys_warn");
  if (warn) warn.textContent = (s.warnings || []).join("；");
}

/* 表单只负责「显示当前值」，第一次渲染后用户改过的内容不会被覆盖。 */
function fillHoForm(j) {
  HO_TUNE = j.tune || {};
  const opt = j.options || {};
  const fillSel = (id, list, cur) => {
    const el = $(id);
    if (!el) return;
    // 当前值可能不在推荐列表里（例如系统本来是 pfifo_fast）：必须补进去，
    // 否则 select 会显示成空白，用户以为参数丢了。
    const vals = [...new Set([cur, ...(list || [])].filter(Boolean))];
    el.innerHTML = vals.map(v => `<option value="${escHtml(v)}">${escHtml(v)}</option>`).join("");
    if (cur) el.value = cur;
  };
  fillSel("ho_cc", opt.cc, HO_TUNE.cc);
  fillSel("ho_qdisc", opt.qdisc, HO_TUNE.qdisc);
  // 缓冲用 MiB 输入，允许两位小数（默认值 212992 B = 0.2 MiB）。
  const mib = b => parseFloat(((b || 0) / 1048576).toFixed(6));
  $("ho_rmem").value = mib(HO_TUNE.rmem_max);
  $("ho_wmem").value = mib(HO_TUNE.wmem_max);
  $("ho_somaxconn").value = HO_TUNE.somaxconn || 4096;
  $("ho_pstart").value = HO_TUNE.port_start || 10240;
  $("ho_pend").value = HO_TUNE.port_end || 60999;
}

function hoDesired() {
  const num = id => parseInt($(id).value, 10) || 0;
  const cur = HO_TUNE || {};
  // MiB → 字节。输入框是十进制近似值，差 1～2 字节时按原值算：
  // 否则「没动过的字段」也会被标成变更，差异预览就失去了意义。
  const bytesOf = (id, curB) => {
    const v = parseFloat($(id).value);
    if (!isFinite(v)) return curB || 0;
    const b = Math.round(v * 1048576);
    return Math.abs(b - (curB || 0)) <= 2 ? (curB || 0) : b;
  };
  return {
    cc: $("ho_cc").value,
    qdisc: $("ho_qdisc").value,
    rmem_max: bytesOf("ho_rmem", cur.rmem_max),
    wmem_max: bytesOf("ho_wmem", cur.wmem_max),
    somaxconn: num("ho_somaxconn"),
    port_start: num("ho_pstart"),
    port_end: num("ho_pend"),
  };
}

/* 「现值 → 目标值」预览。用户改哪一格，这里立刻标红哪一行——
   应用前就知道会动什么，是这个页面最重要的防线。 */
function hoDiffRows() {
  const cur = HO_TUNE || {}, want = hoDesired();
  // 缓冲按字节展示（fmtBytes 会自动降到 KiB），比一律换算 MiB 更不容易出现 0。
  return [
    ["拥塞控制", cur.cc, want.cc],
    ["队列规则", cur.qdisc, want.qdisc],
    ["接收缓冲", fmtBytes(cur.rmem_max), fmtBytes(want.rmem_max)],
    ["发送缓冲", fmtBytes(cur.wmem_max), fmtBytes(want.wmem_max)],
    ["全连接队列", cur.somaxconn, want.somaxconn],
    ["本地端口", `${cur.port_start || "—"}–${cur.port_end || "—"}`, `${want.port_start}–${want.port_end}`],
  ].map(([k, c, v]) => ({ k, c: String(c ?? "—"), v: String(v), ch: String(c) !== String(v) }));
}

function renderHoDiff() {
  const host = $("ho_diff");
  if (!host || !HO_REPORT) return;
  const rows = hoDiffRows();
  const n = rows.filter(r => r.ch).length;
  host.innerHTML =
    `<div class="muted" style="font-size:12px;margin-bottom:6px">` +
    (n ? `${n} 项将被修改` : "与当前生效值一致，应用不会改动系统") + `</div>` +
    rows.map(r =>
      `<div style="display:flex;flex-wrap:wrap;gap:2px 10px;padding:6px 8px;border-radius:6px;` +
      `background:${r.ch ? "var(--acc-soft)" : "transparent"};border-bottom:1px solid var(--line)">
         <span style="flex:0 0 84px;color:var(--muted)">${escHtml(r.k)}</span>
         <span class="mono" style="flex:1 1 100px;min-width:0;word-break:break-word">${escHtml(r.c)}</span>
         <span class="muted" style="flex:0 0 14px">→</span>
         <span class="mono" style="flex:1 1 100px;min-width:0;word-break:break-word;` +
      `color:${r.ch ? "var(--acc-text)" : "var(--muted)"};font-weight:${r.ch ? "600" : "400"}">${escHtml(r.v)}</span>
       </div>`).join("");
}

/* 输入框/下拉一改就重算差异；不写库、不发请求。 */
function hoEdit() { renderHoDiff(); }

function renderHostOpt(j) {
  const kv = [
    ["平台 / 内核", `${j.platform || "—"} ${j.kernel || ""}`],
    ["当前拥塞控制", j.current_cc || "读取失败"],
    ["可用算法", (j.available_cc || []).join(" ") || "读取失败"],
    ["默认 qdisc", j.default_qdisc || "读取失败"],
    ["tc 实测", j.qdisc_detail || "—"],
    ["运行环境", `${j.in_container ? "容器内" : "物理机 / 虚拟机"}，${j.is_root ? "root" : "非 root"}`],
    ["面板配置", j.applied ? "已应用，可撤销" : "未应用"]
  ];
  // 不用 table / 固定 min-width：tc qdisc 这种一长串英文在 320px 屏上会把
  // 整张卡片撑出横向滚动条。flex-wrap + min-width:0 + word-break 让每项自己换行。
  $("ho_meta").innerHTML = kv.map(([k, v]) =>
    `<div style="display:flex;flex-wrap:wrap;gap:2px 12px;padding:6px 0;border-bottom:1px solid var(--line)">
       <span class="muted" style="flex:0 0 128px">${escHtml(k)}</span>
       <span class="mono" style="flex:1 1 140px;min-width:0;word-break:break-word">${escHtml(v)}</span>
     </div>`).join("");
  $("ho_checks").innerHTML = (j.checks || []).map(c => {
    const mark = c.ok ? "✓" : (c.warn ? "!" : "✕");
    const color = c.ok ? "var(--ok, var(--acc))" : (c.warn ? "var(--warn)" : "var(--err, #d44)");
    return `<div style="display:flex;flex-wrap:wrap;gap:3px 9px;align-items:baseline;padding:7px 0;border-bottom:1px solid var(--line)">
      <b style="color:${color};width:16px;flex:none">${mark}</b>
      <span style="flex:1 1 150px;min-width:0">${escHtml(c.name)}</span>
      <span class="muted" style="flex:1 1 100%;min-width:0;word-break:break-word">${escHtml(c.detail || "")}</span>
    </div>`;
  }).join("");
  renderHoSys(j.system);
  fillHoForm(j);
  renderHoDiff();
  $("ho_reason").textContent = (j.apply_enabled
    ? "此主机可以由面板应用；应用前会备份每个键的原值，写完回读复检，不符自动整体回滚。"
    : (j.reason || "当前主机不支持由面板应用，请按下面的命令手动处理。")) +
    (!isOwner() && j.apply_enabled ? "（应用与撤销仅超级管理员可操作）" : "");
  $("ho_cmds").textContent = (j.suggested || []).join("\n");
  const canAct = isOwner();
  $("ho_apply").classList.toggle("hide", !canAct || !j.apply_enabled);
  $("ho_revert").classList.toggle("hide", !canAct || !j.applied);
}

async function hostOptApply() {
  const rows = hoDiffRows().filter(r => r.ch);
  if (!rows.length) { toast("与当前生效值一致，无需应用"); return; }
  const list = rows.map(r => `${r.k} ${r.c} → ${r.v}`).join("\n");
  if (!await ask("应用网络参数",
    "将写入 /etc/sysctl.d/99-triune-bbr.conf 并立即生效，共 " + rows.length + " 项变更：\n" + list +
    "\n\n应用前会备份每个键的原值，写完回读复检，不符自动整体回滚。确定继续？")) return;
  try {
    await req("/api/host-opt/apply", { method:"POST", body:{ confirm: HO_CONFIRM, tune: hoDesired() } });
    toast("已应用网络参数");
    await loadHostOpt();
  } catch (e) { toast(e.message, "err"); }
}

async function hostOptRevert() {
  if (!await ask("撤销面板的改动",
    "将把六个参数恢复成应用前的原值，并删除面板写入的配置文件。确定继续？")) return;
  try {
    await req("/api/host-opt/revert", { method:"POST", body:{ confirm: HO_REVERT_CONFIRM } });
    toast("已撤销面板的改动");
    await loadHostOpt();
  } catch (e) { toast(e.message, "err"); }
}

/* 导出体检报告：纯前端序列化当前报告，不发请求、不写服务端。 */
function hoExport() {
  if (!HO_REPORT) { toast("还没有体检结果", "err"); return; }
  const blob = new Blob([JSON.stringify(HO_REPORT, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = "hostopt-" + new Date().toISOString().slice(0, 19).replace(/[:T]/g, "") + ".json";
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 2000);
  toast("已导出体检报告");
}

/* ---- 实时速率 ---- */
async function loadLive() {
  try {
    const j = await req("/api/traffic/live");
    $("liveUp").textContent = fmtBytes(j.up_bps) + "/s";
    $("liveDown").textContent = fmtBytes(j.down_bps) + "/s";
    const pl = $("pillLive");
    if (pl) pl.textContent = "↓ " + fmtBytes(j.down_bps) + "/s";
    const pt = { up: j.up_bps || 0, down: j.down_bps || 0 };
    if (DEMO && OV.hist.length) OV.hist[OV.hist.length - 1] = pt;  // 演示：替换末点，避免尖峰
    else { OV.hist.push(pt); if (OV.hist.length > 40) OV.hist.shift(); }
    if ($("v-overview") && $("v-overview").classList.contains("active")) drawLiveChart($("ov_live"), OV.hist);
    if (j.updated_at) $("live_at").textContent = "更新于 " + String(j.updated_at).replace("T", " ").slice(11, 19);
  } catch(e){}
}
setInterval(() => { if (TOKEN) loadLive(); }, 10000);

async function loadVersion() {
  try {
    const j = await req("/api/version");
    // 后端版本串可能自带 "v" 前缀（如 v0.8.0），此处只补不重复，避免出现 "vv0.8.0"
    const ver = /^v/i.test(j.version) ? j.version : "v" + j.version;
    $("curver").textContent = j.version;
    $("verinfo").textContent = ver;
    const lv = $("logoVer"); if (lv) lv.textContent = ver.replace(/-demo$/i, "");
    renderVerMismatch(j);
    // 自更新后刷新回来，核对一次「期望版本」：对不上就是重启没成功
    let exp = null;
    try { exp = sessionStorage.getItem("expectVer"); } catch(_) {}
    if (exp) {
      try { sessionStorage.removeItem("expectVer"); } catch(_) {}
      if (normVer(j.version) !== exp) {
        toast("面板重启后仍是 " + j.version + "，这次更新没生效——看「面板自更新」里的说明", "err");
      } else {
        toast("已更新到 " + j.version);
      }
    }
  } catch(e){}
}
/* 「磁盘版本 ≠ 运行版本」的提示。
   面板显示的版本号来自**进程**，所以二进制换了但没重启成功时，界面看着一切正常，
   只有版本号不动 —— 这正是「更新完了还是旧版本」的全部观感。
   两种证据都算：进程自报的重启失败说明，或磁盘二进制版本与运行版本不一致。

   现在的重点不是「告诉用户去重启」，而是「面板自己会切过去」：
   后端有一个守望者盯着二进制，几秒内就 exec 过去。所以这里一边说清楚，
   一边轮询 /api/version —— 版本一变就刷新页面，用户什么都不用做。 */
let __vmTimer = null, __vmTicks = 0;
function stopVerPoll() { if (__vmTimer) { clearInterval(__vmTimer); __vmTimer = null; } }
function startVerPoll(runVer) {
  if (__vmTimer) return;
  __vmTicks = 0;
  __vmTimer = setInterval(async () => {
    // 30 秒还没切过去就收手，别一直打接口
    if (++__vmTicks > 15) {
      stopVerPoll();
      const el = $("vm_auto");
      if (el) el.textContent = "自动切换没有在 30 秒内完成，请看下面的手动办法。";
      const m = $("vm_manual"); if (m) m.classList.remove("hide");
      return;
    }
    try {
      // 用免认证的 /api/public/info 探版本，而不是 /api/version：
      // 换进程意味着内存里的会话表被清空（重启即失效是既定设计），
      // 用带鉴权的接口探，切成功那一刻恰好会 401，反而看不见新版本。
      const p = await req("/api/public/info");
      if (normVer(p.version) !== normVer(runVer)) { location.reload(); return; }
      const j = await req("/api/version");
      renderVerMismatch(j);
    } catch(_) {}
  }, 2000);
}
function renderVerMismatch(j) {
  const box = $("ver_mismatch");
  if (!box) return;
  const disk = (j.binary_version || "").trim();
  const same = disk && normVer(disk) === normVer(j.version);
  const failed = (j.restart_failed || "").trim();
  if (same && !failed) { box.classList.add("hide"); stopVerPoll(); return; }
  $("vm_disk").textContent = disk ? shortVer(disk) : "（读不到）";
  $("vm_run").textContent = j.version;
  $("vm_msg").textContent = failed || "";
  const au = $("vm_auto"), mn = $("vm_manual");
  // 版本已经对上了、只是留着一条旧的失败记录：不用做任何事
  if (same) {
    if (au) au.textContent = "现在运行的已经是这个版本，这条只是上一次失败的记录，可以忽略。";
    if (mn) mn.classList.add("hide");
    box.classList.remove("hide");
    stopVerPoll();
    return;
  }
  // auto_switch 缺省视为开启（后端默认开，只有显式 "0" 才是关）
  const auto = j.auto_switch !== "0";
  if (au) au.textContent = auto
    ? (failed ? "面板会在几秒内再试一次；一直不行就看下面的手动办法。"
              : "面板正在自动切换到新版本，几秒内完成，页面会自动刷新（换进程会重置登录态，需要重新登录一次）。")
    : "「自动切换」当前是关的，所以要你手动重启一次；勾上下面这一项，以后换了二进制面板会自己切过去。";
  if (mn) mn.classList.toggle("hide", auto && !failed);
  box.classList.remove("hide");
  if (auto) startVerPoll(j.version); else stopVerPoll();
}
// 二进制自报的是整行（"triune-panel v1.0.8 (linux/amd64)"），标题里只留版本与平台
function shortVer(s) {
  return String(s || "").replace(/^\s*triune-panel\s+/i, "").trim();
}
// 比较版本号时忽略二进制自报串里的多余描述（"triune-panel v1.0.9 (linux/amd64)"）
function normVer(s) {
  const m = String(s || "").match(/v?\d+\.\d+\.\d+[-\w.]*/i);
  return m ? m[0].toLowerCase() : String(s || "").trim().toLowerCase();
}
async function loadStatus() {
  try {
    const j = await req("/api/status");
    OV.cores = j.cores || [];
    const pc = $("pillCore");
    if (pc) {
      pc.innerHTML = `<i class="pulse"></i> 内核 <b>${OV.cores.filter(c => c.running).length}/${OV.cores.length}</b>`;
      pc.title = "sing-box / xray / mihomo 是三个独立进程，可以同时运行，各自管自己的入站";
    }
    if ($("v-overview") && $("v-overview").classList.contains("active")) { renderKpis(); renderRings(); }
  } catch(e){}
}
let CLIENT_N = 0, CL_INBS = [], CL_MASKED = false;
async function loadClients() {
  try {
    const j = await req("/api/clients");
    CLIENT_N = j.length;
    // 只读账号拿到的凭据与订阅令牌是空的（服务端置空 + masked 标记）。
    // 这里不能渲染成「没配凭据」——要明确说是权限问题。
    CL_MASKED = !!(j[0] && j[0].masked);
    const inbs = await req("/api/inbounds").catch(() => []);
    CL_INBS = inbs;
    $("cltbody").innerHTML = j.map(x => { const c = x.client, m = !!x.masked;
      const cred = m
        ? '<span class="muted" title="只读账号看不到凭据">—</span>'
        : `<span class="mono" title="${c.credential}">${c.credential.slice(0,14)}…</span>`;
      const sub = m
        ? '<span class="muted" title="只读账号看不到订阅令牌">—</span>'
        : `<span class="sub-token"><a href="${DEMO?"#":"/sub/"+c.sub_token}" target="_blank" class="mono">/sub/${c.sub_token.slice(0,10)}…</a>
        <button class="btn sm" title="复制订阅链接" data-act="copySub" data-args="${escJson([c.sub_token])}">
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
        复制</button>
        <button class="btn sm" title="生成订阅/节点二维码，手机扫码即用" data-act="userQRSub" data-args="${escJson([c.sub_token,c.name])}">二维码</button>
        <button class="btn sm" title="重置后旧订阅链接立即失效" data-act="resetSubToken" data-args="${escJson([c.id])}">重置订阅</button></span>`;
      const sw = m
        ? (c.enabled ? '<span class="badge b-ok">启用</span>' : '<span class="badge b-idle">停用</span>')
        : `<label class="sw"><input type="checkbox" ${c.enabled?"checked":""} data-act="toggleClient" data-args="${escJson([c.id])}" data-el="checked"><i></i></label>`;
      const ops = m
        ? '<span class="muted" style="font-size:12px">只读</span>'
        : `<button class="btn sm" data-act="openClientModal" data-args="${escJson([c.id])}">编辑</button>
        <button class="btn sm danger" data-act="delClient" data-args="${escJson([c.id])}">删除</button>`;
      return `<tr>
      <td><b>${c.name}</b></td>
      <td>${cred}</td>
      <td>${sub}</td>
      <td>${c.quota_gb ? c.quota_gb + " GB" : "不限"}</td>
      <td>${c.expires_at ? c.expires_at.slice(0,10) : '<span class="muted">永久</span>'}</td>
      <td>${sw}</td>
      <td class="ta-r">${ops}</td></tr>`;
    }).join("") || `<tr><td colspan="7" class="empty">暂无用户，点击右上角「+ 新增用户」创建</td></tr>`;
  } catch(e){}
}
async function loadInbounds() {
  try {
    const j = await req("/api/inbounds");
    INB_N = j.length;
    const stInb = $("st_inb"); if (stInb) stInb.textContent = j.length;
    $("ibtbody").innerHTML = j.map(i => { return `<tr>
      <td><b>${i.name}</b>${i.draft ? ' <span class="badge b-warn">草稿</span>' : ""}${i.enabled ? "" : ' <span class="badge b-warn" title="未启用：端口没有监听，导出的链接连不上">未启用</span>'}</td>
      <td><span class="badge b-acc">${i.core}</span></td><td>${i.protocol}</td>
      <td class="num">${i.port}</td><td>${i.transport}</td><td>${i.tls}</td>
      <td><label class="sw"><input type="checkbox" ${i.enabled?"checked":""} data-act="toggleInbound" data-args="${escJson([i.id])}" data-el="checked"><i></i></label></td>
      <td class="ta-r"><button class="btn sm" data-act="openInboundModal" data-args="${escJson([i.id])}">编辑</button>
        <button class="btn sm" data-act="openCheckModal" data-args="${escJson([i.id])}" title="检测进程/端口/TLS/防火墙">自检</button>
        ${i.enabled && !i.draft ? `<button class="btn sm" data-act="openSwitchModal" data-args="${escJson([i.id])}">切换内核</button>` : ""}
        ${i.draft ? `<button class="btn sm" data-act="promoteInbound" data-args="${escJson([i.id])}">取消草稿</button>` : ""}
        <button class="btn sm" data-act="openExportModal" data-args="${escJson([i.id])}">导出</button>
        <button class="btn sm danger" data-act="delInbound" data-args="${escJson([i.id])}">删除</button></td></tr>`;
    }).join("") || `<tr><td colspan="8" class="empty">暂无入站，点击右上角「+ 新增入站」创建</td></tr>`;
  } catch(e){}
}

/* ---- 入站导出 ---- */
let exportId = null;
async function openExportModal(id) {
  exportId = id;
  const warn = $("ex_warn");
  if (warn) { warn.innerHTML = ""; warn.classList.add("hide"); }
  const notes = [];
  try {
    const all = await req("/api/inbounds");
    const i = all.find(x => x.id === id);
    $("ex_name").textContent = i ? `${i.name} (${i.core}:${i.port})` : "#" + id;
    // 导出的是「客户端该怎么连」，不是「服务端在监听」。入站停用/草稿时
    // 端口根本没有监听，而链接和 Clash 配置看起来完全正常——排查会一路跑到
    // 客户端、防火墙、REALITY 参数上去（3x-ui JSON 导入忘了开开关就是这么中招的）。
    if (i && (!i.enabled || i.draft)) {
      notes.push(
        `<b>⚠ 这条入站${i.draft ? "还是草稿" : "当前已停用"}，端口 ${i.port} 没有监听。</b><br>` +
        `下面的链接/配置参数本身没问题，但连上去一定失败（超时或被拒）。` +
        `请先在入站列表打开启用开关，点「应用配置」，确认端口在监听后再导出。`);
    }
    // 下载之前先说清「这份文件里到底有几个节点」。以前转换失败是静默跳过的，
    // 于是 vless+xhttp 这类 mihomo 表示不出来的传输会导出一个 proxies 为空的
    // 文件——客户端连加载都不肯，而界面只说「已下载」。
    try {
      const s = await req(`/api/inbounds/${id}/export?format=summary`);
      const skipHtml = (list, fmt) => (list || []).map(k =>
        `<div>· <span class="mono">${escHtml(k.name)}</span> — ${escHtml(k.reason)}</div>`).join("");
      const cs = (s.clash || {}).skipped || [], ss = (s.singbox || {}).skipped || [];
      if (!s.nodes) {
        notes.push(`<b>⚠ 这条入站下没有有效用户，导不出任何节点。</b><br>` +
          `先到「用户」里建一个用户，并在它上面勾选本入站，再回来导出。`);
      }
      if (cs.length) {
        notes.push(`<b>⚠ ${cs.length} 个节点无法转成 Clash 配置</b>（传输在 mihomo 里没有对应类型）。` +
          `下载的 Clash 文件里 PROXY 组只有 DIRECT——能加载，但等于直连：${skipHtml(cs)}`);
      }
      if (ss.length) {
        notes.push(`<b>⚠ ${ss.length} 个节点无法转成 sing-box 配置：</b>${skipHtml(ss)}`);
      }
    } catch (e) { /* 摘要拿不到不影响导出本身 */ }
  } catch(e){ $("ex_name").textContent = "#" + id; }
  if (warn && notes.length) {
    warn.classList.remove("hide");
    warn.innerHTML = notes.join(`<div style="height:8px"></div>`);
  }
  $("ex_links").value = "";
  openModal("mExport");
}
async function exportLinks() {
  if (DEMO) return [
    "vless://a1b2c3d4-1111-2222-3333-444455556666@demo.example.com:443?security=reality&sni=www.apple.com&pbk=demo-pubkey&sid=ab12&fp=chrome#demo-hk-user1",
    "vless://a1b2c3d4-7777-8888-9999-000011112222@demo.example.com:443?security=reality&sni=www.apple.com&pbk=demo-pubkey&sid=ab12&fp=chrome#demo-hk-user2"
  ].join("\n");
  const r = await fetch(`/api/inbounds/${exportId}/export?format=links`,
    { headers: { Authorization: "Bearer " + TOKEN } });
  if (!r.ok) throw new Error("导出失败(" + r.status + ")");
  return r.text();
}
async function exportInboundCopy() {
  try {
    const text = await exportLinks();
    $("ex_links").value = text;
    if (navigator.clipboard) {
      await navigator.clipboard.writeText(text);
      toast("分享链接已复制到剪贴板");
    } else toast("已生成，请手动复制文本框内容", "info");
  } catch(e){ toast(e.message, "err"); }
}
async function exportInboundDownload(fmt) {
  try {
    const name = ($("ex_name").textContent.split(" (")[0] || "inbound").replace(/[\/\s]/g, "_");
    let blob, fname;
    if (DEMO) {
      // 演示模式也给一份**能被 mihomo 加载**的最小配置：
      // 空 proxy-group 会被拒收（`use` or `proxies` missing）
      const body = fmt === "clash"
        ? "proxies: []\nproxy-groups:\n  - name: PROXY\n    type: select\n    proxies: [DIRECT]\nrules:\n  - MATCH,PROXY\n"
        : JSON.stringify({ outbounds: [{ type: "direct", tag: "direct" }] }, null, 2);
      blob = new Blob([body], { type: fmt === "clash" ? "text/yaml" : "application/json" });
      fname = fmt === "clash" ? `${name}-clash.yaml` : fmt === "singbox" ? `${name}-singbox.json` : `${name}-inbound.json`;
    } else {
      const r = await fetch(`/api/inbounds/${exportId}/export?format=${fmt}`,
        { headers: { Authorization: "Bearer " + TOKEN } });
      if (!r.ok) throw new Error("导出失败(" + r.status + ")");
      blob = await r.blob();
      const m = (r.headers.get("Content-Disposition") || "").match(/filename=(.+)/);
      fname = m ? m[1] : `${name}-${fmt}`;
    }
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob); a.download = fname;
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(a.href), 3000);
    toast("已开始下载 " + fname);
  } catch(e){ toast(e.message, "err"); }
}

/* ---- 配置生成器 ---- */
let genCatalog = null, genPolicies = {}, genResult = null;
let genSetOrder = [];
let genCore = "mihomo", genTarget = "client", genScenario = "", genChecked = {}, genClosed = {};
/* sing-box 各版本的配置结构不同，必须让用户明确选。1.11 与 1.12+ 是两套 JSON：
   1.11 的 DNS 用 address、没有 default_domain_resolver；写错一边内核直接 FATAL。
   1.12/1.13/1.14 实测配置完全一致，所以不要按「每个版本都不一样」去理解。 */
const SB_VERS = [
  { v: "1.11", note: "已停止维护（1.11.15 为最后一版），旧 DNS address 格式" },
  { v: "1.12", note: "稳定版，配置与 1.13 / 1.14 完全一致" },
  { v: "1.13", note: "稳定版，配置与 1.12 / 1.14 完全一致" },
  { v: "1.14", note: "当前最新稳定版" },
  { v: "1.15", note: "预览版（alpha），不写 tun.stack" },
  { v: "1.16", note: "尚未发布，按 1.15 的规则生成" }
];
let genSBVer = "1.14";
const GEN_TARGET_NOTES = {
  /* 7890 只是导出文件里的默认值。Clash Verge 默认 7897、FlClash 也有自己的设置，
     它们会覆盖 mixed-port；照着 7890 去配浏览器代理会连到没人监听的端口，
     现象是「节点在线、网页全打不开」。与 genconfig.go 的文件头提示保持一致。 */
  client: "客户端模式：混合端口 7890（HTTP/SOCKS）+ 系统代理或浏览器插件使用，不启用 TUN，最轻量。端口以客户端界面上显示的为准：Clash Verge 等会用自己的设置覆盖 7890（默认 7897）。",
  "main-router": "主路由模式：启用 TUN 接管全机流量 + DNS 劫持 :53 + 流量嗅探，局域网设备零配置；建议先在单设备上验证配置。",
  "side-router": "旁路由模式：启用 TUN + DNS 劫持 + 局域网监听；把设备的网关和 DNS 指向本机 IP 即可接管，主路由无需改动。"
};
const DEMO_CATALOG = {
  sets: [
    {
      "id": "geosite-cn",
      "name": "中国大陆域名",
      "group": "基础分流",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geoip-cn",
      "name": "中国大陆 IP",
      "group": "基础分流",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-private",
      "name": "局域网 / 私有域名",
      "group": "基础分流",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geoip-private",
      "name": "局域网 / 私有 IP",
      "group": "基础分流",
      "cores": "mihomo",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-geolocation-!cn",
      "name": "非中国大陆域名",
      "group": "基础分流",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-ads-all",
      "name": "广告域名（通用全量）",
      "group": "广告与隐私",
      "cores": "both",
      "policy": "REJECT"
    },
    {
      "id": "geosite-category-ads",
      "name": "广告域名（精简）",
      "group": "广告与隐私",
      "cores": "both",
      "policy": "REJECT"
    },
    {
      "id": "geosite-category-public-tracker",
      "name": "公共追踪器",
      "group": "广告与隐私",
      "cores": "both",
      "policy": "REJECT"
    },
    {
      "id": "geosite-category-telemetry",
      "name": "遥测 / 数据上报",
      "group": "广告与隐私",
      "cores": "both",
      "policy": "REJECT"
    },
    {
      "id": "geosite-category-consent-management",
      "name": "Cookie 同意管理",
      "group": "广告与隐私",
      "cores": "both",
      "policy": "REJECT"
    },
    {
      "id": "geosite-category-password-management",
      "name": "密码管理器",
      "group": "广告与隐私",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-ip-geo-detect",
      "name": "IP 归属地检测",
      "group": "广告与隐私",
      "cores": "both",
      "policy": "REJECT"
    },
    {
      "id": "geosite-category-anticensorship",
      "name": "反审查站点",
      "group": "广告与隐私",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-proxy-tunnels",
      "name": "代理隧道与中转",
      "group": "广告与隐私",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-vpnservices",
      "name": "VPN 服务商",
      "group": "广告与隐私",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-stun",
      "name": "STUN / TURN 服务",
      "group": "广告与隐私",
      "cores": "both",
      "policy": "REJECT"
    },
    {
      "id": "geosite-category-ipfs",
      "name": "IPFS 节点",
      "group": "广告与隐私",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-openai",
      "name": "OpenAI / ChatGPT",
      "group": "AI 服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-anthropic",
      "name": "Anthropic / Claude",
      "group": "AI 服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-ai-!cn",
      "name": "AI 服务（海外）",
      "group": "AI 服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-ai-chat-!cn",
      "name": "AI 对话（海外）",
      "group": "AI 服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-ai-cn",
      "name": "AI 服务（国内）",
      "group": "AI 服务",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-ai-ru",
      "name": "AI 服务（俄罗斯区）",
      "group": "AI 服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-google-gemini",
      "name": "Google Gemini",
      "group": "AI 服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-perplexity",
      "name": "Perplexity",
      "group": "AI 服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-deepseek",
      "name": "DeepSeek",
      "group": "AI 服务",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-doubao",
      "name": "豆包 Doubao",
      "group": "AI 服务",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-bing",
      "name": "Bing / Copilot",
      "group": "AI 服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-huggingface",
      "name": "Hugging Face",
      "group": "AI 服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-notion",
      "name": "Notion",
      "group": "AI 服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-netflix",
      "name": "Netflix",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-disney",
      "name": "Disney+",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-youtube",
      "name": "YouTube",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-tiktok",
      "name": "TikTok",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-spotify",
      "name": "Spotify",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-hbo",
      "name": "HBO",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-bahamut",
      "name": "巴哈姆特动漫",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-twitch",
      "name": "Twitch",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-netease",
      "name": "网易云音乐",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-bilibili",
      "name": "哔哩哔哩",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-iqiyi",
      "name": "爱奇艺",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-youku",
      "name": "优酷",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-media",
      "name": "影音媒体站点",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-media-cn",
      "name": "国内影音站点",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-entertainment",
      "name": "娱乐站点",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-entertainment-cn",
      "name": "国内娱乐站点",
      "group": "流媒体影音",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-telegram",
      "name": "Telegram",
      "group": "社交通讯",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-twitter",
      "name": "X / Twitter",
      "group": "社交通讯",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-facebook",
      "name": "Facebook",
      "group": "社交通讯",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-instagram",
      "name": "Instagram",
      "group": "社交通讯",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-whatsapp",
      "name": "WhatsApp",
      "group": "社交通讯",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-line",
      "name": "LINE",
      "group": "社交通讯",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-discord",
      "name": "Discord",
      "group": "社交通讯",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-reddit",
      "name": "Reddit",
      "group": "社交通讯",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-viber",
      "name": "Viber",
      "group": "社交通讯",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-signal",
      "name": "Signal",
      "group": "社交通讯",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-games",
      "name": "游戏平台（综合）",
      "group": "游戏加速",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-games-cn",
      "name": "国内游戏平台",
      "group": "游戏加速",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-game-accelerator-cn",
      "name": "国内游戏加速服务",
      "group": "游戏加速",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-enhance-gaming",
      "name": "游戏加速增强",
      "group": "游戏加速",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-flight-simulator",
      "name": "模拟器类游戏",
      "group": "游戏加速",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-steam",
      "name": "Steam",
      "group": "游戏加速",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-epicgames",
      "name": "Epic Games",
      "group": "游戏加速",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-playstation",
      "name": "PlayStation",
      "group": "游戏加速",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-xbox",
      "name": "Xbox",
      "group": "游戏加速",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-nintendo",
      "name": "Nintendo",
      "group": "游戏加速",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-blizzard",
      "name": "暴雪战网",
      "group": "游戏加速",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-mihoyo",
      "name": "米哈游（HoYoPlay）",
      "group": "游戏加速",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-github",
      "name": "GitHub",
      "group": "开发与技术",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-gitlab",
      "name": "GitLab",
      "group": "开发与技术",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-docker",
      "name": "Docker Hub",
      "group": "开发与技术",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-golang",
      "name": "Go 语言站点",
      "group": "开发与技术",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-nodejs",
      "name": "Node.js / npm",
      "group": "开发与技术",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-jetbrains",
      "name": "JetBrains",
      "group": "开发与技术",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-dev",
      "name": "开发者站点",
      "group": "开发与技术",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-dev-cn",
      "name": "国内开发者站点",
      "group": "开发与技术",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-cdn-!cn",
      "name": "海外 CDN",
      "group": "开发与技术",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-cdn-cn",
      "name": "国内 CDN",
      "group": "开发与技术",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-finance",
      "name": "金融理财站点",
      "group": "金融商务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-bank-cn",
      "name": "国内银行",
      "group": "金融商务",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-securities-cn",
      "name": "国内证券",
      "group": "金融商务",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-cryptocurrency",
      "name": "加密货币",
      "group": "金融商务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-paypal",
      "name": "PayPal",
      "group": "金融商务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-stripe",
      "name": "Stripe",
      "group": "金融商务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-wise",
      "name": "Wise 转账",
      "group": "金融商务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-amazon",
      "name": "Amazon",
      "group": "购物电商",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-ebay",
      "name": "eBay",
      "group": "购物电商",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-pinduoduo",
      "name": "拼多多",
      "group": "购物电商",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-ecommerce",
      "name": "电商平台",
      "group": "购物电商",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-microsoft",
      "name": "Microsoft",
      "group": "办公云服务",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-google",
      "name": "Google",
      "group": "办公云服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-dropbox",
      "name": "Dropbox",
      "group": "办公云服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-zoom",
      "name": "Zoom 会议",
      "group": "办公云服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-onedrive",
      "name": "OneDrive",
      "group": "办公云服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-icloud",
      "name": "iCloud",
      "group": "办公云服务",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-companies",
      "name": "大型企业站点",
      "group": "办公云服务",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-scholar-cn",
      "name": "国内学术资源",
      "group": "教育科研",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-scholar-!cn",
      "name": "海外学术资源",
      "group": "教育科研",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-scholar-hk",
      "name": "港澳学术资源",
      "group": "教育科研",
      "cores": "both",
      "policy": "PROXY"
    },
    {
      "id": "geosite-category-mooc-cn",
      "name": "国内慕课平台",
      "group": "教育科研",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-education-cn",
      "name": "国内教育站点",
      "group": "教育科研",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-documents-cn",
      "name": "国内文档站",
      "group": "教育科研",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-hospital-cn",
      "name": "国内医院与医疗",
      "group": "生活服务",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-food-cn",
      "name": "国内餐饮外卖",
      "group": "生活服务",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-logistics-cn",
      "name": "国内物流",
      "group": "生活服务",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-blog-cn",
      "name": "国内博客",
      "group": "生活服务",
      "cores": "both",
      "policy": "DIRECT"
    },
    {
      "id": "geosite-category-porn",
      "name": "成人内容（拦截用）",
      "group": "广告与隐私",
      "cores": "mihomo",
      "policy": "REJECT"
    }
  ],
  mirrors: [
    {id:"official", name:"官方（GitHub 直连）", kind:"official", note:"raw.githubusercontent.com，境外网络或已配置代理时最快"},
    {id:"gh-proxy", name:"gh-proxy.com", kind:"prefix", prefix:"https://gh-proxy.com/", note:"实测内容与官方一致"},
    {id:"jsdelivr", name:"jsDelivr CDN", kind:"jsdelivr", note:"改写成 cdn.jsdelivr.net/gh/…"},
    {id:"custom", name:"自定义前缀", kind:"prefix", note:"填 https:// 开头的前缀"}
  ],
  mirror: {id:"official", prefix:""},
  scenarios: [
    {
      "id": "balanced",
      "name": "均衡日常",
      "tag": "通用",
      "desc": "国内直连 + 广告拦截 + 常规外网走代理，绝大多数用户的默认选择",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-microsoft",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-google",
          "policy": "PROXY"
        }
      ]
    },
    {
      "id": "streaming",
      "name": "流媒体优先",
      "tag": "流媒体",
      "desc": "海外流媒体全量走代理，避免 CDN 落地变慢、区域限制",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-netflix",
          "policy": "PROXY"
        },
        {
          "id": "geosite-disney",
          "policy": "PROXY"
        },
        {
          "id": "geosite-youtube",
          "policy": "PROXY"
        },
        {
          "id": "geosite-tiktok",
          "policy": "PROXY"
        },
        {
          "id": "geosite-spotify",
          "policy": "PROXY"
        },
        {
          "id": "geosite-hbo",
          "policy": "PROXY"
        },
        {
          "id": "geosite-bahamut",
          "policy": "PROXY"
        },
        {
          "id": "geosite-twitch",
          "policy": "PROXY"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-microsoft",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "gaming",
      "name": "游戏加速",
      "tag": "游戏",
      "desc": "游戏平台与加速服务走代理，降低延迟；国内服务照常直连",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-games",
          "policy": "PROXY"
        },
        {
          "id": "geosite-steam",
          "policy": "PROXY"
        },
        {
          "id": "geosite-epicgames",
          "policy": "PROXY"
        },
        {
          "id": "geosite-playstation",
          "policy": "PROXY"
        },
        {
          "id": "geosite-xbox",
          "policy": "PROXY"
        },
        {
          "id": "geosite-nintendo",
          "policy": "PROXY"
        },
        {
          "id": "geosite-blizzard",
          "policy": "PROXY"
        },
        {
          "id": "geosite-mihoyo",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-enhance-gaming",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-flight-simulator",
          "policy": "PROXY"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "ai",
      "name": "AI 服务",
      "tag": "AI",
      "desc": "ChatGPT / Claude / Gemini 等走代理保证区域可用，国内 AI 服务直连",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-openai",
          "policy": "PROXY"
        },
        {
          "id": "geosite-anthropic",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-ai-!cn",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-ai-chat-!cn",
          "policy": "PROXY"
        },
        {
          "id": "geosite-google-gemini",
          "policy": "PROXY"
        },
        {
          "id": "geosite-perplexity",
          "policy": "PROXY"
        },
        {
          "id": "geosite-deepseek",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-huggingface",
          "policy": "PROXY"
        },
        {
          "id": "geosite-notion",
          "policy": "PROXY"
        },
        {
          "id": "geosite-bing",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-ai-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-doubao",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "privacy",
      "name": "隐私优先",
      "tag": "隐私",
      "desc": "广告 / 追踪 / 遥测全部拦截，除国内必需外一律走代理",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-public-tracker",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-telemetry",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-consent-management",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-password-management",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-ip-geo-detect",
          "policy": "REJECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        }
      ],
      "rules": [
        "DOMAIN-KEYWORD,analytics,REJECT",
        "DOMAIN-KEYWORD,tracker,REJECT",
        "DOMAIN-SUFFIX,doubleclick.net,REJECT"
      ]
    },
    {
      "id": "cn-max",
      "name": "国内直连优先",
      "tag": "通用",
      "desc": "只有明确在境外的站点走代理，可用性最高、延迟最低",
      "sets": [
        {
          "id": "geosite-geolocation-!cn",
          "policy": "PROXY"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "work",
      "name": "办公协作",
      "tag": "办公",
      "desc": "SaaS 协作工具走代理，保证海外团队会议与文档同步顺畅",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-zoom",
          "policy": "PROXY"
        },
        {
          "id": "geosite-dropbox",
          "policy": "PROXY"
        },
        {
          "id": "geosite-onedrive",
          "policy": "PROXY"
        },
        {
          "id": "geosite-microsoft",
          "policy": "PROXY"
        },
        {
          "id": "geosite-google",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-companies",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-cdn-!cn",
          "policy": "PROXY"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "dev",
      "name": "开发调试",
      "tag": "开发",
      "desc": "代码托管 / 镜像源 / 包管理走代理，国内镜像直连",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-github",
          "policy": "PROXY"
        },
        {
          "id": "geosite-gitlab",
          "policy": "PROXY"
        },
        {
          "id": "geosite-docker",
          "policy": "PROXY"
        },
        {
          "id": "geosite-golang",
          "policy": "PROXY"
        },
        {
          "id": "geosite-nodejs",
          "policy": "PROXY"
        },
        {
          "id": "geosite-jetbrains",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-dev",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-dev-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-category-cdn-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "study",
      "name": "学术科研",
      "tag": "学习",
      "desc": "海外学术资源走代理，国内教务与文档站直连",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-scholar-!cn",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-scholar-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-category-scholar-hk",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-category-mooc-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-category-education-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-category-documents-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "finance",
      "name": "金融证券",
      "tag": "商务",
      "desc": "海外支付与理财走代理，国内银行证券直连",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-finance",
          "policy": "PROXY"
        },
        {
          "id": "geosite-paypal",
          "policy": "PROXY"
        },
        {
          "id": "geosite-stripe",
          "policy": "PROXY"
        },
        {
          "id": "geosite-wise",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-cryptocurrency",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-bank-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-category-securities-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "shopping",
      "name": "海淘电商",
      "tag": "商务",
      "desc": "海外电商走代理保物流与风控稳定，国内电商直连",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-amazon",
          "policy": "PROXY"
        },
        {
          "id": "geosite-ebay",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-ecommerce",
          "policy": "PROXY"
        },
        {
          "id": "geosite-pinduoduo",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "social",
      "name": "社交通讯",
      "tag": "社交",
      "desc": "Telegram / X / Discord 等社交平台走代理，保留国内即时通讯直连",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-telegram",
          "policy": "PROXY"
        },
        {
          "id": "geosite-twitter",
          "policy": "PROXY"
        },
        {
          "id": "geosite-facebook",
          "policy": "PROXY"
        },
        {
          "id": "geosite-instagram",
          "policy": "PROXY"
        },
        {
          "id": "geosite-whatsapp",
          "policy": "PROXY"
        },
        {
          "id": "geosite-line",
          "policy": "PROXY"
        },
        {
          "id": "geosite-discord",
          "policy": "PROXY"
        },
        {
          "id": "geosite-reddit",
          "policy": "PROXY"
        },
        {
          "id": "geosite-signal",
          "policy": "PROXY"
        },
        {
          "id": "geosite-viber",
          "policy": "PROXY"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "video-call",
      "name": "视频会议",
      "tag": "办公",
      "desc": "会议与会议相关 SaaS 走代理，注意保留 UDP 以保证实时画质",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-zoom",
          "policy": "PROXY"
        },
        {
          "id": "geosite-microsoft",
          "policy": "PROXY"
        },
        {
          "id": "geosite-google",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-companies",
          "policy": "PROXY"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        }
      ],
      "rules": [
        "DOMAIN-SUFFIX,meet.google.com,PROXY"
      ]
    },
    {
      "id": "low-latency",
      "name": "低延迟精简",
      "tag": "通用",
      "desc": "只保留必要规则集，规则匹配开销最小，适合小内存路由与 NAS",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-geolocation-!cn",
          "policy": "PROXY"
        }
      ]
    },
    {
      "id": "ads-block",
      "name": "纯净拦截",
      "tag": "隐私",
      "desc": "专注拦广告，其余按国内直连 / 境外代理分流",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-ads",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-public-tracker",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-telemetry",
          "policy": "REJECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-geolocation-!cn",
          "policy": "PROXY"
        }
      ]
    },
    {
      "id": "overseas",
      "name": "出海商务",
      "tag": "商务",
      "desc": "除中国大陆外全部走代理，适合出海办公与海外业务",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-geolocation-!cn",
          "policy": "PROXY"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "back-to-cn",
      "name": "回国加速",
      "tag": "通用",
      "desc": "国内站点与国内 CDN 强制直连，境外走代理，适合回国使用",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-cdn-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-geolocation-!cn",
          "policy": "PROXY"
        }
      ]
    },
    {
      "id": "kid-safe",
      "name": "青少年模式",
      "tag": "隐私",
      "desc": "拦截成人内容与广告追踪，其余保持国内直连",
      "sets": [
        {
          "id": "geosite-category-porn",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-public-tracker",
          "policy": "REJECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "music",
      "name": "音乐听歌",
      "tag": "流媒体",
      "desc": "海外音乐平台走代理，国内音乐 App 直连",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-spotify",
          "policy": "PROXY"
        },
        {
          "id": "geosite-youtube",
          "policy": "PROXY"
        },
        {
          "id": "geosite-netease",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "anime",
      "name": "动漫追番",
      "tag": "流媒体",
      "desc": "B 站与巴哈姆特等正版平台，国内平台直连不卡顿",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-bilibili",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-bahamut",
          "policy": "PROXY"
        },
        {
          "id": "geosite-youtube",
          "policy": "PROXY"
        },
        {
          "id": "geosite-netease",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "news",
      "name": "资讯聚合",
      "tag": "生活",
      "desc": "海外媒体走代理，国内资讯与博客直连",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-category-media",
          "policy": "PROXY"
        },
        {
          "id": "geosite-category-media-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-category-blog-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "smart-home",
      "name": "智能家居直连",
      "tag": "生活",
      "desc": "局域网与国内 IoT / 生活服务全部直连，只把境外流量交给代理",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-category-food-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-category-hospital-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-category-logistics-cn",
          "policy": "DIRECT"
        }
      ]
    },
    {
      "id": "global",
      "name": "全局代理",
      "tag": "通用",
      "desc": "除中国大陆外所有流量统一走代理，规则最简洁",
      "sets": [
        {
          "id": "geosite-category-ads-all",
          "policy": "REJECT"
        },
        {
          "id": "geosite-geolocation-!cn",
          "policy": "PROXY"
        },
        {
          "id": "geosite-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geoip-cn",
          "policy": "DIRECT"
        },
        {
          "id": "geosite-private",
          "policy": "DIRECT"
        }
      ]
    }
  ]
};

function coreHasSet(def) {
  if (!def || !def.cores || def.cores === "both") return true;
  return def.cores === genCore;
}

async function loadGen() {
  try {
    const inbs = await req("/api/inbounds");
    const el = $("gen_inbounds");
    const cur = new Set([...el.querySelectorAll("input:checked")].map(i => i.value));
    el.innerHTML = inbs.length ? inbs.map(i =>
      `<label><input type="checkbox" value="${i.id}" ${cur.has(String(i.id)) ? "checked" : ""}>
        <span>${i.name}</span><span class="meta">${i.core}:${i.port}${i.draft ? " · 草稿" : ""}</span></label>`).join("")
      : '<span class="muted">暂无入站，请先在「入站」中创建</span>';
  } catch(e){}
  if (!genCatalog) {
    genCatalog = DEMO ? DEMO_CATALOG : await req("/api/export/catalog").catch(() => null);
    if (genCatalog) { renderScenarios(); renderMirror(); renderSets(); }
  }
  genTargetNote();
  renderSBVerSelect();
  syncSBVerVisibility();
  await loadRuleTags();
}

function renderScenarios() {
  const list = genCatalog.scenarios || [];
  $("gen_scenarios").innerHTML = list.map(s =>
    `<div class="scn-card${s.id===genScenario?" on":""}" data-id="${s.id}" data-act="applyScenario" data-args="${escJson([s.id])}">
      <div class="scn-t"><b>${s.name}</b><span class="chip ghost">${s.tag||"通用"}</span></div>
      <div class="scn-d">${s.desc||""}</div>
      <div class="scn-m"><span class="chip">${(s.sets||[]).length} 规则集</span>${(s.rules||[]).length?`<span class="chip ghost">${s.rules.length} 条自定义规则</span>`:""}</div>
    </div>`).join("");
  $("gen_scn_count").textContent = list.length + " 个预设";
}

/* 规则集的默认策略来自后端目录（国内站点 DIRECT / 广告追踪 REJECT）。
   v1.0.0 之前这里一律按 PROXY 起手，勾「中国大陆域名」也会被塞进代理。 */
function defaultPolicyOf(id) {
  const def = ((genCatalog && genCatalog.sets) || []).find(s => s.id === id);
  return (def && def.policy) || "PROXY";
}

function applySetChoice(s, on) {
  if (on) {
    genChecked[s.id] = true;
    if (!genPolicies[s.id]) genPolicies[s.id] = defaultPolicyOf(s.id);
  } else delete genChecked[s.id];
}

function setRowHTML(s) {
  const usable = coreHasSet(s);
  const on = !!genChecked[s.id];
  const pol = genPolicies[s.id] || defaultPolicyOf(s.id);
  const tag = (s.cores && s.cores !== "both") ? `<span class="tagcore">仅 ${s.cores}</span>` : "";
  return `<div class="set-row${usable?"":" off"}" data-name="${(s.name||"").toLowerCase()} ${(s.id||"").toLowerCase()}">
    <label><input type="checkbox" class="gset" data-id="${s.id}" ${on?"checked":""} ${usable?"":"disabled"}
      data-act="toggleSet" data-args="${escJson([s.id])}" data-el="checked"><span title="${s.name}">${s.name}</span></label>
    ${tag}
    <select class="input gpol" data-id="${s.id}" ${on?"":"disabled"} data-act="genPolicySet" data-args="${escJson([s.id])}" data-el="value">
      <option ${pol==="DIRECT"?"selected":""}>DIRECT</option>
      <option ${pol==="PROXY"?"selected":""}>PROXY</option>
      <option ${pol==="REJECT"?"selected":""}>REJECT</option></select></div>`;
}

/* ==================== 规则集下载源 ==================== */

/* 规则集是**客户端**去下载的，raw.githubusercontent.com 在很多网络下不通，
   而拉不到规则集时内核只说一句 rule-provider load failed——用户看到的现象
   往往是「节点连上了但不能上网」，很难想到问题在规则集。
   这里让用户换源，并且可以真拉一次来验证：有好几家镜像是「HTTP 200 + 网页
   挑战页」，从状态码完全看不出问题，只有看内容才知道。 */
function mirrorList() { return (genCatalog && genCatalog.mirrors) || []; }
function mirrorById(id) { return mirrorList().find(m => m.id === id) || null; }

function renderMirror() {
  const sel = $("gen_mirror");
  if (!sel) return;
  const list = mirrorList();
  if (!list.length) {
    sel.innerHTML = `<option value="official">官方（GitHub 直连）</option>`;
    return;
  }
  // 默认值来自服务端设置：用户上次选过镜像，下次打开还是它
  const def = ((genCatalog && genCatalog.mirror) || {}).id || "official";
  if (def !== "custom" && !mirrorById(def)) { /* 设置里的值已经不在表里了，退回官方 */ }
  sel.innerHTML = list.map(m =>
    `<option value="${m.id}"${m.id === def ? " selected" : ""}>${escHtml(m.name)}</option>`).join("");
  const pref = ((genCatalog && genCatalog.mirror) || {}).prefix || "";
  if (pref) $("gen_mirror_prefix").value = pref;
  syncMirrorUI();
}

function syncMirrorUI() {
  const sel = $("gen_mirror");
  const id = sel ? sel.value : "official";
  const m = mirrorById(id);
  $("gen_mirror_prefix").classList.toggle("hide", id !== "custom");
  $("gen_mirror_note").textContent = m ? (m.note || "") : "";
  // 换了源，上一次的测试结论就不再适用，收起来避免误读
  $("gen_mirror_probe").classList.add("hide");
}

function genMirrorSet(v) {
  if ($("gen_mirror")) $("gen_mirror").value = v || "official";
  syncMirrorUI();
}

/* 记住选择。只在「测试通过」和「成功生成」时写，不在每次下拉变化时写：
   设置写入会记审计、发变更通知，随手点两下不该产生三条记录。 */
function saveMirrorPref() {
  const sel = $("gen_mirror");
  if (!sel) return;
  const id = sel.value;
  const prefix = id === "custom" ? ($("gen_mirror_prefix").value || "").trim() : "";
  req("/api/settings", { method: "PUT", body: { ruleset_mirror: id, ruleset_mirror_prefix: prefix } })
    .catch(() => {});   // 只读角色/网络抖动不该打断生成
}

function mirrorArgs() {
  const sel = $("gen_mirror");
  const id = sel ? sel.value : "official";
  const prefix = id === "custom" ? ($("gen_mirror_prefix").value || "").trim() : "";
  return { mirror: id, prefix };
}

async function checkMirror() {
  const box = $("gen_mirror_probe");
  const a = mirrorArgs();
  if (a.mirror === "custom" && !a.prefix) { toast("请先填自定义前缀", "warn"); return; }
  box.classList.remove("hide");
  box.innerHTML = `<span class="muted" style="font-size:12.5px">正在真拉一次规则集…</span>`;
  try {
    const j = await req("/api/export/check-mirror", { method: "POST", body: a });
    const color = j.ok ? "var(--ok)" : "var(--err)";
    box.innerHTML = `<div style="margin:8px 0 2px;padding:8px 10px;border-radius:8px;font-size:12.5px;
        background:color-mix(in srgb, ${color} 10%, transparent);
        border:1px solid color-mix(in srgb, ${color} 38%, transparent)">
      <b style="color:${color}">${j.ok ? "可用" : "不可用"}</b>
      <span class="muted"> · HTTP ${j.status || "—"} · ${j.bytes} 字节 · ${j.ms}ms</span>
      <div>${escHtml(j.detail || "")}${j.warning ? `<br><span class="muted">${escHtml(j.warning)}</span>` : ""}</div>
      <div class="muted" style="font-size:11.5px;word-break:break-all">${escHtml(j.url || "")}</div>
    </div>`;
    if (j.ok) saveMirrorPref();
  } catch (e) {
    box.innerHTML = `<div style="color:var(--err);font-size:12.5px;margin-top:6px">测试失败：${escHtml(e.message || "")}</div>`;
  }
}

function renderSets() {
  const groups = {};
  (genCatalog.sets||[]).forEach(s => (groups[s.group||"其他"] = groups[s.group||"其他"]||[]).push(s));
  const order = Object.keys(groups);
  genSetOrder = order;             // 下标 → 分组名，供事件委托回查
  $("gen_sets").innerHTML = order.map((g, gi) => {
    const arr = groups[g];
    const usable = arr.filter(s => coreHasSet(s)).length;
    const picked = arr.filter(s => genChecked[s.id]).length;
    const closed = genClosed[g] ? " closed" : "";
    /* 分组名不进内联 onclick：escHtml 不转义引号，名字一旦带引号就会破坏属性。
       统一用 data-gi 下标 + 事件委托（见下方 #gen_sets 监听）。 */
    return `<div class="set-group${closed}" data-gi="${gi}">
      <div class="gh" title="点击展开 / 收起">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>
        <span>${g}</span><span class="cnt">选中 ${picked}/${arr.length} · ${usable} 可用</span>
        <span class="gh-acts">
          <button class="btn sm ghost" data-gact="on" data-gi="${gi}" title="勾选本组内当前可见的可用规则集">全选</button>
          <button class="btn sm ghost" data-gact="off" data-gi="${gi}" title="取消勾选本组全部规则集" ${picked?"":"disabled"}>清空</button>
        </span></div>
      <div class="gb">${arr.map(setRowHTML).join("")}</div></div>`;
  }).join("");
  const picked = Object.keys(genChecked).filter(k => genChecked[k]).length;
  $("gen_sets_count").textContent = `已选 ${picked} / ${(genCatalog.sets||[]).length}`;
}

/* 分组全选 / 清空：只作用于本组内「当前搜索可见 + 本内核可用」的规则集，
   与顶部整体全选保持同一套语义（按钮不触发分组折叠）。 */
function selectGroup(gi, on) {
  if (!genCatalog) return;
  const g = genSetOrder[gi];
  if (!g) return;
  const q = ($("gen_set_search").value || "").trim().toLowerCase();
  (genCatalog.sets || []).forEach(s => {
    if ((s.group || "其他") !== g) return;
    if (!coreHasSet(s)) return;
    if (q && !((s.name || "") + " " + (s.id || "")).toLowerCase().includes(q)) return;
    applySetChoice(s, on);
  });
  renderSets();
  filterSets();
}

$("gen_sets").addEventListener("click", e => {
  const act = e.target.closest("[data-gact]");
  if (act) { e.stopPropagation(); selectGroup(+act.dataset.gi, act.dataset.gact === "on"); return; }
  const h = e.target.closest(".gh");
  if (h) toggleGroup(genSetOrder[+h.parentNode.dataset.gi]);
});

function toggleGroup(g) {
  genClosed[g] = !genClosed[g];
  renderSets();
  filterSets();
}

function toggleSet(id, on) {
  applySetChoice((genCatalog.sets||[]).find(s => s.id === id) || { id }, on);
  renderSets();
  filterSets();
}

/* 全选 / 清空：只作用于当前搜索过滤结果，跳过本内核不可用的规则集（默认策略取目录推荐值） */
function selectSets(on) {
  if (!genCatalog) return;
  const q = ($("gen_set_search").value || "").trim().toLowerCase();
  (genCatalog.sets || []).forEach(s => {
    if (!coreHasSet(s)) return;
    if (q) {
      const hay = ((s.name || "") + " " + (s.id || "")).toLowerCase();
      if (!hay.includes(q)) return;
    }
    applySetChoice(s, on);
  });
  renderSets();
  filterSets();
}

function filterSets() {
  const q = ($("gen_set_search").value || "").trim().toLowerCase();
  document.querySelectorAll("#gen_sets .set-row").forEach(r => {
    r.style.display = (!q || r.dataset.name.includes(q)) ? "" : "none";
  });
  document.querySelectorAll("#gen_sets .set-group").forEach(g => {
    const any = [...g.querySelectorAll(".set-row")].some(r => r.style.display !== "none");
    g.style.display = any ? "" : "none";
  });
}

function applyScenario(id) {
  const s = (genCatalog.scenarios||[]).find(x => x.id === id);
  if (!s) return;
  genScenario = id;
  genChecked = {}; genPolicies = {};
  let skipped = 0;
  (s.sets||[]).forEach(sp => {
    const def = (genCatalog.sets||[]).find(d => d.id === sp.id);
    if (!def) { skipped++; return; }
    if (!coreHasSet(def)) { skipped++; return; }
    genChecked[sp.id] = true;
    genPolicies[sp.id] = sp.policy;
  });
  if ((s.rules||[]).length) {
    const ta = $("gen_rules");
    const cur = ta.value.split("\n").map(x => x.trim()).filter(Boolean);
    (s.rules||[]).forEach(r => { if (cur.indexOf(r) < 0) cur.push(r); });
    ta.value = cur.join("\n");
  }
  renderScenarios(); renderSets(); filterSets();
  $("gen_scn_desc").innerHTML = `<b style="color:var(--text)">${s.name}</b> · ${s.desc||""}` +
    (skipped ? ` <span class="muted">（${skipped} 个规则集在 ${genCore} 上游不可用，已跳过）</span>` : "");
}

function genTargetNote() {
  $("gen_target_note").textContent = GEN_TARGET_NOTES[genTarget] || "";
}

/* 版本选择只在 sing-box 下出现：mihomo 的 url-test / rule-providers 语法
   各版本稳定，不需要让用户在无关的选项上做决定。 */
function renderSBVerSelect() {
  const sel = $("g_sbver");
  sel.innerHTML = SB_VERS.map(o =>
    `<option value="${o.v}"${o.v === genSBVer ? " selected" : ""}>sing-box ${o.v}</option>`).join("");
  onSBVerChange();
}
function onSBVerChange() {
  const sel = $("g_sbver");
  if (sel) genSBVer = sel.value;
  const cur = SB_VERS.find(o => o.v === genSBVer);
  $("g_sbver_note").textContent = cur ? cur.note : "";
  // 换版本后旧结果已失效，避免用户把 1.11 的 JSON 当成 1.14 的下走
  genResult = null;
  $("gen_dl").disabled = true;
  $("gen_verify").disabled = true;
  $("gen_save").disabled = true;
  $("gen_out").classList.add("hide");
  $("gen_vres").classList.add("hide");
  $("gen_check").classList.add("hide");
}
function syncSBVerVisibility() {
  $("g_sbver_wrap").style.display = genCore === "singbox" ? "" : "none";
}

$("g_core_seg").addEventListener("click", e => {
  const b = e.target.closest("button"); if (!b) return;
  genCore = b.dataset.v;
  [...$("g_core_seg").children].forEach(x => x.classList.toggle("on", x === b));
  syncSBVerVisibility();
  renderSets(); filterSets();
  if (genScenario) applyScenario(genScenario);
});
$("g_target_seg").addEventListener("click", e => {
  const b = e.target.closest("button"); if (!b) return;
  genTarget = b.dataset.v;
  [...$("g_target_seg").children].forEach(x => x.classList.toggle("on", x === b));
  genTargetNote();
});

function addCustomSetRow(name, url, beh, pol) {
  const d = document.createElement("div");
  d.className = "cset-row";
  d.style.cssText = "display:flex;gap:8px;margin-bottom:8px;flex-wrap:wrap";
  d.innerHTML = `
    <input class="input cs-name" placeholder="名称" style="width:140px" value="${name || ""}">
    <input class="input cs-url" placeholder="URL（.mrs / .yaml / .srs）" style="flex:1;min-width:220px" value="${url || ""}">
    <select class="input cs-beh" style="width:auto"><option value="domain">domain</option><option value="ipcidr">ipcidr</option><option value="classical">classical</option></select>
    <select class="input cs-pol" style="width:auto"><option>DIRECT</option><option selected>PROXY</option><option>REJECT</option></select>
    <button class="btn sm danger" data-act="removeSelf">删</button>`;
  if (beh) d.querySelector(".cs-beh").value = beh;
  if (pol) d.querySelector(".cs-pol").value = pol;
  $("gen_csets").appendChild(d);
}
let genLast = null, genLastCore = "singbox";

/* buildGenParams 把当前页面上的全部选项收成一个请求体。
   保存配置时原样存进 params，重新生成时原样发回去——所以「一周后想再生成
   同一份」不用凭记忆把入站、规则集、场景重选一遍。 */
function buildGenParams() {
  const ids = [...document.querySelectorAll("#gen_inbounds input:checked")].map(i => +i.value);
  const sets = Object.keys(genChecked).filter(k => genChecked[k])
    // 兜底也要取目录推荐值：万一某条被勾上却没走过 applySetChoice（比如旧参数恢复），
    // 不能退回 PROXY，否则「国内站点走代理」又从这条路绕回来。
    .map(id => ({ id, policy: genPolicies[id] || defaultPolicyOf(id) }));
  const custom_rules = dedupeRules($("gen_rules").value);
  const custom_sets = [...document.querySelectorAll("#gen_csets .cset-row")].map(r => ({
    name: r.querySelector(".cs-name").value.trim(),
    url: r.querySelector(".cs-url").value.trim(),
    behavior: r.querySelector(".cs-beh").value,
    policy: r.querySelector(".cs-pol").value
  })).filter(s => s.name && s.url);
  const body = { core: genCore, target: genTarget, inbound_ids: ids,
    scenario: genScenario || "custom", sets, custom_rules, custom_sets };
  // 规则集下载源：官方时也显式带上，回落逻辑在服务端（空值 = 官方）
  const mir = mirrorArgs();
  body.rule_mirror = mir.mirror || "official";
  if (mir.mirror === "custom") body.rule_mirror_prefix = mir.prefix;
  // 自动直连节点服务器。存进 params，重新生成时才能还原同一份规则——
  // 只写进配置正文、不写进参数的话，一周后重跑会悄悄少掉这组防回环规则。
  body.bypass_server = !!$("gen_bypass").checked;
  if (genCore === "singbox") body.core_version = genSBVer;
  return body;
}

/* 开关变了以后，界面上那份生成结果就过期了：不清掉的话用户点「下载」
   拿到的是旧配置，却以为是刚勾的选项生效了（和换 sing-box 版本同理）。 */
function genOptionChanged() {
  genLast = null;
  ["gen_dl", "gen_verify", "gen_save"].forEach(id => $(id).disabled = true);
  ["gen_out", "gen_vres", "gen_check"].forEach(id => $(id).classList.add("hide"));
  $("gen_fn").textContent = "";
}

async function genGenerate() {
  const core = genCore, target = genTarget;
  const body = buildGenParams();
  if (!body.inbound_ids.length) { toast("请至少选择一个入站", "warn"); return; }
  toast("生成中…", "info");
  try {
    let j;
    if (DEMO) {
      await new Promise(r => setTimeout(r, 600));
      j = { filename: `demo-${target}-${body.scenario}-${core}.${core === "mihomo" ? "yaml" : "json"}`,
            content: demoGenConfig(core, target, genSBVer, body.bypass_server), warnings: [] };
      if (body.bypass_server) j.bypass_servers = ["demo.example.com"];
      if (core === "singbox") {
        const o = SB_VERS.find(x => x.v === genSBVer);
        j.core_version = genSBVer;
        j.version_note = o ? o.note : "";
      }
    } else {
      j = await req("/api/export/config", { method: "POST", body });
    }
    genLast = j;
    genLastCore = core;
    $("gen_out").classList.remove("hide");
    $("gen_out").textContent = j.content;
    $("gen_fn").textContent = j.filename;
    $("gen_dl").disabled = false;
    $("gen_verify").disabled = false;
    $("gen_save").disabled = false;
    // 新配置还没校验过，先清掉上一次的结论，避免张冠李戴
    $("gen_check").classList.add("hide");
    // 版本信息常驻显示，不塞进 toast：用户可能几分钟后才去复制配置，
    // 那时候得还能看出这份 JSON 是给哪个版本写的。
    const vparts = [];
    if (j.core_version) {
      vparts.push(`按 <b>sing-box ${escHtml(j.core_version)}</b> 生成` +
        (j.version_note ? ` · <span style="color:var(--warn)">${escHtml(j.version_note)}</span>` : ""));
    }
    // 规则集走镜像时也留一行：这份配置拿到别的网络里能不能拉到规则集，
    // 取决于这个源，事后要看得见（YAML 头注释里也写了，但 JSON 没有注释）。
    if (j.rule_mirror && j.rule_mirror !== "official") {
      vparts.push(`规则集下载源：<b>${escHtml(j.mirror_label || j.rule_mirror)}</b>`);
    }
    // TUN 下这几行规则决定「能不能连上节点」，把实际直连的地址摆出来，
    // 用户不用去几百行配置里翻，也能发现「公网地址没填、直连的是 127.0.0.1」。
    if (j.bypass_servers && j.bypass_servers.length) {
      vparts.push(`节点服务器直连：<b>${escHtml(j.bypass_servers.join("、"))}</b>`);
    }
    const vres = $("gen_vres");
    if (vparts.length) {
      vres.classList.remove("hide");
      vres.innerHTML = vparts.join(" · ");
    } else {
      vres.classList.add("hide");
    }
    // 生成成功说明这个源至少没被拒绝，值得记住
    if (j.rule_mirror && j.rule_mirror !== "official") saveMirrorPref();
    (j.warnings || []).forEach(w => toast(w, "warn"));
    if (!(j.warnings || []).length) toast("生成完成：" + j.filename);
  } catch(e){
    toast(e.message, "err");
    // 失败原因必须**留在页面上**：toast 三秒就没了，而「为什么生成不出来」
    // 往往要读一会儿（例如「mixed 是本地代理协议，导不出客户端节点」）。
    // 顺便把上一份成功结果清掉——否则「用本机内核校验」还会去验旧配置，
    // 用户会以为刚失败的那次其实通过了。
    genLast = null;
    $("gen_out").classList.remove("hide");
    $("gen_out").textContent = "生成失败：" + e.message;
    $("gen_fn").textContent = "";
    $("gen_vres").classList.add("hide");
    $("gen_check").classList.add("hide");
    $("gen_dl").disabled = true;
    $("gen_verify").disabled = true;
    $("gen_save").disabled = true;
  }
}

/* 用面板上装的内核跑官方 check。这是唯一能证明「这份配置真的能加载」的办法，
   比任何静态检查都硬。但它校验的是**本机装的版本**，未必等于刚选的版本，
   所以结果里必须把实际版本打出来，不能让人误以为验的就是客户端那个版本。 */
async function genVerify() {
  if (!genLast) return;
  const core = genLastCore;
  const box = $("gen_check");
  box.classList.remove("hide");
  box.textContent = "校验中…";
  try {
    let j;
    if (DEMO) {
      await new Promise(r => setTimeout(r, 700));
      j = { ok: true, core, version: "sing-box version " + genSBVer + "（演示）", skipped: false, output: "" };
    } else {
      j = await req("/api/export/verify", { method: "POST", body: { core, content: genLast.content } });
    }
    if (j.skipped) {
      box.innerHTML = `<span style="color:var(--warn)">跳过：${escHtml(j.reason || "面板未配置内核")}</span>`;
      return;
    }
    const who = j.version ? `（${escHtml(j.version)}）` : "";
    if (j.ok) {
      box.innerHTML = `<span style="color:var(--ok)">✓ 内核校验通过</span>${who}` +
        ` <span class="muted">· 只验证能否加载，不联网、不校验节点凭据是否有效</span>`;
    } else {
      box.innerHTML = `<span style="color:var(--err)">✗ 内核拒绝这份配置</span>${who}`;
      if (j.hint) {
        box.innerHTML += `<div class="muted" style="margin-top:4px">${escHtml(j.hint)}</div>`;
      }
      $("gen_out").classList.remove("hide");
      $("gen_out").textContent = (genLast.content || "") +
        "\n\n──────── 校验输出 ────────\n" + (j.output || "（无输出）");
    }
  } catch(e){
    box.innerHTML = `<span style="color:var(--err)">校验失败：${escHtml(e.message)}</span>`;
  }
}
function genDownload() {
  if (!genLast) return;
  const blob = new Blob([genLast.content], { type: genLast.content_type || "text/plain" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob); a.download = genLast.filename;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 3000);
}
function demoGenConfig(core, target, ver, bypass) {
  // 不传时读当前开关：离线演示里「下载已保存配置」也走这里，而它保存时
  // 的开关状态未必等于页面当前状态，所以下面两处调用会显式传值。
  const on = bypass === undefined ? !!$("gen_bypass").checked : !!bypass;
  if (core === "mihomo") return `# Triune Panel 生成 | mihomo | ${target}（演示数据）
${on ? "# 节点服务器直连（避免回环）：demo.example.com\n" : ""}mode: rule
mixed-port: 7890
allow-lan: ${target !== "client"}
tun:
  enable: ${target !== "client"}
  auto-route: true
  dns-hijack:
    - any:53
dns:
  enable: true
  enhanced-mode: fake-ip
proxies:
  - name: demo-hk-user1
    type: vless
    server: demo.example.com
    port: 443
    uuid: a1b2c3d4-1111-2222-3333-444455556666
    tls: true
    udp: true
proxy-groups:
  - name: PROXY
    type: select
    proxies: [Auto, demo-hk-user1]
rule-providers:
  geosite-cn:
    type: http
    behavior: domain
    format: mrs
    url: https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/geosite-cn.mrs
rules:
${on ? "  - DOMAIN,demo.example.com,DIRECT\n" : ""}  - RULE-SET,geosite-cn,DIRECT
  - MATCH,PROXY`;

  /* 演示数据也按版本走真实分支，否则用户切到 1.11 看到的仍是 1.14 的写法，
     演示页就失去了「先看一眼长什么样」的意义。 */
  const [maj, min] = String(ver || "1.14").split(".").map(Number);
  const geq = (a, b) => maj > a || (maj === a && min >= b);
  const newDNS = geq(1, 12), noStack = geq(1, 15);
  const router = target !== "client";

  const dnsServers = newDNS
    ? [{ type: "https", tag: "remote", server: "8.8.8.8", detour: "PROXY" },
       { type: "udp", tag: "local", server: "223.5.5.5", detour: "direct" }]
    : [{ tag: "remote", address: "https://8.8.8.8/dns-query", detour: "PROXY" },
       { tag: "local", address: "223.5.5.5", detour: "direct" }];

  const inbounds = [];
  if (router) {
    const tun = { type: "tun", tag: "tun-in", address: ["172.19.0.1/30"], auto_route: true, strict_route: true };
    if (!noStack) tun.stack = "mixed";   // 1.15 起该字段废弃
    inbounds.push(tun);
  }
  inbounds.push({ type: "mixed", tag: "mixed-in", listen: router ? "0.0.0.0" : "127.0.0.1", listen_port: 7890 });

  const route = {
    rules: [
      { protocol: "dns", action: "hijack-dns" },
      ...(on ? [{ domain: ["demo.example.com"], outbound: "direct" }] : []),
      { rule_set: ["geosite-cn"], outbound: "direct" },
      { ip_cidr: ["192.168.0.0/16", "10.0.0.0/8"], outbound: "direct" }
    ],
    rule_set: [{
      type: "remote", tag: "geosite-cn", format: "binary",
      url: "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs",
      download_detour: "direct", update_interval: "86400s"
    }],
    final: "PROXY",
    auto_detect_interface: true
  };
  if (newDNS) route.default_domain_resolver = { server: "local" };

  return JSON.stringify({
    log: { level: "info", timestamp: true },
    dns: { servers: dnsServers, rules: [{ rule_set: ["geosite-cn"], server: "local" }], final: "remote" },
    inbounds,
    outbounds: [
      { type: "urltest", tag: "Auto", outbounds: ["demo-hk-user1"],
        url: "http://www.gstatic.com/generate_204", interval: "5m" },
      { type: "selector", tag: "PROXY", outbounds: ["Auto", "demo-hk-user1"], default: "Auto" },
      { type: "vless", tag: "demo-hk-user1", server: "demo.example.com", server_port: 443,
        uuid: "a1b2c3d4-1111-2222-3333-444455556666", tls: { enabled: true } },
      { type: "direct", tag: "direct" }
    ],
    route
  }, null, 2);
}

/* ================= 规则标签 ================= */
//
// 标签解决的是「同一组规则每次都要重敲一遍」。两种用法：
//   单选：点标签 → 输入框**替换**成该标签内容（换一组规则用）
//   多选：点标签 → **追加**到现有内容后（去重），再点一次取消该标签
//
// 取消时只移除「该标签独有」的行：如果两行也属于其他已选标签，保留。
// 手写行若恰好与标签行相同，取消会一并移除——去重后本来就只有一份，
// 没法区分来源，这一点在标签栏的 title 里写明了。

let RULE_TAGS = [], tagMode = "single", tagActive = new Set();

// dedupeRules 去空行、去首尾空白、去重（保留首次出现的顺序）。
// 内核不会因为重复行报错，只会白跑一遍匹配，所以在这里就清掉。
//
// 入参既接受多行文本也接受数组：applyTag 里会把「已有行」和「标签行」两个
// 数组合并后再过一遍。注意不能一律 String()——数组会被转成逗号连接的一行，
// 结果所有规则挤成一条，内核解析不了还不报错。
function dedupeRules(input) {
  const lines = Array.isArray(input) ? input : String(input || "").split("\n");
  const seen = new Set(), out = [];
  for (const raw of lines) {
    const s = String(raw).trim();
    if (!s || seen.has(s)) continue;
    seen.add(s); out.push(s);
  }
  return out;
}
function setRules(lines) { $("gen_rules").value = lines.join("\n"); }

async function loadRuleTags() {
  try {
    RULE_TAGS = DEMO ? MDB.ruleTags : await req("/api/rule-tags");
  } catch(e) { RULE_TAGS = []; }
  if (!Array.isArray(RULE_TAGS)) RULE_TAGS = [];
  renderRuleTags();
}

function renderRuleTags() {
  const el = $("gen_tags");
  if (!el) return;
  if (!RULE_TAGS.length) {
    el.innerHTML = `<span class="tag-empty">还没有标签，把常用规则存下来下次直接用</span>`;
    return;
  }
  el.innerHTML = RULE_TAGS.map(t => {
    const on = tagActive.has(t.id);
    return `<span class="tag-chip${on ? " on" : ""}" data-act="applyTag" data-args="${escJson([t.id])}"
      title="${escHtml(t.rules)}">
      <span>${escHtml(t.name)}</span><span class="n">${t.count}</span>
      <span class="x" data-act="deleteTag" data-args="${escJson([event,t.id])}" title="删除这个标签">×</span></span>`;
  }).join("");
}

function applyTag(id) {
  const t = RULE_TAGS.find(x => x.id === id);
  if (!t) return;
  const tagLines = dedupeRules(t.rules);
  const cur = dedupeRules($("gen_rules").value);

  if (tagMode === "single") {
    // 单选：替换。点谁就是谁，不 toggle——想换就点另一个。
    setRules(tagLines);
    tagActive = new Set([id]);
  } else if (tagActive.has(id)) {
    tagActive.delete(id);
    // 其他已选标签也含这些行时不能删
    const keep = new Set();
    tagActive.forEach(a => {
      const o = RULE_TAGS.find(x => x.id === a);
      if (o) dedupeRules(o.rules).forEach(l => keep.add(l));
    });
    const drop = new Set(tagLines);
    setRules(cur.filter(l => !drop.has(l) || keep.has(l)));
  } else {
    tagActive.add(id);
    setRules(dedupeRules(cur.concat(tagLines)));
  }
  renderRuleTags();
}

async function deleteTag(ev, id) {
  ev.stopPropagation();
  const t = RULE_TAGS.find(x => x.id === id);
  if (!t) return;
  if (!await ask("删除标签", `确定删除「${t.name}」？已经生成的配置不受影响。`)) return;
  try {
    if (DEMO) MDB.ruleTags = MDB.ruleTags.filter(x => x.id !== id);
    else await req("/api/rule-tags/" + id, { method: "DELETE" });
    tagActive.delete(id);
    toast("已删除标签「" + t.name + "」");
    await loadRuleTags();
  } catch(e){ toast(e.message, "err"); }
}

function openSaveTag() {
  const lines = dedupeRules($("gen_rules").value);
  if (!lines.length) { toast("输入框里还没有规则", "warn"); return; }
  $("st_name").value = "";
  $("st_meta").textContent = `将保存 ${lines.length} 条规则（重复行已去掉）`;
  openModal("mSaveTag");
  setTimeout(() => $("st_name").focus(), 80);
}

async function saveRuleTag() {
  const name = $("st_name").value.trim();
  if (!name) { toast("请输入标签名称", "warn"); return; }
  const rules = dedupeRules($("gen_rules").value).join("\n");
  if (!rules) { toast("输入框里还没有规则", "warn"); return; }
  try {
    if (DEMO) {
      if (MDB.ruleTags.some(t => t.name === name)) throw new Error("同名标签已存在");
      MDB.ruleTags.push({ id: Math.max(0, ...MDB.ruleTags.map(t => t.id)) + 1,
        name, rules, count: rules.split("\n").length, created_at: "2026-11-01 10:00:00" });
    } else {
      await req("/api/rule-tags", { method: "POST", body: { name, rules } });
    }
    closeModal("mSaveTag");
    toast("标签「" + name + "」已保存");
    await loadRuleTags();
  } catch(e){ toast(e.message, "err"); }
}

if ($("tag_mode_seg")) {
  $("tag_mode_seg").addEventListener("click", e => {
    const b = e.target.closest("button"); if (!b) return;
    tagMode = b.dataset.v;
    [...$("tag_mode_seg").children].forEach(x => x.classList.toggle("on", x === b));
    if (tagMode === "single") { tagActive.clear(); renderRuleTags(); }
  });
}

/* ================= 配置管理 ================= */

const TARGET_LABEL = { client: "客户端", "main-router": "主路由", "side-router": "旁路由" };
let SAVED_CFGS = [];

function saveBlob(blob, filename) {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = filename;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 3000);
}

async function loadSavedConfigs() {
  try {
    SAVED_CFGS = DEMO ? MDB.savedConfigs : await req("/api/configs");
  } catch(e) { SAVED_CFGS = []; }
  if (!Array.isArray(SAVED_CFGS)) SAVED_CFGS = [];
  renderSavedConfigs();
}

function renderSavedConfigs() {
  const q = ($("cfg_search").value || "").trim().toLowerCase();
  const list = SAVED_CFGS.filter(c => !q ||
    (c.name || "").toLowerCase().includes(q) || (c.note || "").toLowerCase().includes(q));
  $("cfgbody").innerHTML = list.map(c => {
    const kern = c.core === "singbox"
      ? "sing-box" + (c.core_version ? " " + escHtml(c.core_version) : "")
      : "mihomo";
    return `<tr>
      <td><b>${escHtml(c.name)}</b>${c.note ? `<div class="muted" style="font-size:12px">${escHtml(c.note)}</div>` : ""}</td>
      <td>${kern}</td>
      <td>${escHtml(TARGET_LABEL[c.target] || c.target || "—")}</td>
      <td class="mono">${fmtBytes(c.size)}</td>
      <td>${escHtml(c.created_by || "—")}</td>
      <td class="muted">${escHtml(c.updated_at || c.created_at || "")}</td>
      <td class="ta-r">
        <button class="btn sm" data-act="downloadConfig" data-args="${escJson([c.id])}">下载</button>
        <button class="btn sm" data-act="regenerateConfig" data-args="${escJson([c.id])}"
                title="用保存时的参数重新生成，节点变了也不用重新点一遍">重新生成</button>
        <button class="btn sm" data-act="editConfig" data-args="${escJson([c.id])}">改名</button>
        <button class="btn sm danger" data-act="deleteConfig" data-args="${escJson([c.id])}">删除</button>
      </td></tr>`;
  }).join("") || `<tr><td colspan="7" class="empty">还没有保存的配置。到「配置生成」页生成后点「保存配置」。</td></tr>`;

  $("cfg_count").textContent = SAVED_CFGS.length
    ? `共 ${SAVED_CFGS.length} 份${q ? `，匹配 ${list.length} 份` : ""}（上限 200 份）`
    : "";
}

async function downloadConfig(id) {
  const c = SAVED_CFGS.find(x => x.id === id);
  if (!c) return;
  const ext = c.core === "mihomo" ? "yaml" : "json";
  try {
    if (DEMO) {
      let by = true;
      try { by = JSON.parse(c.params || "{}").bypass_server !== false; } catch (_) {}
      saveBlob(new Blob([demoGenConfig(c.core, c.target, c.core_version, by)], { type: "text/plain" }),
        `${c.name}.${ext}`);
      toast("已开始下载 " + c.name + "." + ext);
      return;
    }
    // 下载接口要带 Bearer，不能直接 location.href（那样会 401 到登录页）
    const r = await fetch(`/api/configs/${id}/download`, {
      headers: { Authorization: "Bearer " + TOKEN }
    });
    if (!r.ok) {
      let msg = "下载失败（" + r.status + "）";
      try { const j = await r.json(); if (j.error) msg = j.error; } catch(_) {}
      throw new Error(msg);
    }
    saveBlob(await r.blob(), `${c.name}.${ext}`);
    toast("已开始下载 " + c.name + "." + ext);
  } catch(e){ toast(e.message, "err"); }
}

async function deleteConfig(id) {
  const c = SAVED_CFGS.find(x => x.id === id);
  if (!c) return;
  if (!await ask("删除配置", `确定删除「${c.name}」？删除后无法恢复。`)) return;
  try {
    if (DEMO) MDB.savedConfigs = MDB.savedConfigs.filter(x => x.id !== id);
    else await req("/api/configs/" + id, { method: "DELETE" });
    toast("已删除「" + c.name + "」");
    await loadSavedConfigs();
  } catch(e){ toast(e.message, "err"); }
}

function editConfig(id) {
  const c = SAVED_CFGS.find(x => x.id === id);
  if (!c) return;
  $("mSaveCfg").dataset.editId = String(id);
  $("sc_title").textContent = "重命名配置";
  $("sc_ok").textContent = "保存";
  $("sc_name").value = c.name;
  $("sc_note").value = c.note || "";
  $("sc_meta").textContent = `${c.core === "singbox" ? "sing-box " + (c.core_version || "") : "mihomo"} · ` +
    `${TARGET_LABEL[c.target] || c.target || "—"} · ${fmtBytes(c.size)} · 保存于 ${c.created_at || ""}`;
  openModal("mSaveCfg");
  setTimeout(() => { $("sc_name").focus(); $("sc_name").select(); }, 80);
}

/* regenerateConfig 用保存时的参数重跑一遍生成。
   节点/用户可能已经变了，所以结果未必和存的那份一样——先比，再让用户决定
   覆盖还是放弃，而不是悄悄改掉他几个月前存的东西。 */
async function regenerateConfig(id) {
  const c = SAVED_CFGS.find(x => x.id === id);
  if (!c) return;
  let params;
  try { params = JSON.parse(c.params || "{}"); } catch(_) { params = null; }
  if (!params || !Array.isArray(params.inbound_ids) || !params.inbound_ids.length) {
    toast("这份配置没留下可用的生成参数，无法重新生成", "err");
    return;
  }
  toast("重新生成中…", "info");
  try {
    let old, fresh;
    if (DEMO) {
      await new Promise(r => setTimeout(r, 500));
      old = { content: demoGenConfig(c.core, c.target, c.core_version, params.bypass_server) };
      fresh = { content: demoGenConfig(c.core, c.target, c.core_version, params.bypass_server) };
    } else {
      old = await req("/api/configs/" + id);
      fresh = await req("/api/export/config", { method: "POST", body: params });
    }
    if (old.content === fresh.content) {
      toast("重新生成完成，内容与保存的版本一致（节点与参数都没变）");
      return;
    }
    const okOverwrite = await ask("配置有变化",
      `旧版本 ${fmtBytes(old.content.length)} → 新版本 ${fmtBytes(fresh.content.length)}。` +
      `\n\n覆盖已保存的「${c.name}」？选「取消」则保留原样。`);
    if (!okOverwrite) { toast("已保留原版本"); return; }
    if (DEMO) {
      const t = MDB.savedConfigs.find(x => x.id === id);
      t.content = fresh.content; t.size = fresh.content.length;
      t.updated_at = "2026-11-01 10:30:00";
    } else {
      await req("/api/configs/" + id, {
        method: "PUT",
        body: { content: fresh.content, params: JSON.stringify(params) }
      });
    }
    toast("已覆盖「" + c.name + "」");
    await loadSavedConfigs();
  } catch(e){ toast(e.message, "err"); }
}

/* ---- 保存当前生成结果 ---- */

function defaultConfigName() {
  const d = new Date();
  const mm = String(d.getMonth() + 1).padStart(2, "0");
  const dd = String(d.getDate()).padStart(2, "0");
  const core = genCore === "singbox" ? "singbox" + genSBVer : "mihomo";
  return `${TARGET_LABEL[genTarget] || genTarget}-${core}-${mm}${dd}`;
}

function openSaveConfig() {
  if (!genLast) return;
  $("mSaveCfg").dataset.editId = "";
  $("sc_title").textContent = "保存配置";
  $("sc_ok").textContent = "保存";
  $("sc_name").value = defaultConfigName();
  $("sc_note").value = "";
  const c = genLast.content || "";
  const lines = (c.match(/\n/g) || []).length + 1;
  $("sc_meta").textContent =
    `${genCore === "singbox" ? "sing-box " + genSBVer : "mihomo"} · ` +
    `${TARGET_LABEL[genTarget] || genTarget} · ${fmtBytes(c.length)} · ${lines} 行`;
  openModal("mSaveCfg");
  setTimeout(() => { $("sc_name").focus(); $("sc_name").select(); }, 80);
}

async function submitSaveConfig() {
  const name = $("sc_name").value.trim();
  if (!name) { toast("请输入名称", "warn"); return; }
  const note = $("sc_note").value.trim();
  const editId = $("mSaveCfg").dataset.editId;

  try {
    if (editId) {
      if (DEMO) {
        const c = MDB.savedConfigs.find(x => x.id === +editId);
        if (c) { c.name = name; c.note = note; c.updated_at = "2026-11-01 10:20:00"; }
      } else {
        await req("/api/configs/" + editId, { method: "PUT", body: { name, note } });
      }
      closeModal("mSaveCfg");
      toast("已更新");
      await loadSavedConfigs();
      return;
    }

    if (!genLast) { toast("先生成配置", "warn"); return; }
    const params = JSON.stringify(buildGenParams());
    const body = {
      name, note,
      core: genCore,
      core_version: genCore === "singbox" ? genSBVer : "",
      target: genTarget,
      scenario: genScenario || "custom",
      params,
      content: genLast.content
    };
    if (DEMO) {
      MDB.savedConfigs.unshift({
        id: Math.max(0, ...MDB.savedConfigs.map(x => x.id)) + 1,
        name, note, core: body.core, core_version: body.core_version,
        target: body.target, scenario: body.scenario, params,
        content: body.content, size: body.content.length,
        hash: "demo", created_by: ME.user || "admin",
        created_at: "2026-11-01 10:20:00", updated_at: "2026-11-01 10:20:00"
      });
    } else {
      await req("/api/configs", { method: "POST", body });
    }
    closeModal("mSaveCfg");
    toast("已保存到「配置管理」");
    if (CURRENT === "configs") await loadSavedConfigs();
  } catch(e){ toast(e.message, "err"); }
}

/* ---- 用户弹窗 ---- */
async function openClientModal(id) {
  $("cltitle").textContent = id ? "编辑用户" : "新增用户";
  $("cl_name").dataset.editing = id || "";
  const inbs = await req("/api/inbounds").catch(() => []);
  let sel = [], vals = { name:"", cred:"", quota:0, exp:"" };
  if (id) {
    const all = await req("/api/clients");
    const x = all.find(v => v.client.id === id);
    if (x) { sel = x.inbound_ids || [];
      vals = { name:x.client.name, cred:x.client.credential, quota:x.client.quota_gb||0, exp:x.client.expires_at ? fmtLocalDT(x.client.expires_at) : "" }; }
  }
  $("cl_inbs").innerHTML = inbs.length ? inbs.map(i =>
    `<label><input type="checkbox" value="${i.id}" ${sel.includes(i.id)?"checked":""}> ${i.name} <span class="muted">(${i.core}:${i.port})</span></label>`).join("")
    : '<span class="muted">暂无入站，请先创建入站</span>';
  $("cl_name").value = vals.name; $("cl_cred").value = vals.cred;
  $("cl_quota").value = vals.quota; $("cl_exp").value = vals.exp;
  openModal("mClient");
  setTimeout(() => $("cl_name").focus(), 120);
}
function fmtLocalDT(iso){ return iso ? iso.slice(0,16) : ""; }
async function saveClient() {
  const ids = [...document.querySelectorAll("#cl_inbs input:checked")].map(i => +i.value);
  const exp = $("cl_exp").value ? new Date($("cl_exp").value).toISOString() : null;
  const editing = $("cl_name").dataset.editing;
  try {
    if (editing) {
      const all = await req("/api/clients");
      const x = all.find(v => v.client.id === +editing);
      const c = x.client;
      c.name = $("cl_name").value; c.credential = $("cl_cred").value;
      c.quota_gb = +$("cl_quota").value || 0; c.expires_at = exp;
      await req("/api/clients/" + editing, { method:"PUT", body:c });
      await req("/api/clients/" + editing + "/inbounds", { method:"PUT", body:{ inbound_ids:ids } });
      toast("用户已更新");
    } else {
      await req("/api/clients", { method:"POST", body:{ name:$("cl_name").value, credential:$("cl_cred").value, quota_gb:+$("cl_quota").value||0, expires_at:exp, inbound_ids:ids } });
      toast("用户已创建");
    }
    closeModal("mClient"); loadClients();
  } catch (e) { toast(e.message, "err"); }
}
async function genUuid(){ try { const j = await req("/api/tools/uuid"); $("cl_cred").value = j.uuid; } catch(e){ toast(e.message,"err"); } }
async function toggleClient(id, en) {
  try { const all = await req("/api/clients"); const x = all.find(v=>v.client.id===id);
    const c = x.client; c.enabled = en;
    await req("/api/clients/"+id, { method:"PUT", body:c });
    toast(en ? "用户已启用" : "用户已停用", en ? "ok" : "warn");
    toast("开关变更需「应用配置」后才影响内核", "info");
  } catch(e){ toast(e.message,"err"); }
}
async function delClient(id) {
  if (!(await ask("删除用户", "删除后该用户的订阅立即失效（运行配置在下次应用后更新）。确定删除？"))) return;
  try { await req("/api/clients/"+id, { method:"DELETE" }); toast("已删除"); loadClients(); } catch(e){ toast(e.message,"err"); }
}
function copySub(token) {
  const url = location.origin + "/sub/" + token;
  (navigator.clipboard ? navigator.clipboard.writeText(url) : Promise.reject())
    .then(() => toast("订阅链接已复制"))
    .catch(() => viewContent("订阅链接", url));
}

/* ---- 二维码分享 ---- */
let QR_LINKS = [], QR_IDX = 0;
function showQRModal(links, title) {
  QR_LINKS = (links || []).filter(Boolean);
  if (!QR_LINKS.length) { toast("没有可生成二维码的链接", "err"); return; }
  $("qr_name").textContent = title || "";
  QR_IDX = 0;
  if (QR_LINKS.length > 1) {
    $("qr_list").innerHTML = QR_LINKS.map((l, i) => {
      let name = ("节点 " + (i + 1));
      try { name = decodeURIComponent((l.split("#")[1] || name)).slice(0, 24); } catch (e) {}
      return `<button class="btn sm${i === 0 ? " primary" : ""}" data-act="qrPick" data-args="${escJson([i])}" data-el="1">${name}</button>`;
    }).join("");
  } else $("qr_list").innerHTML = "";
  qrRender();
  openModal("mQR");
}
function qrPick(i, btn) {
  QR_IDX = i;
  document.querySelectorAll("#qr_list .btn").forEach(b => b.classList.remove("primary"));
  if (btn) btn.classList.add("primary");
  qrRender();
}
function qrRender() {
  const text = QR_LINKS[QR_IDX] || "";
  $("qr_hint").textContent = text;
  const cv = $("qr_canvas");
  const ctx = cv.getContext("2d");
  try {
    const qr = qrcode(0, "M");
    qr.addData(text);
    qr.make();
    const n = qr.getModuleCount(), size = 264, cell = Math.floor(size / n);
    const off = Math.floor((size - cell * n) / 2);
    cv.width = cv.height = size;
    ctx.fillStyle = "#fff"; ctx.fillRect(0, 0, size, size);
    ctx.fillStyle = "#111";
    for (let r = 0; r < n; r++) for (let c = 0; c < n; c++)
      if (qr.isDark(r, c)) ctx.fillRect(off + c * cell, off + r * cell, cell, cell);
  } catch (e) {
    cv.width = cv.height = 264;
    ctx.fillStyle = "#fff"; ctx.fillRect(0, 0, 264, 264);
    $("qr_hint").textContent = "链接过长无法生成二维码，请使用「复制链接」：" + text;
  }
}
function qrDownload() {
  const a = document.createElement("a");
  a.href = $("qr_canvas").toDataURL("image/png");
  a.download = "node-qr-" + (QR_IDX + 1) + ".png";
  document.body.appendChild(a); a.click(); a.remove();
  toast("二维码已保存");
}
function qrCopy() {
  const text = QR_LINKS[QR_IDX] || "";
  (navigator.clipboard ? navigator.clipboard.writeText(text) : Promise.reject())
    .then(() => toast("链接已复制"))
    .catch(() => viewContent("节点链接", text));
}
// 用户订阅二维码：真实模式扫订阅链接（App 内自动拉取全部节点）；演示模式给示例节点
function userQRSub(token, name) {
  if (DEMO) return showQRModal(
    ["vless://a1b2c3d4-1111-2222-3333-444455556666@demo.example.com:443?security=reality&sni=www.apple.com&pbk=demo-pubkey&sid=ab12&fp=chrome#" + name],
    name + "（演示节点）");
  showQRModal([location.origin + "/sub/" + token], name + " 的订阅");
}

async function exportInboundQR() {
  try {
    const text = await exportLinks();
    $("ex_links").value = text;
    const lines = text.split("\n").map(s => s.trim()).filter(s => s && !s.startsWith("#"));
    showQRModal(lines, $("ex_name").textContent);
  } catch (e) { toast(e.message, "err"); }
}

/* ---- 订阅令牌重置 ---- */
async function resetSubToken(id) {
  if (!(await ask("重置订阅令牌", "该用户当前订阅链接将立即失效（客户端会拉取失败），需要把新链接重新发给用户。确定重置？"))) return;
  try {
    await req("/api/clients/" + id + "/reset-token", { method: "POST" });
    toast("订阅令牌已重置，旧链接已失效", "warn");
    loadClients();
  } catch (e) { toast(e.message, "err"); }
}

/* ---- 入站自检 ---- */
let CK_ID = null;
function openCheckModal(id) { CK_ID = id; $("ck_body").innerHTML = '<span class="muted">检测中…</span>'; openModal("mCheck"); checkInboundRun(); }
async function checkInboundRun() {
  if (!CK_ID) return;
  $("ck_body").innerHTML = '<span class="muted">检测中…</span>';
  try {
    const j = await req("/api/inbounds/" + CK_ID + "/check");
    $("ck_name").textContent = (j.inbound || {}).name || "";
    $("ck_body").innerHTML = (j.checks || []).map(c =>
      `<div style="display:flex;gap:9px;padding:8px 0;border-bottom:1px solid var(--line)">
        <span style="flex:none">${c.ok ? (c.warn ? "🟡" : "🟢") : "🔴"}</span>
        <div style="min-width:0"><b style="font-size:13.5px">${c.name}</b>
        <div class="muted" style="font-size:12.5px;margin-top:2px;word-break:break-all">${c.detail}</div></div></div>`).join("") +
      `<div style="margin-top:10px;font-weight:600;color:${j.all_ok ? "var(--ok)" : "var(--warn)"}">${j.all_ok ? "✓ 全部通过" : "存在未通过项，请按提示处理"}</div>`;
  } catch (e) {
    $("ck_body").innerHTML = `<span style="color:var(--err)">检测失败: ${e.message}</span>`;
  }
}

/* ---- 配置 diff ---- */
function escHtml(s) { return (s || "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }
async function showDiff() {
  const core = $("ap_core").value;
  try {
    const j = await req("/api/config/diff", { method: "POST", body: { core } });
    const stat = j.old_missing
      ? "尚无运行中的配置文件，应用后将全部新建"
      : `共 ${j.changed} 行变更` + (j.changed === 0 ? " —— 当前已是最新，无需应用" : "");
    const pre = apResult("变更对比 · " + core, stat);
    pre.innerHTML = (j.diff || []).map(o => {
      if (o.op === "+") return `<span style="color:var(--ok)">+ ${escHtml(o.text)}</span>`;
      if (o.op === "-") return `<span style="color:var(--err)">- ${escHtml(o.text)}</span>`;
      return `<span style="opacity:.5">  ${escHtml(o.text)}</span>`;
    }).join("\n");
  } catch (e) { toast(e.message, "err"); }
}

/* ---- 订阅 URL 聚合 ---- */
async function importSubURL() {
  const url = $("iu_url").value.trim();
  if (!url) { toast("请填写订阅 URL", "err"); return; }
  try {
    const j = await req("/api/external-nodes/import-url", { method: "POST", body: { url, prefix: $("iu_prefix").value.trim() } });
    const ok = (j.created || []).length, sk = (j.skipped || []).length;
    $("iumsg").innerHTML =
      (j.created || []).map(s => `<div style="color:var(--ok)">✓ ${s}</div>`).join("") +
      (j.skipped || []).map(s => `<div class="muted">= ${s}</div>`).join("") +
      (j.failed || []).map(s => `<div style="color:var(--err)">✕ ${s}</div>`).join("") +
      (j.parse_errors || []).map(s => `<div style="color:var(--err)">✕ 解析失败: ${s}</div>`).join("");
    if (ok) { toast(`已导入 ${ok} 个外部节点`); loadExternal(); }
    else if (sk) toast(`无新增节点（${sk} 个已存在）`, "info");
  } catch (e) { toast(e.message, "err"); }
}

/* ---- 入站弹窗 ---- */
/* ---- 入站规格矩阵（与各内核渲染器的支持范围保持同步）---- */
const IB_SPECS = {
  singbox: { protocols:["vless","vmess","trojan","shadowsocks","hysteria2","tuic","anytls","mixed","socks","http","direct"],
             transports:["tcp","ws","grpc","h2","httpupgrade","quic"], tls:["none","tls","reality"] },
  xray:    { protocols:["vless","vmess","trojan","shadowsocks","socks","http","dokodemo-door","wireguard"],
             transports:["tcp","ws","grpc","httpupgrade","xhttp","kcp"], tls:["none","tls","reality"] },
  mihomo:  { protocols:["mixed","socks","http","shadowsocks","vmess","trojan","tuic","hysteria2"],
             transports:["tcp"], tls:["none","tls"] }
};
const IB_PROTO_LABEL = {
  vless:"VLESS（推荐，配 Reality 免域名）", vmess:"VMess", trojan:"Trojan（需域名+证书）",
  shadowsocks:"Shadowsocks（单密码共享）", hysteria2:"Hysteria2（QUIC/UDP，弱网快）",
  tuic:"TUIC（QUIC/UDP 低延迟）", anytls:"AnyTLS（新协议）",
  mixed:"Mixed（HTTP+SOCKS 本机代理）", socks:"SOCKS5（本机代理/多用户）",
  http:"HTTP 代理（本机/多用户）", "dokodemo-door":"dokodemo-door（端口转发）",
  direct:"direct（端口转发，sing-box）", wireguard:"WireGuard（原始参数透传）"
};
const IB_PROTO_HINT = {
  vless:"最常用组合：Reality 传输 + 443 端口，无需域名和证书，客户端伪装成访问真实网站。",
  vmess:"老牌协议；明文传输易被识别，建议配 WS+TLS 使用。",
  trojan:"密码认证 + 标准 TLS 流量，必须有域名和证书（可在下方填入，或用面板 ACME 签发）。",
  shadowsocks:"对称加密；注意多用户共享同一端口和加密方法。",
  hysteria2:"基于 QUIC（UDP 端口），高丢包/移动网络下速度优势明显；确保防火墙/安全组放行的是 UDP。",
  tuic:"基于 QUIC（UDP 端口），低延迟；确保放行 UDP。",
  anytls:"TLS 伪装的新一代协议，仅 sing-box 支持。",
  mixed:"给本机或局域网设备用的混合代理端口，公网机器建议配合强密码。",
  socks:"本机 SOCKS5 代理端口；多用户时每行一个「用户名:密码」。",
  http:"本机 HTTP 代理端口；多用户时每行一个「用户名:密码」。",
  "dokodemo-door":"端口转发：把本机端口原样转到目标地址，不需要用户与订阅。",
  direct:"端口转发（sing-box 的 direct 入站）：把本机端口转到目标地址。",
  wireguard:"WireGuard 入站参数（secretKey / peers）以原始 settings 透传，只支持 xray。"
};
const IB_PORT_DEFAULT = { vless:443, vmess:10086, trojan:443, shadowsocks:8388,
  hysteria2:36712, tuic:443, anytls:443, mixed:2080, socks:1080, http:8080,
  "dokodemo-door":9000, direct:9000, wireguard:51820 };

// ---- 兼容内核（mihomo）可见性 ----
// mihomo 的协议是 sing-box 的真子集且缺 vless / reality，选它是纯降级，
// 所以默认不出现在「新建入站」和「切换目标」里。但两件事必须守住：
//   1. 已存在的 mihomo 入站要能在下拉里看到自己 —— 否则 select 赋不上值会
//      静默回落成第一项，用户点保存就把内核换了，界面上毫无提示；
//   2. 开关改了要立刻生效，不必刷新页面。
let __compatCore = null;           // null = 还不知道，首用时去拉
async function compatCoreEnabled() {
  if (__compatCore !== null) return __compatCore;
  try {
    const j = await req("/api/settings");
    __compatCore = j.show_compat_core === "1";
  } catch (e) { __compatCore = false; }
  return __compatCore;
}
async function saveCompatCore(on) {
  try {
    await req("/api/settings", { method:"PUT", body:{ show_compat_core: on ? "1" : "0" } });
    __compatCore = on;
    toast(on ? "已显示 mihomo：新建入站时可选" : "已隐藏 mihomo：新建入站只提供 sing-box / xray");
  } catch (e) {
    $("s_compat").checked = !on;   // 存不上就把勾退回去，别让界面撒谎
    toast(e.message, "err");
  }
}
function coreChoices(allowMihomo) {
  return allowMihomo ? ["singbox", "xray", "mihomo"] : ["singbox", "xray"];
}
// fillCoreSelect 重建选项并尽量保住当前值
function fillCoreSelect(el, allowMihomo) {
  const cur = el.value;
  el.innerHTML = coreChoices(allowMihomo).map(v => `<option value="${v}">${v}</option>`).join("");
  if (cur && coreChoices(allowMihomo).includes(cur)) el.value = cur;
}

function ibSpec() { return IB_SPECS[$("ib_core").value] || IB_SPECS.singbox; }
function ibProto() { return $("ib_protocol").value; }
function ibGetSettings() { try { return JSON.parse($("ib_settings").value||"{}") || {}; } catch(e){ return {}; } }
function ibPutSettings(s) { $("ib_settings").value = JSON.stringify(s, null, 2); }

function ibRecommendedTLS(core, proto) {
  if (proto==="trojan"||proto==="hysteria2"||proto==="tuic"||proto==="anytls") return "tls";
  if (proto==="vless") return IB_SPECS[core].tls.includes("reality") ? "reality" : "none";
  return "none";
}

// ibFillSelects 按内核刷新 协议/传输/TLS 三个下拉的可选项
function ibFillSelects() {
  const sp = ibSpec();
  $("ib_protocol").innerHTML = sp.protocols.map(p=>`<option value="${p}">${IB_PROTO_LABEL[p]||p}</option>`).join("");
  $("ib_transport").innerHTML = sp.transports.map(t=>`<option>${t}</option>`).join("");
  $("ib_tls").innerHTML = sp.tls.map(t=>`<option>${t}</option>`).join("");
}

// ibOnCore 内核切换：保留仍受支持的协议/传输/TLS，否则换成推荐值，再联动动态字段
function ibOnCore(keepProto) {
  const sp = ibSpec();
  const cur = { proto: ibProto(), tr: $("ib_transport").value, tls: $("ib_tls").value };
  ibFillSelects();
  $("ib_protocol").value = sp.protocols.includes(cur.proto) ? cur.proto : sp.protocols[0];
  $("ib_transport").value = sp.transports.includes(cur.tr) ? cur.tr : "tcp";
  $("ib_tls").value = sp.tls.includes(cur.tls) ? cur.tls : ibRecommendedTLS($("ib_core").value, ibProto());
  if (!keepProto) ibSuggestNamePort();
  ibDynamic(true);
}

function ibOnProto() {
  // 协议切换：重置为该协议推荐的 TLS，端口重推
  $("ib_tls").value = ibRecommendedTLS($("ib_core").value, ibProto());
  ibSuggestNamePort();
  ibDynamic(true);
}

// ibOnTLS 切换 TLS：清掉不再适用的键；新建入站切到 Reality 时把短 ID 补上。
// 只有「新建」才补：编辑已有入站时，空的 shortIds 可能是用户有意为之
// （客户端确实不带短 ID），自动填进去会把在用的配置改掉。
function ibOnTLS() {
  const tls = $("ib_tls").value;
  const editing = !!$("ib_name").dataset.editing;
  if (tls === "reality" && !editing) {
    const s = ibGetSettings();
    const cur = Array.isArray(s.short_ids) ? s.short_ids.join("") : String(s.short_ids || s.short_id || "");
    if (!cur.trim()) {
      Object.assign(s, realityDefaultSettings());
      ibPutSettings(s);
    }
  }
  ibDynamic(true);
}

// ibSuggestNamePort 自动名称与推荐端口（端口冲突自动顺延）
function ibSuggestNamePort() {
  const used = new Set((window.__ibAll||[]).map(i=>+i.port));
  let port = IB_PORT_DEFAULT[ibProto()] || 8443;
  while (used.has(port)) port++;
  $("ib_port").value = port;
  const tls = $("ib_tls").value;
  const suffix = tls==="reality" ? "-reality" : tls==="tls" ? "-tls" : $("ib_transport").value==="ws" ? "-ws" : "";
  if (!$("ib_name").value || $("ib_name").dataset.auto === "1")
    $("ib_name").value = ibProto() + suffix + "-" + port;
  $("ib_name").dataset.auto = "1";
}

/* 入站表单的下拉候选：能选就不让人手打，手打一定会打错大小写和分隔符 */
const IB_ALPN = [["", "不指定（内核默认）"], ["h2,http/1.1", "h2 + http/1.1"], ["http/1.1", "http/1.1"], ["h2", "h2"], ["h3", "h3"]];
const IB_SS_METHODS = [
  ["aes-256-gcm", "aes-256-gcm（默认）"], ["aes-128-gcm", "aes-128-gcm"],
  ["chacha20-ietf-poly1305", "chacha20-ietf-poly1305"],
  ["2022-blake3-aes-256-gcm", "2022-blake3-aes-256-gcm（多用户）"],
  ["2022-blake3-aes-128-gcm", "2022-blake3-aes-128-gcm"],
  ["2022-blake3-chacha20-poly1305", "2022-blake3-chacha20-poly1305"],
  ["none", "none（不加密，仅内网）"]
];
const IB_SS_NET = [["tcp,udp", "TCP + UDP（默认）"], ["tcp", "仅 TCP"], ["udp", "仅 UDP"]];
const IB_HANDSHAKE_SITES = ["www.apple.com", "www.microsoft.com", "www.cloudflare.com",
  "addons.mozilla.org", "www.bing.com", "www.yahoo.com", "www.samsung.com",
  "gateway.icloud.com", "www.nvidia.com", "www.amd.com", "www.lovelive-anime.jp"];
/* spiderX 没有「正确值」这回事：它只是握手成功后客户端去目标站点上「爬」的路径，
   用来模仿真实浏览。固定填 / 是最常见的默认值，也正好是最好认的指纹，
   所以新建时直接给一串随机的——想手填就在输入框里改。 */
function realityRandSpider() {
  const chars = "abcdefghijklmnopqrstuvwxyz0123456789";
  const b = new Uint8Array(12);
  crypto.getRandomValues(b);
  return "/" + Array.from(b, x => chars[x % chars.length]).join("");
}

/* 新建 Reality 入站的默认短 ID 与 spiderX（参考 3x-ui 的做法）。
   为什么不留空：
     - 留空时 xray 的 shortIds 就是 [""]，等于放行「不带短 ID」的客户端——
       新建入站默认开这个口子没有道理，要留空可以自己删掉；
     - 给多个是为了轮换：一个客户端分一个，泄了只换那一个，不用全员重发配置；
     - 长度都取偶数（8 或 16 个十六进制字符）：xray 只认十六进制、且必须成对。 */
function realityRandHex(bytes) {
  const b = new Uint8Array(bytes);
  crypto.getRandomValues(b);
  return Array.from(b, x => x.toString(16).padStart(2, "0")).join("");
}
function realityDefaultSettings() {
  return { short_ids: [realityRandHex(8), realityRandHex(8), realityRandHex(4)], spider_x: realityRandSpider() };
}

// ibDynamic 按协议/传输/TLS 渲染需要的动态字段；表单值写回 settings
function ibDynamic(userTriggered) {
  const core = $("ib_core").value, proto = ibProto(), tr = $("ib_transport").value, tls = $("ib_tls").value;
  // 兼容内核提示：让人在选的时候就看见代价，而不是建完发现没有 vless
  const cw = $("ib_core_warn");
  if (cw) {
    if (core === "mihomo") {
      cw.textContent = "mihomo 是兼容内核：不支持 vless / reality / anytls，建议改用 sing-box";
      cw.classList.remove("hide");
    } else { cw.textContent = ""; cw.classList.add("hide"); }
  }
  let s = ibGetSettings();
  if (userTriggered) {
    // 切换走后清理不再适用的键，避免残留脏配置
    const pathTr = ["ws","h2","httpupgrade","xhttp"];
    if (!pathTr.includes(tr)) { delete s.path; delete s.ws_host; }
    if (tr !== "grpc") delete s.service_name;
    if (tr !== "xhttp") delete s.xhttp_mode;
    if (tr !== "kcp") { delete s.kcp_seed; delete s.kcp_header; }
    if (tr !== "quic") { delete s.quic_security; delete s.quic_key; delete s.quic_header; }
    if (tr !== "tcp") delete s.accept_proxy_protocol;
    if (!["socks","http"].includes(proto)) { delete s.account_auth; delete s.udp; }
    if (!["dokodemo-door","direct"].includes(proto)) { delete s.dd_address; delete s.dd_port; delete s.dd_network; delete s.dd_follow_redirect; }
    if (tls !== "tls") { delete s.server_name; delete s.certificate_path; delete s.key_path; delete s.alpn; delete s.certificate_pem; delete s.key_pem; }
    if (tls !== "reality") { delete s.handshake_server; delete s.handshake_port; delete s.server_names; delete s.private_key; delete s.pbk; delete s.short_id; delete s.short_ids; delete s.spider_x; delete s.max_time_diff; }
    if (proto !== "shadowsocks") { delete s.method; delete s.ss_network; }
    if (proto !== "vless") delete s.flow;
  }
  const esc = v => String(v == null ? "" : v).replace(/&/g,"&amp;").replace(/"/g,"&quot;").replace(/</g,"&lt;");
  const sel = (id, pairs, cur) => `<select id="${id}" class="input">` + pairs.map(([v,n]) =>
    `<option value="${esc(v)}"${String(cur||"")===v?" selected":""}>${n}</option>`).join("") + `</select>`;
  let rows = "";

  // ---- 传输层 ----
  // ws / h2 / httpupgrade / xhttp 都用 path + Host，只是内核里叫法不同
  if (["ws","h2","httpupgrade","xhttp"].includes(tr)) {
    const def = s.path || (tr === "ws" ? ("/ws-" + Math.random().toString(36).slice(2,6)) : "/");
    rows += `<div><label>${tr === "ws" ? "WS 路径" : "路径 path"}</label><input id="ib_f_path" class="input" value="${esc(def)}"></div>`;
    rows += `<div><label>Host 头 <span class="muted" style="font-weight:400">（CDN 前置/虚拟主机时填）</span></label>
      <input id="ib_f_wshost" class="input" value="${esc(s.ws_host)}" placeholder="cdn.example.com"></div>`;
  }
  if (tr === "grpc") {
    rows += `<div><label>gRPC 服务名</label><input id="ib_f_svc" class="input" value="${esc(s.service_name)}" placeholder="可留空"></div>`;
  }
  if (tr === "xhttp") {
    rows += `<div><label>xhttp 模式</label>${sel("ib_f_xhmode",
      [["auto","auto（默认）"],["packet-up","packet-up"],["stream-up","stream-up"],["stream-one","stream-one"]], s.xhttp_mode || "auto")}</div>`;
  }
  if (tr === "kcp") {
    rows += `<div><label>KCP seed <span class="muted" style="font-weight:400">（客户端必须一致）</span></label>
      <input id="ib_f_kcpseed" class="input" value="${esc(s.kcp_seed)}" placeholder="留空则随机"></div>`;
    rows += `<div><label>KCP 伪装头</label>${sel("ib_f_kcpheader",
      [["","none"],["srtp","srtp"],["utp","utp"],["wechat-video","wechat-video"],["dtls","dtls"],["wireguard","wireguard"]], s.kcp_header || "")}</div>`;
  }
  if (tr === "quic") {
    rows += `<div><label>QUIC 加密</label>${sel("ib_f_qsecurity",
      [["none","none"],["aes-128-gcm","aes-128-gcm"],["chacha20-poly1305","chacha20-poly1305"]], s.quic_security || "none")}</div>`;
    rows += `<div><label>QUIC key</label><input id="ib_f_qkey" class="input" value="${esc(s.quic_key)}" placeholder="加密不为 none 时必填"></div>`;
    rows += `<div><label>QUIC 伪装头</label>${sel("ib_f_qheader",
      [["","none"],["srtp","srtp"],["utp","utp"],["wechat-video","wechat-video"],["dtls","dtls"],["wireguard","wireguard"]], s.quic_header || "")}</div>`;
  }
  if (tr === "tcp" && core === "xray") {
    rows += `<div><label>PROXY protocol <span class="muted" style="font-weight:400">（前面有 nginx/haproxy 时开）</span></label>
      ${sel("ib_f_pp", [["0","否"],["1","是"]], String(s.accept_proxy_protocol||"0"))}</div>`;
  }

  // ---- 协议级 ----
  if (proto === "vless") {
    rows += `<div><label>Flow</label>${sel("ib_f_flow",
      [["", "无"], ["xtls-rprx-vision", "xtls-rprx-vision（Reality/TLS 推荐）"]], s.flow)}</div>`;
  }
  if (proto === "shadowsocks") {
    rows += `<div><label>加密方法</label>${sel("ib_f_method", IB_SS_METHODS, s.method || "aes-256-gcm")}</div>`;
    rows += `<div><label>网络</label>${sel("ib_f_ssnet", IB_SS_NET, s.ss_network || "tcp,udp")}</div>`;
  }
  if (proto === "socks" || proto === "http") {
    rows += `<div><label>认证方式</label>${sel("ib_f_auth",
      [["noauth","免认证（内网/本机）"],["password","用户名 + 密码"]], s.account_auth || "noauth")}</div>`;
    if (proto === "socks") {
      rows += `<div><label>UDP 转发</label>${sel("ib_f_udp",
        [["0","关闭"],["1","开启（需客户端支持）"]], String(s.udp ? 1 : 0))}</div>`;
    }
  }
  if (proto === "wireguard") {
    rows += `<div class="full"><div class="muted" style="font-size:12.5px;line-height:1.55">
      WireGuard 的 <code>secretKey</code> / <code>peers</code> / <code>mtu</code> 填在下方「透传 Settings」里（JSON 对象），
      面板原样交给 xray，不做结构化校验。
    </div></div>`;
  }
  if (proto === "dokodemo-door" || proto === "direct") {
    rows += `<div><label>目标地址</label><input id="ib_f_ddaddr" class="input" value="${esc(s.dd_address)}" placeholder="1.1.1.1"></div>`;
    rows += `<div><label>目标端口</label><input id="ib_f_ddport" class="input" type="number" min="1" max="65535" value="${esc(s.dd_port)}" placeholder="80"></div>`;
    rows += `<div><label>网络</label>${sel("ib_f_ddnet", [["tcp","TCP"],["udp","UDP"],["tcp,udp","TCP + UDP"]], s.dd_network || "tcp")}</div>`;
    if (core === "xray") {
      rows += `<div><label>跟随目标地址</label>${sel("ib_f_ddfollow",
        [["0","否（固定转发到上面填的地址）"],["1","是（按客户端请求的地址转发）"]], String(s.dd_follow_redirect ? 1 : 0))}</div>`;
    }
  }
  $("ib_dyn_row").style.display = rows ? "" : "none";
  $("ib_dyn_inner").innerHTML = rows;

  // ---- TLS ----
  let tlsRows = "";
  if (tls === "tls") {
    tlsRows += `<div><label>TLS 域名 SNI</label><input id="ib_f_sni" class="input" value="${esc(s.server_name)}" placeholder="panel.example.com"></div>`;
    tlsRows += `<div><label>ALPN</label>${sel("ib_f_alpn", IB_ALPN, s.alpn || "")}</div>`;
    tlsRows += `<div><label>证书路径 <span class="muted" style="font-weight:400">（留空可用面板 ACME）</span></label><input id="ib_f_cert" class="input" value="${esc(s.certificate_path)}" placeholder="/opt/triune-panel/data/certs/fullchain.pem"></div>`;
    tlsRows += `<div><label>私钥路径</label><input id="ib_f_key" class="input" value="${esc(s.key_path)}" placeholder="/opt/triune-panel/data/certs/privkey.pem"></div>`;
    // 没有现成文件时可以直接粘贴 PEM：保存时后端会落盘成文件，
    // 这样 sing-box（只认路径）也能用，不用先手动 scp 证书上机。
    tlsRows += `<div class="full"><details>
      <summary class="muted" style="cursor:pointer;font-size:12.5px">没有证书文件？直接粘贴 PEM（保存时自动写入 data/certs/）</summary>
      <div class="frm" style="margin-top:8px">
        <div class="full"><label>证书 PEM</label><textarea id="ib_f_certpem" class="input mono" style="min-height:60px;font-size:11.5px" placeholder="-----BEGIN CERTIFICATE-----">${esc(s.certificate_pem)}</textarea></div>
        <div class="full"><label>私钥 PEM</label><textarea id="ib_f_keypem" class="input mono" style="min-height:60px;font-size:11.5px" placeholder="-----BEGIN PRIVATE KEY-----">${esc(s.key_pem)}</textarea></div>
      </div>
    </details></div>`;
  }
  if (tls === "reality") {
    // 目标择优：dest 决定了「每个客户端每次握手要多花多久」，只能从这台服务器
    // 实测，所以把测速做进表单，而不是让人凭印象填个大站。
    tlsRows += `<div class="full"><label>握手目标 dest
        <span class="muted" style="font-weight:400">（客户端伪装成访问它；越近越快）</span></label>
      <div style="display:flex;gap:8px;flex-wrap:wrap">
        <input id="ib_f_handshake" class="input" list="ib_sites" style="flex:1;min-width:180px" value="${esc(s.handshake_server || "www.apple.com")}" placeholder="www.apple.com">
        <button class="btn sm" data-act="openDestPick">目标择优</button>
      </div>
      <datalist id="ib_sites">${IB_HANDSHAKE_SITES.map(d=>`<option value="${d}">`).join("")}</datalist>
      <span class="muted" style="font-size:11.5px">实测这台服务器到各目标的 TLS 1.3 握手延迟与证书，挑最快的那个</span></div>`;
    tlsRows += `<div><label>握手端口</label><input id="ib_f_hsport" class="input" type="number" min="1" max="65535" value="${esc(s.handshake_port || 443)}"></div>`;
    tlsRows += `<div><label>SNI serverNames <span class="muted" style="font-weight:400">（逗号分隔多个）</span></label>
      <input id="ib_f_snames" class="input" value="${esc(s.server_names || s.server_name || "www.apple.com")}" placeholder="www.apple.com,apple.com"></div>`;
    tlsRows += `<div><label>shortIds <span class="muted" style="font-weight:400">（逗号分隔；留空=允许空 ID）</span></label>
      <input id="ib_f_shortids" class="input mono" value="${esc(Array.isArray(s.short_ids) ? s.short_ids.join(",") : (s.short_ids || s.short_id || ""))}" placeholder="ab12,cd34"></div>`;
    tlsRows += `<div><label>spiderX <span class="muted" style="font-weight:400">（握手后爬的路径）</span></label>
      <div style="display:flex;gap:6px">
        <input id="ib_f_spider" class="input mono" style="flex:1;min-width:0" value="${esc(s.spider_x || "/")}" placeholder="/abc123">
        <button class="btn sm" data-act="genSpider" title="重新生成一个随机路径">随机</button>
      </div>
      <span class="muted" style="font-size:11.5px">客户端握手成功后去目标站点的这个路径「爬」一下，模仿真实浏览；固定的 / 最好认</span></div>`;
    tlsRows += `<div><label>maxTimeDiff <span class="muted" style="font-weight:400">（毫秒，0=默认）</span></label>
      <input id="ib_f_mtd" class="input" type="number" min="0" value="${esc(s.max_time_diff || 0)}"></div>`;
  }
  $("ib_dyn_tls").style.display = tlsRows ? "" : "none";
  $("ib_dyn_tls_inner").innerHTML = tlsRows;

  // ---- Reality 密钥对（独立区块，不随 innerHTML 重建，避免正在输入的私钥被清掉）----
  const isReality = tls === "reality";
  $("ib_reality_row").style.display = isReality ? "" : "none";
  if (isReality) {
    if (!$("ib_f_privkey").dataset.dirty) $("ib_f_privkey").value = s.private_key || "";
    $("ib_f_pubkey").value = s.pbk || "";
  }
  $("ib_hint").textContent = IB_PROTO_HINT[proto] || "";

  if (userTriggered) ibPutSettings(s);
}

// ibFormToSettings 保存时把动态表单值并入 settings（保留用户透传的其它键）
function ibFormToSettings() {
  const core = $("ib_core").value, proto = ibProto(), tr = $("ib_transport").value, tls = $("ib_tls").value;
  const s = ibGetSettings();
  const g = id => { const el = $(id); return el ? el.value.trim() : ""; };
  const setIf = (k, v) => { if (v !== "" && v != null) s[k] = v; else delete s[k]; };
  if (["ws","h2","httpupgrade","xhttp"].includes(tr)) { setIf("path", g("ib_f_path")); setIf("ws_host", g("ib_f_wshost")); }
  if (tr === "grpc") setIf("service_name", g("ib_f_svc"));
  if (tr === "xhttp") {
    const m = g("ib_f_xhmode");
    if (m && m !== "auto") s.xhttp_mode = m; else delete s.xhttp_mode;
  }
  if (tr === "kcp") { setIf("kcp_seed", g("ib_f_kcpseed")); setIf("kcp_header", g("ib_f_kcpheader")); }
  if (tr === "quic") {
    const qs = g("ib_f_qsecurity");
    if (qs && qs !== "none") s.quic_security = qs; else delete s.quic_security;
    setIf("quic_key", g("ib_f_qkey"));
    setIf("quic_header", g("ib_f_qheader"));
  }
  if (tr === "tcp" && core === "xray") {
    if (g("ib_f_pp") === "1") s.accept_proxy_protocol = 1; else delete s.accept_proxy_protocol;
  }
  if (proto === "socks" || proto === "http") {
    const a = g("ib_f_auth");
    if (a && a !== "noauth") s.account_auth = a; else delete s.account_auth;
    if (proto === "socks" && g("ib_f_udp") === "1") s.udp = true; else delete s.udp;
  }
  if (proto === "dokodemo-door" || proto === "direct") {
    setIf("dd_address", g("ib_f_ddaddr"));
    const dp = +g("ib_f_ddport") || 0;
    if (dp > 0) s.dd_port = dp; else delete s.dd_port;
    const dn = g("ib_f_ddnet");
    if (dn && dn !== "tcp") s.dd_network = dn; else delete s.dd_network;
    if (g("ib_f_ddfollow") === "1") s.dd_follow_redirect = true; else delete s.dd_follow_redirect;
  }
  if (proto === "vless") setIf("flow", g("ib_f_flow"));
  if (proto === "shadowsocks") {
    setIf("method", g("ib_f_method"));
    const net = g("ib_f_ssnet");
    if (net && net !== "tcp,udp") s.ss_network = net; else delete s.ss_network;
  }
  if (tls === "tls") {
    setIf("server_name", g("ib_f_sni"));
    setIf("alpn", g("ib_f_alpn"));
    setIf("certificate_path", g("ib_f_cert"));
    setIf("key_path", g("ib_f_key"));
    setIf("certificate_pem", g("ib_f_certpem"));
    setIf("key_pem", g("ib_f_keypem"));
    // 填了 PEM 就以 PEM 为准：旧路径可能指向不存在的文件（迁移场景），
    // 留着会让后端以为「已经有文件」而不落盘。
    if (s.certificate_pem) delete s.certificate_path;
    if (s.key_pem) delete s.key_path;
  }
  if (tls === "reality") {
    setIf("handshake_server", g("ib_f_handshake"));
    const hp = +g("ib_f_hsport") || 443;
    if (hp !== 443) s.handshake_port = hp; else delete s.handshake_port;
    setIf("server_names", g("ib_f_snames"));
    // shortIds 存数组：xray 的 "" 是合法短 ID，拼成字符串会把它当空项丢掉
    const rawSids = g("ib_f_shortids");
    if (rawSids === "") { delete s.short_ids; }
    else { s.short_ids = rawSids.split(",").map(x => x.trim()); delete s.short_id; }
    setIf("spider_x", g("ib_f_spider"));
    const mtd = +g("ib_f_mtd") || 0;
    if (mtd > 0) s.max_time_diff = mtd; else delete s.max_time_diff;
    // 私钥/公钥以表单为准：手改私钥后必须重新推导公钥，否则两边对不上
    const pk = g("ib_f_privkey"), pub = g("ib_f_pubkey");
    if (pk) s.private_key = pk; else delete s.private_key;
    if (pub) s.pbk = pub; else delete s.pbk;
  }
  return s;
}

/* ---- 从 JSON 填入入站表单 ----
   解析结果逐项写进表单控件（不是直接落库）：每个字段改得动、看得见来源，
   确认无误再点保存。只解析入站本身，DNS / 路由这类全局段不在这里处理。 */
let __ibImpItems = [], __ibImpIdx = 0, __ibImpClients = [], __ibImpShowCred = false;

function ibImpPick(input) {
  const f = input.files && input.files[0];
  if (!f) return;
  if (f.size > 8*1024*1024) { toast("文件超过 8 MB，请拆分后再填", "err"); input.value = ""; return; }
  const r = new FileReader();
  r.onload = () => { $("ib_imp_text").value = String(r.result || ""); ibImpParse(); };
  r.onerror = () => toast("读取文件失败", "err");
  r.readAsText(f);
}

async function ibImpParse() {
  const text = $("ib_imp_text").value.trim();
  if (!text) { toast("先粘贴 JSON 或选一个文件", "warn"); return; }
  const core = $("ib_core").value;
  $("ib_imp_msg").textContent = "解析中…";
  $("ib_imp_result").innerHTML = "";
  try {
    const j = await req("/api/inbounds/parse-json", { method:"POST", body:{ text, core } });
    const items = j.items || [];
    if (!items.length) throw new Error("没有解析到入站（根节点既不是数组，也没有 inbounds 字段）");
    __ibImpItems = items; __ibImpIdx = 0; __ibImpShowCred = false;
    $("ib_imp_msg").textContent = `${items.length} 条入站 · 目标内核 ${j.core}` + (j.global_ignored ? " · " + j.global_ignored : "");
    const first = items.findIndex(x => !x.skip);
    ibImpFill(first < 0 ? 0 : first);   // 全都不支持时也让用户看到原因
  } catch (e) {
    __ibImpItems = []; __ibImpClients = [];
    $("ib_imp_msg").textContent = "";
    $("ib_imp_sel_wrap").classList.add("hide");
    $("ib_imp_result").innerHTML = `<div style="color:var(--err);font-size:12.5px">✕ ${escHtml(e.message)}</div>`;
  }
}

function ibImpFill(idx) {
  const it = (__ibImpItems || [])[idx];
  if (!it) return;
  __ibImpIdx = idx;
  __ibImpClients = (it.clients || []).slice();
  ibImpRenderSelect();
  if (it.skip) {
    $("ib_imp_result").innerHTML =
      `<div style="font-size:12.5px;color:var(--err)">✕ 这条填不进来：${escHtml((it.warnings||[])[0] || "当前内核不支持")}</div>` +
      ibImpWarnHtml(it.warnings);
    return;
  }
  const sp = ibSpec();
  if (!sp.protocols.includes(it.protocol)) {
    $("ib_imp_result").innerHTML = `<div style="font-size:12.5px;color:var(--err)">✕ 当前内核不支持 ${escHtml(it.protocol)}，请先切换内核再解析</div>`;
    return;
  }
  // ---- 逐项回填 ----
  $("ib_protocol").value = it.protocol;
  $("ib_transport").value = sp.transports.includes(it.transport) ? it.transport : "tcp";
  $("ib_tls").value = sp.tls.includes(it.tls) ? it.tls : "none";
  $("ib_name").value = it.name || "";
  $("ib_name").dataset.auto = "0";   // 填进来的名字不能被「自动命名」再改一遍
  $("ib_listen").value = it.listen || "";
  $("ib_port").value = it.port || "";
  ibPutSettings(it.settings || {});
  ibDynamic(false);                  // 用 settings 渲染动态字段，值一并带出
  // Reality 私钥单独处理：genReality() 会打 dirty 标记，不清掉的话渲染时
  // 不会用导入的私钥，最后保存下来是另一对密钥，客户端全都连不上。
  if (it.tls === "reality") {
    $("ib_f_privkey").dataset.dirty = "";
    $("ib_f_privkey").value = (it.settings && it.settings.private_key) || "";
    $("ib_f_pubkey").value = (it.settings && it.settings.pbk) || "";
  }
  $("ib_imp_result").innerHTML = ibImpSummaryHtml(it) + ibImpWarnHtml(it.warnings) + ibImpUsersHtml();
}

function ibImpRenderSelect() {
  const items = __ibImpItems || [];
  $("ib_imp_sel_wrap").classList.toggle("hide", items.length < 2);
  $("ib_imp_count").textContent = items.length;
  $("ib_imp_sel").innerHTML = items.map((it, i) =>
    `<option value="${i}">${i+1}. ${escHtml(it.name || "(未命名)")} · ${escHtml(it.protocol)} :${it.port}${it.skip ? "（不支持）" : ""}</option>`).join("");
  $("ib_imp_sel").value = String(__ibImpIdx);
}

function ibImpSummaryHtml(it) {
  const bits = [escHtml(it.protocol), ":" + it.port, escHtml(it.transport || "tcp"), it.tls && it.tls !== "none" ? escHtml(it.tls) : "无 TLS"];
  return `<div style="font-size:12.5px;color:var(--ok)">✓ 已填入表单：${bits.join(" · ")}
    <span class="muted">— 上面的字段都可直接改，改完点「保存」</span></div>`;
}

function ibImpWarnHtml(ws) {
  return (ws || []).map(w =>
    `<div style="font-size:12px;color:var(--warn,#c47f17);margin-top:3px">⚠ ${escHtml(w)}</div>`).join("");
}

// ibImpUsersHtml 展示「保存时会建哪些用户」。
// 凭据默认打码：这块内容常被截图发群里，明文 UUID/密码没必要整段露出来。
function ibImpUsersHtml() {
  const cs = __ibImpClients || [];
  const editing = !!$("ib_name").dataset.editing;
  if (!cs.length) {
    return `<div class="muted" style="font-size:12.5px;margin-top:10px">
      这条入站不需要用户（端口转发 / 免认证代理），保存时不会建用户。</div>`;
  }
  const auto = cs.filter(c => c.auto).length;
  const rows = cs.map(c => `<tr>
      <td>${escHtml(c.name)}</td>
      <td>${escHtml(c.kind || "")}</td>
      <td class="mono" style="font-size:11.5px;word-break:break-all">${escHtml(__ibImpShowCred ? c.credential : maskCred(c.credential))}</td>
      <td>${c.auto ? '<span class="badge b-acc">自动生成</span>' : '<span class="badge">来自 JSON</span>'}</td>
    </tr>`).join("");
  return `<div style="margin-top:10px;padding:10px 12px;border:1px solid var(--line);border-radius:10px">
    <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
      <b style="font-size:13px">用户 ${cs.length} 个</b>
      ${editing
        ? `<span class="muted" style="font-size:12.5px">编辑已有入站不会自动建用户（避免重复）；要加用户请保存后到「用户」页添加</span>`
        : `<label class="muted" style="font-size:12.5px;display:flex;align-items:center;gap:5px;margin:0">
             <input type="checkbox" id="ib_imp_users" checked> 保存入站时一并创建
           </label>`}
      <span style="flex:1"></span>
      <button class="btn sm" data-act="ibImpToggleCred">${__ibImpShowCred ? "隐藏凭据" : "显示凭据"}</button>
    </div>
    <div class="tbl-wrap" style="margin-top:8px"><table>
      <thead><tr><th>用户名</th><th>凭据类型</th><th>凭据</th><th>来源</th></tr></thead>
      <tbody>${rows}</tbody></table></div>
    ${auto ? `<div class="muted" style="font-size:12px;margin-top:6px">
      其中 ${auto} 个是 JSON 里没有、由面板生成的（UUID / 密码已生成好，保存后可在「用户」页看到并用于客户端配置）。</div>` : ""}
  </div>`;
}

function ibImpToggleCred() {
  __ibImpShowCred = !__ibImpShowCred;
  const it = (__ibImpItems || [])[__ibImpIdx];
  if (!it) return;
  const keep = ($("ib_imp_users") || {}).checked;
  $("ib_imp_result").innerHTML = (it.skip ? "" : ibImpSummaryHtml(it)) + ibImpWarnHtml(it.warnings) + ibImpUsersHtml();
  const box = $("ib_imp_users");
  if (box && keep !== undefined) box.checked = keep;
}

/* 凭据打码：与后端预览用同一套规则 */
function maskCred(s) {
  s = String(s == null ? "" : s);
  if (!s) return "";
  const i = s.indexOf(":");
  if (i > 0 && i < s.length - 1) return s.slice(0, Math.min(i, 4)) + "…:***";
  return s.length <= 8 ? "*".repeat(s.length) : s.slice(0, 6) + "…（" + s.length + " 位）";
}

async function openInboundModal(id) {
  $("ibtitle").textContent = id ? "编辑入站" : "新增入站";
  // 每次打开都清空上一次的 JSON 解析结果：新增入站时带着上一条的
  // 「待创建用户」是灾难——点保存会给别家入站多建几个用户。
  __ibImpItems = []; __ibImpIdx = 0; __ibImpClients = []; __ibImpShowCred = false;
  $("ib_imp_text").value = "";
  $("ib_imp_msg").textContent = "";
  $("ib_imp_result").innerHTML = "";
  $("ib_imp_sel_wrap").classList.add("hide");
  $("ib_name").dataset.editing = id || "";
  $("ib_name").dataset.auto = id ? "0" : "1";
  // 每次打开都清掉"用户改过私钥"的标记，否则新入站会沿用上一条的脏值
  $("ib_f_privkey").dataset.dirty = "";
  $("ib_f_privkey").value = "";
  $("ib_f_pubkey").value = "";
  try { window.__ibAll = await req("/api/inbounds"); } catch(e){ window.__ibAll = []; }
  let v = { name:"", core:"singbox", protocol:"vless", listen:"", port:"", transport:"tcp", tls:"none", settings:"{}" };
  if (id) { const i = (window.__ibAll).find(x=>x.id===id); if (i) v = i; }
  // 已有 mihomo 入站必须把 mihomo 放回下拉：否则 value 赋不上，
  // 浏览器留空 → ibSpec() 回退 sing-box，用户点保存就把内核静默换了。
  fillCoreSelect($("ib_core"), (await compatCoreEnabled()) || v.core === "mihomo");
  $("ib_core").value = v.core || "singbox";
  ibFillSelects();
  $("ib_protocol").value = v.protocol || ibSpec().protocols[0];
  $("ib_transport").value = v.transport || "tcp";
  $("ib_tls").value = v.tls || "none";
  $("ib_name").value = v.name || "";
  $("ib_listen").value = v.listen || "";
  $("ib_port").value = v.port || "";
  $("ib_settings").value = v.settings || "{}";
  // 运行中的入站改内核必须走安全切换流程，编辑弹窗里直接锁住内核选择
  const lockCore = id && v.enabled && !v.draft;
  $("ib_core").disabled = lockCore;
  $("ib_core").title = lockCore ? "运行中的入站请用「切换内核」按钮安全迁移（只影响这一条，其它入站留在原内核）" : "";
  ibDynamic(false);
  if (!id) {
    // 新增：应用该协议在当前内核下的推荐 TLS，再自动名称/端口与动态字段
    $("ib_tls").value = ibRecommendedTLS($("ib_core").value, ibProto());
    ibSuggestNamePort();
    ibPutSettings({}); // 新增：settings 从零开始，动态字段负责填充
    // 新建 Reality：短 ID 与 spiderX 一并给好（参考 3x-ui）。否则默认是空
    // shortIds，等于放行不带短 ID 的客户端，且用户还得自己去凑一串合法的十六进制。
    if ($("ib_tls").value === "reality") ibPutSettings(realityDefaultSettings());
    ibDynamic(false);
    // 默认组合自动生成 Reality 密钥（失败不阻塞，可后补）
    if ($("ib_tls").value === "reality") genReality().catch(()=>{});
  }
  openModal("mInbound");
  setTimeout(() => $("ib_name").focus(), 120);
}
async function saveInbound() {
  const editing = $("ib_name").dataset.editing;
  // Reality 必须有服务端私钥。缺了就自动生成——但只在这里兜底，
  // 手填的私钥一律以表单为准（导入的配置可能自带私钥）。
  if ($("ib_tls").value === "reality") {
    if (!$("ib_f_privkey").value.trim()) {
      try {
        await genReality(true);
        toast("已自动生成 Reality 密钥对：客户端需改用新的公钥（重新导出/扫码）", "info");
      } catch (e) {
        toast("Reality 密钥对生成失败：" + (e.message || e), "err");
        return;
      }
    } else if (!$("ib_f_pubkey").value.trim()) {
      // 有私钥没公钥：能推就推，推不出来让用户自己点
      try { await ibDerivePub(true); } catch (e) { /* 交给后端 apply 时报错 */ }
    }
  }
  let settings;
  try { settings = JSON.stringify(ibFormToSettings()); }
  catch(e){ toast("Settings JSON 无效: " + e.message, "err"); return; }
  const body = { name:$("ib_name").value, core:$("ib_core").value, protocol:ibProto(),
    listen:$("ib_listen").value, port:+$("ib_port").value, transport:$("ib_transport").value,
    tls:$("ib_tls").value, settings };
  if (!body.name || !body.port) { toast("名称与端口不能为空", "err"); return; }
  // 从 JSON 填表时勾了「一并创建用户」：和入站一起提交。
  // 分两个请求会出现「入站建好了、用户没建上」——面板上看着和成功一模一样。
  const userBox = $("ib_imp_users");
  const wantUsers = !editing && userBox && userBox.checked && __ibImpClients.length;
  if (wantUsers) {
    body.clients = __ibImpClients.map(c => ({
      name: c.name, credential: c.credential,
      quota_gb: c.quota_gb || 0, expires_at: c.expires_at || null
    }));
  }
  try {
    if (editing) { await req("/api/inbounds/"+editing, { method:"PUT", body:Object.assign({},body,{id:+editing}) }); toast("入站已更新，如已启用请记得「应用配置」"); }
    else {
      await req("/api/inbounds", { method:"POST", body });
      toast(wantUsers
        ? `入站已保存（未启用），同时提交 ${body.clients.length} 个用户`
        : "入站已保存（未启用），确认无误后打开开关并应用");
    }
    closeModal("mInbound");
    if (wantUsers) loadClients();
    loadInbounds();
  } catch (e) { toast(e.message, "err"); }
}
async function toggleInbound(id, en) {
  try { const all = await req("/api/inbounds"); const inb = all.find(x=>x.id===id);
    inb.enabled = en; await req("/api/inbounds/"+id, { method:"PUT", body:inb });
    toast(en ? "已启用，需「应用配置」后生效" : "已停用，需「应用配置」后从内核移除", "info");
  } catch(e){ toast(e.message,"err"); }
}
async function promoteInbound(id) {
  try { const all = await req("/api/inbounds"); const inb = all.find(x=>x.id===id);
    inb.draft = false; await req("/api/inbounds/"+id, { method:"PUT", body:inb }); toast("已取消草稿"); loadInbounds();
  } catch(e){ toast(e.message,"err"); }
}

/* ---- 切换内核 ---- */
let swInbound = null;
async function openSwitchModal(id) {
  const all = await req("/api/inbounds");
  const i = all.find(x=>x.id===id); if (!i) return;
  swInbound = i;
  $("sw_inbound").value = `#${i.id} ${i.name}（当前 ${i.core}，端口 ${i.port}）`;
  // 只列出同时支持该协议的内核，避免选完才被后端预检拒绝。
  // mihomo 默认不作为切换目标：切过去是降级（缺 vless / reality）。
  const allowMihomo = await compatCoreEnabled();
  const opts = coreChoices(allowMihomo)
    .filter(c => c !== i.core && (IB_SPECS[c]||{protocols:[]}).protocols.includes(i.protocol));
  if (!opts.length) {
    toast(`协议 ${i.protocol} 只有 ${i.core} 支持，无法切换内核`, "err");
    return;
  }
  $("sw_to").innerHTML = opts.map(c => `<option>${c}</option>`).join("");
  $("sw_preview").classList.add("hide"); $("sw_preview").textContent = "";
  $("sw_go").disabled = false;
  openModal("mSwitch");
}
async function previewSwitch() {
  if (!swInbound) return;
  try {
    const j = await req("/api/switch-core", { method:"POST", body:{ to:$("sw_to").value, inbound_ids:[swInbound.id], dry_run:true } });
    const p = $("sw_preview"); p.classList.remove("hide");
    p.textContent = `✅ 预检通过（${j.to} 校验${j.validated ? "成功" : "已跳过：未配置二进制"}，配置哈希 ${j.hash}）\n\n` + j.content;
  } catch(e){ $("sw_preview").classList.remove("hide"); $("sw_preview").textContent = "❌ " + e.message; }
}
async function doSwitch() {
  if (!swInbound) return;
  const to = $("sw_to").value;
  if (!(await ask("切换内核", `确认把入站「${swInbound.name}」从 ${swInbound.core} 迁移到 ${to}？只影响这一条，${swInbound.core} 上其它入站继续运行。预检或旧内核收缩失败都不会切换；新内核启动失败会自动回滚。`))) return;
  $("sw_go").disabled = true;
  try {
    const j = await req("/api/switch-core", { method:"POST", body:{ to, inbound_ids:[swInbound.id] } });
    closeModal("mSwitch");
    toast(`已切换到 ${to}（配置哈希 ${j.hash}），当前服务正常`, "ok");
    (j.warnings||[]).forEach(w => toast(w, "warn"));
    loadInbounds(); loadStatus();
  } catch(e){ toast(e.message, "err"); $("sw_go").disabled = false; }
}
async function delInbound(id) {
  if (!(await ask("删除入站", "删除入站不会立即影响运行中的配置（下次应用后生效）。确定删除？"))) return;
  try { await req("/api/inbounds/"+id, { method:"DELETE" }); toast("已删除"); loadInbounds(); } catch(e){ toast(e.message,"err"); }
}
// genSpider 换一个随机 spiderX。同时写回 settings：动态字段是按 settings
// 重建的，只改输入框的话，切一下传输方式就被冲掉了。
function genSpider() {
  const el = $("ib_f_spider");
  if (!el) return;
  const v = realityRandSpider();
  el.value = v;
  const s = ibGetSettings();
  s.spider_x = v;
  ibPutSettings(s);
  toast("已生成随机 spiderX：" + v, "info");
}

async function genReality(quiet) {
  try {
    // 不传 core：密钥是标准 X25519，后端用 crypto/ecdh 直接算，与内核无关
    const j = await req("/api/tools/reality-keypair", { method:"POST", body:{} });
    $("ib_f_privkey").value = j.private_key;
    $("ib_f_privkey").dataset.dirty = "1";
    $("ib_f_pubkey").value = j.public_key;
    if (!quiet) toast("已生成新的 Reality 密钥对：客户端需改用新公钥", "info");
    return j;
  } catch (e) {
    if (!quiet) toast("生成密钥对失败：" + e.message, "err");
    throw e;
  }
}

// ibDerivePub 由私钥推导公钥。手改私钥后必须重推，否则客户端拿到的 pbk
// 与服务端私钥不匹配，表现为"握手一直失败而服务端日志一切正常"。
async function ibDerivePub(quiet) {
  const pk = $("ib_f_privkey").value.trim();
  if (!pk) { if (!quiet) toast("先填私钥，或点「重新生成」", "warn"); return ""; }
  try {
    const j = await req("/api/tools/reality-keypair", { method:"POST", body:{ private_key: pk } });
    $("ib_f_pubkey").value = j.public_key;
    if (!quiet) toast("已由私钥推导出公钥");
    return j.public_key;
  } catch (e) {
    if (!quiet) toast(e.message, "err");
    throw e;
  }
}

async function ibCopyPub() {
  const v = $("ib_f_pubkey").value.trim();
  if (!v) { toast("还没有公钥", "warn"); return; }
  try { await navigator.clipboard.writeText(v); toast("公钥已复制"); }
  catch (e) { toast("复制失败，请手动选中：" + v, "err"); }
}

/* ---- Reality 握手目标择优 ----
   测的是「这台服务器 → 目标」的那段网络，这也正是客户端握手时要走的路：
   REALITY 服务端收到 ClientHello 后会把它转给 dest，dest 慢的话每个客户端
   每次握手都要多等这么久。所以这个数只能在这里测，在家里测没有意义。 */
let __destResults = [];
// 上一次「取 SNI」验过的名字（小写）。只用来判断「serverNames 首位可不可信」，
// 不做任何回填——验过的名单在弹窗里看得见，回填由用户点按钮决定。
let __sniVerified = null;

function openDestPick() {
  const cur = ($("ib_f_handshake") || {}).value || "";
  const port = +($("ib_f_hsport") || {}).value || 443;
  const lines = [];
  const push = v => { if (v && !lines.includes(v)) lines.push(v); };
  if (cur.trim()) push(cur.trim().includes(":") ? cur.trim() : cur.trim() + ":" + port);
  IB_HANDSHAKE_SITES.forEach(h => push(h + ":443"));
  $("dest_list").value = lines.join("\n");
  $("dest_msg").textContent = "";
  $("dest_result").innerHTML = `<div class="muted" style="font-size:12.5px">
    还没测。点「开始测速」会从这台服务器逐个真握手 3 次，按「可用 → 证书对得上 → 快」排序。</div>`;
  $("dest_sni").innerHTML = "";
  __destResults = [];
  __sniVerified = null;
  openModal("mDest");
}

// parseDestLines 每行一个 host 或 host:port（也认空格分隔）。
function parseDestLines(text) {
  const out = [];
  String(text || "").split(/[\s,]+/).forEach(s => {
    s = s.trim();
    if (!s || out.length >= 16) return;
    let host = s, port = 443;
    const i = s.lastIndexOf(":");
    if (i > 0 && /^\d+$/.test(s.slice(i + 1))) {
      host = s.slice(0, i);
      port = +s.slice(i + 1) || 443;
    }
    out.push({ host, port });
  });
  return out;
}

async function runDestProbe() {
  const cands = parseDestLines($("dest_list").value);
  if (!cands.length) { toast("先填至少一个目标", "warn"); return; }
  $("dest_msg").textContent = `正在实测 ${cands.length} 个目标（每个握手 3 次取中位数）…`;
  $("dest_result").innerHTML = "";
  $("dest_sni").innerHTML = "";
  __sniVerified = null;
  try {
    const j = await req("/api/tools/reality-dest", { method: "POST", body: { candidates: cands } });
    __destResults = j.results || [];
    renderDestResults();
    const ok = __destResults.filter(r => r.ok).length;
    $("dest_msg").textContent = `实测完成：${ok} / ${__destResults.length} 个可用` +
      (j.best ? `，推荐 ${j.best}` : "，没有一个可用——换个目标或确认这台机器能正常出网");
  } catch (e) {
    $("dest_msg").textContent = "";
    $("dest_result").innerHTML = `<div style="color:var(--err);font-size:12.5px">测速失败：${escHtml(e.message)}</div>`;
  }
}

function renderDestResults() {
  // 列数刻意少：说明太长时整张表会横着撑出弹窗，把「使用」按钮推到看不见的地方。
  // TLS / ALPN / 证书这些次要信息压到目标下面一行，主列只留结论与说明。
  const rows = (__destResults || []).map((r, i) => {
    const badge = !r.ok ? `<span class="badge b-err">不可用</span>`
      : !r.cert_ok ? `<span class="badge b-warn">证书不符</span>`
      : `<span class="badge b-ok">可用</span>`;
    const best = r.best ? ` <span class="badge b-acc">推荐</span>` : "";
    const info = [
      r.tls ? "TLS " + r.tls : "",
      r.alpn ? "ALPN " + r.alpn : "无 ALPN",
      r.issuer || "",
      r.days_left > 0 ? `证书 ${r.days_left} 天` : ""
    ].filter(Boolean).join(" · ");
    return `<tr>
      <td style="white-space:normal"><b style="font-size:12.5px">${escHtml(r.host)}</b><span class="muted">:${r.port}</span>
        <div class="muted" style="font-size:11.5px">${escHtml(info)}</div></td>
      <td class="ta-r num" style="white-space:nowrap">${r.ok ? r.ms + " ms" : "—"}</td>
      <td style="white-space:nowrap">${badge}${best}</td>
      <td style="font-size:12px;white-space:normal;word-break:break-word">${escHtml(r.detail || "")}${r.warning ? `<div class="muted" style="margin-top:2px">⚠ ${escHtml(r.warning)}</div>` : ""}</td>
      <td class="ta-r" style="white-space:nowrap">
        <button class="btn sm" data-act="useDest" data-args="${escJson([i])}"${r.ok ? "" : " disabled"}>使用</button>
        <button class="btn sm" data-act="collectSNI" data-args="${escJson([i])}"${r.ok ? "" : " disabled"} title="读这个目标的证书，验出能填进 serverNames 的域名">取 SNI</button>
      </td>
    </tr>`;
  }).join("");
  // table-layout:fixed + 固定列宽：失败行的 warning 是一长句，自动布局会把表格
  // 撑到弹窗外面，把「使用」按钮推到水平滚动区——用户根本看不见该点哪儿。
  $("dest_result").innerHTML = `<div class="tbl-wrap" style="margin-top:8px"><table style="table-layout:fixed">
    <colgroup><col style="width:26%"><col style="width:72px"><col style="width:104px"><col><col style="width:132px"></colgroup>
    <thead><tr><th>目标</th><th class="ta-r">握手</th><th>结论</th><th>说明</th><th></th></tr></thead>
    <tbody>${rows}</tbody></table></div>`;
}

/* ---- 同站 SNI 收集 ----
   目标择优回答「填哪个 dest」，这里回答「serverNames 填什么」。
   REALITY 只接受名单里的 SNI，而该填哪些取决于 dest 那张证书覆盖了谁；
   接口会把证书里的域名逐个真握手验一遍，只有验过的才返回。 */
async function collectSNI(i) {
  const r = (__destResults || [])[i];
  if (!r) return;
  $("dest_msg").textContent = `正在读 ${r.host} 的证书，并逐个验证…`;
  $("dest_sni").innerHTML = `<div class="muted" style="font-size:12.5px;margin-top:8px">读取中…</div>`;
  try {
    const j = await req("/api/tools/reality-sni", { method:"POST", body:{ host: r.host, port: r.port } });
    renderSNIPanel(r, j);
    $("dest_msg").textContent = `「${r.host}」验出 ${(j.snis || []).length} 个可用 SNI（共试 ${j.tried} 个）`;
    // 结果在目标表格下方，弹窗高度有限时会被挡在折叠线外。
    // 不用平滑滚动：动画途中按钮还在动，点下去容易点空。
    const panel = $("dest_sni");
    if (panel && panel.scrollIntoView) panel.scrollIntoView({ block: "nearest" });
  } catch (e) {
    $("dest_msg").textContent = "";
    $("dest_sni").innerHTML = `<div style="color:var(--err);font-size:12.5px;margin-top:8px">取 SNI 失败：${escHtml(e.message)}</div>`;
  }
}

function renderSNIPanel(r, j) {
  const cert = j.cert || {};
  __sniVerified = new Set((j.snis || []).map(s => String(s.sni || "").toLowerCase()));
  const meta = [cert.issuer || "",
    cert.not_after ? "到期 " + cert.not_after : "",
    cert.days_left > 0 ? "剩 " + cert.days_left + " 天" : ""].filter(Boolean).join(" · ");
  const wc = (cert.wildcards || []).length
    ? `<div class="muted" style="font-size:11.5px;margin-top:2px">
        证书还覆盖 ${(cert.wildcards || []).map(escHtml).join("、")}——单级子域都能用，但下面只列真验过的</div>`
    : "";
  const snis = j.snis || [];
  const rows = snis.map(s => `<tr>
      <td class="mono" style="font-size:12px">${escHtml(s.sni)}</td>
      <td class="muted" style="font-size:11.5px">${escHtml(s.source || "")}</td>
      <td class="ta-r num" style="font-size:11.5px">${s.ms} ms</td>
      <td class="ta-r"><button class="btn sm" data-act="mergeSNI" data-args="${escJson([[s.sni]])}">加入</button></td>
    </tr>`).join("");
  const rej = (j.rejected || []).map(x =>
    `<div class="muted" style="font-size:11.5px">✗ <span class="mono">${escHtml(x.sni)}</span> — ${escHtml(x.detail || "")}</div>`).join("");
  $("dest_sni").innerHTML = `<div style="margin-top:12px;padding:10px 12px;border-radius:8px;
      background:rgba(127,127,127,.07);border:1px solid rgba(127,127,127,.22)">
    <div style="display:flex;align-items:baseline;gap:8px;flex-wrap:wrap;margin-bottom:6px">
      <b style="font-size:12.5px">${escHtml(r.host)} 的合法 SNI</b>
      <span class="muted" style="font-size:11.5px">${escHtml(meta)}</span>
      <span style="flex:1"></span>
      ${snis.length ? `<button class="btn sm primary" data-act="mergeSNI" data-args="${escJson([snis.map(s => s.sni)])}">全部并入 serverNames</button>` : ""}
    </div>
    ${wc}
    ${snis.length ? `<div class="tbl-wrap" style="margin-top:6px"><table style="table-layout:fixed">
        <colgroup><col><col style="width:92px"><col style="width:66px"><col style="width:56px"></colgroup>
        <thead><tr><th>域名</th><th>来源</th><th class="ta-r">握手</th><th></th></tr></thead>
        <tbody>${rows}</tbody></table></div>`
      : `<div class="muted" style="font-size:12px">一个都没验过。${escHtml(j.note || "")}</div>`}
    ${rej ? `<div style="margin-top:8px"><div class="muted" style="font-size:11.5px">没验过的（不能填）：</div>${rej}</div>` : ""}
    ${j.note && snis.length ? `<div class="muted" style="font-size:11.5px;margin-top:8px">${escHtml(j.note)}</div>` : ""}
  </div>`;
}

// mergeSNI 把验过的域名并进 serverNames。只加不换：用户自己填的其它名字
// 可能来自别处（多目标轮换），删掉等于替他做决定。
function mergeSNI(names) {
  const sn = $("ib_f_snames");
  if (!sn) return;
  const cur = String(sn.value || "").split(/[\s,]+/).map(x => x.trim()).filter(Boolean);
  const seen = new Set(cur.map(x => x.toLowerCase()));
  let added = 0;
  (names || []).forEach(n => {
    n = String(n || "").trim().toLowerCase();
    if (!n || seen.has(n)) return;
    seen.add(n);
    cur.push(n);
    added++;
  });
  sn.value = cur.join(",");
  toast(added ? `已并入 ${added} 个 SNI（现在共 ${cur.length} 个）` : "这些域名已经在 serverNames 里了",
    added ? "ok" : "warn");
}

function useDest(i) {
  const r = (__destResults || [])[i];
  if (!r) return;
  const oldHost = ($("ib_f_handshake").value || "").trim().replace(/:\d+$/, "");
  $("ib_f_handshake").value = r.host;
  $("ib_f_hsport").value = r.port;
  // SNI 要跟着 dest 走：REALITY 只接受 serverNames 里的 SNI，而且导出时客户端
  // 拿**第一项**当 SNI——换了目标却把旧目标的域名留在首位，客户端就会拿一个
  // 新 dest 证书覆盖不到的名字去握手，直接连不上。
  //
  // 所以这里只做两件事：摘掉「旧 dest 主机名」那一项（它只是旧目标的镜像），
  // 再确保新目标的 SNI 在名单里。用户自己填的其它名字一律不动——那可能是
  // 他按轮换配的。
  const sn = $("ib_f_snames");
  const name = r.sni || r.host;
  const cur = String(sn.value || "").split(/[\s,]+/).map(x => x.trim()).filter(Boolean);
  // 目标没换（重复点同一个「使用」）时不要摘：那一项就是当前目标的域名，仍然有效，
  // 摘掉再追加只会把首位换掉。
  const changing = oldHost && oldHost.toLowerCase() !== String(r.host).toLowerCase();
  const kept = changing ? cur.filter(x => x.toLowerCase() !== oldHost.toLowerCase()) : cur.slice();
  const has = kept.some(x => x.toLowerCase() === name.toLowerCase());
  if (!has) kept.push(name);
  sn.value = kept.join(",");
  if (kept.length === 1) {
    toast(`已选用 ${r.host}:${r.port}，SNI 同步为 ${name}`);
  } else {
    // 客户端导出时取首位当 SNI。首位是不是可信的：等于目标自己的名字，
    // 或者刚被「取 SNI」验过。都不是就提醒一句，别让人带着一个错的首位走。
    const first = String(kept[0] || "").toLowerCase();
    const trusted = first === name.toLowerCase() || (__sniVerified && __sniVerified.has(first));
    toast(`已选用 ${r.host}:${r.port}；serverNames 现在 ${kept.length} 个，客户端会用首位 ${kept[0]}` +
      (trusted ? "" : "——它没被验证过是否在该目标的证书里"), trusted ? "ok" : "warn");
  }
  closeModal("mDest");
}

/* ---- 流量 ---- */
async function loadTraffic() {
  try {
    const j = await req("/api/traffic"); // 后端已按用量降序排行
    let totalAll = 0;
    $("trtbody").innerHTML = j.map((r, i) => {
      totalAll += r.total;
      const pct = r.pct || 0;
      const bar = r.quota_gb
        ? `<span class="progress"><i style="width:${Math.min(100, pct).toFixed(1)}%;background:${pct>=100?"var(--err)":r.warn?"var(--warn)":"var(--ok)"}"></i></span> <span class="muted">${pct.toFixed(0)}%</span>`
        : '<span class="muted">—</span>';
      const st = !r.enabled ? '<span class="badge b-err">已停用</span>'
        : r.exceeded ? '<span class="badge b-err">已达配额</span>'
        : r.expired ? '<span class="badge b-err">已到期</span>'
        : r.expiring ? '<span class="badge b-warn">3天内到期</span>'
        : r.warn ? '<span class="badge b-warn">接近配额</span>'
        : '<span class="badge b-ok">正常</span>';
      return `<tr>
      <td class="muted mono">${i + 1}</td>
      <td><b>${r.name}</b></td>
      <td class="ta-r num">${fmtBytes(r.up)}</td><td class="ta-r num">${fmtBytes(r.down)}</td>
      <td class="ta-r num"><b>${fmtBytes(r.total)}</b></td><td>${bar}</td>
      <td class="ta-r">${r.quota_gb ? r.quota_gb + " GB" : "不限"}</td>
      <td>${st}</td></tr>`;
    }).join("") || `<tr><td colspan="8" class="empty">暂无数据（开启统计并应用后生效）</td></tr>`;
    const st = $("st_traffic"); if (st) st.textContent = fmtBytes(totalAll);
  } catch(e){}
  loadDaily();
}
async function loadDaily() {
  try {
    const points = await req("/api/traffic/daily?days=" + $("tr_days").value);
    drawFlowChart($("tr_chart"), points, $("tr_tip"));
    const total = (points || []).reduce((s, p) => s + p.up + p.down, 0);
    $("tr_total").textContent = "区间合计: " + fmtBytes(total);
  } catch(e){}
}
function cssVar(n){ return getComputedStyle(document.documentElement).getPropertyValue(n).trim(); }
function drawDailyChart(points) {
  const cv = $("tr_chart"), dpr = window.devicePixelRatio || 1;
  const W = cv.clientWidth || 900, H = 230;
  cv.width = W * dpr; cv.height = H * dpr;
  const ctx = cv.getContext("2d");
  ctx.scale(dpr, dpr);
  ctx.clearRect(0, 0, W, H);
  if (!points || !points.length) return;
  const pad = { l:54, r:8, t:12, b:26 };
  const maxV = Math.max(1, ...points.map(p => p.up + p.down));
  const niceMax = niceCeil(maxV);
  const cw = W - pad.l - pad.r, ch = H - pad.t - pad.b;
  const n = points.length;
  const step = cw / n;
  const bw = Math.max(2, Math.min(30, step * 0.6));
  const cUp = cssVar("--chart-up") || "#3b82f6", cDown = cssVar("--chart-down") || "#22c55e";
  const cLine = cssVar("--line") || "#e5e9ef", cText = cssVar("--muted") || "#6b7280";
  const cFont = "11px " + (cssVar("--font") || "sans-serif");
  ctx.font = cFont;
  for (let i = 0; i <= 4; i++) {
    const y = pad.t + ch - ch * i / 4;
    ctx.strokeStyle = cLine; ctx.lineWidth = 1;
    ctx.beginPath(); ctx.moveTo(pad.l, y); ctx.lineTo(W - pad.r, y); ctx.stroke();
    ctx.fillStyle = cText; ctx.textAlign = "right";
    ctx.fillText(fmtBytes(niceMax * i / 4), pad.l - 8, y + 4);
  }
  points.forEach((p, i) => {
    const x = pad.l + step * i + (step - bw) / 2;
    const hDown = ch * p.down / niceMax, hUp = ch * p.up / niceMax;
    const yBase = pad.t + ch;
    if (p.down > 0) { ctx.fillStyle = cDown; ctx.beginPath();
      ctx.roundRect(x, yBase - hDown, bw, hDown, [2,2,0,0]); ctx.fill(); }
    if (p.up > 0) { ctx.fillStyle = cUp; ctx.beginPath();
      ctx.roundRect(x, yBase - hDown - hUp, bw, hUp, [2,2,0,0]); ctx.fill(); }
    if (n <= 8 || i === 0 || i === n - 1 || i % Math.ceil(n / 8) === 0) {
      ctx.fillStyle = cText; ctx.textAlign = "center";
      ctx.fillText(p.day.slice(5), x + bw / 2, H - 8);
    }
  });
  const total = points.reduce((s, p) => s + p.up + p.down, 0);
  $("tr_total").textContent = "区间合计: " + fmtBytes(total);
}
function niceCeil(v) {
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1,2,5,10]) if (v <= m * p) return m * p;
  return 10 * p;
}
window.addEventListener("resize", () => { if (typeof redrawCharts === "function") redrawCharts(); });

/* ================= 图表引擎（纯 Canvas，无外部依赖） ================= */
function chartSetup(cv, h) {
  const dpr = window.devicePixelRatio || 1;
  const H = h || 200;
  // 宽度一律交给 CSS（.chart-wrap canvas{width:100%}），这里只按**量到的实际宽度**
  // 设位图尺寸，绝不写死 style.width。
  // 旧实现是 `clientWidth || 父宽 || 600` 再 `style.width = W + "px"`：容器还没布局
  // （display:none 的 view、刚插入的节点）时量到 0，就回落到 600 并把画布钉成 600px
  // 固定宽——卡片内宽只有 314px，右边直接被裁掉，正是手机上「图表宽度太大、
  // 显示不全」的成因。现在量不到就这轮不画，等切页/缩放的 redraw 再来。
  cv.style.width = "";
  const W = Math.round(cv.getBoundingClientRect().width);
  if (!W) return null;
  cv.width = Math.round(W * dpr);
  cv.height = Math.round(H * dpr);
  cv.style.height = H + "px";
  const ctx = cv.getContext("2d");
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, W, H);
  return { ctx, W, H };
}
function hexA(c, a) {
  c = (c || "").trim();
  if (!c) return "rgba(120,140,180," + a + ")";
  if (c.startsWith("rgb")) return c.replace(/rgba?\(([^)]+)\)/, (m, g) => "rgba(" + g.split(",").slice(0, 3).join(",") + "," + a + ")");
  if (c.length === 4) c = "#" + c[1] + c[1] + c[2] + c[2] + c[3] + c[3];
  const n = parseInt(c.slice(1), 16);
  return "rgba(" + ((n >> 16) & 255) + "," + ((n >> 8) & 255) + "," + (n & 255) + "," + a + ")";
}
function smoothPath(ctx, pts) {
  if (!pts.length) return;
  ctx.moveTo(pts[0][0], pts[0][1]);
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[i - 1] || pts[i], p1 = pts[i], p2 = pts[i + 1], p3 = pts[i + 2] || p2;
    ctx.bezierCurveTo(p1[0] + (p2[0] - p0[0]) / 6, p1[1] + (p2[1] - p0[1]) / 6,
                     p2[0] - (p3[0] - p1[0]) / 6, p2[1] - (p3[1] - p1[1]) / 6, p2[0], p2[1]);
  }
}
function fmtRate(b) {
  if (!b) return "0";
  const u = ["B", "KB", "MB", "GB"]; let i = 0;
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return b.toFixed(i ? 1 : 0) + " " + u[i];
}

/* —— 流量趋势：双序列平滑面积图 + 悬停十字线 —— */
function drawFlowChart(cv, points, tipEl) {
  const s = chartSetup(cv, cv.id === "ov_chart" ? 268 : 236); if (!s) return;
  const { ctx, W, H } = s;
  if (!points || !points.length) return;
  const maxV = Math.max(1, ...points.map(p => p.up + p.down));
  const nice = niceCeil(maxV);
  const n = points.length;
  const cUp = cssVar("--chart-up"), cDn = cssVar("--chart-down");
  const cGrid = cssVar("--grid"), cText = cssVar("--muted");
  ctx.font = "11px " + (cssVar("--font") || "sans-serif");
  ctx.textBaseline = "middle";

  // Y 轴刻度先算好并测量最宽标签，动态决定左留白——
  // 否则 "46.57 GB" 这类长标签会被画布左缘裁掉首位数字，看起来像刻度错乱
  const tickLabels = [];
  for (let i = 0; i <= 4; i++) tickLabels.push(fmtBytes(nice * i / 4));
  let yw = 0; tickLabels.forEach(t => yw = Math.max(yw, ctx.measureText(t).width));
  const pad = { l: Math.min(W - 120, Math.ceil(yw) + 14), r: 10, t: 14, b: 24 };
  const cw = W - pad.l - pad.r, ch = H - pad.t - pad.b;
  const step = cw / Math.max(1, n - 1);

  // 网格 + Y 轴刻度
  for (let i = 0; i <= 4; i++) {
    const y = pad.t + ch - ch * i / 4;
    ctx.strokeStyle = cGrid; ctx.lineWidth = 1;
    ctx.beginPath(); ctx.moveTo(pad.l, y + .5); ctx.lineTo(W - pad.r, y + .5); ctx.stroke();
    ctx.fillStyle = cText; ctx.textAlign = "right";
    ctx.fillText(tickLabels[i], pad.l - 8, y);
  }
  const X = i => pad.l + step * i;
  const Y = v => pad.t + ch - ch * (v / nice);

  const series = [
    { key: "down", color: cDn, vals: points.map(p => p.down) },
    { key: "up", color: cUp, vals: points.map(p => p.up) }
  ];
  series.forEach(s => {
    const pts = s.vals.map((v, i) => [X(i), Y(v)]);
    // 面积
    ctx.beginPath(); smoothPath(ctx, pts);
    ctx.lineTo(pts[pts.length - 1][0], pad.t + ch);
    ctx.lineTo(pts[0][0], pad.t + ch);
    ctx.closePath();
    const g = ctx.createLinearGradient(0, pad.t, 0, pad.t + ch);
    g.addColorStop(0, hexA(s.color, .34));
    g.addColorStop(1, hexA(s.color, 0));
    ctx.fillStyle = g; ctx.fill();
    // 描线
    ctx.beginPath(); smoothPath(ctx, pts);
    ctx.strokeStyle = s.color; ctx.lineWidth = 2; ctx.lineJoin = "round"; ctx.lineCap = "round";
    ctx.shadowColor = hexA(s.color, .35); ctx.shadowBlur = 8; ctx.shadowOffsetY = 2;
    ctx.stroke();
    ctx.shadowBlur = 0; ctx.shadowOffsetY = 0;
  });

  // X 轴日期
  ctx.fillStyle = cText; ctx.textAlign = "center";
  const every = Math.max(1, Math.ceil(n / 7));
  points.forEach((p, i) => {
    if (i % every === 0 || i === n - 1) ctx.fillText(String(p.day || "").slice(5), X(i), H - 10);
  });

  // 交互层
  cv._geo = { pts: points, X, Y, pad, cw, ch, tip: tipEl };
  if (!cv._hoverBound) {
    cv._hoverBound = true;
    cv.addEventListener("mousemove", e => {
      const g = cv._geo; if (!g) return;
      const r = cv.getBoundingClientRect();
      const mx = e.clientX - r.left;
      let idx = Math.round((mx - g.pad.l) / (g.cw / Math.max(1, g.pts.length - 1)));
      idx = Math.max(0, Math.min(g.pts.length - 1, idx));
      drawFlowChart(cv, g.pts, g.tip);
      drawCrosshair(cv, g, idx);
    });
    cv.addEventListener("mouseleave", () => {
      const g = cv._geo; if (!g) return;
      if (g.tip) g.tip.classList.remove("show");
      drawFlowChart(cv, g.pts, g.tip);
    });
  }
}
function drawCrosshair(cv, g, idx) {
  const { ctx } = { ctx: cv.getContext("2d") };
  const dpr = window.devicePixelRatio || 1;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  const p = g.pts[idx];
  const x = g.X(idx);
  const cUp = cssVar("--chart-up"), cDn = cssVar("--chart-down");
  ctx.strokeStyle = cssVar("--muted"); ctx.globalAlpha = .45; ctx.lineWidth = 1;
  ctx.setLineDash([4, 4]);
  ctx.beginPath(); ctx.moveTo(x, g.pad.t); ctx.lineTo(x, g.pad.t + g.ch); ctx.stroke();
  ctx.setLineDash([]); ctx.globalAlpha = 1;
  [["down", p.down, cDn], ["up", p.up, cUp]].forEach(([k, v, c]) => {
    const y = g.Y(v);
    ctx.beginPath(); ctx.arc(x, y, 4.5, 0, Math.PI * 2);
    ctx.fillStyle = c; ctx.fill();
    ctx.lineWidth = 2; ctx.strokeStyle = cssVar("--card"); ctx.stroke();
  });
  if (g.tip) {
    g.tip.innerHTML = `<div class="t-d">${p.day || ""}</div>
      <div class="t-r"><span><i style="background:${cDn}"></i>下行</span><b>${fmtBytes(p.down)}</b></div>
      <div class="t-r"><span><i style="background:${cUp}"></i>上行</span><b>${fmtBytes(p.up)}</b></div>
      <div class="t-r"><span>合计</span><b>${fmtBytes(p.up + p.down)}</b></div>`;
    g.tip.style.left = x + "px";
    g.tip.style.top = (g.pad.t + 8) + "px";
    g.tip.classList.add("show");
  }
}

/* —— 迷你折线（KPI 卡内） —— */
function drawSpark(cv, values, color) {
  const s = chartSetup(cv, 52); if (!s) return;
  const { ctx, W, H } = s;
  if (!values || values.length < 2) return;
  const max = Math.max(1, ...values);
  const pts = values.map((v, i) => [ (W - 4) * i / (values.length - 1) + 2, H - 4 - (H - 10) * (v / max) ]);
  ctx.beginPath(); smoothPath(ctx, pts);
  ctx.lineTo(pts[pts.length - 1][0], H); ctx.lineTo(pts[0][0], H); ctx.closePath();
  const g = ctx.createLinearGradient(0, 0, 0, H);
  g.addColorStop(0, hexA(color, .35)); g.addColorStop(1, hexA(color, 0));
  ctx.fillStyle = g; ctx.fill();
  ctx.beginPath(); smoothPath(ctx, pts);
  ctx.strokeStyle = color; ctx.lineWidth = 1.6; ctx.stroke();
}

/* —— 实时速率双线 —— */
function drawLiveChart(cv, hist) {
  const s = chartSetup(cv, 120); if (!s) return;
  const { ctx, W, H } = s;
  if (!hist || hist.length < 2) {
    ctx.fillStyle = cssVar("--muted"); ctx.font = "12px " + (cssVar("--font") || "sans-serif");
    ctx.textAlign = "center"; ctx.fillText("等待采样数据…", W / 2, H / 2);
    return;
  }
  const max = Math.max(1, ...hist.map(h => Math.max(h.up, h.down)));
  const cUp = cssVar("--chart-up"), cDn = cssVar("--chart-down"), cGrid = cssVar("--grid");
  ctx.font = "10px " + (cssVar("--font") || "sans-serif");
  ctx.textBaseline = "middle";
  // 速率标签动态留白，避免长标签（如 "12.5 MB/s"）被左缘裁切
  let yw = 0;
  for (let i = 0; i <= 2; i++) yw = Math.max(yw, ctx.measureText(fmtRate(max * i / 2) + "/s").width);
  const pad = { l: Math.min(W - 100, Math.ceil(yw) + 12), r: 8, t: 10, b: 16 };
  const cw = W - pad.l - pad.r, ch = H - pad.t - pad.b;
  for (let i = 0; i <= 2; i++) {
    const y = pad.t + ch - ch * i / 2;
    ctx.strokeStyle = cGrid; ctx.lineWidth = 1;
    ctx.beginPath(); ctx.moveTo(pad.l, y + .5); ctx.lineTo(W - pad.r, y + .5); ctx.stroke();
    ctx.fillStyle = cssVar("--muted"); ctx.textAlign = "right";
    ctx.fillText(fmtRate(max * i / 2) + "/s", pad.l - 6, y);
  }
  const X = i => pad.l + cw * i / (hist.length - 1);
  const Y = v => pad.t + ch - ch * (v / max);
  [{ key: "down", color: cDn }, { key: "up", color: cUp }].forEach(s => {
    const pts = hist.map((h, i) => [X(i), Y(h[s.key] || 0)]);
    ctx.beginPath(); smoothPath(ctx, pts);
    ctx.lineTo(pts[pts.length - 1][0], pad.t + ch); ctx.lineTo(pts[0][0], pad.t + ch); ctx.closePath();
    const g = ctx.createLinearGradient(0, pad.t, 0, pad.t + ch);
    g.addColorStop(0, hexA(s.color, .28)); g.addColorStop(1, hexA(s.color, 0));
    ctx.fillStyle = g; ctx.fill();
    ctx.beginPath(); smoothPath(ctx, pts);
    ctx.strokeStyle = s.color; ctx.lineWidth = 1.8; ctx.stroke();
  });
}

/* —— 环形图 —— */
function drawDonut(cv, items) {
  const s = chartSetup(cv, 172); if (!s) return;
  const { ctx, W, H } = s;
  const total = items.reduce((s, i) => s + i.value, 0);
  const cx = W / 2, cy = H / 2, r = Math.min(W, H) / 2 - 12, ri = r * 0.62;
  if (!total) {
    ctx.strokeStyle = cssVar("--line"); ctx.lineWidth = r - ri;
    ctx.beginPath(); ctx.arc(cx, cy, (r + ri) / 2, 0, Math.PI * 2); ctx.stroke();
    ctx.fillStyle = cssVar("--muted"); ctx.font = "12px " + (cssVar("--font") || "sans-serif");
    ctx.textAlign = "center"; ctx.textBaseline = "middle"; ctx.fillText("暂无流量", cx, cy);
    return;
  }
  let a = -Math.PI / 2;
  items.forEach(it => {
    const ang = it.value / total * Math.PI * 2;
    ctx.beginPath();
    ctx.arc(cx, cy, r, a, a + ang - 0.012);
    ctx.arc(cx, cy, ri, a + ang - 0.012, a, true);
    ctx.closePath();
    const g = ctx.createLinearGradient(cx - r, cy - r, cx + r, cy + r);
    g.addColorStop(0, it.color); g.addColorStop(1, hexA(it.color, .62));
    ctx.fillStyle = g; ctx.fill();
    ctx.strokeStyle = cssVar("--card"); ctx.lineWidth = 1.5; ctx.stroke();
    a += ang;
  });
  ctx.fillStyle = cssVar("--text"); ctx.textAlign = "center"; ctx.textBaseline = "middle";
  ctx.font = "700 15px " + (cssVar("--font") || "sans-serif");
  ctx.fillText(fmtBytes(total), cx, cy - 7);
  ctx.fillStyle = cssVar("--muted"); ctx.font = "11px " + (cssVar("--font") || "sans-serif");
  ctx.fillText("总流量", cx, cy + 11);
}

/* —— 状态环（内核运行状态） —— */
function drawRing(cv, pct, color, label) {
  const s = chartSetup(cv, 92); if (!s) return;
  const { ctx, W, H } = s;
  const cx = W / 2, cy = H / 2, r = Math.min(W, H) / 2 - 9, lw = 7;
  ctx.lineCap = "round";
  ctx.strokeStyle = cssVar("--line"); ctx.lineWidth = lw;
  ctx.beginPath(); ctx.arc(cx, cy, r, 0, Math.PI * 2); ctx.stroke();
  if (pct > 0) {
    ctx.strokeStyle = color; ctx.lineWidth = lw;
    ctx.beginPath(); ctx.arc(cx, cy, r, -Math.PI / 2, -Math.PI / 2 + Math.PI * 2 * Math.min(1, pct));
    ctx.stroke();
  }
  ctx.fillStyle = color; ctx.textAlign = "center"; ctx.textBaseline = "middle";
  ctx.font = "700 15px " + (cssVar("--font") || "sans-serif");
  ctx.fillText(label, cx, cy);
}

/* ================= 仪表盘 ================= */
const DONUT_COLORS = ["--acc", "--acc2", "--chart-3", "--chart-4", "--ok", "--warn"];

function ovCoresRunning() { return OV.cores.filter(c => c.running).length; }

function renderKpis() {
  const total = OV.traffic.reduce((s, r) => s + (r.total || 0), 0);
  const running = ovCoresRunning(), totalC = OV.cores.length;
  const spark = OV.daily.slice(-14).map(p => p.down + p.up);
  const yesterday = OV.daily.length > 1 ? (OV.daily[OV.daily.length - 2].up + OV.daily[OV.daily.length - 2].down) : 0;
  const today = OV.daily.length ? (OV.daily[OV.daily.length - 1].up + OV.daily[OV.daily.length - 1].down) : 0;
  const delta = yesterday ? Math.round((today - yesterday) / yesterday * 100) : 0;
  const deltaHTML = yesterday
    ? `<span class="${delta >= 0 ? "up" : "down"}">${delta >= 0 ? "▲" : "▼"} ${Math.abs(delta)}%</span> 较昨日`
    : "暂无对比数据";
  $("kpis").innerHTML = `
    <div class="kpi">
      <div class="kpi-top"><div class="kpi-ic"><svg width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 2 3 7v6c0 5 4 8 9 9 5-1 9-4 9-9V7z"/></svg></div>
        <div class="kpi-lb">内核运行中</div></div>
      <div class="kpi-val">${running}<span class="unit">/ ${totalC}</span></div>
      <div class="kpi-delta">${running === totalC && totalC ? "全部在线" : (totalC - running) + " 个未运行"} · 各内核独立并存</div>
    </div>
    <div class="kpi">
      <div class="kpi-top"><div class="kpi-ic g2"><svg width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/></svg></div>
        <div class="kpi-lb">用户总数</div></div>
      <div class="kpi-val">${CLIENT_N}</div>
      <div class="kpi-delta">${OV.traffic.filter(r => r.enabled).length} 个启用中 · ${OV.traffic.filter(r => !r.enabled).length} 个停用</div>
    </div>
    <div class="kpi">
      <div class="kpi-top"><div class="kpi-ic g3"><svg width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg></div>
        <div class="kpi-lb">累计流量</div></div>
      <div class="kpi-val">${fmtBytes(total)}</div>
      <div class="kpi-delta">${deltaHTML}</div>
      <canvas class="kpi-spark" id="kpi_spark"></canvas>
    </div>
    <div class="kpi">
      <div class="kpi-top"><div class="kpi-ic g4"><svg width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="2" y="2" width="20" height="8" rx="2"/><rect x="2" y="14" width="20" height="8" rx="2"/></svg></div>
        <div class="kpi-lb">入站数量</div></div>
      <div class="kpi-val">${INB_N}</div>
      <div class="kpi-delta">${OV.cores.filter(c => c.config_exists).length} 个内核已有配置</div>
    </div>`;
  const sp = $("kpi_spark");
  if (sp) drawSpark(sp, spark.length > 1 ? spark : [0, 1], cssVar("--chart-down"));
}

function renderRings() {
  const el = $("ov_rings");
  if (!el) return;
  if (!OV.cores.length) { el.innerHTML = `<div class="muted" style="grid-column:1/-1">未获取到内核状态</div>`; return; }
  el.innerHTML = OV.cores.map((c, i) => {
    let pct = 0, color = cssVar("--muted"), label = "未配置", st = "未配置二进制路径";
    if (c.running) { pct = 1; color = cssVar("--ok"); label = "运行中"; st = "配置与进程正常"; }
    else if (c.config_exists) { pct = .5; color = cssVar("--warn"); label = "待启动"; st = "有配置但未运行"; }
    return `<div class="ring-card">
      <canvas id="ring_${i}" height="92"></canvas>
      <div class="nm">${c.core}</div><div class="st">${st}</div></div>`;
  }).join("");
  OV.cores.forEach((c, i) => {
    const cv = $("ring_" + i); if (!cv) return;
    let pct = 0, color = cssVar("--muted"), label = "未配";
    if (c.running) { pct = 1; color = cssVar("--ok"); label = "运行"; }
    else if (c.config_exists) { pct = .5; color = cssVar("--warn"); label = "待启"; }
    drawRing(cv, pct, color, label);
  });
}

function renderDonut() {
  const items = OV.traffic.filter(r => r.total > 0).sort((a, b) => b.total - a.total).slice(0, 6);
  const cv = $("ov_donut"); if (!cv) return;
  const data = items.map((r, i) => ({
    name: r.name, value: r.total,
    color: cssVar(DONUT_COLORS[i % DONUT_COLORS.length]) || cssVar("--acc")
  }));
  drawDonut(cv, data);
  const lg = $("ov_legend");
  if (lg) {
    const total = OV.traffic.reduce((s, r) => s + (r.total || 0), 0) || 1;
    lg.innerHTML = data.length ? data.map(d =>
      `<span class="li" style="width:100%"><i style="background:${d.color}"></i>
        <span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${d.name}</span>
        <b style="color:var(--text);font-variant-numeric:tabular-nums">${fmtBytes(d.value)}</b>
        <span style="width:44px;text-align:right">${(d.value / total * 100).toFixed(1)}%</span></span>`).join("")
      : `<span class="muted">暂无流量数据 —— 需在设置中开启统计并应用生效</span>`;
    lg.style.flexDirection = "column";
    lg.style.gap = "9px";
  }
}

function renderRank() {
  const el = $("ov_rank"); if (!el) return;
  const rows = OV.traffic.filter(r => r.total > 0).sort((a, b) => b.total - a.total).slice(0, 6);
  const max = rows.length ? rows[0].total : 1;
  el.innerHTML = rows.length ? rows.map((r, i) => {
    const flag = r.exceeded ? ' <span style="color:var(--err)" title="已达配额">✕</span>'
      : r.warn ? ' <span style="color:var(--warn)" title="接近配额">⚠</span>' : "";
    return `<div class="rank-row"><span class="idx">${i + 1}</span>
      <span class="nm" title="${r.name}">${r.name}${flag}</span>
      <span class="vl">${fmtBytes(r.total)}</span>
      <span class="bar"><i style="width:${(r.total / max * 100).toFixed(1)}%"></i></span></div>`;
  }).join("")
    : `<div class="muted" style="padding:14px 2px">暂无流量数据</div>`;
}

function renderMiniStats() {
  const el = $("ov_stats"); if (!el) return;
  const d = OV.daily || [];
  const total = d.reduce((s, p) => s + p.up + p.down, 0);
  const peak = d.reduce((m, p) => (p.up + p.down) > (m.v || 0) ? { v: p.up + p.down, day: p.day } : m, {});
  const active = d.filter(p => p.up + p.down > 0).length;
  el.innerHTML = `
    <div><div class="ms-l">区间合计</div><div class="ms-v">${fmtBytes(total)}</div></div>
    <div><div class="ms-l">日均流量</div><div class="ms-v">${fmtBytes(d.length ? total / d.length : 0)}</div></div>
    <div><div class="ms-l">峰值日${peak.day ? " · " + String(peak.day).slice(5) : ""}</div><div class="ms-v">${fmtBytes(peak.v || 0)}</div></div>`;
}

function drawOverviewChart() {
  const cv = $("ov_chart"); if (!cv) return;
  drawFlowChart(cv, OV.daily, $("ov_tip"));
  const total = OV.daily.reduce((s, p) => s + p.up + p.down, 0);
  const el = $("ov_total");
  if (el) el.textContent = "区间合计 " + fmtBytes(total) + " · 日均 " + fmtBytes(OV.daily.length ? total / OV.daily.length : 0);
}

async function loadOverview() {
  try {
    const [daily, traffic, status] = await Promise.all([
      req("/api/traffic/daily?days=14").catch(() => []),
      req("/api/traffic").catch(() => []),
      req("/api/status").catch(() => ({ cores: [] }))
    ]);
    OV.daily = daily || [];
    OV.traffic = traffic || [];
    OV.cores = (status && status.cores) || [];
    renderKpis(); drawOverviewChart(); renderMiniStats(); renderRings(); renderDonut(); renderRank();
  } catch (e) {}
}

function redrawCharts() {
  if ($("v-overview") && $("v-overview").classList.contains("active")) {
    drawOverviewChart(); renderRings(); renderDonut();
    const sp = $("kpi_spark");
    if (sp) drawSpark(sp, OV.daily.slice(-14).map(p => p.down + p.up), cssVar("--chart-down"));
  }
  if ($("v-traffic") && $("v-traffic").classList.contains("active")) loadDaily();
}

/* ---- 导入 ---- */
async function doImport() {
  try {
    const j = await req("/api/import", { method:"POST", body:{ text:$("im_text").value } });
    const ok = (j.created||[]).length, sk = (j.skipped||[]).length;
    $("immsg").innerHTML =
      (j.created||[]).map(s => `<div class="muted" style="color:var(--ok)">✓ ${escHtml(s)}</div>`).join("") +
      (j.skipped||[]).map(s => `<div class="muted">= ${escHtml(s)}</div>`).join("") +
      (j.failed||[]).map(s => `<div class="muted" style="color:var(--err)">✕ ${escHtml(s)}</div>`).join("") +
      (j.parse_errors||[]).map(s => `<div class="muted" style="color:var(--err)">✕ 解析失败: ${escHtml(s)}</div>`).join("");
    if (ok) { toast(`已导入 ${ok} 条外部节点` + (sk ? `，跳过 ${sk} 条重复` : "")); loadExternal(); }
    else if (sk) toast(`这 ${sk} 条已经在列表里了`, "info");
  } catch (e) { toast(e.message, "err"); }
}

/* ---- 出站 / 路由 / DNS（只读）---- */
// 这一页不做任何配置：服务端出站固定 direct，展示的只是"渲染出来是什么"。
// 价值在排障——「我导入的路由规则到底生效了没」「统计规则的顺序对不对」
// 这两个问题，看渲染结果最快。
async function loadOutbounds() {
  const core = $("ob_core").value;
  try {
    const j = await req("/api/outbounds?core=" + core);
    if (j.render_error) {
      $("obtbody").innerHTML = `<tr><td colspan="4" style="color:var(--err)">渲染失败：${escHtml(j.render_error)}</td></tr>`;
      $("obrbody").innerHTML = `<tr><td colspan="4" class="empty">先修掉上面的渲染错误</td></tr>`;
      $("ob_dns").textContent = "—"; $("ob_dns_note").textContent = "";
      $("ob_notes").innerHTML = "";
      return;
    }
    $("ob_notes").innerHTML = (j.notes || []).map(n =>
      `<div class="muted" style="font-size:12.5px;margin-bottom:4px">· ${escHtml(n)}</div>`).join("");

    const outs = j.outbounds || [];
    $("obtbody").innerHTML = outs.map(o => {
      const src = o.source === "imported"
        ? `<span style="color:var(--warn,#c47f17)">导入</span>`
        : `<span style="color:var(--ok)">内置</span>`;
      return `<tr><td class="mono">${escHtml(o.tag)}</td><td>${escHtml(o.type)}</td><td>${src}</td>
        <td class="muted">${escHtml(o.note || "")}</td></tr>`;
    }).join("") || `<tr><td colspan="4" class="empty">没有任何出站</td></tr>`;

    const rules = j.route_rules || [];
    $("obrbody").innerHTML = rules.map(r => {
      const src = r.source === "panel"
        ? `<span style="color:var(--acc-text)">面板统计</span>`
        : `<span style="color:var(--warn,#c47f17)">导入</span>`;
      const raw = escHtml(JSON.stringify(r.raw));
      return `<tr><td class="muted">${r.index}</td>
        <td style="max-width:520px" title="${raw}">${escHtml(r.summary)}</td>
        <td class="mono">${escHtml(r.outbound || "—")}</td><td>${src}</td></tr>`;
    }).join("") || `<tr><td colspan="4" class="empty">没有路由规则：全部流量直接出网</td></tr>`;

    if (j.dns) {
      $("ob_dns").textContent = JSON.stringify(j.dns, null, 2);
      $("ob_dns_note").textContent = j.dns_imported ? "来自导入的 3x-ui / xray 配置" : "";
    } else {
      $("ob_dns").textContent = "—（没有 dns 段，域名解析走内核默认）";
      $("ob_dns_note").textContent = "";
    }
  } catch (e) {
    $("obtbody").innerHTML = `<tr><td colspan="4" style="color:var(--err)">${escHtml(e.message)}</td></tr>`;
  }
}

/* ---- 外部节点 ---- */
// 这一页的每个字都来自后端的 parsed 摘要，**前端不再自己猜**：
// vmess:// 与 ss:// 的正文是 base64，用 new URL() 解析得到的是那坨 base64，
// 之前列表上「地址」一列显示的正是它——看着像地址，其实不是节点地址。
let EXT = [];
const EXT_NET = { tcp: "TCP", ws: "WebSocket", grpc: "gRPC", h2: "HTTP/2", http: "HTTP", quic: "QUIC", kcp: "mKCP", httpupgrade: "HTTPUpgrade" };
const EXT_SEC = { reality: "REALITY", tls: "TLS", none: "无 TLS" };

function extRow(n) {
  const p = n.parsed || {};
  const ok = !!p.ok;
  const proto = ok ? p.protocol : "—";
  const addr = ok ? `${p.host}:${p.port}` : "—";
  const trans = ok ? [EXT_NET[p.network] || p.network || "TCP", EXT_SEC[p.security] || p.security || "无 TLS"]
    .filter(Boolean).join(" · ") : "";
  const sni = ok && p.sni && p.sni !== p.host
    ? `<div class="muted" style="font-size:11.5px">SNI ${escHtml(p.sni)}</div>` : "";
  // 解析失败必须在列表上说出来：订阅渲染会静默跳过这类节点，
  // 用户看不到提示就只会觉得「明明加进去了怎么没有」。
  const state = ok
    ? `<span class="badge b-ok">可导出</span>`
    : `<span class="badge b-err">无法解析</span>
       <div style="color:var(--err);font-size:11.5px;margin-top:3px;white-space:normal;max-width:220px">这条不会出现在订阅里：${escHtml(p.error || "无法解析")}</div>`;
  return `<tr>
    <td style="white-space:normal;word-break:break-word;max-width:240px">
      <b>${escHtml(n.name)}</b>
      <div class="muted" style="font-size:11.5px">${escHtml((n.created_at || "").replace("T", " ").slice(0, 16))}</div>
    </td>
    <td><span class="badge b-acc">${escHtml(proto)}</span></td>
    <td class="mono" style="white-space:normal;word-break:break-all;max-width:220px">${escHtml(addr)}</td>
    <td style="font-size:12px">${escHtml(trans)}${sni}</td>
    <td style="white-space:normal">${state}</td>
    <td class="ta-r" style="white-space:nowrap">
      <button class="btn sm" data-act="editExternal" data-args="${escJson([n.id])}">编辑</button>
      <button class="btn sm danger" data-act="delExternal" data-args="${escJson([n.id])}">删除</button>
    </td></tr>`;
}

async function loadExternal() {
  try {
    EXT = await req("/api/external-nodes") || [];
    $("extbody").innerHTML = EXT.map(extRow).join("") ||
      `<tr><td colspan="6" class="empty">暂无外部节点 —— 在上方粘贴链接导入后显示</td></tr>`;
    const bad = EXT.filter(n => !(n.parsed && n.parsed.ok)).length;
    const sum = $("ext_sum");
    if (sum) sum.innerHTML = !EXT.length ? "" :
      `共 <b>${EXT.length}</b> 个 · ${EXT.length - bad} 个可导出` +
      (bad ? ` · <span style="color:var(--err)">${bad} 个无法解析</span>（不会出现在订阅里）` : "") +
      ` · 解析结果实时来自链接，不落库`;
  } catch (e) { /* 静默：这一页失败时表格留空，不弹重复错误 */ }
}

let EXT_EDIT_ID = 0;
function editExternal(id) {
  const n = EXT.find(x => x.id === id);
  if (!n) return;
  EXT_EDIT_ID = id;
  $("ext_name").value = n.name || "";
  $("ext_link").value = n.raw_link || "";
  const p = n.parsed || {};
  $("ext_hint").innerHTML = p.ok
    ? `当前链接解析为 <b>${escHtml(p.protocol)}</b> · ${escHtml(p.host)}:${p.port}` +
      `（${escHtml([EXT_NET[p.network] || p.network || "TCP", EXT_SEC[p.security] || p.security || "无 TLS"].join(" · "))}）`
    : `<span style="color:var(--err)">当前链接无法解析：${escHtml(p.error || "未知原因")}</span>`;
  openModal("mExt");
  setTimeout(() => { const el = $("ext_name"); if (el) { el.focus(); el.select(); } }, 40);
}

async function saveExternal() {
  const name = $("ext_name").value.trim();
  const link = $("ext_link").value.trim();
  if (!name) { toast("名称不能为空", "warn"); $("ext_name").focus(); return; }
  if (!link) { toast("链接不能为空", "warn"); $("ext_link").focus(); return; }
  try {
    await req("/api/external-nodes/" + EXT_EDIT_ID, { method: "PUT", body: { name, raw_link: link } });
    closeModal("mExt");
    toast("已保存");
    loadExternal();
  } catch (e) { toast(e.message, "err"); }
}

async function delExternal(id) {
  const n = EXT.find(x => x.id === id);
  const name = n ? n.name : "#" + id;
  if (!(await ask("删除外部节点", `删除「${name}」？它将从订阅中移除，不影响任何运行中的配置。`))) return;
  try { await req("/api/external-nodes/" + id, { method: "DELETE" }); toast("已删除"); loadExternal(); }
  catch (e) { toast(e.message, "err"); }
}

/* ---- 应用 / 修订 / 日志 ---- */
/* 「应用配置」是动作，不是页面：任何界面点常驻按钮都开 #mApply，关掉后仍停在
   原页面。以前是 goto("apply")——改完配置点一下，页面被换走，回来还得重新找位置，
   用户报的「违反操作逻辑」就是这个。 */
function openApply() {
  if (isViewer()) { toast("当前账号为只读角色，无权应用配置", "err"); return; }
  $("ap_res").classList.add("hide");
  $("ap_pre").textContent = "";
  $("ap_res_title").textContent = "";
  $("ap_res_sub").textContent = "";
  openModal("mApply");
}
/* 修订历史不占导航位，从弹窗里的「修订历史 →」进来；顺手把内核切到刚操作的那个，
   否则看的是另一个内核的记录，会以为刚才那次没留下修订。 */
function openRevisions() {
  const core = $("ap_core").value;
  closeModal("mApply");
  if (core) $("rev_core").value = core;
  goto("apply");
}
/* 预检 / 对比的结果就地展开在弹窗里，不叠第二层遮罩。 */
function apResult(title, sub) {
  $("ap_res").classList.remove("hide");
  $("ap_res_title").textContent = title;
  $("ap_res_sub").textContent = sub || "";
  return $("ap_pre");
}
async function dryRun() {
  const core = $("ap_core").value;
  const pre = apResult("预检 · " + core, "渲染 + 校验，不生效");
  pre.textContent = "渲染中…";
  try {
    const j = await req("/api/apply/dry-run", { method:"POST", body:{ core } });
    pre.textContent = j.content + "\n\n—— 校验: " +
      (j.result && j.result.validated ? "通过（内核校验）" : "未配置二进制，跳过校验");
  } catch (e) { pre.textContent = "预检失败：" + e.message; toast(e.message, "err"); }
}
async function applyCfg() {
  const core = $("ap_core").value;
  if (!(await ask("应用配置", "将把「" + core + "」的配置渲染 → 内核校验 → 原子替换 → 重启内核。校验失败时运行中的配置不会受任何影响。继续？"))) return;
  try {
    const j = await req("/api/apply", { method:"POST", body:{ core, note:"web apply" } });
    toast(`应用成功 · 修订 #${j.revision_id} · 校验${j.validated?"通过":"跳过"} · 内核${j.restarted?"已重启":"未重启"}`);
    // 应用完就关掉：这是「做完」的动作，弹窗留着反而挡住结果。当前页面不受影响。
    closeModal("mApply");
    $("rev_core").value = core;
    loadRevisions(); loadStatus();
  } catch (e) {
    toast(e.message, "err");
    toast("运行中的配置未被修改，服务不受影响", "info");
    loadRevisions();
  }
}
async function rollback() {
  if (!(await ask("回滚配置", "将恢复到最近一次自动备份并重启内核，确定？"))) return;
  try { const j = await req("/api/rollback", { method:"POST", body:{ core:$("ap_core").value } });
    toast("已从 " + j.restored_from + " 恢复");
    $("rev_core").value = $("ap_core").value;
    loadRevisions();
  } catch (e) { toast(e.message, "err"); }
}
async function loadRevisions() {
  const core = $("rev_core").value;
  try {
    const j = await req("/api/revisions?core=" + core + "&limit=20");
    $("revtbody").innerHTML = j.map(r => `<tr>
      <td class="mono">#${r.id}</td><td><span class="badge b-acc">${r.core}</span></td>
      <td><span class="badge ${r.status==="applied"?"b-ok":r.status==="pending"?"b-warn":"b-err"}">${r.status}</span></td>
      <td>${r.note}</td><td class="muted mono">${(r.created_at||"").replace("T"," ").slice(0,19)}</td>
      <td class="ta-r"><button class="btn sm" data-act="viewRevision" data-args="${escJson([r.id])}">查看</button></td></tr>`).join("")
      || `<tr><td colspan="6" class="empty">暂无修订记录</td></tr>`;
  } catch(e){}
}
async function viewRevision(id) {
  const core = $("rev_core").value;
  try {
    const j = await req("/api/revisions?core=" + core + "&limit=50");
    const r = j.find(x => x.id === id);
    if (r) viewContent("修订 #" + r.id + " · " + r.status, r.detail || r.content || "（无详情）");
  } catch(e){}
}
async function loadLogs() {
  try { const j = await req("/api/logs?core=" + $("lg_core").value + "&lines=80");
    $("lgout").textContent = (j.logs||[]).join("\n") || "（暂无日志）";
    $("lgout").scrollTop = $("lgout").scrollHeight;
  } catch (e) { $("lgout").textContent = e.message; }
}

/* ---- 设置 ---- */
/* 证书剩余天数：ACME 续期是后台自动跑的，一旦续期失败（80 端口被占、
   DNS 变了、Let's Encrypt 限流），证书会静默到期，等到浏览器报错就晚了。
   所以这里按剩余天数分级上色，14 天内直接标红——那时续期早该成功了。 */
function certDaysLeft(notAfter) {
  const t = Date.parse(notAfter);
  if (!t) return null;
  return Math.floor((t - Date.now()) / 86400000);
}
async function loadCerts() {
  try {
    const j = await req("/api/certs");
    const el = $("certinfo"), badge = $("cert_badge");
    if (!j.length) {
      el.classList.add("hide"); el.innerHTML = "";
      badge.classList.add("hide");
      return;
    }
    el.classList.remove("hide");
    let worst = 0; // 0 正常 / 1 即将到期 / 2 需要处理
    el.innerHTML = j.map(c => {
      const dom = escHtml(c.domain || "?");
      if (c.error) {
        worst = 2;
        return `<span style="color:var(--err)">✕ ${dom}：签发 / 续期失败 — ${escHtml(c.error)}</span>`;
      }
      const d = certDaysLeft(c.not_after);
      const until = String(c.not_after || "").replace("T", " ").slice(0, 16);
      let tail;
      if (d === null) {
        worst = Math.max(worst, 1);
        tail = `<span style="color:var(--warn)">到期时间未知</span>`;
      } else if (d < 0) {
        worst = 2;
        tail = `<span style="color:var(--err)">已过期 ${-d} 天</span>`;
      } else if (d <= 14) {
        worst = 2;
        tail = `<span style="color:var(--err)">仅剩 ${d} 天 · 自动续期可能已失败</span>`;
      } else if (d <= 30) {
        worst = Math.max(worst, 1);
        tail = `<span style="color:var(--warn)">剩余 ${d} 天</span>`;
      } else {
        tail = `<span style="color:var(--ok)">剩余 ${d} 天</span>`;
      }
      return `✓ ${dom}：有效期至 ${escHtml(until)} · ${tail} · ${escHtml(c.file || "")}`;
    }).join("\n");
    badge.classList.remove("hide", "b-ok", "b-warn", "b-err", "b-idle");
    if (worst === 2) { badge.classList.add("b-err"); badge.textContent = "证书需要处理"; }
    else if (worst === 1) { badge.classList.add("b-warn"); badge.textContent = "证书即将到期"; }
    else { badge.classList.add("b-ok"); badge.textContent = "证书正常"; }
  } catch(e){}
}
async function issueCert() {
  try {
    const j = await req("/api/certs/issue", { method:"POST" });
    $("certmsg").textContent = j.msg || "已触发";
    toast("已触发签发检查（HTTP-01 需域名解析到本机且 :80 可用）", "info");
    setTimeout(loadCerts, 6000);
    setTimeout(loadCerts, 20000);
    setTimeout(() => { $("certmsg").textContent = ""; }, 10000);
  } catch(e){ toast(e.message, "err"); }
}
/* HTTPS 一键引导 */
function openHttpsModal() {
  $("hs_domain").value = $("s_acme_domain").value || $("s_host").value || "";
  $("hs_email").value = $("s_acme_email").value || "";
  $("mHttps").classList.add("show");
  syncBodyLock();
  $("hs_domain").focus();
}
async function submitHTTPS() {
  const domain = $("hs_domain").value.trim(), email = $("hs_email").value.trim();
  if (!domain) { toast("请填写面板域名", "err"); return; }
  if (!email) { toast("请填写邮箱（用于证书到期通知）", "err"); return; }
  try {
    await req("/api/settings", { method:"PUT", body:{
      acme_enabled:"1", acme_domain:domain, acme_email:email } });
    // 同步回设置卡片，避免状态不一致
    $("s_acme").checked = true; $("s_acme_domain").value = domain; $("s_acme_email").value = email;
    closeModal("mHttps");
    toast("已启用 HTTPS 并触发签发：HTTP-01 验证通常几秒到一分钟，稍后刷新证书卡片查看结果", "info");
    await issueCert();
    $("certmsg").textContent = "正在为 " + domain + " 签发证书…";
  } catch (e) { toast("启用失败：" + e.message, "err"); }
}
async function selfUpdate() {
  if (!(await ask("面板自更新", "下载新版本并重启面板？更新自动备份旧文件为 *.bak，数据与配置不受影响。"))) return;
  toast("下载中…", "info");
  try {
    const j = await req("/api/self-update", { method:"POST", body:{ url:$("up_url").value, sha256:$("up_sha").value } });
    // 记住期望版本：刷新回来先核对一次。只报「更新完成」是不够的——
    // 重启那一步失败时进程仍跑旧映像，用户得等到发现版本号没变才起疑。
    try { sessionStorage.setItem("expectVer", normVer(j.new_version || "")); } catch(_) {}
    toast("更新完成，面板重启中，5 秒后刷新页面");
    setTimeout(() => location.reload(), 5000);
  } catch (e) { toast(e.message, "err"); }
}
/* 外部节点是否混入订阅：开关只影响订阅输出，不动本机内核配置。
   默认关闭——外部节点是全局的，混进去等于给每个用户都塞上别人的节点。 */
function extSwitchLabel(on) {
  const el = $("ext_sw_state");
  if (el) el.textContent = on ? "已开启：外部节点会进入每个用户的订阅" : "已关闭：订阅只包含本机入站";
}
async function saveExtInSub(on) {
  try {
    await req("/api/settings", { method:"PUT", body:{ external_in_sub: on ? "1" : "0" } });
    extSwitchLabel(on);
    toast(on ? "已开启：外部节点将混入所有用户的订阅" : "已关闭：订阅只含本机节点");
  } catch (e) {
    // 保存失败要把勾选状态退回去，否则界面显示"已开启"但实际没生效
    $("ext_in_sub").checked = !on;
    extSwitchLabel(!on);
    toast(e.message, "err");
  }
}

/* 二进制换了要不要自己切过去（后端 watch.go 的守望者）。
   默认开：用户要的是「升级完成后立刻是最新版」，而不是「面板提示该重启了」。 */
async function saveAutoSwitch(on) {
  try {
    await req("/api/settings", { method:"PUT", body:{ auto_switch_panel: on ? "1" : "0" } });
    toast(on ? "已开启：磁盘上的二进制换了新版本会自动切过去" : "已关闭：换二进制后需要手动重启");
  } catch (e) {
    $("s_autoswitch").checked = !on;
    toast(e.message, "err");
  }
}

/* ---- 面板 HTTPS ----
   自签证书能加密、不能证明身份，所以这里必须把指纹摊开：
   「首次信任时人工比对指纹」是自签唯一能落地的认证手段。
   只显示一个绿色「已加密」会让人以为有了证书就等于安全。 */
async function loadPanelTLS() {
  const badge = $("tls_state");
  if (!badge) return;
  if (DEMO) {
    $("s_panel_https").checked = false;
    badge.className = "badge b-idle"; badge.textContent = "演示";
    $("tls_active").textContent = "HTTP（明文，演示数据）";
    $("tls_source").textContent = "—";
    $("tls_sans").textContent = "127.0.0.1 / 10.0.0.2";
    $("tls_expire").textContent = "—";
    $("tls_fp").textContent = "—";
    return;
  }
  try {
    const j = await req("/api/panel/tls");
    $("s_panel_https").checked = !!j.enabled;
    if (j.active) {
      badge.className = "badge b-ok"; badge.textContent = "HTTPS 运行中";
      $("tls_active").textContent = "HTTPS（TLS）";
    } else if (j.enabled) {
      // 开了但没生效：最可能是刚保存还没重启。必须说清楚，
      // 否则用户会以为已经在加密连接上，实际还在明文传密码。
      badge.className = "badge b-warn"; badge.textContent = "已开启，待重启";
      $("tls_active").textContent = "HTTP（明文）— 设置已开启，重启面板后生效";
    } else {
      badge.className = "badge b-idle"; badge.textContent = "未启用";
      $("tls_active").textContent = "HTTP（明文）";
    }
    if (!j.cert_ready) {
      $("tls_source").textContent = "尚未生成（启用后首次启动自动生成自签证书）";
      $("tls_expire").textContent = "—";
      $("tls_fp").textContent = "—";
      $("tls_sans").textContent = "—";
      return;
    }
    $("tls_source").textContent = j.source === "user"
      ? "用户提供（面板不会覆盖，升级/重启都不动它）"
      : "面板自签（浏览器会警告，需在设备上手动信任）";
    $("tls_sans").textContent = (j.sans || []).join("  ") || "—";
    const days = j.days_left;
    $("tls_expire").textContent = (j.not_after || "").slice(0, 10) +
      (j.expired ? "（已过期！）" : days != null ? `（剩 ${days} 天）` : "");
    $("tls_expire").style.color = j.expired ? "var(--err)" : days != null && days < 15 ? "var(--warn)" : "";
    $("tls_fp").textContent = j.fingerprint || "—";
  } catch(e){
    badge.className = "badge b-idle"; badge.textContent = "未知";
  }
}

async function savePanelTLS(on) {
  try {
    await req("/api/settings", { method:"PUT", body:{ panel_https: on ? "1" : "0" } });
    // 重启后加密方式会变，当前页面还在旧协议上——旧页面的请求会失效，
    // 所以只提示怎么做，不假装「已生效」。
    $("tls_msg").textContent = on
      ? "已保存。执行 sudo systemctl restart triune-panel 后生效，之后请用 https:// 访问（浏览器会提示证书不受信任，属正常）。"
      : "已保存。重启面板后恢复 http:// 访问。";
    toast(on ? "已开启 HTTPS，重启面板后生效" : "已关闭 HTTPS，重启面板后生效");
    loadPanelTLS();
  } catch(e){
    $("s_panel_https").checked = !on;
    toast(e.message, "err");
  }
}

async function downloadPanelCert() {
  if (DEMO) { toast("演示模式不提供下载", "info"); return; }
  try {
    // 下载接口要带 Bearer，不能直接 location.href（那样会 401 到登录页）
    const r = await fetch("/api/panel/tls/cert", { headers: { Authorization: "Bearer " + TOKEN } });
    if (!r.ok) {
      let msg = "下载失败（" + r.status + "）";
      try { const j = await r.json(); if (j.error) msg = j.error; } catch(_) {}
      throw new Error(msg);
    }
    saveBlob(await r.blob(), "triune-panel.crt");
    toast("已下载公钥证书；私钥留在服务器上，不会外传");
  } catch(e){ toast(e.message, "err"); }
}

async function loadSettings() {
  // 有没保存的改动就别回填：goto 离开设置页再回来会走 loadAll，
  // 这一趟会把用户正在编辑的内容整个冲掉，而且悄无声息。
  // 保存成功后脏标记就清了，下次进来照常刷新。
  if (SET_DIRTY.size) return;
  try {
    const j = await req("/api/settings");
    $("s_binsingbox").value = j.bin_singbox || ""; $("s_binxray").value = j.bin_xray || "";
    $("s_binmihomo").value = j.bin_mihomo || ""; $("s_host").value = j.public_host || "";
    $("s_stats").checked = j.stats_enabled === "1"; $("s_stats_secret").value = j.stats_secret || "";
    $("ext_in_sub").checked = j.external_in_sub === "1"; extSwitchLabel(j.external_in_sub === "1");
    $("s_metrics_token").value = j.metrics_token || "";
    $("s_warn_pct").value = j.quota_warn_pct || 80;
    $("s_fwauto").checked = j.fw_auto !== "0";
    $("s_compat").checked = j.show_compat_core === "1";
    __compatCore = j.show_compat_core === "1";
    // 未设置 = 默认开启，只有显式 "0" 才是关
    $("s_autoswitch").checked = j.auto_switch_panel !== "0";
    $("s_trusted_proxies").value = j.trusted_proxies || "";
    $("s_acme").checked = j.acme_enabled === "1"; $("s_acme_domain").value = j.acme_domain || ""; $("s_acme_email").value = j.acme_email || "";
    $("s_gh_mirror").value = j.gh_mirror || ""; $("s_panel_index").value = j.panel_update_index || "";
    $("s_auth_email").value = j.auth_email || "";
    $("s_smtp_host").value = j.smtp_host || ""; $("s_smtp_port").value = j.smtp_port || "";
    $("s_smtp_user").value = j.smtp_user || ""; $("s_smtp_from").value = j.smtp_from || "";
    applyNotifyValues(j);
    // 回填的是服务器真值，之前的「未保存」已经不存在了
    SET_SAVE_CARDS.forEach(id => markDirty(id, false));
  } catch(e){}
}

/* ---- 变更通知 ----
   订阅项按审计动作前缀登记：勾「设置变更」= settings（含 settings.update /
   settings.password）。全不选时后端用默认集合（不含登录，否则收件箱很快就没人看）。 */
const NOTIFY_EVENTS = [
  ["settings","设置变更"], ["inbound","入站增删改"], ["client","用户增删改"],
  ["admin","管理员账号"], ["apply","应用配置"], ["session","吊销会话"],
  ["auth2fa","二次验证"], ["update","版本更新"]
];
function renderNotifyEvents() {
  const box = $("nt_events");
  if (!box) return;
  box.innerHTML = NOTIFY_EVENTS.map(([k, label]) =>
    `<label><input type="checkbox" value="${k}"> ${label}</label>`).join("");
}
renderNotifyEvents();

function applyNotifyValues(j) {
  if (!$("s_notify_email")) return;
  $("s_notify_email").value = j.notify_email || "";
  $("s_notify_webhook").value = j.notify_webhook || "";
  const sel = String(j.notify_events || "").split(",").map(s => s.trim()).filter(Boolean);
  document.querySelectorAll("#nt_events input").forEach(i => { i.checked = sel.includes(i.value); });
  const ch = [];
  if ((j.notify_email || "").trim()) ch.push("邮件");
  if ((j.notify_webhook || "").trim()) ch.push("Webhook");
  $("nt_state").textContent = ch.length ? "已启用：" + ch.join(" + ") : "未启用";
}

async function saveNotify() {
  const events = [...document.querySelectorAll("#nt_events input:checked")].map(i => i.value).join(",");
  try {
    await req("/api/settings", { method: "PUT", body: {
      notify_email: $("s_notify_email").value.trim(),
      notify_webhook: $("s_notify_webhook").value.trim(),
      notify_events: events,
    }});
    markDirty("set_card_notify", false);
    $("nt_msg").textContent = "已保存";
    toast("通知设置已保存");
    // 这里原来调 loadSettings() 整体重载，会把「基础设置」里还没保存的改动
    // 一起冲掉。nt_state 只依赖刚存过的两个输入框，就地重算即可。
    const ch = [];
    if ($("s_notify_email").value.trim()) ch.push("邮件");
    if ($("s_notify_webhook").value.trim()) ch.push("Webhook");
    $("nt_state").textContent = ch.length ? "已启用：" + ch.join(" + ") : "未启用";
  } catch (e) { toast("保存失败：" + e.message, "err"); }
}

async function testNotify() {
  $("nt_msg").textContent = "发送中…";
  try {
    const j = await req("/api/notify/test", { method: "POST" });
    if (j.ok) {
      $("nt_msg").textContent = "已发送：" + (j.channels || []).join("、");
      toast("测试通知已发送");
    } else {
      $("nt_msg").textContent = (j.errors || []).join("；");
      toast("测试通知发送失败", "err");
    }
  } catch (e) {
    $("nt_msg").textContent = e.message;
    toast("测试失败：" + e.message, "err");
  }
}

/* ================= 登录二次验证（2FA 管理） ================= */

/* TOTP 绑定二维码：内容就是后端给的 otpauth:// URI，和下方只读输入框一字不差。
   必须本地画（qr.js 已在 /assets 里）——把密钥发给第三方二维码服务等于把
   二次验证直接交出去。白底 + 4 模块静区是手机识别率的最低要求。 */
function renderTotpQR(uri) {
  const cv = $("fa_totp_qr"), msg = $("fa_totp_qr_msg");
  if (!cv) return;
  const size = 180;
  cv.width = cv.height = size;                 // 顺带擦掉上一次的密钥
  const ctx = cv.getContext("2d");
  ctx.fillStyle = "#fff"; ctx.fillRect(0, 0, size, size);
  try {
    const qr = qrcode(0, "M");
    qr.addData(uri); qr.make();
    const n = qr.getModuleCount();
    const cell = Math.max(1, Math.floor((size - 16) / n));  // 上下左右各留 4 模块静区
    const off = Math.floor((size - cell * n) / 2);
    ctx.fillStyle = "#111";
    for (let r = 0; r < n; r++) for (let c = 0; c < n; c++)
      if (qr.isDark(r, c)) ctx.fillRect(off + c * cell, off + r * cell, cell, cell);
    if (msg) msg.textContent = "二维码已生成：扫码后输入下方 6 位口令确认绑定。";
  } catch (e) {
    if (msg) msg.textContent = "二维码生成失败，请手动输入下方密钥：" + e.message;
  }
}
/* 启用成功 / 关闭 2FA 后把暂存密钥从界面上清掉：二维码贴在屏幕上等于把
   二次验证的凭据留在明处，别人路过拍一张就能算出后续所有动态口令。 */
function clearTotpStaged() {
  ["fa_secret", "fa_uri", "fa_totp_code"].forEach(id => { $(id).value = ""; });
  const cv = $("fa_totp_qr");
  if (cv) {
    cv.width = cv.height = 180;
    const ctx = cv.getContext("2d");
    ctx.fillStyle = "#fff"; ctx.fillRect(0, 0, 180, 180);
  }
  $("fa_totp_qr_msg").textContent = "";
  $("fa_totp_box").classList.add("hide");
  $("fa_totp_start").classList.remove("hide");
}
async function faCopyUri() {
  const uri = $("fa_uri").value;
  if (!uri) { toast("请先生成密钥", "warn"); return; }
  try {
    if (!navigator.clipboard) throw new Error("clipboard unavailable");
    await navigator.clipboard.writeText(uri);
    toast("绑定 URI 已复制");
  } catch (e) {
    viewContent("绑定 URI", uri);   // file:// / 非安全上下文里选中复制兜底
  }
}

async function loadAuth2FA() {
  try {
    const j = await req("/api/auth2fa");
    const names = { off:"状态：未开启", totp:"状态：TOTP 动态口令", email:"状态：邮箱验证码" };
    $("fa_mode").textContent = names[j.mode] || ("状态：" + (j.mode || "off"));
    $("fa_off").classList.toggle("hide", !j.mode || j.mode === "off");
  } catch(e){}
}
async function faTotpSetup() {
  try {
    const j = await req("/api/auth2fa/totp/setup", { method:"POST", body:{} });
    $("fa_totp_box").classList.remove("hide");
    $("fa_totp_start").classList.add("hide");
    $("fa_secret").value = j.secret;
    $("fa_uri").value = j.uri;
    $("fa_totp_code").value = "";
    renderTotpQR(j.uri || "");
    toast("密钥已生成：用验证器 App 扫码，或手动输入密钥，再输入当前口令确认", "info");
  } catch(e){ toast(e.message, "err"); }
}
async function faTotpEnable() {
  const code = $("fa_totp_code").value.trim();
  if (!code) { toast("请先输入验证器 App 显示的 6 位口令", "warn"); return; }
  try {
    await req("/api/auth2fa/totp/enable", { method:"POST", body:{ code } });
    toast("TOTP 二次验证已启用，下次登录需要输入动态口令");
    clearTotpStaged();
    loadAuth2FA();
  } catch(e){ toast(e.message, "err"); }
}
function faEmailBody() {
  return { email:$("s_auth_email").value.trim(), smtp_host:$("s_smtp_host").value.trim(),
    smtp_port:$("s_smtp_port").value.trim(), smtp_user:$("s_smtp_user").value.trim(),
    smtp_pass:$("s_smtp_pass").value, smtp_from:$("s_smtp_from").value.trim() };
}
async function faEmailEnable() {
  const b = faEmailBody();
  if (!b.email.includes("@")) { toast("请填写有效的收件邮箱", "warn"); return; }
  if (!b.smtp_host) { toast("请填写 SMTP 服务器地址", "warn"); return; }
  try {
    await req("/api/auth2fa/email/enable", { method:"POST", body:b });
    $("s_smtp_pass").value = "";
    toast("邮箱验证码二次验证已启用，测试邮件已发送到 " + b.email);
    loadAuth2FA();
  } catch(e){ toast(e.message, "err"); }
}
async function faEmailTest() {
  const b = faEmailBody();
  try {
    // 先保存表单（不含密码——留空即保持已保存值），再用已保存配置发测试邮件
    await req("/api/settings", { method:"PUT", body:{ auth_email:b.email, smtp_host:b.smtp_host,
      smtp_port:b.smtp_port, smtp_user:b.smtp_user, smtp_from:b.smtp_from } });
    await req("/api/auth2fa/email/test", { method:"POST", body:{} });
    toast("测试邮件已发送，请查收");
  } catch(e){ toast(e.message, "err"); }
}
async function faDisable() {
  if (!(await ask("关闭二次验证", "关闭后登录仅需账号密码，安全性将降低。确认关闭？"))) return;
  try {
    await req("/api/auth2fa/disable", { method:"POST", body:{} });
    toast("二次验证已关闭");
    clearTotpStaged();
    loadAuth2FA();
  } catch(e){ toast(e.message, "err"); }
}

/* ================= 组件更新与版本管理 ================= */
let compData = null;
async function loadComponents() {
  try {
    compData = await req("/api/components");
    const rows = [];
    const p = compData.panel;
    rows.push(`<tr><td><b>panel</b>（本面板）</td><td class="mono">${p.version}</td><td><span class="badge b-ok">运行中</span></td>
      <td class="ta-r"><button class="btn sm" data-act="fetchVersions" data-args='["panel"]'>版本列表</button>
      <button class="btn sm" data-act="panelRollbackDlg">回滚</button></td></tr>`);
    for (const c of compData.cores) {
      const st = c.running ? '<span class="badge b-ok">运行中</span>'
        : c.version_error ? `<span class="badge b-warn" title="${c.version_error}">二进制异常</span>`
        : '<span class="badge b-idle">未运行</span>';
      const primary = c.bin_set
        ? `<button class="btn sm" data-act="fetchVersions" data-args="${escJson([c.component])}">版本列表</button>`
        : `<select class="sel-sm" id="ch_${c.component}" data-comp="${c.component}" data-act="pickChannel" data-args='[]' data-el="1" title="安装分支：稳定版不含 alpha / beta / rc">
             ${channelOptions(c.component)}</select>
           <button class="btn sm primary" data-act="fetchVersions" data-args="${escJson([c.component,true])}">一键安装</button>`;
      rows.push(`<tr>
        <td><b>${c.component}</b><div class="muted mono" style="font-size:11.5px">${c.bin || "未配置路径"}</div></td>
        <td class="mono">${c.version || '<span class="muted">未知</span>'}</td>
        <td>${st}</td>
        <td class="ta-r">${primary}
          ${c.backups && c.backups.length ? `<button class="btn sm" data-act="coreRollbackDlg" data-args="${escJson([c.component])}">回滚(${c.backups.length})</button>` : ""}</td></tr>`);
    }
    $("updtbody").innerHTML = rows.join("");
  } catch(e){ $("updtbody").innerHTML = `<tr><td colspan="4" class="muted">加载失败</td></tr>`; }
}

/* ================= 下载镜像 ================= */
// 候选来自后端（update.Mirrors），前端不写死任何镜像地址：
// 加速站换域名是常态，改一处比改两处可靠。
let MIRROR_LIST = null;
const OFFICIAL = { id: "official", name: "官方地址", base: "" };

async function loadMirrors() {
  if (MIRROR_LIST) return MIRROR_LIST;
  try {
    const j = await req("/api/mirrors");
    MIRROR_LIST = j.mirrors && j.mirrors.length ? j.mirrors : [OFFICIAL];
  } catch (e) {
    MIRROR_LIST = [OFFICIAL]; // 拿不到列表也不能挡住安装，至少留官方地址
  }
  return MIRROR_LIST;
}

// 画镜像下拉。results 为空 = 还没测速，按原始顺序（官方地址在最前）。
// 测速之后只保留可用的：把九个「不可用」摆在列表里除了让人犹豫没别的作用，
// 具体挂了几个写在下面的提示里。
function renderMirrorOptions(results, pick) {
  let list;
  if (results) {
    list = results.filter(m => m.ok);
    if (!list.length) list = [OFFICIAL];
  } else {
    list = MIRROR_LIST || [OFFICIAL];
  }
  $("vs_mirror").innerHTML = list.map(m => {
    const tag = results ? ` · ${m.ms}ms` : "";
    return `<option value="${m.base}">${m.name}${tag}</option>`;
  }).join("");
  const want = pick !== undefined ? pick : "";
  $("vs_mirror").value = want;
  // 选中的镜像不在列表里（列表变了）就退回第一个，而不是留一个空白选项
  if ($("vs_mirror").value === "" && want !== "") $("vs_mirror").selectedIndex = 0;
}

function mirrorMsg(text, cls) {
  const el = $("vs_mirror_msg");
  el.className = "mirror-msg" + (cls ? " " + cls : "");
  el.innerHTML = text;
}

async function testMirrors() {
  const comp = $("vs_component").textContent;
  const ver = verList[+$("vs_select").value]?.tag || "";
  const url = $("vs_url").value.trim();
  if (!url && !ver) { toast("先选一个版本再测速", "warn"); return; }
  const btn = $("vs_probe");
  btn.disabled = true;
  const old = btn.textContent;
  btn.textContent = "测速中…";
  mirrorMsg("正在并发探测全部镜像（最多 6 秒）。测的是<b>你要下的那个文件</b>，只取首字节——加速站只转发 release 路径，拿别的地址测会误判成挂了。");
  try {
    const j = await req("/api/mirrors/test", { method: "POST", body: url ? { url } : { component: comp, version: ver } });
    const results = j.results || [];
    const best = results.find(r => r.ok);
    if (!best) {
      renderMirrorOptions(results, "");
      mirrorMsg("所有镜像和官方地址都不可达 —— 先检查本机网络或代理设置，再试一次。", "err");
      return;
    }
    renderMirrorOptions(results, best.base);
    const dead = results.length - j.ok_count;
    const tail = dead ? `，${dead} 个不可用已从列表里隐藏` : "";
    if (!best.base) {
      mirrorMsg(`<b>官方地址最快</b>（${best.ms}ms），不用镜像${tail}。`, "ok");
    } else {
      mirrorMsg(`已选择 <b>${best.name}</b>（${best.ms}ms），其余按延迟排在后面${tail}。`, "ok");
    }
  } catch (e) {
    mirrorMsg("测速失败：" + e.message, "err");
  } finally {
    btn.disabled = false;
    btn.textContent = old;
  }
}

/* 版本列表弹窗（复用通用内容弹窗思路，独立实现带选择） */
let verList = [], verAll = [], verCounts = {}, verChannel = "stable", verInstallMode = false;
// 每个内核记住用户上次选的分支：装过一次之后再看别的内核，不该又跳回默认。
const CHANNEL_PICK = {};
const CHANNEL_DEFS = [["stable", "稳定版"], ["prerelease", "预发布"], ["all", "全部"]];
function channelOptions(comp) {
  const cur = CHANNEL_PICK[comp] || "stable";
  return CHANNEL_DEFS.map(([v, n]) =>
    `<option value="${v}"${v === cur ? " selected" : ""}>${n}</option>`).join("");
}
function pickChannel(sel) {
  CHANNEL_PICK[sel.dataset.comp] = sel.value;
}

async function fetchVersions(comp, install = false) {
  verInstallMode = install;
  // 一次拿全量，分支切换在本地过滤：换分支不该再等一次网络往返
  verChannel = install ? (CHANNEL_PICK[comp] || "stable") : "all";
  toast("正在获取版本列表…", "info");
  try {
    const j = await req("/api/updates/versions?component=" + comp + "&channel=all");
    verAll = j.releases || [];
    verCounts = j.counts || {};
    if (!verAll.length) { toast("版本列表为空", "warn"); return; }
    $("vs_component").textContent = comp;
    $("vs_title").firstChild.textContent = install ? "安装 " : "更新 ";
    $("vs_confirm").textContent = install ? "安装选中版本" : "更新到选中版本";
    $("vs_url").value = ""; $("vs_sha").value = "";
    $("vs_confirm").disabled = false;
    // 每次打开都回到官方地址：上一次选的镜像不该悄悄延续到另一次安装
    await loadMirrors();
    renderMirrorOptions(null, "");
    mirrorMsg("默认走官方地址。连不上或太慢时，点「自动选择最快」让面板并发测一遍。");
    renderChannelSeg();
    openModal("mVersions");
  } catch(e){ toast(e.message, "err"); }
}
// 分支条：括号里是该分支的版本数，空分支直接禁用（点了只会看到空列表）
function renderChannelSeg() {
  $("vs_chan").innerHTML = CHANNEL_DEFS.map(([v, n]) => {
    const c = verCounts[v] || 0;
    const on = v === verChannel ? " on" : "";
    return `<button data-ch="${v}" class="${on.trim()}"${c ? "" : " disabled"}
      data-act="setChannel" data-args="${escJson([v])}">${n} <span style="opacity:.6;font-weight:400">${c}</span></button>`;
  }).join("");
  renderVersionOptions();
}
function setChannel(ch) {
  if (ch === verChannel) return;
  verChannel = ch;
  renderChannelSeg();
}
function renderVersionOptions() {
  verList = verAll.filter(r => verChannel === "all" || (verChannel === "prerelease") === !!r.prerelease);
  if (!verList.length) {
    $("vs_select").innerHTML = `<option>该分支暂无版本</option>`;
    $("vs_confirm").disabled = true;
    $("vs_notes_wrap").style.display = "none";
    return;
  }
  $("vs_select").innerHTML = verList.map((r, i) =>
    `<option value="${i}">${r.tag}${r.prerelease ? " · 预发布" : ""}${r.sha256 ? " ✔" : ""}</option>`).join("");
  $("vs_confirm").disabled = false;
  pickVersion(); // 默认选中该分支最新版本并展示其 Release Notes
}
function pickVersion() {
  const r = verList[+$("vs_select").value];
  if (!r) return;
  $("vs_url").value = r.url || ""; $("vs_sha").value = r.sha256 || "";
  if (!r.url) $("vs_url").placeholder = `将按官方地址自动下载 ${r.tag}`;
  // Release Notes：轻量 markdown 渲染（标题/加粗/行内代码/列表/链接）
  const wrap = $("vs_notes_wrap"), box = $("vs_notes");
  if (r.notes && r.notes.trim()) {
    box.innerHTML = mdLite(r.notes);
    $("vs_notes_tag").textContent = r.tag;
    wrap.style.display = "";
  } else {
    box.innerHTML = ""; wrap.style.display = "none";
  }
}
// mdLite 极简 markdown → HTML（仅用于 Release Notes 展示，全部先做 HTML 转义）
function mdLite(src) {
  const esc = s => s.replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;");
  const inline = s => esc(s)
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<b>$1</b>")
    .replace(/\[([^\]]+)\]\((https?:[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
  const lines = String(src).split(/\r?\n/);
  let html = "", inUl = false, inCode = false, codeBuf = [];
  const closeUl = () => { if (inUl) { html += "</ul>"; inUl = false; } };
  for (const ln of lines) {
    if (/^```/.test(ln.trim())) {
      if (inCode) { html += "<pre>" + esc(codeBuf.join("\n")) + "</pre>"; codeBuf = []; inCode = false; }
      else { closeUl(); inCode = true; }
      continue;
    }
    if (inCode) { codeBuf.push(ln); continue; }
    let m;
    if ((m = ln.match(/^(#{1,4})\s+(.*)/))) {
      closeUl();
      html += `<div style="font-weight:600;margin:8px 0 2px">${inline(m[2])}</div>`;
    } else if ((m = ln.match(/^\s*[-*]\s+(.*)/))) {
      if (!inUl) { html += "<ul style=\"margin:4px 0 4px 18px;padding:0\">"; inUl = true; }
      html += `<li>${inline(m[1])}</li>`;
    } else if (ln.trim() === "") {
      closeUl();
    } else {
      closeUl();
      html += `<div style="margin:3px 0">${inline(ln)}</div>`;
    }
  }
  if (inCode && codeBuf.length) html += "<pre>" + esc(codeBuf.join("\n")) + "</pre>";
  closeUl();
  return html;
}
async function confirmVersionUpdate() {
  const comp = $("vs_component").textContent;
  const ver = verList[+$("vs_select").value]?.tag || "";
  const url = $("vs_url").value;
  const mirror = $("vs_mirror").value;
  if (!ver && !url) { toast("请选择版本或填写 URL", "warn"); return; }
  const isPanel = comp === "panel";
  const action = verInstallMode ? "安装" : "更新";
  // 走了镜像就在确认框里说清楚：下载来源变了，出事时能一眼看出走的哪条路
  const via = mirror ? `\n下载走镜像 ${mirror}（二进制仍要过可执行验证与配置校验）。` : "";
  if (!(await ask(action + " " + comp,
    (verInstallMode
      ? `确认下载 ${comp} ${ver || "指定版本"} 并安装到面板专用目录（data/bin/${comp}/）？\n不触碰系统里其他服务使用的二进制。`
      : `确认把 ${comp} ${ver ? "更新/降级到 " + ver : "更新到指定 URL 版本"}？\n全程安全保护：验证 → 校验 → 备份 → 替换 → 健康检查，失败自动回滚。`) + via))) return;
  $("vs_confirm").disabled = true;
  toast(verInstallMode ? "下载与安装中，请稍候…" : "下载与更新中，请稍候（视网络可能需要 1-3 分钟）…", "info");
  try {
    if (isPanel) {
      await req("/api/self-update", { method:"POST", body:{ url, sha256:$("vs_sha").value, mirror } });
      closeModal("mVersions");
      toast("更新完成，面板重启中，5 秒后刷新页面");
      setTimeout(() => location.reload(), 5000);
    } else {
      const j = await req("/api/cores/" + comp + "/update", { method:"POST", body:{ version: url ? "" : ver, url, sha256:$("vs_sha").value, mirror } });
      closeModal("mVersions");
      toast(j.installed
        ? `${comp} ${j.version} 已安装到面板专用目录（${j.target}），在「设置」中即可看到路径`
        : `${comp} 已更新：${j.version}${j.restarted ? "，进程已重启且健康检查通过" : ""}`, "ok");
      loadComponents(); loadStatus();
    }
  } catch(e){ toast(e.message, "err"); $("vs_confirm").disabled = false; }
}

async function panelRollbackDlg() {
  try {
    const j = await req("/api/panel/backups");
    const all = [...(j.backups||[])];
    const opts = all.map(b=>`<option>${b}</option>`).join("")
      + (j.bak_exists ? `<option value="__bak__">${"(最近一次自更新备份) *.bak"}</option>` : "");
    if (!opts) { toast("暂无历史备份（更新过才会有）", "warn"); return; }
    $("rb_select").innerHTML = opts;
    openModal("mRollback");
  } catch(e){ toast(e.message, "err"); }
}
async function doPanelRollback() {
  let b = $("rb_select").value;
  if (!(await ask("回滚面板", `确认回滚到 ${b} 并重启？数据与配置不受影响。`))) return;
  try {
    if (b === "__bak__") {
      // exe.bak 模式：直接用 self-update 的 URL 不适用 —— 后端 panel/rollback 仅支持版本化备份，
      // exe.bak 通过重启脚本恢复；这里提示用户
      toast("最近备份请通过恢复 exe.bak 的方式手动还原，或选择版本化备份", "warn");
      return;
    }
    await req("/api/panel/rollback", { method:"POST", body:{ backup:b } });
    closeModal("mRollback");
    toast("回滚完成，面板重启中，5 秒后刷新页面");
    setTimeout(() => location.reload(), 5000);
  } catch(e){ toast(e.message, "err"); }
}

async function coreRollbackDlg(comp) {
  const c = (compData?.cores||[]).find(x=>x.component===comp);
  const bs = c?.backups || [];
  if (!bs.length) { toast("暂无备份", "warn"); return; }
  const pick = prompt(`回滚 ${comp}，可用备份（复制一份粘贴）：\n\n${bs.join("\n")}\n\n输入要恢复的备份名：`);
  if (!pick) return;
  if (!(await ask("回滚内核", `确认把 ${comp} 回滚到 ${pick}？将自动重启并通过健康检查。`))) return;
  try {
    const j = await req("/api/cores/" + comp + "/rollback", { method:"POST", body:{ backup:pick } });
    toast(`${comp} 已回滚到 ${j.restored}，健康检查通过`, "ok");
    loadComponents(); loadStatus();
  } catch(e){ toast(e.message, "err"); }
}

async function saveUpdateSettings() {
  try {
    await req("/api/settings", { method:"PUT", body:{ gh_mirror:$("s_gh_mirror").value, panel_update_index:$("s_panel_index").value } });
    markDirty("set_card_update", false);
    $("updsetmsg").textContent = "已保存"; setTimeout(()=>$("updsetmsg").textContent="", 2500);
  } catch(e){ toast(e.message, "err"); }
}
// 生成 /metrics 访问令牌（24 字节 → 48 位十六进制）
function genMetricsToken(){
  const a = new Uint8Array(24); crypto.getRandomValues(a);
  $("s_metrics_token").value = [...a].map(b => b.toString(16).padStart(2, "0")).join("");
}
async function saveSettings() {
  const body = { bin_singbox:$("s_binsingbox").value, bin_xray:$("s_binxray").value,
    bin_mihomo:$("s_binmihomo").value, public_host:$("s_host").value,
    stats_enabled:$("s_stats").checked?"1":"0", stats_secret:$("s_stats_secret").value,
    metrics_token:$("s_metrics_token").value.trim(),
    quota_warn_pct:String(+$("s_warn_pct").value || 80),
    fw_auto:$("s_fwauto").checked?"1":"0",
    trusted_proxies:$("s_trusted_proxies").value.trim(),
    acme_enabled:$("s_acme").checked?"1":"0", acme_domain:$("s_acme_domain").value, acme_email:$("s_acme_email").value };
  try {
    await req("/api/settings", { method:"PUT", body });
    markDirty("set_card_basic", false);
    toast("设置已保存（统计开关需应用后生效）");
  }
  catch (e) { toast(e.message, "err"); }
}

/* ---- 设置页常驻操作条 ----
   设置页十几张卡片、每张各有保存按钮，滚出视野后就找不着了。
   底部这条钉在视口上，按钮跟着「当前滚到哪一块」变，左侧写明它保存的是哪一块。
   注意别用 position:sticky 挂在卡片上——卡片本身大多比视口矮，sticky 不会触发。 */
const SET_BAR_SECTIONS = [
  { id:"set_card_basic",  name:"基础设置", label:"保存设置",     run:() => saveSettings() },
  { id:"set_card_notify", name:"变更通知", label:"保存通知设置", run:() => saveNotify() },
  { id:"set_card_update", name:"组件更新", label:"保存更新设置", run:() => saveUpdateSettings() },
];
let setBarCur = null;

/* ---- 设置页分组 ----
   整页十张卡片一屏放不下：常用（基础设置 / 通知）、安全（人、密码、登录）、
   运维（证书与更新）。切换只动 [data-sect] 卡片的 hidden，不动数据。 */

/* 分组条钉在顶栏下面，top 要等于顶栏实际高度。顶栏高度随字号、窄屏换行变化，
   写死像素迟早对不齐，所以量出来塞进 --topbar-h。 */
function syncTopbarH() {
  // 方案 B 之后顶栏就是命令条本身（原 .topbar 已并入），量的对象跟着换；
  // 变量名保持 --topbar-h，设置页分组条仍靠它算吸顶位置。
  const tb = document.querySelector(".cmdbar");
  // 登录页时 #app 是 hide 的，量出来是 0——别拿 0 去覆盖，留 CSS 里的兜底值
  if (!tb || !tb.offsetHeight) return;
  document.documentElement.style.setProperty("--topbar-h", tb.offsetHeight + "px");
}
syncTopbarH();
window.addEventListener("resize", syncTopbarH);
if (window.ResizeObserver) {
  const tb = document.querySelector(".cmdbar");
  if (tb) new ResizeObserver(syncTopbarH).observe(tb);
}

/* 窄屏导航条（.side 变成横向一行）是 sticky 的，设置页的分组条要钉在它下面，
   高度同样随字号和安全区变化，一起量进 --nav-h。桌面上 .side 是整列侧栏，
   量出来的值只被窄屏媒体查询用，不影响桌面布局。 */
function syncNavH() {
  const s = document.querySelector(".side");
  if (!s || !s.offsetHeight) return;
  document.documentElement.style.setProperty("--nav-h", s.offsetHeight + "px");
}
syncNavH();
window.addEventListener("resize", syncNavH);
if (window.ResizeObserver) {
  const s = document.querySelector(".side");
  if (s) new ResizeObserver(syncNavH).observe(s);
}

const SET_SECTS = {
  common:   "内核路径、节点公开地址、统计与通知",
  security: "账号密码、二次验证、登录设备与管理员",
  maintain: "HTTPS 证书、面板自更新、内核版本管理",
};
const SECT_LABEL = { common: "常用", security: "安全", maintain: "运维" };
const SET_SECT_ORDER = ["common", "security", "maintain"];
let setSectCur = "common";   // 当前分组，角色变化后要按新权限重算一遍

/* 当前角色下，每组还剩几张能显示的卡片。
   先把卡片全显出来再量 offsetParent：.need-admin / .need-owner 是用
   display:none!important 藏父级的，卡片自身的 hidden 仍是 false，
   只有 offsetParent 能反映「真的看不到」。 */
function setSectUsable(cards) {
  const was = cards.map(el => el.hidden);
  cards.forEach(el => { el.hidden = false; });
  const usable = new Set();
  cards.forEach(el => { if (el.offsetParent !== null) usable.add(el.dataset.sect); });
  cards.forEach((el, i) => { el.hidden = was[i]; });
  return usable;
}

// persist 只在「用户自己点的」时候为真；按存储恢复、角色变化后的重算都不写，
// 否则只读账号被顶到「安全」会把 owner 存的「运维」冲掉。
function setSect(name, persist = true) {
  // 首屏 CSS 用 !important 藏了非目标组的卡片。量 offsetParent 之前必须撤掉，
  // 否则 setSectUsable() 会把「被首屏规则藏住」误当成「当前角色没权限」，
  // 于是 usable 只剩一组，分组条被收起来、权限判定也跟着错。
  document.documentElement.removeAttribute("data-boot-sect");
  const cards = [...document.querySelectorAll("#v-settings .card[data-sect]")];
  const usable = setSectUsable(cards);
  // 上次存的那组在这个角色下可能是空的（只读账号没有「常用」「运维」），
  // 退回第一个有内容的组，否则进设置页会是白板。
  let pick = SET_SECTS[name] ? name : "common";
  if (!usable.has(pick)) pick = SET_SECT_ORDER.find(s => usable.has(s)) || pick;
  setSectCur = pick;
  cards.forEach(el => { el.hidden = el.dataset.sect !== pick; });

  document.querySelectorAll("#set_sect_seg button").forEach(b => {
    const ok = usable.has(b.dataset.sect);
    const on = b.dataset.sect === pick;
    b.classList.toggle("on", on);
    b.setAttribute("aria-selected", on ? "true" : "false");
    b.disabled = !ok;
    b.title = ok ? "" : "当前角色没有可修改的项";
  });
  // 只剩一组时分组条没意义（只读账号只看得到「安全」），整条收起来
  const tabs = document.querySelector("#v-settings .set-tabs");
  if (tabs) tabs.hidden = usable.size <= 1;

  const hint = $("set_sect_hint");
  if (hint) hint.textContent = SET_SECTS[pick];
  // 只在「要的那组真的要到了」时才记住。因权限被顶到别的组时（只读账号
  // 被顶到「安全」）不写，否则会把用户自己的选择盖掉。
  if (persist && pick === name) {
    try { localStorage.setItem("triune_settings_sect", pick); } catch (e) { /* 隐私模式 */ }
  }
  setBarCur = null;   // 分组变了，常驻保存条要重新判定该显示哪一块
  updateSetBar();
}
/* 按记住的分组显示，不写回存储 */
function setSectSaved() {
  let want = "common";
  try { want = localStorage.getItem("triune_settings_sect") || "common"; } catch (e) { /* 隐私模式 */ }
  setSect(want, false);
}
const setSectSeg = $("set_sect_seg");
if (setSectSeg) setSectSeg.addEventListener("click", e => {
  const b = e.target.closest("button[data-sect]");
  if (!b) return;
  const from = setSectCur;
  setSect(b.dataset.sect, true);
  // 切走时提示一句：卡片一藏，底部的保存条也跟着没了，
  // 容易让人以为「已经存过了」。改动其实还在，切回来就能接着改。
  if (from !== b.dataset.sect && SET_DIRTY.size) {
    const left = [...SET_DIRTY].some(id => { const el = $(id); return el && el.dataset.sect === from; });
    if (left) toast(`「${SECT_LABEL[from]}」还有未保存的改动`, "warn");
  }
});

/* ---- 未保存改动标记 ----
   三张卡各有自己的保存按钮，切组会把卡片藏起来。给改过的组在标签上点个圆点，
   切回来一眼能看到「这组还有东西没存」。
   只认这三张卡：2FA 与管理员账号是即时生效的按钮，没有「未保存」这个状态。 */
const SET_SAVE_CARDS = ["set_card_basic", "set_card_notify", "set_card_update"];
const SET_DIRTY = new Set();
function markDirty(id, on) {
  if (on) SET_DIRTY.add(id); else SET_DIRTY.delete(id);
  const sects = new Set();
  for (const cid of SET_DIRTY) {
    const el = $(cid);
    if (el) sects.add(el.dataset.sect);
  }
  document.querySelectorAll("#set_sect_seg button").forEach(b => {
    b.classList.toggle("dirty", sects.has(b.dataset.sect));
  });
  setBarCur = null;   // 底部条要跟着变（未保存时补一句）
  updateSetBar();
}
// input 管文本框，change 管下拉与勾选框；两者都触发也没关系，markDirty 是幂等的
["input", "change"].forEach(ev => {
  const host = document.querySelector("#v-settings");
  if (!host) return;
  host.addEventListener(ev, e => {
    const card = e.target.closest(".card");
    if (card && SET_SAVE_CARDS.includes(card.id)) markDirty(card.id, true);
  });
});

function updateSetBar() {
  const bar = $("set_bar");
  if (!bar) return;
  const main = document.querySelector(".main");
  // 判定线取视口上沿往下 140px：卡片标题滚过这条线就算「正在看这一块」
  const line = main.getBoundingClientRect().top + 140;
  let cur = null;
  for (const s of SET_BAR_SECTIONS) {
    const el = $(s.id);
    // 看不见的卡片不参与判定：被分组隐藏的、以及被权限藏起来的，
    // rect 全是 0，会被误判成「已滚过」，结果给只读账号递了个「保存设置」。
    if (!el || !el.offsetParent) continue;
    if (el.getBoundingClientRect().top <= line) cur = s;
  }
  // 还没滚到任何「有保存按钮」的分区（比如停在「我的账号」）就收起来，
  // 免得显示一个跟眼前内容无关的保存按钮。
  bar.style.visibility = cur ? "visible" : "hidden";
  if (!cur || cur === setBarCur) return;
  setBarCur = cur;
  $("set_bar_hint").innerHTML = "当前分区：<b>" + cur.name + "</b>" +
    (SET_DIRTY.has(cur.id) ? ' <span style="color:var(--warn);font-weight:600">· 未保存</span>' : "");
  const btn = $("set_bar_btn");
  btn.textContent = cur.label;
  btn.onclick = cur.run;
}

document.querySelector(".main").addEventListener("scroll", () => {
  if (CURRENT === "settings") updateSetBar();
}, { passive: true });

/* ---- 我的账号：改自己的密码（需原密码，首次设密码时留空） ---- */
async function changeMyPassword() {
  const oldP = $("me_old").value, newP = $("me_new").value;
  if (newP.length < 8) { toast("新密码至少 8 位", "err"); return; }
  if (!(await ask("修改密码", "确认修改当前账号的密码？改完需要用新密码重新登录。"))) return;
  try {
    const r = await req("/api/me/password", { method:"PUT", body:{ old: oldP, new: newP } });
    $("me_old").value = ""; $("me_new").value = "";
    if (r && r.relogin) { toast("密码已更新，请用新密码重新登录"); logout(); return; }
  } catch (e) { toast("修改失败：" + e.message, "err"); }
}

/* ---- 管理员账号（仅超级管理员） ---- */
async function loadMe() {
  try {
    const j = await req("/api/me");
    ME = j; ROLE = j.role || "";
    localStorage.setItem("triune_role", ROLE);
    localStorage.setItem("triune_user", j.user || "");
    applyRole();
    $("me_user").value = j.user || "";
    $("me_last").value = j.last_ip ? `${j.last_ip}${j.last_login ? " · " + j.last_login : ""}` : "—";
    // 概览页展示最近一次登录（按人记录，便于发现异常访问）
    if (j.last_ip) {
      const el = $("last_login_line");
      el.textContent = `上次登录：${j.last_ip}${j.last_login ? " · " + j.last_login : ""}`;
      el.classList.remove("hide");
    }
    const badge = $("me_role_badge");
    if (badge) { badge.textContent = ROLE_LABEL[ROLE] || ROLE; badge.className = "badge " + (ROLE === "owner" ? "b-warn" : ROLE === "viewer" ? "b-idle" : "b-ok"); }
  } catch (e) { /* 401 已由 req 处理 */ }
}

function applyRole() {
  document.body.dataset.role = ROLE || "admin";
  const u = $("whoami_user"), r = $("whoami_role");
  if (u) u.textContent = ME.user || "—";
  if (r) { r.textContent = ROLE_LABEL[ROLE] || ROLE || ""; r.className = "whoami-role " + (ROLE || ""); }
  // 只读账号没有应用权限：常驻按钮与概览的两个入口都带 .need-admin，
  // 由 body[data-role="viewer"] 的规则统一藏掉（见 index.html），不在这里逐个判。
  renderNav();
  // 角色变了，可用的分组也跟着变（只读账号没有「常用」「运维」），
  // 停在设置页时得按新权限重挑一次，否则会停在刚被禁用的那组。
  if (CURRENT === "settings") setSectSaved();
}

async function loadAdmins() {
  if (!isOwner()) return;
  try {
    const list = await req("/api/admins");
    const tb = $("admbody");
    if (!list.length) { tb.innerHTML = '<tr><td colspan="5" class="muted">暂无账号</td></tr>'; return; }
    tb.innerHTML = list.map(a => {
      const roleTxt = ROLE_LABEL[a.role] || a.role;
      const self = a.username === ME.user;
      const ops = [];
      if (a.role !== "owner") {
        ops.push(`<button class="btn sm" data-act="toggleAdmin" data-args="${escJson([a.id,!a.enabled])}">${a.enabled ? "停用" : "启用"}</button>`);
        ops.push(`<button class="btn sm" data-act="resetAdminPassword" data-args="${escJson([a.id,a.username])}">重置密码</button>`);
        ops.push(`<button class="btn sm danger" data-act="deleteAdmin" data-args="${escJson([a.id,a.username])}">删除</button>`);
      } else {
        ops.push('<span class="muted" style="font-size:12px">受保护</span>');
      }
      return `<tr>
        <td><b>${escHtml(a.username)}</b>${self ? ' <span class="muted" style="font-size:11px">（我）</span>' : ""}${a.has_totp ? ' <span class="badge b-ok" style="font-size:10px">TOTP</span>' : ""}</td>
        <td>${roleTxt}</td>
        <td>${a.enabled ? '<span class="badge b-ok">启用</span>' : '<span class="badge b-idle">停用</span>'}</td>
        <td class="muted" style="font-size:12px">${a.last_login ? escHtml(a.last_ip) + " · " + escHtml(a.last_login) : "—"}</td>
        <td class="ta-r">${ops.join(" ")}</td></tr>`;
    }).join("");
  } catch (e) { toast(e.message, "err"); }
}

async function createAdmin() {
  const username = $("adm_user").value.trim();
  const role = $("adm_role").value;
  const password = $("adm_pass").value;
  if (!username) { toast("请填写用户名", "err"); return; }
  try {
    const j = await req("/api/admins", { method:"POST", body:{ username, role, password } });
    $("adm_user").value = ""; $("adm_pass").value = "";
    if (j.password) {
      // 生成的密码只出现这一次，必须让用户有机会抄下来
      openModal("mAdminPass");
      $("ap_user").textContent = username;
      $("ap_pass").textContent = j.password;
    } else {
      toast("已新建管理员 " + username);
    }
    loadAdmins();
  } catch (e) { toast(e.message, "err"); }
}

async function toggleAdmin(id, enabled) {
  try {
    await req("/api/admins/" + id, { method:"PUT", body:{ enabled } });
    toast(enabled ? "已启用" : "已停用（该账号在线会话立即失效）");
    loadAdmins();
  } catch (e) { toast(e.message, "err"); }
}

async function resetAdminPassword(id, name) {
  const p = prompt(`为 ${name} 设置新密码（至少 8 位，留空自动生成）：`);
  if (p === null) return;
  const body = p ? { password: p } : { password: "" };
  try {
    if (!p) {
      // 留空 = 服务端不接受空密码，这里自己生成一个再提交，避免「重置成空」
      const gen = Array.from(crypto.getRandomValues(new Uint8Array(16))).map(b => "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"[b % 56]).join("");
      body.password = gen;
      await req("/api/admins/" + id, { method:"PUT", body });
      openModal("mAdminPass"); $("ap_user").textContent = name; $("ap_pass").textContent = gen;
    } else {
      if (p.length < 8) { toast("密码至少 8 位", "err"); return; }
      await req("/api/admins/" + id, { method:"PUT", body });
      toast("密码已重置（该账号旧会话立即失效）");
    }
    loadAdmins();
  } catch (e) { toast(e.message, "err"); }
}

async function deleteAdmin(id, name) {
  if (!(await ask("删除管理员", `确认删除 ${name}？该账号的在线会话会立即失效，操作会记入审计。`))) return;
  try {
    await req("/api/admins/" + id, { method:"DELETE" });
    toast("已删除 " + name);
    loadAdmins();
  } catch (e) { toast(e.message, "err"); }
}

/* ---- 登录设备：会话列表与精准吊销 ----
   服务端只回 sid（token 的哈希前缀），前端拿不到也不需要真凭据；
   账号名/IP 这些不放进内联 onclick 的字符串里（escHtml 不转义引号，
   设备名里带个单引号就能把属性撑破），统一用 SESS 缓存按 id 回查。 */
let SESS = [];
async function loadSessions() {
  const tb = $("sessbody");
  if (!tb) return;
  try {
    const list = await req("/api/sessions");
    SESS = Array.isArray(list) ? list : [];
    $("sess_count").textContent = SESS.length ? SESS.length + " 个在线会话" : "";
    if (!SESS.length) {
      tb.innerHTML = '<tr><td colspan="6" class="muted">暂无在线会话</td></tr>';
      return;
    }
    tb.innerHTML = SESS.map(s => {
      const roleTxt = ROLE_LABEL[s.role] || s.role || "—";
      let ops;
      if (s.current) ops = '<span class="badge b-ok">本机</span>';
      else if (s.revocable) ops = `<button class="btn sm danger" data-act="revokeSession" data-args="${escJson([s.id])}">踢下线</button>`;
      else ops = '<span class="muted" style="font-size:12px" title="只能吊销自己或权限更低的会话">不可吊销</span>';
      // UA 全文放 title：设备名是压缩过的，排查异常登录时原文有用
      return `<tr>
        <td title="${escHtml(s.ua || "")}"><b>${escHtml(s.device)}</b>${s.current ? ' <span class="muted" style="font-size:11px">（当前）</span>' : ""}</td>
        <td>${escHtml(s.user)} <span class="muted" style="font-size:11px">${roleTxt}</span></td>
        <td class="mono" style="font-size:12px">${escHtml(s.ip || "—")}</td>
        <td class="muted" style="font-size:12px">${escHtml(s.created || "—")}</td>
        <td class="muted" style="font-size:12px">${escHtml(s.last_seen || "—")}</td>
        <td class="ta-r">${ops}</td></tr>`;
    }).join("");
    const mine = SESS.filter(s => s.user === (ME.user || "") && !s.current).length;
    $("sess_msg").textContent = mine
      ? `你还有 ${mine} 台设备在线，可一键退出。`
      : "被踢的设备下一次请求就会掉回登录页。";
  } catch (e) {
    tb.innerHTML = `<tr><td colspan="6" class="muted">${escHtml(e.message)}</td></tr>`;
  }
}

async function revokeSession(sid) {
  const s = SESS.find(x => x.id === sid);
  const label = s ? `${s.user} · ${s.device} · ${s.ip}` : "该会话";
  if (!(await ask("吊销会话", `确认把「${label}」踢下线？该设备下一次请求会掉回登录页，需要重新输入密码。`))) return;
  try {
    await req("/api/sessions/" + encodeURIComponent(sid), { method: "DELETE" });
    toast("已吊销该会话");
    loadSessions();
  } catch (e) { toast("吊销失败：" + e.message, "err"); }
}

async function revokeOtherDevices() {
  const mine = SESS.filter(s => s.user === (ME.user || "") && !s.current).length;
  if (!mine) { toast("没有其他在线设备", "info"); return; }
  if (!(await ask("退出其他设备", `确认吊销你账号在另外 ${mine} 台设备上的会话？本机保持登录。`))) return;
  try {
    const j = await req("/api/sessions", { method: "DELETE" });
    toast(`已退出 ${(j && j.revoked) || 0} 台设备`);
    loadSessions();
  } catch (e) { toast("操作失败：" + e.message, "err"); }
}

/* ---- 登录历史：审计里的 login 记录按人聚合 ---- */
let LH = [];
async function loadLoginHistory() {
  const tb = $("lhbody");
  if (!tb) return;
  try {
    const days = $("lh_days") ? $("lh_days").value : 7;
    const j = await req("/api/login-history?days=" + days);
    LH = j.items || [];
    $("lh_msg").textContent = j.truncated
      ? `只扫描了最近 ${j.scanned} 条登录记录，更早的没统计进来（缩小时间范围会更准）。`
      : (j.scanned ? `该时间段内共 ${j.scanned} 条登录记录。` : "该时间段内没有登录记录。");
    if (!LH.length) {
      tb.innerHTML = '<tr><td colspan="6" class="muted">暂无登录记录</td></tr>';
      return;
    }
    tb.innerHTML = LH.map((h, i) => {
      const fail = h.fail ? `<span class="badge b-warn">${h.fail}</span>` : '<span class="muted">0</span>';
      const ips = (h.ips || []).length
        ? h.ips.map(ip => `<span class="mono" style="font-size:12px">${escHtml(ip)}</span>`).join(", ")
        : '<span class="muted">—</span>';
      // 账号名可能是任意「被尝试的用户名」（有人在猜 admin"），不能拼进内联 JS 字符串，
      // 所以传下标，函数里再回查
      return `<tr>
        <td><b>${escHtml(h.user)}</b>${h.user === (ME.user || "") ? ' <span class="muted" style="font-size:11px">（我）</span>' : ""}</td>
        <td><span class="badge b-ok">${h.ok}</span> ${fail}</td>
        <td>${ips}</td>
        <td class="muted" style="font-size:12px">${escHtml(h.last_ok || "—")}</td>
        <td class="muted" style="font-size:12px">${escHtml(h.last_fail || "—")}</td>
        <td class="ta-r"><button class="btn sm" data-act="showLoginTimeline" data-args="${escJson([i])}">时间线</button></td></tr>`;
    }).join("");
  } catch (e) {
    tb.innerHTML = `<tr><td colspan="6" class="muted">${escHtml(e.message)}</td></tr>`;
  }
}
function showLoginTimeline(idx) {
  const h = LH[idx];
  if (!h) return;
  const lines = (h.events || []).map(e =>
    `${e.time}  ${e.result === "ok" ? "✓ 成功" : "✕ 失败"}  ${e.ip || "-"}  ${e.detail || ""}`);
  viewContent(`登录时间线 · ${h.user}`, lines.length ? lines.join("\n") : "暂无记录");
}

/* ================= 审计日志 ================= */
const AU_LABEL = {
  "login":"登录","logout":"登出",
  "settings.password":"修改密码","settings.update":"修改设置",
  "inbound.create":"新增入站","inbound.update":"修改入站","inbound.delete":"删除入站",
  "client.create":"新增用户","client.update":"修改用户","client.delete":"删除用户",
  "client.reset-token":"重置订阅令牌",
  "apply":"应用配置","apply.dry-run":"预检","rollback":"回滚配置","switch-core":"切换内核",
  "import":"导入配置","external":"外部节点","auth2fa":"二次验证",
  "cert.issue":"证书签发","update":"版本更新","restore":"数据恢复","audit.clear":"清空审计",
  "admin":"管理员账号","rbac.deny":"越权拦截",
  "session":"登录设备","session.revoke":"吊销会话"
};
// 未登记的动作名退化为前缀标签（如 foo.bar → foo 的标签），不至于显示成一串英文
function auLabel(a) {
  if (AU_LABEL[a]) return AU_LABEL[a];
  const p = String(a || "").split(".")[0];
  return AU_LABEL[p] || a || "—";
}
let auOffset = 0;
async function loadAudit(more) {
  const body = $("autbody");
  if (!more) { auOffset = 0; body.innerHTML = `<tr><td colspan="7" class="muted">加载中…</td></tr>`; }
  try {
    const q = new URLSearchParams({ limit: 100, offset: auOffset });
    if ($("au_prefix").value) q.set("prefix", $("au_prefix").value);
    if ($("au_result").value) q.set("result", $("au_result").value);
    const j = await req("/api/audit?" + q.toString());
    const rows = j.map(e => {
      const badge = e.result === "ok"
        ? `<span class="badge b-ok">成功</span>`
        : `<span class="badge b-err">失败</span>`;
      return `<tr>
        <td class="muted" style="white-space:nowrap">${escHtml(e.time)}</td>
        <td>${e.actor ? escHtml(e.actor) : '<span class="muted">—</span>'}</td>
        <td>${escHtml(auLabel(e.action))}<div class="muted" style="font-size:11px">${escHtml(e.action)}</div></td>
        <td>${e.target ? escHtml(e.target) : '<span class="muted">—</span>'}</td>
        <td class="muted">${e.ip ? escHtml(e.ip) : "—"}</td>
        <td>${badge}</td>
        <td class="muted">${escHtml(e.detail)}</td></tr>`;
    }).join("");
    if (!more) body.innerHTML = rows || `<tr><td colspan="7" class="muted">暂无记录</td></tr>`;
    else body.insertAdjacentHTML("beforeend", rows);
    auOffset += j.length;
    $("au_more").classList.toggle("hide", j.length < 100);
    $("au_msg").textContent = auOffset ? `已显示 ${auOffset} 条` : "";
  } catch(e) {
    body.innerHTML = `<tr><td colspan="7" style="color:var(--err)">加载失败：${escHtml(e.message)}</td></tr>`;
  }
}
async function clearAudit() {
  if (!confirm("确定清空全部审计记录？\n\n清空动作本身会留下一条记录，说明是谁在什么时候清的。")) return;
  try {
    await req("/api/audit", { method: "DELETE" });
    toast("审计记录已清空");
    loadAudit();
  } catch(e) { toast(e.message, "err"); }
}

/* ================= PWA：注册 service worker =================
   service worker 只在安全上下文里可用（HTTPS，或 localhost/127.0.0.1）。
   面板经常直接挂在 http://<公网 IP> 上，那种情况下 register() 必然抛
   SecurityError——先判断再注册，别在控制台留一堆红字。

   SW 本身不做离线缓存（面板每屏都是要登录的实时数据），它只负责让浏览器
   认可「可安装」，以及断网时给一页说明。详见 internal/webui/pwa/sw.js。 */
(function registerSW() {
  if (!("serviceWorker" in navigator)) return;
  const secure = location.protocol === "https:" ||
    /^(localhost|127\.0\.0\.1|\[::1\])$/.test(location.hostname);
  if (!secure) return;
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js", { scope: "/" }).catch(() => {});
  });
})();

/* ================= 启动 ================= */
if (DEMO) {
  // 演示模式：直接进入主界面，无需登录（演示的是超级管理员视角）
  ROLE = "owner"; ME = { user: "admin", role: "owner" };
  $("forgot_hint").classList.add("hide");
  applyRole();
  showApp();
} else if (TOKEN) {
  /* 首屏已经由 boot.js 画成主界面，这里**不再等** /api/status 的往返才决定显隐：
     立刻套上本地角色（只读账号不该闪一下管理员按钮）并开始拉数据。 */
  applyRole();
  showApp();
  /* 会话可能在别的设备上被吊销、或面板重启后 token 失效：后台复核一次，
     只有明确的 401 才回登录页。网络暂时不通（面板正在重启、断网重连）不踢人，
     否则一次抖动就变成「刷新一下要重新登录」。 */
  fetch("/api/status", { headers:{ Authorization:"Bearer "+TOKEN } })
    .then(r => { if (r.status === 401) { dropSession(); showLogin(); } })
    .catch(() => {});
} else showLogin();
