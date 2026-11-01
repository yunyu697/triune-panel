/* Triune Panel — service worker
 *
 * 这里**刻意不做任何缓存**，这在 service worker 里很少见，值得解释：
 *
 *   面板每一屏都是登录后才能拿的实时数据（流量、在线设备、节点状态），
 *   缓存下来第二天就是错的。更麻烦的是缓存住 index.html 之后，升级面板
 *   会出现「旧界面调新接口」——字段对不上、接口 404，而且用户完全不知道
 *   自己拿到的是旧页面。这类问题排查成本远高于它省下的那点流量。
 *
 * 它存在的理由只有两条：
 *   1. Chrome / Edge 要求站点有 service worker 才允许「安装到主屏」；
 *   2. 断网时给一页能看懂的说明，而不是浏览器的 ERR_INTERNET_DISCONNECTED。
 *
 * 所以除了页面导航（用于兜底离线页），其余请求一律不接管：
 * 浏览器自带的 HTTP 缓存 + 服务端下发的 no-cache/ETag 已经够用，
 * 再套一层只会制造「到底是谁在缓存」的糊涂账。
 */

self.addEventListener("install", () => {
  // 新版直接接管，不等旧标签页关闭：这里没有离线数据要迁移，
  // 让用户继续跑旧代码没有任何好处。
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    // 万一以后有人加了缓存，或者旧版本留了缓存，这里一次性清干净
    const keys = await caches.keys();
    await Promise.all(keys.map((k) => caches.delete(k)));
    await self.clients.claim();
  })());
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  // 写请求（登录、改配置、下单）一律不插手
  if (req.method !== "GET") return;

  const url = new URL(req.url);
  // 跨源请求没有我们的东西；带凭据的接口更不能经过 SW
  if (url.origin !== self.location.origin) return;
  if (/^\/(api|sub)\//.test(url.pathname)) return;

  // 只有「打开页面」这一种请求需要兜底：断网时给一页说明
  if (req.mode === "navigate") {
    event.respondWith(fetch(req).catch(() => offlinePage()));
  }
  // 其余（图标、manifest、sw 自己）不调用 respondWith，原样走网络。
});

// offlinePage 断网兜底页。
// 纯静态、不依赖任何接口，也不用内联脚本（重试就是普通链接），
// 这样即使哪天 CSP 收紧到禁止内联执行，这一页也不会坏。
function offlinePage() {
  const html = [
    '<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">',
    '<meta name="viewport" content="width=device-width,initial-scale=1">',
    '<title>已离线 · Triune Panel</title><style>',
    ':root{color-scheme:light dark}',
    'html,body{height:100%;margin:0}',
    'body{display:flex;align-items:center;justify-content:center;padding:24px;',
    'font:-apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei",sans-serif;',
    'background:#f4f6fa;color:#141a22}',
    '.box{max-width:360px;text-align:center}',
    '.ico{width:56px;height:56px;margin:0 auto 18px;border-radius:17px;',
    'background:linear-gradient(135deg,#3b82f6,#8b5cf6);display:flex;',
    'align-items:center;justify-content:center}',
    '.ico svg{width:26px;height:26px}',
    'h1{font-size:17px;margin:0 0 8px}',
    'p{font-size:14px;line-height:1.7;margin:0 0 22px;color:#68727f}',
    'a{display:inline-block;padding:10px 22px;border-radius:10px;font-size:14px;',
    'font-weight:600;text-decoration:none;color:#fff;',
    'background:linear-gradient(135deg,#3b82f6,#8b5cf6)}',
    '@media (prefers-color-scheme:dark){',
    'body{background:#0d1016;color:#e6eaf0}p{color:#8b94a3}}',
    '</style></head><body><div class="box">',
    '<div class="ico"><svg viewBox="0 0 24 24" fill="none" stroke="#fff" ',
    'stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">',
    '<path d="M12 2 3 7v6c0 5 4 8 9 9 5-1 9-4 9-9V7z"/></svg></div>',
    "<h1>连不上面板</h1>",
    "<p>这台设备现在是离线的，或者面板所在的主机暂时不可达。<br>",
    "面板显示的都是实时数据，所以这里不做离线查看。</p>",
    '<a href="/">重新加载</a>',
    "</div></body></html>"
  ].join("");
  return new Response(html, {
    status: 503,
    headers: { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" }
  });
}
