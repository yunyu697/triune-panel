#!/usr/bin/env python3
"""面板导出的 vless+REALITY 分享链接，贴进 v2rayN 真的能连吗？

对一台**测试用**面板跑完整链路：

    建 vless+reality 入站 → 建用户 → 应用（真 xray 起内核）
      → 导出 `vless://` 分享链接 → **按 v2rayN 的方式解析链接** → 用链接里的参数
      拼一个 xray 客户端 → 经它访问外网

它会真的建入站、应用配置、重启内核，所以：

    ⚠️ 只能指向测试实例。生产面板会被反复重启，正在跑的节点会断。

用法：

    # 1) 起一个干净的测试实例
    ./triune-panel serve -addr 127.0.0.1:18080 -data /tmp/tp-test
    # 2) 指定面板要调起的 xray
    python3 deploy/verify-reality-link.py --pass 'Passw0rd!test' --xray /tmp/cores/xray

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
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

TOKEN = {"v": ""}
PASS = FAIL = 0


def ck(name, ok, detail=""):
    global PASS, FAIL
    if ok:
        PASS += 1
        print(f"  PASS {name}")
    else:
        FAIL += 1
        print(f"  FAIL {name}  {detail}")


def call(base, method, path, body=None, raw=False):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if TOKEN["v"]:
        req.add_header("Authorization", "Bearer " + TOKEN["v"])
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
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


def main():
    ap = argparse.ArgumentParser(description="验证面板导出的 REALITY 链接在客户端侧能不能连")
    ap.add_argument("--base", default="http://127.0.0.1:18080", help="面板地址")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--pass", dest="password", required=True, help="面板密码")
    ap.add_argument("--xray", required=True, help="xray 可执行文件（面板与本脚本共用）")
    ap.add_argument("--port", type=int, default=24443, help="入站端口（用完会删掉）")
    ap.add_argument("--node-host", default="127.0.0.1",
                    help="写进「公网地址」，也就是链接里的节点地址")
    ap.add_argument("--data", default="",
                    help="可选：面板数据目录，给了就额外核对内核实际渲染出的 REALITY 参数")
    ap.add_argument("--probe", default="https://cp.cloudflare.com/generate_204")
    ap.add_argument("--probe-want", default="204")
    ap.add_argument("--socks-port", type=int, default=17894, help="临时 socks 端口")
    a = ap.parse_args()

    st, j = call(a.base, "POST", "/api/login", {"user": a.user, "pass": a.password})
    if st != 200:
        print(f"登录失败：{st} {j}")
        return 1
    TOKEN["v"] = j["token"]

    # 面板渲染出的节点地址取自「公网地址」；不设它导出的就是回环地址
    call(a.base, "PUT", "/api/settings", {"public_host": a.node_host})
    call(a.base, "PUT", "/api/settings", {"bin_xray": a.xray})

    st, kp = call(a.base, "POST", "/api/tools/reality-keypair", {})
    if st != 200:
        print(f"生成密钥对失败：{st} {kp}")
        return 1
    priv, pub = kp["private_key"], kp["public_key"]

    # 完全按界面「新增 vless+reality 入站」写出来的 settings：
    # server_names 是字符串、short_ids 是 3 个、spider_x 是 "/"、pbk 也存着
    short_ids = ["aabbccdd", "11223344", "5566"]
    server_names = "www.apple.com"
    settings = {
        "handshake_server": server_names,
        "server_names": server_names,
        "short_ids": short_ids,
        "spider_x": "/",
        "private_key": priv,
        "pbk": pub,
    }
    st, inb = call(a.base, "POST", "/api/inbounds", {
        "name": "verify-reality", "core": "xray", "protocol": "vless",
        "port": a.port, "transport": "tcp", "tls": "reality",
        "tag": "verify-reality", "enabled": True,
        "settings": json.dumps(settings),
    })
    if st != 200:
        print(f"建入站失败：{st} {inb}")
        return 1
    iid = inb["id"]

    try:
        st, cl = call(a.base, "POST", "/api/clients", {
            "name": "u1",
            "credential": "11111111-2222-3333-4444-555555555555",
            "enabled": True, "inbound_ids": [iid],
        })
        if st != 200:
            print(f"建用户失败：{st} {cl}")
            return 1

        st, ap_ = call(a.base, "POST", "/api/apply", {"core": "xray"})
        ck("应用配置", st == 200 and ap_.get("restarted") is True, f"{st} {str(ap_)[:200]}")
        if st != 200:
            return 1
        time.sleep(1)

        # 内核实际渲染出来的参数（可选核对）：链接必须和它一致，
        # 只和「我们提交的 settings」一致是不够的——渲染那一步也可能丢字段。
        rs = None
        if a.data:
            p = os.path.join(a.data, "cores", "xray", "config.json")
            try:
                sj = json.load(open(p))
                rs = sj["inbounds"][0]["streamSettings"]["realitySettings"]
                print("  服务端 realitySettings:", json.dumps(
                    {k: v for k, v in rs.items() if k != "privateKey"}, ensure_ascii=False))
            except Exception as e:
                ck("读取内核渲染出的 REALITY 参数", False, f"{p}: {e}")
        want_sids = rs.get("shortIds", short_ids) if rs else short_ids
        want_names = rs.get("serverNames", [server_names]) if rs else [server_names]

        st, body = call(a.base, "GET", f"/api/inbounds/{iid}/export?format=links", None, raw=True)
        if st != 200:
            ck("导出分享链接", False, f"{st} {body[:200] if isinstance(body, bytes) else body}")
            return 1
        link = body.decode().strip().splitlines()[0]
        print("  分享链接:", link)
        c = parse_vless(link)

        # ---- 逐项对照：这几项对不上，服务端就会把连接转给 dest ----
        ck("链接带 security=reality", c["security"] == "reality", c["security"])
        ck("链接的 pbk 与服务端私钥配对", c["pbk"] == pub, f"链接={c['pbk']} 期望={pub}")
        ck("链接的 sid 在服务端 shortIds 里", c["sid"] in want_sids,
           f"链接={c['sid']!r} 服务端={want_sids}")
        ck("链接的 sni 在服务端 serverNames 里", c["sni"] in want_names,
           f"链接={c['sni']!r} 服务端={want_names}")
        ck("链接带 fp（uTLS 指纹）", bool(c["fp"]), c["fp"])
        ck("链接的节点地址与端口正确", c["host"] == a.node_host and c["port"] == a.port,
           f"链接={c['host']}:{c['port']} 期望={a.node_host}:{a.port}")

        # 先确认探测目标本身从这台机器可达，否则后面那条断言没有意义
        direct = subprocess.run(
            ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "8",
             "--noproxy", "*", a.probe],
            capture_output=True, text=True).stdout.strip()
        egress_ok = direct == a.probe_want
        if not egress_ok:
            print(f"  注意：{a.probe} 直连返回 {direct or '(无响应)'}，"
                  f"不是 {a.probe_want}，出网断言将跳过")

        # ---- 真连：只按链接里的参数拼客户端 ----
        # 前缀别带 reality：客户端日志里会回显这个路径，会误命中下面的 REALITY 过滤
        work = tempfile.mkdtemp(prefix="tprl-")
        rs_client = {"serverName": c["sni"], "fingerprint": c["fp"] or "chrome",
                     "publicKey": c["pbk"], "shortId": c["sid"]}
        if c["spx"]:
            rs_client["spiderX"] = c["spx"]
        user = {"id": c["uuid"], "encryption": "none"}
        if c["flow"]:
            user["flow"] = c["flow"]
        cfg = {
            "log": {"loglevel": "debug"},
            "inbounds": [{"listen": "127.0.0.1", "port": a.socks_port,
                          "protocol": "socks", "settings": {"udp": False}}],
            "outbounds": [{
                "protocol": "vless",
                "settings": {"vnext": [{"address": c["host"], "port": c["port"],
                                        "users": [user]}]},
                "streamSettings": {"network": c["type"], "security": "reality",
                                   "realitySettings": rs_client},
            }],
        }
        path = os.path.join(work, "client.json")
        json.dump(cfg, open(path, "w"), indent=2)
        print("  客户端 realitySettings:", json.dumps(rs_client, ensure_ascii=False))

        log = os.path.join(work, "client.log")
        with open(log, "w") as lf:
            proc = subprocess.Popen([a.xray, "run", "-c", path], stdout=lf,
                                    stderr=subprocess.STDOUT, text=True)
            try:
                time.sleep(2.5)
                env = dict(os.environ, ALL_PROXY="", all_proxy="", NO_PROXY="")
                r = subprocess.run([
                    "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
                    "--max-time", "20", "--socks5-hostname",
                    f"127.0.0.1:{a.socks_port}", a.probe,
                ], capture_output=True, text=True, env=env, timeout=30)
                code = r.stdout.strip()
                if not egress_ok:
                    print(f"SKIP 按链接连节点出网（{a.probe} 直连不可达，"
                          f"换 --probe / --probe-want）")
                else:
                    ck(f"按链接连节点出网（期望 {a.probe_want}）", code == a.probe_want,
                       f"HTTP {code or '(无响应)'} {r.stderr[:120]}")
            finally:
                proc.send_signal(signal.SIGTERM)
                try:
                    proc.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    proc.kill()

        txt = open(log, errors="replace").read()
        bad = [l for l in txt.splitlines()
               if re.search(r"REALITY|failed|error", l, re.I)
               and "Reading config" not in l]
        if bad:
            print("  ---- 客户端日志 ----")
            for l in bad[:10]:
                print("    " + l[:260])
        shutil.rmtree(work, ignore_errors=True)
    finally:
        # 收尾：删掉自己造的入站，脚本要能反复跑
        call(a.base, "DELETE", f"/api/inbounds/{iid}")

    print(f"\n结果：{PASS} 通过 / {FAIL} 失败")
    return 0 if FAIL == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
