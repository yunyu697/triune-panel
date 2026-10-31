// Package firewall 提供入站端口的运行时防火墙管理。
//
// 设计目标：新增入站时自动放行其端口，删除/改端口入站时回收旧端口。
// 探测逻辑与 deploy/install.sh 的 ensure_port_open 对齐：优先 firewalld（running）
// → ufw（active）→ iptables（INPUT 默认 DROP/REJECT 才需要），三者皆无则视为
// 「系统默认放行」，此时 OpenPort/ClosePort 直接返回 nil（不执行任何命令），
// 因此在无对应防火墙的机器（如本机开发环境、云主机默认放行）上完全无副作用。
//
// 注意：云厂商的安全组在机器之外，本包无法代改，只能管机器内防火墙；
// 若 DetectBackend 返回 none，意味着本机无需额外放行（或仅安全组层面控制）。
package firewall

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Backend 防火墙后端类型。
const (
	BackendFirewalld = "firewalld"
	BackendUFW       = "ufw"
	BackendIptables  = "iptables"
	BackendNone      = "none"
)

// ProtoOf 根据协议/传输推断端口协议：hysteria2、tuic 以及 quic 传输走 UDP，其余 TCP。
func ProtoOf(protocol, transport string) string {
	if protocol == "hysteria2" || protocol == "tuic" || transport == "quic" {
		return "udp"
	}
	return "tcp"
}

// DetectBackend 探测当前系统生效（或最可能生效）的防火墙后端。
func DetectBackend() string {
	if b, err := exec.LookPath("firewall-cmd"); err == nil {
		if out, e := exec.Command(b, "--state").CombinedOutput(); e == nil &&
			strings.Contains(strings.ToLower(string(out)), "running") {
			return BackendFirewalld
		}
	}
	if b, err := exec.LookPath("ufw"); err == nil {
		if out, e := exec.Command(b, "status").CombinedOutput(); e == nil &&
			strings.Contains(strings.ToLower(string(out)), "status: active") {
			return BackendUFW
		}
	}
	if b, err := exec.LookPath("iptables"); err == nil {
		if out, e := exec.Command(b, "-S", "INPUT").CombinedOutput(); e == nil {
			if strings.Contains(string(out), "-P INPUT DROP") ||
				strings.Contains(string(out), "-P INPUT REJECT") {
				return BackendIptables
			}
		}
	}
	return BackendNone
}

// OpenPort 在生效防火墙放行指定端口/协议，幂等（已放行则跳过）。
// 无可用防火墙后端时返回 nil（无需操作）。
func OpenPort(port int, proto string) error {
	backend := DetectBackend()
	if backend == BackendNone {
		return nil
	}
	p := strconv.Itoa(port)
	switch backend {
	case BackendFirewalld:
		if out, e := exec.Command("firewall-cmd", "--query-port", p+"/"+proto).CombinedOutput(); e == nil &&
			strings.Contains(string(out), "yes") {
			return nil // 已放行
		}
		if out, e := exec.Command("firewall-cmd", "--permanent", "--add-port", p+"/"+proto).CombinedOutput(); e != nil {
			return fmt.Errorf("firewalld add-port: %s (%s)", e, strings.TrimSpace(string(out)))
		}
		if out, e := exec.Command("firewall-cmd", "--reload").CombinedOutput(); e != nil {
			return fmt.Errorf("firewalld reload: %s (%s)", e, strings.TrimSpace(string(out)))
		}
	case BackendUFW:
		// ufw allow 幂等：重复 allow 不会产生重复规则
		if out, e := exec.Command("ufw", "allow", p+"/"+proto).CombinedOutput(); e != nil {
			return fmt.Errorf("ufw allow: %s (%s)", e, strings.TrimSpace(string(out)))
		}
	case BackendIptables:
		// 仅当规则不存在时插入（避免重复）
		match := []string{"INPUT", "-p", proto, "--dport", p, "-j", "ACCEPT"}
		if _, e := exec.Command("iptables", append([]string{"-C"}, match...)...).CombinedOutput(); e != nil {
			if out2, e2 := exec.Command("iptables", append([]string{"-I"}, match...)...).CombinedOutput(); e2 != nil {
				return fmt.Errorf("iptables insert: %s (%s)", e2, strings.TrimSpace(string(out2)))
			}
		}
	}
	return nil
}

// ClosePort 在生效防火墙回收指定端口/协议，幂等（未放行则跳过）。
// 调用方需先确认该端口/协议不再被其它入站使用——本函数仅负责防火墙规则本身。
// 注意：ufw/firewalld 是按端口删除该协议的全部规则，若已确认无其他入站占用则安全；
// iptables 按精确规则删除（仅删我们这类 ACCEPT 规则）。
func ClosePort(port int, proto string) error {
	backend := DetectBackend()
	if backend == BackendNone {
		return nil
	}
	p := strconv.Itoa(port)
	switch backend {
	case BackendFirewalld:
		if out, e := exec.Command("firewall-cmd", "--query-port", p+"/"+proto).CombinedOutput(); e != nil ||
			!strings.Contains(string(out), "yes") {
			return nil // 未放行，跳过
		}
		if out, e := exec.Command("firewall-cmd", "--permanent", "--remove-port", p+"/"+proto).CombinedOutput(); e != nil {
			return fmt.Errorf("firewalld remove-port: %s (%s)", e, strings.TrimSpace(string(out)))
		}
		exec.Command("firewall-cmd", "--reload").CombinedOutput()
	case BackendUFW:
		// ufw delete allow 幂等：规则不存在时仅打印提示，无副作用
		exec.Command("ufw", "delete", "allow", p+"/"+proto).CombinedOutput()
	case BackendIptables:
		match := []string{"INPUT", "-p", proto, "--dport", p, "-j", "ACCEPT"}
		if out, e := exec.Command("iptables", append([]string{"-D"}, match...)...).CombinedOutput(); e != nil {
			_ = out // 规则不存在时 -D 失败，忽略
		}
	}
	return nil
}
