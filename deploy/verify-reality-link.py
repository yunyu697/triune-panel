#!/usr/bin/env python3
"""面板导出的 vless+REALITY 分享链接，贴进客户端真的能连吗？

对一台**测试用**面板跑完整链路，**xray 与 sing-box 两个内核各跑一遍**：

    建 vless+reality 入站 → 建用户 → 应用（真内核起进程）
      → 读内核实际渲染出的 REALITY 参数（结构断言）
      → 导出 `vless://` 分享链接 → **按 v2rayN 的方式解析链接**
      → 用链接里的参数拼客户端 → 经它的 socks 端口访问外网

它会真的建入站、应用配置、重启内核，所以：

    ⚠️ 只能指向测试实例。生产面板会被反复重启，正在跑的节点会断。

用法：

    # 1) 起一个干净的测试实例
    ./triune-panel serve -addr 127.0.0.1:18080 -data /tmp/tp-test
    # 2) 跑回归（--data 指向同一个数据目录，结构断言才读得到渲染结果）
    python3 deploy/verify-reality-link.py --pass 'Passw0rd!test' \\
        --xray /tmp/cores/xray --singbox /tmp/cores/sing-box --data /tmp/tp-test
    # 只测一个内核
    python3 deploy/verify-reality-link.py --pass '…' --core singbox …

退出码 0 表示全部通过；非 0 表示有用例不通，逐条打印原因与客户端日志。

## 为什么需要这个脚本

`REALITY: received real certificate (potential MITM or redirection)` 这句话的字面
意思是「客户端拿到的证书是真的」，而它真正想说的是**服务端没认出这个客户端**：
REALITY 对认不出的连接不拒绝、而是原样转发给 `dest`（比如 `www.apple.com:443`），
客户端于是真收到了 apple 的证书。服务端日志看起来一切正常——`received request …
accepted … email: u1`，回落也正常，两边都看不出问题在哪。

能让服务端「认不出」的只有三个字段：公钥、短 ID、SNI。三者任一与链接里不一致，
现象都是这一句。单元测试只验证「导出的链接长这样」，验证不了「按这个链接拼出来的
客户端能过认证」——这一层必须用真二进制。

## 为什么两个内核都要跑（v1.2.0 的教训）

v1.2.0 之前，sing-box 内核的 REALITY 入站**渲染时漏了 `tls.server_name`**。这一项
在 sing-box 的 REALITY 服务端上是必填的，缺了它内核照样启动、端口照样监听、
`sing-box check` 也过，但**每个客户端都在握手阶段被拒**。而当时的回归里：

  * 单元测试验证的是「渲染出的 JSON 长这样」——没有这一项也没人断言；
  * `deploy/verify-inbounds.py` 对 sing-box REALITY 只验证「内核起得来」；
  * 这个脚本只跑 xray 内核，而 xray 的 REALITY 用 `serverNames` 列表，
    根本没有 `tls.server_name` 这个字段，照着 xray 的字段写不会出错。

三处都不覆盖 → 一路绿灯发出去。**启动成功不等于能握手**，所以现在两个内核都跑，
并且专门断言 sing-box 的 `tls.server_name` 非空（`check_singbox_rendered`）。

## 客户端用谁

  * **xray 客户端：两个内核都测。** 这是 v2rayN / v2rayNG / mihomo 的形态，
    也是用户实际报障的那条路。
  * **sing-box 客户端：只测 sing-box 内核。** xray ≥ 26.7.11 的 REALITY 服务端默认
    只接受 26.3.27 以上的客户端，sing-box 的 REALITY 客户端会带着自己的版本号，
    大概率被拒——那是「客户端太老」，不是「链接错了」，记成 FAIL 只会制造假红。

## 关于探测目标

某些网络的 DNS 会把域名的 AAAA 投毒成 fc00::/ULA，xray 的 freedom 出站会拦私有
地址，测出来的「不通」是环境不是产品。脚本会先直连一次探测目标：连不上就明确打
`SKIP`，不记假红。要换目标用 `--probe` / `--probe-want`。
"""

