# HomeVault 配置说明

HomeVault 的配置分成两层，这是刻意的设计：

| 层次 | 存放位置 | 内容 | 生效方式 |
| --- | --- | --- | --- |
| **引导配置** | `config.ini` | 只有 6 项：监听地址、空间目录、TLS 证书与私钥、首次启动的管理员账号与密码 | 改完**必须重启**进程 |
| **运行期设置** | `<space_dir>/state.json` | 配额、限速、保留期、单文件上限、分片大小、会话 TTL、信任代理、访问日志、Office 预览模式、存储位置 | 管理页面改完**立即生效**（存储位置除外，需重启） |

引导配置里只放"改了就换进程形态"的参数；其余参数都放在运行期设置里，避免"改个限额要重启服务"。

---

## 1. 配置文件 `config.ini`

### 1.1 从哪来

- 默认路径：**进程当前工作目录**下的 `config.ini`。
- 可用 `-config` 指定：`homevault serve -config /etc/homevault/config.ini`。
- 文件**不存在时自动创建**（权限 `0600`），内容为内置模板（见 §1.4），目录不存在会自动创建（`0750`）。
- 服务启动时会在控制台打印真实使用的配置文件绝对路径：

```
  homevault 0.1.2 已启动
  ─────────────────────────────────────────────
  用户端：  http://127.0.0.1:18090/
  管理端：  http://127.0.0.1:18090/admin/
  空间目录：/var/lib/homevault/HomeVaultSpace
  配置文件：/etc/homevault/config.ini
  Office 预览：不可用（未检测到 LibreOffice，将退回内置预览）
```

### 1.2 语法规则

| 规则 | 说明 |
| --- | --- |
| 形式 | 扁平的 `key = value`，**不支持 `[section]` 分节**（遇到 `[` 直接启动失败） |
| 键大小写 | 不敏感（`Listen` 与 `listen` 等价） |
| 注释 | 整行注释用 `#` 或 `;` |
| 行内注释 | **值不加引号时**，`#` 与 `;` 之后的内容会被当注释截断 |
| 引号 | 值用双引号包裹；含 `#`、`;`、空格、反斜杠或空值的路径**必须**加引号 |
| 引号闭合 | `"` 开头但没闭合 → 启动失败；引号值后面允许跟 `#` / `;` 行尾注释（`listen = "0.0.0.0:18090" # 监听地址`），其它多余内容会报错 |
| 空值 | `listen` 与 `admin_user` 不允许为空；`tls_cert` / `tls_key` 允许为空；`space_dir` 允许为空（= 用系统默认） |
| 未知键 | **启动直接失败**，消息形如 `第 12 行：未知配置项 "max_upload_size"（运行期参数请在管理页面配置）`。绝不静默忽略 |
| 热加载 | 没有。改完必须重启进程（`systemctl restart homevault` 或 Ctrl+C 后重新运行） |

> 为什么未知键要报错：写错名字（例如把运行期参数写进 config.ini）如果被静默忽略，运维会以为配置生效了。直接启动失败比"以为生效"安全。

### 1.3 六项配置

| 键 | 类型 | 默认值 | 留空时 | 说明 |
| --- | --- | --- | --- | --- |
| `listen` | string | `"0.0.0.0:18090"` | 不允许 | HTTP(S) 监听地址。放在反向代理后面时建议改成 `"127.0.0.1:18090"` |
| `space_dir` | string | `""`（模板值） | 用系统默认 | 空间目录的绝对路径。**相对路径会相对于配置文件所在目录解析**；必须是绝对路径，否则启动失败 |
| `tls_cert` | string | `""` | 不启用 HTTPS | TLS 证书路径（PEM）。**必须与 `tls_key` 同时非空**才启用内置 HTTPS，否则建议交给反向代理终结 TLS |
| `tls_key` | string | `""` | 不启用 HTTPS | TLS 私钥路径（PEM） |
| `admin_user` | string | `"admin"` | 回落到 `admin` | **只在首次启动创建管理员时读取**，之后改这里不会改动已有账号 |
| `admin_password` | string | `"admin"` | 回落到 `admin` | 同上。首次启动用它生成管理员密码哈希；之后改这里不会改密码（改密码用管理页面或 `homevault setpass`） |

