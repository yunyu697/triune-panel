/* 真后端首屏回归（建议 1）。
 *
 * 之前所有首屏回归都跑在 file://demo.html 上：没有 CSP、没有 HTTP 往返、
 * 没有 /api/status 的延迟 / 401 / 网络错误。这条用真的 `tp serve` 起后端，
 * 覆盖三件离线页测不到的事：
 *   1. CSP 是 script-src 'self'，boot.js 必须真从 /assets/boot.js 下发；
 *   2. 首屏显隐不能等 /api/status——把它延迟 800ms，界面也不许回登录页；
 *   3. 明确的 401 要回登录页并清干净本地会话，网络错误则不许踢人。
 *
 * 探针用同源外部脚本注入（不是内联 <script>）：真后端的 CSP 会拦掉内联脚本，
 * 那样测的就不是线上那份页面了。探针插在 app.js 之前，读到的是 app.js
 * 接手显隐之前的真实 computedStyle。
 *
 * 用法：node .e2e/first-paint-server.js
 *   E2E_TP_BIN=...  复用已编译的二进制（CI 里先 go build 再传进来）
 *   E2E_CHANNEL=bundled  CI 用 Playwright 自带 Chromium
 */
const fs = require("fs");
const os = require("os");
const net = require("net");
const path = require("path");
const { spawn, execFileSync } = require("child_process");
const { launch } = require("./browser");

const ROOT = path.resolve(__dirname, "..");
const PASS = "E2e-Passw0rd-2026";
const PROBE_PATH = "/assets/__fp_probe.js";
const APP_TAG = '<script src="/assets/app.js"></script>';

let pass = 0, fail = 0;
/* 模块级：seed() 与 IIFE 里的路由闭包必须读写同一个变量。放在 IIFE 内的话，
   seed() 里的赋值会变成隐式全局，路由永远看到 "pass"，全部场景静默假绿。 */
let statusMode = "pass";
const statusCalls = { delay: 0, "401": 0, "network-error": 0, pass: 0 };
const ck = (n, want, got) => {
  const ok = String(want) === String(got);
  ok ? pass++ : fail++;
  console.log(`${ok ? "PASS" : "FAIL"} ${n}${ok ? "" : `  期望=[${want}] 实际=[${got}]`}`);
};
const transparent = c => c === "rgba(0, 0, 0, 0)" || c === "transparent";

/* 探针在 app.js 之前同步执行；另外 15ms 后补采一次，覆盖「app.js 已经撤掉
   data-boot* 但分组恢复仍排在 30ms 定时器里」这类中间帧。 */
const PROBE = `
window.__snap = function () {
  var h = document.documentElement;
  var g = function (id) { return document.getElementById(id); };
  var d = function (id) { var e = g(id); return e ? getComputedStyle(e).display : "missing"; };
  var card = function (s) {
    var e = document.querySelector('#v-settings .card[data-sect="' + s + '"]');
    return e ? getComputedStyle(e).display : "missing";
  };
  var btn = function (s) {
    var e = document.querySelector('#set_sect_seg button[data-sect="' + s + '"]');
    return e ? getComputedStyle(e).backgroundColor : "missing";
  };
  return {
    boot: h.getAttribute("data-boot"), view: h.getAttribute("data-boot-view"),
    sect: h.getAttribute("data-boot-sect"),
    mode: h.getAttribute("data-mode"), scheme: h.getAttribute("data-scheme"),
    login: d("login"), app: d("app"), overview: d("v-overview"), settings: d("v-settings"),
    commonCard: card("common"), maintainCard: card("maintain"),
    commonBtn: btn("common"), maintainBtn: btn("maintain"),
    bg: getComputedStyle(document.body).backgroundColor,
    probeTag: !!document.querySelector('script[src="${PROBE_PATH}"]'),
    switching: h.hasAttribute("data-theme-switch")
  };
};
window.__firstFrame = window.__snap();
setTimeout(function () { window.__at15ms = window.__snap(); }, 15);
`;

