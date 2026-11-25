/* Android 兼容性验证。真机内核（微信 X5 / Chromium 86）装不到这里，
   所以分两条路验证：
   1) JS 分支：覆盖 CSS.supports 让它认为不支持 dvh，看 --app-h 有没有被写对；
   2) CSS 分支：注入等价于「@supports not 命中」的规则，看 .shell 是否真的取 --app-h。
   顺带检查 text-size-adjust、移动端顶栏去毛玻璃、登录页装饰动画是否已停。 */
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

  // ---- 1. 现代内核：走 dvh，--app-h 不该被写 ----
  {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
    await page.goto(URL);
    await page.waitForTimeout(700);
    const r = await page.evaluate(() => ({
      appH: document.documentElement.style.getPropertyValue("--app-h"),
      shell: Math.round(document.querySelector(".shell").getBoundingClientRect().height),
      vh: innerHeight,
      adjust: getComputedStyle(document.documentElement).webkitTextSizeAdjust,
      topbarFilter: getComputedStyle(document.querySelector(".cmdbar")).backdropFilter,
      topbarBg: getComputedStyle(document.querySelector(".cmdbar")).backgroundColor,
      orbAnim: getComputedStyle(document.getElementById("login"), "::before").animationName,
    }));
    console.log("\n-- 现代内核 390x844 --");
    ck("支持 dvh 时不写 --app-h（走 CSS 的 dvh）", "", r.appH);
    // 窄屏改成文档滚动之后 .shell 是「height:auto + min-height:0」，
    // 高度随内容撑开（见 android-scroll.js 那组断言）。这里只保证它至少
    // 铺满一屏，不出现「内容只有半屏、下面一片空」。
    ck(".shell 至少铺满视口（窄屏随内容撑开）", true, r.shell >= r.vh - 1);
    ck("text-size-adjust 已显式设为 100%", "100%", r.adjust);
    ck("移动端顶栏关掉毛玻璃", "none", r.topbarFilter);
    // 判「不透明」而不是判某个色值：写死 rgb() 会在换主题时误报，
    // 而这条要防的是「顶栏用了半透明底 + 毛玻璃，滚动时内容透出来发花」。
    ck("移动端顶栏用不透明底色（不是 rgba）", "rgb(", r.topbarBg.slice(0, 4));
    ck("登录页装饰圆在手机上停止动画", "none", r.orbAnim);
    await page.close();
  }

  // ---- 2. 老内核 JS 分支：CSS.supports 认为不支持 dvh ----
  {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
    await page.addInitScript(() => {
      const orig = CSS.supports.bind(CSS);
      CSS.supports = (a, b) => (a === "height" && String(b).includes("dvh")) ? false : orig(a, b);
    });
    await page.goto(URL);
    await page.waitForTimeout(700);
    const r = await page.evaluate(() => ({
      appH: document.documentElement.style.getPropertyValue("--app-h"),
      vv: Math.round(visualViewport.height),
    }));
    console.log("\n-- 模拟老内核（不支持 dvh）--");
    ck("检测到不支持 dvh 后写入 --app-h", true, /^\d+px$/.test(r.appH));
    ck("--app-h 等于实际可视高度", r.vv + "px", r.appH);

    // 再注入等价于 @supports not 命中的规则，确认 CSS 侧真的取这个变量
    await page.addStyleTag({ content: "@media all{.shell{height:var(--app-h,100vh)}}" });
    const after = await page.evaluate(() => ({
      shell: Math.round(document.querySelector(".shell").getBoundingClientRect().height),
      main: Math.round(document.querySelector(".main").getBoundingClientRect().height),
      appH: parseInt(document.documentElement.style.getPropertyValue("--app-h"), 10),
    }));
    ck("老内核分支下 .shell 取 --app-h", after.appH, after.shell);
    // 有了确定高度就该把内容装住，而不是留一条被地址栏盖住的缝；
    // 现在是文档滚动，内容比 --app-h 长也算正常，所以判「不溢出外壳」。
    ck("内容装在外壳里（不再被地址栏盖住）", true, after.main <= after.shell + 1);
    await page.close();
  }

  // ---- 3. 不支持 backdrop-filter 时的兜底 ----
  {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
    await page.goto(URL);
    await page.waitForTimeout(600);
    const css = await page.evaluate(() =>
      [...document.styleSheets].flatMap(s => { try { return [...s.cssRules].map(r => r.cssText); } catch { return []; } }).join("\n"));
    console.log("\n-- 兜底规则 --");
    // 顶栏（.cmdbar）已经不用毛玻璃了——半透明底 + 模糊在滚动时会透出内容发花，
    // 所以现在唯一用毛玻璃的是「更新遮罩」那一层。兜底规则要跟着搬到它身上。
    ck("用毛玻璃的层有不支持时的兜底（更新遮罩）", true,
      /@supports not \(\(-webkit-backdrop-filter/.test(css));
    ck("有 dvh 不支持时的 --app-h 兜底", true, /@supports not \(height: 100dvh\)|@supports not \(height:100dvh\)/.test(css));
    await page.close();
  }

  console.log(`\n结果：${pass} 通过 / ${fail} 失败`);
  await browser.close();
  process.exit(fail ? 1 : 0);
})();
