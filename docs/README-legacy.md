# HomeVault

**一个单文件、零依赖、开箱即用的自建网盘：`go build` 出一个二进制就能跑，不需要数据库、不需要 Node、不需要配置文件。**

```bash
go build -o homevault . && ./homevault serve
# 打开 http://127.0.0.1:8080/ ，用 admin / admin 登录（会强制要求改密码）
```

---

## 为什么再做一个

自建网盘的项目已经很多了，但真要在自己的小服务器、NAS 或者家里的旧电脑上跑一个"给自己和家人朋友用"的网盘时，常见的门槛是这样的：

- **Nextcloud** 功能最全，但要一套 PHP + 数据库 + Redis + 一堆扩展，装完还要担心升级把数据搞坏；
- **Seafile** 性能好，但依赖 Python 与 MySQL，还得单独部署一个文件服务进程；
- **FileBrowser / Alist** 很轻，但更像"文件管理器"：没有真正的配额、没有回收站保留期、没有审计日志、没有多用户各自的存储空间；
- 还有一些用 Go 写的项目，前端要单独 `npm run build`，或者引入了十几个第三方库，升级要跟着一堆依赖走。

HomeVault 想解决的就是这个夹缝里的需求 —— **"我就想要一个能放心交给家人用、自己维护成本接近零的网盘"**：

| 它的选择 | 换来什么 |
| --- | --- |
| **只用 Go 标准库**，零第三方依赖 | `go build` 不需要联网拉依赖，永远不会因为某个库停止维护而卡住；供应链攻击面极小 |
| **前端直接内嵌进二进制**（`//go:embed`） | 没有构建步骤、没有 `node_modules`、没有静态资源的部署路径问题；一个文件就是完整应用 |
| **元数据用单个 JSON 文件 + 原子替换** | 没有数据库要装、要备份、要迁移；`state.json` 拷走就是完整的账号与配置 |
| **零配置启动** | 第一次运行自动写出 `config.ini` 并创建管理员，不需要先读半小时文档 |
| **引导配置只有 6 项** | 其余参数在管理页面里改，改完立即生效，不需要重启 |
| **配额真的会拦上传** | 很多同类项目的"配额"只是个显示数字；HomeVault 在上传、复制、回收站恢复、分片合并四处都做校验 |
| **拒绝跟随符号链接** | 逐段 `Lstat` 检查，符号链接不进列表、不进搜索、不可复制，避免"顺着链接读到整个磁盘" |
| **写文件是临时文件 + fsync + 原子 rename** | 任何时刻断电都不会留下半个文件 |

