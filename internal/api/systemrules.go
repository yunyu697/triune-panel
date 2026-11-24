package api

// 系统规则的「人可读视图」：体检脚本、覆盖提示、漂移快照三件事都从这里派生。
//
// 清单本身（systemDirectRules 等）是平铺的规则行——适合渲染，不适合回答
// 「这一类还缺不缺」「这条规则在真机上生效了吗」。把「类」抽成结构体之后：
//
//   - 体检脚本按类生成探针，用户在自己设备上跑一次就知道分流对不对；
//   - 生成页按类提示「由哪个规则集覆盖 / 是否只有系统清单兜底」；
//   - 不变量测试按类断言，整类被删会红（而不是只删一个域名时静默通过）。
//
// 这是 v2.0.7「按类维护」的延伸：那次管的是生成器，这次管的是验证与提示。
// 新增一条规则时只要在对应类里加一行，三处自动跟上。

import (
	"encoding/json"
	"fmt"
	"strings"
)

// systemClass 一组「会一起失效」的系统规则。
//
// Expect 决定体检时什么算通过：直连类必须解析到真实 IP（fake-ip 说明流量
// 进了代理），代理类必须解析到 fake-ip（真实 IP 说明规则没生效）。
type systemClass struct {
	Key    string   `json:"key"`    // 稳定标识，前端与脚本都用它，别拿中文名做键
	Name   string   `json:"name"`   // 展示名
	Expect string   `json:"expect"` // direct / proxy / port
	Probes []string `json:"probes"` // 代表域名：第一个用于体检，全部用于断言
	URL    string   `json:"url"`    // 体检用的轻量 URL（空 = 只查解析归属）
	Hint   string   `json:"hint"`   // 体检不通过时告诉用户会坏在哪
	Sets   []string `json:"sets"`   // 能覆盖这一类的规则集 id；空 = 只有系统清单兜底
}

