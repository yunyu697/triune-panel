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
| KL-12 | 直连清单里的域名被排除出 fake-ip，连接进内核时只剩 IP，域名只能靠嗅探补回；而嗅探的端口覆盖是**按协议分别定的**：TLS 全端口，明文 HTTP 只有 80 与 8080–8880，QUIC 只有 443 与 8443 | 两头都不报错，只能从「服务行为不对」倒推：①**协议层面**——私有二进制协议没有 ClientHello / Host，客户端直接连 IP 时也拿不到域名，域名规则匹配不到，流量落进 `MATCH,PROXY`；②**端口层面**——明文 HTTP 或 HTTP/3 走非常规端口时补不回域名。TLS 已放开全端口（APNs 的 5223 正是因此修好）；另两种不收全端口的代价分别是「把非 HTTP 流量当请求解析」与「UDP 嗅探要跨包缓存，全端口会把游戏 / 语音一起卷进来」，而它们的端口高度集中，暂按端口收窄 | 已缓解 | TLS 面：`TestMihomoSniffsDomainOnNonStandardPort`——真内核 + 真 TLS 服务 + 裸 IP 连接，断言 `/connections` 里 `sniffHost` 等于客户端发的 SNI；把 ports 改回 `443,8443` 该用例转红、`sniffHost` 为空。端口清单本身由 `TestTLSSniffCoversEveryPort` 与 `TestMihomoSnifferCoversQUIC` 钉住。出现「该直连却在走代理」时，先看连接列表里 `Host` 是否为空，再判断属于协议类还是端口类 | v2.1.5 |
| KL-13 | 拦截豁免的范围有两条边界：①只对**目录里**的规则集生效，自定义规则集（`custom-*`）不参与判断；②即使勾的是目录集，豁免内容也只有实测过的两条（`DOMAIN-KEYWORD,applog` 与 `DOMAIN-SUFFIX,zijieapi.com`） | ①你自己贴进来的第三方广告 / 追踪清单，如果里面含应用日志域名，会被 `REJECT` 且不会有任何豁免：现象是某个 App 首屏卡住，而 API 与图片全通。②别的厂商若也被同一类集合拒到阻塞首屏，面板不会自动认出来——「哪些域名是应用日志、拒了会不会卡首屏」需要实测证据，无法从域名数据派生，猜错的代价是把真广告放行。兜底按提示处理：把该 App 的日志域名加成 `DOMAIN-KEYWORD` 自定义规则（放在那条 REJECT 之前），或把这份清单改成「走代理」 | 未验证 | ①勾一个自定义规则集（内容含 `log5-applog.fqnovel.com`）并设为 `REJECT`：产物里没有 `DOMAIN-KEYWORD,applog,DIRECT`，界面也没有豁免提示；反向换成「广告域名（精简）」应自动出现豁免（`TestBlockExemptAppliesToTrimmedAdSet`）。②豁免内容与目录的判定分开看：`TestCatalogBlockSetsDeclareAppLog` 只保证每个 REJECT 广告类集合都回答了「含不含应用日志」，不保证豁免清单认得那个厂商的命名 | v2.1.6 |
| KL-14 | 系统直连清单（Apple 更新 / 激活 / 定位 / APNs、Microsoft 登录、Windows 更新、NCSI）是按类人工维护的白名单：上游新增的系统服务域名不会自动跟进 | 新端点仍落进 `MATCH,PROXY`，表现与当初那份故障单一样——检查更新转圈、登录被判异地、推送延迟，而配置与日志都干净。准入判据「只收系统·更新·激活·认证，不收内容服务」必须人工判断，无法从域名数据派生；整类放开（例如 `DOMAIN-SUFFIX,apple.com,DIRECT`）会把 iCloud 与内容服务一起拉成直连，是 v2.0.8 已否掉的越权做法 | 已缓解 | 症状出现时：先跑体检脚本（已知类别逐个断言），再在面板「连接」里看该域名命中哪条规则；确认属于系统服务后，在对应类的 `Rules` 里加一行。方向由类的 `Expect` 决定，`TestSystemClassRulesMatchExpect` 拦住写反，`TestSystemClassProbesAreInRuleLists` 拦住「加了规则忘归类」，`TestSystemDirectFakeIPFilterDerived` 拦住「忘派生 fake-ip / DNS」 | v2.1.7 |
| KL-15 | 「局域网 DNS」只覆盖 `.lan` / `.local` / `home.arpa` / `localdomain` 四类后缀，而且只是把这四类的查询指过去，不是把局域网解析器当全局默认上游 | 公司内网用 `.corp` / `.internal` / 自家域名时覆盖不到：那些名字仍然被拿去问公网，回 NXDOMAIN。sing-box 侧勾了 `geosite-private` 时能多覆盖一批私有后缀，mihomo 侧做不到（那边只有域名策略，引 rule-set 得赌内核版本行为，没赌）。补救办法：把内网域名加成 `DOMAIN-SUFFIX,corp.example.com,DIRECT` 自定义规则（让路由对），再把这台能回答它的服务器填进「局域网 DNS」。「让所有查询都走局域网 DNS」这一版没做——那会把公网解析全压到一台家用路由器上，慢，且多一个单点 | 已缓解 | `TestLANResolverBothCores` 钉住「被改指向的只有这四个后缀」；DNS 规则只由 `localOnlySuffixes` 派生，自定义规则里的 `.corp` 不会顺带进 DNS 侧（改错了这条用例会报多出来的后缀） | v2.1.8 |
| KL-16 | 自更新的「不用重新登录」只覆盖**手动点「下载并更新重启」**这一条路径；磁盘上的二进制被换成别的版本、面板自己 exec 过去的那条（`auto_switch_panel` 开着时默认如此）仍会把人踢回登录页 | 那条路径事先不知道要切，服务端没有时机签发接力凭证，前端也来不及挂等待层：表现是正在某页操作时连接断一下，刷新后回到登录页。数据与配置不受影响 | 未验证 | 症状：没点过更新却忽然需要重登。`TestResumeTicket*` 只钉住凭证本身的边界（一次性 / 过期 / 改密失效 / 并发只换一张），不覆盖这条路径；要验证得在目标机上 `cp` 一个新版本二进制到安装目录等 watch 触发，观察是否掉登录 | v2.1.9 |
| KL-17 | 配额耗尽后改读的 `releases.atom` 只给 tag 与时间，**没有资产清单与校验值**；且这条兜底只对提供 atom 订阅的站点有效（`github.com` 有，多数 API 镜像没有） | 面板的下载直链按 `releases/download/<tag>/triune-panel-<os>-<arch>` 拼，能装，但没有 sha256 可比；内核的资产名由第三方项目定，拼不出来，所以内核那条路只保证「列表能看、点得动」，真正的下载地址仍按版本号走 `AssetURL`。订阅只保留最近 20 条，更老的版本要靠版本索引清单。影响面：配额耗尽时的体验是「少一层校验」，不是「用不了」 | 未验证 | 症状：列表出现了、`sha256` 一栏是空的。`TestListVersionsAtom*` 钉住解析与排序，`TestVersionsFallBackToAtomWhenQuotaExhausted` 钉住「配额耗尽真的换通道且面板直链拼得出来」，两条都不覆盖「atom 站点被墙但 API 镜像可用」这种半通状态 | v2.2.0 |