它的定位很明确：**面向几十个用户、几万到几十万个文件**。再往上（比如几百万文件、需要并发写入元数据）就该换 SQLite 了 —— 见 [已知限制](#已知限制)。

---

## 功能

**存储与操作**

- 目录浏览、多级目录、新建文件夹、重命名、移动、复制、删除
- 小文件直传（multipart）与大文件**分片上传**：默认 8 MiB 分片，支持断点续传、分片重传幂等、刷新页面后续传
- 下载支持 HTTP `Range`（音视频可拖动进度条、下载可断点续传）
- 回收站：软删除、恢复、永久删除、清空，支持保留期
- 批量操作（单次最多 500 项）

**查找与预览**

- 递归文件名搜索，支持按类型、扩展名、大小区间、时间区间、子树过滤
- 搜索结果带聚合统计：按类型 / 扩展名 / 大小区间 / 日期分布
- 图片、PDF、文本、音频、视频由浏览器直接内联预览（不需要服务端处理）
- Word / Excel / PPT / ODF / RTF 可选调用 LibreOffice 在服务端转 PDF 预览，转换结果带缓存

**多用户与安全**

- 多账号，每个账号独立的存储空间标识（`root`）与独立的配额、限速
- **各账号的 `root` 必须互不相同、也不能互相嵌套**（否则一个账号能看到另一个账号的文件、用量还会互相计入）；冲突时返回 `400 root_conflict`
- **管理员不占用存储空间**，只能以某个用户的身份去操作文件，且每个写操作都要重新输入管理员密码
- **账号管理与服务端设置只允许管理员**：`/api/users*`、`/api/settings*`、`/api/system/disks` 对非管理员一律 `403`，即使该账号被授予了 `users_manage`（防止普通用户自己造一个管理员账号来提权）
- **管理员有且只有一个**：接口不接受创建第二个管理员，也不允许把管理员降级；普通账号只可能有 `read` / `upload` / `modify` / `delete` 四个权限位，回收站与审计日志是管理员职能
- **空间总容量**：`spaceQuota` 默认 5 GB，所有账号的配额之和不能超过它（10 GB 的空间里 A 分了 8 GB，B 最多只能再分 2 GB）；写满后上传/复制返回 `507 space_full`
- 首次启动创建的默认管理员必须改账号或密码，否则除改凭据外所有接口都返回 `403`
- 密码用 PBKDF2-HMAC-SHA256（210000 次迭代 + 随机盐）存储
- 会话是 HMAC-SHA256 签名的无状态 Cookie，令牌里带密码版本号；改密码会让该账号所有旧会话**立即失效**
- 登录失败限流：同账号 5 次 / 5 分钟；IP 维度只在同一 IP 上出现 **8 个不同账号**失败时才封禁（避免 NAT/办公网连坐）；`429` 带 `Retry-After`（秒）
- 「修改凭据」的当前密码校验共用同一限流器，不能拿来无限猜密码
- 精细权限位：`read` / `upload` / `modify` / `delete` / `trash_view` / `trash_manage` / `users_manage` / `logs_view`
- 路径安全：拒绝绝对路径、拒绝 `..` 逃逸、拒绝符号链接、根目录不可操作
- 静态资源不引用任何外部域名，CSP 为 `default-src 'self'`

**运维**

- 审计日志：登录、上传、下载、删除、恢复、用户管理、设置变更等全部落盘；按天一个 JSONL 文件，支持按账号 / 动作 / 时间 / 关键字过滤，可导出带 BOM 的 CSV
- 后台自动清理：回收站过期（默认 30 天）、日志过期（默认 90 天）、未完成上传会话（默认 24 小时）、Office 预览缓存（24 小时）
- 磁盘用量与可选存储位置枚举（Windows 盘符 / Linux 挂载点）
- 命令行管理：`serve` / `migrate-space` / `setpass` / `adduser` / `version`
- 优雅退出（收到 SIGTERM 时保存元数据并等待在途请求结束）

---

## 一分钟上手

### 方式一：从源码构建

需要 **Go 1.24 或更高版本**（`go.mod` 声明 `go 1.24`）。不需要联网 —— 本模块没有任何第三方依赖，也没有构建脚本。

```bash
git clone <仓库地址> homevault
cd homevault

# 构建（Windows 上把 homevault 换成 homevault.exe）
go build -o homevault .

# 运行：什么参数都不给也能跑
./homevault serve
```

想要一次性交叉编译出所有平台的产物，在 Windows 上执行：

```powershell
powershell -ExecutionPolicy Bypass -File .\build.ps1
```

产物在 `dist\` 下（windows/amd64、linux/amd64、linux/arm64、linux/arm/v7、darwin/amd64、darwin/arm64），脚本会打印每个产物的路径与大小。只想编某几个平台时用 `-Targets`：

```powershell
powershell -ExecutionPolicy Bypass -File .\build.ps1 -Targets linux/amd64,linux/arm64
```

构建完成后可以跑两个脚本，它们都会在临时目录里起一个真实进程（不碰你的正式数据）：

```powershell
# 冒烟：健康检查、首次启动的目录骨架、默认管理员登录、强制改密拦截、静态资源、429+Retry-After
powershell -ExecutionPolicy Bypass -File .\scripts\smoke.ps1

# 完整旅程：改密 → 建用户 → 建目录 → 分片上传 → 下载 → 搜索 → 删除 → 回收站恢复
#           → 审计留痕 → 配额拒绝(507) → 管理员代操作 → 管理员无目标存储(403)
powershell -ExecutionPolicy Bypass -File .\scripts\e2e.ps1

# 前端组件自检（需要 node）：登录按钮是否真的提交表单、首登改密面板是否渲染
node .\scripts\ui-selftest.mjs
```

> 注意：脚本需要先构建出 `homevault.exe`（默认路径是仓库根目录，可用 `-Binary` 指定别的路径）。

### 方式二：Docker

```bash
# 精简版（25–40 MB）：图片、PDF、文本、音视频预览
docker build -f deploy/Dockerfile.lite -t homevault:lite .
mkdir -p ./data
docker run -d --name homevault -p 127.0.0.1:8080:8080 \
  -v "$PWD/data:/data" --restart unless-stopped homevault:lite

# 完整版（700 MB–1 GB）：额外带 LibreOffice，支持 Office 文档转 PDF 预览
docker build -f deploy/Dockerfile.full -t homevault:full .
docker run -d --name homevault-full -p 127.0.0.1:8081:8080 \
  -v "$PWD/data-full:/data" --restart unless-stopped homevault:full
```

或者用 compose（`deploy/docker-compose.yml` 里两个服务用 profile 区分）：

```bash
cd deploy
docker compose --profile lite up -d --build
```

### 方式三：systemd（Linux 生产环境）

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin homevault
sudo install -m 0755 homevault /usr/local/bin/homevault
sudo mkdir -p /etc/homevault /var/lib/homevault/HomeVaultSpace
sudo chown -R homevault:homevault /var/lib/homevault
sudo install -m 0644 deploy/homevault.service /etc/systemd/system/homevault.service
sudo systemctl daemon-reload && sudo systemctl enable --now homevault
sudo journalctl -u homevault -f
```

完整的目录权限、配置内容与加固说明见 [docs/DEPLOY.md](docs/DEPLOY.md)。

---

## 首次启动与强制改密

第一次运行时会做三件事：

1. 在当前目录写出 `config.ini`（权限 `0600`，6 项配置，全部有非空默认值）；
2. 创建空间目录骨架（`files/`、`trash/`、`audit/`、`uploads/`、`cache/`）；
3. 创建管理员账号，用户名与密码取自 `admin_user` / `admin_password`（默认 `admin` / `admin`），并打上"必须修改凭据"标记。

控制台会打印：

```
  homevault 0.1.1 已启动
  ─────────────────────────────────────────────
  用户端：  http://127.0.0.1:8080/
  管理端：  http://127.0.0.1:8080/admin/
  空间目录：/root/HomeVaultSpace
  配置文件：/var/lib/homevault/config.ini
  Office 预览：不可用（未检测到 LibreOffice，将退回内置预览）
  ─────────────────────────────────────────────
  首次启动已创建管理员：admin
  登录后必须修改账号或密码，否则无法使用其他功能。
```

在修改之前，**除了健康检查、登出、查看自己和修改凭据之外的接口一律返回 `403 must_change_credentials`**，这是为了让默认密码不可能被留在生产环境里。

> 空间目录在 Linux 上的系统默认值是 `/root/HomeVaultSpace`。以非 root 用户运行（例如 systemd 的 `homevault` 用户）时，**必须**在 `config.ini` 里显式设置 `space_dir`，否则会因为 `/root` 不可写而启动失败。

---

## 用户端与管理端

界面分成两套页面，各有独立入口：

| 入口 | 路径 | 谁用 | 能做什么 |
| --- | --- | --- | --- |
| **用户端** | `/` | 普通用户（`web/index.html` + `web/app.js`） | 浏览自己的文件、上传下载、搜索预览、回收站、改自己的账号密码 |
| **管理端** | `/admin/` | 只有管理员（`web/admin.html` + `web/admin.js`） | 选一个用户去管理他的文件、创建/编辑/禁用用户、改运行期设置、看审计日志、看系统与磁盘用量 |

管理员登录后前端会**自动跳转到 `/admin/`**；普通用户访问 `/admin/` 会被引导回 `/`。访问 `/admin`（不带结尾斜杠）会 `301` 跳到 `/admin/`。

两套页面共用 `web/ui.js` 这个 ES module：`/app.js` 与 `/admin.js` 都从它 `import` 公共的请求封装与组件，所以**登录状态、主题、上传队列在两端是一致的**。

关键设计：**管理员没有自己的存储空间**。管理员账号的 `root` 必须为空，所以：

- 文件类接口在管理员**不带** `?user=<账号>` 时返回 `403 admin_no_storage`（"管理员没有自己的存储空间，请在管理页面选择目标用户后再操作"）；
- 管理员在管理端选定某个用户后，前端之后的每个请求都会带上 `?user=<账号>`，请求以该用户的身份与根目录执行；
- 这种"代操作"里的**每一个写操作都要重新输入管理员密码**（不做任何时间窗缓存），密码既可以用请求体的 `password` 字段传，也可以用请求头 `X-LD-Password-B64`（base64）或 `X-LD-Password`；缺密码返回 `428 reauth_required`，密码错误返回 `403 reauth_failed`（并计入登录限流，429 会带 `Retry-After`）；
- 每一次代操作都会在审计日志里留下 `actingAs` 字段，值就是**被操作的那个目标账号**（管理员以 alice 的身份操作 → `actingAs: "alice"`）。

未登录访问任意界面都会回到登录页。

---

## 目录结构

```
homevault/
├── main.go                    CLI 入口：serve / migrate-space / setpass / adduser / version
├── go.mod                     模块声明（仅标准库，无 require）
├── build.ps1                  Windows 交叉编译脚本 → dist/
├── .gitignore
├── .dockerignore
├── internal/
│   ├── config/                引导配置：解析、校验、模板、空间目录布局
│   ├── state/                 元数据：用户、回收站索引、上传会话、运行期设置
│   ├── auth/                  密码哈希（PBKDF2）、会话令牌（HMAC）、登录限流
│   ├── fsops/                 路径清洗与安全解析、复制/移动/临时文件
│   ├── audit/                 审计日志：追加写、查询、CSV 导出、过期清理
│   ├── core/                  业务编排：Service、搜索、预览、配额与用量
│   └── server/                HTTP 层：路由、鉴权中间件、各接口处理器
├── web/                       前端（//go:embed all:web 内嵌进二进制）
│   ├── index.html             用户端外壳 → 加载 app.js
│   ├── app.js                 用户端逻辑（import 共享库 ui.js）
│   ├── admin.html             管理端外壳 → 加载 admin.js
│   ├── admin.js               管理端逻辑（选目标用户、用户管理、设置、日志；同样 import ui.js）
│   ├── ui.js                  共享 ES module：请求封装、弹层、上传队列、各类组件
│   ├── icons.svg              SVG sprite（无外部 CDN）
│   └── style.css
├── scripts/
│   ├── smoke.ps1              冒烟测试：临时空间 + 真实二进制跑关键路径
│   ├── e2e.ps1                完整用户旅程：上传/下载/回收站/审计/配额/代操作
│   └── ui-selftest.mjs        前端组件自检（node，不需要浏览器）
├── docs/
│   ├── API.md                 完整 HTTP 接口契约（11 节，含 curl 全流程）
│   ├── CONFIG.md              配置文件与运行期设置逐项说明
│   ├── DEPLOY.md              Docker / systemd / 反向代理部署
│   └── UPGRADE_BACKUP.md      升级、备份、恢复、换盘
└── deploy/
    ├── Dockerfile.lite        精简版镜像（alpine，约 25–40 MB）
    ├── Dockerfile.full        完整版镜像（debian + LibreOffice，700 MB–1 GB）
    ├── docker-compose.yml     lite / full 两个 profile
    ├── homevault.service      systemd 单元（含加固项）
    ├── Caddyfile              反向代理示例（Caddy，自动 HTTPS）
    ├── nginx.conf             反向代理示例（Nginx）
    └── env.example            环境变量示例（只有 HOMEVAULT_SOFFICE）
```

---

## 命令行

```
homevault serve [-config 路径] [-log-level debug|info|warn|error]
homevault migrate-space --to <目录> [-config 路径] [-yes]
homevault setpass -user <账号> [-password <新密码>] [-config 路径]
homevault adduser -user u -password p [-root r] [-quota 10GiB] [-rate 50] [-admin] [-display 名字] [-config 路径]
homevault version
homevault help
```

| 命令 | 说明 |
| --- | --- |
| `serve` | 启动服务（也是默认命令，直接跑 `homevault` 等于 `homevault serve`） |
| `migrate-space` | 把空间目录整体搬迁到新位置：复制 → 校验 → 改写 `config.ini` → 提示重启。**不删除旧数据** |
| `setpass` | 重置某个账号的密码；不给 `-password` 会随机生成 16 位并打印到控制台 |
| `adduser` | 命令行创建账号（也可以全程在 `/` 页面的「用户」里做） |
| `version` | 打印版本号（当前 `0.1.1`） |
| `help` | 打印帮助（`-h` / `--help` 同效；`-v` / `--version` 等于 `version`） |

对应关系：`migrate-space` 等价于升级与换盘流程里的数据搬迁；`setpass` 用于"管理员密码忘了"；`adduser` 用于批量初始化。

---

## 配置速查

引导配置（`config.ini`，**改完必须重启**）只有 6 项：

| 键 | 默认值 | 说明 |
| --- | --- | --- |
| `listen` | `"0.0.0.0:8080"` | 监听地址；放在反向代理后面时建议改 `"127.0.0.1:8080"` |
| `space_dir` | `""`（用系统默认） | 空间目录的绝对路径。Windows 默认 `C:\HomeVaultSpace`，Linux 默认 `/root/HomeVaultSpace` |
| `tls_cert` | `""` | TLS 证书路径；必须与 `tls_key` 同时设置才启用内置 HTTPS |
| `tls_key` | `""` | TLS 私钥路径 |
| `admin_user` | `"admin"` | **只在首次启动创建管理员时读取**，之后改这里不影响已有账号 |
| `admin_password` | `"admin"` | 同上；之后改密码请用管理页面或 `homevault setpass` |

**未知配置键会导致启动失败**（不会静默忽略）—— 运行期参数请写在管理页面里。完整的语法规则、引号与注释行为见 [docs/CONFIG.md](docs/CONFIG.md)。

---

## 运行期设置

在管理端「系统设置」里改，**保存后立即生效**（只有"存储位置"需要重启）：

| 设置项 | 默认值 | 说明 |
| --- | --- | --- |
| 存储位置 `spaceDir` | 空（沿用 `config.ini`） | 换盘用；有数据时必须先 `migrate-space` |
| 默认配额 `defaultQuota` | `10GiB` | 新建用户的默认配额，`0` = 不限 |
| 默认限速 `defaultRateLimitMbps` | `0` | 新建用户的默认双向限速（Mbps），`0` = 不限 |
| 回收站保留期 `trashRetention` | `720h`（30 天） | `0` = 永不自动清理 |
| 日志保留期 `logRetention` | `2160h`（90 天） | `0` = 不清理 |
| 上传会话有效期 `uploadSessionTTL` | `24h` | 未完成分片上传的过期时间 |
| 单文件大小上限 `maxUploadSize` | `0` | `0` = 不限；必须 ≥ 分片大小 |
| 分片大小 `chunkSize` | `8MiB` | 范围 `[64KiB, 1GiB]` |
| 登录会话有效期 `sessionTTL` | `168h`（7 天） | 只影响之后登录产生的会话 |
| 信任反向代理 `trustProxy` | `false` | 打开后才采信 `X-Forwarded-For` / `X-Real-IP` |
| 关闭访问日志 `accessLogDisabled` | `false` | 只关 HTTP 访问日志，审计日志不受影响 |
| Office 预览 `officePreview` | `auto` | `auto` / `off` / `builtin` |

容量可以写字节数或 `"10GiB"` 这类字符串；时长可以写 `"720h"` 或 `"30d"`。逐项细节见 [docs/CONFIG.md](docs/CONFIG.md)。

---

## 安全说明

| 方面 | 实现 |
| --- | --- |
| 密码存储 | PBKDF2-HMAC-SHA256，210000 次迭代，16 字节随机盐，格式 `pbkdf2-sha256$迭代次数$盐$散列` |
| 会话 | HMAC-SHA256 签名的无状态 Cookie（`homevault_session`），`HttpOnly` + `SameSite=Lax`，HTTPS 下自动加 `Secure`；令牌里带密码版本号，**改密码即让全部旧会话失效** |
| 会话绑定 | **不绑定 IP、不绑定 User-Agent**（换网络、换设备不会掉线）；安全性由短有效期与改密码失效机制保证 |
| 登录限流 | 同账号 5 次失败 / 5 分钟；IP 维度只在同一 IP 出现 **8 个不同账号**失败时才封（避免 NAT 连坐）；`429` 带 `Retry-After`；登录失败不区分"账号不存在"与"密码错误" |
| 二次密码 | 管理员代操作他人文件时，**每个写操作**都要重新输入管理员密码，无时间窗缓存；失败同样计入限流 |
| 账号管理 | `/api/users*`、`/api/settings*`、`/api/system/disks` **只允许管理员账号**（`users_manage` 不再单独放行，防止自造管理员提权） |
| 存储根 | 每个账号的 `root` 唯一且不能互相嵌套（大小写不敏感），冲突返回 `400 root_conflict` |
| 路径安全 | 逐段 `Lstat` 拒绝符号链接；以 `/` 为根做 `path.Clean` 使 `..` 无法逃逸；拒绝绝对路径；长度上限 4096 字节 |
| 文件写入 | 目标目录内临时文件 → `fsync` → 原子 `rename`，不会出现半个文件 |
| 前端安全 | 不引用任何外部域名；CSP `default-src 'self'`；HTML 响应 `Cache-Control: no-cache` |
| TLS | 可以内置（`tls_cert` / `tls_key`），也可以交给反向代理终结 |

**要暴露到公网时请务必**：改掉默认管理员密码、把监听地址改成 `127.0.0.1` 并挂反向代理（或配置内置 TLS）、打开"信任反向代理"以便审计日志记录真实 IP。检查清单见 [docs/DEPLOY.md](docs/DEPLOY.md) §7。

---

## 常见问题

**忘了管理员密码怎么办？**

停掉服务，然后：

```bash
homevault setpass -user admin                 # 随机生成 16 位密码并打印
homevault setpass -user admin -password '新密码'   # 指定新密码
```

不需要数据库操作，直接改 `state.json`（改密码会让该账号的所有旧会话失效，需要重新登录）。

**怎么把数据换到另一块盘？**

用命令行的方式最稳：

```bash
sudo systemctl stop homevault
homevault migrate-space --to /mnt/big/HomeVaultSpace
sudo systemctl start homevault
```

它会复制 `files/`、`trash/`、`audit/`、`uploads/`、`cache/`、`state.json`，校验目标里出现了 `state.json`，然后把新路径写回 `config.ini`，**不会删除旧数据**。

也可以直接在管理端「系统设置 → 存储位置」里改，但服务端会先检查目标位置是否已有 `state.json`；如果当前空间有数据而目标没有，会返回 `400 migration_required` 并提示你先跑 `migrate-space`（这是为了防止"切过去就看起来丢数据"）。用 systemd 部署且新路径不在 `/var/lib/homevault` 下时，别忘了同步修改单元文件里的 `ReadWritePaths`。

**Office 预览需要什么条件？**

需要目标机器上有 **LibreOffice**（`soffice`），以及**中文字体**（否则中文会显示成方块）：

```bash
# Debian / Ubuntu
sudo apt-get install -y --no-install-recommends \
  libreoffice-writer libreoffice-calc libreoffice-impress fonts-noto-cjk
```

装在非标准位置时用环境变量指定：

```bash
HOMEVAULT_SOFFICE=/opt/libreoffice/program/soffice homevault serve
```

检测结果缓存 60 秒。**没有 LibreOffice 也能正常使用** —— Office 文件会退化为"提示下载查看"（接口返回 `409 office_preview_unavailable`），图片、PDF、文本、音视频的预览完全不依赖它。Docker 场景直接用 `deploy/Dockerfile.full`（已装好这些包）。不需要这个功能时，把运行期设置 `officePreview` 改成 `off` 即可彻底跳过。

**怎么挂在反向代理后面？**

核心是四件事：**不缓冲请求体**、**不限制请求体大小**、**拉长超时**、**不缓存 `/api/`**。

Nginx：

```nginx
client_max_body_size 0;
proxy_request_buffering off;
proxy_buffering off;
proxy_read_timeout 3600s;
proxy_send_timeout 3600s;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
```

Caddy 的 `reverse_proxy` 默认就是流式转发，不需要显式关缓冲；把 `reverse_proxy 127.0.0.1:8080` 交给它并依赖自动 HTTPS 即可。

**改完还要做一件事**：在管理端「系统设置」里打开「信任反向代理」。不打开的话，服务端不会采信 `X-Forwarded-For` / `X-Real-IP`，审计日志里记录的会是代理的 IP。完整示例见 `deploy/nginx.conf` 与 `deploy/Caddyfile`。

**怎么改默认配额和限速？**

管理端「系统设置」里改 **默认配额** 与 **默认限速**，保存后立即生效 —— 但注意它们**只影响之后新建的用户**，不会改动已有用户。要给已有用户单独调整，去「用户管理」里编辑该用户。

**为什么管理员不能上传文件？**

因为**管理员没有自己的存储空间**。管理员账号的 `root` 为空（后端会强制校验），所以不存在"管理员的目录"可以上传到：文件类接口在管理员不带 `?user=<账号>` 时直接返回 `403 admin_no_storage`。

设计上管理员要以某个用户的身份去操作文件：在管理端「用户」里选中目标账号后，前端之后的每个请求都会带 `?user=<账号>`，每个写操作都要再输一次管理员密码，并在审计日志里留下 `actingAs`（值就是被操作的账号）。管理端还提供「文件」页，可以直接浏览/上传/删除所选用户的文件。

这个设计的好处是：管理员权限和文件数据彻底分离，不存在"管理员偷偷有自己的空间"这种模糊地带；而且每次代操作都会在审计日志里留下 `actingAs` 记录。

---

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/API.md](docs/API.md) | 完整 HTTP 接口契约：鉴权、文件、分片上传、搜索预览、回收站、审计日志、用户管理、系统设置、路径与权限规则，最后一节是用 curl 走完一遍 |
| [docs/CONFIG.md](docs/CONFIG.md) | `config.ini` 的语法与 6 项配置、空间目录布局、`state.json` 结构、12 项运行期设置逐项说明、换盘流程 |
| [docs/DEPLOY.md](docs/DEPLOY.md) | 二进制 / Docker / systemd / 反向代理四种部署方式，Office 预览依赖，生产环境检查清单 |
| [docs/UPGRADE_BACKUP.md](docs/UPGRADE_BACKUP.md) | 备份什么、怎么备份（rsync / robocopy / 快照）、怎么恢复、怎么升级与回滚、怎么换盘、常见事故处置 |