function freePort() {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.on("error", reject);
    s.listen(0, "127.0.0.1", () => {
      const p = s.address().port;
      s.close(() => resolve(p));
    });
  });
}

async function waitReady(url, ms = 20000) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) {
    try {
      const r = await fetch(url);
      if (r.ok) return;
    } catch (_) { /* 还没监听 */ }
    await new Promise(r => setTimeout(r, 100));
  }
  throw new Error("后端 " + ms + "ms 内没起来");
}

function api(pathname, opts = {}) {
  return fetch(BASE + pathname, opts).then(async r => ({ status: r.status, json: await r.json().catch(() => ({})) }));
}

let BASE = "";
let server = null;
let dataDir = "";
let builtBin = "";

async function getToken() {
  const hdr = { "Content-Type": "application/json" };
  let r = await api("/api/login", {
    method: "POST", headers: hdr,
    body: JSON.stringify({ user: "admin", pass: "admin" })
  });
  if (r.status !== 200 || !r.json.token) {
    // 数据目录复用过：直接用改过的密码登录
    r = await api("/api/login", {
      method: "POST", headers: hdr,
      body: JSON.stringify({ user: "admin", pass: PASS })
    });
    if (r.status === 200 && r.json.token) return r.json.token;
    throw new Error("拿不到登录 token: " + JSON.stringify(r));
  }
  if (!r.json.must_change) return r.json.token;
  const t = r.json.token;
  await api("/api/me/password", {
    method: "PUT", headers: Object.assign({ Authorization: "Bearer " + t }, hdr),
    body: JSON.stringify({ old: "admin", new: PASS })
  });
  r = await api("/api/login", {
    method: "POST", headers: hdr,
    body: JSON.stringify({ user: "admin", pass: PASS })
  });
  if (!r.json.token) throw new Error("改密后重新登录失败: " + JSON.stringify(r));
  return r.json.token;
}

async function setup() {
  let bin = process.env.E2E_TP_BIN || "";
  if (!bin) {
    const localGo = path.join(os.homedir(), "sdk", "go", "bin", "go");
    const go = process.env.E2E_GO || (fs.existsSync(localGo) ? localGo : "go");
    builtBin = path.join(__dirname, "tp-first-paint");
    execFileSync(go, ["build", "-o", builtBin, "./cmd/server"], { cwd: ROOT, stdio: "inherit" });
    bin = builtBin;
  }
  const port = await freePort();
  BASE = "http://127.0.0.1:" + port;
  dataDir = fs.mkdtempSync(path.join(__dirname, "fp-data-"));
  server = spawn(bin, ["serve", "-addr", "127.0.0.1:" + port, "-data", dataDir],
    { cwd: ROOT, stdio: ["ignore", "pipe", "pipe"] });
  let log = "";
  server.stdout.on("data", d => { log += d.toString(); });
  server.stderr.on("data", d => { log += d.toString(); });
  try {
    await waitReady(BASE + "/");
  } catch (e) {
    throw new Error(String(e) + "\n--- 后端日志 ---\n" + log.slice(-2000));
  }
}

function cleanup() {
  if (server) { try { server.kill("SIGTERM"); } catch (_) {} }
  if (dataDir) { try { fs.rmSync(dataDir, { recursive: true, force: true }); } catch (_) {} }
  if (builtBin) { try { fs.rmSync(builtBin, { force: true }); } catch (_) {} }
}

const PREFS = token => ({
  triune_token: token, triune_role: "owner", triune_user: "admin",
  triune_view: "settings", triune_settings_sect: "maintain",
  triune_mode: "dark", triune_scheme: "forest"
});

/* 每个场景都先落一次地（写好 localStorage）再 reload，探针采到的才是真正
   带偏好的那一次首屏。落地那次一律让 /api/status 正常返回：否则上一场遗留的
   token 会在落地导航里吃到 401，而那个 401 的回包可能晚于 evaluate 才到，
   把刚写进去的 token 又清掉——下一场就会假红成「首帧是登录页」。
   目标场景的 status 行为只在 reload 之前切换。 */