import argparse
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

TOKEN = {"v": ""}
PASS = FAIL = SKIP = 0

# 短 ID 故意给非空：空串时两端都空，测不出偏差（xray 的 "" 是合法短 ID）
SHORT_IDS = ["aabbccdd", "11223344", "5566"]


def ck(name, ok, detail=""):
    global PASS, FAIL
    if ok:
        PASS += 1
        print(f"  PASS {name}")
    else:
        FAIL += 1
        print(f"  FAIL {name}  {detail}")


def skip(name, why):
    global SKIP
    SKIP += 1
    print(f"  SKIP {name}（{why}）")


def opener_for(url):
    """回环地址上的请求不走代理。

    开发机上普遍设着 HTTP_PROXY / HTTPS_PROXY，urllib 会老老实实把
    `http://127.0.0.1:18080` 也塞进代理，于是连面板本身都连不上，
    报的是 `upstream connect failed: Connection refused`——看起来像面板没起。
    远端面板仍然照旧走代理。
    """
    host = urllib.parse.urlsplit(url).hostname or ""
    if host in ("127.0.0.1", "localhost", "::1"):
        return urllib.request.build_opener(urllib.request.ProxyHandler({}))
    return urllib.request.build_opener()


def call(base, method, path, body=None, raw=False):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if TOKEN["v"]:
        req.add_header("Authorization", "Bearer " + TOKEN["v"])
    try:
        with opener_for(base + path).open(req, timeout=120) as r:
            b = r.read()
            return r.status, (b if raw else (json.loads(b) if b else None))
    except urllib.error.HTTPError as e:
        b = e.read()
        try:
            return e.code, (b if raw else json.loads(b))
        except Exception:
            return e.code, b.decode("utf-8", "replace")


def parse_vless(link):
    """按 v2rayN 的解析方式取参数：query 全部小写键，同名取第一个。"""
    u = urllib.parse.urlsplit(link)
    q = urllib.parse.parse_qs(u.query)

    def one(k):
        return (q.get(k) or [""])[0]

    return {
        "uuid": urllib.parse.unquote(u.username or ""),
        "host": u.hostname or "",
        "port": u.port or 443,
        "name": urllib.parse.unquote(u.fragment or ""),
        "security": one("security"),
        "sni": one("sni"),
        "fp": one("fp"),
        "pbk": one("pbk"),
        "sid": one("sid"),
        "spx": one("spx"),
        "type": one("type") or "tcp",
        "flow": one("flow"),
    }


# ---------------------------------------------------------------- 渲染结果断言

def read_rendered(data, core, port):
    """读内核实际渲染出的入站对象。

    按端口匹配而不是 `inbounds[0]`：测试实例上可能还留着别的入站，
    取第一个会让断言对错对象，报出来的错也指向错的地方。
    """
    p = os.path.join(data, "cores", core, "config.json")
    sj = json.load(open(p))
    key = "port" if core == "xray" else "listen_port"
    for ib in sj.get("inbounds", []):
        if ib.get(key) == port:
            return ib
    return None


def check_xray_rendered(ib):
    """xray 的 REALITY 参数长这样：streamSettings.realitySettings{privateKey, serverNames, shortIds}。"""
    rs = ((ib or {}).get("streamSettings") or {}).get("realitySettings") or {}
    print("  服务端 realitySettings:", json.dumps(
        {k: v for k, v in rs.items() if k != "privateKey"}, ensure_ascii=False))
    ck("渲染：xray realitySettings 存在", bool(rs))
    ck("渲染：xray privateKey 已写入", bool(rs.get("privateKey")))
    names = rs.get("serverNames") or []
    ck("渲染：xray serverNames 非空", bool(names), str(names))
    sids = rs.get("shortIds") or []
    ck("渲染：xray shortIds 含我们填的短 ID",
       any(s in sids for s in SHORT_IDS), f"shortIds={sids}")
    return sids, names