---

## 已知限制

诚实地列出来，避免用错场景：

- **元数据是单个 JSON 文件**：每次变更都会原子重写整个 `state.json`。这适合**几十个用户、几万到几十万个文件**；再往上（高频并发写入元数据）应该换 SQLite —— 代码里的 `state` 层是刻意隔离的，替换成本可控。
- **没有文件版本历史**：同名文件不覆盖（自动改名 `a (1).txt`），但改过之后没有历史版本可回退。
- **没有公开分享链接**：没有"生成一个外链给未登录的人下载"的功能。
- **没有跨账号共享目录**：每个账号一个独立的 `root`，且 `root` 不允许重复或互相嵌套（见「功能」一节）。想让两个人看到同一批文件，目前只能各自上传。
- **没有在线编辑**：Office 预览是"转 PDF 看"（或交给前端内置渲染器），不能在线编辑、不能多人协同。
- **没有内置的完整备份打包**：不提供"一键导出全站 zip"。备份请用 `rsync` / `robocopy` 抄整个空间目录，配置清单用 `GET /api/settings` 与 `GET /api/users` 导出（详见 [docs/UPGRADE_BACKUP.md](docs/UPGRADE_BACKUP.md)）。
- **缩短回收站保留期不会追溯生效**：`ExpiresAt` 是删除那一刻按当时的保留期算好并写进 `state.json` 的，之后改 `trashRetention` 只影响新删除的文件，已有条目的过期时间不会提前。
- **配额按"已用 + 回收站 + 新增"判定**：`已用` 里还包含该账号"已收分片但尚未提交"的上传占用（否则可以靠只传不提交把磁盘放大数倍）。删除与恢复**不做配额校验**，所以配额满也删得掉。
- **限流是进程内的令牌桶**：按用户、分上传/下载两个方向限速；多进程部署时各自独立（本项目的设计目标本来就是单进程）。
- **会话不绑定 IP / UA**：这是有意的（避免换网络掉线），代价是 Cookie 泄露后到有效期结束前都能用 —— 因此默认有效期是 7 天；令牌里带密码版本号，改密码会让该账号的全部旧会话立即失效。

---

## 许可

见仓库根目录的 LICENSE 文件。
