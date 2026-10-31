# Triune Panel

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

# 运行（默认端口 2087，默认账号 admin/admin）
./triune-panel -addr :2087 -data ./data

# 交叉编译 Linux 服务器
GOOS=linux GOARCH=amd64 go build -o triune-panel-linux ./cmd/server
```

1. 登录 → **设置**：填三个内核的二进制路径（未填的内核跳过校验但功能可用）
2. **入站**：创建入站（默认"停用"，确认无误后启用）
3. **用户**：创建用户并勾选入站，获得订阅链接 `/sub/<token>`
4. **应用/修订**：先"预检"（渲染+校验不生效），再"应用"（校验→原子替换→重启内核）

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

## 部署到 VPS

### 一键部署（推荐）

在 VPS 上执行一条命令（自动识别架构，下载二进制，配置 systemd 并启动）：

```bash
curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/install.sh | sudo bash
```

GitHub 直连慢时脚本会**自动依次尝试多个加速镜像**（ghfast.top / gh-proxy.com / ghproxy.net / gh-proxy.net / gh.ddlc.top），无需手动配置；也可指定镜像：`| sudo GH_PROXY=https://ghfast.top bash`。

指定架构：`| sudo bash -s -- arm64`。重复执行即**原地升级**（只替换面板二进制，`/opt/triune-panel/data` 数据原样保留）。

安装完成后：

- 访问 `http://<服务器IP>:2087`（默认 `admin/admin`，**立即改密码**）
- 日志：`journalctl -u triune-panel -f`
- 下一步：**设置 → 组件更新与版本管理 → 一键安装内核**（sing-box / xray / mihomo 自动下载到面板专用目录，不碰系统现有二进制）

### 一键卸载

```bash
# 默认保留数据目录（可随时重装恢复全部数据）
curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh | sudo bash

# 连数据一起彻底删除（不可恢复）
curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/uninstall.sh | sudo bash -s -- --purge
```

卸载会自动停止面板及其托管的内核进程、删除 systemd 服务单元与二进制；交互终端下会二次确认是否删除数据，管道执行默认保留。

### 手动部署

```bash
# 1. 下载二进制（或上传本地 deploy/ 目录）
curl -LO https://github.com/yunyu697/triune-panel/releases/latest/download/triune-panel-linux-amd64
chmod +x triune-panel-linux-amd64 && sudo mv triune-panel-linux-amd64 /usr/local/bin/triune-panel

# 2. 运行（也可自行编写 systemd unit，参考 deploy/triune-panel.service）
sudo triune-panel -addr :2087 -data /opt/triune-panel/data
```