def check_singbox_rendered(ib):
    """sing-box 的 REALITY 参数长这样：tls{enabled, server_name, reality{handshake, private_key, short_id}}。

    其中 `tls.server_name` 就是 v1.2.0 补上的那一项。它缺失时内核照样启动、
    端口照样监听、`sing-box check` 也过，只有客户端握不上手——所以这里必须断言，
    而且要在「真连」之前断言：结构不对时这条更早、更直接地指出是哪一项。
    """
    tls = (ib or {}).get("tls") or {}
    real = tls.get("reality") or {}
    print("  服务端 tls:", json.dumps(
        {"enabled": tls.get("enabled"), "server_name": tls.get("server_name"),
         "reality": {k: v for k, v in real.items() if k != "private_key"}},
        ensure_ascii=False))
    ck("渲染：sing-box tls.enabled", tls.get("enabled") is True, str(tls.get("enabled")))
    ck("渲染：sing-box tls.server_name 非空（REALITY 服务端必填，v1.2.0 回归点）",
       bool((tls.get("server_name") or "").strip()), f"server_name={tls.get('server_name')!r}")
    ck("渲染：sing-box reality.handshake.server 非空",
       bool(((real.get("handshake") or {}).get("server"))),
       json.dumps(real.get("handshake"), ensure_ascii=False))
    ck("渲染：sing-box reality.private_key 已写入", bool(real.get("private_key")))
    sids = real.get("short_id") or []
    if isinstance(sids, str):
        sids = [sids]
    ck("渲染：sing-box short_id 含我们填的短 ID",
       any(s in sids for s in SHORT_IDS), f"short_id={sids}")
    return sids, ([tls["server_name"]] if tls.get("server_name") else [])


# ---------------------------------------------------------------- 客户端

def xray_client_cfg(c, socks_port):
    """按 v2rayN 的形态拼：vless + reality + uTLS 指纹。"""
    rs = {"serverName": c["sni"], "fingerprint": c["fp"] or "chrome",
          "publicKey": c["pbk"], "shortId": c["sid"]}
    if c["spx"]:
        rs["spiderX"] = c["spx"]
    user = {"id": c["uuid"], "encryption": "none"}
    if c["flow"]:
        user["flow"] = c["flow"]
    return {
        "log": {"loglevel": "debug"},
        "inbounds": [{"listen": "127.0.0.1", "port": socks_port,
                      "protocol": "socks", "settings": {"udp": False}}],
        "outbounds": [{
            "protocol": "vless",
            "settings": {"vnext": [{"address": c["host"], "port": c["port"],
                                    "users": [user]}]},
            "streamSettings": {"network": c["type"], "security": "reality",
                               "realitySettings": rs},
        }],
    }


def singbox_client_cfg(c, socks_port):
    """sing-box 客户端的 REALITY：tls.utls + tls.reality{public_key, short_id}。

    注意客户端这边的字段是 `server_name`（SNI），服务端那边也是同一个键——
    v1.2.0 漏的就是服务端这个键，而客户端一直是对的，所以从客户端配置上看不出问题。
    """
    ob = {
        "type": "vless", "tag": "proxy",
        "server": c["host"], "server_port": c["port"], "uuid": c["uuid"],
        "tls": {
            "enabled": True,
            "server_name": c["sni"],
            "utls": {"enabled": True, "fingerprint": c["fp"] or "chrome"},
            "reality": {"enabled": True, "public_key": c["pbk"], "short_id": c["sid"]},
        },
    }
    if c["flow"]:
        ob["flow"] = c["flow"]
    return {
        "log": {"level": "debug", "timestamp": True},
        "inbounds": [{"type": "socks", "tag": "in",
                      "listen": "127.0.0.1", "listen_port": socks_port}],
        "outbounds": [ob, {"type": "direct", "tag": "direct"}],
        "route": {"final": "proxy"},
    }


