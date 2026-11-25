/* 面板自更新的等待层与「不用重新登录」回归。
 *
 * 这条链路里有三件事只能整体验，单测各管一段：
 *   1. 更新那几秒页面到底给用户看了什么（等待层是否挡住误操作）
 *   2. 进程回来后是自动回原页面，还是老老实实要求重登
 *   3. 失败时能不能说清是「没下载下来」「没重启成功」还是「还没回来」
 *
 * 演示页没有真进程可以 exec，所以用 window.__demoUpdate 指定结局，
 * 前端走的是与线上完全相同的状态机（见 app.js 的 runPanelOp / updWatch）。
 *
 * 覆盖三条**都会换进程**的入口：面板自更新卡片、版本列表弹窗、面板回滚。
 * v2.1.9 只接了第一条，用户点第二条时既没等待层也掉了登录——所以每个入口
 * 各留一条用例，漏一个就红。
 */
const { launch } = require("./browser");
const path = require("path");
const URL = "file://" + path.resolve(__dirname, "..", "demo.html") + "#demo";

let pass = 0, fail = 0;
const ck = (name, want, got) => {
  const ok = String(want) === String(got);
  ok ? pass++ : fail++;
  console.log(`${ok ? "PASS" : "FAIL"} ${name}${ok ? "" : `  期望=[${want}] 实际=[${got}]`}`);
};

const $txt = id => `document.getElementById(${JSON.stringify(id)}).textContent.trim()`;

/* 打开一个干净的演示页，设定结局分支与超时预算，然后触发更新。
   __updBudgetMs 调小是为了让「等不到」那条分支在用例里几秒内走完；
   线上是 90 秒（见 app.js 的 updBudget）。 */
async function start(browser, mode) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  await page.goto(URL);
  await page.waitForTimeout(350);
  await page.evaluate(m => {
    window.__demoUpdate = m;
    window.__updBudgetMs = 2200;
    selfUpdate();
  }, mode);
  await page.waitForTimeout(150);
  // 自更新前有确认弹窗；不点它什么都不会发生
  await page.click("#cf_ok");
  return { page, errors };
}

const waitTitle = (page, text, timeout = 6000) =>
  page.waitForFunction(
    t => document.getElementById("upwait_title").textContent.trim() === t,
    text, { timeout });