`space_dir` 为**空**时的系统默认值（`config.DefaultSpaceDir()`）：

| 平台 | 默认空间目录 |
| --- | --- |
| Windows | `C:\HomeVaultSpace` |
| Linux / macOS 等 | `/root/HomeVaultSpace` |

> Linux 上的默认值写在 `/root` 下，属于**以 root 运行时的应急默认**。以非 root 用户（例如 systemd 的 `homevault` 用户）运行时，`/root` 通常不可写，**必须**在 `config.ini` 里显式设置 `space_dir`，否则启动时会报"创建空间目录失败"。部署文档里的示例一律显式设置。

### 1.4 内置模板（首次启动自动写出）

```ini
# homevault 引导配置
# 绝大多数参数不需要在这里配置：启动后在管理页面修改并立即生效。
# 这里只放"改了就换进程形态"的必须项。
# 含 # 的值请用双引号包起来。改完需要重启进程。

# HTTP 监听地址。放在反向代理后面时建议改成 127.0.0.1:18090。
listen = "0.0.0.0:18090"

# 空间目录：用户文件、回收站、审计日志、上传分片、缓存与元数据都在其中。
# 目录整体可搬迁：换盘时改这里 + 执行 homevault migrate-space。
space_dir = ""

# 同时设置下面两项才启用内置 HTTPS；否则建议由反向代理（Caddy / Nginx）终结 TLS。
tls_cert = ""
tls_key = ""

# 首次启动创建管理员用。默认 admin / admin，登录后会强制要求改账号或密码。
# 只有第一次启动（空间目录里还没有 state.json）时读取这两项。
admin_user = "admin"
admin_password = "admin"
```

### 1.5 典型配置

**A. 开发/内网直连（默认即用）**

```ini
listen = "0.0.0.0:18090"
space_dir = "D:\\HomeVaultSpace"
tls_cert = ""
tls_key = ""
admin_user = "admin"
admin_password = "admin"
```

**B. 多人各自独立的空间（每个账号一个根目录）**

```ini
listen = "0.0.0.0:18090"
space_dir = "D:\\HomeVaultSpace"
# 账号与它们的 root 都在管理页面创建；每个 root 必须唯一且不能互相嵌套。
# 想让两个人看同一批文件：只给一个账号分配该 root，另一个账号用共享凭据登录。
admin_user = "admin"
admin_password = "改成你自己的强密码"
```

**C. 反向代理后（Caddy / Nginx 终结 TLS）**

```ini
listen = "127.0.0.1:18090"
space_dir = "/var/lib/homevault/HomeVaultSpace"
tls_cert = ""
tls_key = ""
admin_user = "admin"
admin_password = "改成你自己的强密码"
```

别忘了在管理页面把「信任反向代理」打开，否则审计日志里记录的会是代理的 IP。详见 [DEPLOY.md](DEPLOY.md)。

**D. 内置 HTTPS（不挂反代）**

```ini
listen = "0.0.0.0:8443"
space_dir = "/var/lib/homevault/HomeVaultSpace"
tls_cert = "/etc/homevault/tls/fullchain.pem"
tls_key = "/etc/homevault/tls/privkey.pem"
admin_user = "admin"
admin_password = "改成你自己的强密码"
```

同时设置了 `tls_cert` 与 `tls_key` 时服务端启动 `ListenAndServeTLS`，并且登录 Cookie 会自动带 `Secure`。

**E. Windows 服务化（用 NSSM 之类的包装器）**

```ini
listen = "127.0.0.1:18090"
space_dir = "E:\\HomeVaultSpace"
tls_cert = ""
tls_key = ""
admin_user = "admin"
admin_password = "改成你自己的强密码"
```