async function seed(page, prefs, mode) {
  statusMode = "pass";
  await page.goto(BASE + "/", { waitUntil: "domcontentloaded" });
  await page.evaluate(p => {
    for (const [k, v] of Object.entries(p)) localStorage.setItem(k, v);
  }, prefs);
  statusMode = mode;
}

function checkFirstFrame(label, s, finalBg) {
  console.log("\n  " + label + " 首帧:", JSON.stringify(s));
  ck(label + "：首帧就是主界面（不等 status）", "app", s.boot);
  ck(label + "：首帧停在上次的设置页", "settings", s.view);
  ck(label + "：首帧分组就是运维", "maintain", s.sect);
  ck(label + "：首帧主题 dark/forest", "dark/forest", s.mode + "/" + s.scheme);
  ck(label + "：首帧背景色与最终态一致（无渐变）", finalBg, s.bg);
  ck(label + "：首帧隐藏登录页", "none", s.login);
  ck(label + "：首帧显示主界面外壳", "flex", s.app);
  ck(label + "：首帧不画概览", "none", s.overview);
  ck(label + "：首帧画设置页", "block", s.settings);
  ck(label + "：首帧隐藏常用卡片", "none", s.commonCard);
  ck(label + "：首帧显示运维卡片", "block", s.maintainCard);
  ck(label + "：首帧运维按钮高亮", "true", String(!transparent(s.maintainBtn)));
  ck(label + "：首帧常用按钮不高亮", "true", String(transparent(s.commonBtn)));
  ck(label + "：首帧没有残留 data-theme-switch", "false", String(s.switching));
  ck(label + "：探针是同源外部脚本（CSP 未拦）", "true", String(s.probeTag));
}