var systemClasses = []systemClass{
	{
		Key: "apple-update", Name: "Apple 系统更新 / 恢复", Expect: "direct",
		Probes: []string{"mesu.apple.com", "swcdn.apple.com"},
		URL:    "https://mesu.apple.com/",
		Hint:   "检查更新会一直转圈，或下到一半校验失败；软恢复不可用",
		Sets:   []string{"geosite-apple"},
	},
	{
		Key: "apple-ocsp", Name: "Apple 证书校验（OCSP）", Expect: "direct",
		Probes: []string{"ocsp.apple.com", "ocsp2.apple.com"},
		URL:    "http://ocsp.apple.com/",
		Hint:   "装 App 或更新时卡在「正在验证」，或直接报「无法验证」",
		Sets:   []string{"geosite-apple"},
	},
	{
		Key: "apple-loc", Name: "Apple 定位与区域判定", Expect: "direct",
		Probes: []string{"gs-loc.apple.com", "gsp-ssl.ls.apple.com"},
		URL:    "https://gs-loc.apple.com/",
		Hint:   "「查找我的」定位漂到国外，时区与 App Store 区域判定跟着错",
		Sets:   []string{"geosite-apple"},
	},
	{
		Key: "apple-id", Name: "Apple ID 登录与激活", Expect: "direct",
		Probes: []string{"appleid.apple.com", "idmsa.apple.com"},
		URL:    "https://appleid.apple.com/",
		Hint:   "登录被判异地，触发二次验证甚至锁账号",
		Sets:   []string{"geosite-apple"},
	},
	{
		Key: "apple-push", Name: "Apple 推送（APNs）", Expect: "direct",
		// 裸 push.apple.com 不解析，真实主机形如 7-courier2.push.apple.com，
		// 探针必须用能解析的那个，否则体检会报「解析失败」，看起来像规则坏了。
		Probes: []string{"courier.push.apple.com"},
		// APNs 不说 HTTP：只查解析归属，不发 HTTP 请求。
		URL:  "",
		Hint: "推送延迟高、反复断连重连，iPhone / iPad 待机耗电明显加快",
		Sets: []string{"geosite-apple"},
	},
	{
		Key: "ms-login", Name: "Microsoft 账号登录", Expect: "direct",
		Probes: []string{"login.live.com", "login.microsoftonline.com"},
		URL:    "https://login.live.com/",
		Hint:   "OneDrive / Office / Teams 登不上，或反复要求重新登录",
		Sets:   []string{"geosite-microsoft", "geosite-onedrive"},
	},
	{
		Key: "win-update", Name: "Windows 更新 / 激活", Expect: "direct",
		// 裸 windowsupdate.com 没有 A 记录（实测 NXDOMAIN），必须用真实子域，
		// 否则体检会报「解析失败」，看起来像规则坏了。
		Probes: []string{"download.windowsupdate.com", "activation.sls.microsoft.com"},
		URL:    "https://windowsupdate.microsoft.com/",
		Hint:   "更新龟速或报 0x8024402c 一类错误；激活提示「环境异常」",
		Sets:   []string{"geosite-microsoft"},
	},
	{
		Key: "connectivity", Name: "系统连通性检测（NCSI）", Expect: "direct",
		// 同理：裸 msftconnecttest.com 不解析，Windows 实际访问的是 www. 前缀。
		Probes: []string{"www.msftconnecttest.com", "captive.apple.com"},
		URL:    "http://www.msftconnecttest.com/connecttest.txt",
		Hint:   "系统误判「无网络」，反复重连、Wi-Fi 标记为已连接但无法访问",
		Sets:   nil,
	},
	{
		Key: "mediafire", Name: "MediaFire（内置直连）", Expect: "direct",
		Probes: []string{"mediafire.com"},
		URL:    "https://www.mediafire.com/",
		Hint:   "下载站与文件节点都该直连；进代理会明显变慢",
		Sets:   nil,
	},
	{
		Key: "github", Name: "GitHub 全系（内置代理）", Expect: "proxy",
		Probes: []string{"github.com", "codeload.github.com"},
		URL:    "https://github.com/",
		Hint:   "直连时握手超时：网页打不开、git push 连不上",
		Sets:   nil,
	},
	{
		Key: "ntp", Name: "时间同步（NTP 按端口直连）", Expect: "port",
		Hint: "时间漂移会让 TLS 证书随机报「已过期 / 无效」，且极难归因",
	},
}

// ruleListMatches 判断某个域名是否被清单里的规则命中（只认 DOMAIN 与
// DOMAIN-SUFFIX，这正是清单允许的两种类型）。
//
// 用后缀语义而不是字符串相等：清单写 DOMAIN-SUFFIX,ls.apple.com 时，
// gsp-ssl.ls.apple.com 也必须算命中，否则「探针在清单里」这类断言会误报。
func ruleListMatches(rules []string, policy, domain string) bool {
	for _, line := range rules {
		typ, val, pol, ok := parseCustomRule(line)
		if !ok {
			continue
		}
		// 用解析出来的策略比较，而不是看行尾后缀：带 no-resolve 的规则行尾
		// 不是策略，后缀判断会把它整条漏掉。
		if policy != "" && pol != policy {
			continue
		}
		switch typ {
		case "DOMAIN":
			if strings.EqualFold(val, domain) {
				return true
			}
		case "DOMAIN-SUFFIX":
			if strings.EqualFold(val, domain) || strings.HasSuffix(strings.ToLower(domain), "."+strings.ToLower(val)) {
				return true
			}
		}
	}
	return false
}

