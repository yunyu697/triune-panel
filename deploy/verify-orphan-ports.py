#!/usr/bin/env python3
"""面板重启/升级之后，入站端口会不会被「自己上一代的内核」占着？

这个脚本自己起停面板，走两条真实路径：

  A. 崩溃 / 被 SIGKILL（以及自更新走 syscall.Exec 的情形）：内核是面板的子进程，
     父进程没了它还活着，而内核状态只存在面板内存里——新面板不认识它。
     于是「应用配置」必然死在端口预检：`端口 X/TCP 已被其它程序占用（连接成功）`，
     而面板既停不掉它（内存里没有），也起不来新的，界面上完全无解。

  B. 面板还在跑，但那个内核被换成了它不认识的（手工起了一个、上一次 apply 中途
     被打断、外部 supervisor 又拉起来一个）：内存里那个早就死了，端口却被占着。
     这条走的是「应用配置」里的兜底。

用法：

    python3 deploy/verify-orphan-ports.py --bin ./triune-panel --xray /tmp/cores/xray

它会在临时目录里建数据、起面板、真跑内核，端口用完即删，可以反复跑。
退出码 0 表示两条路径都自愈成功。
"""

import argparse
import json
import os
import shutil
import signal
import socket
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
        print(f"PASS {name}")
    else:
        FAIL += 1
        print(f"FAIL {name}  {detail}")


class Panel:
    def __init__(self, bin_path, addr, data, log_path):
        self.bin, self.addr, self.data, self.log_path = bin_path, addr, data, log_path
        self.proc = None

    def start(self):
        log = open(self.log_path, "a")
        self.proc = subprocess.Popen(
            [self.bin, "serve", "-addr", self.addr, "-data", self.data],
            stdout=log, stderr=subprocess.STDOUT)
        for _ in range(80):
            try:
                with urllib.request.urlopen("http://" + self.addr + "/api/health", timeout=2):
                    return
            except Exception:
                time.sleep(0.5)
        raise SystemExit("面板没起来，见 " + self.log_path)

    def stop(self):
        if self.proc and self.proc.poll() is None:
            self.proc.send_signal(signal.SIGTERM)
            try:
                self.proc.wait(timeout=8)
            except subprocess.TimeoutExpired:
                self.proc.kill()


