# Triune Panel

[![CI](https://github.com/yunyu697/triune-panel/actions/workflows/ci.yml/badge.svg)](https://github.com/yunyu697/triune-panel/actions/workflows/ci.yml)

三内核（**mihomo / sing-box / xray**）统一管理面板，Go 单二进制 + 内置 Web UI。

> 下载：[Releases](https://github.com/yunyu697/triune-panel/releases) ·
> 离线演示：`demo.html`（浏览器直接打开，内置模拟数据，可交互体验全部功能）

## 核心目标：升级 / 变更永不导致配置失效

| 防线 | 机制 |
|---|---|
| 1. 意图/产物分离 | 数据库只存"用户意图"（入站、用户、透传字段），运行时配置文件是每次从 DB 渲染的**产物**，面板升级不碰产物 |
| 2. 应用前强制校验 | 渲染结果先写临时文件，调用内核自带校验（`sing-box check` / `xray run -test` / `mihomo -t`），**失败即终止，运行中配置零改动** |
| 3. 原子替换 | 校验通过后 `os.Rename` 原子替换，不存在半写状态 |
| 4. 自动备份 + 回滚 | 每次替换前自动备份到 `data/revisions/<core>/`，一键回滚 |
| 5. 修订审计 | 每次渲染的完整配置作为 `ConfigRevision` 存 DB，任何变更可追溯 |
| 6. 迁移只增不删 | schema 版本化、前向兼容；未知字段以 JSON 原样保留，升级不丢数据 |

## 快速开始

```bash
# 编译（需 Go 1.23+，国内加速：export GOPROXY=https://goproxy.cn,direct）
go build -o triune-panel ./cmd/server

# 运行（默认端口 10010；默认账号 admin/admin，首次登录会强制要求修改）
./triune-panel -addr :10010 -data ./data

# 交叉编译 Linux 服务器
GOOS=linux GOARCH=amd64 go build -o triune-panel-linux ./cmd/server
```

1. 登录 → **设置**：填三个内核的二进制路径（未填的内核跳过校验但功能可用）
2. **入站**：创建入站（默认"停用"，确认无误后启用）
3. **用户**：创建用户并勾选入站，获得订阅链接 `/sub/<token>`
4. **应用/修订**：先"预检"（渲染+校验不生效），再"应用"（校验→原子替换→重启内核）

## 忘记密码

面板不提供网页端"找回密码"（那等于把账号入口暴露到公网，而且 SMTP 配置本身就在面板里）。
忘记密码时在服务器上直接重置——**需要 SSH 权限，这本来就是面板的最高权限**：

```bash
# 方式一：已安装的面板（systemd）
sudo /opt/triune-panel/triune-panel reset-password

# 方式二：不用记路径，走安装脚本直通
curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/install.sh \
  | sudo bash -s -- --reset-password

# 方式三：自定义密码（避免命令行留下历史，走标准输入）
echo 'MyNewPassw0rd' | sudo /opt/triune-panel/triune-panel reset-password --password-stdin
```

默认行为：

| 行为 | 说明 |
|---|---|
| 生成 16 位随机密码并打印一次 | 字符集去掉 `0O1lI` 等易混淆字符；`--password-stdin` 可自定义 |
| **同时关闭登录二次验证** | 丢手机和忘密码常是同一场事故，否则改完密码仍进不去；要保留加 `--keep-2fa` |
| 所有已登录会话立即失效 | 服务端换会话纪元（`session_epoch`），旧 token 立刻 401，无需重启 |
| 密码以 bcrypt 哈希入库 | v0.8.7 起不再明文存储；旧版遗留的明文密码在下次登录时自动升级 |

> 重置命令直接改 SQLite，**面板可以继续运行**（WAL + busy_timeout），不用停服务。

## 命令行工具

`triune-panel` 除启动服务外还带几个一次性运维命令（都不依赖面板进程在跑）：

```bash
triune-panel status [--json]     # 版本/数据目录/入站/用户/内核/证书/上次应用/最近登录
triune-panel doctor              # 环境自检：数据目录、DB 完整性、密码状态、内核、防火墙、端口监听、代理变量、磁盘
triune-panel backup [--out DIR] [--keep N]   # 打包数据目录（默认 <data>/backups，保留 7 份，自动排除 backups 自身）
triune-panel restore <归档.tar.gz> [--yes]   # 用备份覆盖数据目录（恢复前自动备份当前数据）
triune-panel reset-password      # 见上一节
triune-panel version
```

### 备份与恢复

`backup` 与 `restore` 是一对，建议在动配置前先手动备份一次：

```bash
sudo triune-panel backup                       # → data/backups/triune-data-<时间>.tar.gz
sudo systemctl stop triune-panel               # 恢复前必须停服务
sudo triune-panel restore data/backups/triune-data-20261031-205341.tar.gz
sudo systemctl start triune-panel
```

`restore` 有四道保险：

| 保险 | 说明 |
|---|---|
| 拒绝运行中恢复 | 检测 10010 端口是否仍在监听——daemon 持有旧 db 句柄，替换后它会继续写被移走的旧 inode，造成"数据分裂"；确认无误可 `--force` 跳过 |
| 恢复前自动备份 | 先把当前数据打一份到 `backups/`，恢复错了还能回去（`--no-backup` 可关） |
| schema 版本校验 | 只读解析归档里的 `triune.db`；归档比当前二进制**新**就拒绝（旧二进制打开新库会缺列）；更旧则允许，启动时自动迁移 |
| 路径穿越防护 | 归档里的绝对路径与 `..` 条目直接拒绝；`backups/` 不参与替换，历史备份不会被恢复清掉 |

失败会自动回滚到恢复前状态（旧数据先移到 `.pre-restore-<时间>`，成功后才删除）。


- **数据目录自动探测**：`--data` > 环境变量 `TRIUNE_DATA` > `./data` > `/opt/triune-panel/data`，
  所以 `sudo triune-panel status` 在任意目录下都能找到 systemd 安装的实例。
- **`--json`** 便于脚本/监控取值；退出码 `0` 成功、`1` 执行失败、`2` 用法错误
  （`doctor` 有 `fail` 项时返回 1，可直接接告警）。
- **刻意不做** `apply` / 切换内核这类命令：内核进程由 daemon 托管，CLI 裸跑会抢端口、
  让 daemon 的内存状态与磁盘不一致，必须走面板的 API（`/api/apply`、`/api/switch-core`）。
- **CI**（`.github/workflows/ci.yml`）：每次推送跑 `go vet` + `go test` + 三平台交叉编译
  + 二进制 `-version` 格式校验（install.sh 靠 `awk '{print $2}'` 取版本做终检，
  格式一变安装脚本就会误判"装到旧版本"）+ shellcheck 扫 `deploy/` 脚本。

## 反向导入现有链接（迁移不失效）

**导入链接** 页支持粘贴 `vless://` `vmess://` `trojan://` `ss://` `hysteria2://` `tuic://`
（可多条，也支持整段 base64 订阅内容），两种模式：

- **外部节点**：只存进订阅聚合，不生成本地配置、不触碰任何现有服务器 —— 零风险
- **本地入站草稿**：生成本机入站（`draft` 状态，不参与渲染），确认后启用并 apply，
  校验通过才真正生效 —— 迁移期内现有配置始终在线

## 订阅输出

`GET /sub/<token>`（按 User-Agent 或 `?target=` 参数）：

- 默认：base64 节点列表
- `?target=clash`：Clash/mihomo YAML（含 proxy-groups）
- `?target=singbox`：sing-box outbounds JSON

节点公开地址在 **设置 → 节点公开地址** 配置（如 `vps.example.com`）。

## 架构

```
cmd/server          入口（flag: -addr -data）
internal/model      数据模型（Inbound/Client/ExternalNode/ConfigRevision）
internal/store      SQLite（modernc 纯 Go 驱动）+ 版本化迁移
internal/core       内核抽象 Core 接口 + 渲染流水线（校验→原子替换→备份）
  ├─ singbox        sing-box renderer
  ├─ xray           xray renderer
  └─ mihomo         mihomo renderer（listeners 模式，vless 入站不支持会明确报错）
internal/link       分享链接反向解析 + 订阅导出（vless/vmess/trojan/ss/hy2/tuic）
internal/proc       内核进程 supervisor（启停/重启/日志）
internal/api        HTTP API + 订阅服务
internal/webui      go:embed 内置单页 UI（data/web/index.html 可覆盖）
```

## 目录约定

```
data/
  triune.db                 真相源（用户意图）
  cores/<core>/config.*      渲染产物 + 内核日志
  revisions/<core>/*.bak     每次应用前的自动备份
  web/index.html             可自定义的前端（覆盖嵌入版）
```

## 当前状态与后续计划

MVP 已验证：登录鉴权、入站/用户 CRUD、双模式导入、dry-run 预检、
真实 sing-box 校验 + 原子应用 + 进程托管、三种订阅输出、修订历史。

v0.2 已验证（按序追加的能力）：

1. **流量统计**：设置中开启统计并 apply 后，渲染注入 sing-box `clash_api` /
   xray `StatsService`（本地 dokodemo :10085）/ mihomo `external-controller`，
   后台每 30s 采样每用户上/下行增量，累计进 `traffic_counters`（总量）与
   `traffic_daily`（按日），订阅 header `subscription-userinfo` 输出真实用量。
2. **到期/配额自动禁用**：巡检循环发现到期或超配额用户即停用，
   自动走 apply 流水线重新渲染受影响内核（有校验保护，失败不影响运行配置），
   并记入修订历史；`rate_limit_mbps` 为预留字段（三内核暂无原生服务端每用户限速）。
3. **ACME + Reality 工具**：lego 实现 Let's Encrypt HTTP-01 自动签发/续期，
   证书落盘 `data/certs/<domain>/fullchain.pem + privkey.pem`（签发期间临时占用 :80）；
   `POST /api/tools/reality-keypair`（调内核 x25519 生成）、`GET /api/tools/uuid`。
4. **面板自更新**：`POST /api/self-update` 下载新二进制 → 可选 sha256 校验 →
   备份为 `*.bak` → 原子替换 → `syscall.Exec` 无缝重启；数据/WAL 不受影响。

> 注：已修复"schema 版本号未落库导致重启后迁移重跑"的 bug；
> 迁移执行器对 `ALTER TABLE ADD COLUMN` 做列存在跳过，兼容存量库。

v0.3 已验证：

1. **流量图表**：`GET /api/traffic/daily?days=N&client_id=0` 返回补零的按日
   上/下行序列；流量页内置纯 canvas 柱状图（无外部依赖，离线可用），
   支持近 7/30/90 天切换；用户表增加配额用量进度条。
2. **用户管理增强**：支持编辑配额（GB）、到期时间、凭据、关联入站，
   一键启停；到期/超配额自动禁用联动。
3. **部署物料**（`deploy/`）：Linux amd64/arm64 静态二进制（CGO 关闭，
   任何发行版直接跑）、systemd unit、一键安装脚本 `install.sh`。

v0.5 已验证：

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

v0.6 已验证：

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
7. **入站导出（v0.6.2）**：入站列表新增「导出」按钮，一键为该入站下所有
   有效用户生成 `GET /api/inbounds/{id}/export`：分享链接（复制）、
   Clash/mihomo YAML、sing-box outbounds JSON、入站 JSON（含下载文件名）。
   多用户节点自动以「入站名-用户名」区分；草稿/停用/过期用户不导出。
   同时修复未配置节点公开地址时，回退 Host 带面板端口导致链接地址畸形的
   问题（订阅输出同步受益）。

v0.7 已验证：

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

v0.8 已验证：

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

v0.8.7 已验证：

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

v0.8.6 已验证：

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

v0.8.5 已验证：

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

v0.8.4 已验证：

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

v0.8.2 已验证：

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

v0.8.1 已验证：

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

## 已知限制与未验证项

以下内容**代码已实现但未在真实环境完成端到端验证**，或属于**设计上的边界**。
上线前请按自己的环境确认一遍，出问题时优先看面板日志（`journalctl -u triune-panel -f`）。

1. **ACME 真实签发未验证**（最需要你自测的一项）
   - 代码路径完整：lego + Let's Encrypt HTTP-01，支持自动签发与到期前续期，
     证书落盘 `data/certs/<domain>/fullchain.pem + privkey.pem`。
   - 但开发环境没有公网域名与 80 端口，**尚未对真实 Let's Encrypt 完成一次
     签发 → 续期 → 生效的全流程**。已通过的只是：Reality 密钥生成、设置读写、
     触发逻辑、状态查询（无证书时正确显示为空）。
   - 自测前提：域名 A/AAAA 记录指向本机公网 IP；**80 端口可被面板临时占用**
     （签发期间若 80 被 nginx/caddy 等占用会失败，需先停用或改用其代理转发
     `/.well-known/acme-challenge/`）；云安全组放行 80。
   - 失败时设置页「ACME 证书」卡片会显示 lego 返回的错误详情，可据此排查
     （常见：DNS 未生效、80 不可达、Let's Encrypt 速率限制）。
2. **云厂商安全组不在面板管辖范围内**：面板只能改本机防火墙
   （firewalld / ufw / iptables），阿里云/腾讯云/Vultr 等控制台的安全组需自行放行。
3. **iptables 规则重启不保留**：面板写入的是运行时规则，重启后需重新放行
   （面板启动会对账补放行，前提是面板随系统自启）；ufw / firewalld 规则持久化。
4. **每用户限速未实现**：`rate_limit_mbps` 为预留字段，三个内核均无原生
   服务端每用户限速能力，填了也不会生效。
5. **mihomo 流量统计依赖内核字段**：需 `inboundName` / `inboundUser` 出现在
   mihomo 的连接统计里，不同版本字段可能有差异；代码已做多键防御，
   若某版本统计恒为 0，先升级 mihomo 再排查。
6. **订阅与导出依赖 `public_host`**：未设置时回退为「当前请求的 Host（剥掉面板端口）」，
   所以用内网 IP 或 localhost 访问面板时，生成的节点地址会不可用。
   请在设置页填域名或公网 IP（安装脚本会把探测到的公网 IP 打印在安装结果里，可直接填进去）。

## 部署到 VPS

### 一键部署（推荐）

在 VPS 上执行一条命令（自动识别架构，下载二进制，配置 systemd 并启动）：

```bash
curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/install.sh | sudo bash
```

GitHub 直连慢时脚本会**自动择优**：脚本内置「GitHub 原始地址 + 多个加速镜像」（ghfast.top / gh-proxy.com / gh-proxy.org / ghproxy.cn / gh-proxy.net / ghproxy.net / gh.ddlc.top / ghproxy.link）统一候选池，运行时对**真实 Release 资产**做 Range 探测（剔除返回 200 + HTML 挑战页的假镜像），自动选择实测最快的源下载，失败自动切换次优，无需手动配置；也可指定镜像跳过探测：`| sudo GH_PROXY=https://ghfast.top bash`。

指定架构：`| sudo bash -s -- arm64`；指定版本：`| sudo bash -s -- amd64 v0.8.0`。重复执行即**原地升级**（只替换面板二进制，`/opt/triune-panel/data` 数据原样保留；已安装版本更高时**拒绝降级**）。

#### 防「装到旧版本」（镜像缓存问题）

加速镜像/CDN 会缓存 `releases/latest` 这条 302 重定向——仓库发了新 Release 后，缓存期内镜像仍会把请求导向旧 tag，于是「装到的还是旧版本」。脚本用三道闸保证版本正确：

1. **先解析真实 tag 再下载**：通过 GitHub API（退路：latest 302 的 Location）解析出最新 tag，按 `releases/download/<tag>/<file>` 不可变 URL 下载，**不使用 `releases/latest/download`**；
2. **sha256 完整性校验**：从 Release 资产 digest（退路：随 Release 附带的 `sha256sums.txt`）取得期望值，下载后不符即换下一个源——实测有镜像会返回 HTTP 200 但内容是截断文件或 HTML 挑战页，仅凭状态码无法识别；
3. **版本号终检 + 拒绝降级**：安装前运行 `-version`，必须等于目标 tag；且不得低于已安装版本。

安装成功提示会直接显示**已安装版本**，可立即核对。

安装完成后：

- 访问 `http://<服务器IP>:10010`。**全新安装会自动生成随机管理密码并在安装完成时打印一次**（不再使用默认 admin/admin），请立即保存；首次用默认密码登录时会强制要求修改
- 端口：面板默认监听 **10010**（安装时可用 `TRIUNE_PANEL_PORT=端口` 覆盖）；
  安装脚本会自动检测并放行本机防火墙（ufw / firewalld / iptables），
  **云服务器还需在厂商控制台安全组放行同一端口**（机器内无法代改）
- 仅本机监听：`TRIUNE_LISTEN=local` 安装后面板只绑定 `127.0.0.1`，不暴露公网，
  本机 `ssh -L 10010:127.0.0.1:10010 user@server` 后访问 `http://127.0.0.1:10010`
- 自动备份：安装时自动部署 systemd timer，**每日 03:30 打包数据目录到
  `backups/`（保留最近 7 份）**；手动备份 `sudo bash /opt/triune-panel/backup.sh`
- 公网 IP：安装完成时自动探测显示（全部 `--noproxy` 直连探测，绝不会被
  系统代理污染显示成代理 IP）；探测失败时会给出自查指引
- 日志：`journalctl -u triune-panel -f`
- 下一步：**设置 → 组件更新与版本管理 → 一键安装内核**（sing-box / xray / mihomo 自动下载到面板专用目录，不碰系统现有二进制）；
  有域名时建议 **设置 → ACME 证书 → 一键启用 HTTPS**（首次签发前先看[已知限制与未验证项](#已知限制与未验证项)第 1 条）

> 注意：在线安装依赖匿名访问 GitHub，要求仓库为 **Public**；私有仓库所有源（含镜像）都会返回 404，请将仓库设为 Public，或改用「手动部署」离线安装。

#### 权限说明（避免 `Permission denied`）

安装需要写入 `/opt/triune-panel` 与 `/etc/systemd/system`，这两个位置需要 root。脚本已做健壮化处理，**忘记加 `sudo` 也能装**——特权步骤会逐条交给 `sudo` 执行：

```bash
# 忘记 sudo 也没问题：脚本内部会对写目录/写 systemd/启服务这些动作自动提权
curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/install.sh | bash
```

常见报错与对应处理：

| 现象 | 原因 | 处理 |
|---|---|---|
| `install: cannot create regular file '/opt/triune-panel/triune-panel': Permission denied` | 目录已存在（此前用 root 装过）但当前用户无写权限 | 加 `sudo` 重跑，或让脚本自动提权 |
| 提示当前用户无写权限、且系统无 `sudo` | rootless 容器 | 用 `TRIUNE_INSTALL_DIR` 装到用户目录 |
| 提示二进制无法执行 | 架构不匹配 | 显式指定架构：`... | bash -s -- amd64` |

安装目录可自定义（无需 root，适合 rootless 环境）：

```bash
curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/install.sh \
  | TRIUNE_INSTALL_DIR=~/triune-panel bash
```

自定义目录时 systemd 单元的 `ExecStart` 会自动跟随实际路径；卸载脚本同样支持 `TRIUNE_INSTALL_DIR`。

### 一键卸载

```bash
# 首选：安装时卸载脚本已落地在服务器本地，直接执行（无需联网，不受仓库可见性影响）
sudo bash /opt/triune-panel/uninstall.sh            # 默认卸载，保留数据目录（可随时重装恢复全部数据）
sudo bash /opt/triune-panel/uninstall.sh --purge    # 连数据一起彻底删除（不可恢复）

# 直通：用安装脚本一行卸载（本地有卸载脚本用本地的，没有则自动在线拉取）
curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/install.sh | sudo bash -s -- --uninstall

# 备用：本地脚本已丢时在线拉取（原始地址+镜像写在同一条命令里，逐个尝试自动择优）
for u in \
  https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh \
  https://ghfast.top/https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh \
  https://gh-proxy.com/https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh \
  https://gh-proxy.org/https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh \
  https://ghproxy.cn/https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh; do
  curl -fsSL "$u" | sudo bash && break
done
```

卸载会自动停止面板及其托管的内核进程、删除 systemd 服务单元与二进制；交互终端下会二次确认是否删除数据，管道执行默认保留。

### 手动部署

```bash
# 1. 下载二进制（或上传本地 deploy/ 目录）
curl -LO https://github.com/yunyu697/triune-panel/releases/latest/download/triune-panel-linux-amd64
chmod +x triune-panel-linux-amd64 && sudo mv triune-panel-linux-amd64 /usr/local/bin/triune-panel

# 2. 运行（也可自行编写 systemd unit，参考 deploy/triune-panel.service）
sudo triune-panel -addr :10010 -data /opt/triune-panel/data
```