(async () => {
  const browser = await launch();

  /* ---- 1. 正常：等待层立刻出现，挡住了下面的内容，最后自动回主界面 ---- */
  {
    const { page, errors } = await start(browser, "ok");

    // 下载阶段就该看到等待层，而不是等 5 秒后才发现「更新完了」
    await page.waitForTimeout(200);
    ck("更新一开始就有等待层", false,
      await page.evaluate(() => document.getElementById("upwait").classList.contains("hide")));
    ck("等待层标题说在更新", "正在更新面板",
      await page.evaluate($txt("upwait_title")));
    // 挡住误操作：点在屏幕中心命中的必须是等待层，不是登录表单或旧页面
    ck("等待层盖住整页", "upwait",
      await page.evaluate(() => {
        const el = document.elementFromPoint(window.innerWidth / 2, window.innerHeight / 2);
        const box = el && el.closest("#upwait");
        return box ? "upwait" : (el ? el.id || el.tagName.toLowerCase() : "none");
      }));
    ck("等待层没有关闭按钮", 0,
      await page.evaluate(() => document.querySelectorAll('#upwait [data-act="closeModal"]').length));
    // 遮罩要的是「毛玻璃」，不是把页面涂死：能看出底下是面板，但不能操作
    ck("遮罩是高斯模糊而非实底", true,
      await page.evaluate(() => {
        const s = getComputedStyle(document.getElementById("upwait"));
        return /blur\(/.test(s.backdropFilter || s.webkitBackdropFilter || "");
      }));
    // inert 管的是键盘：只有全屏覆盖的话，Tab 还能跑到下面的按钮上
    ck("底下页面被 inert", true,
      await page.evaluate(() => document.getElementById("app").hasAttribute("inert")));

    await waitTitle(page, "更新完成");
    ck("换到了新会话 token", "demo-resumed-token",
      await page.evaluate(() => localStorage.getItem("triune_token")));
    ck("凭证用完即清", null,
      await page.evaluate(() => sessionStorage.getItem("updResume")));
    ck("没有页面异常", 0, errors.length);
    if (errors.length) console.log(errors.join("\n"));
    await page.close();
  }

  /* ---- 2. 换票失败：说清楚要重登，账密没变，登录页已经备好 ---- */
  {
    const { page, errors } = await start(browser, "resume-fail");
    await waitTitle(page, "更新完成，请重新登录");
    ck("副标题带出服务端给的原因", true,
      await page.evaluate(() => document.getElementById("upwait_sub").textContent.includes("过期")));
    ck("登录页已切好（不再显示主界面）", "login",
      await page.evaluate(() => {
        const app = document.getElementById("app").classList.contains("hide");
        const login = document.getElementById("login").classList.contains("hide");
        return app && !login ? "login" : "app";
      }));
    await page.click('[data-act="upwaitGo"]');
    await page.waitForTimeout(150);
    ck("点「去登录」后等待层撤掉", true,
      await page.evaluate(() => document.getElementById("upwait").classList.contains("hide")));
    ck("撤层后 inert 一起撤掉", false,
      await page.evaluate(() => document.getElementById("login").hasAttribute("inert")));
    ck("没有页面异常", 0, errors.length);
    if (errors.length) console.log(errors.join("\n"));
    await page.close();
  }

  /* ---- 3. 没拿到凭证：同样要求重登，但不是卡住不动 ---- */
  {
    const { page, errors } = await start(browser, "no-ticket");
    await waitTitle(page, "更新完成，请重新登录");
    ck("没凭证时也给明确出口", "去登录",
      await page.evaluate(() => document.querySelector('[data-act="upwaitGo"]').textContent.trim()));
    ck("没有页面异常", 0, errors.length);
    if (errors.length) console.log(errors.join("\n"));
    await page.close();
  }

  /* ---- 4. 换了文件但没重启成功：这是「版本号没变」的真正原因，必须摊开说 ---- */
  {
    const { page, errors } = await start(browser, "restart-fail");
    await waitTitle(page, "更新没有完成");
    ck("副标题带出重启失败原因", true,
      await page.evaluate(() => document.getElementById("upwait_sub").textContent.includes("重启失败")));
    ck("给了回滚入口", true,
      await page.evaluate(() => document.getElementById("upwait_hint").textContent.includes("tp update --rollback")));
    ck("这种情况不出现「再等一会儿」（等也没用）", true,
      await page.evaluate(() => document.querySelector('[data-act="upwaitRetry"]').classList.contains("hide")));
    ck("没有页面异常", 0, errors.length);
    if (errors.length) console.log(errors.join("\n"));
    await page.close();
  }

  /* ---- 5. 一直没回来：允许再等，也给出回滚办法；「再等一会儿」要能重来一轮 ---- */
  {
    const { page, errors } = await start(browser, "timeout");
    await waitTitle(page, "面板还没有回来");
    ck("超时后保留「再等一会儿」", false,
      await page.evaluate(() => document.querySelector('[data-act="upwaitRetry"]').classList.contains("hide")));
    await page.click('[data-act="upwaitRetry"]');
    await page.waitForTimeout(150);
    ck("点「再等一会儿」会重新开始等", "继续等待面板",
      await page.evaluate($txt("upwait_title")));
    ck("没有页面异常", 0, errors.length);
    if (errors.length) console.log(errors.join("\n"));
    await page.close();
  }

  /* ---- 6. 下载就失败了：进程一步都没动，不该假装在重启 ---- */
  {
    const { page, errors } = await start(browser, "download-fail");
    await waitTitle(page, "更新没有开始");
    ck("副标题带出下载错误", true,
      await page.evaluate(() => document.getElementById("upwait_sub").textContent.includes("404")));
    ck("提示现有版本还在跑", true,
      await page.evaluate(() => document.getElementById("upwait_hint").textContent.includes("现有版本还在跑")));
    ck("没有页面异常", 0, errors.length);
    if (errors.length) console.log(errors.join("\n"));
    await page.close();
  }

  /* ---- 7. 重启期间那批 401 不能把人踢走 ----
     这是「不用重新登录」的底线：更新中途任何一次后台请求 401 都清掉本地
     token 的话，后面就算换到票也没地方放。 */
  {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors = [];
    page.on("pageerror", e => errors.push(e.message));
    await page.goto(URL);
    await page.waitForTimeout(300);

    ck("平时 401 会把页面切到登录", true, await page.evaluate(() => {
      const kicked = authLost();
      const onLogin = !document.getElementById("login").classList.contains("hide");
      return kicked && onLogin;
    }));
    ck("等待层在时 401 不踢人", false, await page.evaluate(() => {
      UPD.on = true;
      const kicked = authLost();
      UPD.on = false;
      return kicked;
    }));
    ck("没有页面异常", 0, errors.length);
    if (errors.length) console.log(errors.join("\n"));
    await page.close();
  }

  /* ---- 8. 版本列表弹窗里的「更新」也必须走等待层 ----
     v2.1.9 只接了「面板自更新」卡片那条路，用户实际点的是这里，
     于是既看不到等待界面、也必然掉登录。这条用例就是那次漏接的门闩。 */
  {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors = [];
    page.on("pageerror", e => errors.push(e.message));
    await page.goto(URL);
    await page.waitForTimeout(300);
    await page.evaluate(() => {
      window.__demoUpdate = "ok";
      window.__updBudgetMs = 2200;
      verList = [{ tag: "v9.9.9-demo" }];
      const sel = document.getElementById("vs_select");
      sel.innerHTML = '<option>v9.9.9-demo</option>';
      sel.value = "0";
      document.getElementById("vs_component").textContent = "panel";
      document.getElementById("vs_url").value = "";
      document.getElementById("vs_sha").value = "";
      document.getElementById("vs_mirror").value = "";
      confirmVersionUpdate();
    });
    await page.waitForTimeout(150);
    await page.click("#cf_ok");
    await page.waitForTimeout(300);
    ck("版本弹窗更新也会挂等待层", false,
      await page.evaluate(() => document.getElementById("upwait").classList.contains("hide")));
    ck("版本弹窗更新：等待层盖住整页", "upwait",
      await page.evaluate(() => {
        const el = document.elementFromPoint(window.innerWidth / 2, window.innerHeight / 2);
        return (el && el.closest("#upwait")) ? "upwait" : "other";
      }));
    await waitTitle(page, "更新完成");
    ck("版本弹窗更新：换到新会话，不用重登", "demo-resumed-token",
      await page.evaluate(() => localStorage.getItem("triune_token")));
    ck("没有页面异常", 0, errors.length);
    if (errors.length) console.log(errors.join("\n"));
    await page.close();
  }

  /* ---- 9. 面板回滚同样是换进程，同样要给等待层与接力凭证 ---- */
  {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors = [];
    page.on("pageerror", e => errors.push(e.message));
    await page.goto(URL);
    await page.waitForTimeout(300);
    await page.evaluate(() => {
      window.__demoUpdate = "ok";
      window.__updBudgetMs = 2200;
      const sel = document.getElementById("rb_select");
      sel.innerHTML = '<option value="v2.0.0-20260101-000000.bak">b</option>';
      sel.value = "v2.0.0-20260101-000000.bak";
      doPanelRollback();
    });
    await page.waitForTimeout(150);
    await page.click("#cf_ok");
    await page.waitForTimeout(300);
    ck("回滚也挂等待层", false,
      await page.evaluate(() => document.getElementById("upwait").classList.contains("hide")));
    ck("回滚的标题说的是回滚", "正在回滚面板",
      await page.evaluate($txt("upwait_title")));
    // 回滚和更新在视觉上要能分开（琥珀色 + 回退箭头），否则用户以为在升级
    ck("回滚有独立的遮罩状态", true,
      await page.evaluate(() => document.getElementById("upwait").classList.contains("rollback")));
    await waitTitle(page, "回滚完成");
    ck("回滚完也不用重登", "demo-resumed-token",
      await page.evaluate(() => localStorage.getItem("triune_token")));
    ck("没有页面异常", 0, errors.length);
    if (errors.length) console.log(errors.join("\n"));
    await page.close();
  }

  /* ---- 10. 更新中途页面被刷新：等待层要自己接回去 ----
     这条是兜底：用户手快按了 F5、浏览器把标签页回收、或将来新加的入口
     只写了请求没写等待层——只要请求经过了 runPanelOp，标记就在，
     重新载入时不能再画一遍旧界面然后 401 掉到登录页。 */
  {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors = [];
    page.on("pageerror", e => errors.push(e.message));
    await page.goto(URL);
    await page.waitForTimeout(300);
    await page.evaluate(() => {
      sessionStorage.setItem("updRun", JSON.stringify({
        kind: "update", t0: Date.now(), old: "v2.1.8-demo", expect: "v9.9.9-demo"
      }));
      sessionStorage.setItem("updResume", "demo-ticket");
      window.__updBudgetMs = 2200;
    });
    await page.reload();
    await page.waitForTimeout(400);
    ck("刷新后等待层自己接回来", false,
      await page.evaluate(() => document.getElementById("upwait").classList.contains("hide")));
    ck("接回来时说明是「接着等」", true,
      await page.evaluate(() => document.getElementById("upwait_sub").textContent.includes("重新载入")));
    await waitTitle(page, "更新完成");
    ck("接回来后照样换到会话", "demo-resumed-token",
      await page.evaluate(() => localStorage.getItem("triune_token")));
    ck("在途标记用完即清", null,
      await page.evaluate(() => sessionStorage.getItem("updRun")));
    ck("没有页面异常", 0, errors.length);
    if (errors.length) console.log(errors.join("\n"));
    await page.close();
  }

  await browser.close();
  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})();
