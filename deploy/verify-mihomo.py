#!/usr/bin/env python3
"""导出给 mihomo 客户端的配置，真的能用吗？

对一台**测试用**面板跑完整链路：

    建入站 → 建用户 → 应用（真 xray 起内核）→ 导出 mihomo 客户端配置
      → mihomo -t 校验 → 真跑 mihomo → 经它的 socks 端口访问外网

它会真的建入站、应用配置、重启内核、起停 mihomo，所以：

    ⚠️ 只能指向测试实例。生产面板会被反复重启，正在跑的节点会断。

用法：

    # 1) 起一个干净的测试实例
    ./triune-panel serve -addr 127.0.0.1:18080 -data /tmp/tp-test
    # 2) 准备内核二进制（面板要能调起 xray，本脚本要能调起 mihomo）
    python3 deploy/verify-mihomo.py --pass 'Passw0rd!test' \\
        --xray /tmp/cores/xray --mihomo /tmp/cores/mihomo

退出码 0 表示全部通过；非 0 表示有用例不通，逐条打印原因与 mihomo 日志。

为什么需要这个脚本：`internal/livecheck` 只验证「渲染出的服务端配置内核认不认」，
单元测试只验证「导出的 YAML 长这样」。两者都证明不了「这份 YAML 贴进 mihomo
之后能出网」——而用户报的正是这件事。这一层踩过的坑（REALITY 公钥与私钥不是
同一代、mihomo 缺 client-fingerprint、REALITY 认证失败只在客户端报一句
authentication failed）单测全都发现不了。

关于探测目标：默认用 IP 字面量 `https://1.1.1.1/cdn-cgi/trace`。某些网络的
DNS 会把域名的 AAAA 投毒成 fc00::/ULA，xray 的 freedom 出站会拦私有地址，
测出来的「不通」是环境不是产品。要换目标用 --probe / --probe-want。
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
            if raw:
                return r.status, b
            return r.status, (json.loads(b.decode() or "{}") if b else {})
    except urllib.error.HTTPError as e:
        b = e.read()
        if raw:
            return e.code, b
        try:
            return e.code, json.loads(b.decode() or "{}")
        except Exception:
            return e.code, b.decode("utf-8", "replace")


def proxies_block(yaml):
    """把 proxies 段整段抠出来打印，出问题时一眼能看出少了哪项。"""
    out, inside = [], False
    for ln in yaml.splitlines():
        if ln.startswith("proxies:"):
            inside = True
        elif inside and ln and not ln.startswith((" ", "\t", "-")):
            break
        if inside:
            out.append(ln)
    return "\n".join(out)


def run_case(args, case, idx, work_root):
    print(f"\n=== {case['label']} ===")
    st, inb = call(args.base, "POST", "/api/inbounds", case["inbound"])
    if st != 200:
        ck("建入站", False, f"{st} {inb}")
        return
    iid = inb["id"]
    ck("建入站", True)

    st, cl = call(args.base, "POST", "/api/clients", {
        "name": "u1", "credential": case["cred"], "enabled": True,
        "inbound_ids": [iid],
    })
    if st != 200:
        ck("建用户", False, f"{st} {cl}")
        call(args.base, "DELETE", f"/api/inbounds/{iid}")
        return
    ck("建用户", True)

    st, ap = call(args.base, "POST", "/api/apply", {"core": "xray"})
    ok = st == 200 and isinstance(ap, dict) and ap.get("restarted") is True
    ck("应用配置（内核重启）", ok, f"{st} {str(ap)[:200]}")
    if not ok:
        call(args.base, "DELETE", f"/api/inbounds/{iid}")
        return
    time.sleep(1)

    st, env = call(args.base, "POST", "/api/export/config", {
        "core": "mihomo", "target": "client", "inbound_ids": [iid],
    })
    if st != 200 or not isinstance(env, dict) or "content" not in env:
        ck("导出 mihomo 客户端配置", False, f"{st} {str(env)[:300]}")
        call(args.base, "DELETE", f"/api/inbounds/{iid}")
        return
    ck("导出 mihomo 客户端配置", True)
    yaml = env["content"]
    print("  ---- proxies ----")
    print("\n".join("    " + l for l in proxies_block(yaml).splitlines()))

    work = os.path.join(work_root, f"c{idx}")
    os.makedirs(work, exist_ok=True)
    path = os.path.join(work, "config.yaml")
    with open(path, "w") as f:
        f.write(yaml)

    r = subprocess.run([args.mihomo, "-t", "-f", path, "-d", work],
                       capture_output=True, text=True, timeout=180)
    ck("mihomo -t 校验", r.returncode == 0, (r.stdout + r.stderr)[-400:])

    # 换端口避免撞上本机已有的 mihomo；规则集要联网下载，与本用例无关，摘掉
    socks = args.socks_base + idx
    yaml = yaml.replace("mixed-port: 7890", f"mixed-port: {socks}")
    yaml = yaml.replace("external-controller: 127.0.0.1:9090",
                        f"external-controller: 127.0.0.1:{args.ctrl_base + idx}")
    yaml = yaml.replace("log-level: warning", "log-level: debug")
    keep, skip = [], False
    for ln in yaml.splitlines():
        if ln.startswith("rule-providers:"):
            skip = True
            continue
        if skip and ln and not ln.startswith((" ", "\t")):
            skip = False
        if skip:
            continue
        keep.append(ln)
    yaml = "\n".join(keep)
    with open(path, "w") as f:
        f.write(yaml)

    logpath = os.path.join(work, "run.log")
    with open(logpath, "w") as logf:
        proc = subprocess.Popen([args.mihomo, "-f", path, "-d", work],
                                stdout=logf, stderr=subprocess.STDOUT, text=True)
        try:
            time.sleep(4)
            ck("mihomo 进程存活", proc.poll() is None, "进程已退出，见 run.log")
            env2 = dict(os.environ, ALL_PROXY="", all_proxy="", NO_PROXY="")
            r = subprocess.run([
                "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
                "--max-time", "25", "--noproxy", "*",
                "--socks5-hostname", f"127.0.0.1:{socks}", args.probe,
            ], capture_output=True, text=True, env=env2, timeout=45)
            code = r.stdout.strip()
            ck(f"经节点出网（期望 {args.probe_want}）", code == args.probe_want,
               f"HTTP {code or '(无响应)'} {r.stderr[:120]}")
        finally:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()

    log = open(logpath, errors="replace").read()
    bad = [l for l in log.splitlines() if re.search(r"level=(error|fatal)", l)]
    if bad:
        print("  ---- mihomo 日志 error ----")
        for l in bad[:8]:
            print("    " + l[:280])

    call(args.base, "DELETE", f"/api/inbounds/{iid}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:18080")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--pass", dest="password", required=True)
    ap.add_argument("--xray", default="/tmp/cores/xray", help="测试实例上可用的 xray 二进制")
    ap.add_argument("--mihomo", default="/tmp/cores/mihomo", help="本机要调起来的 mihomo 二进制")
    ap.add_argument("--probe", default="https://1.1.1.1/cdn-cgi/trace",
                    help="经节点探测的目标，默认 IP 字面量以绕开被投毒的 AAAA")
    ap.add_argument("--probe-want", default="200", help="探测目标期望的 HTTP 状态码")
    ap.add_argument("--socks-base", type=int, default=17900, help="客户端 socks 端口起点")
    ap.add_argument("--ctrl-base", type=int, default=19100, help="mihomo 控制端口起点")
    args = ap.parse_args()

    for name, p in (("xray", args.xray), ("mihomo", args.mihomo)):
        if not (os.path.isfile(p) and os.access(p, os.X_OK)):
            print(f"{name} 二进制不可执行: {p}")
            return 2

    st, j = call(args.base, "POST", "/api/login",
                 {"user": args.user, "pass": args.password})
    if st != 200:
        print("登录失败:", st, j)
        return 2
    TOKEN["v"] = j.get("token", "")

    # 面板要能自己调起 xray；公网地址填 127.0.0.1，本脚本就在同一台机器上连
    call(args.base, "PUT", "/api/settings", {"public_host": "127.0.0.1"})
    call(args.base, "PUT", "/api/settings", {"bin_xray": args.xray})

    st, kp = call(args.base, "POST", "/api/tools/reality-keypair", {})
    if st != 200 or not kp.get("private_key"):
        print("生成 REALITY 密钥对失败:", st, kp)
        return 2

    UUID = "11111111-2222-3333-4444-555555555555"
    cases = [
        {
            "label": "vless + REALITY（tcp）",
            "cred": UUID,
            "inbound": {
                "name": "re-01", "core": "xray", "protocol": "vless", "port": 24443,
                "transport": "tcp", "tls": "reality", "tag": "re01", "enabled": True,
                # 短 ID 故意给非空：空串时两端都空，测不出偏差
                "settings": json.dumps({
                    "handshake_server": "www.apple.com",
                    "server_names": ["www.apple.com"],
                    "short_ids": ["ab12cd34"],
                    "private_key": kp["private_key"],
                }),
            },
        },
        {
            "label": "vless + ws（无 TLS）",
            "cred": UUID,
            "inbound": {
                "name": "ws-01", "core": "xray", "protocol": "vless", "port": 24444,
                "transport": "ws", "tls": "none", "tag": "ws01", "enabled": True,
                "settings": json.dumps({"path": "/ray"}),
            },
        },
        {
            "label": "vmess + ws（无 TLS）",
            "cred": UUID,
            "inbound": {
                "name": "vm-01", "core": "xray", "protocol": "vmess", "port": 24445,
                "transport": "ws", "tls": "none", "tag": "vm01", "enabled": True,
                "settings": json.dumps({"path": "/vm"}),
            },
        },
        {
            "label": "shadowsocks",
            "cred": "verify-pass-123",
            "inbound": {
                "name": "ss-01", "core": "xray", "protocol": "shadowsocks", "port": 24446,
                "transport": "tcp", "tls": "none", "tag": "ss01", "enabled": True,
                "settings": json.dumps({"method": "aes-128-gcm"}),
            },
        },
        {
            "label": "vless + grpc（无 TLS）",
            "cred": UUID,
            "inbound": {
                "name": "gr-01", "core": "xray", "protocol": "vless", "port": 24447,
                "transport": "grpc", "tls": "none", "tag": "gr01", "enabled": True,
                "settings": json.dumps({"service_name": "gsvc"}),
            },
        },
    ]

    work_root = tempfile.mkdtemp(prefix="verify-mihomo-")
    try:
        for i, c in enumerate(cases):
            run_case(args, c, i, work_root)
    finally:
        if FAIL:
            print(f"\n（现场留在 {work_root}，每个用例一份 config.yaml 与 run.log）")
        else:
            shutil.rmtree(work_root, ignore_errors=True)

    print(f"\n结果：{PASS} 通过 / {FAIL} 失败")
    return 0 if FAIL == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