def wait_port(port, timeout=8.0):
    """等 socks 端口真的起来，比固定 sleep 稳：内核冷启动快慢不一。"""
    end = time.time() + timeout
    while time.time() < end:
        with socket.socket() as s:
            s.settimeout(0.3)
            if s.connect_ex(("127.0.0.1", port)) == 0:
                return True
        time.sleep(0.2)
    return False


def run_client(label, binpath, cfg, work, socks_port, a, egress_ok):
    """起客户端 → 经它的 socks 端口访问探测目标 → 收尾。

    这里**不能**给 curl 加 `--noproxy '*'`：它会把命令行上显式给的
    `--socks5-hostname` 一起废掉，curl 直接连出去。于是「经节点出网」测的是
    本机能不能上网，不是节点通不通——一个死代理也能让它绿（实测：
    `--noproxy '*' --socks5-hostname 127.0.0.1:1` 访问可达站点仍返回 204）。
    """
    path = os.path.join(work, f"{label}.json")
    json.dump(cfg, open(path, "w"), indent=2)
    log = os.path.join(work, f"{label}.log")
    with open(log, "w") as lf:
        proc = subprocess.Popen([binpath, "run", "-c", path], stdout=lf,
                                stderr=subprocess.STDOUT, text=True, cwd=work)
        try:
            if not wait_port(socks_port):
                ck(f"{label} 客户端起来", False, "socks 端口未监听，见 " + log)
            else:
                env = dict(os.environ, ALL_PROXY="", all_proxy="", NO_PROXY="")
                r = subprocess.run([
                    "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
                    "--max-time", "20",
                    "--socks5-hostname", f"127.0.0.1:{socks_port}", a.probe,
                ], capture_output=True, text=True, env=env, timeout=40)
                code = r.stdout.strip()
                if not egress_ok:
                    skip(f"{label} 客户端经节点出网",
                         f"{a.probe} 直连不可达，换 --probe / --probe-want")
                else:
                    ck(f"{label} 客户端经节点出网（期望 {a.probe_want}）",
                       code == a.probe_want, f"HTTP {code or '(无响应)'} {r.stderr[:120]}")
        finally:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=8)
            except subprocess.TimeoutExpired:
                proc.kill()

    txt = open(log, errors="replace").read()
    bad = [l for l in txt.splitlines()
           if re.search(r"REALITY|failed|error|FATAL", l, re.I)
           and "Reading config" not in l]
    if bad:
        print(f"  ---- {label} 客户端日志 ----")
        for l in bad[:8]:
            print("    " + l[:260])


# ---------------------------------------------------------------- 单个内核的完整链路