(async () => {
  await setup();
  const bootAsset = await fetch(BASE + "/assets/boot.js");
  const bootText = await bootAsset.text();
  console.log("后端已就绪:", BASE);
  ck("/assets/boot.js 由真后端下发", "200", String(bootAsset.status));
  ck("/assets/boot.js 是最新版本（含分组白名单）", "true", String(bootText.includes("BOOT_SECTS")));

  const token = await getToken();
  ck("真后端登录拿到 token", "true", String(token.length > 20));

  const browser = await launch();
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 }, serviceWorkers: "block"
  });
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));

  // 文档：把探针作为同源外部脚本插到 app.js 之前，保留原响应头（含 CSP）。
  await page.route(u => new URL(u).pathname === "/", async route => {
    const resp = await route.fetch();
    let body = await resp.text();
    if (!body.includes(APP_TAG)) throw new Error("真后端返回的 HTML 里找不到 app.js 引用");
    body = body.replace(APP_TAG, `<script src="${PROBE_PATH}"></script>\n` + APP_TAG);
    const headers = Object.assign({}, resp.headers());
    delete headers["content-length"];
    delete headers["content-encoding"];
    await route.fulfill({ response: resp, body, headers });
  });
  await page.route(u => new URL(u).pathname === PROBE_PATH, route =>
    route.fulfill({ status: 200, contentType: "application/javascript", body: PROBE }));
  await page.route(u => new URL(u).pathname === "/api/status", async route => {
    statusCalls[statusMode]++;
    if (statusMode === "delay") {
      await new Promise(r => setTimeout(r, 800));
      await route.continue();
    } else if (statusMode === "401") {
      await route.fulfill({ status: 401, contentType: "application/json",
        body: JSON.stringify({ error: "unauthorized" }) });
    } else if (statusMode === "network-error") {
      await route.abort("failed");
    } else {
      await route.continue();
    }
  });

  const finalBg = await (async () => {
    await seed(page, PREFS(token), "pass");
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.waitForTimeout(250);
    return page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  })();

  // ---- 场景 1：/api/status 延迟 800ms ----
  await seed(page, PREFS(token), "delay");
  await page.reload({ waitUntil: "domcontentloaded" });
  let r = await page.evaluate(() => ({ first: window.__firstFrame, mid: window.__at15ms, now: window.__snap() }));
  checkFirstFrame("status 延迟 800ms", r.first, finalBg);
  ck("延迟场景：+15ms 仍未画登录页", "none", r.mid.login);
  await page.waitForTimeout(70);
  let later = await page.evaluate(() => window.__snap());
  ck("延迟场景：status 回来之前仍在主界面", "none", later.login);
  ck("延迟场景：status 回来之前分组没跳回常用", "none", later.commonCard);
  await page.waitForTimeout(900);
  later = await page.evaluate(() => window.__snap());
  ck("延迟场景：status 最终 200 后仍在主界面", "none", later.login);
  ck("延迟场景：status 回来后主题没跳变", finalBg, later.bg);

  // ---- 场景 2：/api/status 明确 401 ----
  await seed(page, PREFS(token), "401");
  await page.reload({ waitUntil: "domcontentloaded" });
  r = await page.evaluate(() => ({ first: window.__firstFrame }));
  ck("401 场景：首帧仍先按本地会话画主界面", "none", r.first.login);
  await page.waitForTimeout(300);
  const after401 = await page.evaluate(() => ({
    login: getComputedStyle(document.getElementById("login")).display,
    app: getComputedStyle(document.getElementById("app")).display,
    token: localStorage.getItem("triune_token"),
    role: localStorage.getItem("triune_role")
  }));
  ck("401 场景：明确 401 后回登录页", "flex", after401.login);
  ck("401 场景：主界面收起", "none", after401.app);
  ck("401 场景：本地 token 被清掉", "null", String(after401.token));
  ck("401 场景：本地角色被清掉", "null", String(after401.role));
  // 再刷一次必须直接是登录页，不能「先画主界面再被踢回」
  await page.reload({ waitUntil: "domcontentloaded" });
  const relog = await page.evaluate(() => ({
    boot: window.__firstFrame.boot, login: window.__firstFrame.login,
    app: window.__firstFrame.app
  }));
  ck("401 后刷新：首帧就是登录页", "guest", relog.boot);
  ck("401 后刷新：首帧不再显示主界面壳", "none", relog.app);
  ck("401 后刷新：登录页可见", "flex", relog.login);

  // ---- 场景 3：/api/status 网络错误 ----
  await seed(page, PREFS(token), "network-error");
  await page.reload({ waitUntil: "domcontentloaded" });
  r = await page.evaluate(() => ({ first: window.__firstFrame }));
  checkFirstFrame("status 网络错误", r.first, finalBg);
  await page.waitForTimeout(300);
  const afterErr = await page.evaluate(() => window.__snap());
  ck("网络错误：不许把人踢回登录页", "none", afterErr.login);
  ck("网络错误：分组仍停在运维", "none", afterErr.commonCard);
  ck("网络错误：主界面仍可见", "flex", afterErr.app);

  // ---- 场景 4：status 正常 200 ----
  await seed(page, PREFS(token), "pass");
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForTimeout(250);
  const ok = await page.evaluate(() => window.__snap());
  ck("正常 200：主界面正常显示", "flex", ok.app);
  ck("正常 200：分组由 JS 接手（标记已撤）", "null", String(ok.sect));
  ck("正常 200：运维卡片仍显示", "block", ok.maintainCard);
  ck("正常 200：常用卡片仍隐藏", "none", ok.commonCard);
  // 自证三种异常真的被路由拦到过：模式变量写错作用域时，所有场景都会
  // 静默走 "pass"，界面断言照样全绿 —— 这个坑实际踩过一次。
  ck("延迟场景真的拦到过 /api/status", "true", String(statusCalls.delay > 0));
  ck("401 场景真的拦到过 /api/status", "true", String(statusCalls["401"] > 0));
  ck("网络错误场景真的拦到过 /api/status", "true", String(statusCalls["network-error"] > 0));
  console.log("  /api/status 拦截计数:", JSON.stringify(statusCalls));
  ck("页面无 JS 异常", "", errors.join(" | "));

  await browser.close();
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); }).finally(cleanup);