// systemRuleSnapshot 当前二进制内置的整套系统规则，一条一行、原文保留。
//
// 存进「保存的配置」里，用来事后回答「这份配置比现在少了哪几条」。
// 只放会影响分流结果的东西：端口与私网段也各有策略，所以一并收进来——
// 少了 100.64.0.0/10 这种变化，用户同样会撞上奇怪的现象。
func systemRuleSnapshot() []string {
	out := make([]string, 0,
		len(blockExemptRules)+len(systemDirectRules)+len(systemProxyDomains)+len(systemProxyCIDRs)+len(privateCIDRs)+len(systemDirectPorts))
	// 拦截豁免也算「影响分流结果」：老配置少了这两条，勾着广告拦截时
	// 还是会踩到「App 首屏卡死」，所以必须让它在漂移里显示出来。
	out = append(out, blockExemptRules...)
	out = append(out, systemDirectRules...)
	out = append(out, systemProxyDomains...)
	for _, c := range systemProxyCIDRs {
		out = append(out, "IP-CIDR,"+c+",PROXY")
	}
	for _, c := range privateCIDRs {
		out = append(out, "IP-CIDR,"+c+",DIRECT")
	}
	for _, p := range systemDirectPorts {
		out = append(out, fmt.Sprintf("PORT,%d,DIRECT", p))
	}
	return out
}

// diffRuleSnapshot 按集合求差，保持各自原有顺序（配置文件是给人看的，
// 顺序乱了「新增 3 条」就不好核对）。
func diffRuleSnapshot(old, cur []string) (added, removed []string) {
	inOld := make(map[string]bool, len(old))
	for _, r := range old {
		inOld[r] = true
	}
	inCur := make(map[string]bool, len(cur))
	for _, r := range cur {
		inCur[r] = true
	}
	for _, r := range cur {
		if !inOld[r] {
			added = append(added, r)
		}
	}
	for _, r := range old {
		if !inCur[r] {
			removed = append(removed, r)
		}
	}
	return added, removed
}

// shq 把字符串包成 shell 单引号字面量。
// 探针里有域名与中文，理论上不含单引号，但一旦含了就会把脚本拆坏，
// 所以按标准写法转义，不赌输入。
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// healthCheckScript 生成在设备上直接跑的规则体检脚本。
//
// 为什么由后端生成而不是前端拼：探针清单与类别必须与生成器同源。
// 前端拼的话，改了 systemDirectRules 而忘了改 JS，脚本会去测一条早就
// 不在清单里的域名，通过与否都没有意义。
func healthCheckScript() string {
	var cases strings.Builder
	for _, c := range systemClasses {
		if c.Expect == "port" || len(c.Probes) == 0 {
			continue
		}
		for i, p := range c.Probes {
			url := ""
			if i == 0 {
				url = c.URL // 每个类只对第一个探针发 HTTP，避免把一个类测成 20 次请求
			}
			fmt.Fprintf(&cases, "run_case %s %s %s %s %s\n",
				shq(c.Name), shq(c.Expect), shq(p), shq(url), shq(c.Hint))
		}
	}
	out := strings.ReplaceAll(healthCheckTmpl, "@VERSION@", Version)
	return strings.ReplaceAll(out, "@CASES@", strings.TrimRight(cases.String(), "\n"))
}