def run_core(a, core, idx, work_root, egress_ok):
    global PASS, FAIL
    print(f"\n=== {core} 内核 ===")
    port = a.port + idx
    socks_base = a.socks_port + idx * 10
    work = os.path.join(work_root, core)
    os.makedirs(work, exist_ok=True)

    # 完全按界面「新增 vless+reality 入站」写出来的 settings：
    # server_names 是字符串（表单里手填的形态）、short_ids 3 个、spider_x "/"
    settings = {
        "handshake_server": a.sni,
        "server_names": a.sni,
        "short_ids": SHORT_IDS,
        "spider_x": "/",
        "private_key": a.priv,
        "pbk": a.pub,
    }
    st, inb = call(a.base, "POST", "/api/inbounds", {
        "name": f"verify-reality-{core}", "core": core, "protocol": "vless",
        "port": port, "transport": "tcp", "tls": "reality",
        "tag": f"verify-reality-{core}", "enabled": True,
        "settings": json.dumps(settings),
    })
    if st != 200:
        ck(f"建 {core} 入站", False, f"{st} {str(inb)[:300]}")
        return
    iid = inb["id"]
    cid = None

    try:
        st, cl = call(a.base, "POST", "/api/clients", {
            "name": f"u-{core}",
            "credential": "11111111-2222-3333-4444-555555555555",
            "enabled": True, "inbound_ids": [iid],
        })
        if st != 200:
            ck("建用户", False, f"{st} {str(cl)[:300]}")
            return
        cid = cl.get("id")
        ck("建入站 + 建用户", True)

        st, ap = call(a.base, "POST", "/api/apply", {"core": core, "note": "verify reality link"})
        if not (st == 200 and isinstance(ap, dict) and ap.get("restarted") is True):
            ck(f"应用配置（{core} 内核重启）", False, f"{st} {str(ap)[:300]}")
            return
        ck(f"应用配置（{core} 内核重启）", True)
        time.sleep(1)

        # ---- 结构断言：链接必须和内核实际渲染出来的参数一致 ----
        # 只和「我们提交的 settings」一致是不够的——渲染那一步也可能丢字段，
        # v1.2.0 丢的正是 tls.server_name。
        if not a.data:
            skip("渲染结果断言", "没给 --data，读不到内核实际写出的配置")
            sids, names = list(SHORT_IDS), [a.sni]
        else:
            try:
                ib = read_rendered(a.data, core, port)
            except Exception as e:
                ck("读取内核渲染出的配置", False,
                   f"{os.path.join(a.data, 'cores', core, 'config.json')}: {e}")
                return
            if ib is None:
                ck("在渲染结果里找到本入站", False, f"端口 {port} 不在配置里")
                return
            ck("在渲染结果里找到本入站", True)
            sids, names = (check_xray_rendered(ib) if core == "xray"
                           else check_singbox_rendered(ib))

        # ---- 导出的链接 ----
        st, body = call(a.base, "GET", f"/api/inbounds/{iid}/export?format=links", None, raw=True)
        if st != 200:
            ck("导出分享链接", False,
               f"{st} {body[:200] if isinstance(body, bytes) else body}")
            return
        link = body.decode().strip().splitlines()[0]
        print("  分享链接:", link)
        c = parse_vless(link)

        # 这几项对不上，服务端就会把连接转给 dest，客户端报 received real certificate
        ck("链接带 security=reality", c["security"] == "reality", c["security"])
        ck("链接的 pbk 与服务端私钥配对", c["pbk"] == a.pub, f"链接={c['pbk']} 期望={a.pub}")
        ck("链接的 sid 在服务端短 ID 列表里", c["sid"] in sids, f"链接={c['sid']!r} 服务端={sids}")
        ck("链接的 sni 与服务端认的 SNI 一致", c["sni"] in names, f"链接={c['sni']!r} 服务端={names}")
        ck("链接带 fp（uTLS 指纹）", bool(c["fp"]), c["fp"])
        ck("链接的节点地址与端口正确", c["host"] == a.node_host and c["port"] == port,
           f"链接={c['host']}:{c['port']} 期望={a.node_host}:{port}")

        # ---- 真连：只按链接里的参数拼客户端 ----
        run_client("xray", a.xray, xray_client_cfg(c, socks_base), work,
                   socks_base, a, egress_ok)
        # sing-box 客户端只在 sing-box 服务端上测，理由见文件头「客户端用谁」
        if core == "singbox":
            run_client("singbox", a.singbox, singbox_client_cfg(c, socks_base + 1), work,
                       socks_base + 1, a, egress_ok)
    finally:
        # 收尾：删掉自己造的入站和用户，脚本要能反复跑
        if cid:
            call(a.base, "DELETE", f"/api/clients/{cid}")
        call(a.base, "DELETE", f"/api/inbounds/{iid}")
        call(a.base, "POST", "/api/apply", {"core": core, "note": "verify reality link cleanup"})