---

## 2. 空间目录布局

`space_dir` 下固定是这些内容（启动时自动创建，权限 `0750`）：

```
<space_dir>/
├── files/           用户文件根目录；每个 root 是其中一层或多层子目录
│   ├── alice/       root = "alice"
│   └── shared/
│       └── team/    root = "shared/team"
├── trash/           回收站；每条记录一个子目录
│   └── t_1a2b…/
│       └── payload  被删除条目的实体（文件或目录）
├── audit/           审计日志，按天一个文件
│   ├── audit-2026-02-13.jsonl
│   └── audit-2026-02-14.jsonl
├── uploads/         分片上传的临时分片；每个会话一个子目录
│   └── u_6f1c…/
├── cache/
│   └── office/      Office→PDF 预览缓存（24 小时自动清理）
└── state.json       全部元数据：用户、回收站索引、上传会话、运行期设置
```

| 路径 | 谁在写 | 说明 |
| --- | --- | --- |
| `files/` | 业务写入 | 用户数据唯一存放处；备份的重心 |
| `trash/` | 删除/恢复/清理 | 恢复是"从 `trash/<id>/payload` 搬回 `files/<root>/<原路径>`" |
| `audit/` | 每个动作 | 追加写，每天一个 JSONL 文件；保留期到点按文件删除 |
| `uploads/` | 分片上传 | `complete` 时合并成文件；未完成的会话默认 24 小时后清理 |
| `cache/office/` | Office 预览 | 可随时整体删除，只会导致下次预览重新转换 |
| `state.json` | 每次元数据变更 | **单文件 + 原子替换**（写临时文件 → `fsync` → `rename`），因此永远是完整 JSON |

`state.json` 的结构（节选，字段名与 `internal/state` 一致）：

```json
{
  "users": {
    "admin": {
      "username": "admin",
      "displayName": "系统管理员",
      "passwordHash": "pbkdf2-sha256$210000$…$…",
      "root": "",
      "permissions": { "read": true, "upload": true, "…": true },
      "isAdmin": true,
      "quota": 0,
      "rateLimitMbps": 0,
      "disabled": false,
      "mustChangeCredentials": true,
      "createdAt": "2026-02-14T10:00:00Z",
      "updatedAt": "0001-01-01T00:00:00Z"
    }
  },
  "trash": [ { "id": "t_1a2b…", "name": "a.pdf", "originalPath": "docs/a.pdf", "root": "alice",
               "size": 12345, "isDir": false, "deletedBy": "alice",
               "deletedAt": "2026-02-14T10:00:00Z", "expiresAt": "2026-03-16T10:00:00Z" } ],
  "uploads": { "u_6f1c…": { "id": "u_6f1c…", "username": "alice", "dir": "videos",
                            "name": "a.mp4", "size": 1073741824, "chunkSize": 8388608,
                            "chunkCount": 128, "received": [0,1,2],
                            "createdAt": "…", "expiresAt": "…" } },
  "settings": {
    "defaultQuota": 10737418240,
    "defaultRateLimitMbps": 0,
    "trashRetention": "720h",
    "logRetention": "2160h",
    "uploadSessionTTL": "24h",
    "maxUploadSize": 0,
    "chunkSize": 8388608,
    "sessionTTL": "168h",
    "trustProxy": false,
    "accessLogDisabled": false,
    "officePreview": "auto",
    "spaceDir": ""
  }
}
```

`state.json` 里**不含任何明文密码**，只有 PBKDF2-HMAC-SHA256 哈希（格式 `pbkdf2-sha256$210000$<盐>$<散列>`）。所以它可以直接备份、也可以直接看。

---

## 3. 运行期设置（`state.json` + 管理页面）

管理页面路径：**`/admin/` → 左侧「存储与设置」**。对应接口是 `GET /api/settings` 与 `PATCH /api/settings`，
**只有管理员**能访问（非管理员 403），且每次提交都要输入管理员自己的密码。普通用户看不到这一页。

