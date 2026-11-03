# Triune Panel

[![CI](https://github.com/yunyu697/triune-panel/actions/workflows/ci.yml/badge.svg)](https://github.com/yunyu697/triune-panel/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/yunyu697/triune-panel)](https://github.com/yunyu697/triune-panel/releases)

三内核（**mihomo / sing-box / xray**）统一管理面板 —— Go 单二进制 + 内置 Web UI。
一条命令装完，不需要另配数据库、反向代理或前端构建。

> mihomo 是**兼容内核**：它的协议是 sing-box 的子集，且缺 vless / reality，所以新建入站时
> 默认只提供 sing-box / xray（可在「设置 → 常用」里打开）。已存在的 mihomo 入站照常运行。

> **下载** [Releases](https://github.com/yunyu697/triune-panel/releases) ·
> **离线演示** [`demo.html`](demo.html)（浏览器直接打开，内置模拟数据，所有功能可点） ·
> **更新日志** [CHANGELOG.md](CHANGELOG.md) ·
> **已知限制** [见文末](#已知限制与未验证项)

## 快速开始

**装到服务器**——自动识别架构、下载二进制、配 systemd、放行防火墙、部署每日备份：

```bash
curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/install.sh | sudo bash
```

装完访问 `http://<服务器IP>:10010`。**随机管理密码只在安装结束时打印一次**，请立即保存。
重复执行即原地升级，`/opt/triune-panel/data` 数据原样保留。
（完整选项：指定架构 / 指定版本 / 仅本机监听 / 自定义目录 → [部署到 VPS](#部署到-vps)）

**只想先看看界面**——不用装任何东西，直接打开 [`demo.html`](demo.html)。

**本机编译试用**：

```bash
go build -o triune-panel ./cmd/server            # 需 Go 1.23+；国内加速 export GOPROXY=https://goproxy.cn,direct
./triune-panel -addr :10010 -data ./data         # 默认账号 admin/admin，首次登录强制改密

GOOS=linux GOARCH=amd64 go build -o triune-panel-linux ./cmd/server   # 交叉编译到服务器
```

**上手四步**：

1. **设置** → 填三个内核的二进制路径（没装就点「一键安装」，见下）
2. **入站** → 创建（默认「停用」，确认无误后启用）
3. **用户** → 建号并勾选入站，拿到订阅链接 `/sub/<token>`
4. **应用/修订** → 先「预检」（渲染 + 校验但不生效），再「应用」（校验 → 原子替换 → 重启内核）

内核不用自己装：设置里 **一键安装** 会把 sing-box / xray / mihomo 下载到面板专用目录
`data/bin/<内核>/`，**与系统里现有代理服务的二进制完全隔离**，互不影响。

## 能力一览

| 领域 | 能做什么 |
|---|---|
| **入站** | 三内核随时互切 · 协议按内核联动 · 端口自动放行防火墙 · 连通性自检 · 3x-ui / xray JSON 导入 |
| **用户** | 配额/到期自动停用 · 三种订阅格式 · 二维码 · 令牌重置 · 批量导出 |
| **生成** | mihomo / sing-box · sing-box 版本矩阵 · 规则标签 · 本机内核实校 |
| **保存** | 命名保存（存参数不只存文件）· 随时下载 · 重新生成先比差异 |
| **安全** | bcrypt · 2FA · 防爆破 · 三角色 RBAC · 会话管理 · 90 天审计 |
| **观测** | 流量统计与图表 · 实时速率 · Prometheus · 变更通知 · `/healthz` |
| **运维** | 一键装卸 · 短命令 `tp`（状态/启停/升级/入站/用户/审计，[文档](docs/cli.md)） · `doctor` 自检 · 备份加密与异地 |
| **更新** | 面板与三内核任意版本升降 · 14 个镜像可选可测速 · 失败自动回滚 |
| **界面** | 亮暗 × 8 套配色 · 窄屏可用 · 加到主屏（PWA，需 HTTPS） |

## 核心设计：升级 / 变更永不导致配置失效

这是整个面板的第一原则——改配置、升级面板，都不该把正在跑的服务弄挂。

| 防线 | 机制 |
|---|---|
| ① 意图/产物分离 | 数据库只存"用户意图"（入站、用户、透传字段），运行时配置文件是每次从 DB 渲染的**产物**，面板升级不碰产物 |
| ② 应用前强制校验 | 渲染结果先写临时文件，调用内核自带校验（`sing-box check` / `xray run -test` / `mihomo -t`），**失败即终止，运行中配置零改动** |
| ③ 原子替换 | 校验通过后 `os.Rename` 原子替换，不存在半写状态 |
| ④ 自动备份 + 回滚 | 每次替换前自动备份到 `data/revisions/<core>/`，一键回滚 |
| ⑤ 修订审计 | 每次渲染的完整配置作为 `ConfigRevision` 存 DB，任何变更可追溯 |
| ⑥ 迁移只增不删 | schema 版本化、前向兼容；未知字段以 JSON 原样保留，升级不丢数据 |
| ⑦ 保存期拦矛盾输入 | 端口越界、同内核 tag 重名、**同端口同协议族冲突**在保存时就 400。内核的 `check` / `-test` 只校验语法、不 bind socket，这类问题原本要等内核起不来才暴露——而那时配置已被替换，该内核下所有节点一起断。同端口一 TCP 一 UDP（如 vless 443 + hysteria2 443）仍允许共存 |

## 适用边界（先看这几条）

不想让人装完才发现，所以放在前面。完整 20 条见 [已知限制与未验证项](#已知限制与未验证项)。

- **ACME 真实签发未做端到端验证**：代码路径完整（lego + Let's Encrypt HTTP-01），但开发环境没有公网域名与 80 端口。首次启用 HTTPS 前请按第 1 条自查。
- **每用户限速做不到**：三个内核都没有这个能力。用量控制请用 `quota_gb`（真会被执行）；要限速只能在内核之外做（`tc` / 上游交换机 / 云厂商带宽上限）。
- **云厂商安全组不归面板管**：面板只能改本机防火墙（firewalld / ufw / iptables），控制台要自己放行；iptables 规则重启不保留。
- **会话只存在内存**：面板重启后所有设备要重新登录。这是刻意的——换来「旧 token 不可能残留」。
- **订阅与导出依赖 `public_host`**：没填就回退成请求的 Host，所以用内网 IP / localhost 访问面板时，生成的节点地址不可用。
- **样式仍靠 `'unsafe-inline'` 放行**：脚本已经全部外置、`script-src` 收到 `'self'`，但界面有 300 多处 `style="..."`，`style-src` 还留着 `'unsafe-inline'`。样式注入的危害远低于脚本执行。
- **xray ≥ 26.7.11 的 REALITY 不接受 mihomo / 旧客户端**：这是上游的版本策略（mihomo 把客户端版本硬编码成 1.8.2），不是节点配错。面板自检会明确提示；两条出路见[组件更新](#xray--26711-的-reality-不接受老客户端)。
- **下载镜像与主题列表都会过期**：镜像还可能返回被替换过的文件，要求强保证就填 `sha256` 或干脆走官方地址。

## 目录

**上手**

- [部署到 VPS](#部署到-vps) — 一键部署、镜像自动择优、防「装到旧版本」、权限排错、卸载、手动部署、健康检查
- [命令行工具](#命令行工具) — 短命令 `tp`：状态 / 启停 / 升级 / 入站 / 用户 / 审计，完整参考见 [docs/cli.md](docs/cli.md)，含 [备份加密](#备份加密) 与 [异地备份](#异地备份可选)
- [忘记密码](#忘记密码) — 离线重置，不用登面板

**网络与访问**

- [挂在反向代理后面](#挂在反向代理后面nginx--caddy--traefik) — nginx / Caddy / Traefik 下的真实 IP 与防爆破计数
- [安全响应头](#安全响应头) — CSP、HSTS、点击劫持防护，以及 `script-src 'self'` 是怎么收回来的

**日常使用**

- [导入入站配置](#导入配置3x-ui--xray-的入站定义) · [外部节点](#分享链接--外部节点默认不进订阅) · [订阅输出](#订阅输出)
- [出站与路由](#出站与路由只读) · [配置生成](#配置生成客户端) · [配置管理](#配置管理保存下载重新生成) · [规则标签](#规则标签快捷套用)
- [界面主题](#界面主题亮暗--色彩方案) · [加到主屏](#加到主屏pwa) · [组件更新与下载镜像](#组件更新与下载镜像)

**协作与可观测**

- [管理员与角色](#管理员与角色) · [登录设备](#登录设备会话管理) · [登出与会话](#登出与会话)
- [审计日志](#审计日志) · [变更通知](#变更通知邮件--webhook) · [监控指标](#监控指标prometheus)

**参考**

- [已知限制与未验证项](#已知限制与未验证项) · [架构](#架构) · [目录约定](#目录约定) · [版本历史](#版本历史)

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

安装成功提示会直接显示**已安装版本**与**运行中版本**，两者应当一致。

#### 更新完打开还是旧版本？

先分清是哪种「旧」——**页面上的版本号来自正在运行的进程**（`/api/version` 返回二进制自报的 `Version`），不是磁盘上的文件。所以版本号还是旧的，基本就是**新二进制已经落盘、但跑着的还是老进程**。

**不用管，面板自己会切过去。** 从 1.1.1 起有个守望者盯着自己的二进制：每 3 秒 `stat` 一次，只有 size/mtime 变了才探一次版本（平时不 fork），确认文件稳定地变成另一个版本后，几秒内自己 `exec` 过去，监听与数据无缝衔接。所以「一键脚本 `cp` / 手工 `mv` / `docker cp` / systemd 只换了文件没重启」这些情况都不再需要你手动重启。换进程会清空内存里的登录会话，页面会回到登录页——那是重启的信号，不是出错。

不想让它自己动（容器编排、supervisor 下由外部决定重启时机）：设置 → 运维 →「二进制换了新版本时自动切换」取消勾选，或 `PUT /api/settings {"auto_switch_panel":"0"}`。关掉后 `/api/version` 的 `auto_switch` 为 `"0"`。

**它也会说清楚。** 设置 → 运维 →「面板自更新」里，磁盘二进制与运行进程对不上就亮一条黄框：*「磁盘上的二进制已经是 v1.1.1，但进程仍在运行 v1.1.0」*。开着自动切换时它写「面板正在自动切换…」并每 2 秒查一次（用免认证的 `/api/public/info`，因为换进程会让鉴权接口恰好 401），版本一变就刷新页面；只有关掉开关、或 30 秒还没切成功，才把 `systemctl restart` 亮出来。`/api/version` 同时返回 `binary_version`（磁盘）、`restart_failed`（上次重启失败的说明）、`auto_switch`（开关），脚本里可以直接比。

手动确认：

```bash
# 磁盘上那个文件是什么版本
sudo /opt/triune-panel/triune-panel version

# 进程自报版本（= 页面上显示的那个）
curl -s --noproxy '*' http://127.0.0.1:10010/api/public/info

# 进程跑的是哪个文件：带 " (deleted)" = 文件已被替换，但进程没重启
sudo readlink /proc/$(systemctl show -p MainPID --value triune-panel)/exe
```

| 磁盘 | 进程 | 原因 | 处理 |
|---|---|---|---|
| 新 | 旧 | 进程还没切过去 | 等几秒；仍不动就看黄框里的说明（多半是 exec 被禁，见下） |
| 旧 | 旧 | 压根没装上新 | 看下面两条 |
| 新 | 新 | 只是浏览器/反代缓存 | `Ctrl+Shift+R`；仍不行删 `data/web/index.html` 再重启 |

自更新（面板里点「下载并更新重启」）报错时，这两类原因最常见：

- **`替换失败: … cross-device link`**：数据目录与安装目录不在同一个文件系统（容器把 data 挂成卷、`/var` 是独立分区、data 在 tmpfs）。新二进制会先落到安装目录再原子 rename，所以 1.0.9 之后这个错误不该再出现；更早的版本请用一键脚本升级。
- **`二进制已换成 vX，但重启失败`**：安装目录所在分区 `noexec`，或容器 / systemd 的安全策略禁掉了 `execve`（seccomp、`SystemCallFilter`）。这两种环境下**任何**自重启手段都不可用，`systemctl restart` 即可（systemd 用的是新文件）。守望者同样切不过去，试满 2 次就停手并留说明——不会变成重启风暴。

脚本现在会在装完后回读 `/api/public/info` 核对，对不上**直接报错并给出上面这几条诊断**，不再打印「安装并启动成功」。它同时堵掉了两个容易静默发生的坑：

- **离线模式不再由「当前目录里有同名文件」触发**。`curl … | bash` 时脚本目录会解析成**执行命令时的当前目录**，以前只要那个目录里躺着一个旧的 `triune-panel-linux-amd64`，就会静默装回旧版本（离线模式不联网核对最新 tag，版本比对被跳过）。现在管道执行一律走在线；要离线必须显式 `TRIUNE_OFFLINE=1`，或把二进制和 `install.sh` 放同一目录后直接 `bash install.sh`。
- **替换二进制前确认旧进程真的退出了**，装完用 `systemctl restart` 而不是 `enable --now`——后者对已经在运行的单元是空操作，这正是「二进制换了、进程没换」的来源。
- **停旧进程的依据是「谁在监听端口」，不是「有没有 systemd 单元」**。以前只要单元不存在（手动 `nohup`、docker、openrc、单元名被改过、装了 systemd 但没以它启动），脚本一个进程都不停，紧接着的端口检查必然失败——面板自己占着端口，用户看到的是「端口仍被占用，新版本不会生效」，而脚本从没说过该杀谁。现在先 `systemctl stop` 并**轮询到端口真正释放**（进程收尾期间套接字还在，单次采样必然误判）；仍有占用就按 PID 判断是不是面板自己，是就结束它（无 systemd 时按原参数自动拉起；脚本自己就在容器里则只换文件、提示 `docker restart`），不是才带着可执行文件路径与命令行**中止**，绝不替用户杀无关进程。枚举不到占用者时（非 root、或系统没装 `ss`/`lsof`）会再做一次**不需要权限**的 TCP 探测：端口有人在应答就中止并说明「看不到是哪个进程」，不会误以为端口是空的、把二进制换给一个跑着的旧进程。
- **不跨容器边界动手**：占用端口的进程跑在容器里、而脚本在宿主机上时，脚本**不会**去杀它——那等于停掉整个容器，配了 `restart` 策略的还会被立刻拉起来（端口重新被占），而宿主机上既改不了容器内的二进制，也不该替容器做主。此时会打印可执行路径、命令行、所属单元，并指出做法：容器内升级后重建容器（`docker compose pull && docker compose up -d`），或 `docker exec` 进容器再跑脚本。判据是 `/proc/<pid>/cgroup` 里的 `docker` / `kubepods` / `containerd` / `libpod` / `lxc`；`lxc`、`systemd-nspawn` 这类「整机容器」只认作**占用者**在容器里，脚本自己跑在里面时照常「杀 + 拉起」（那种环境里结束面板不会带走整机）。

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
| 提示二进制无法执行 | 架构不匹配 | 显式指定架构：`... \| bash -s -- amd64` |

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

### 健康检查

`GET /healthz` 免认证、极轻量（一次 `SELECT 1`），供 systemd、uptime 监控、
负载均衡或云厂商健康探针使用：

```bash
$ curl -s http://127.0.0.1:10010/healthz
{"db":"ok","status":"ok","time":"2026-10-31T21:20:00+08:00","uptime_sec":3600,"version":"v0.8.8"}
```

- **200 + `status:ok`**：进程与数据库都正常。
- **503 + `status:degraded`**：进程活着但数据库打不开（盘满、库被删、权限不对），
  此时面板的每个接口都会报错，探针应当报警而不是显示健康。
- 只暴露「是否可用 / 版本号 / 运行时长」，不含任何配置或用户数据；
  版本号本来就在 `/api/public/info` 公开。

systemd 部署时可在启动后自检一次（可选，`deploy/triune-panel.service` 默认不启用）：

```ini
[Service]
ExecStartPost=/bin/sh -c 'for i in 1 2 3 4 5; do curl -fsS --noproxy "*" http://127.0.0.1:10010/healthz >/dev/null && exit 0; sleep 1; done; exit 1'
```

外部监控（Uptime Kuma / 云监控 / Prometheus blackbox）直接探 `/healthz`，
按 HTTP 状态码判活即可，不用带 token。注意 `--noproxy '*'`：本机配了
`http_proxy` 时 curl 会绕道代理，探活结果会骗人。

## 命令行工具

装完之后有个短命令 `tp`（`/usr/local/bin/tp`，指向面板二进制本身）：

```bash
tp                       # 概览（= tp status），绝不启动任何东西
sudo tp restart          # 重启并等 /healthz 就绪才返回
tp in ls                 # 入站列表
tp user link alice       # 直接给出三种客户端的订阅地址
tp audit --since 7d      # 审计日志
tp update --check        # 只看有没有新版

# 写操作：交给面板执行（自动应用配置）
sudo tp user add alice --quota 50 --expires 2026-12-31 --inbounds 1
sudo tp in off 2
sudo tp apply
```

**完整命令参考见 [docs/cli.md](docs/cli.md)。** 里面逐条写了参数、输出样例、退出码，
以及"面板打不开时先看什么"这类场景。

几个要点：

- **只读命令全部直连 SQLite，不经过 daemon**。面板起不来的时候正是最需要查数据的时候，
  这类命令必须还能用。所以 `tp status` / `tp in ls` / `tp user ls` / `tp audit` / `tp doctor`
  在服务全挂的情况下照跑。
- **`tp` 不带参数只会打印状态**。同一个二进制以 `triune-panel` 名字调用时不带参数才是启动服务
  （systemd 的 `ExecStart` 依赖它），两者靠 `argv[0]` 区分。
- **命令名支持唯一前缀**（`tp au` = `tp audit`），但**有歧义时报错不猜**：`tp st` 同时是
  `status` 和 `start`，猜错的代价是"本想看状态，结果把服务启动了"。
- **`tp update` 失败会自动回滚**，回滚也失败会打印备份文件路径。校验不止 sha256——
  还会跑一遍 `-version` 确认拿到的真是面板二进制且版本对得上。
- **面板启动时会自动恢复内核**。内核是面板的子进程，systemd 默认 `KillMode=control-group`
  会把它们一起杀掉，所以重启面板 / 重启服务器之后，面板启动时会按"有启用且非草稿的入站"
  这个口径把该跑的内核拉起来（用上次渲染好的配置，不重新渲染）。
- **写操作交给 daemon 执行，CLI 不碰运行状态**。`in on/off/rm`、`user add/set/rm/token`、
  `apply`、`rollback` 这些会动内核的命令，经 unix 套接字 `/run/triune-panel/control.sock`
  把请求交给面板，由面板改库、渲染配置、重启内核、写审计——和你在面板上点按钮走的是
  同一段代码。原因很简单：内核是面板的子进程，端口和进程句柄都在 daemon 内存里，
  CLI 直接改库会和 daemon 抢端口。
- **控制套接字有三层门**：目录 `0700` + 文件 `0600`（只有 root 与面板用户进得去）；
  连上之后用 `SO_PEERCRED` 读内核记录的对端 uid，只接受 `root` 和面板自己的 uid
  ——这个 uid 是 `connect()` 时内核记的，对端伪造不了、也换不掉；最后是一张**白名单**，
  只挂 9 个写接口，不挂整套 API（否则 peer 校验出一次岔子，后果就从「改错一个入站」
  变成「整个面板被接管」）。取不到对端凭据的平台一律拒绝，不是「取不到就放行」。
- **审计里认得出是谁**：CLI 发来的写操作记成 `cli@root` / `cli:uid1000`，不借用真实
  账号名——否则事后翻日志分不清「面板上点的」和「服务器上敲的」。

旧的完整命令名一个都没删，`triune-panel status` / `doctor` / `backup` / `restore` /
`reset-password` 全部照旧可用（`install.sh` 内部就在用 `reset-password`）。

- **数据目录自动探测**：`--data` > 环境变量 `TRIUNE_DATA` > `./data` > `/opt/triune-panel/data`，
  所以 `sudo tp status` 在任意目录下都能找到 systemd 安装的实例。
- **`--json`** 便于脚本/监控取值；退出码 `0` 成功、`1` 执行失败、`2` 用法错误
  （`doctor` 有 `fail` 项时返回 1，可直接接告警）。
- **CI**（`.github/workflows/ci.yml`）：每次推送跑 `go vet` + `go test` + 三平台交叉编译
  + 二进制 `-version` 格式校验（install.sh 靠 `awk '{print $2}'` 取版本做终检，
  格式一变安装脚本就会误判"装到旧版本"）+ shellcheck 扫 `deploy/` 脚本。

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
| schema 版本校验 | 只读解析归档里的 `triune.db`（连同 `-wal`/`-shm`——WAL 模式库停服后最后一个事务常还在 WAL 里，只解主库会误判成空库）；归档比当前二进制**新**就拒绝（旧二进制打开新库会缺列）；更旧则允许，启动时自动迁移 |
| 路径穿越防护 | 归档里的绝对路径与 `..` 条目直接拒绝；`backups/` 不参与替换，历史备份不会被恢复清掉 |

失败会自动回滚到恢复前状态（旧数据先移到 `.pre-restore-<时间>`，成功后才删除）。

### 备份加密

归档里有 `triune.db`——密码哈希、TOTP 密钥、全部用户凭据、订阅令牌都在里面。
一旦落到对象存储、网盘、另一台机器，等于把面板钥匙一起交出去。加密是可选的：

```bash
sudo triune-panel backup --gen-passphrase     # 随机生成口令，只打印一次
sudo triune-panel backup --encrypt            # 手动输入两次（无回显）
```

输出 `triune-data-<时间>.tar.gz.age`，是**标准 age 格式**（scrypt 口令模式），
不依赖本面板也能解：

```bash
age -d triune-data-20261031-220347.tar.gz.age > 还原.tar.gz
tar -xzf 还原.tar.gz -C /tmp/restore
```

`restore` 按文件头魔数（`age-encryption.org/v1`）自动识别，**不看扩展名**——
改名的加密包不会当成明文去解，反过来也一样：

```bash
sudo triune-panel restore triune-data-20261031-220347.tar.gz.age --passphrase-file /etc/triune-panel/backup.pass
```

口令的四种给法（优先级从高到低）：

| 方式 | 场景 |
|---|---|
| `--passphrase-file FILE` | systemd timer / cron 无人值守（文件建议 `chmod 600`） |
| `--passphrase-stdin` | `pass show x \| triune-panel backup --passphrase-stdin` |
| `TRIUNE_BACKUP_PASSPHRASE` | 容器环境变量注入 |
| 交互式无回显 | 手动运维 |

几点硬约定：

- **口令不接受命令行明文参数**（`--passphrase xxx` 会留在 shell 历史与
  `/proc/<pid>/cmdline` 里），只认上面四种。
- 口令至少 12 个字符；太短直接拒绝，且**不会留下半个密文文件**。
- **口令丢失 = 归档永久打不开**。age 没有后门，面板也没有恢复通道，
  请把口令存进密码管理器，别和归档放同一台机器。
- 恢复加密归档时，`restore` 的"恢复前自动备份"会沿用同一口令，
  不会悄悄降级成明文备份。
- 明文与加密归档可以混存，`--keep` 轮转按时间戳排序，一视同仁。

WAL 模式的一个提醒：面板运行中 `triune.db-wal` 里可能还有未合并的事务，
`backup` 会把它们一起打包（恢复时 SQLite 自动重放），但直接拷运行中的库
理论上可能拿到不一致快照。`backup` 检测到未合并 WAL 会打印提示——
要严格一致就先停服再备份。

### 异地备份（可选）

加密解决的是「备份被人看到」，异地解决的是「备份跟着磁盘一起没」。
两者独立，建议都开——备份和数据库躺在同一块盘上，磁盘坏、VPS 被回收、
误删整机，加密再强也救不回来。

走 [rclone](https://rclone.org)，它已经处理好 S3 / R2 / B2 / WebDAV / SFTP /
各类网盘的签名与分片差异，不用为每家对象存储写一套客户端：

```bash
# 一次性：装 rclone 并配好远端（以 root 身份，systemd 也用它）
curl https://rclone.org/install.sh | sudo bash
sudo rclone config

# 之后每次备份加 --upload
sudo triune-panel backup --upload r2:triune-backups
```

远端写法就是 rclone 的 `remote:path`：

| 远端 | 写法 |
|---|---|
| Cloudflare R2 | `r2:triune-backups` |
| Backblaze B2 | `b2:my-bucket/triune` |
| S3 / MinIO / 阿里云 OSS | `s3:my-bucket/backups` |
| 任意 SSH 机器 | `myserver:/srv/backups/triune` |
| WebDAV / 坚果云 | `webdav:triune` |
| Google Drive / OneDrive | `gdrive:triune` |

**上传后会回读校验**：传完再用 `rclone lsjson` 读回远端对象的字节数，
与本地归档比对，不一致直接报错。备份最怕"看起来传上去了"——
网络抖动导致的截断在 rclone 退出码里不一定体现。

远端轮转要显式开：

```bash
sudo triune-panel backup --upload r2:triune-backups --upload-keep 7
```

`--upload-keep` 为 0（默认）时**不动远端任何文件**。设了之后只删
`triune-data-*.tar.gz[.age]` 这类本包命名的归档，远端目录里别的文件一律不碰——
你很可能把备份传进一个共用目录，误删别人的东西是不可接受的。

上传失败时退出码为 1（systemd timer 会报红），但会明确告诉你
**本地归档仍然可用**——不要把"没传上去"当成"已备份"。

每日自动备份开启异地只需一步：在安装目录写一个 `backup.env`

```bash
# /opt/triune-panel/backup.env
TRIUNE_BACKUP_REMOTE=r2:triune-backups
TRIUNE_BACKUP_REMOTE_KEEP=7
```

`backup.sh` 会自动读取（配合 `backup.pass` 就是「加密 + 异地」全套）。

从远端恢复：先拉回来再按普通流程恢复，`restore` 不需要知道远端的存在。

```bash
rclone copy r2:triune-backups/triune-data-20261101-030000.tar.gz.age ./
sudo triune-panel restore ./triune-data-20261101-030000.tar.gz.age --passphrase-file /etc/triune-panel/backup.pass
```

## 忘记密码

面板不提供网页端"找回密码"（那等于把账号入口暴露到公网，而且 SMTP 配置本身就在面板里）。
忘记密码时在服务器上直接重置——**需要 SSH 权限，这本来就是面板的最高权限**：

```bash
# 方式一：已安装的面板（systemd）
sudo tp passwd                       # 等价于下面那条，tp 是安装脚本建的软链

# 方式一之完整路径（没装 tp 软链时）
sudo /opt/triune-panel/triune-panel reset-password

# 方式二：不用记路径，走安装脚本直通
curl -fsSL https://raw.githubusercontent.com/yunyu697/triune-panel/main/deploy/install.sh \
  | sudo bash -s -- --reset-password

# 方式三：自定义密码（避免命令行留下历史，走标准输入）
echo 'MyNewPassw0rd' | sudo tp passwd --password-stdin
```

默认行为：

| 行为 | 说明 |
|---|---|
| 生成 16 位随机密码并打印一次 | 字符集去掉 `0O1lI` 等易混淆字符；`--password-stdin` 可自定义 |
| **同时关闭登录二次验证** | 丢手机和忘密码常是同一场事故，否则改完密码仍进不去；要保留加 `--keep-2fa` |
| 只影响**超级管理员**账号 | 改的是 `admin_users` 里那条 `owner`；`--user` 可顺带把 owner 改名 |
| 该账号的会话立即失效 | 密码哈希变化 → 指纹不匹配，旧 token 立刻 401，无需重启；其他管理员不受影响 |
| 密码以 bcrypt 哈希入库 | v0.8.7 起不再明文存储；旧版遗留的明文密码在下次登录时自动升级 |

> 重置命令直接改 SQLite，**面板可以继续运行**（WAL + busy_timeout），不用停服务。

## 面板 HTTPS（没有域名也能加密）

面板可以自己签一张证书直接以 TLS 监听，**不需要域名、不需要公网 80/443、不需要反代**：

**设置 → 安全 → 面板 HTTPS → 勾选「启用 HTTPS」→ 保存 → 重启面板**，
之后用 `https://<服务器IP>:<端口>` 访问。

首次启用时会在 `data/tls/` 下生成 `panel.crt` / `panel.key`（ECDSA P-256，
有效期 825 天，到期前 30 天自动续签）。证书的 SAN 包含本机全部网卡 IP、
`127.0.0.1`、`::1`、`localhost` 与主机名，用 IP 或主机名访问都对得上。

### 它解决什么、不解决什么

| | 自签 HTTPS | 明文 HTTP |
|---|---|---|
| 链路上抓包能拿到密码 / Bearer token | 不能 | 能 |
| 中间人能用另一张自签证书冒充（DNS 劫持等） | **可能** | 本来就没有身份概念 |
| 浏览器是否警告 | **会**（需手动信任） | 不会（但不安全） |

自签只保证「路上没人看得懂」，不保证「对面就是这台机器」。所以界面里把证书的
**SHA-256 指纹**直接摊开——在手机 / 电脑上安装并信任证书之前，先对照这串指纹，
这是自签唯一能落地的人工认证步骤。不做这一步，自签就只是「习惯性点继续」。

### 想彻底没有警告

两条路，都比自签严格：

1. **有域名** → 面板自带 ACME（Let's Encrypt HTTP-01），用域名签发真证书；
2. **只有 IP** → 买能签 IP 的证书（商业 CA 的 IP 证书，或 Let's Encrypt 2025 年起的
   短周期 IP 证书），把公钥 / 私钥覆盖到 `data/tls/panel.crt` 与 `data/tls/panel.key`。

第 2 种情况面板会识别出「这不是我自己签的」（靠 `data/tls/panel.crt.selfsigned` 标记），
**永不覆盖、永不重签**，升级和重启都原样使用。

### 注意事项

- **改完必须重启才生效**，当前页面不会自动切协议；重启后旧页面的请求会 401，重新打开即可。
- 启用后 `curl http://127.0.0.1:<端口>` 会返回 400（Go 的「HTTP 请求打到了 HTTPS 端口」提示），
  自检请用 `curl -k https://127.0.0.1:<端口>/healthz`。
- `tp start` / `tp restart` 的健康检查与一键脚本的版本核对都已兼容两种协议（先 https 后 http），
  开了 HTTPS 不会把「已就绪」误判成「起不来」。
- 证书生成失败时面板**拒绝以明文启动**（`systemd` 起不来 + 日志给出恢复命令），
  不做「静默降级」——那会让人以为在加密、实际在明文传密码。
- 前面已经有 nginx / Caddy 做 TLS 时，不要重复开这个开关。

## 挂在反向代理后面（nginx / Caddy / Traefik）

面板前面套一层反向代理时，TCP 直连对端永远是代理自己（本机 nginx 就是 `127.0.0.1`），
于是「最近登录 IP」和登录防爆破的计数键都会退化成同一个地址 ——
**一个人输错五次密码，所有访客一起被锁 10 分钟**。

在 **设置 → 基础设置 → 可信反向代理网段** 填入代理地址即可（也可直接写设置
`trusted_proxies`）：

| 场景 | 填什么 |
|---|---|
| 代理与面板同机（nginx / Caddy） | `127.0.0.1` |
| 代理在 Docker / 内网另一台机 | 代理所在网段，如 `172.17.0.0/16` 或 `10.0.0.0/8` |
| 多级代理（CDN → nginx → 面板） | 各层代理网段全填，逗号分隔 |

支持 CIDR 与裸 IP（裸 IP 等价于 `/32`），逗号/空格/分号/换行分隔，**任一项写错直接报错**，
不会静默忽略。

取值规则：

- 直连对端**不在**可信网段内 → 一律用直连对端，完全不看转发头。
  所以公网攻击者直接打面板时伪造 `X-Forwarded-For` 无效。
- 直连对端可信 → 把 `X-Forwarded-For` 当作「客户端, 代理1, 代理2…」，
  **从右往左**跳过可信代理，取第一个不可信的地址（右侧由最近一跳代理追加、不可伪造；
  左侧可被客户端随意伪造，所以不能取最左）。没有 XFF 时再看 `X-Real-IP`。

对应的 nginx 配置（关键是 `proxy_set_header`，缺了它们转发头根本不会出现）：

```nginx
location / {
    proxy_pass http://127.0.0.1:10010;
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

Caddy 默认就会带 `X-Forwarded-For`，同样把 `127.0.0.1` 填进可信网段即可。

## 安全响应头

面板对所有响应统一下发一组浏览器侧防护头（`internal/api/security.go`），
不依赖你在 nginx 上另配：

| 响应头 | 值 | 作用 |
|---|---|---|
| `Content-Security-Policy` | 见下 | 禁止加载外部资源、禁止跨源外发数据、禁止被 iframe 套用 |
| `X-Frame-Options` | `DENY` | 防点击劫持（老浏览器兜底） |
| `X-Content-Type-Options` | `nosniff` | 禁止 MIME 嗅探，避免回显内容被当 HTML 执行 |
| `Referrer-Policy` | `no-referrer` | 不把面板地址通过 Referer 泄漏给外部站点 |
| `Permissions-Policy` | `camera=(), microphone=(), geolocation=(), payment=(), usb=()` | 面板不需要任何设备能力 |
| `Cross-Origin-Opener-Policy` | `same-origin` | 跨源打开时不给 `window.opener` |
| `Strict-Transport-Security` | `max-age=15552000` | **仅在确实走 HTTPS 时下发**（见下） |

CSP 的实际内容：

```
default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline';
img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self';
worker-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self';
frame-ancestors 'none'
```

`manifest-src` / `worker-src` 本来会分别回落到 `default-src` 与 `script-src`
（都是 `'self'`，PWA 能正常工作），显式写出来是为了将来收紧 `default-src` 时
不会连带把「加到主屏」弄哑——那种故障的表现只是手机上装不了，很难归因。

**`script-src` 是 `'self'`，没有 `'unsafe-inline'`。** 这件事不是一开始就能做到：
早先界面是单个 HTML，脚本内嵌、交互全靠 `onclick="fn(...)"` 这类内联属性，只能靠
`'unsafe-inline'` 放行——而那一项同时放行了**注入的** `<script>`，等于最后一道
执行型防线是空的。后来分两步收回来的：

1. 脚本从 HTML 里搬出去，成为 `internal/webui/qr.js` 与 `app.js`，经
   `/assets/qr.js`、`/assets/app.js` 同源下发（`embed` 进二进制，带 ETag）；
2. 全部内联事件属性换成 **document 级事件委托**：按钮上只留
   `data-act="fn"` / `data-args='[...]'` / `data-enter` / `data-el`，由 `app.js`
   里一个 `click` / `change` / `input` / `keydown` 监听器统一分派。动态参数走
   `escJson()` 生成，属性值里不会因为用户名带引号而炸掉。

现在内置 UI 里既没有内联 `<script>`，也没有内联事件属性——`security_test.go`
的 `TestCSPNoInlineScriptForBuiltinUI` 直接扫 `index.html` 字节来守这条，
谁再塞一个 `onclick` 进去就会红。

**没有用 nonce，也不用**。脚本全部同源外置之后 nonce 没有意义：它唯一的作用是
放行内联 `<script>`，而一旦出现，注入者只要能把 nonce 读出来（同源页面上就有）
就又能执行；同时按 CSP3，`script-src` 里有 nonce 时浏览器会**忽略**同一列表里的
`'unsafe-inline'`，行为变得难以推理。所以用例里有一条反过来断言「不许出现
`nonce-` / `sha256-`」。

`style-src` 仍保留 `'unsafe-inline'`：界面有 300 多处 `style="..."` 和一个内联
`<style>` 块，拆出去的收益远小于风险（样式注入也读不到 token）。

除此之外，仍然保留「把 XSS 变现路径掐死」那几条：`connect-src 'self'` 让注入脚本
连 `fetch`/`XHR`/`WebSocket` 都发不到外部域名（token 偷出去也没地方送），
`form-action 'self'` 堵表单外发，`base-uri 'none'` 防 `<base>` 改写相对 URL，
`frame-ancestors 'none'` 防点击劫持。

**自定义 UI 会自动放宽。** `data/web/index.html` 允许被换成用户自己的构建产物
（这是有意的功能）。服务端下发 HTML 前会扫一遍：只要发现「没有 `src` 的
`<script>`」或内联事件属性，这一份响应就改用带 `'unsafe-inline'` 的旧策略，
否则一收紧就把别人的界面打成白屏。内置 UI 永远走严格版。

**HSTS 的下发条件**：直连看 `r.TLS`；反代后看 `X-Forwarded-Proto`，但**只采信
可信代理发来的值**——否则攻击者随手加一个 `X-Forwarded-Proto: https` 就能让面板
给纯 HTTP 连接下发 HSTS，把用户后续访问顶到 https 而彻底打不开。

刻意**不下发** `Cross-Origin-Resource-Policy`：`/sub/<token>` 会被各种订阅客户端
拉取，其中一些是浏览器里的 Web 面板，CORP 会把它们挡掉，收益小于风险。

## 导入配置（3x-ui / xray 的入站定义）

建入站只有一条路：**入站 → + 新增入站**，在弹窗顶部展开「从 JSON 填入」，粘贴 JSON
（或选文件）后点「解析并填入」，面板把这条入站**逐项回填**到下面每个表单项——
名称、端口、协议、传输、TLS / Reality、路径与 Host、KCP seed……都填好，你核对或改完再保存。

为什么做成「填表」而不是「直接入库」：JSON 是别人家的服务端定义，端口是否和本机冲突、
Reality 私钥有没有带过来、证书路径在本机存不存在，这些只有你看得出来。把值摊在表单里，
每个字段的来源和后果都是可见的；直接写库则会得到一批「看着导入成功、一启用就报错」的入站。

支持三种输入形态：

- **3x-ui / x-ui 的入站导出**：数组，`settings` / `streamSettings` 是**字符串**形式的 JSON；
- **xray 原生配置**：`{"inbounds":[...]}` 对象，字段本身就是 JSON；
- **单个入站对象**（`{"protocol":"vless",...}`）也可以。

JSON 里有多条入站时，弹窗会给出下拉框让你挑一条填入（一次填一条，避免把端口冲突的
一堆入站一股脑塞进表单）。保存出来的入站是 `enabled=false`：不点启用就绝不会进内核配置。

**用户是一起处理的**：JSON 里带 `clients`（`id` / `password` / `accounts`）就原样带出，
连 UUID、密码、配额、到期时间一起填进「用户」预览块；如果 JSON 里**没有用户**，
面板按协议自动生成一个（vless/vmess/tuic 用 UUID，trojan/ss/hysteria2/anytls 用随机密码，
socks/http 开了密码认证才生成 `用户名:密码`，端口转发这类不需要用户的协议则不生成）。
保存时用户和入站**同一个请求**提交——分两步会出现「入站建好了、用户没建上」，
面板上看着和成功一模一样，只是没人能连上。

编辑已有入站时不会自动建用户（避免重复），要加人请到「用户」页。

### 字段映射

支持的协议：`vless` / `vmess` / `trojan` / `shadowsocks` / `socks` / `http` /
`dokodemo-door`（sing-box 下自动改叫 `direct`）/ `wireguard`（原始参数透传，仅 xray）。
传输：`tcp` / `ws` / `grpc` / `h2` / `httpupgrade` / `xhttp` / `kcp` / `quic`，
按目标内核的矩阵落地：

| 传输 | xray | sing-box | 说明 |
|---|---|---|---|
| tcp / ws / grpc | ✅ | ✅ | |
| h2 | ➡️ xhttp | ✅（`type: http`） | Xray 26 已移除独立 h2，导入时转成 xhttp `stream-one` 并提示 |
| httpupgrade | ✅ | ✅ | |
| xhttp | ✅ | ❌ 降级 tcp | sing-box 没有 xhttp |
| kcp | ✅ | ❌ 降级 tcp | |
| quic | ➡️ xhttp | ✅（必须配 TLS） | Xray 26 同样移除了裸 QUIC |

映射规则（服务端视角，字段落在面板的 `settings` 里）：

| 3x-ui / xray | 面板 |
|---|---|
| `streamSettings.network` | `tcp` / `ws`（`path`、`host`→`ws_host`）/ `grpc`（`serviceName`）/ `h2`、`httpupgrade`、`xhttp`（`path`+`host`+`xhttp_mode`）/ `kcp`（`kcp_seed`、`kcp_header`）/ `quic`（`quic_security`、`quic_key`） |
| `security: tls` | `server_name`、`alpn`、`certificate_path`、`key_path`；**内联 PEM** 会被写成 `data/certs/imported/…` 下的文件（私钥 0600） |
| `security: reality` | `handshake_server` + `handshake_port`、`server_names`、`private_key`、`short_ids`（**数组，含空串**）、`spider_x`、`max_time_diff`、`pbk`（公钥，仅供分享） |
| `settings.clients` | 面板用户：`email`→名称、`id`/`password`→凭据、`totalGB`（字节）→`quota_gb`、`expiryTime`（毫秒）→到期时间、`enable`→启用 |
| `settings.accounts`（socks/http） | 面板用户，凭据存成 `用户名:密码` |
| `settings.auth` / `udp` | `account_auth`（`noauth`/`password`）、`udp` |
| `dokodemo-door` 的 `address`/`port`/`network`/`followRedirect` | `dd_address`、`dd_port`、`dd_network`、`dd_follow_redirect` |
| `settings.fallbacks` | 原样保留（vless） |
| `sniffing` / `allocate` | 原样透传（仅 xray 目标内核） |
| `flow`（客户端级） | 提到入站级 `flow`（面板按入站渲染；多值不同时取第一个并提示） |

这个流程**只解析入站本身**。整份 xray 配置里的 `dns` 与 `routing` 不会跟着填表走
（单条入站没有全局段，混进来只会让「填表」变成「猜你要哪一半」）；需要搬全局段时
用批量接口 `POST /api/inbounds/import-json`（`dry_run=true` 先预览），它按内核各存一份：

- xray 目标：原样注入，DNS 照搬、路由规则追加在统计 API 规则之后；
- sing-box 目标：转成 `dns` / `route` 结构，只搬能一一对应的部分
  （`geosite:`/`geoip:` 集合需要 rule-set，会逐条提示未导入）；
- 指向代理出站的路由规则一律**不搬**——面板只有 `direct` 与 `block` 出站，
  照搬会把流量绕进不存在的出站，规则会直接失效甚至断网。

Reality 私钥在 3x-ui 的导出里是**有**的（`privateKey`），所以这类导入是完整的；
只有分享链接才没有私钥——这也是链接不再支持「生成入站」的原因。

**仍然不覆盖的**（提示里逐条说明）：`tunnel` 之外的其它 xray 扩展协议、
mihomo 内核的 DNS/路由（结构完全不同，请手工配）、
`tlsSettings` 里的 `ech`/`ocspStapling` 等冷门开关。

### 分享链接 → 外部节点（默认不进订阅）

**外部节点** 页把分享链接、远端订阅聚合成外部节点：粘贴 `vless://` `vmess://`
`trojan://` `ss://` `hysteria2://` `tuic://`（可多条，也支持整段 base64 订阅），
**只存进订阅聚合**，不生成本地配置。

链接是客户端视角的快照：没有 Reality 私钥（只有公钥 `pbk`）、没有证书、没有多用户。
反推出来的服务端必然缺东西，表现为「导入成功、一启用就报错」，所以这条路径已移除。

**外部节点默认不混进订阅**，页面上有一个显式开关。原因是它是**全局**的：打开后
每个用户的订阅里都会带上这些节点，而它们既不受本机配额约束、也不经过本机内核
（是别人的服务器）。自建自用保持关闭即可；只有做「多订阅合并」（把自己的服务器
和外部订阅并成一个订阅发出去）时才需要打开。开关只影响订阅输出，改动立即生效，
不需要「应用配置」——外部节点从来不会被渲染进服务端配置。

## 订阅输出

`GET /sub/<token>`，格式按 `?target=` 参数或 **User-Agent** 自动判断：

| 客户端 | 拿到的东西 |
|---|---|
| Clash Verge / mihomo / FlClash / Stash | Clash/mihomo YAML（含 proxy-groups） |
| sing-box | sing-box outbounds JSON |
| 其它（v2rayN、Shadowrocket、NekoBox、浏览器） | base64 节点列表 |

也可以强制：`?target=clash` / `?target=singbox`，显式参数优先于 UA。

**同一个地址对所有客户端都可用**——面板界面上给的订阅地址不带参数，靠 UA 分流。
只给 base64 列表时，Clash 系客户端会「拉取失败」，而用户看到的现象是「节点不通」。

**REALITY 节点的导出会补齐四件套**：`sni`（取 `server_names` 第一个）、
`sid`（取 `short_ids` 里第一个非空的）、`fp=chrome`（uTLS 指纹，mihomo 的
`client-fingerprint`）、`pbk`（没填公钥时由 `private_key` 现算）。
缺任何一个都握不上手，而且失败现象完全一样（超时 / 连接重置 / `REALITY
authentication failed`），在客户端侧看不出是哪一项。

订阅内容的构成：**只有本机已启用的入站**（按用户绑定的入站逐个生成）。
外部节点仅在「外部节点」页的开关打开时才追加，见上一节。

节点公开地址在 **设置 → 节点公开地址** 配置（如 `vps.example.com`）。

## 出站与路由（只读）

面板是**服务端**：入站给客户端连，进来的流量只能从本机网卡出去，所以出站是固定的
`direct`（xray 的 `freedom` / sing-box 的 `direct` / mihomo 不写 `proxies` 时的隐式
`DIRECT`），**没有**「选出口」这个语义，界面上也就不提供出站管理。

只有一种情况会出现第二条出站：导入的路由里有 `block` 规则时，面板补一条
`blackhole/tag=block`——否则那条规则会因为找不到出站而让整份配置校验失败。

**出站** 页把当前内核渲染出来的三段摊开给人看（只读，不写盘、不重启）：

| 区块 | 内容 |
|---|---|
| 出站 | 每条出站的 `tag` / 类型 / 来源（内置 or 导入） |
| 路由规则 | 按生效顺序列出，标注来源：面板统计规则在前，导入的追加在后 |
| DNS | 导入的 `dns` 段，没有导入时显示内核默认 |

它的价值在排障：判断「导入的路由规则到底生效了没」「统计规则的顺序对不对」。
页面上不能改任何东西——改配置请走导入或入站表单。

**中转 / 链式代理不支持**：那需要给入站流量指定一个上游出站，三个内核的写法
（xray 的 `outbounds` + 路由 `outboundTag`、sing-box 的 `detour`、mihomo 的
`proxies` + `proxy-groups`）各不相同，而且会和面板的统计规则抢优先级，
不是加个列表就能解决的。

## 配置生成（客户端）

**配置生成** 页把所选入站 × 有效用户展开成节点，按目标（客户端 / 主路由 / 旁路由）
生成 mihomo YAML 或 sing-box JSON。

### 两个内核的写法不能混

同一件事两边拼写不同，混用会让内核在启动时直接拒绝配置：

| | mihomo | sing-box |
|---|---|---|
| 自动测速组 | `type: url-test`（连字符） | `type: urltest`（连写） |
| 规则集格式 | `.mrs` | `.srs` |
| 规则集引用 | `RULE-SET,geosite-cn,DIRECT` | `{"rule_set":["geosite-cn"]}` |

两条路径各有模板，拼写由测试钉住（`TestUrlTestSpelling`）。

### 规则集勾选有推荐策略

规则集目录（`/api/export/catalog`）里每条都带一个 `policy`，勾选时按它起手：
**国内站点与私有网络 DIRECT，广告 / 追踪 / 遥测 / IP 归属探测 REJECT，其余 PROXY**。
107 条里 30 条 DIRECT、8 条 REJECT、69 条 PROXY。

这样勾「中国大陆域名」「拼多多」不会再把国内流量塞进代理，勾「广告域名」也真的是
拦截而不是转发。推荐值只放在后端目录一处，是唯一事实源，前端不自己编。

它只是**起手值**：勾上之后下拉框可以改，改过就以你为准。生成时请求里没带 `policy`
（升级后残留的旧页面缓存、直接调 `/api/export/config` 的脚本）也按目录推荐值走，
不会退回一律 PROXY。

> **升级提示**：保存过的配置里记的是当时的策略，**重新生成不会自动套用新的推荐值**。
> 想让老配置用上新策略，回配置生成页把规则集重新勾一遍再保存。

### 规则集下载源（国内网络的关键一环）

规则集是**客户端**去下载的，不是面板替它下。而 `raw.githubusercontent.com` 在很多
网络下不可达——此时内核日志只会说一句 `initial rule provider geosite-cn error`，
用户看到的现象却是「节点连上了但不能上网」，很难想到问题在规则集。

配置生成页的 **下载源** 下拉就是为此准备的，默认仍是官方直连（老配置行为不变）：

| 源 | 做法 | 实测（2026-11） |
|---|---|---|
| 官方 | 原样 | 200，内容正确 |
| gh-proxy.com | 前缀拼接 | 200，与官方**字节一致** |
| gh-proxy.org | 前缀拼接 | 同上 |
| ghproxy.net | 前缀拼接 | 同上 |
| jsDelivr | `raw.githubusercontent.com/{o}/{r}/{b}/{p}` → `cdn.jsdelivr.net/gh/{o}/{r}@{b}/{p}` | 200，可拉；`@branch` 形式最多缓存 12 小时，规则集更新会滞后一版（不影响使用，规则集本来就按天更新） |
| 自定义前缀 | 自己填 `https://…`，拼在原地址前 | 建议先点「测试」 |

**这几个源是筛过的，不是随手列的一堆。** 同期实测被排除的：`ghfast.top` 对 raw 文件
回 403；`ghproxy.cn` / `gh-proxy.net` / `ghproxy.link` 回 **200 但内容是 HTML 挑战页**；
`gh.ddlc.top` 回 429。后一类最坑——状态码漂亮，浏览器点开也「正常」，内核拿到只会
报格式错误。所以镜像旁边有个 **测试** 按钮：它真的去拉一次规则集，读前 64 KiB 判断
首字节是不是 `<`，把「200 + 网页」这种情况直接指出来。

自定义前缀只接受 `https://`（明文 http 等于让别人改你的分流结果），且**拒绝指向内网**
——否则这个「帮我去拉一下」的按钮就成了内网探针，跳转链也逐跳校验。

**规则集一律直连下载。** 生成的 `rule-providers` 都带 `proxy: DIRECT`，sing-box 侧对应的
`download_detour: "direct"` 一直都在。不写这一项时 mihomo 会按 rules 匹配出站，而最后
一条规则是 `MATCH,PROXY`——于是「下载规则集」要经过节点，首启时节点还没就绪就先失败
一轮。选镜像的动机本来就是「不依赖节点也能拿到规则集」，走节点会把这个前提抵消掉。

### sing-box 要选版本

sing-box 对未知字段是**严格校验**的：多一个字段就是 `FATAL` 起不来，少一个字段
则行为不对（比如 DNS 不回落到代理侧）。所以生成前必须知道目标版本。

实测（真实二进制跑 `sing-box check -c`，2026-11）：

| 版本 | 当前模板 | 说明 |
|---|---|---|
| 1.10.7 | ✗ | `route.rules[0].action: unknown field "action"` |
| 1.11.15 | ✗ | `dns.servers[0].server: unknown field "server"` |
| 1.12.25 | ✓ | |
| 1.13.21 | ✓ | |
| 1.14.4 | ✓ | |
| 1.15.0-alpha.13 | ✓ | `tun.stack` 已废弃，生成时不写 |

结论：**断点只有 1.11 → 1.12 一处**，1.12 / 1.13 / 1.14 的配置逐字节等价。
不要按「每个版本都不一样」去理解——那会写出没人维护得动的分支。

实际差异只有三处：

| | 1.11 | 1.12+ |
|---|---|---|
| DNS 服务器 | `{"tag":"remote","address":"https://8.8.8.8/dns-query"}` | `{"type":"https","tag":"remote","server":"8.8.8.8"}` |
| 域名解析器 | 无，走内核默认 | `route.default_domain_resolver: {"server":"local"}` |
| `tun.stack` | 写 `mixed` | 写到 1.14；1.15 起不写（1.17 移除） |

代码里不按版本号铺 `if-else`，而是把「这份配置用到哪些能力」抽成门限
（`internal/api/sbversion.go` 的 `capsFor`）：将来出现新断点只加一行，生成器不动。

1.10 及以前直接拒绝——没有路由 action，要退回 `block` / `dns` 出站加
`inet4_address`，那是另一套模板，收益不足。1.16 尚未发布，按 1.15 的规则生成，
界面会明说这一点。

### 用本机内核校验

生成结果下方有 **用本机内核校验**：`POST /api/export/verify` 用设置里配置的内核
二进制跑一次官方命令（`sing-box check -c` / `mihomo -t -f` / `xray -test -config`），
把「应该没问题」变成「校验通过」。

三条边界写在返回里，不装作比实际更强：

- 用的是**本机装的那个版本**，未必等于你为客户端选的版本（返回带实际版本号）；
- 只做语法与初始化校验，**不联网**；
- 通过只说明「内核愿意加载它」，不说明节点连得上。

没配内核时如实返回 `skipped: true` 加原因，不假装通过。

## 配置管理（保存、下载、重新生成）

生成一次配置往往要用好几次：手机装一次、路由器装一次、换台电脑再装一次。
每次都回生成器重选一遍入站、规则集、场景，既慢又容易漏选。

**配置生成** 页底部有 **保存配置**：起个名字存进面板，之后到 **配置管理** 页
随时下载。

### 存的是参数，不只是文件

保存时同时存两份东西：

| 存什么 | 干什么用 |
|---|---|
| `content` | 生成的完整配置文本，下载就是它，一个字节都不重算 |
| `params` | 当时的全部选择（入站、用户、规则集、场景、目标、版本……） |

只存 `content` 的话，一周后想加个用户就得从头再选一遍——「当时勾了哪些规则集」
这件事在生成的文本里是拼不回来的。存了 `params`，**重新生成** 就能原样再来一次。

### 重新生成会先比差异

点 **重新生成** 会拿当前面板状态（新加的入站、刚改的密码）重算一份，
然后和新旧内容做比对：

- **完全一样** → 直接提示「配置没有变化」，不打扰你；
- **不一样** → 先给你看差异，让你选「覆盖原记录」还是「另存为新记录」。

不悄悄改掉几个月前存的东西。改过密码之后旧配置会失效，这个决定应该由人来做。

### 列表不带正文

`GET /api/configs` 只返回元信息（名称 / 内核 / 目标 / 大小 / 保存人 / 更新时间 /
`sha256` 前 16 位），**不返回 `content`**。配置动辄几十 KB，一页 200 份就是几 MB，
每次打开列表都拉一遍没有意义。要看正文走 `GET /api/configs/{id}`。

### 中文名下载不乱码

`Content-Disposition` 同时给两套名字：ASCII 回退（`filename="-.json"`）加
RFC 5987 的 `filename*=UTF-8''%E5%AE%B6...`。老浏览器用前者，现代浏览器用后者，
「家宽-主路由.json」在 Safari / Chrome / curl 里都是中文。

### 上限与权限

- 最多 **200 份**，超了返回 400 并提示先删旧的（没有静默淘汰）；
- 单份正文上限 **4 MiB**，`params` 上限 **256 KiB**；
- 整组接口按 **写权限** 档：`viewer` 看不到也下不了（配置里有节点凭据，
  让人下载等于把凭据给出去了）；
- 增删改记审计（`config`），但不进变更通知——存一份配置不是基础设施变更。

## 规则标签（快捷套用）

自定义规则框下面有一排标签。把常用的几行规则存成标签，下次点一下就回来。

### 单选 = 替换，多选 = 追加

| 模式 | 点一个标签的效果 |
|---|---|
| 单选 | **替换**输入框内容（一个标签就是一套完整规则） |
| 多选 | **追加**到现有内容后面（几个标签拼起来用） |

多选模式下再点一次已选中的标签会取消它，并且**只移除这个标签独有的行**——
如果某行别的已选标签也有，那行留着。手写的行如果和标签里的行字面相同，会被
一并移除（去重后分不出来源，这一点在标签的 `title` 里写明了）。

### 重复行自动去掉

无论手输、粘贴、套用标签，还是多个标签叠加，落库和落进配置前都会去重：
去空行、去首尾空白、**保留首次出现的顺序**、丢掉后面的重复行。

服务端也做一遍（`cleanRuleLines`）——接口是公开的，不能让重复行进到内核里
白跑一遍。实测两行 `DOMAIN-SUFFIX,corp.example.com,DIRECT` 存进去只剩一行。

> 顺带修掉一个真 bug：前端去重函数收到**数组**时被 `String()` 转成了逗号连接的
> 一行，多选两个标签后所有规则挤成
> `DOMAIN-SUFFIX,corp.example.com,DIRECT,IP-CIDR,...`。内核解析不了，而且不报错。

### 标签也归写权限

标签里存的就是规则文本，跟配置一样按 `write` 档：只读账号看不到、改不了。

## 界面主题（亮暗 × 色彩方案）

侧栏底部「主题外观」拆成两层：**亮暗**（浅色 / 暗色）与**色彩方案**（7 套）。
两者独立，换亮暗不换风格、换风格不换亮暗。

| 色彩方案 | 浅色主色 | 暗色主色 |
|---|---|---|
| 蔚蓝 | `#3b82f6` | `#5b9cf8` |
| 靛夜 | `#6366f1` | `#818cf8` |
| 森野 | `#16a34a` | `#34d399` |
| 玫瑰 | `#e11d48` | `#fb7185` |
| 暖阳 | `#e08a2e` | `#e8a04a` |
| 雾蓝 | `#5470a8` | `#7f9bd1` |
| 海盐 | `#0f9db8` | `#22b8d4` |

### 为什么以前「显示的颜色和实际不符」

旧版是 8 个一维主题，每个色块上只画了**一个**主色：浅色和曜石都是蓝、雾蓝和
海盐都是青，点下去一个是白底一个是黑底。色块既不代表背景也不代表对比度，
只能靠名字猜。

现在每张方案卡片画的是**真实配色组合**：一张迷你界面（左侧栏条 + 卡片块 +
两行文字 + 强调色按钮），下面列出用到的 6 个颜色（背景 / 侧栏 / 卡片 / 文字 /
主色 / 辅色）小圆点，悬停看色值，再下面是主色与辅色的十六进制。

关键是这些颜色**不是另抄一份常量**——`schemeVars()` 会临时挂一个带
`data-mode`/`data-scheme` 的探针元素，用 `getComputedStyle` 把 CSS 变量读回来
再画。预览和真实主题共用同一份定义，改一处两边一起变，不会再对不上。

### CSS 也按两层写

```
[data-mode="light"]{ 背景/卡片/文字/线/阴影/语义色 }   ← 中性色
[data-mode="dark"]{ … }
[data-mode="light"][data-scheme="solar"]{ 主色/辅色/图表色 + 米黄中性色 }
```

模式层给中性色，方案层给强调色；个别方案会额外微调中性色（暖阳偏米黄、
森野偏墨绿）。方案选择器权重更高，不用关心先后顺序。

### 老用户不会掉回默认配色

旧版把两层揉成一个 `triune_theme`（`light` / `midnight` / `forest` …）。
升级后先读新键，读不到再按 `LEGACY_THEME` 把旧 id 翻译成「模式 + 方案」，
然后清掉旧键。选过午夜的打开还是午夜。

## 加到主屏（PWA）

手机浏览器里打开面板，「添加到主屏幕」后是个没有地址栏的全屏 App。清单、
service worker 与图标都由二进制内嵌（`internal/webui/pwa/`），`/manifest.webmanifest`、
`/sw.js`、`/icons/*` 三条路由免认证直出——不落盘到 `data/web`，所以升级时不会
在数据目录里留残件。

**前提是 HTTPS**：service worker 只在安全上下文里可用。面板挂在
`http://<公网 IP>` 上时能正常用，但不会出现安装入口——代码里对这种情况是静默
跳过的（注册前先判协议），控制台不会报错，所以「找不到入口」时先看地址栏。

**它不提供离线使用**，这是刻意的。service worker 里没有 `caches`，除页面导航
外一律不接管请求：

- 面板每一屏都是登录后才拿得到的实时数据（流量、在线设备、节点状态），
  缓存下来第二天就是错的；
- 缓存 `index.html` 更糟——升级面板后会出现「旧界面调新接口」，字段对不上、
  接口 404，而用户完全不知道自己拿到的是旧页面。

它只做两件事：让浏览器认可「可安装」，以及断网时给一页「连不上面板」的说明。
图标这类真·静态资源交给浏览器 HTTP 缓存（服务端下发 `ETag` + `no-cache`，
没变就 304），比 SW 里再套一层缓存更好查。

`theme-color` 会跟着主题走：`manifest` 里的值只能是静态的浅色，深色主题下
会露出浅色顶栏，所以 `applyTheme()` 每次切换都把 `<meta name="theme-color">`
改成当前的 `--bg`。

## 组件更新与下载镜像

### 面板的版本列表不再需要先配东西

以前点「面板 → 版本列表」会直接 400：必须先在设置里填 `panel_update_index`
或 `panel_repo`。问题是你得先知道该填什么才能用——而这本该是开箱即用的。

现在没配就落到内置的官方仓库（`defaultPanelRepo`），响应里 `source: "default"`
标明「这是内置默认值，不是你配的」。fork 或自建分发在设置里改成自己的仓库即可，
改了就 `source: "github"`。

另外面板自更新只认 URL（不像内核那样能按版本号拼地址），而一个 release 里躺着
四个平台的资产。所以服务端按当前 `GOOS/GOARCH` 挑出对应资产，把直链填进
`url` 返回——前端不用猜资产名。

### 「最新版本」按 semver 排，不按字符串

版本列表的第一条就是弹窗里默认选中的那条，所以排序错了等于「默认推荐了一个旧版本」。
以前预发布段是直接比字符串的：

- 字典序里 `alpha.9` > `alpha.13`（`'9' > '1'`），于是「预发布」分支默认选中的是
  10 月 3 日的 `v1.15.0-alpha.9`，而官方最新是 10 月 24 日的 `v1.15.0-alpha.13`；
- 同一个比较函数还用于 CLI 的升级判断（`update.Newer`），`alpha.13 → alpha.9`
  会被判成"升级"。

现在按 semver 2.0 逐段比较：数字段按数值比、纯数字段低于字母段、前缀全等时段数
少的更低。滚动 tag（mihomo 的 `Prerelease-Alpha`）没有版本号，兜底时间取
`updated_at` 与 `published_at` 的较晚者——它的 `published_at` 永远停在 2024-08，
只有资产刷新时间会跟着新构建前进。

面板不锁定内核版本：版本列表、下载地址、升级判断全部来自官方仓库的实时查询，
装了哪个版本就用哪个版本渲染配置。本文的验证基线（2026-11-02，真实二进制）：

| 组件 | 验证过的版本 |
|---|---|
| sing-box | `1.15.0-alpha.13`（最新预发布）、`1.14.4`（最新稳定） |
| Xray | `26.10.31`（最新稳定） |
| mihomo | `alpha-69c1631`（最新预发布）、`v1.19.33`（最新稳定） |

这三套内核上跑了 27 项「内核 × 协议」矩阵与 3 个切换场景（含端口被占时明确报错），
全部通过。

### 镜像：默认官方，一键测速

装/更新弹窗里多了一行「下载镜像」：

- **默认「官方地址」**，不用镜像。每次打开弹窗都回到官方——上一次选的镜像
  不该悄悄延续到另一次安装。
- 下拉里是 **14 个预设加速站**（`internal/update/mirror.go` 的 `Mirrors`），
  前端不写死任何地址：加速站换域名是常态，改一处比改两处可靠。
- **「自动选择最快」**：并发探测全部候选（含官方地址本身），6 秒超时，
  按「可用 + 延迟」排序，自动选中第一名，并把延迟写在下拉里（`ghfast.top · 186ms`）。
  测完把不可用的从列表里隐藏，只在小字里说「N 个不可用已隐藏」——
  一排「不可用」摆在选项里除了让人犹豫没别的作用。

### 探针必须打真实下载地址

第一版拿 `https://github.com/robots.txt` 当探针，结果 ghfast.top、gh-proxy.com
全部报 403/404——它们其实好好的，只是**只转发 release / raw / archive 这几类
路径**，别的一律拒。用这种探针会得出「所有镜像都挂了」的相反结论。

所以测速目标由调用方给：就是「你接下来真要下的那个文件」
（`{component, version}` 由服务端按官方规则拼地址，或直接给 `url`）。
只取首字节（`Range: bytes=0-0`），10MB 的包也只花一个 RTT。

实测（同一台机器，sing-box v1.14.4）：

| 镜像 | 结果 |
|---|---|
| slink.ltd | ✓ 981ms |
| gh-proxy.com | ✓ 1246ms |
| ghfast.top | ✓ 1369ms |
| 官方地址 | ✓ 2676ms |
| 其余 11 个 | ✗（403 / 429 / 证书过期 / 超时） |

也就是说**镜像确实有用**（快 2.7 倍），但「哪个快」完全取决于你所在地区到它的
链路——所以不写死推荐，让人自己测。

### 镜像不降低任何校验标准

走镜像只改变下载来源，装上去的二进制照样要过：可执行验证 → 用新二进制校验
当前配置 → 备份 → 原子替换 → 重启 + 健康检查。加速站返回一个被替换过的文件
也过不了第一关。确认框里会写明这次走的是哪个镜像，出问题时一眼看得出走的哪条路。

### 接口

| 接口 | 说明 |
|---|---|
| `GET /api/mirrors` | 候选镜像（官方排第一）+ 默认值 + 测速超时 |
| `POST /api/mirrors/test` | `{url}` 或 `{component, version}`，并发测速 |
| `POST /api/cores/{name}/update` | 新增 `mirror` 字段（`""` = 强制官方） |
| `POST /api/self-update` | 同上 |

`mirror` 用**指针**接收：`null`/缺省 = 沿用设置里的 `gh_mirror`，
`""` = 本次强制走官方，`"https://..."` = 用这个。三者语义不同，不能用空串兼职。
测速接口只接受 `https://github.com/` 开头的地址——它会把 URL 交给十几个第三方
站点去请求，放开任意地址等于把面板变成公开的探活器。

### xray ≥ 26.7.11 的 REALITY 不接受老客户端

**症状**：面板上建好 vless + REALITY 入站，用 mihomo / Clash Verge 连不上，
日志里是 `REALITY authentication failed`；同一台 VPS 上 3x-ui 的节点却是通的；
换成 xray 内核的客户端（v2rayN / Shadowrocket）也连不上，但面板自检、
端口监听、TLS 握手全都正常。

**原因不在节点参数上**，在版本线上：

- xray 从 **26.7.11** 起，`realitySettings` 不写 `minClientVer` 时**默认按 26.3.27 判定**；
- 而 mihomo 的 REALITY 实现把客户端版本**硬编码成 1.8.2**（MetaCubeX/mihomo#3039）；
- 服务端于是直接判握手失败，日志只有一句
  `REALITY: processed invalid connection ...: authentication failed or validation criteria not met`。

实测矩阵（同一份订阅配置、同一台机器、同一组 uuid/pbk/sid/sni，2026-11）：

| 服务端 | mihomo 1.19.33 | xray 1.8 客户端 | xray 26 客户端 |
|---|---|---|---|
| xray 1.8.24 | ✅ 362ms | — | — |
| xray 26.3.27 | ✅ 352ms | — | — |
| xray 26.10.31 | ❌ auth failed | ❌ | ✅ |
| xray 26.10.31 + `minClientVer: 1.8.2` | ❌（26.7.11 上加这条能救） | — | ✅ |

**两条出路**（任选其一）：

1. **把 xray 降到 26.3.27 或 1.8.x**：「组件更新 → 版本列表」里选具体版本安装，
   面板会照常做备份与校验；降级后 mihomo 与 xray 客户端都能连。
2. **客户端换 xray 内核**：v2rayN、Shadowrocket、NekoBox 都行——它们上报的版本是真的。
   mihomo 侧目前无解，只能等上游改。

面板的「入站自检」新增了**客户端兼容性**一项：当入站是 xray + REALITY、且磁盘上的
xray ≥ 26.7.11 时会明确提示，并给出上面两条出路——这一类问题最容易把人带进
「反复比对链接参数」的死胡同。

## 管理员与角色

v0.9.6 起面板支持多管理员账号。在这之前只有一个账号（凭据存在 `settings` 里），
审计日志的「操作人」永远是同一个名字——多人运维只能共享密码，
出了问题无法追溯到底是谁改的。

| 角色 | 能做什么 |
|---|---|
| 超级管理员 `owner` | 全部权限，外加管理管理员账号、清空审计、整面板回滚。**唯一**，不能删除 / 降级 / 停用 |
| 管理员 `admin` | 入站、用户、应用与回滚、设置、证书、版本更新、导出、审计查看 |
| 只读 `viewer` | 概览、入站列表、**用户列表（脱敏：无凭据 / 无订阅令牌）**、流量、日志、组件状态、自己的登录设备与登录历史；任何写操作返回 403 |

几个刻意的设计：

- **唯一超级管理员**：`owner` 由数据库迁移播种，接口层不允许新建第二个，也不允许
  删除 / 降级 / 停用。把最后一个 owner 弄掉，面板就永久失去「管理账号」的能力，
  只能靠命令行救回来。
- **账号级密码**：每个账号独立 bcrypt 哈希。改密只让**自己**的会话失效
  （v0.9.5 及以前是全局 `session_epoch`，A 改密码会把 B 一起踢下线）。
- **即时生效**：会话每次请求都回库校验「账号还在、还启用、密码哈希没变」。
  停用或删除一个管理员，他正在用的浏览器下一个请求就 401——不必等 token 过期。
- **越权留痕**：只读账号调写接口返回 403，并记一条 `rbac.deny` 审计。
  反复试探权限边界通常不是手滑，不记的话面板上完全看不出来。
- **脱敏而不是关门**（v0.9.8）：`GET /api/clients` 对 `viewer` 不再 403，而是返回
  把 `credential` / `sub_token` 置空的副本，并带 `masked: true`。只读账号本来就要
  盯用量和配额，把整页关掉只会逼人去求管理员截图；真正敏感的只有那两列。
- **初始密码**：新建账号时密码留空由服务端生成 16 位随机密码，只在创建响应里
  出现一次；列表接口永远不返回密码哈希与 TOTP 密钥。
- **2FA 按人**：TOTP 密钥存在账号上，没绑验证器的人登录时**跳过**这一步，
  不会因为同事绑了 TOTP 就把自己锁在外面；邮箱验证码仍是面板级单收件人。

审计日志因此多了「操作人」列。登录失败时记的是**被尝试的用户名**——
「有人在猜 alice 的密码」和「有人在猜 root 的密码」是两条不同的线索。

升级兼容：v0.9.5 及以前库里的 `admin_user` / `admin_pass_hash` 由迁移 v4 原样
搬进 `admin_users` 作为 `owner`，用户名和密码都不变，升级后直接用原账号登录。

```bash
# 忘记 owner 密码（无需登录，直接改库里的 owner 账号）
sudo triune-panel reset-password             # 生成随机密码并打印
sudo triune-panel reset-password --user me   # 顺带把 owner 改名
```

## 登录设备（会话管理）

v0.9.7 起「设置 → 登录设备」能看到当前在线的全部会话，并**挑一台踢掉**。
在此之前，发现陌生登录只有一条路：改密码——那是霰弹枪，把这个人的所有设备
连同自己一起打下来，而且看不出到底有几台设备在线。

```
GET    /api/sessions            列出在线会话
DELETE /api/sessions/{id}       吊销指定会话
DELETE /api/sessions            吊销「我」除当前之外的全部会话（退出其他设备）
```

列表字段：设备（UA 压缩成「Chrome · macOS」「脚本 · Python」这类可读形式，原文放
`title`）、账号、角色、IP、登录时间、最后活跃、是否当前会话、是否可吊销。
当前会话置顶，其余按最后活跃倒序——最该被注意的（还在动的）排前面。

几个刻意的设计：

- **凭据永不外发**。对外只有 `id`，它是 `sha256("triune-sid-v1|"+token)` 的前 16 位，
  吊销时服务端自己算哈希比对。把 token 发给前端，等于给任何能看一眼面板的人
  一把万能钥匙。
- **不能踢比自己高的**。`owner` 可踢所有人；`admin` 可踢 `admin` / `viewer`，
  但**不能踢 `owner`**——否则一个被钓鱼的管理员就能把超级管理员锁在门外，
  面板直接失去管理能力。自己的会话永远可以踢（「我在外面登录过，全退掉」）。
- **只读账号只看自己**。`viewer` 调列表只返回自己的会话：别人的登录 IP 和
  设备本身是情报，不该顺手给出去。但「退出其他设备」对他开放——管自己的登录
  不是改配置。
- **改密 / 停用 / 删除账号会顺手清内存**。指纹校验本来就是兜底（旧 token 下一个
  请求就 401），但内存里留着僵尸会话会让列表撒谎，所以同时清掉，并把
  「吊销 N 个会话」写进审计详情。
- **审计留痕**：每次吊销记 `session.revoke`，详情带被踢会话的 sid 前缀、IP 与设备名；
  越权吊销记 `fail`。审计页的动作筛选里有「登录设备」一档。

```bash
# 只看不发请求也能核对：会话存在内存，重启面板即清空
curl -H "Authorization: Bearer $TOKEN" https://panel.example.com/api/sessions
```

### 登录历史（谁在什么时候从哪来过）

会话表只回答「现在谁还连着」。「上周三凌晨谁从哪个 IP 登录过」得看审计。
v0.9.8 起同一张设置页里有一张**按人聚合**的表，不用再去审计页翻几百条：

```
GET /api/login-history?days=7&user=<可选>
```

每个人一行：成功 / 失败次数、来源 IP、最近一次成功与失败，点「时间线」看逐条记录
（时间 + 结果 + IP + 说明）。`days` 默认 7，上限 90（审计保留期）。

两条统计口径值得说清楚：

- **来源 IP 只算成功的登录**。失败尝试多半来自扫描器，混进「这个人用过的 IP」里
  会让人误以为自己的账号被人用过——那是最容易引起恐慌的假信号。
- **一次最多扫 500 条**审计。超出时返回 `truncated: true`，界面会明说「更早的没统计
  进来」，而不是假装这就是全部。

`viewer` 只能看到自己的记录，带 `?user=` 也越不了权。

## 登出与会话

`POST /api/logout` 会**在服务端销毁会话**（前端登出按钮会先调它再清 localStorage）。
只清本地存储是假登出——token 在剩余有效期（24 小时）内仍可被任何拿到它的人使用。

会话存在内存里，面板重启即全部失效。改密会让该账号的**密码指纹**变化，
于是**这个账号**的旧会话立刻失效，其他管理员不受影响
（这也是密码泄漏后踢掉攻击者会话的手段，代价是这个账号要重新登录）。
登出只销毁当前这一个会话，同一账号在其他设备上的会话不受影响。登出会记一条审计。

## 审计日志

面板里 **审计** 页记录关键操作，用于事后追查「谁在什么时候动了什么」：

| 类别 | 记录内容 |
|---|---|
| 登录 | 成功 / 失败 / 锁定期拒绝 / 账号停用，含**真实来源 IP**（反代下按 `trusted_proxies` 解析） |
| 账号安全 | 改密（含 CLI `reset-password`）、启用/关闭 2FA、管理员增删改（`admin`） |
| 会话 | 吊销登录会话（`session.revoke`，含被踢会话的 IP 与设备名，越权吊销记 `fail`） |
| 权限 | 越权访问被拒（`rbac.deny`，含角色与目标端点） |
| 配置变更 | 设置项改动（**只记键名不记值**）、入站与用户的增删改、订阅令牌重置 |
| 发布动作 | 应用配置（成功/失败）、回滚、内核切换、版本更新、数据恢复 |

设计取舍：

- **只记动作，不记值**。`smtp_pass` / `stats_secret` / 密码一旦进日志就等于明文落盘，
  比不记更糟。审计里的设置条目长这样：`target: public_host,smtp_pass`，
  只说明改了哪几个键。
- **按人追溯**。v0.9.6 起 `actor` 是真实登录账号；登录失败记被尝试的用户名，
  匿名场景记 `anonymous`——不会再出现「所有人都是 admin」这种无法追查的记录。
- **旁路**。写审计失败只打日志，不会让主操作失败——审计用于追查，不用于拦截。
- **清空也留痕**。点「清空审计」后列表里会剩一条 `audit.clear`，说明是谁在什么时候清的
  （该操作仅超级管理员可执行）。
- **保留 90 天**，面板启动后每 12 小时清理一次过期记录，避免长期运行把库撑大。

接口：`GET /api/audit?prefix=login&result=fail&limit=100&offset=0`、`DELETE /api/audit`（仅 owner）。

## 变更通知（邮件 / Webhook）

审计只解决「事后能查」，前提是有人想起来去查。半夜有人把入站端口改成 22、
给陌生账号加了 100GB 配额，等下次登录才看到就太晚了。v0.9.8 起关键写操作之后
会推一条摘要出去：**谁、什么时候、改了什么**，设置项还带字段级 diff。

在「设置 → 变更通知」配置：

| 设置项 | 说明 |
|---|---|
| `notify_email` | 收件邮箱，留空 = 不发邮件（复用 2FA 里配置的 SMTP 通道） |
| `notify_webhook` | Webhook 地址，留空 = 不推。POST 一个 JSON，`X-Triune-Event: change` |
| `notify_events` | 订阅哪些事件，逗号分隔；留空 = 默认集合 |

默认集合是 `settings,inbound,client,admin,apply,session`——**刻意不含 `login`**：
登录本来就频繁，失败尝试又有防爆破兜底，把它也推出去收件箱很快就没人看了。

Webhook 报文长这样：

```json
{
  "panel": "panel.example.com",
  "version": "v0.9.8",
  "time": "2026-11-01 03:55:48",
  "event": {
    "actor": "admin",
    "action": "settings.update",
    "target": "public_host,smtp_pass",
    "detail": "修改设置",
    "diff": [
      "public_host: (空) → panel.example.com",
      "smtp_pass: 已变更（不显示值）"
    ]
  }
}
```

三条设计约束：

- **绝不阻塞主操作**。通知走 goroutine，8 秒超时，失败只打日志——推送服务挂了
  不该让人连「改个设置」都失败。
- **敏感值打码**。判定是「键名包含 `pass` / `secret` / `token` / `key`」而不是白名单：
  将来加个 `smtp_xxx_pass` 之类的新键，忘了登记白名单就会把密钥发到邮箱里。
  凭据与订阅令牌同理，只报「已更换」。
- **diff 只比关键字段**。入站比名称/内核/协议/端口/传输/TLS/启用/草稿，用户比
  名称/配额/到期/启用；`settings` 全文太长且含凭据，整段塞进邮件没人看。

「发送测试」会**同步**投递一条并把错误原样返回（`{"ok":false,"errors":[...]}`）：
配了 Webhook 却因为地址写错一直静默失败，比没有这个功能更糟。

## 监控指标（Prometheus）

面板内置 `/metrics`，输出 Prometheus 文本格式。**默认关闭**——没配令牌就返回 403，
升级不会凭空多出一个「知道地址就能看用户列表和流量」的接口。

在「设置 → 基础设置 → 指标访问令牌」点「生成」（24 字节随机数 → 48 位十六进制），
保存后即生效：

```bash
curl -H "Authorization: Bearer <令牌>" http://127.0.0.1:10010/metrics
```

Prometheus 侧：

```yaml
scrape_configs:
  - job_name: triune-panel
    scrape_interval: 30s
    static_configs:
      - targets: ["panel.example.com:10010"]
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/triune-metrics.token   # 内容就是那个令牌
```

也接受 `?token=<令牌>`（有些采集器不方便加 header），但 query 更容易出现在
访问日志里，优先用 header。

### 指标一览

| 指标 | 类型 | 说明 |
|---|---|---|
| `triune_up` | gauge | 进程存活，恒为 1 |
| `triune_build_info{version,go_version}` | gauge | 构建信息 |
| `triune_uptime_seconds` | gauge | 运行时长 |
| `triune_db_up` | gauge | 数据库可查询（1=正常） |
| `triune_go_goroutines` / `triune_go_heap_alloc_bytes` | gauge | 排查 goroutine 泄漏与内存增长 |
| `triune_clients_total{enabled}` | gauge | 用户数 |
| `triune_inbounds_total{core,enabled}` | gauge | 入站数（草稿计入 `enabled="false"`） |
| `triune_core_running{core}` | gauge | 内核进程是否在跑 |
| `triune_client_enabled{client}` | gauge | 单用户是否启用 |
| `triune_client_traffic_bytes{client,direction}` | counter | 单用户累计上下行 |
| `triune_client_quota_bytes{client}` | gauge | 单用户配额（0=不限量） |
| `triune_client_expires_timestamp_seconds{client}` | gauge | 到期时间（仅设置了到期的用户） |
| `triune_cert_expiry_timestamp_seconds{domain}` / `triune_cert_valid{domain}` | gauge | 证书到期与是否有效 |
| `triune_traffic_up_bytes_per_second` / `triune_traffic_down_bytes_per_second` | gauge | 最近一个采样周期的总速率 |

几条常用告警：

```yaml
- alert: TriunePanelDown
  expr: triune_up == 0
  for: 2m
- alert: TriuneCertExpiringSoon
  expr: (triune_cert_expiry_timestamp_seconds - time()) < 14 * 86400
- alert: TriuneClientQuotaAlmostFull
  expr: triune_client_traffic_bytes{job="triune-panel"} / on(client) triune_client_quota_bytes > 0.9
```

### 两个注意点

- **标签值会转义**。用户名字由你输入，`"` `\` 换行都会被正确转义——否则一个叫
  `x"} 1\ntriune_fake 999` 的用户就能往指标里塞任意行，伪造出不存在的序列。
- **单用户指标是逐用户展开的**，标签基数等于用户数。几百人以内没问题；
  上千人时建议在 Prometheus 侧用 `metric_relabel_configs` 丢掉
  `triune_client_*`，只保留聚合指标。
- 指标里**不含**任何凭据、订阅令牌或设置里的密钥（有测试守着）。

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
4. **每用户带宽限速做不到**：sing-box / xray / mihomo 三个内核都没有这个能力
   （xray 的 `policy.levels` 只有超时、统计开关与 `bufferSize`；sing-box 与
   mihomo 的 `users` 只有名字和凭据）。面板曾有一个 `rate_limit_mbps` 字段，
   能写能读但内核完全不消费——填了「100」以为限速了，其实没有，这比没有这个
   功能更糟，所以已经移除：API 收到非零值会返回 400 并说明原因，响应里也不再
   出现该字段（数据库里的历史列保留但不再读写）。
   真要限速只能在内核之外做：`tc`/HTB 按出口整形、上游交换机限速、或云厂商的
   带宽上限。**用量控制请用 `quota_gb`**，那是真的会被执行的（超限自动停用）。
5. **mihomo 流量统计依赖内核字段**：需 `inboundName` / `inboundUser` 出现在
   mihomo 的连接统计里，不同版本字段可能有差异；代码已做多键防御，
   若某版本统计恒为 0，先升级 mihomo 再排查。
6. **订阅与导出依赖 `public_host`**：未设置时回退为「当前请求的 Host（剥掉面板端口）」，
   所以用内网 IP 或 localhost 访问面板时，生成的节点地址会不可用。
   请在设置页填域名或公网 IP（安装脚本会把探测到的公网 IP 打印在安装结果里，可直接填进去）。
7. **systemd 加固项未在真实 systemd 上逐条验证**：`NoNewPrivileges` / `ProtectSystem`
   等是沙箱化手段，开发环境是 macOS 无法实测。若升级后出现「能启动但写配置失败」
   「防火墙不放行」「自更新失败」这类症状，先按 `deploy/triune-panel.service`
   里的注释逐条放开（最可能是 `ReadWritePaths` 少了你的安装目录）。
8. **运行中备份不是严格一致快照**：`backup` 直接拷数据目录里的文件，面板正在跑时
   SQLite 可能在两次读取之间写入新事务。归档里 `-wal` 是完整的，恢复时能重放，
   但理论上存在拿到"半新半旧"状态的窗口。要严格一致就停服后再备份
   （`backup` 检测到未合并 WAL 会打印提示）。真正的在线快照要用
   `VACUUM INTO` 或 SQLite Backup API，目前没做。
9. **备份口令丢失无法找回**：age 是端到端加密，没有后门也没有恢复密钥。
   `--gen-passphrase` 生成的口令只打印一次，务必当场存进密码管理器。
   面板不会把口令写进任何配置或日志。
10. **`style-src` 仍靠 `'unsafe-inline'`**：脚本那条已经收干净了（见
    [安全响应头](#安全响应头)），但界面有 300 多处 `style="..."`，`style-src`
    还留着 `'unsafe-inline'`。这意味着注入的 `<style>` 或行内样式会生效——能做
    视觉欺骗，但读不到 token、也发不出数据。要彻底收掉得把样式全改成 class，
    目前收益不抵工作量。
11. **会话只存在内存**：面板重启即全部失效（所有设备都要重新登录），也无法跨实例
    共享。这是刻意的——会话落库就要处理清理、并发与「重启后旧 token 还能用」的
    风险，收益不足以抵消。v0.9.7 起可在「设置 → 登录设备」里看在线会话并精准踢掉，
    v0.9.8 起同一页还有「登录历史」（来自审计，按人聚合，最多回溯 90 天）。
    仍然缺的：**按设备维度的历史**——历史里只有 IP 与 UA 摘要，认不出「这是不是
    上次那台机器」；会话本身也仍是内存态，重启后 ID 全变。
12. **邮箱验证码 2FA 是面板级单收件人**：多管理员共用一个收件邮箱（适合小团队
    共用运维邮箱的场景）。要按人收信得给每个账号配邮箱，目前没做——TOTP 是
    按账号的，需要严格隔离的账号建议用 TOTP 而不是邮箱验证码。
13. **异地备份依赖外部 rclone**：没有内置 S3 客户端，`--upload` 要求系统里装了
    rclone。这是刻意的取舍——各家对象存储的签名/分片差异太多，自己实现一遍
    等于重造 rclone 且更难验证。没装 rclone 时 `--upload` 会明确报错并给出安装提示，
    本地备份不受影响。
14. **上传校验只比对字节数**：`--upload` 传完会读回远端对象大小与本地比对，
    但不比对内容哈希（那要再拉一遍全量数据，成本翻倍）。要更强的一致性保证，
    可以在 `backup.sh` 里追加 `rclone check`。
15. **变更通知没有重试队列**：Webhook / SMTP 失败只记一行日志，不重试也不入队。
    一次变更就是一个事件，补发两小时前的通知价值不大，反而容易在上游故障时
    堆出成百上千条积压；要保证不丢就在接收侧做持久化。另外通知是**异步**的：
    接口返回 200 只代表变更已落库，「发没发出去」要看「发送测试」或日志。
16. **配置生成的版本矩阵只覆盖实测过的范围**：sing-box 是 1.11–1.15（1.15 为
    alpha，1.16 尚未发布，按 1.15 规则生成）。**mihomo 没有做版本矩阵**——
    `url-test` / `rule-providers` / `format: mrs` 是当前 mihomo 的写法，
    更老的 Clash 内核（以及不支持 `.mrs` 的 mihomo < 1.18）会认不出规则集格式；
    面板的 mihomo 生成器不区分版本，也不提供版本选择。1.16 正式发布后，
    需要按官方迁移文档复核 `capsFor` 的门限，别默认「和 1.15 一样」。
17. **保存的配置与标签存在面板自己的库里，没有版本历史**：
    - **含明文凭据**：配置正文里有节点密码 / UUID / Reality 公钥，所以整组接口
      按 `write` 档（只读账号看不到、下不了）。但这也意味着**备份面板数据目录
      等于备份了全部节点凭据**，异地备份请用 `--gen-passphrase` 加密。
    - **没有版本历史**：一份配置只有「当前内容」一个版本，重新生成时要么覆盖
      要么另存为新记录。想留痕就自己另存一份（名字里带日期），面板不帮你管
      diff 链。真正的版本管理要引入快照表 + 差异存储，收益不足。
    - **上限 200 份、单份 4 MiB**：超了直接 400 并提示先删旧的，不静默淘汰。
      小团队够用；要长期归档请把文件下载走。
    - **标签是全局共享的**：不区分账号，谁都能改（同样按 `write` 档）。
      小团队共用一套规则集是常态，但如果多人各自维护完全不同的规则，
      目前没有「我的标签 / 公共标签」的区分。
    - **重新生成读的是当前面板状态**：删掉的入站、停用的用户、改过的密码都会
      反映到新结果里。这正是想要的行为（旧配置本来就该失效），但要知道
      「重新生成」不等于「还原到保存那天」。
18. **主题与分支选择的两个边界**：
    - **主题只存在浏览器本地**（`localStorage`），不跟账号走、不跨设备同步。
      换台电脑或清了缓存会回到默认的「浅色 · 蔚蓝」。要跨设备一致得存进设置表，
      但那是把「个人偏好」写进「系统配置」，多管理员场景下会互相覆盖，目前不做。
    - **分支判定依赖 GitHub 的 Release 元数据**：`prerelease` 字段 + tag 后缀
      双重判定，`nightly-20260101` 这类非语义化 tag 认不出预发布标记，会归到
      「稳定版」里。内核三家的 tag 都是 `vX.Y.Z[-pre]` 格式，实测没问题；
      换成日期型 tag 的组件要重新看这段逻辑。另外分支只对 GitHub 源生效，
      面板走 `panel_update_index` 索引清单时按清单原样列出（清单里没有预发布
      标记，作者应自己别把 alpha 写进去）。
19. **下载镜像是第三方服务，随时会变**：
    - **列表会过期**：14 个预设里总有几个已经挂了或换域名。面板不联网更新这份
      列表，靠「自动选择最快」现场把不可用的筛掉。发现长期可用的新站，改
      `internal/update/mirror.go` 里的 `Mirrors` 即可（一处定义，前端不写死）。
    - **测速只测「能不能通、多快连上」**：取首字节算延迟，不测实际吞吐。
      一个 100ms 但限速 50KB/s 的镜像会比 300ms 不限速的更慢——真要准得下
      10MB 试，那就不叫「一键测速」了。大文件下载慢时手动换一个试试。
    - **测速结果有随机性**：同一批镜像两次测可能第一名不同（尤其差几十毫秒时），
      这是网络本身的性质，不是 bug。
20. **PWA（加到主屏）只在 HTTPS 下可用**：
    - service worker 要求安全上下文，`http://<公网 IP>` 上面板能正常用，但
      **不会**出现「安装到主屏」的入口。想要装到主屏，先用 ACME 证书把面板
      切到 HTTPS（或在本机用 `localhost` / `127.0.0.1` 访问）。
    - 代码里对这种情况是**静默跳过**（注册前判断协议），不会报错也不会提示。
      如果找不到安装入口，先看地址栏是不是 `https://`。
    - **不提供离线使用**：service worker 刻意不做缓存（面板每屏都是登录后的
      实时数据，缓存下来只会显示过期数字），断网时只会得到一页「连不上面板」。
      想离线查数据请用导出/备份，不要指望主屏图标。
    - **iOS 的安装入口在分享菜单里**：Safari 没有 `beforeinstallprompt`，
      要手动「分享 → 添加到主屏幕」；面板没有做安装引导按钮。
    - **镜像可能返回被替换过的文件**：这是第三方服务的固有风险。面板的防线是
      「可执行验证 + 用新二进制校验当前配置 + 健康检查」，能挡住坏的/错的文件，
      但挡不住「能跑但被植入后门」的二进制。要求强保证就填 `sha256`
      （Release 页的 `sha256sums.txt` 里有），或者干脆走官方地址。

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
  └─ pwa            清单 / service worker / 图标，只从内嵌副本出（不落盘）
```

## 目录约定

```
data/
  triune.db                 真相源（用户意图）
  cores/<core>/config.*      渲染产物 + 内核日志
  revisions/<core>/*.bak     每次应用前的自动备份
  web/index.html             可自定义的前端（覆盖嵌入版）
```

## 版本历史

完整更新日志见 [CHANGELOG.md](CHANGELOG.md)——每个条目都是「代码已实现 + 实测通过」，
不是计划清单。发布说明与安装包在 [Releases](https://github.com/yunyu697/triune-panel/releases)。

| 版本 | 主题 |
|---|---|
| v1.1.8 | 收掉 REALITY「客户端收到真证书」剩下的两条路。这句报错的真实含义是**服务端没认出客户端**——认不出就原样转发给 `dest`，客户端真收到 dest 的证书，而服务端日志一切正常（`received request … accepted … email: u1`），两边都看不出问题。能让服务端认不出的只有公钥、短 ID、SNI，v1.1.7 已保证导出时公钥由私钥现算，这次补另外两条：保存入站时把 `pbk` 归一化成私钥推导值（表单里那一栏是给人复制去手填客户端的，私钥解不开不动它、已经正确时逐字节不改写）；列表接口也归一化，存量入站不用手动再存一次（只改返回的副本、不落库，`GET` 不该有副作用）；短 ID 以 `short_ids` 为准（残留的单数 `sid` 会顶掉它，导出一个服务端不认的短 ID，症状与公钥不配对一模一样）。新增 `deploy/verify-reality-link.py` 真内核端到端：建入站 → 应用 → 导出 `vless://` → 按 v2rayN 的方式解析 → 只用链接参数拼 xray 客户端 → 经它出网，8 项断言；并做负向对照（公钥改中间一位 → 复现原报错、断言转红），确保它不是「怎么都能过」 |
| v1.1.7 | 两类「看着没问题、实际用不了」一起收。**通知连弹三次**：`select`/`checkbox` 会连发 `click → input → change`，三个委托监听器都派发，动作执行三次、写库三次——改成按事件源控件只认一种事件。**导出的 mihomo 配置连不通**，三个独立成因：REALITY 公钥采信了 settings 里的旧 `pbk`（换过密钥没同步就导出错公钥，客户端报 `REALITY authentication failed` 而服务端日志一切正常，现在一律由私钥现算）；gRPC 服务名丢失（settings 存 `service_name`、转换读 `serviceName`，导出的节点没有 `grpc-opts`）；h2/httpupgrade/xhttp 传输被静默降级成 tcp（客户端映射只认 ws/grpc，不写 `network` 等于 tcp，服务端却在用 xhttp）——现在 h2 落到 `h2-opts`，sing-box 侧补 httpupgrade/h2/quic，表示不出来的明确报「这个传输在客户端里没有对应类型」并区分「协议不支持」与「传输不支持」。另加导出提示：节点地址是回环（`public_host` 没填而用 `127.0.0.1` 打开面板）时说明只有同机能用。新增 `deploy/verify-mihomo.py` 真内核端到端回归：建入站 → 应用 → 导出 → `mihomo -t` → 真跑 mihomo → 经 socks 出网，5 种组合 35 项断言 |
| v1.1.6 | 两处「窗口小就有一部分用不了」：矮窗口下左侧导航滚不动（`.shell` 是 `overflow:hidden` 而导航列没有自己的滚动容器，`#nav` 补 `overflow-y:auto` + `min-height:0`，否则 flex 子项不收缩、只会撑破侧栏）；手机竖屏显示不全的三个独立成因——`100vh` 在手机上比可视区高（底部内容落到地址栏后，改用 `100vh`+`100dvh`）、配置生成页用内联 `grid-template-columns` 写死两列（媒体查询盖不掉，窄屏顶出横向滚动，改 `.frm-2` 类）、顶栏副标题把胶囊与版本号挤成断字（640px 以下收副标题、380px 以下让出版本号） |
| v1.1.5 | 修「所有对话框都无法编辑，敲第一个字就关」：`data-self` 守卫原先只写在 click 里，change/input 各写了一份不带判断的版本，输入事件冒泡到遮罩被当成点了空白区——现在三个事件统一走 `actFrom()`，并加了两条回归测试（派发 `data-act` 的委托必须经过守卫、静态 HTML 不许出现 `${…}`）；顺带修「配置变更对比 → 确认无误，应用」点了没反应。外部节点页不再由前端 `new URL()` 猜协议/地址（vmess/ss 是 base64，猜出来是错的），改由后端实时解析并给失败原因；导入按「同一链接」「同一节点（忽略备注名）」两级去重；节点可改名/换链接，换到已存在的节点 409 |
| v1.1.4 | Reality 两处「填错了不报错、只是连不上」：新建入站自动给 3 个 shortId（16/16/8 位十六进制，可轮换）与 `spiderX: /`，不再默认 `[""]` 放行无短 ID 客户端，且只有新建时生成、编辑不动既有配置；shortIds 非十六进制/奇数长度/超 16 字符、spiderX 不以 `/` 开头改在保存期 400 并点名第几个；握手目标 dest 新增「目标择优」——从本机对每个候选真握手 3 次取中位数，查延迟/TLS 版本/ALPN/证书剩余天数，只谈成 TLS 1.2 判不可用、证书与 SNI 不符给警告且不推荐，点「使用」回填 dest/端口/SNI |
| v1.1.3 | 三件「环境不对却怪配置」的事：规则集可换下载源并可先测再选（镜像按内容 sha256 筛，不看状态码）；本机内核校验显式给 `-d` 并钉住 HOME，只读 rootfs 不再被误报成「内核拒绝配置」；没有域名也能启用面板 HTTPS（自签 ECDSA、SAN 覆盖全部网卡 IP、摊开指纹、自备证书永不覆盖，证书不可用时拒绝明文启动） |
| v1.1.2 | 两类「能存进去但连不上」一起收：保存入站时就拦同端口同协议族冲突（TCP+UDP 共用端口仍放行）；订阅导出的 REALITY 节点补上 `sni` / `sid` / `fp`，只填私钥也能导出（公钥现算），订阅按 UA 给 Clash YAML / sing-box JSON；脚本外置 + 事件委托后 `script-src` 收到 `'self'`；自检新增「客户端兼容性」（xray ≥26.7.11 的 REALITY 拒 mihomo） |
| v1.1.1 | 「升级完成后立刻切换成最新版本」：守望者盯着自己的二进制，换了就自己 exec 过去（一键脚本 / `docker cp` / systemd 没重启都算），同版本最多试 2 次；修 `/proc/self/exe` 的 ` (deleted)` 后缀让第二次更新失败 |
| v1.1.0 | 修掉「更新完还是运行旧版本」：自更新不再跨文件系统 rename 直搬（容器/独立分区必失败），重启失败不再静默，`/api/version` 与设置页直接给出「磁盘版本 vs 运行版本」 |
| v1.0.9 | 修掉「点生成、内核回一句看不懂的话」：本地代理协议（mixed/socks/http）不再被静默跳过、订阅的 proxy-group 名单与 proxies 同源、节点重名自动补序号；生成失败的原因留在页面上 |
| v1.0.8 | 新增只读「出站」页（出站 / 路由 / DNS 三段带来源标注）；外部节点默认不进订阅；mihomo 降为兼容内核、默认不出现在新建入口；修 direct 规则丢 `outboundTag`、Reality 握手检查误报、UDP 入站判定等一批「能存进去但起不来」的问题 |
| v1.0.7 | JSON 只解析入站并逐项回填「新增入站」表单，缺用户自动生成；同批补齐 socks/http/dokodemo-door/wireguard、h2/httpupgrade/xhttp/kcp/quic、内联 PEM、DNS 与路由导入 |
| v1.0.5 | 导入改成 JSON（3x-ui / xray 入站定义）；移除「链接反推入站」；入站表单每项可改，Reality 密钥对可编辑 |
| v1.0.4 | 「最新版本」按 semver 排序（预发布段曾按字符串比，默认选中了旧 alpha）；滚动 tag 用 `updated_at` 兜底 |
| v1.0.3 | 修掉启用/切换内核的各类报错：三内核 9 处渲染错误、3 处静默失败、Reality 密钥自动生成 |
| v1.0.2 | 命令行 `tp` 全套（只读 + 写操作，走 unix socket）；支持加到主屏（PWA）；设置页分组的五个缺口 |
| v1.0.1 | 规则集按推荐策略起手（国内 DIRECT / 广告 REJECT）；设置页分三组 + 常驻吸顶大标签 |
| v1.0.0 | 修掉「更新完打开还是旧版本」；版本号改为次版本/修订号单位数进位 |
| v0.12.0 | 面板版本列表开箱可用；下载镜像可选可测速 |
| v0.11.0 | 内核安装可选分支（稳定/预发布/全部）；主题拆成「亮暗 × 色彩方案」 |
| v0.10.0 | 配置命名保存 + 配置管理页 + 规则标签 |
| v0.9.x | 多管理员与 RBAC、在线会话与登录历史、变更通知、审计日志、Prometheus 指标 |
| v0.8.x | 2FA、忘记密码、CLI、备份加密、防火墙自动放行、连通性自检、二维码 |
| v0.7 – v0.5 | 配置生成器、内核随时互切、全新 Web UI、全组件更新与回滚 |
| MVP – v0.3 | 登录鉴权、入站/用户 CRUD、双模式导入、预检应用、订阅、流量统计 |

### 版本号规则

**次版本、修订号**只用 **0–9**，满 9 进位；**主版本不受限**，可以是两位数：

```
v1.0.8 → v1.0.9 → v1.1.0 → … → v1.9.9 → v2.0.0 → … → v9.9.9 → v10.0.0
```

这样「大版本跳了一级」一眼可见，不用心算 0.12 和 0.9 谁大。主版本不设上限，
是留给真正的里程碑用的——到 `v9.9.9` 之后还能往上走，不用为了凑格式另起炉灶。

进位规则只写在一处——[`deploy/check-version.sh`](deploy/check-version.sh)，
CI 在推 tag 时调用的就是它，避免两边各抄一份正则慢慢长歪。

已发布的历史版本不改号（`v0.12.0` 就留在那儿），规则从下一个发布号开始生效。
`v1.0.0` 就是这么来的：`v0.12.0` 之后按新规则第一个合规号就是它。

```bash
./deploy/check-version.sh v1.0.0          # 校验，不合规会告诉你下一个合规号
./deploy/check-version.sh --next v1.0.9   # → v1.1.0
./deploy/check-version.sh --next v9.9.9   # → v10.0.0
```

发版走 [`deploy/release.sh`](deploy/release.sh)：校验版本号 → 四平台构建 → 生成
`sha256sums.txt` → 打 tag → 推送 → 建 Release，一步到底；`--dry-run` 只构建不动远端。