// healthCheckTmpl 体检脚本主体。POSIX sh + busybox 兼容：
// OpenWrt 上不一定有 dig（bind-dig 是可选包），所以 nslookup 必须能用；
// curl 缺失时退回 wget。两者都没有就明说测不了，不猜。
const healthCheckTmpl = `#!/bin/sh
# ───────────────────────────────────────────────────────────────
# triune-panel 规则体检脚本
# 生成于面板 @VERSION@
#
# 做什么：逐个关键类别解析域名，看它被分到直连还是代理，再验一下能不能连通。
#
# 判定依据是 fake-ip：返回 198.18 / 198.19 段说明流量进了代理（面板生成的
# 配置用 198.18.0.1/16，momo 目标用 198.18.0.0/15，两者都覆盖），返回真实 IP
# 说明命中了强制直连——强制直连的域名必须被排除在 fake-ip 之外，否则拿不到
# 真实地址、也连不上。
#
# 在哪跑：**跑这份配置的那台设备上**（旁路由 / 软路由 / 本机）。在别的机器上
#         跑，测的是那台机器的 DNS，不是这份配置。
#
# 用法：
#   sh tp-check.sh              # 用 /etc/resolv.conf 里的 DNS
#   sh tp-check.sh 10.1.1.1     # 指定 DNS（旁路由地址）
#
# 退出码：0 = 全部通过，1 = 有异常项
# ───────────────────────────────────────────────────────────────

DNS=$1
PASS=0
FAIL=0
TOTAL=0

if [ -z "$DNS" ] && [ -r /etc/resolv.conf ]; then
  DNS=$(awk '/^nameserver/{print $2; exit}' /etc/resolv.conf)
fi

echo "规则体检 · 面板 @VERSION@"
if [ -n "$DNS" ]; then
  echo "DNS 服务器：$DNS"
else
  echo "DNS 服务器：系统默认"
fi
echo ""

if ! command -v dig >/dev/null 2>&1 && ! command -v nslookup >/dev/null 2>&1; then
  echo "找不到 dig 也找不到 nslookup，无法解析域名。"
  echo "OpenWrt 上装一下：opkg update && opkg install bind-dig"
  exit 2
fi

is_fake() {
  case $1 in
    198.18.*|198.19.*) return 0 ;;
  esac
  return 1
}

resolve() {
  _d=$1
  if command -v dig >/dev/null 2>&1; then
    if [ -n "$DNS" ]; then
      dig +short +time=3 +tries=2 A "$_d" @"$DNS" 2>/dev/null | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | head -1
    else
      dig +short +time=3 +tries=2 A "$_d" 2>/dev/null | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | head -1
    fi
    return
  fi
  # busybox nslookup：Server 段与 Name 段各有一行 Address，取最后一行才是解析结果
  if [ -n "$DNS" ]; then
    _out=$(nslookup "$_d" "$DNS" 2>/dev/null)
  else
    _out=$(nslookup "$_d" 2>/dev/null)
  fi
  echo "$_out" | awk '/^Address/{print $2}' | sed 's/:53$//' \
    | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | tail -1
}

http_code() {
  if command -v curl >/dev/null 2>&1; then
    curl -sS -o /dev/null --connect-timeout 8 --max-time 20 -w '%{http_code}' "$1" 2>/dev/null
  elif command -v wget >/dev/null 2>&1; then
    if wget -q -T 20 -O /dev/null "$1" 2>/dev/null; then echo 200; else echo 000; fi
  else
    echo -
  fi
}

run_case() {
  _name=$1
  _expect=$2
  _dom=$3
  _url=$4
  _hint=$5
  TOTAL=$((TOTAL+1))

  _ip=$(resolve "$_dom")
  if [ -z "$_ip" ]; then
    echo "  [✗] $_name · $_dom → 解析失败"
    echo "      ↳ $_hint"
    FAIL=$((FAIL+1))
    return
  fi

  if is_fake "$_ip"; then _got=proxy; else _got=direct; fi
  _bad=0
  if [ "$_got" != "$_expect" ]; then _bad=1; fi

  if [ "$_got" = "proxy" ]; then _got_txt="进了代理"; else _got_txt="直连"; fi

  _http=""
  if [ -n "$_url" ]; then
    _code=$(http_code "$_url")
    case $_code in
      000|-) _http=" · HTTP 不通"; _bad=1 ;;
      *)     _http=" · HTTP $_code" ;;
    esac
  fi

  if [ "$_bad" = "1" ]; then
    if [ "$_got" != "$_expect" ]; then
      if [ "$_expect" = "direct" ]; then
        _why="应直连，实际进了代理"
      else
        _why="应走代理，实际直连"
      fi
      echo "  [✗] $_name · $_dom → ${_ip}（${_why}）$_http"
    else
      echo "  [✗] $_name · $_dom → ${_ip}（${_got_txt}）$_http"
    fi
    echo "      ↳ $_hint"
    FAIL=$((FAIL+1))
  else
    echo "  [✓] $_name · $_dom → ${_ip}（${_got_txt}）$_http"
    PASS=$((PASS+1))
  fi
}

echo "分流归属"
@CASES@

echo ""
echo "时间同步"
# NTP 按端口直连，没有域名可查，只能比时间。工具缺失就明说，不给假结论。
_off=""
if command -v ntpdate >/dev/null 2>&1; then
  _off=$(ntpdate -q pool.ntp.org 2>/dev/null | awk '/offset/{gsub(/,/,""); print $NF; exit}')
elif command -v sntp >/dev/null 2>&1; then
  # macOS 的 sntp 先打一行表头（sntp_exchange ...），只认形如 +0.001234 的字段
  _off=$(sntp -t 5 pool.ntp.org 2>/dev/null | awk '$1 ~ /^[+-][0-9]+\.[0-9]+$/{print $1; exit}')
fi
if [ -z "$_off" ]; then
  echo "  [–] 跳过：没装 ntpdate / sntp，无法自动比对"
  echo "      ↳ 手动看：date -u 与本机实际时间差超过 5 分钟，说明 NTP 没走直连"
else
  _abs=$(echo "$_off" | tr -d '+-')
  _int=${_abs%%.*}
  if [ -z "$_int" ]; then _int=0; fi
  if [ "$_int" -lt 5 ]; then
    echo "  [✓] 与 pool.ntp.org 偏差 ${_abs} 秒"
    PASS=$((PASS+1))
  else
    echo "  [✗] 与 pool.ntp.org 偏差 ${_abs} 秒（超过 5 秒）"
    echo "      ↳ 时间漂移会让 TLS 证书随机报「已过期 / 无效」，且极难归因"
    FAIL=$((FAIL+1))
  fi
  TOTAL=$((TOTAL+1))
fi

echo ""
echo "───────────────────────────────────────────────────────────────"
if [ "$FAIL" = "0" ]; then
  echo "全部通过：$PASS 项"
  echo ""
  echo "如果设备上刚换过配置、这里有项不通过，先清一次 DNS 缓存再跑："
  echo "  macOS：sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder"
  echo "  Windows：ipconfig /flushdns"
  echo "  路由器：/etc/init.d/dnsmasq restart"
  exit 0
fi
echo "有 $FAIL 项异常（通过 $PASS 项，共 $TOTAL 项）"
echo "把这段输出发给维护者，能直接定位到是哪一类规则没生效。"
exit 1
`