| 设置项 | JSON 键 | 默认值 | 作用 | 重启 |
| --- | --- | --- | --- | --- |
| 存储位置 | `spaceDir` | `""` | 改到新位置时服务端会先把现有数据复制过去，然后提示重启 | **需要** |
| 监听地址 | `listen` | `""` | 改端口**不需要**再编辑 `config.ini`：保存时写回配置文件，重启后生效 | **需要** |
| TLS 证书 / 私钥 | `tlsCert` / `tlsKey` | `""` | 两项同时填写才启用内置 HTTPS；留空表示由反向代理终结 TLS | **需要** |
| 空间总容量 | `spaceQuota` | `5368709120`（5 GiB） | 整个空间目录的上限（所有用户 + 回收站 + 未提交分片）；`0` = 不限。所有账号配额之和不能超过它 | 否 |
| 默认配额 | `defaultQuota` | `1073741824`（1 GiB） | 新建用户的默认配额；不能超过 `spaceQuota` | 否 |
| 默认限速 | `defaultRateLimitMbps` | `0` | 新建用户的默认双向限速（Mbps），`0` = 不限 | 否 |
| 回收站保留期 | `trashRetention` | `"720h"`（30 天） | 过期条目由后台每 15 分钟清理；`"0"` = 永不清理 | 否 |
| 日志保留期 | `logRetention` | `"2160h"`（90 天） | 过期日志文件由后台每小时清理；`"0"` = 不清理 | 否 |
| 上传会话有效期 | `uploadSessionTTL` | `"24h"` | 未完成分片会话的过期时间，必须 > 0 | 否 |
| 单文件大小上限 | `maxUploadSize` | `0` | `0` = 不限（只受配额约束）；必须 ≥ `chunkSize` | 否 |
| 分片大小 | `chunkSize` | `8388608`（8 MiB） | 范围 `[64KiB, 1GiB]` | 否 |
| 登录会话有效期 | `sessionTTL` | `"168h"`（7 天） | 登录 Cookie 的有效期，必须 > 0 | 否 |
| 信任反向代理 | `trustProxy` | `false` | 打开后才采信 `X-Forwarded-For` / `X-Real-IP` | 否 |
| 关闭访问日志 | `accessLogDisabled` | `false` | 只关 HTTP 访问日志，**审计日志不受影响** | 否 |
| Office 预览 | `officePreview` | `"auto"` | `auto` = 有 LibreOffice 就服务端转 PDF；`off` = 关闭 Office 预览；`builtin` = 一律用前端内置渲染，不探测 LibreOffice | 否 |

> **配额口径**：`已用 = 用户根目录真实大小 + 尚未提交的分片占用`，判断式是
> `已用 + 回收站 + 新增 > 配额`。所以"只传分片不提交"同样会占配额，不能靠这个把磁盘放大数倍；
> 而**删除与恢复不做配额校验**（删除只是把文件挪进回收站，总额不变，配额满了也必须能删）。

### 值的写法

**容量类**（`defaultQuota`、`maxUploadSize`、`chunkSize`）两种写法等价：

```jsonc
{ "defaultQuota": 10737418240 }     // 纯字节数
{ "defaultQuota": "10GiB" }         // 带单位字符串
{ "defaultQuota": "0" }             // 不限
{ "defaultQuota": "unlimited" }     // 同上
```

支持单位：`B` `KB` `KiB` `MB` `MiB` `GB` `GiB` `TB` `TiB`。**`KB` 与 `KiB` 都按 1024 计算**（不区分二进制/十进制前缀）。允许小数（`"1.5GiB"`）。**不接受空字符串** —— 要表示不限必须显式写 `0`。

**时长类**（`trashRetention`、`logRetention`、`uploadSessionTTL`、`sessionTTL`）：

```jsonc
{ "trashRetention": "720h" }    // 30 天
{ "trashRetention": "30d" }     // 同上
{ "trashRetention": "2w" }      // 14 天
{ "trashRetention": "1h30m" }   // 组合写法
{ "trashRetention": "0" }       // 永不自动清理
```

