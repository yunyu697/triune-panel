# 更新日志

Triune Panel 每个版本已验证的能力。README 只保留[版本摘要](README.md#版本历史)，
完整历史在这里；发布说明与安装包见 [Releases](https://github.com/yunyu697/triune-panel/releases)。

**记录口径**：只写「代码已实现且实测通过」的东西，不写计划。
未验证的边界统一收在 README 的[已知限制与未验证项](README.md#已知限制与未验证项)。

---

## 未发布

1. **安装脚本：修掉「更新完打开还是旧版本」的两个根因**
   - **离线模式判定用错了目录**：`curl … | bash` 时 `BASH_SOURCE` 为空、`$0=bash`，
     `SCRIPT_DIR` 会解析成**执行命令时的当前目录**。只要该目录里躺着一个旧的
     `triune-panel-linux-<arch>`，重跑安装就静默装回旧版本——而且离线分支没有目标
     tag，那道「版本必须等于 tag」的闸被整个跳过。改为：管道执行一律走在线；离线
     只认「脚本本身是真实文件且同目录有包」或显式 `TRIUNE_OFFLINE=1`（找不到包直接
     报错，不偷偷联网）。
   - **`systemctl enable --now` 对已经在运行的单元是空操作**，于是升级时二进制换了、
     进程没换。改为 `systemctl restart`（对未运行的单元等价于 start）；替换二进制
     前先确认旧进程真的退出，端口仍被占用（手动 nohup / screen / 容器）则打印占用者
     并中止，而不是等 systemd 起不来。
   - **装完核对运行中版本**：回读 `/api/public/info`（返回的是运行中二进制的
     `Version`，不是磁盘文件），与目标版本不一致就报错并给出 `/proc/<pid>/exe`
     （带 `(deleted)` = 旧 inode 还在跑）与端口占用诊断，不再误报「启动成功」。
2. **修掉 `$VAR，` 的写法**（3 处）：变量后面直接跟中文标点时，bash 会把标点的
   首个字节当成变量名的一部分，在 `set -u` 下报 `unbound variable`。三处都在
   出错分支里——最需要看清提示的时候，提示本身先炸了。其中 `--reset-password`
   找不到二进制那处（`$RP_BIN，`）是存量问题。全部改为 `${VAR}`。
   用 `\$[A-Za-z_]\w*[^\x00-\x7f]` 扫一遍可确认无残留。
3. **版本号改为单位数进位制**：**次版本与修订号**只用 0–9，满 9 进位，不再出现
   `0.12.0` 这种后两段是两位数的号；**主版本不受限**（`v9.9.9` 之后是 `v10.0.0`）。
   规则与进位逻辑集中在 `deploy/check-version.sh`，CI 在推 tag 时调用同一份判定
   （`version` job），不另抄正则。
   已发布的历史版本不改号；按新规则，`v0.12.0` 之后第一个合规号是 **v1.0.0**。

## 起点（MVP）

登录鉴权、入站/用户 CRUD、双模式导入、dry-run 预检、
真实 sing-box 校验 + 原子应用 + 进程托管、三种订阅输出、修订历史。

## v0.12.0

1. **修掉「面板无法获取版本列表」**：未配置 `panel_repo` 时不再 400，
   落到内置官方仓库。实测空库直接查面板版本列表：`source=default`、21 条、
   首条 `v0.11.0`，且 `url` 已按当前平台填好
   （`.../download/v0.11.0/triune-panel-darwin-arm64`）。配了 `panel_repo`
   则走配置并标 `source=github`。
2. **下载镜像预设 + 测速**：14 个预设 + 官方地址共 15 个候选，
   `GET /api/mirrors` 返回，`POST /api/mirrors/test` 并发探测。
   实测本机：4 个可用（slink.ltd 981ms / gh-proxy.com 1246ms / ghfast.top 1369ms
   / 官方 2676ms），11 个失败（403 / 429 / 证书过期 / 超时），
   自动选中 981ms 那个。
3. **踩了个探针坑并修掉**：最初拿 `github.com/robots.txt` 测，ghfast.top 与
   gh-proxy.com 全报 403/404（它们只转发 release 路径），会得出「所有镜像都挂了」
   的相反结论。改成探测真实下载地址（`Range: bytes=0-0` 只取首字节）后同一批
   镜像全部返回 206。这条写成了单测 `TestProbeOneHitsRealPath`（断言镜像前缀
   拼法 + Range 头 + 206 判可用）。
4. **默认官方地址**：每次打开弹窗都重置为官方；测速后只保留可用项，
   不可用的隐藏并计数提示。
5. **镜像按次生效**：`mirror` 用指针字段区分「没传 / 传空串 / 传地址」，
   实测三种语义各自正确；非法地址（缺 `https://`）在下载前就 400。
6. **测速接口只收 github.com 地址**：`https://evil.example`、`http://github.com`
   （非 https）、缺 version、面板组件缺 url 全部 400——这个接口会把地址交给
   十几个第三方站点请求，不能当公开探活器用。
7. **测试**：新增 10 个用例（镜像 5 + API 5），`internal/api` 覆盖率
   46.5% → **46.8%**，`internal/update` 13.7% → **21.9%**，`-race` 无竞争。

## v0.11.0

1. **内核一键安装可选分支**：`GET /api/updates/versions` 新增 `channel`
   （`all` / `stable` / `prerelease`，默认 `all` 保持旧行为），响应带各分支
   数量。实测假 GitHub 上 5 条 release（含 1 条草稿、2 条预发布）：
   `all` 返回 5 条且草稿被丢掉、`stable` 3 条首条为 `v1.15.0`、
   `prerelease` 2 条首条为 `v1.16.0-rc.1`；未知分支 400。
2. **真实数据坐实了这个问题**：拿线上 GitHub 跑了一遍 sing-box 的版本列表，
   首页 30 条里 **22 条是预发布**，`all` 的第一条是 `v1.15.0-alpha.9` ——
   也就是说改动前的「一键安装」装的就是 alpha。现在默认 `stable`，
   首条是 `v1.14.4`。mihomo 那边 30 条里只有 1 条预发布，首条 `v1.19.33`。
2. **预发布不再混进「最新」**：GitHub 按创建时间返回，补丁回填会让第一条
   不是最高版本，改为按版本号降序重排（`TestSortReleasesByVersion`）。
   同版本号时正式版优先于 `-rc`。有些仓库发 alpha 不勾 pre-release 标记，
   所以除 `prerelease` 字段外还从 tag 后缀判定（`TestUpdateVersionsMarksPrereleaseByTag`）。
3. **前端分支切换在本地过滤**：一次拿全量，切分支不再等网络往返。实测无头
   浏览器：稳定版 `[v1.10.0, v1.9.7, v1.9.5, v1.8.9]`、预发布
   `[v1.11.0-alpha.2 · 预发布]`、全部按版本序合并；空分支的按钮自动禁用。
4. **主题拆成亮暗 × 色彩方案**：8 个一维主题改成 2 模式 × 7 方案（共 14 套
   调色板）。旧版色块只画一个主色，浅色与曜石看着都是蓝、点下去一个白底一个
   黑底——这就是「显示的颜色与实际不符」的根因。
5. **预览色从 CSS 变量实时读**（`getComputedStyle` 探针），预览与真实主题共用
   一份定义。实测浅色/暗色两种模式下 7 张卡片的迷你界面与 6 个色点均正确，
   暗色下森林选中态（绿色描边 + ✓）正常。
6. **老用户配置迁移**：`triune_theme` → `triune_mode` + `triune_scheme`，
   `LEGACY_THEME` 映射表覆盖全部 8 个旧 id，迁移后删除旧键。
7. **顺手收拾界面**：「装到面板专用目录，与系统现有二进制隔离」原本挤在组件
   表格的操作列里，把「一键安装」按钮顶到了表格外（实测被 `overflow-x:auto`
   裁掉）；移到表格下方「装到哪里」一段，操作列只剩「分支下拉 + 一键安装」。
   下拉原本被全局 `select{width:100%}` 撑成 182px，加 `width:auto` 后收回。
8. **测试**：`internal/update` 从 0 个用例到 5 个（tag 解析 / 排序 / 分支过滤 /
   归一化），`internal/api` 新增 2 个分支用例；覆盖率 46.1% → **46.5%**，
   `internal/update` 13.7%；全量 `go test ./...` 通过。

## v0.10.0

1. **配置命名保存 + 配置管理页**：生成后点「保存配置」起名入库，
   **配置管理** 页列出全部记录（名称 / 内核 / 目标 / 大小 / 保存人 / 更新时间 /
   操作），支持搜索、下载、重新生成、改名、删除，底部显示「共 N 份（上限 200 份）」。
   实测保存 → 列表 → 详情 → 下载 → 改名 → 删除全链路通，列表按名称排序。
2. **参数与正文一起存**：`params` 存下当时的全部选择，`content` 存生成结果。
   重新生成时用 `params` 原样再跑一次，再和新旧内容比对：一致就提示
   「配置没有变化」，不一致先展示差异让人选「覆盖」还是「另存」。
   实测只改备注时 `hash` 不变（`c0cfca93d22441a4 → c0cfca93d22441a4`），
   说明比的是正文不是元信息。
3. **中文文件名不乱码**：`Content-Disposition` 实测返回
   `attachment; filename="-.json"; filename*=UTF-8''%E5%AE%B6%E5%AE%BD-...json`，
   ASCII 回退与 RFC 5987 两套名字都在。前端用 `fetch` 带 `Authorization` 头再
   `blob()` 落盘——直接 `location.href` 会 401（下载接口同样要鉴权）。
4. **规则标签：单选替换 / 多选追加**：无头浏览器断言实测
   `multi lines=5 uniq=5`（两个标签追加、无重复）；取消一个标签后
   `lines=3 hasCN=false`（只移除该标签独有的行）；单选切换
   `lines=3 first=DOMAIN-KEYWORD,doubleclick,REJECT`（整段替换）。
5. **重复行自动去掉**：前端 `dedupeRules` + 服务端 `cleanRuleLines` 各做一遍。
   实测两行相同的 `DOMAIN-SUFFIX,corp.example.com,DIRECT` 存进去只剩一行
   （`count=2 rules='DOMAIN-SUFFIX,corp.example.com,DIRECT\nIP-CIDR,10.0.0.0/8,DIRECT'`）。
   顺手修掉一个真 bug：`dedupeRules` 收到数组时被 `String()` 转成逗号连接的一行，
   多选两个标签后所有规则挤成一整行——内核解析不了且不报错。
6. **只读账号拿不到凭据**：`viewer` 访问 `GET /api/configs/1/download` 实测 403，
   列表接口同样按 `write` 档拦截。配置里有节点密码，这不是「看着不方便」的问题。
7. **审计照记**：实测审计里出现 `config` / `tag` 两类动作；不进变更通知。
8. **端到端 40/40**（真实二进制 + 真实 HTTP + 真实文件下载），新增 8 个用例，
   `internal/api` 覆盖率 44.3% → **46.1%**，`go test -race` 无竞争。

## v0.9.9

1. **sing-box 版本感知生成**：`core_version` 字段 + 能力门限（`capsFor`）。
   1.11 生成旧 DNS `address` 格式、不写 `default_domain_resolver`；1.12+ 用
   `type`+`server` 并写 `default_domain_resolver`；1.15+ 不写 `tun.stack`。
2. **五个真实内核交叉校验**：同一份生成结果分别喂给
   `sing-box 1.11.15 / 1.12.25 / 1.13.21 / 1.14.4 / 1.15.0-alpha.13`，
   全部 `check` 通过；`1.10.7` 确认被拒绝（`route.rules[0].action: unknown field`），
   与「最低支持 1.11」的判定一致。
3. **1.12 / 1.13 / 1.14 生成的配置逐字节相同**（`TestGenSingboxSameAcrossPatchVersions`）
   —— 实测推翻了「每个版本都不一样」的直觉，也把门限表钉成了回归测试。
4. **1.10 及以前明确 400**，错误信息写清原因（没有路由 action，需要另一套模板）；
   非法版本号同样 400；mihomo 忽略 `core_version` 不报错。
5. **内核自检**：`POST /api/export/verify` 用面板上的二进制跑官方 check。
   实测未配置时返回 `skipped: true` + 原因；配置后返回 `ok: true` 与实际版本
   `sing-box version 1.14.4`；塞入 `{"bogus_key":1}` 时如实返回内核的 FATAL 输出。
6. **端到端 33/33**（真实二进制 + 真实 HTTP + 真实内核校验），新增 10 个用例，
   `internal/api` 覆盖率 38.9% → 44.3%，`go test -race` 无竞争。
7. **修掉一个演示模式的既有 bug**：`mockRoute` 里 `(.*)?` 不参与匹配时给的是
   `null` 而不是 `""`，`/api/inbounds` 这类无子路径的请求会抛 TypeError。调用方
   几乎都 `catch(e){}`，症状是「演示页某块永远空白」——生成器页因此一直没有入站
   可选。改为 `mt[2] || ""` 后恢复正常。

## v0.9.8

1. **只读账号脱敏视图**：`GET /api/clients` 对 `viewer` 从 403 改为返回脱敏副本
   （`credential` / `sub_token` 置空 + `masked: true`），配额、到期、启用照常可见。
   实测 viewer 拿到 200 且两列为空串、`masked=true`，POST 仍然 403；前端「用户」
   页对只读角色隐藏「+ 新增用户」、凭据列渲染为「—」、启用列变成只读徽标。
2. **登录历史按人聚合**：`GET /api/login-history?days=7` 从审计的 `login` 记录聚合出
   成功/失败次数、来源 IP、最近成功/失败与逐条时间线。来源 IP 只统计成功登录；
   一次最多扫 500 条，超出时 `truncated: true`。实测 owner 看到 2 人、viewer 只看
   自己，带 `?user=admin` 也越不了权；10 天前的记录在 `days=1` 下不出现、`days=30`
   下出现。
3. **变更通知**：`notify_email` / `notify_webhook` / `notify_events` 三个设置项，
   关键写操作后异步推送。实测本地 Webhook 收到 `settings.update`，diff 为
   `public_host: (空) → panel.example.com` + `smtp_pass: 已变更（不显示值）`，
   报文里搜不到明文密码；改入站端口收到 `端口: 443 → 22`；订阅改成只含 `inbound`
   后设置变更不再推送；清空通道后完全静默；`POST /api/notify/test` 在未配置时提示
   「还没配置任何通道」，上游返回 500 时如实回 `{"ok":false,"errors":["HTTP 500 ..."]}`。
4. **端到端 28/28 通过**（真实二进制 + 真实 HTTP + 真实 UA + 真实 Webhook 接收端）；
   新增 13 个用例（脱敏 1 + 登录历史 4 + 通知 8）；`internal/api` 覆盖率 35.8% → 38.9%。

## v0.9.7

1. **登录设备页**：`GET /api/sessions` 列出在线会话（设备 / 账号 / IP / 登录时间 /
   最后活跃），`DELETE /api/sessions/{id}` 精准踢单个，`DELETE /api/sessions`
   一键退出自己其余设备。设置页新增「登录设备」卡片，所有角色可见。
2. **凭据不外发**：对外标识是 `sha256("triune-sid-v1|"+token)[:8]` 的十六进制，
   端到端实测响应体里搜不到任何 token 字样（有用例守着）。
3. **吊销权限按等级**：实测 admin 踢 owner → 403「不能吊销同级或更高权限账号的会话」，
   owner 踢 admin → 200 且被踢方下一个请求 401；owner 踢自己的手机端 → 200，
   本机不受影响；不存在的 sid → 404（先定位再判权限，不泄漏也不误删）。
4. **只读账号只看自己**：viewer 调列表只返回自己那 1 条；踢 owner 的会话 403；
   「退出其他设备」可用（返回吊销 0 台）。
5. **改密 / 停用 / 删除即清内存**：改密后旧 token 立即 401；停用账号后其在线会话
   下一个请求 401，列表里也不再出现。
6. **UA 压缩**：Chrome/macOS、Firefox/Windows、Safari/iOS、`python-requests`
   分别渲染为 `Chrome · macOS` / `Firefox · Windows` / `Safari · iOS` / `脚本 · Python`。
7. **端到端 30/30 通过**（真实二进制 + 真实 HTTP + 真实 UA，不是内存直调）；
   `go test -race ./internal/api/` 无竞争；`internal/api` 覆盖率 32.8% → 35.8%。

## v0.9.6

1. **多管理员 + 按人追溯**：账号从 `settings` 单槽迁到 `admin_users` 表（迁移 v4 播种
   `owner`，老库的用户名与哈希原样搬过去，升级后原账号密码照常可用）。审计日志的
   `Actor` 从此是真实登录人，不再永远是同一个 `admin`。
2. **角色三级 + 显式声明**：`owner` / `admin` / `viewer`，路由分三档包装
   （`auth` / `write` / `ownerOnly`）。刻意不按 HTTP 方法自动推断——`GET /api/clients`
   和 `GET /api/inbounds/{id}/export` 都会吐凭据，靠方法推断一定会漏。
3. **会话失效改为「每账号密码指纹」**：`session{uid, fp}`，每次请求回库校验账号存在 /
   启用 / 哈希未变。停用或删除账号后旧会话下一个请求即 401；改密只踢自己，
   不再像全局 `session_epoch` 那样把所有人一起踢下线。
4. **越权与停用都留痕**：403 记 `rbac.deny`（含角色与目标端点）；登录失败记被尝试的
   用户名；管理员增删改记 `admin`。实测审计输出：
   `ops inbound.create ok` / `ops rbac.deny fail 角色 admin 越权访问（需要 超级管理员）`。
5. **不可自伤**：owner 不能删除 / 降级 / 停用，也不能新建第二个；不能删除或停用自己。
   接口层与 UI 双层拦截，实测均返回 400 并给出可读原因。
6. **TOTP 密钥改为账号级**：没绑验证器的人登录时跳过第二步；关闭 2FA 只清自己的密钥。
   暂存密钥改存内存（每账号一份），不再落 `settings`。
7. **端到端实测**：出厂默认登录 → 首次设密码（原密码留空）→ 旧 token 401 →
   新密码登录 → 新建 viewer/admin → 只读账号 GET 入站/流量 200、GET 客户端/设置/审计 403、
   POST 403 → 停用后登录 403「账号已停用」→ 删除/降级 owner 400。

## v0.9.5

1. **`/metrics`（Prometheus 文本格式）**：`internal/api/metrics.go` 输出 14 个指标家族，
   覆盖进程（up / 构建信息 / 运行时长 / goroutine / 堆）、数据库可用性、用户与入站计数、
   内核运行状态、单用户流量与配额、证书到期、实时总速率。
2. **默认关闭 + 令牌鉴权**：没配 `metrics_token` 返回 403（附启用提示），配了之后
   错误/缺失令牌一律 401，用 `crypto/subtle` 定长比较。刻意不做「本机免认证」——
   面板常挂在 nginx 后面，那样等于任何能碰到反代的人都能拉指标。
3. **标签值转义**：客户端名是用户输入，`"`、`\`、换行必须转义。否则一个叫
   `x"} 1\ntriune_fake 999` 的用户就能往指标里注入伪造序列。有专门用例断言
   「注入的名字不会变成独立一行」。
4. **家族连续输出**：Prometheus 规范要求同一家族的 HELP/TYPE 与样本连成一组，
   所以表头是「首次写样本时惰性输出」，且样本按家族分组而不是按用户交错。
   有格式合规用例守着（这条用例在实现阶段真的抓到了交错输出的 bug）。
5. **独立解析验证**：用官方 `github.com/prometheus/common/expfmt` 解析输出，
   14 个家族全部识别、类型与样本数正确（不只靠自写校验）。

## v0.9.4

1. **`PUT /api/inbounds/{id}` 也是同一个坑，且后果更重**：旧实现整对象解包再全列
   UPDATE。`PUT {"name":"改名"}` 会把 `core`/`protocol`/`port`/`tag`/`settings`
   全部清成零值（`settings` 变 `"{}"`、`port` 变 0），返回 200。
2. **UI 编辑入站会把它顺手停用**：前端 `saveInbound()` 提交的 body 里没有
   `enabled`/`draft`/`tag`，旧代码就用零值覆盖——在界面上改个名字，保存后该入站
   变成「已停用」，自定义 tag 也丢了（渲染时会退化成 `in-<id>`）。
   这条是日常操作路径，影响面比 API 调用大得多。
3. **内核切换守卫会误伤**：旧守卫拿零值 `core` 去比，于是任何「只改名」的请求在
   启用中的入站上都会收到「请使用 POST /api/switch-core」——调用方根本没想换内核。
4. **改为部分更新**：先读当前记录，只覆盖请求里出现的键；真的想换内核仍然拦下来
   并指向 `/api/switch-core`（那条路径有预检与失败回滚）；改不存在的入站返回 404。
   防火墙端口对账逻辑不变，仍按「旧值 vs 新值」判断草稿转正与端口/协议变化。

## v0.9.3

1. **移除假的每用户限速字段**：`rate_limit_mbps` 从 `model.Client`、SQL 读写
   与 API 响应中全部摘掉。此前它能写、能存库、能被 GET 回来，唯独内核不消费——
   脚本传个「100」会以为限速生效，实际什么也没发生。
2. **改为明确拒绝而不是静默忽略**：`POST/PUT /api/clients` 收到非零
   `rate_limit_mbps` 返回 400，错误里写清「三个内核都没有这个能力」并指出
   该用 `quota_gb`（超限自动停用）或在内核外做整形。传 0 或不传都放行。
3. **修掉 `PUT /api/clients/{id}` 的数据丢失 bug**：旧实现把请求体解成完整的
   `model.Client` 再整行 UPDATE，于是 `PUT {"quota_gb":42}` 会把没传的
   `credential`、`sub_token` 清成空串、`enabled` 置为 false，**还返回 200**——
   用户的订阅链接和登录凭据静默失效。现在改成真正的部分更新：先读当前记录，
   只覆盖请求里出现的键；`expires_at: null` 表示清除到期时间，不传则保持。
   改不存在的用户返回 404 而不是假装成功。
4. **历史列兼容**：数据库里的 `rate_limit_mbps` 列保留（schema 版本号就是迁移
   条目数，删条目会让已升级的库跳过后续迁移），但不再读写；旧库里该列有值也
   不影响加载与更新，有回归测试守着。
5. **列顺序回归测试**：`clientCols` 与 `scanClient` 的扫描顺序都是手写的，
   改一处忘另一处会让字段静默串位（`enabled` 读到 `quota_gb` 上之类），
   新增 CRUD round-trip 用例锁住。

## v0.9.2

1. **异地备份**（`internal/backup/upload.go`）：`backup --upload <remote:path>`
   经 rclone 把归档传到 S3 / R2 / B2 / WebDAV / SFTP / 网盘等，覆盖
   「备份跟着磁盘一起没」这个加密解决不了的风险。
   **上传后必回读校验**——传完用 `rclone lsjson` 读回远端字节数与本地比对，
   不一致直接报错（网络截断不一定体现在 rclone 退出码里）。
2. **远端轮转是显式开关**：`--upload-keep N` 默认 0（不动远端任何文件）；
   开启后只删 `triune-data-*.tar.gz[.age]`，远端目录里的其他文件一律不碰。
3. **失败语义明确**：上传失败退出码 1（systemd timer 会报红），但输出会写明
   「本地归档仍可用」，避免把"没传上去"误当成"已备份"。
4. 安装脚本的每日备份支持 `backup.env` 里写 `TRIUNE_BACKUP_REMOTE`，
   与 `backup.pass` 组合即「加密 + 异地」全套。

## v0.9.1

1. **安全响应头**（`internal/api/security.go`）：CSP、`X-Frame-Options`、`nosniff`、
   `Referrer-Policy`、`Permissions-Policy`、COOP 统一下发，连 401/404 响应也带
   （中间件包在最外层）。HSTS 只在确实走 HTTPS 时下发，且反代场景下只采信
   **可信代理**发来的 `X-Forwarded-Proto`——否则公网伪造一个头就能让面板给
   HTTP 连接下发 HSTS，把用户锁死。
2. **服务端登出**（`POST /api/logout`）：此前只有前端清 localStorage，token 在
   剩余 24 小时内仍然有效，且没有任何单独吊销手段（只能改密踢掉全部会话）。
   现在登出会真正删除会话并记审计，其他设备的会话不受影响。
3. **鉴权矩阵测试**（`internal/api/auth_test.go`）：52 个受保护端点逐一断言
   匿名访问必须 401（新增路由忘了包 `a.auth()` 会被这条测试逮住），外加
   过期会话、改密后旧会话失效、伪造 token、401 不回显 token、登出只影响当前会话。
4. **浏览器实测**：Chrome 真实跑「登录 → 强制改密 → 重新登录 → 切页 → 登出」，
   控制台**零 CSP 违规、零 JS 报错**，界面完整渲染（证明 `'unsafe-inline'`
   确实放行了 126 处内联事件），登出后旧 token 立即 401。

## v0.9.0

1. **备份加密**（`internal/backup/crypt.go` + `cmd/server/passphrase.go`）：age 口令模式
   （scrypt + X25519），流式 `tar → gzip → age` 写入，归档名加 `.age`。
   归档是标准 age 格式，`age -d` 可独立解密（已用官方 `age` 源码编译出 CLI 交叉验证：
   解出的 tar.gz 内容与原始数据一致）。口令四种来源：`--passphrase-file` >
   `--passphrase-stdin` > `TRIUNE_BACKUP_PASSPHRASE` > 交互无回显；
   不接受命令行明文参数。口令不足 12 位直接拒绝且不留半个文件。
   `restore` 按魔数自动识别加密与否，改名骗不过去；恢复前备份沿用同一口令。
2. **修掉一个 WAL 相关的恢复 bug**：面板的库是 WAL 模式，停服后最后一个事务
   常常还在 `triune.db-wal` 里没合并回主库。`restore` 的 schema 只读校验原先只解
   `triune.db`，于是把正常备份误判成「no such table: settings」并拒绝恢复
   （数据其实完好）。现在 `ExtractDB` 连 `-wal`/`-shm` 一起解，实测 79 KB 真实库
   加密备份 → 恢复 → `status` 全部正常。补了 `TestExtractDBCarriesWAL` 等回归用例。
3. **每日自动备份改用 `triune-panel backup`**：原先 `deploy/install.sh` 里是裸
   `tar czf`，既拿不到一致快照也不能加密。现在脚本调用二进制自身的 backup
   （统一轮转与排除逻辑），把口令写进 `$INSTALL_DIR/backup.pass` 并 `chmod 600`
   即自动加密。
4. **WAL 提示**：`backup` 发现 `triune.db-wal` 非空时提示「已一并打包，但运行中
   建议停服再备份」，避免用户误以为拿到了严格一致的快照。

## v0.8.9

1. **审计日志**（`internal/store/audit.go` + `internal/api/audit.go`）：新增 `audit_log`
   表（schema v3）与「审计」页。记录登录（含失败与真实来源 IP）、改密、设置变更、
   入站/用户增删改、应用与回滚、内核切换、版本更新、CLI 重置密码与数据恢复。
   只记键名不记值；清空动作本身留痕；保留 90 天自动清理。
2. **`/healthz` 免认证探活**：返回 `status/version/uptime_sec/db`，DB 打不开时
   返回 **503 + degraded**（而不是 200），避免「进程活着但服务坏了」被监控当成健康。
3. **证书到期 UI 标红**：证书卡片显示剩余天数并按 30 天/14 天分级上色，
   14 天内与已过期直接标红并提示「自动续期可能已失败」，卡片标题旁给状态徽章。
4. **systemd 加固**：`NoNewPrivileges` / `ProtectSystem` / `PrivateTmp` / `ProtectHome`
   / `ProtectKernelTunables` / `RestrictSUIDSGID` / `UMask=0027` 等，
   并用 `ReadWritePaths` 放开安装目录、数据目录与 `/usr/local/bin`（自更新与内核更新要用）。
   同时修正模板里过时的 `-addr :2087` → `:10010`。
5. **apply 流水线单测**（`internal/core/pipeline_test.go`）：覆盖未配二进制跳过校验、
   **校验失败时运行配置零改动**（含临时文件不残留）、成功替换前自动备份、
   dry-run 不落盘、回滚取最新备份、无备份时报错、未知内核报错。

## v0.8.8

1. **反向代理 IP 识别**（`internal/api/clientip.go`）：新增设置 `trusted_proxies`
   （默认空 = 不信任任何代理，行为与旧版一致）。只有直连对端落在可信网段内才采信
   `X-Forwarded-For` / `X-Real-IP`，且从右往左取第一个不可信地址，客户端伪造左侧无效。
   `login_last_ip` 与登录防爆破计数键都改用真实客户端 IP。
   设置页写入前整体校验，非法 CIDR/IP 直接 400，不会留下半套配置。
2. 端到端实测（本地实例 + 伪造转发头）：配了 `127.0.0.1` 后 A 客户端失败 5 次被锁 429、
   B 客户端不受影响；清空可信网段后伪造 `X-Forwarded-For: 6.6.6.6` 被忽略、
   仍记为直连对端。

## v0.8.7

1. **忘记密码（离线重置）**：`triune-panel reset-password` 直接改本地 SQLite，
   面板可继续运行。默认生成 16 位随机密码、**同时关闭 2FA**（丢设备场景）、
   让所有已登录会话立即失效；`--password-stdin` 支持脚本化，
   不接受命令行明文密码（会留在 shell 历史）。
2. **命令行工具**：`status`（含 `--json`）、`doctor`（10 项环境自检，
   有 fail 时退出码 1）、`backup`（纯 Go tar.gz，排除 backups 自身并按份数轮转）。
   数据目录自动探测 `--data` → `TRIUNE_DATA` → `./data` → `/opt/triune-panel/data`。
   刻意不提供 `apply` / 切换内核：内核进程归 daemon 管，CLI 裸跑会抢端口。
3. **密码改为 bcrypt 哈希存储**：新增 `admin_pass_hash`，遗留明文槽只用于兼容——
   登录命中明文时静默升级为哈希；安装脚本的 `bootstrap.creds` 首启即写成哈希，
   明文不落库。密码最小长度 8 位。
4. **会话纪元（session_epoch）**：改密/重置密码会更换纪元，旧 token 立即 401，
   不需要重启面板；CLI 在另一个进程改库也能被运行中的 daemon 立刻感知。
5. **前端**：改密后强制回登录页（旧会话已失效，避免"保存成功但下一步 401"的困惑）；
   登录页直接给出忘记密码的命令提示。
6. **`restore` 恢复命令**：`backup` 的另一半。四道保险——拒绝运行中恢复（端口探测）、
   恢复前自动备份当前数据、归档 schema 版本不得高于当前二进制、拒绝路径穿越；
   失败自动回滚，`backups/` 不参与替换。
7. **CI**：GitHub Actions 跑 vet/test/三平台交叉编译/`-version` 格式校验/shellcheck，
   堵住"推坏代码"与"版本字段格式被改坏导致安装脚本误判"两类事故。

## v0.8.6

1. **入站端口自动放行防火墙**（`internal/firewall`）：
   - 新增入站时自动在生效防火墙放行其端口/协议；删除入站、修改端口或协议时
     回收旧端口——回收前会检查是否仍有其它「已启用且非草稿」入站占用该端口，
     避免误关仍在服务的端口。
   - 后端探测顺序与安装脚本 `ensure_port_open` 一致：firewalld（running）→
     ufw（active）→ iptables（INPUT 默认 DROP/REJECT）。三者皆无则视为系统
     默认放行，函数直接返回 nil，不做任何操作（开发机 / 云安全组场景零副作用）。
   - 协议推断：`hysteria2` / `tuic` 与 `quic` 传输走 UDP，其余 TCP。
   - 全部操作幂等（已放行则跳过、规则不存在则忽略），失败只记日志不阻断 API。
   - 设置页「基础设置」新增开关 `fw_auto`（默认开启，`fw_auto=0` 关闭）。
   - 说明：ufw / firewalld 规则持久化，重启保留；iptables 规则重启不保留，
     云厂商安全组需在控制台另行放行。
2. **修复 `core.All()` 返回 nil 元素**：未 import 对应实现包（如单元测试）时，
   `All()` 会返回含 nil 的切片，调用方 `range` 后取 `Name()` 直接空指针 panic；
   现改为跳过未注册内核。
3. **测试**：`firewall` 包协议推断与无后端零副作用断言；`api` 包补
   `portInUseByOthers`（停用/草稿/不同协议均不计占用）与 `fw_auto`
   设置读写回显用例（守住「PUT 能存、GET 不返回」的白名单不一致）。

## v0.8.5

1. **用户二维码分享**：
   - 用户列表一键生成订阅链接二维码（手机 App 扫码即用）；
   - 入站导出弹窗可对每个分享链接生成二维码（多用户时按钮切换），
     支持下载 PNG 与复制链接；QR 生成器内嵌（MIT，无外部 CDN 依赖）。
2. **入站连通性自检**：入站列表新增「自检」，四项只读探测逐项给出红黄绿结论：
   配置状态（草稿/停用提示）→ 内核进程 → 端口监听（TCP 拨号 / UDP 独占绑定反证）→
   TLS 握手与证书到期（Reality 标注为提示性）→ 公网可达（验证防火墙放行，
   UDP 端口提示到安全组确认）。
3. **流量排行与告警**：
   - 流量表按用量降序排行（带序号），新增配额百分比与状态徽章
     （接近配额 / 已达配额 / 3 天内到期 / 已到期），概览排行同步 ⚠/✕ 标记；
   - 告警阈值可调（默认 80%，设置页「流量告警阈值」），越过阈值或配额时
     经已配置的 SMTP 发送邮件提醒（同一级别只发一次，回落自动复位）。
4. **订阅令牌重置**：用户列表「重置订阅」——旧订阅链接立即失效（实测 404），
   新链接重新发放；用于链接泄露或换设备场景。
5. **配置 diff 视图**：应用页新增「对比变更」，行级对比「运行中配置」与
   「按当前意图重新渲染的配置」（LCS unified diff，绿增红删），无运行配置时
   提示将全部新建；应用前先看差异，防误改。
6. **订阅 URL 聚合（多节点）**：导入页新增「订阅 URL 聚合」——填入远端订阅
   地址与前缀（可选）即可拉取并入外部节点：支持 base64 链接列表、明文分享
   链接与 Clash YAML（vless/vmess/trojan/ss/hysteria2/tuic，Reality 参数
   pbk/sid 完整转换）；已存在的节点自动跳过；仅聚合进订阅输出，
   不改动本机任何内核配置。

## v0.8.4

1. **新增入站智能化**（尽可能零手填）：
   - 协议改为下拉框，只显示当前内核实际支持的协议（与渲染器同步：
     sing-box 9 种 / xray 3 种 / mihomo 8 种），切换内核自动联动；
   - 打开弹窗即自动填充：名称（协议-端口）、推荐端口（冲突自动顺延）、
     推荐传输与 TLS（如 vless+sing-box 默认 Reality 并自动生成密钥对）；
   - 按 协议 × 传输 × TLS 联动出需要的字段：WS 路径（自动生成）、gRPC 服务名、
     TLS 域名与证书路径、Reality 握手目标 / short_id、SS 加密方法、
     xray VLESS flow，保存时自动并入 Settings JSON（高级透传区保留）。
2. **版本列表显示 Release Notes**：组件更新弹窗选中版本即显示该版本
   GitHub Release 说明（轻量 markdown 渲染），更新前先看变更。
3. **渲染安全修复**：面板托管 / 客户端专用的 settings 键（`pbk`、`short_id`、
   `flow`、`private_key` 等）不再原样透传到内核配置顶层——sing-box 严格
   校验未知字段，多余顶层键会导致配置校验失败；`short_id` 现按结构写入
   sing-box `tls.reality.short_id` / xray `realitySettings.shortIds`；
   同时修复 sing-box 渲染器不支持 shadowsocks 入站的问题。

## v0.8.2

1. **登录二次验证（2FA）**：在账号密码之外追加一道验证，两种方式二选一：
   - **TOTP 动态口令**：兼容 Google / Microsoft Authenticator、1Password、
     FreeOTP 等常用验证器（RFC 6238，SHA1/6 位/30 秒，允许 ±1 周期时钟偏差）。
     设置页生成密钥 → 验证器手动输入密钥绑定 → 输入当前口令确认启用。
   - **邮箱验证码**：登录时向配置的邮箱发送 6 位验证码（5 分钟有效、
     一次性使用、限 5 次尝试）。支持 465 端口 SSL 直连与 587 端口
     STARTTLS，启用时自动发送测试邮件验证 SMTP 配置。
   - 登录流程变为两步：账密正确后返回 `need_2fa`，前端展示验证码输入行；
     验证失败同样计入登录防爆破锁定（5 次锁 10 分钟）。
   - 验证码只存内存、不下发密钥（`totp_secret` 不出现在任何 GET 响应），
     SMTP 密码留空保存表示沿用已存授权码。
2. **配置生成器规则集「全选 / 清空」**：只作用于当前搜索过滤结果，
   并自动跳过当前内核不可用的规则集，新勾选默认 PROXY 策略。

## v0.8.1

1. **修复「升级后界面还是旧版」**：面板启动时会把内嵌 UI 落地到
   `data/web/index.html`，但旧逻辑"已存在即不覆盖"——从旧版本升级后
   二进制是新的，磁盘上却一直是旧 UI 文件。现在内嵌页面带 build 标记
   （`<!-- triune-panel-web/<ver> -->`），启动时标记缺失或版本不一致即覆盖；
   同时静态页响应加 `Cache-Control: no-cache`，避免浏览器缓存旧界面。
   自定义 UI 的用户：替换文件时请同步更新头部 build 标记为当前版本号。
2. **修复仪表盘图表变形**：图表画布初始化强制最小 220px 宽且不设
   `style.width`，窄容器（内核状态环 / KPI 迷你走势图 / donut）里宽位图被
   CSS `width:100%` 压回实际宽度——圆环被压成竖椭圆。改为按容器实测宽度
   初始化并显式设置画布尺寸；窗口缩放后重绘前先清除旧内联宽度再测量。
3. **修复图表 Y 轴刻度被裁切**：长刻度标签（如 "46.57 GB"）按固定左留白
   绘制会被画布左缘裁掉首位数字，看起来像刻度错乱。趋势图与实时速率图
   均改为先测量最宽标签再动态决定左留白。

## v0.8

1. **规则集目录扩容到 107 个**（原 18 个）：覆盖基础分流、广告与隐私、
   AI 服务、流媒体影音、社交通讯、游戏加速、开发与技术、金融商务、购物电商、
   办公云服务、教育科研、生活服务共 12 个分组。
   - 全部条目已对上游真实文件逐个校验（mihomo 侧 MetaCubeX/meta-rules-dat、
     sing-box 侧 SagerNet/sing-geosite / sing-geoip），共 212 个下载地址全部可达
   - **修复规则集 URL 拼写错误**：mihomo 应为 `geo/{geosite|geoip}/{name}.mrs`，
     sing-box 应为 `geosite-{name}.srs` / `geoip-{name}.srs`（此前三种拼法均错位，
     生成的是 404 链接）
   - 按内核标注可用性（如 `geoip-private`、`category-porn` 仅 mihomo 上游提供），
     切换内核时自动置灰并跳过，不会生成无效引用
2. **场景预设扩容到 23 种**（原 6 种）：均衡日常 / 流媒体优先 / 游戏加速 /
   AI 服务 / 隐私优先 / 国内直连优先 / 办公协作 / 开发调试 / 学术科研 /
   金融证券 / 海淘电商 / 社交通讯 / 视频会议 / 低延迟精简 / 纯净拦截 /
   出海商务 / 回国加速 / 青少年模式 / 音乐听歌 / 动漫追番 / 资讯聚合 /
   智能家居直连 / 全局代理，每种预设附带说明、规则集与策略，内置自定义规则。
3. **界面全面重设计**：渐变主色体系（8 套主题各配双色渐变）、玻璃拟态顶栏、
   分组侧边导航与激活指示条、卡片渐变高光与分层阴影、圆角与排版层次全面提升。
4. **仪表盘图表**（纯 Canvas 自绘，不依赖任何外部 CDN）：
   - 近 14 天流量趋势：双序列平滑面积图 + 渐变填充 + 悬停十字线与浮层提示
   - 实时速率双线滚动曲线 + KPI 卡内迷你趋势线
   - 用户流量占比环形图（含中心总量）与流量排行榜
   - 内核运行状态环形指示（运行中 / 待启动 / 未配置）
   - 流量页柱状图同步升级为同风格面积图，主题切换即时重绘
5. **配置生成器交互升级**：场景预设改为卡片网格（带分类标签与规则集数量），
   规则集按分组折叠、显示「可用/总数」并支持关键词搜索，内核与目标改为分段控件。
6. **修复**：sing-box 生成配置时 DNS 规则引用了未选中的 `geosite-cn` 导致
   `sing-box check` 直接失败（未勾选时自动补充该规则集用于 DNS 分流）。
7. **修复**：Reality 公钥此前只写入 `private_key`，导致生成的客户端配置公钥为空、
   sing-box 校验失败；现兼容 `pbk` / `public_key` / `publicKey` 三种写法，
   密钥工具也会同步写入 `pbk`。
8. **已用真实内核全矩阵校验**：mihomo 1.19.33（`mihomo -t`）与
   sing-box 1.14.4（`sing-box check`）**54/54 通过**（23 场景 × 2 内核、
   全规则集 107/105 条 × 2 内核、3 种目标设备 × 2 内核）。
9. **修复安装脚本权限问题**：此前以普通用户重跑安装时，
   `install: cannot create regular file '/opt/triune-panel/triune-panel': Permission denied`
   （目录已存在但属主为 root）。现在特权步骤逐条交给 `sudo` 执行，
   忘记加 `sudo` 也能装；安装失败时输出当前用户 / 目录权限 / 磁盘空间的完整诊断；
   新增 `TRIUNE_INSTALL_DIR` 自定义安装目录（rootless 环境可用）；
   二进制不可执行（架构不匹配）时立即中止并提示正确架构。

## v0.7

1. **配置生成器**（新页面「配置生成」）：多选入站 × 选择目标设备
   （客户端 / 主路由 / 旁路由）× 场景预设，一键生成可直接使用的
   mihomo（YAML）或 sing-box（JSON）完整配置。
   - 目标模板：客户端=混合端口 7890 不启用 TUN；主路由=TUN 接管 +
     auto-redirect + DNS 劫持 :53 + 嗅探；旁路由=TUN + 局域网监听，
     设备网关指向本机即可接管
   - 场景预设（6 种）：均衡日常 / 流媒体优先 / 游戏加速 / AI 服务 /
     隐私优先 / 国内直连优先，预设自动勾选规则集与策略
   - 规则集目录（18 个，基于 geosite/geoip 官方数据，自动定期更新），
     每个规则集可独立选择 DIRECT / PROXY / REJECT 策略
   - 自定义规则：`类型,值,策略`（DOMAIN/DOMAIN-SUFFIX/DOMAIN-KEYWORD/
     IP-CIDR/GEOSITE/GEOIP…），GEOSITE/GEOIP 自动挂载对应在线规则集；
     sing-box 侧自动转换为 domain_suffix / ip_cidr / rule_set / reject
   - 自定义规则集：远程 URL + behavior + 策略，mihomo 按 .mrs/.yaml/.txt
     推断格式，sing-box 建议 .srs
   - 已用真实内核全矩阵校验：mihomo 1.19.33（`mihomo -t`）与
     sing-box 1.14.4（`sing-box check`）6/6 通过（含 sing-box 1.12+ 新
     DNS 格式、rule actions、uTLS、default_domain_resolver 等兼容处理）
2. **修复**：sing-box 订阅/导出中 Reality 出站缺少 uTLS 导致无法初始化。

## v0.6

1. **登录防爆破**：同一来源连续失败 5 次锁定 10 分钟（HTTP 429 + 明确提示），
   登录成功自动清零；防暴力破解，锁定期内正确密码同样拒绝。
2. **ACME 手动签发**：`POST /api/certs/issue` 立即触发签发/续期检查（无需等下一
   检查周期）；保存 ACME 设置后也自动触发；设置页可查看证书状态与错误详情。
   同时修复：API 与后台续期循环此前使用两个 certmgr 实例，API 永远查不到证书状态。
3. **外部节点管理**：导入页新增外部节点列表（协议/地址/导入时间）与删除，
   删除只影响订阅聚合，不触碰任何运行配置。
4. **实时速率**：`GET /api/traffic/live` 基于采样差值输出实时上/下行速率，
   概览页卡片每 10 秒自动刷新。
5. **修复设置白名单**：`gh_mirror` / `gh_api_base` / `panel_repo` /
   `panel_update_index` 此前不在 `PUT /api/settings` 白名单中，
   「保存更新设置」静默无效；现已可正常保存与回显。
6. **内核一键安装（v0.6.1）**：未配置二进制路径的内核，在
   「设置 → 组件更新与版本管理」点「一键安装」，自动从官方 Release 下载并
   安装到**面板专用目录** `data/bin/<内核>/`，路径自动回填设置 —— 与系统里
   其他服务（如现有代理）使用的二进制完全隔离，互不影响。已实测：全新环境
   一键安装 sing-box 1.14.4 约 8 秒完成，设置自动回填、版本正确识别。
   之后该内核的更新/回滚都只作用于这份专用副本。
   v0.11.0 起安装前可选**分支**（稳定版 / 预发布 / 全部），默认稳定版——
   GitHub Releases 把 alpha/beta/rc 和正式版混在一张列表里，不区分分支的
   「装最新」会把 `1.15.0-alpha` 直接装到生产面板上。
7. **入站导出（v0.6.2）**：入站列表新增「导出」按钮，一键为该入站下所有
   有效用户生成 `GET /api/inbounds/{id}/export`：分享链接（复制）、
   Clash/mihomo YAML、sing-box outbounds JSON、入站 JSON（含下载文件名）。
   多用户节点自动以「入站名-用户名」区分；草稿/停用/过期用户不导出。
   同时修复未配置节点公开地址时，回退 Host 带面板端口导致链接地址畸形的
   问题（订阅输出同步受益）。

## v0.5

1. **内核随时安全切换**（`POST /api/switch-core`）：已运行的入站可在
   sing-box / xray / mihomo 之间随时迁移。流程：内存预检（目标内核真实校验，
   失败即拒、零影响）→ 提交 DB → 停旧内核释放端口 → 标准流水线启动新内核 →
   健康检查（捕获端口冲突等"校验能过但起不来"的问题）→ 失败**自动回滚**
   （DB 还原 + 旧内核重启）。切换/回滚全程记入修订历史；支持单入站或批量，
   支持跨内核并存（原内核仍有入站时自动收缩配置继续运行）。
   运行中的入站禁止通过普通编辑直接改内核字段，防止绕过安全流程。
2. **全新 Web UI**：侧边栏布局、8 套主题（浅色/曜石/午夜/森野/玫瑰/暖阳/雾蓝/海盐）、
   弹窗+Toast 交互、主题感知图表；`demo.html` 为内置模拟数据的离线交互演示。
3. **全组件更新与任意版本管理**：面板与三个内核二进制均可更新/降级到任意版本
   （`POST /api/cores/{name}/update`、`POST /api/self-update`）。安全链路：
   下载（Content-Length 防截断 + 可选 sha256）→ 解压（tar.gz/zip/gz）→
   新二进制可执行验证 → **用新二进制校验当前配置** → 备份旧版本 →
   原子替换 → 重启 + 健康检查，任一环节失败**自动回滚**。
   版本列表来自 GitHub Releases（支持 `gh_mirror` 下载镜像、`gh_api_base` API 镜像）；
   面板支持版本索引清单 `panel_update_index`；历史版本可随时回滚
   （`POST /api/cores/{name}/rollback`、`POST /api/panel/rollback`）。
   v0.12.0 起面板的版本列表**开箱可用**（默认读内置官方仓库，不用先配
   `panel_repo`），装/更新时还能选下载镜像并一键测速。

## v0.3

1. **流量图表**：`GET /api/traffic/daily?days=N&client_id=0` 返回补零的按日
   上/下行序列；流量页内置纯 canvas 柱状图（无外部依赖，离线可用），
   支持近 7/30/90 天切换；用户表增加配额用量进度条。
2. **用户管理增强**：支持编辑配额（GB）、到期时间、凭据、关联入站，
   一键启停；到期/超配额自动禁用联动。
3. **部署物料**（`deploy/`）：Linux amd64/arm64 静态二进制（CGO 关闭，
   任何发行版直接跑）、systemd unit、一键安装脚本 `install.sh`。

## v0.2

1. **流量统计**：设置中开启统计并 apply 后，渲染注入 sing-box `clash_api` /
   xray `StatsService`（本地 dokodemo :10085）/ mihomo `external-controller`，
   后台每 30s 采样每用户上/下行增量，累计进 `traffic_counters`（总量）与
   `traffic_daily`（按日），订阅 header `subscription-userinfo` 输出真实用量。
2. **到期/配额自动禁用**：巡检循环发现到期或超配额用户即停用，
   自动走 apply 流水线重新渲染受影响内核（有校验保护，失败不影响运行配置），
   并记入修订历史。
3. **ACME + Reality 工具**：lego 实现 Let's Encrypt HTTP-01 自动签发/续期，
   证书落盘 `data/certs/<domain>/fullchain.pem + privkey.pem`（签发期间临时占用 :80）；
   `POST /api/tools/reality-keypair`（调内核 x25519 生成）、`GET /api/tools/uuid`。
4. **面板自更新**：`POST /api/self-update` 下载新二进制 → 可选 sha256 校验 →
   备份为 `*.bak` → 原子替换 → `syscall.Exec` 无缝重启；数据/WAL 不受影响。

> 注：已修复"schema 版本号未落库导致重启后迁移重跑"的 bug；
> 迁移执行器对 `ALTER TABLE ADD COLUMN` 做列存在跳过，兼容存量库。