def call(addr, method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request("http://" + addr + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if TOKEN["v"]:
        req.add_header("Authorization", "Bearer " + TOKEN["v"])
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            b = r.read()
            return r.status, (json.loads(b) if b else None)
    except urllib.error.HTTPError as e:
        b = e.read()
        try:
            return e.code, json.loads(b)
        except Exception:
            return e.code, b.decode("utf-8", "replace")


def login(addr, user, password):
    st, j = call(addr, "POST", "/api/login", {"user": user, "pass": password})
    if st == 200:
        TOKEN["v"] = j["token"]
        return True
    return False


def port_open(port):
    with socket.socket() as s:
        s.settimeout(0.7)
        return s.connect_ex(("127.0.0.1", port)) == 0


def core_pids(xray, data):
    """跑着我们的 xray、配置指向本数据目录的进程。

    用 pgrep 而不是 ps：命令行匹配足够精确，且 ps 在某些受限环境里不可用。
    """
    r = subprocess.run(["pgrep", "-f", f"{xray} run -c {data}"],
                       capture_output=True, text=True)
    return [int(x) for x in r.stdout.split() if x.strip()]


def main():
    ap = argparse.ArgumentParser(description="验证面板重启后能自己清掉残留内核、夺回入站端口")
    ap.add_argument("--bin", default="./triune-panel", help="要测的面板二进制")
    ap.add_argument("--xray", required=True, help="xray 二进制（面板与脚本共用）")
    ap.add_argument("--addr", default="127.0.0.1:18098", help="面板监听地址")
    ap.add_argument("--port", type=int, default=24551, help="测试入站端口")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--pass", dest="password", default="admin")
    ap.add_argument("--keep", action="store_true", help="保留数据目录（排查用）")
    a = ap.parse_args()

    if not (os.path.isfile(a.bin) and os.access(a.bin, os.X_OK)):
        print(f"面板二进制不可执行: {a.bin}")
        return 2
    if not (os.path.isfile(a.xray) and os.access(a.xray, os.X_OK)):
        print(f"xray 不可执行: {a.xray}")
        return 2

    data = tempfile.mkdtemp(prefix="verify-orphan-")
    log_path = os.path.join(data, "panel.log")
    panel = Panel(a.bin, a.addr, data, log_path)
    iid = None
    manual = None
    try:
        panel.start()
        if not login(a.addr, a.user, a.password):
            print("登录失败")
            return 2
        call(a.addr, "PUT", "/api/settings", {"bin_xray": a.xray})

        st, inb = call(a.addr, "POST", "/api/inbounds", {
            "name": "orphan", "core": "xray", "protocol": "vless",
            "listen": "127.0.0.1", "port": a.port, "transport": "tcp",
            "tls": "none", "tag": "orphan", "enabled": True, "settings": "{}",
        })
        if st != 200:
            print("建入站失败:", st, inb)
            return 1
        iid = inb["id"]
        st, _ = call(a.addr, "POST", "/api/clients", {
            "name": "u1", "credential": "11111111-2222-3333-4444-555555555555",
            "enabled": True, "inbound_ids": [iid],
        })
        if st != 200:
            print("建用户失败:", st)
            return 1

        st, ap_ = call(a.addr, "POST", "/api/apply", {"core": "xray"})
        ck("首次应用成功", st == 200 and ap_.get("restarted") is True, f"{st} {ap_}")
        time.sleep(1)
        ck("内核在监听端口", port_open(a.port))
        before = core_pids(a.xray, data)
        ck("只有 1 个内核进程", len(before) == 1, f"pids={before}")

        # ---- 路径 A：面板被直接干掉（崩溃 / SIGKILL；exec 自更新同理） ----
        panel.proc.send_signal(signal.SIGKILL)
        panel.proc.wait(timeout=10)
        time.sleep(1.5)
        ck("面板已死", panel.proc.poll() is not None)
        ck("内核进程仍在（成了孤儿）", len(core_pids(a.xray, data)) == 1,
           f"pids={core_pids(a.xray, data)}")
        ck("端口仍被孤儿占着", port_open(a.port))

        TOKEN["v"] = ""
        panel.start()
        if not login(a.addr, a.user, a.password):
            print("重启后登录失败")
            return 1
        time.sleep(3)  # 给启动时的恢复流程一点时间
        after = core_pids(a.xray, data)
        ck("清理后仍只有 1 个内核进程（不是 2 个）", len(after) == 1, f"pids={after}")

        st, ap_ = call(a.addr, "POST", "/api/apply", {"core": "xray"})
        ck("重启后应用配置成功", st == 200 and ap_.get("restarted") is True,
           f"{st} {str(ap_)[:220]}")
        time.sleep(1)
        ck("端口仍可用", port_open(a.port))
        ck("最终只有 1 个内核进程", len(core_pids(a.xray, data)) == 1,
           f"pids={core_pids(a.xray, data)}")

        # ---- 路径 B：面板在跑，但内核被换成了它不认识的一个 ----
        pids = core_pids(a.xray, data)
        ck("路径 B 前提：有一个在跑的内核", len(pids) == 1, f"pids={pids}")
        if pids:
            cfg = os.path.join(data, "cores", "xray", "config.json")
            os.kill(pids[0], signal.SIGKILL)
            time.sleep(0.8)
            manual = subprocess.Popen([a.xray, "run", "-c", cfg],
                                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            time.sleep(1.5)
            ck("手工起的进程占住了端口（面板内存里没有它）", port_open(a.port))
            st, ap_ = call(a.addr, "POST", "/api/apply", {"core": "xray"})
            ck("应用配置时能自己认出并清掉，然后成功",
               st == 200 and ap_.get("restarted") is True, f"{st} {str(ap_)[:220]}")
            try:
                manual.kill()
            except Exception:
                pass
            manual = None
            time.sleep(1)
            ck("路径 B 结束后只有 1 个内核进程", len(core_pids(a.xray, data)) == 1,
               f"pids={core_pids(a.xray, data)}")
    finally:
        if iid is not None:
            try:
                call(a.addr, "DELETE", f"/api/inbounds/{iid}")
            except Exception:
                pass
        panel.stop()
        if manual is not None:
            try:
                manual.kill()
            except Exception:
                pass
        for pid in core_pids(a.xray, data):
            try:
                os.kill(pid, signal.SIGKILL)
            except Exception:
                pass
        if a.keep:
            print(f"\n（数据与日志留在 {data}）")
        else:
            shutil.rmtree(data, ignore_errors=True)

    print(f"\n结果：{PASS} 通过 / {FAIL} 失败")
    return 0 if FAIL == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