支持单位：`d`（天）、`w`（周）、`h`、`m`、`s`，也兼容 Go 标准时长串。**负数会被拒绝**。

### 改设置的影响范围（重要）

| 设置项 | 改动后的实际影响 |
| --- | --- |
| `defaultQuota` / `defaultRateLimitMbps` | 只影响**之后新建**的用户，不改动已有用户 |
| `trashRetention` | 只影响**之后被删除**的文件；缩短保留期**不会**追溯把已有条目的过期时间提前 |
| `logRetention` | 只影响之后的清理任务，不会立即删除历史日志 |
| `uploadSessionTTL` | 只影响之后新建的分片会话；进行中的会话沿用创建时的有效期 |
| `maxUploadSize` | 只影响之后的上传；进行中的上传不受影响 |
| `chunkSize` | 只影响之后新建的分片会话 |
| `sessionTTL` | 只影响之后登录产生的会话；已登录会话按原有效期失效 |
| `trustProxy` | 立即生效 |
| `accessLogDisabled` | 立即生效 |
| `officePreview` | 立即生效（LibreOffice 探测结果本身缓存 60 秒） |
| `spaceDir` | **需要重启**；详见 §4 |

### 用 curl 改设置

```bash
curl -s -b admin.txt -X PATCH http://127.0.0.1:18090/api/settings \
  -H 'Content-Type: application/json' -d '{
  "defaultQuota": "10GiB",
  "defaultRateLimitMbps": 0,
  "trashRetention": "30d",
  "logRetention": "2160h",
  "uploadSessionTTL": "24h",
  "maxUploadSize": 0,
  "chunkSize": "8MiB",
  "sessionTTL": "168h",
  "trustProxy": false,
  "accessLogDisabled": false,
  "officePreview": "auto",
  "adminPassword": "你的管理员密码"
}'
```

**未知字段会被拒绝**（`400 bad_json`），所以键名写错会立刻暴露，不会被静默忽略。

---

## 4. 改存储位置（换盘）

有两种做法，**推荐命令行的做法**。

### 4.1 推荐：`homevault migrate-space`

```bash
# 先把服务停掉（迁移过程中不要让进程继续写）
sudo systemctl stop homevault

# 迁移到新盘：复制 files/trash/audit/uploads/cache/state.json → 校验 state.json → 改写 config.ini
homevault migrate-space --to /mnt/big/HomeVaultSpace

# 不加 -yes 时会先打印迁移计划并要求确认
# 重启
sudo systemctl start homevault
```

该命令会：创建目标目录 → 递归复制 `files/`、`trash/`、`audit/`、`uploads/`、`cache/` → 校验目标里已经出现 `state.json` → 把新路径写回 `config.ini` 的 `space_dir` → 提示重启。**它不会删除旧数据**，确认无误后可以自行清理旧目录。

### 4.2 管理页面改存储位置

在"系统设置"里改「存储位置」并保存时，服务端会依次检查：

1. 必须是**绝对路径**，否则 `400 invalid_setting`。
2. 目标位置必须**可写**（创建并删除一个探测文件），否则 `400 invalid_setting`。
3. 目标位置里**已经有 `state.json`**，或者当前空间本身是空的（还没有 `state.json`）。否则返回 `400 migration_required`：

