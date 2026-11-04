#!/usr/bin/env python3
"""对一台**测试用**面板跑「新增入站 → 应用」的全协议回归。

它会真的建入站、应用配置、重启内核、再删掉这些入站，所以：

    ⚠️ 只能指向测试实例。生产面板会被反复重启，正在跑的节点会断。

用法：

    # 1) 起一个干净的测试实例
    ./triune-panel serve -addr 127.0.0.1:18080 -data /tmp/tp-test
    # 2) 跑回归（密码默认为启动时 bootstrap.creds 里写的那一个）
    python3 deploy/verify-inbounds.py --pass 'Passw0rd!test'

退出码 0 表示全部符合预期；非 0 表示有场景与预期不符，逐条打印原因。

为什么要这个脚本：`internal/livecheck` 只验证「渲染出的配置内核认不认」，
而这套脚本验证的是**整条链路**——HTTP 接口 → 入库 → 渲染 → 内核校验 →
进程真的起来 → 端口真的在监听。这一层踩过的坑（UDP 端口按 TCP 探测、
切内核后 reality 握手目标为空）单元测试全都发现不了。

**它不验证「客户端握不握得上手」。** 这里所有的绿只说明「配置被接受、内核起来了」，
而 v1.2.0 那个 bug 正好落在这条缝里：sing-box 的 REALITY 渲染漏了
`tls.server_name`，内核照常启动、端口照常监听、`sing-box check` 也过，
**每个客户端却都在握手阶段被拒**——这个脚本全绿。握手层的回归在
`deploy/verify-reality-link.py`（xray 与 sing-box 两个内核都跑，客户端也用真的）。
改了 REALITY 相关代码，两个都要跑。
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
import http.cookiejar

TOKEN = {"v": ""}
OPENER = None


def call(base, method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if TOKEN["v"]:
        req.add_header("Authorization", "Bearer " + TOKEN["v"])
    try:
        with OPENER.open(req, timeout=120) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "{}")
        except Exception:
            return e.code, {"error": e.reason}


def mk(name, core, proto, port, **kw):
    body = {
        "name": name, "core": core, "protocol": proto, "listen": "127.0.0.1",
        "port": port, "tag": name,
        "transport": kw.pop("transport", "tcp"),
        "tls": kw.pop("tls", "none"),
        "settings": json.dumps(kw.pop("settings", {})),
        "enabled": True, "draft": False,
    }
    body.update(kw)
    return body


def gen_cert(dirpath):
    cert = os.path.join(dirpath, "cert.pem")
    key = os.path.join(dirpath, "key.pem")
    subprocess.run(
        ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
         "-keyout", key, "-out", cert, "-days", "2",
         "-subj", "/CN=test.local", "-addext", "subjectAltName=DNS:test.local"],
        check=True, capture_output=True)
    return cert, key


def reality_keypair():
    """用 xray 自己生成密钥对；拿不到就退回一个固定值（只影响是否能校验，不影响结论）。"""
    try:
        out = subprocess.run(["xray", "x25519"], capture_output=True, text=True, timeout=10).stdout
        for line in out.splitlines():
            if line.startswith("PrivateKey:"):
                return line.split(":", 1)[1].strip()
    except Exception:
        pass
    return "UNv-pbZPCIGTrr5PeRJeTI-AaIKPJOJmHlfBOlCglVM"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:18080")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--pass", dest="password", required=True)
    ap.add_argument("--xray", default="/tmp/cores/xray", help="测试实例上可用的 xray 二进制")
    ap.add_argument("--singbox", default="/tmp/cores/sing-box")
    ap.add_argument("--base-port", type=int, default=21000, help="测试入站端口起点")
    args = ap.parse_args()

    global OPENER
    OPENER = urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    code, r = call(args.base, "POST", "/api/login",
                   {"user": args.user, "pass": args.password})
    if code != 200:
        print(f"登录失败 {code}: {r}")
        return 2
    TOKEN["v"] = r.get("token", "")

    call(args.base, "PUT", "/api/settings", {
        "bin_xray": args.xray, "bin_singbox": args.singbox, "fw_auto": "false",
    })

    tmp = tempfile.mkdtemp(prefix="tp-verify-")
    cert, key = gen_cert(tmp)
    rpriv = reality_keypair()
    p = args.base_port

    cases = [
        # (名称, 内核, body, 期望能否应用)
        ("xray vless+tcp+reality", "xray", mk(
            "vf-reality", "xray", "vless", p + 1, tls="reality",
            settings={"private_key": rpriv, "server_names": ["www.apple.com"],
                      "short_ids": ["", "abcd1234"]},
            clients=[{"name": "u1", "credential": "11111111-1111-4111-8111-111111111111"}]), True),
        ("xray reality 原生 dest", "xray", mk(
            "vf-reality-dest", "xray", "vless", p + 2, tls="reality",
            settings={"private_key": rpriv, "dest": "www.apple.com:443",
                      "server_names": ["www.apple.com"]},
            clients=[{"name": "u1", "credential": "22222222-2222-4222-8222-222222222222"}]), True),
        ("xray vmess+ws+tls(PEM)", "xray", mk(
            "vf-vmess", "xray", "vmess", p + 3, transport="ws", tls="tls",
            settings={"path": "/vm", "ws_host": "test.local",
                      "certificate_pem": open(cert).read(), "key_pem": open(key).read(),
                      "server_name": "test.local"},
            clients=[{"name": "u2", "credential": "33333333-3333-4333-8333-333333333333"}]), True),
        ("xray trojan+tls", "xray", mk(
            "vf-trojan", "xray", "trojan", p + 4, tls="tls",
            settings={"certificate_path": cert, "key_path": key, "server_name": "test.local"},
            clients=[{"name": "u3", "credential": "trojan-pass"}]), True),
        ("xray shadowsocks", "xray", mk(
            "vf-ss", "xray", "shadowsocks", p + 5, settings={"method": "aes-256-gcm"},
            clients=[{"name": "u4", "credential": "ss-pass-123456"}]), True),
        ("xray socks", "xray", mk(
            "vf-socks", "xray", "socks", p + 6, settings={"udp": 1},
            clients=[{"name": "u5", "credential": "su:sp"}]), True),
        ("xray http(2 用户)", "xray", mk(
            "vf-http", "xray", "http", p + 7,
            clients=[{"name": "h1", "credential": "hu1:hp1"},
                     {"name": "h2", "credential": "hu2:hp2"}]), True),
        ("xray dokodemo-door", "xray", mk(
            "vf-dd", "xray", "dokodemo-door", p + 8,
            settings={"dd_address": "127.0.0.1", "dd_port": 22, "dd_network": "tcp"}), True),
        ("xray wireguard（UDP）", "xray", mk(
            "vf-wg", "xray", "wireguard", p + 9,
            settings={"xray_raw_settings": {
                "secretKey": rpriv, "address": ["10.66.66.1/24"],
                "peers": [{"publicKey": rpriv, "allowedIPs": ["10.66.66.2/32"]}],
                "mtu": 1420}}), True),
        ("xray kcp（UDP）", "xray", mk(
            "vf-kcp", "xray", "vless", p + 10, transport="kcp",
            settings={"kcp_seed": "s3cret", "kcp_header": "wechat-video"},
            clients=[{"name": "u7", "credential": "77777777-7777-4777-8777-777777777777"}]), True),
        ("xray xhttp stream-one+tls", "xray", mk(
            "vf-xhttp", "xray", "vless", p + 11, transport="xhttp", tls="tls",
            settings={"path": "/xh", "xhttp_mode": "stream-one",
                      "certificate_path": cert, "key_path": key,
                      "server_name": "test.local", "alpn": ["h2", "http/1.1"]},
            clients=[{"name": "u6", "credential": "66666666-6666-4666-8666-666666666666"}]), True),

        ("sb vless+vision+tls", "singbox", mk(
            "vf-sb-vless", "singbox", "vless", p + 21, tls="tls",
            settings={"flow": "xtls-rprx-vision", "certificate_path": cert,
                      "key_path": key, "server_name": "test.local"},
            clients=[{"name": "s1", "credential": "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"}]), True),
        ("sb hysteria2", "singbox", mk(
            "vf-sb-hy2", "singbox", "hysteria2", p + 22, tls="tls",
            settings={"certificate_path": cert, "key_path": key, "server_name": "test.local"},
            clients=[{"name": "s2", "credential": "hy2-pass"}]), True),
        ("sb tuic", "singbox", mk(
            "vf-sb-tuic", "singbox", "tuic", p + 23, tls="tls",
            settings={"certificate_path": cert, "key_path": key, "server_name": "test.local"},
            clients=[{"name": "s3", "credential": "cccccccc-3333-4333-8333-cccccccccccc"}]), True),
        ("sb reality（只填 SNI）", "singbox", mk(
            "vf-sb-reality", "singbox", "vless", p + 24, tls="reality",
            settings={"private_key": rpriv, "server_names": ["www.apple.com", "apple.com"],
                      "short_ids": ["ab12"]},
            clients=[{"name": "s4", "credential": "dddddddd-4444-4444-8444-dddddddddddd"}]), True),
        ("sb direct 端口转发", "singbox", mk(
            "vf-sb-direct", "singbox", "direct", p + 25,
            settings={"dd_address": "127.0.0.1", "dd_port": 22}), True),
        ("sb socks", "singbox", mk(
            "vf-sb-socks", "singbox", "socks", p + 26,
            clients=[{"name": "s5", "credential": "su:sp"}]), True),

        # 负向：必须被拦下，且原因可读
        ("拒绝：reality 缺私钥", "xray", mk(
            "vf-neg-nokey", "xray", "vless", p + 31, tls="reality",
            settings={"server_names": ["www.apple.com"]}), False),
        ("拒绝：reality 缺握手目标", "xray", mk(
            "vf-neg-nodest", "xray", "vless", p + 32, tls="reality",
            settings={"private_key": rpriv}), False),
        ("拒绝：xray h2 传输已移除", "xray", mk(
            "vf-neg-h2", "xray", "vless", p + 33, transport="h2"), False),
        ("拒绝：xray TLS 无证书", "xray", mk(
            "vf-neg-nocert", "xray", "vless", p + 34, tls="tls",
            settings={"server_name": "test.local"},
            clients=[{"name": "n", "credential": "99999999-9999-4999-8999-999999999999"}]), False),
        ("拒绝：sb quic 无 TLS", "singbox", mk(
            "vf-neg-quic", "singbox", "vless", p + 35, transport="quic",
            clients=[{"name": "n", "credential": "99999998-9999-4999-8999-999999999999"}]), False),
    ]

    fails = []
    print(f"共 {len(cases)} 个场景，端口 {p + 1}..{p + 35}\n")
    for title, core, body, expect in cases:
        code, r = call(args.base, "POST", "/api/inbounds", body)
        if code >= 300:
            # 保存期就拦下也算「被拦」，但正向场景是失败
            ok = not expect
            print(f"[{'BLOCKED' if ok else 'FAIL   '}] {title}: 建入站 {code} {r.get('error', '')[:120]}")
            if not ok:
                fails.append((title, "create", code, r))
            continue
        iid = r.get("id") or (r.get("inbound") or {}).get("id")
        code2, r2 = call(args.base, "POST", "/api/apply", {"core": core, "note": "verify " + title})
        applied = code2 < 300 and r2.get("validated") and r2.get("restarted")
        ok = applied == expect
        label = "OK     " if ok else ("LEAK!!! " if not expect else "FAIL   ")
        print(f"[{label}] {title}: apply={code2} "
              f"{'' if applied else (r2.get('error') or '')[:160].replace(chr(10), ' / ')}")
        if not ok:
            fails.append((title, "apply", code2, r2))
        if iid:
            call(args.base, "DELETE", f"/api/inbounds/{iid}")
            call(args.base, "POST", "/api/apply", {"core": core, "note": "verify cleanup"})

    # ---- 同端口规则：同协议族必须拦在保存期，TCP+UDP 必须能共存 ----
    # 以前没有这条校验：两个入站抢同一端口能一路存库、渲染、替换配置，直到内核
    # 起不来——而那一刻配置已经被换掉，该内核下所有节点一起断。
    print()
    same_port = p + 40
    keep = []

    code, r = call(args.base, "POST", "/api/inbounds",
                   mk("vf-same-tcp", "xray", "vless", same_port))
    if code < 300:
        keep.append(r.get("id"))
        print(f"[OK     ] 同端口：先建 TCP 入站 create={code}")
    else:
        print(f"[FAIL   ] 同端口：先建 TCP 入站 create={code} {r.get('error', '')[:120]}")
        fails.append(("同端口-基座", "create", code, r))

    code, r = call(args.base, "POST", "/api/inbounds",
                   mk("vf-same-dup", "xray", "vmess", same_port))
    if code == 400 and "已被" in (r.get("error") or ""):
        print(f"[OK     ] 同端口：同协议族第二个被拦下 -> {(r.get('error') or '')[:90]}")
    else:
        print(f"[LEAK!!! ] 同端口：同协议族第二个应 400，实际 {code} {(r.get('error') or '')[:120]}")
        fails.append(("同端口-重复", "create", code, r))
        if code < 300:
            keep.append(r.get("id"))

    code, r = call(args.base, "POST", "/api/inbounds",
                   mk("vf-same-udp", "singbox", "hysteria2", same_port, tls="tls",
                      settings={"certificate_path": cert, "key_path": key,
                                "server_name": "test.local"},
                      clients=[{"name": "hy2", "credential": "hy2-pass"}]))
    if code < 300:
        keep.append(r.get("id"))
        print(f"[OK     ] 同端口：TCP + UDP 共存被允许 create={code}")
    else:
        print(f"[FAIL   ] 同端口：TCP + UDP 应允许，实际 {code} {r.get('error', '')[:120]}")
        fails.append(("同端口-TCP+UDP", "create", code, r))

    # 两个真的能同时跑起来：xray 占 TCP、singbox 占 UDP
    for c in ("xray", "singbox"):
        code, r = call(args.base, "POST", "/api/apply", {"core": c, "note": "verify same-port"})
        applied = code < 300 and r.get("validated") and r.get("restarted")
        ok = applied
        print(f"[{'OK     ' if ok else 'FAIL   '}] 同端口：{c} 应用 -> {code} "
              f"{'' if ok else (r.get('error') or '')[:120]}")
        if not ok:
            fails.append((f"同端口-{c}应用", "apply", code, r))

    for iid in keep:
        if iid:
            call(args.base, "DELETE", f"/api/inbounds/{iid}")
    for c in ("xray", "singbox"):
        call(args.base, "POST", "/api/apply", {"core": c, "note": "verify same-port cleanup"})

    print()
    if fails:
        print(f"{len(fails)} 个场景与预期不符：")
        for t, stage, code, r in fails:
            print(f"  - {t} [{stage} {code}] {json.dumps(r, ensure_ascii=False)[:300]}")
        return 1
    print("全部场景符合预期")
    print("（注意：本轮只验证「配置被接受、内核起来了」，不验证客户端能不能握手；"
          "握手层见 deploy/verify-reality-link.py）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