// systemRuleSnapshotJSON 快照的落库形态。编码失败只会是内存问题，
// 返回空串即「未知」，下游按「建议重新生成」处理，不阻断保存。
func systemRuleSnapshotJSON() string {
	b, err := json.Marshal(systemRuleSnapshot())
	if err != nil {
		return ""
	}
	return string(b)
}

// configDriftView 列表页要的漂移摘要：只说「落后几条」，不搬整份清单。
type configDriftView struct {
	Stale          bool   `json:"stale"`
	Unknown        bool   `json:"unknown"`
	PanelVersion   string `json:"panel_version"`
	CurrentVersion string `json:"current_version"`
	AddedCount     int    `json:"added_count"`
	RemovedCount   int    `json:"removed_count"`
}

// configDriftDetail 详情页要的漂移明细（规则行原文）。
type configDriftDetail struct {
	configDriftView
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

// driftOf 算出某份配置相对当前二进制的规则漂移。
//
// 快照为空说明是旧库里的记录（加字段之前保存的）。这种情况**不假装它是最新的**：
// Unknown=true，界面按「建议重新生成」提示，用户点一下就能拿到新配置。
func driftOf(snapshot string) configDriftDetail {
	d := configDriftDetail{configDriftView: configDriftView{CurrentVersion: Version}}
	d.Added, d.Removed = []string{}, []string{}
	if strings.TrimSpace(snapshot) == "" {
		d.Unknown = true
		d.Stale = true
		return d
	}
	var old []string
	if err := json.Unmarshal([]byte(snapshot), &old); err != nil {
		d.Unknown = true
		d.Stale = true
		return d
	}
	added, removed := diffRuleSnapshot(old, systemRuleSnapshot())
	d.Added, d.Removed = added, removed
	d.AddedCount, d.RemovedCount = len(added), len(removed)
	d.Stale = len(added) > 0 || len(removed) > 0
	return d
}