```json
{
  "error": "目标位置里没有 state.json。请先执行 `homevault migrate-space --to \"/mnt/big/HomeVaultSpace\"` 完成数据迁移，再修改此设置",
  "code": "migration_required"
}
```

通过检查后，新路径会被写进 `config.ini`，接口响应的 `restartRequired` 包含 `"spaceDir"`，需要重启才生效。

> 为什么这么严：直接改路径而数据还在旧盘，重启后就会"看起来数据全没了"。宁可报错让人先迁移。

### 4.3 迁移后核对

```bash
# 三处必须一致
grep '^space_dir' /etc/homevault/config.ini        # 新路径
ls -l /mnt/big/HomeVaultSpace/state.json           # 存在
curl -s -b admin.txt http://127.0.0.1:18090/api/settings | grep -o '"spaceDir":"[^"]*"'
```

备份与升级流程见 [UPGRADE_BACKUP.md](UPGRADE_BACKUP.md)。

---

## 5. 环境变量

HomeVault 只有**一个**环境变量：

| 变量 | 作用 | 默认 |
| --- | --- | --- |
| `HOMEVAULT_SOFFICE` | 指定 LibreOffice 可执行文件的绝对路径，用于 Office→PDF 预览 | 空（自动探测 `soffice` / `libreoffice`，以及 Windows 下的标准安装路径） |

**其他配置一律不走环境变量**：引导配置只有 6 项，且都"改了就换进程形态"，放在文件里比散落在环境里更好审计、更好备份（`config.ini` 一个文件就够）；运行期参数放在管理页面里，改完立即生效，不需要重启，也不需要重放环境。日志级别是唯一的例外，它用命令行参数：`homevault serve -log-level debug`。

```bash
# Linux：把 soffice 指到自定义位置
HOMEVAULT_SOFFICE=/opt/libreoffice/program/soffice homevault serve -config /etc/homevault/config.ini

# Windows PowerShell
$env:HOMEVAULT_SOFFICE = "C:\Program Files\LibreOffice\program\soffice.exe"
.\homevault.exe serve -config .\config.ini
```

---

## 6. 命令行参数速查

```
homevault serve [-config 路径] [-log-level debug|info|warn|error]
homevault migrate-space --to <目录> [-config 路径] [-yes]
homevault setpass -user <账号> [-password <新密码>] [-config 路径]
homevault adduser -user u -password p [-root r] [-quota 10GiB] [-rate 50] [-admin] [-display 名字] [-config 路径]
homevault version
homevault help
```

| 命令 | 参数 | 默认 | 说明 |
| --- | --- | --- | --- |
| `serve` | `-config` | `config.ini` | 配置文件路径 |
| | `-log-level` | `info` | 日志级别。大小写不敏感；非法值退化成 `info` |
| `migrate-space` | `--to` | 必填 | 目标空间目录 |
| | `-config` | `config.ini` | 配置文件路径（读旧空间目录、并回写新路径） |
| | `-yes` | `false` | 跳过确认（不加时会要求输入 `yes` 回车） |
| `setpass` | `-user` | 必填 | 要重置密码的账号 |
| | `-password` | 空 | 不给则**随机生成 16 位并打印到控制台** |
| | `-config` | `config.ini` | 配置文件路径 |
| `adduser` | `-user` / `-password` | 必填 | 账号与密码 |
| | `-root` | 小写账号名 | 普通用户的存储空间标识 |
| | `-quota` | 运行期设置的 `defaultQuota` | 支持 `10GiB` 这类写法 |
| | `-rate` | 运行期设置的 `defaultRateLimitMbps` | Mbps；传负数则回落到默认值 |
| | `-admin` | `false` | 建为管理员（此时 `-root` 会被忽略，管理员没有存储空间） |
| | `-display` | 空 | 显示名 |
| | `-config` | `config.ini` | 配置文件路径 |

`migrate-space` 的额外保护：目标目录不能等于当前空间目录，也不能位于当前空间目录内部（否则直接报错）；目标目录里已有 `state.json` 时**只切换配置、不复制**；目标目录非空且没有 `state.json` 时报错"目标目录不是空目录，请换一个位置"。

不带子命令直接运行等于 `serve`。忘掉管理员密码时用 `homevault setpass -user admin`。

---

## 7. 相关文档

- [API.md](API.md) — 完整 HTTP 接口契约
- [DEPLOY.md](DEPLOY.md) — Docker / systemd / 反向代理部署
- [UPGRADE_BACKUP.md](UPGRADE_BACKUP.md) — 升级与备份恢复