def main():
    ap = argparse.ArgumentParser(description="验证面板导出的 REALITY 链接在客户端侧能不能连")
    ap.add_argument("--base", default="http://127.0.0.1:18080", help="面板地址")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--pass", dest="password", required=True, help="面板密码")
    ap.add_argument("--core", choices=["xray", "singbox", "both"], default="both",
                    help="测哪个内核（默认两个都测）")
    ap.add_argument("--xray", required=True, help="xray 可执行文件（面板与本脚本共用）")
    ap.add_argument("--singbox", default="", help="sing-box 可执行文件（面板与本脚本共用）")
    ap.add_argument("--data", default="",
                    help="面板数据目录；给了才能断言内核实际渲染出的参数")
    ap.add_argument("--port", type=int, default=24443, help="入站端口起点（每个内核 +1）")
    ap.add_argument("--node-host", default="127.0.0.1",
                    help="写进「公网地址」，也就是链接里的节点地址")
    ap.add_argument("--sni", default="www.apple.com", help="REALITY 握手目标与 SNI")
    ap.add_argument("--probe", default="https://cp.cloudflare.com/generate_204")
    ap.add_argument("--probe-want", default="204")
    ap.add_argument("--socks-port", type=int, default=17894, help="客户端 socks 端口起点")
    a = ap.parse_args()

    cores = ["xray", "singbox"] if a.core == "both" else [a.core]

    if not (os.path.isfile(a.xray) and os.access(a.xray, os.X_OK)):
        print(f"xray 不可执行：{a.xray}")
        return 2
    if "singbox" in cores:
        if not a.singbox:
            print("测 sing-box 内核需要 --singbox（面板要能调起它，脚本也要用它当客户端）")
            return 2
        if not (os.path.isfile(a.singbox) and os.access(a.singbox, os.X_OK)):
            print(f"sing-box 不可执行：{a.singbox}")
            return 2
    if not a.data:
        print("注意：没给 --data，「渲染结果」这一层断言会跳过；"
              "真连仍然会测，但那要等握手失败才知道是哪一项丢了")

    st, j = call(a.base, "POST", "/api/login", {"user": a.user, "pass": a.password})
    if st != 200:
        print(f"登录失败：{st} {j}")
        return 2
    TOKEN["v"] = j["token"]

    # 面板渲染出的节点地址取自「公网地址」；不设它导出的就是回环地址
    call(a.base, "PUT", "/api/settings", {"public_host": a.node_host})
    call(a.base, "PUT", "/api/settings", {"bin_xray": a.xray})
    if a.singbox:
        call(a.base, "PUT", "/api/settings", {"bin_singbox": a.singbox})

    st, kp = call(a.base, "POST", "/api/tools/reality-keypair", {})
    if st != 200:
        print(f"生成密钥对失败：{st} {kp}")
        return 2
    a.priv, a.pub = kp["private_key"], kp["public_key"]

    # 先确认探测目标本身从这台机器可达，否则后面那条断言没有意义
    direct = subprocess.run(
        ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "8",
         "--noproxy", "*", a.probe],
        capture_output=True, text=True).stdout.strip()
    egress_ok = direct == a.probe_want
    if not egress_ok:
        print(f"注意：{a.probe} 直连返回 {direct or '(无响应)'}，"
              f"不是 {a.probe_want}，出网断言将跳过（换 --probe / --probe-want）")

    work_root = tempfile.mkdtemp(prefix="tprl-")
    try:
        for i, core in enumerate(cores):
            run_core(a, core, i, work_root, egress_ok)
    finally:
        if FAIL:
            print(f"\n（现场留在 {work_root}）")
        else:
            shutil.rmtree(work_root, ignore_errors=True)

    tail = f" / {SKIP} 跳过" if SKIP else ""
    print(f"\n结果：{PASS} 通过 / {FAIL} 失败{tail}")
    return 0 if FAIL == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
