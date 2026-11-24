# 已知限制登记册

发布门禁（`deploy/preflight.sh` 第 7 项）读这份表，不读 README。规则只有一条：

> **允许带已登记的已知限制发布，不允许带未登记的缺陷发布。**

所以这里的每一条都必须写清「影响什么」和「怎么验」——写不出来的，说明它还没被
想清楚，状态就只能是 `未决`，而 `未决` 会直接阻断发布。

README 的[已知限制与未验证项](../README.md#已知限制与未验证项)是面向使用者的完整说明
（含使用建议与自测前提），这里只收**会影响产物能否正常启动/工作**的条目，两者不是
互相替代关系：新增条目先在这里登记 ID，README 再展开解释。

## 状态含义

| 状态 | 含义 | 对发布的影响 |
|---|---|---|
| `未验证` | 已登记、已知影响范围、接受风险发布 | 不阻断，发布时逐条打印 |
| `已验证` | 已在真实环境跑通，链接见「验证方式」 | 不阻断 |
| `已缓解` | 有兜底或降级路径，风险可控 | 不阻断 |
| `未决` | 影响或验证方式写不清 | **阻断** |

## 登记表

| ID | 条目 | 影响 | 状态 | 验证方式 | 登记于 |
|---|---|---|---|---|---|
| KL-01 | momo 产物的 Profile Check 与 OpenWrt 真机启动未验证 | momo 自己会按 UCI 的 `tcp_mode`/`udp_mode` 核对入站 tag，与面板无关；对不上时 momo 直接退出，表现为「导入成功但内核不起来」 | 未验证 | 在 OpenWrt 24.10+ 导入产物，看 LuCI → Momo 的 Profile Check 输出与内核日志 | v2.1.0 |
| KL-02 | ACME 真实签发（HTTP-01 / DNS-01）未对 Let's Encrypt 跑通全流程 | 证书签发可能失败在 DNS 传播、80 端口占用、凭据权限等环节；失败时设置页会显示 lego 原始错误 | 未验证 | 用一个真实域名走一次「签发 → 到期续期 → 生效」，DNS-01 需域名的 NS 在该服务商 | v2.0.0 |
| KL-03 | 面板 HTTPS 与 ACME 证书是两套，改造已暂缓 | DNS-01 签下的证书只给入站用；面板自身仍是自签证书，`https://域名:端口` 会警告或打不开 | 未验证 | 见 `.workbuddy/memory` 的分期方案（P0 证书来源三分 + `GetCertificate` 热加载），重启该工作前先读方案，不要重新调研 | v2.0.0 |
| KL-04 | sing-box / mihomo 的每用户流量是「按入站归属 + 存活连接采样」 | 多用户共用入站时无法区分归属；存活短于 5 秒采样周期的连接会漏计。内核接口的结构性限制，非面板 bug | 已缓解 | 精确计费场景改用 xray（`StatsService` 累计计数）；UI 与 README 已写明精确到哪一步 | v2.0.4 |
| KL-05 | systemd 加固项（`NoNewPrivileges`/`ProtectSystem`/`ReadWritePaths`）未在真实 systemd 上逐条验证 | 可能出现「能启动但写配置失败」「防火墙不放行」「自更新失败」；最常见是安装目录不在 `ReadWritePaths` | 未验证 | Linux 上安装后跑一次「应用配置 + 更新组件 + 备份」，异常按 service 文件注释逐条放开 | v1.6.0 |
| KL-06 | 三类产物的真内核启动验证只在开发机（macOS arm64）做过 | Linux 上的内核行为差异（发行版 glibc、无 `auto_detect_interface` 所需权限）未覆盖 | 未验证 | 在目标发行版上跑 `deploy/preflight.sh`，或手动 `sing-box run -c <产物>` / `mihomo -d <dir> -f <产物>` 后看进程能否存活 | v2.1.0 |
| KL-07 | 需要特权的入站（TUN / redirect / tproxy / 绑定 443）未纳入自动启动验证 | 这些入站非 root 起不来，自动验证只能覆盖 mixed / socks 这类用户态入站；TUN 的开机自启行为只能真机验 | 未验证 | 用 root 在目标机跑一次完整产物，确认 TUN 建立、路由表生效、DNS 劫持正常 | v2.1.0 |
| KL-08 | sing-box 服务端产物曾只做到 `check`（`check` 不 bind 端口、不读证书、不初始化 DNS） | 校验通过但启动失败的情况不会在门禁里暴露——v2.1.0 的 DNS detour 事故就是这个形状，当时只有客户端侧补了防线 | 已验证 | `TestSingboxServerConfigStarts`：真内核 `run` + 3 秒判活，覆盖 socks / vless-vision-tls / reality / 注入 DNS 四类配置。变异（证书缺失、`detour` 指向空 direct）均在 0.06s 报出 FATAL；同一配置走 `check` 不报，证明这条防线不可由 check 替代。特权入站（TUN/redirect/tproxy）仍见 KL-07 | v2.1.0 → 2026-11-18 已验证 |
| KL-09 | xray 启动判活只覆盖 socks / vless-reality / vmess-ws-tls 三类用户态入站 | wireguard、dokodemo-door、kcp 等未纳入自动用例；它们要么需要特定运行环境，要么依赖对端 | 未验证 | 在目标机上手工跑一次完整入站组合：`xray run -c <产物>` 后看进程存活与端口监听 | v2.1.0 |
| KL-10 | sing-box 1.14 起 `rule_set[].download_detour` 已 deprecated，官方注明 1.16 移除 | 现在仍可用，只在日志里刷一条 WARN。1.16 正式版发布后，面板生成的客户端配置会因 `unknown field` 直接 FATAL——所有用 1.16 的用户都起不来，且是确定性失败而非偶发 | 未验证 | 1.16 发布后用 `sing-box check -c <产物>` 跑一遍；迁移目标见官方 1.14 迁移文档（remote rule-set 改走 `http_client` / `route.default_http_client`）。实测 1.14.4 已打印该 WARN | v2.1.2 |
| KL-11 | mihomo 产物的规则集统一用 `format: mrs`（二进制），要求 mihomo 1.18 或更高 | 旧内核不会「退回按文本解析」，而是报 format 不支持：表现为「配置导入成功，但分流失效或内核起不来」，而配置每一行都没错。面板不探测客户端内核版本，无法提前拦截 | 未验证 | 产物头部已写明下限。验证：拿 mihomo 1.17 实例跑 `mihomo -t -f <产物>`，应报 format 不支持；1.18+ 应通过。上游文档对 mrs 的引入点表述不一（1.17 尾号 / 1.18 系列都有出现），版本下限按「1.18 起确定可用」表述 | v2.1.3 |
| KL-12 | 直连清单里的域名被排除出 fake-ip，域名只能靠嗅探补回，而嗅探只认 HTTP / TLS / QUIC 三种协议 | 某个直连服务若用私有二进制协议（ClientHello、Host 都没有），或客户端干脆直接连 IP，域名规则匹配不到，流量落进 `MATCH,PROXY`。这类不报错，只能从「服务行为不对」倒推。TLS 已覆盖全端口，所以「加密业务用了非 443 端口」这一类已消除——APNs 的 5223 曾是它的受害者 | 已缓解 | `TestMihomoSniffsDomainOnNonStandardPort`：起真内核 + 真 TLS 服务（系统随机高位端口），走 socks5 用裸 IP 连，断言 `/connections` 里的 `sniffHost` 等于客户端发的 SNI。变异（TLS ports 改回 `443,8443`）时该用例转红、`sniffHost` 为空。私有协议类覆盖不到，出现症状时按连接列表的 IP 归属判断 | v2.1.5 |
