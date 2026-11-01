# 命令行（`tp`）

面板装完之后，日常真正会重复做的就那么几件事：看它还活着没、重启一下、升级、查某个用户的订阅、翻一眼审计。这些不该逼人开浏览器登录。`tp` 就是给这些事用的。

它也能改东西——启停入站、增删用户、应用配置——但改的动作由面板执行，`tp` 只负责把意图说清楚。为什么必须这样，见 [写操作为什么必须走面板](#写操作为什么必须走面板)。

```
$ tp
Triune Panel v1.0.3

  数据目录  /opt/triune-panel/data   1.2 MB
  服务      运行中 · :10010
  入站      4 个（启用 2 / 草稿 1）
  用户      4 个（启用 3）
  内核      singbox  /opt/triune-panel/bin/sing-box
  内核      xray     未配置
  内核      mihomo   /opt/triune-panel/bin/mihomo
  证书      example.com 到期 2027-01-02（剩 62 天）
  上次应用  2026-11-01 21:40:12 · singbox · ok
  最近登录  203.0.113.7 · 2026-11-01 22:01:37
  防火墙    nftables
  公开地址  panel.example.com
  二次验证  totp
```

---

## 一、`tp` 是什么，不是什么

`tp` **不是**第二个程序，它就是面板二进制本身。安装脚本在 `/usr/local/bin/tp` 建了一个软链：

```
/usr/local/bin/tp -> /opt/triune-panel/triune-panel
```

同一个文件，靠 `argv[0]`（被调用的名字）区分两种人格：

| 调用方式 | 不带参数时的行为 |
|---|---|
| `triune-panel` | **启动服务**（systemd 的 `ExecStart` 就是这个，不能改） |
| `tp` | **打印状态**（绝不启动任何东西） |

这条规则很重要：`tp` 敲下去回车，最坏结果是"看到状态"，不可能是"把服务拉起来了"。误按一次不会有副作用。

`triune-panel` 这个名字和它下面所有旧命令（`status` / `doctor` / `backup` / `restore` / `reset-password` / `serve`）全部保留可用。`tp` 只是给它们套了一个更短、更整齐的入口。

---

## 二、全局约定

### 数据目录

```
--data DIR  >  环境变量 TRIUNE_DATA  >  ./data  >  /opt/triune-panel/data
```

所以在任意目录下 `sudo tp status` 都能找到 systemd 装的那个实例，不用 `cd`。

### 控制套接字

写操作要找到面板的控制套接字：

```
--sock PATH  >  环境变量 TRIUNE_CONTROL_SOCK  >  /run/triune-panel/control.sock  >  <数据目录>/control.sock
```

正常情况下不用管它。手动前台跑面板、或者装在奇怪位置时，用 `--sock` 指定即可。

### 机器可读输出

标了 `--json` 的命令支持 `--json`，输出一份 JSON 给脚本取字段：

```bash
tp user ls --json | jq -r '.[] | select(.state=="超限") | .name'
```

### 退出码

| 码 | 含义 |
|---|---|
| `0` | 成功 |
| `1` | 执行失败（服务没起来、文件不存在、用户主动取消…） |
| `2` | 用法错误（未知命令、参数写错、前缀有歧义） |

`doctor` 有 `fail` 项时也返回 `1`，可以直接接告警。

### 前缀缩写

命令名支持唯一前缀：`tp au` = `tp audit`，`tp res` 会报错因为同时命中 `restart` / `restore`。

**有歧义时一律报错，不猜。** `tp st` 同时是 `status` 和 `start` —— 猜错的代价是"本想看状态，结果把服务启动了"，这种错误比多打几个字贵得多。

```
$ tp st
"st" 有歧义，可能是：status / start / stop
```

### 开关和参数可以任意顺序

```
tp in show 1 --data /opt/triune-panel/data
tp in show --data /opt/triune-panel/data 1
```

两种写法等价。标准库的 `flag` 遇到第一个位置参数就停止解析，会把后面的 `--data` 当成多余参数悄悄丢掉，然后报"未找到数据库，请用 --data 指定数据目录"——用户明明指定了。所以入口处做了重排。

---

## 三、命令速查

| 命令 | 作用 | 需要 root | 需要面板在跑 |
|---|---|---|---|
| `tp` / `tp status` | 只读汇总 | 否 | 否 |
| `tp start` | 启动面板 | 是 | — |
| `tp stop` | 停止面板 | 是 | — |
| `tp restart` | 重启并等就绪 | 是 | — |
| `tp logs [-f] [-n N]` | 面板日志 | 否 | 否 |
| `tp update` | 升级面板 | 是 | 否 |
| `tp in ls` / `tp in show <id>` | 入站 | 否 | 否 |
| `tp in on` / `off` / `rm <id>` | **改入站** | 是 | **是** |
| `tp user ls` / `show` / `link` | 用户与订阅 | 否 | 否 |
| `tp user add` / `set` / `rm` / `token` | **改用户** | 是 | **是** |
| `tp apply [内核]` | **应用配置** | 是 | **是** |
| `tp rollback <内核>` | **回滚配置** | 是 | **是** |
| `tp audit` | 审计日志 | 否 | 否 |
| `tp doctor` | 环境自检 | 否 | 否 |
| `tp backup` | 备份数据目录 | 视目录权限 | 否 |
| `tp restore <归档>` | 从备份恢复 | 视目录权限 | 否 |
| `tp passwd` | 重置管理密码 | 是 | 否 |
| `tp version` | 打印版本 | 否 | 否 |
| `tp help [--full]` | 帮助 | 否 | 否 |

两列「需要面板在跑」分出了两类命令，这是整套设计的核心：

- **读（否）**：直连 SQLite，不经过 daemon。面板起不来的时候，正是你最需要查数据的时候——如果那时候 CLI 还要连它，这套工具就白做了。
- **写（是）**：必须经控制套接字交给 daemon 执行。内核进程状态只存在 daemon 的内存里（没有 pid 文件），CLI 自己拉起内核会和 daemon 抢端口，还会让 daemon 的内存状态和磁盘对不上——表现为「面板显示运行中，实际早死了」。所以写操作只有一个执行者。

`tp help` 打上面这张表；`tp help --full` 额外列出备份加密、异地备份的全部参数（那部分长到不适合当默认帮助，但"口令丢失 = 归档永久打不开"这类话不能省）。

---

## 四、逐条说明

### `tp status`

只读汇总，也是 `tp` 不带参数时的默认行为。

```bash
tp status
tp status --json
```

看得到：版本、数据目录（含占用）、服务是否在跑及监听地址、入站数（启用/草稿）、用户数、每个内核的二进制路径与是否就位、证书到期、上次应用、最近登录、防火墙后端、公开地址、是否开了二次验证、管理密码是否还是出厂默认。

看不到：实时速率、在线连接数、**内核此刻是否在跑**。这些只在 daemon 内存里，CLI 拿不到——**宁可不显示，也不显示一个可能过期的值**。内核运行状态以面板页面为准。

「服务」那一行只查 systemd 的账本，不探 `/healthz`：`status` 号称秒回，不能因为面板半死就卡在这里。没有 systemd 的环境会显示「未由 systemd 托管」，而不是吓人的「未运行」。

### `tp start` / `tp stop` / `tp restart`

```bash
sudo tp start            # 已在跑则直接报版本，不重复启动
sudo tp stop             # 未在跑则直接返回
sudo tp restart          # 重启后轮询 /healthz，就绪才返回
sudo tp restart --wait 30s
```

`restart` 会等到 `/healthz` 真的响应才退出（默认 10 秒超时），所以脚本里 `tp restart && tp status` 不会看到"重启后立刻查，结果显示未运行"这种假故障。超时返回 `1`。

单元名默认 `triune-panel`，不是这个名就 `--unit <名字>`。

**`stop` 会把内核一起停掉。** 内核是面板的子进程，systemd 默认的 `KillMode=control-group` 会连同 cgroup 一起杀。所以 `tp stop` / `tp restart` 期间节点会断流——这不是 bug，是"面板不在，没人管内核"的必然结果。

### `tp logs`

```bash
tp logs                  # 最近 200 行
tp logs -n 50
tp logs -f               # 持续跟踪，Ctrl-C 退出
```

走 `journalctl -u triune-panel`。没有 journalctl（容器、macOS）会明确告诉你日志在哪：手动前台跑的话，日志就在那个终端里。

### `tp update`

```bash
tp update --check        # 只看有没有新版，什么都不改
sudo tp update           # 升级到最新正式版
sudo tp update --to v1.0.3   # 指定版本，可升可降
sudo tp update --file ./triune-panel-linux-amd64   # 离线 / 救砖
```

流程和面板里的「一键升级」完全一致：

1. 取版本列表（自建索引清单优先，其次 GitHub Releases）
2. 下载 → 校验 `sha256`
3. **跑 `-version` 自检** —— 确认拿到的真是面板二进制，且版本号对得上目标
4. 备份现有二进制（`exe.bak` + `data/backups/panel/<版本>-<时间>.bak`）
5. 原子替换
6. 重启服务并等 `/healthz` 就绪；**不就绪自动回滚**，并打印失败原因

第 3 步值得单独说：`sha256` 只能证明"文件没被篡改"，证明不了"这是我该装的东西"。一个被正确签名/校验过的错误文件照样能通过哈希，所以必须再问它一句"你是谁"。

`--dry-run` 走完 1~3 就停，不碰任何文件，用来先验一遍网络和镜像通不通。

### `tp in ls` / `tp in show <id>`

```
$ tp in ls
Triune Panel · 入站（4 个）

  ID  端口  协议                内核     状态  用户  名称
  1   443   vless+reality       singbox  启用  3     主力节点
  2   8443  hysteria2+quic+tls  singbox  启用  2     备用 hy2
  3   2053  trojan+tls          mihomo   草稿  0     待测试
  4   1080  socks               mihomo   停用  0     已停用
```

`show` 给出单个入站的全部字段，含原样透传给内核的协议配置 JSON：

```bash
tp in show 1
tp in show 1 --json
```

状态列把 `enabled` / `draft` 两个布尔压成一个词，**草稿优先**——草稿即使 `enabled` 也不参与配置渲染，说它"启用"会误导。

### `tp user ls` / `show` / `link`

```
$ tp user ls
Triune Panel · 用户（4 个）

  名称   状态  已用 / 配额        到期               令牌       备注
  alice  正常  12.4 GB / 50 GB    —                  demo1234…
  bob    正常  3.1 GB / 不限      2026-12-31         demo5678…
  carol  停用  100.0 GB / 100 GB  —                  demo9012…  已超配额
  dave   过期  500.0 MB / 10 GB   2026-10-22 已过期  demoxxxx…
```

状态判定顺序：`停用`（`enabled=false`，备注里说清是因为超配额还是过期）→ `超限` → `过期` → `正常`。备注列只在"停用"时写原因，因为 `超限` / `过期` 本身已经把话说完了。

`show` 给出凭据、令牌、订阅地址：

```bash
tp user show alice
tp user show alice --json
```

`link` 直接给三种客户端格式的订阅地址，省得自己拼：

```bash
tp user link alice
tp user link alice --qr     # 终端二维码（需要系统装 qrencode）
```

```
alice 的订阅

  通用（base64）  http://panel.example.com/sub/demo1234abcd5678
  Clash / mihomo  http://panel.example.com/sub/demo1234abcd5678?target=clash
  sing-box        http://panel.example.com/sub/demo1234abcd5678?target=singbox
```

地址由「设置 → 常用 → `public_host`」拼出来，scheme 跟 `acme_enabled` 走。没填 `public_host` 会直接报错并告诉你该去哪填，而不是吐一个 `http:///sub/xxx` 让你自己猜。

### `tp audit`

```bash
tp audit                 # 最近 30 条
tp audit --since 7d
tp audit --since 24h --limit 100
tp audit --json
```

`--since` 支持 `30m` / `24h` / `7d`。注意 Go 的 `time.ParseDuration` 不认 `d`，这里的 `d` 是 CLI 自己补的。

---

### 写操作：`tp in on|off|rm`、`tp user ...`、`tp apply`、`tp rollback`

这一类命令**需要面板在跑**。它们把意图经 unix 套接字交给 daemon，由 daemon 统一执行——原因见下一节「写操作为什么必须走面板」。

### `tp in on <id>` / `tp in off <id>`

```bash
sudo tp in off 2
```
```
  ✔ 入站 备用 hy2 已停用
  ✔ singbox  已应用 · 修订 #42 · 已校验 · 已重启
```

**默认顺手应用配置**（`--no-apply` 可跳过）。只翻一个数据库开关而不应用，用户会以为节点已经停了，实际端口还在服务——这是危险的那个方向。反过来「多应用一次」最坏结果是内核重启一下。

### `tp in rm <id>`

```bash
sudo tp in rm 3          # 会先问一遍
sudo tp in rm 3 --yes    # 脚本里必须显式加
```

确认提示会提前说明后果：正在服务的入站删掉会断流、绑着的用户会被解绑（用户本身不删）。

**非交互环境（管道、cron）一律拒绝**，不会因为 stdin 恰好有内容就当真。脚本里少写一个 `--yes` 就静默删数据，比多问一句危险得多。

### `tp user add <name>`

```bash
sudo tp user add alice --quota 50 --expires 2026-12-31 --inbounds 1,备用hy2
```
```
  ✔ 已创建用户 alice
  凭据      8f2b1c3d-9e4a-4f5b-8c6d-7e8f9a0b1c2d
  订阅令牌  24d34785947183fc7d29e06afae9c8a6
  订阅地址  http://panel.example.com/sub/24d34785947183fc7d29e06afae9c8a6
  ✔ singbox  已应用 · 修订 #43 · 已校验 · 已重启
```

| 开关 | 说明 |
|---|---|
| `--quota GB` | 流量配额，`0`（默认）= 不限 |
| `--expires 2026-12-31` | 到期日，**落到当天 23:59:59**——「到期日 12-31」应该覆盖整个 12-31，而不是当天零点就失效 |
| `--inbounds 1,备用hy2` | 绑定入站，id 和名称都认（脚本用 id 稳定，人手上敲名称更自然） |
| `--disabled` | 建好先停用 |
| `--json` | 机器可读输出，便于脚本取凭据 |

凭据和订阅令牌由服务端生成，CLI 不自己造——否则「面板上建的」和「命令行建的」会有两套生成逻辑。

### `tp user set <name>`

```bash
sudo tp user set alice --quota 100          # 改配额（不重启内核）
sudo tp user set alice --expires none       # 取消到期
sudo tp user set alice --disable            # 停用（会重启内核）
sudo tp user set alice --inbounds 1,2       # 重绑（会重启内核）
```

**只有「启用状态」和「绑定关系」会影响内核配置**，改配额和到期日不会重启内核——那两项是流量采集器在管，重启内核只会白白断掉所有在线连接。

### `tp user token <name>`

```bash
sudo tp user token alice
```

重置订阅令牌，旧链接立即失效。令牌只影响订阅 URL，不进内核配置，所以**不需要 apply**。

### `tp user rm <name>`

```bash
sudo tp user rm alice --yes
```

同样二次确认、同样非交互拒绝。删掉后订阅链接立即失效。

### `tp apply [内核]`

```bash
sudo tp apply            # 所有「有启用且非草稿入站」的内核
sudo tp apply singbox    # 只应用一个
```

不带参数时**只应用该跑的内核**（与启动时的内核恢复同一口径）。把三个内核都 apply 一遍会给没有入站的内核也起一个空进程，白占内存。

输出里的「已校验 / 未校验」来自内核自带的 `check` 命令：没装内核二进制时会跳过校验并明说，不会假装校验过了。

### `tp rollback <内核>`

```bash
sudo tp rollback singbox
```

回到上一份配置备份。注意**回滚的是配置文件，数据库里的入站/用户没有变**——所以回滚之后数据库和运行配置会不一致，改完还得再 `tp apply` 一次才真正对齐。这条提示命令会打出来。

---

### 写操作为什么必须走面板

内核进程状态只存在 daemon 的 `proc.Manager` 内存里，没有 pid 文件。如果 CLI 自己改库、自己拉起内核：

- 会和 daemon 抢端口（两个进程都想 bind 443）
- daemon 的内存状态会和磁盘对不上，表现为「面板里显示运行中，实际上早死了」
- 防火墙对账、审计、变更通知、修订记录这几件事都会漏掉——它们都在 handler 里

所以写操作只有一个执行者。CLI 负责「说得清楚」，daemon 负责「做得正确」。

### 控制套接字的三层门

| 层 | 做法 | 挡住什么 |
|---|---|---|
| 目录 | `/run/triune-panel`，`0700` | 别的用户进不来 |
| 文件 | `control.sock`，`0600` | 进得来也连不上 |
| 对端凭据 | `SO_PEERCRED`（Linux）/ `LOCAL_PEERCRED`（macOS） | 连上了也不是 root |

第三层是关键：**内核在 `connect()` 那一刻就把对端 uid 记在 socket 上**，之后由内核负责回答，对端既不能伪造也不能事后更换。允许 `uid 0` 或 daemon 自己的 uid——「能 sudo 的人本来就能改库」，多卡一层只是把同样的权限换个更绕的路走。

取不到凭据的平台（Windows 等）**一律拒绝**，不是「取不到就放行」：放行意味着任何能连上套接字的进程都能改面板配置，而那正是这套机制要防的事。

### 白名单，不是整套 API

套接字上只挂了 9 个写接口：

```
PUT    /api/inbounds/{id}
DELETE /api/inbounds/{id}
POST   /api/clients
PUT    /api/clients/{id}
DELETE /api/clients/{id}
PUT    /api/clients/{id}/inbounds
POST   /api/clients/{id}/reset-token
POST   /api/apply
POST   /api/rollback
```

**不直接把 `Handler()` 挂上来。** 那等于把账号管理、设置读写、面板自更新、整面板回滚搬到本地套接字上——一旦 peer 校验哪天出了岔子，后果从「改错一个入站」变成「整个面板被接管」。白名单短到可以一眼看完，这就是它的价值。

读接口也不开放：CLI 直连 SQLite 就够了，不需要多一条路径。

### 审计里认得出是谁

命令行改的东西，审计的操作人是 `cli@root`（非 root 账号是 `cli:uid1000`），来源 IP 记 `127.0.0.1`。

```
  时间                 操作者      动作            对象       结果
  2026-11-01 22:44:18  cli:uid501  apply           mihomo     ok
  2026-11-01 22:44:18  cli:uid501  inbound.update  测试节点B  ok
  2026-11-01 22:44:14  admin       inbound.create  测试节点B  ok
```

不借用真实 owner 账号：审计里必须分得清「面板上点的」和「服务器上敲的」。冒充 owner 会让这两种来源长得一模一样，出事时查不出是谁做的。

### 套接字在哪

```
--sock PATH  >  TRIUNE_CONTROL_SOCK  >  /run/triune-panel/control.sock  >  <数据目录>/control.sock
```

优先 `/run`：它是 tmpfs，停止/重启后自动清空，不会留下上次崩掉的陈尸套接字。非 root 环境（本地开发、用户目录安装）自动退到数据目录。

daemon 和 CLI 用的是同一份顺序（`api.ControlSocketCandidates`），两边各写一份路径规则迟早会走岔。

顺带一个好处：传输是 HTTP over unix socket，所以能直接手工调试——

```bash
curl -s --unix-socket /run/triune-panel/control.sock -X POST http://unix/api/apply -d '{"core":"singbox"}'
```


### `tp doctor`

环境自检：数据目录、DB 完整性、密码状态、内核二进制、防火墙、端口监听、代理环境变量、磁盘余量。有 `fail` 项返回 `1`。

```bash
sudo tp doctor
sudo tp doctor --json
```

### `tp backup` / `tp restore`

```bash
sudo tp backup                                   # → data/backups/triune-data-<时间>.tar.gz
sudo tp backup --out /tmp --keep 3
sudo tp restore data/backups/triune-data-20261031-205341.tar.gz
```

完整参数（加密、异地、`--force`、`--no-backup`、schema 校验）见 README 的「备份与恢复」一节——这部分行为没变，`tp` 只是换了个入口。

### `tp passwd`

```bash
sudo tp passwd                       # 生成随机密码并打印
sudo tp passwd --user me             # 顺带把管理员改名
echo 'MyNewPassw0rd' | sudo tp passwd --password-stdin
```

`reset-password` 和 `reset` 仍然可用（`install.sh` 里就在用），只是不出现在帮助里了。

---

## 五、常见场景

### 面板打不开，先看什么

```bash
sudo tp status      # 服务在不在跑？端口、数据目录对不对？
sudo tp logs -n 80  # 起不来的原因通常在这 80 行里
sudo tp doctor      # 数据目录权限、DB 损坏、端口被占
```

这三条都不依赖面板进程，所以面板挂了照样能用。这是整套 CLI 最有价值的地方。

### 服务器重启 / `systemctl restart` 之后节点全断

内核是面板的子进程，没有任何东西给它做开机自启。所以面板**启动时会自己把该跑的内核拉起来**：

- 判定口径：某个内核只要有 `enabled` 且非 `draft` 的入站，它就该在跑
- 配置用上一次渲染的产物，**不重新渲染**（渲染是「应用配置」的职责，启动阶段不该顺手改运行中的配置）
- 配置不存在 / 二进制没配 / 二进制不在磁盘上 → 跳过并记日志，不硬起
- 任何一步失败都只记日志，**不让面板跟着起不来**——否则就成了"节点挂了 → 面板也进不去 → 没法排查"

想确认恢复情况：

```bash
sudo tp logs -n 80 | grep '\[core\]'   # 应该有「已恢复 singbox」这样的行
```

内核此刻是否在跑，`tp status` 看不到（状态只在 daemon 内存里），以面板页面为准。

### 升级后起不来

`tp update` 发现 `/healthz` 不就绪会自动回滚到旧二进制并重启。如果连回滚都失败，它会明确告诉你备份文件在哪，手动救：

```bash
ls -l /opt/triune-panel/triune-panel.bak
sudo cp /opt/triune-panel/triune-panel.bak /opt/triune-panel/triune-panel
sudo systemctl restart triune-panel
```

### 把查询塞进脚本

```bash
# 超配额的用户名单
tp user ls --json | jq -r '.[] | select(.state=="超限") | .name'

# 有没有草稿忘了处理
tp in ls --json | jq -r '.[] | select(.state=="草稿") | .name'

# 最近一天有没有人爆破登录
tp audit --since 24h --json | jq -r '.[] | select(.action=="login.fail") | .ip'
```

### 批量加用户（一次应用，不是 N 次）

```bash
for u in alice bob carol; do
  sudo tp user add "$u" --quota 50 --inbounds 1 --no-apply
done
sudo tp apply singbox      # 只重启一次内核
```

`--no-apply` 的意义就在这里：每条命令都应用的话，加 10 个用户会重启 10 次内核，在线用户被断 10 次。

### 换掉一个用户的订阅链接

```bash
sudo tp user token alice          # 旧链接立刻失效
tp user link alice                # 把新链接发给对方
```

---

## 六、边界

### 还没有的

- `tp in add` / `tp in edit`：建入站要填十几个协议字段，命令行不适合，走面板。
- `tp core install|update`：内核更新有「下载 → 校验 → 备份 → 替换 → 回滚」一整套流程，面板里已经做好了；命令行的 `tp update` 只管面板自己的二进制。
- `tp cert issue`：ACME 要绑 :80 做 HTTP-01，失败原因五花八门（DNS、端口、限流），面板里有实时日志更适合排查。
- 管理员账号管理：`/api/admins` 刻意不在白名单里——那是「能 sudo 就绕过一切」的最后一层，不该多开一个入口。

需要这些时走面板 UI，它本来就在做这件事。

### 一个已知的别扭之处

`tp rollback` 回滚的是**配置文件**，不是数据库。回滚完数据库里的入站/用户和运行配置会不一致，得再 `tp apply` 一次才对得上。命令输出里会提醒这一句，但本质上「配置」和「意图」在这里是两个东西——面板的「回滚」按钮也一样。

---

## 七、排错

| 现象 | 原因与处理 |
|---|---|
| `未找到数据库 data/triune.db` | `--data` 指错了，或面板不是 systemd 装的。用 `sudo tp status` 让它自动探测 |
| `没有找到 systemd 单元 triune-panel` | 面板是手动前台跑的 / 在容器里。用 `--unit` 指定，或回到那个终端 Ctrl-C |
| `"st" 有歧义` | 打全：`tp status` 或 `tp start` |
| `需要 root 权限：sudo tp restart` | 启停服务必须 root，这是 systemd 的要求 |
| `面板已启动，但 10s 内没有就绪` | 端口被占 / 数据目录权限 / 配置损坏。看 `tp logs` |
| `这不像面板二进制` | `tp update --file` 给的文件不是面板，或架构不对（`file <路径>` 看一眼） |
| `找不到控制套接字` | 写操作需要面板在跑。`sudo tp start`；手动前台跑的用 `--sock` 指定 |
| `connect: connection refused` | 套接字文件在但没人监听——面板刚崩。看 `tp logs -n 50` |
| `面板拒绝了这次操作：…（对端 uid=1000）` | 控制套接字只接受 root 或面板运行账号。加 `sudo` |
| `apply` 报 422「运行中配置未变」 | 渲染或内核校验没过。运行中的配置**没有**被改动，修完再试 |
| `tp in off` 之后端口还在服务 | 加了 `--no-apply`。跑一次 `tp apply <内核>` |
| 输出中文列没对齐 | 终端字体不是等宽 CJK 字体。表格按显示宽度算，正常等宽字体不会歪 |
