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
triune-panel backup [--out DIR] [--keep N] [--encrypt | --gen-passphrase] [--upload REMOTE]   # 打包数据目录（默认 <data>/backups，保留 7 份，自动排除 backups 自身）
triune-panel restore <归档.tar.gz[.age]> [--yes]   # 用备份覆盖数据目录（恢复前自动备份当前数据）
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


- **数据目录自动探测**：`--data` > 环境变量 `TRIUNE_DATA` > `./data` > `/opt/triune-panel/data`，
  所以 `sudo triune-panel status` 在任意目录下都能找到 systemd 安装的实例。
- **`--json`** 便于脚本/监控取值；退出码 `0` 成功、`1` 执行失败、`2` 用法错误
  （`doctor` 有 `fail` 项时返回 1，可直接接告警）。
- **刻意不做** `apply` / 切换内核这类命令：内核进程由 daemon 托管，CLI 裸跑会抢端口、
  让 daemon 的内存状态与磁盘不一致，必须走面板的 API（`/api/apply`、`/api/switch-core`）。
- **CI**（`.github/workflows/ci.yml`）：每次推送跑 `go vet` + `go test` + 三平台交叉编译
  + 二进制 `-version` 格式校验（install.sh 靠 `awk '{print $2}'` 取版本做终检，
  格式一变安装脚本就会误判"装到旧版本"）+ shellcheck 扫 `deploy/` 脚本。

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
default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline';
img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none';
base-uri 'none'; form-action 'self'; frame-ancestors 'none'
```

**关于 `'unsafe-inline'`——这里没有假装能挡 XSS。** UI 是单文件，用了 126 处内联
`onclick="..."` 事件属性，只能靠 `'unsafe-inline'` 放行；也不能改用 nonce：
按 CSP3，`script-src` 里一旦出现 nonce 或 hash，浏览器会**忽略** `'unsafe-inline'`，
126 个内联事件会全部失效、界面直接瘫掉。

所以策略是「不挡内联执行，但掐死变现路径」：`connect-src 'self'` 让注入脚本
连 `fetch`/`XHR`/`WebSocket` 都发不到外部域名（token 偷出去也没地方送），
`form-action 'self'` 堵表单外发，`base-uri 'none'` 防 `<base>` 改写相对 URL，
`frame-ancestors 'none'` 防点击劫持。这比什么都不加实质性强得多。

**HSTS 的下发条件**：直连看 `r.TLS`；反代后看 `X-Forwarded-Proto`，但**只采信
可信代理发来的值**——否则攻击者随手加一个 `X-Forwarded-Proto: https` 就能让面板
给纯 HTTP 连接下发 HSTS，把用户后续访问顶到 https 而彻底打不开。

刻意**不下发** `Cross-Origin-Resource-Policy`：`/sub/<token>` 会被各种订阅客户端
拉取，其中一些是浏览器里的 Web 面板，CORP 会把它们挡掉，收益小于风险。

## 登出与会话

`POST /api/logout` 会**在服务端销毁会话**（前端登出按钮会先调它再清 localStorage）。
只清本地存储是假登出——token 在剩余有效期（24 小时）内仍可被任何拿到它的人使用。

会话存在内存里，面板重启即全部失效；改密会让「会话纪元」变化，所有旧会话立刻失效
（这也是密码泄漏后踢掉攻击者会话的手段，代价是所有设备都要重新登录）。
登出只销毁当前这一个会话，同一账号在其他设备上的会话不受影响。登出会记一条审计。

## 审计日志

面板里 **审计** 页记录关键操作，用于事后追查「谁在什么时候动了什么」：

| 类别 | 记录内容 |
|---|---|
| 登录 | 成功 / 失败 / 锁定期拒绝，含**真实来源 IP**（反代下按 `trusted_proxies` 解析） |
| 账号安全 | 改密（含 CLI `reset-password`）、启用/关闭 2FA |
| 配置变更 | 设置项改动（**只记键名不记值**）、入站与用户的增删改、订阅令牌重置 |
| 发布动作 | 应用配置（成功/失败）、回滚、内核切换、版本更新、数据恢复 |

设计取舍：

- **只记动作，不记值**。`smtp_pass` / `stats_secret` / 密码一旦进日志就等于明文落盘，
  比不记更糟。审计里的设置条目长这样：`target: public_host,smtp_pass`，
  只说明改了哪几个键。
- **旁路**。写审计失败只打日志，不会让主操作失败——审计用于追查，不用于拦截。
- **清空也留痕**。点「清空审计」后列表里会剩一条 `audit.clear`，说明是谁在什么时候清的。
- **保留 90 天**，面板启动后每 12 小时清理一次过期记录，避免长期运行把库撑大。

接口：`GET /api/audit?prefix=login&result=fail&limit=100&offset=0`、`DELETE /api/audit`。

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
   并记入修订历史。
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

v0.9.3 已验证：

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

v0.9.2 已验证：

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

v0.9.1 已验证：

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

v0.9.0 已验证：

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

v0.8.9 已验证：

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

v0.8.8 已验证：

1. **反向代理 IP 识别**（`internal/api/clientip.go`）：新增设置 `trusted_proxies`
   （默认空 = 不信任任何代理，行为与旧版一致）。只有直连对端落在可信网段内才采信
   `X-Forwarded-For` / `X-Real-IP`，且从右往左取第一个不可信地址，客户端伪造左侧无效。
   `login_last_ip` 与登录防爆破计数键都改用真实客户端 IP。
   设置页写入前整体校验，非法 CIDR/IP 直接 400，不会留下半套配置。
2. 端到端实测（本地实例 + 伪造转发头）：配了 `127.0.0.1` 后 A 客户端失败 5 次被锁 429、
   B 客户端不受影响；清空可信网段后伪造 `X-Forwarded-For: 6.6.6.6` 被忽略、
   仍记为直连对端。

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
10. **CSP 挡不住内联脚本注入**：UI 有 126 处内联 `onclick="..."`，只能启用
    `'unsafe-inline'`，所以注入的 `<script>` 仍会执行。当前防线是「掐死变现路径」
    （`connect-src 'self'` 让偷到的 token 发不出去）。**根治办法**是把内联事件
    全部改成 `addEventListener` + 用 nonce，工作量在单文件 UI 上不小，
    目前没做——知道这是已知短板，不假装它不存在。
11. **会话只能在「全部」和「当前一个」之间吊销**：没有会话列表页，看不到
    「哪些设备正在登录」，也不能挑一个踢掉。要踢掉某个不明会话只能改密
    （会踢掉所有设备）。
12. **异地备份依赖外部 rclone**：没有内置 S3 客户端，`--upload` 要求系统里装了
    rclone。这是刻意的取舍——各家对象存储的签名/分片差异太多，自己实现一遍
    等于重造 rclone 且更难验证。没装 rclone 时 `--upload` 会明确报错并给出安装提示，
    本地备份不受影响。
13. **上传校验只比对字节数**：`--upload` 传完会读回远端对象大小与本地比对，
    但不比对内容哈希（那要再拉一遍全量数据，成本翻倍）。要更强的一致性保证，
    可以在 `backup.sh` 里追加 `rclone check`。

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
