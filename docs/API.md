# HomeVault HTTP API 契约

> 本文以 **Go 后端实际实现** 为准（`internal/server/*.go`、`internal/core/*.go`）。
> 版本：`server.Version = "0.1.1"`（定义于 `internal/server/server.go`）。
> 所有接口都在 `/api/` 下；任何未列出的 `/api/...` 路径返回 `404 not_found`。

---

## 1. 约定

### 1.1 鉴权

登录成功后服务端下发 Cookie `homevault_session`（`HttpOnly`、`SameSite=Lax`、`Path=/`；启用内置 HTTPS 时自动带 `Secure`）。两种传法，二者取其一：

| 方式 | 写法 |
| --- | --- |
| Cookie | 浏览器自动携带 `Cookie: homevault_session=<token>` |
| 请求头 | `Authorization: Bearer <token>` |

会话是 **HMAC-SHA256 签名的无状态令牌**，令牌内容为 `用户名 + 密码版本号 + 过期时间`。要点：

- **不绑定 IP、不绑定 User-Agent**，换网络/换设备不会掉线。
- 修改密码后 `密码版本号` 变化，该用户**所有旧会话立即失效**，需要重新登录。
- 登录会话有效期由运行期设置 `sessionTTL` 决定（默认 `168h` = 7 天）；修改 `sessionTTL` 只影响之后登录产生的会话。
- 登录接口本身无需鉴权；`GET /api/healthz` 也无需鉴权。其余接口全部需要有效会话。

### 1.2 强制改凭据状态

首次启动创建的管理员带 `mustChangeCredentials = true`。该状态下，**除以下接口外所有接口返回 `403 must_change_credentials`**：

- `GET /api/healthz`
- `GET /api/auth/me`
- `POST /api/auth/logout`
- `POST /api/auth/change-credentials`

### 1.3 管理员代操作（`?user=<账号>`）

- 管理员**没有自己的存储空间**（内部 `user.root` 必须为空），不能直接操作"自己的"文件。
- 管理员在管理端选定一个普通用户后，前端之后的每个请求都会带上 `?user=<账号>`；不带 `?user=` 的文件类请求一律 `403 admin_no_storage`。
- 带 `?user=<账号>` 时，该请求实际以目标用户的身份与根目录执行，且 **目标用户的权限位被跳过**（管理员代办不看普通用户权限位）。
- 只有管理员能使用 `?user=`；普通用户带了会得到 `403 forbidden`（"只有管理员可以操作用户文件"）。
- 目标用户不存在 → `404 user_not_found`。
- 管理员每访问一次某用户的文件，都会写一条 `action=admin_access` 的审计记录。

### 1.4 二次密码（管理员代操作写操作时必需）

管理员通过 `?user=<账号>` 操作**他人**文件时，每一个写操作都必须重新输入**管理员自己的密码**：

- **不做任何时间窗缓存**，每次写操作都要带。
- 缺密码 → `428 reauth_required`。
- 密码错误 → `403 reauth_failed`，并计入登录限流（同账号 5 次失败 / 5 分钟窗口）。
- 尝试过多 → `429 too_many_attempts`，响应带 `Retry-After: <秒>`。
- 管理员**不带** `?user=` 时没有自己的存储空间，文件类接口一律 `403 admin_no_storage`（也就不会误碰别人的数据）。

两种传法（按优先级）：

| 场景 | 传法 |
| --- | --- |
| JSON 请求体 | body 里的 `"password"`（文件类写操作）或 `"adminPassword"`（用户管理/设置类写操作） |
| 原始体 / multipart（如 `PUT /api/upload/{id}/{index}`、`POST /api/fs/upload`） | 请求头 `X-LD-Password-B64` = 密码 UTF-8 字节的 **标准 base64**；或退化的明文头 `X-LD-Password` |

以下接口属于"每次都要二次密码"：

- 管理员代操作时的全部文件写操作：`fs/mkdir`、`fs/rename`、`fs/move`、`fs/copy`、`fs/delete`、`fs/upload`、`upload/init`、`PUT upload/{id}/{index}`、`upload/{id}/complete`、`DELETE upload/{id}`、`trash/restore`、`trash/delete`、`trash/empty`
- 用户管理：`POST/PATCH/DELETE /api/users...`
- 运行期设置：`PATCH /api/settings`

自己操作自己的文件**不需要**二次密码；普通用户调用上述接口也不会被要求（只有管理员才校验）。

### 1.5 错误响应

所有失败响应统一为：

```json
{ "error": "中文错误说明", "code": "英文错误代码" }
```

| HTTP | code | 含义 |
| --- | --- | --- |
| 400 | `bad_request` | 参数非法、路径非法、根目录不可操作、上传会话状态不符等 |
| 400 | `bad_json` | 请求体不是合法 JSON（含**未知字段**，`DisallowUnknownFields`） |
| 400 | `invalid_setting` | 运行期设置项校验失败（消息中指明字段） |
| 400 | `invalid_username` | 账号名不合法（需满足 `^[A-Za-z0-9_-]{3,32}$`） |
| 400 | `invalid_root` | 存储空间标识（`root`）不合法 |
| 400 | `root_conflict` | 该 `root` 与其它账号的存储根相同或互相嵌套 |
| 400 | `invalid_permission` | 权限位名称未知 |
| 400 | `admin_only_permission` | 试图把 `trash_view` / `trash_manage` / `users_manage` / `logs_view` 授给普通账号（管理员专属） |
| 400 | `admin_unique` | 试图创建第二个管理员，或把账号提升为管理员（系统只允许一个管理员） |
| 400 | `quota_budget` | 用户配额超过空间总容量，或所有账号配额之和会超过 `spaceQuota` |
| 400 | `invalid_user` | 用户记录校验失败 |
| 400 | `weak_password` | 密码少于 8 个字符或超过 200 个字符 |
| 400 | `same_password` | 新密码与当前密码相同 |
| 400 | `no_change` | 改凭据时没有任何需要修改的内容 |
| 400 | `self_lockout` | 试图取消或禁用当前登录的管理员自己 |
| 400 | `self_delete` | 试图删除当前登录的账号 |
| 400 | `last_admin` | 系统必须保留至少一个可用的管理员 |
| 400 | `migration_required` | 改了存储位置但目标没有 `state.json`（需先迁移） |
| 401 | `unauthorized` | 未登录或会话已过期 |
| 401 | `invalid_credentials` | 登录时账号或密码错误（不区分二者，避免账号枚举） |
| 403 | `forbidden` | 缺少权限位、路径越界，或普通用户试图使用 `?user=` |
| 403 | `admin_no_storage` | 管理员没有自己的存储空间（文件类接口未带 `?user=`） |
| 403 | `disabled` | 账号已被禁用 |
| 403 | `reauth_failed` | 二次密码不正确 |
| 403 | `must_change_credentials` | 尚未修改默认账号/密码 |
| 403 | `invalid_password` | 修改凭据时"当前密码"不正确 |
| 404 | `not_found` | 文件/目录/上传会话不存在，或访问了未知的 `/api/...` 路径 |
| 404 | `user_not_found` | `?user=` 指定的账号不存在 |
| 409 | `already_exists` | 同名条目已存在 / 账号已存在 |
| 409 | `office_preview_unavailable` | 未检测到 LibreOffice，无法服务端转换 Office |
| 411 | `length_required` | 分片上传缺少 `Content-Length` |
| 413 | `too_large` | 超过单文件上限 `maxUploadSize` |
| 428 | `reauth_required` | 该操作需要再次输入管理员密码 |
| 429 | `too_many_attempts` | 密码错误次数过多（登录限流） |
| 500 | `internal_error` | 服务端内部错误 |
| 507 | `quota_exceeded` | 超出该账号的配额 |
| 507 | `space_full` | 整个空间目录已达 `spaceQuota` 上限（所有账号共享） |

`admin_no_storage` 与 `forbidden` 都是 `403`：前者专指"管理员没有自己的存储空间，请在管理页面选择目标用户后再操作"（由 `core.ErrNoStorage` / `fsops.ErrNoRoot` 映射而来），后者用于权限不足、路径越界、普通用户误用 `?user=` 等。

### 1.6 数据格式

- 审计日志中的时间一律 **RFC3339（UTC）**，例如 `2026-02-14T10:00:00Z`。
- 路径一律是**相对用户根的 POSIX 风格相对路径**（`docs/2026/a.pdf`）；`""` 表示用户根目录自身；一律不接受绝对路径与 `..`（见 §10）。
- 目录列表项里的 `modTime` 为文件系统本地时区序列化的 RFC3339（Go `time.Time` 默认行为）。
- 容量字段单位都是**字节**，`0` 表示不限。

### 1.7 内置前端

`web/` 下的前端是**零构建**的原生 ES Module 实现，不依赖任何框架与 CDN：

- `web/index.html` → `web/app.js`（用户端，挂载 `web/ui.js` 里的 Web Components）；
- `web/admin.html` → `web/admin.js`（管理端，同样复用 `web/ui.js`）；
- 所有请求都经过 `ui.js` 的 `api()` / `apiGet()` 辅助函数，它负责拼 `?user=`、注入二次密码（JSON body 字段或 `X-LD-Password-B64` 头）、把非 2xx 响应转成带 `code` 的错误对象。

因此前端与本文档的契约由这两个辅助函数统一保证；如果自己写客户端，按本文档的字段表实现即可。

---

## 2. 健康检查与鉴权

### 2.1 `GET /api/healthz`（公开）

无需登录。注意：该接口在"必须改凭据"状态下**依然可用**。

响应 `200`：

```json
{
  "status": "ok",
  "version": "0.1.1",
  "time": "2026-02-14T10:00:00.123456Z"
}
```

### 2.2 `POST /api/auth/login`（公开）

请求字段：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `username` | string | 是 | 账号（会 `TrimSpace`；内部按小写匹配） |
| `password` | string | 是 | 密码 |

响应 `200`（同时 `Set-Cookie: homevault_session=...`）：

```json
{
  "user": {
    "username": "admin",
    "displayName": "系统管理员",
    "isAdmin": true,
    "quota": 0,
    "rateLimitMbps": 0,
    "disabled": false,
    "permissions": ["read","upload","modify","delete","trash_view","trash_manage","users_manage","logs_view"],
    "mustChangeCredentials": true,
    "createdAt": "2026-02-14T09:59:00Z",
    "updatedAt": "2026-02-14T09:59:00Z"
  },
  "home": "/admin/"
}
```

普通用户 `home` 为 `"/"`，管理员为 `"/admin/"`。

错误：

- `400 bad_request`：账号或密码为空。
- `401 invalid_credentials`：账号或密码错误。
- `403 disabled`：账号已被禁用。
- `429 too_many_attempts`：**同账号** 5 分钟内失败 ≥ 5 次，或**同一 IP** 上出现 8 个不同账号的失败（撞库特征）。响应带 `Retry-After: <秒>`，消息里也带剩余等待时间。
  注意：单个账号输错密码不会连坐同 IP 的其他人（避免 NAT/共享出口下一人锁全公司）。

说明：登录成功与失败都会写审计日志（`login` / `login_failed`）。成功登录会清空该账号的失败计数。

### 2.3 `POST /api/auth/logout`（需登录）

请求体可以为空。响应 `200`，并清除 Cookie：

```json
{ "ok": true }
```

写审计记录 `action=logout`。

### 2.4 `GET /api/auth/me`（需登录）

响应用户自身信息与环境信息：

```json
{
  "user": {
    "username": "alice",
    "displayName": "Alice",
    "isAdmin": false,
    "root": "alice",
    "quota": 10737418240,
    "rateLimitMbps": 0,
    "disabled": false,
    "permissions": ["read","upload","modify","delete","trash_view","trash_manage"],
    "mustChangeCredentials": false,
    "used": 1234567,
    "trashUsed": 4242,
    "createdAt": "2026-02-01T08:00:00Z",
    "updatedAt": "2026-02-10T08:00:00Z"
  },
  "home": "/",
  "officeAvailable": true,
  "sessionTTL": "168h",
  "spaceDir": "C:\\HomeVaultSpace",
  "version": "0.1.1",
  "permissionLabels": ["read","upload","modify","delete","trash_view","trash_manage","users_manage","logs_view"]
}
```

`used` / `trashUsed` 仅在用户 `root` 非空（即普通用户）时出现，单位字节。管理员 `root` 为空，不会带用量字段。

错误：`401 unauthorized`。

### 2.5 `POST /api/auth/change-credentials`（需登录）

普通用户与管理员都可以修改**自己的**账号名、密码、显示名，必须验证当前密码。这也是解除"必须改凭据"状态的唯一途径。

请求字段：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `currentPassword` | string | 是 | 当前密码 |
| `newUsername` | string | 否 | 新账号名，需满足 `^[A-Za-z0-9_-]{3,32}$`，且未被占用 |
| `newPassword` | string | 否 | 新密码，长度 8–200 字符；不能与当前密码相同 |
| `newDisplayName` | string | 否 | 新显示名（`TrimSpace` 后非空才生效） |

至少要有实际变化，否则 `400 no_change`（`mustChangeCredentials` 为真时例外，此时改凭据本身即视为变化）。改名时会同步接管该账号所有未完成的上传会话，并清理旧账号记录。

响应 `200`（会重新下发 Cookie，因为密码版本号可能已变）：

```json
{
  "user": { "username": "alice2", "displayName": "Alice", "isAdmin": false, "root": "alice",
            "quota": 10737418240, "rateLimitMbps": 0, "disabled": false,
            "permissions": ["read","upload","modify","delete","trash_view","trash_manage"],
            "mustChangeCredentials": false, "createdAt": "2026-02-01T08:00:00Z" },
  "home": "/"
}
```

错误：

- `400 bad_json`：请求体不是合法 JSON 或含未知字段。
- `403 invalid_password`：`currentPassword` 不正确（同时计入登录限流，多次失败 → `429 too_many_attempts`）。
- `400 invalid_username`：新账号名不合法。
- `400 weak_password`：新密码少于 8 个字符或超过 200 个字符。
- `400 same_password`：新密码与当前密码相同。
- `400 no_change`：没有任何需要修改的内容。
- `409 already_exists`：新账号名已被占用。
- `401 unauthorized`。

---

## 3. 文件

所有文件接口的路径参数都是**用户根内相对路径**。管理员使用 `?user=<账号>` 时，相对的是**目标用户**的根。

### 3.1 `GET /api/fs/list`（权限 `read`）

查询参数：

| 参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `path` | string | `""` | 目录相对路径，空表示用户根 |
| `user` | string | — | 管理员代操作目标账号 |

> 服务端**不解析** `sort` / `order` / `limit` / `offset`；排序与分页由前端本地完成，一次返回该目录全部条目。单目录条目上限 100000，超出会返回 `500 internal_error`（消息为"目录条目超过 100000，请使用分页或拆分目录"）。

响应 `200`：

```json
{
  "path": "docs",
  "parent": "",
  "entries": [
    { "name": "2026", "path": "docs/2026", "isDir": true, "size": 0,
      "modTime": "2026-02-14T10:00:00+08:00", "ext": "", "kind": "folder", "mime": "" },
    { "name": "a.pdf", "path": "docs/a.pdf", "isDir": false, "size": 12345,
      "modTime": "2026-02-14T10:00:00+08:00", "ext": "pdf", "kind": "pdf",
      "mime": "application/pdf" }
  ],
  "usage": { "used": 1234567, "trashUsed": 4242, "quota": 10737418240 }
}
```

- `kind` 取值：`folder` / `image` / `video` / `audio` / `pdf` / `text` / `office` / `archive` / `other`。
- `usage.quota = 0` 表示不限。
- **符号链接一律不展示**。

错误：`404 not_found`（目录不存在）、`400 bad_request`（路径是文件而非目录、路径非法）、`403 forbidden`（管理员未指定 `user=`）、`401 unauthorized`。

### 3.2 `GET /api/fs/download`（权限 `read`）

查询参数：

| 参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `path` | string | — | 文件相对路径，**不能为空，也不能是目录** |
| `inline` | `1` | 否 | `inline=1` → `Content-Disposition: inline`，供浏览器内联渲染 |
| `download` | `1` | 否 | 显式下载标记；会写进审计 `detail`，便于和预览区分 |
| `user` | string | — | 管理员代操作目标账号 |

响应：文件字节流。

- `Content-Type` 按扩展名推断（内置表，未知为 `application/octet-stream`）。
- `Content-Disposition: attachment; filename=...`（`inline=1` 时为 `inline`）。
- `Accept-Ranges: bytes`，**完整支持 HTTP `Range`**，因此音视频可以拖动进度条、下载可以断点续传。
- 该用户 `rateLimitMbps > 0` 时，下载方向按令牌桶限速（单位 Mbps）。

审计：**任何真实读取都会写一条 `action=download`**（带文件字节数与来源 IP/UA），
`detail` 区分 `attachment` / `inline` / `+download=1`。这样"谁看过这个文件"在日志里一定查得到；
代价是前端预览也会留痕（一次 Range 请求一条）。

错误：`400 bad_request`（`path` 为空 / 目标目录 / 路径非法）、`404 not_found`、`403 forbidden`、`401 unauthorized`。

### 3.3 `POST /api/fs/mkdir`（权限 `modify`）

请求字段：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `dir` | string | 是 | 父目录相对路径，`""` 表示用户根 |
| `name` | string | 是 | 新目录名；不能含 `/` `\`，不能是 `.` / `..`，长度 ≤ 255 字节 |
| `password` | string | 管理员代操作时 | 管理员密码（§1.4） |

响应 `200`：

```json
{ "path": "docs/2026" }
```

错误：`400 bad_json`、`400 bad_request`（名称非法）、`404 not_found`（父目录不存在）、`409 already_exists`、`428 reauth_required`、`403 reauth_failed`、`429 too_many_attempts`。

### 3.4 `POST /api/fs/rename`（权限 `modify`）

同目录内重命名。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `path` | string | 是 | 原路径，**不能是根目录** |
| `newName` | string | 是 | 新名称（仅名称，不含路径分隔符） |
| `password` | string | 管理员代操作时 | 管理员密码 |

响应 `200`：

```json
{ "path": "docs/b.pdf" }
```

若 `newName` 与原名称相同，直接返回原路径（幂等，不报错）。

错误：`400 bad_request`（根目录、名称非法）、`404 not_found`、`409 already_exists`、`428`/`403 reauth_failed`/`429`。

### 3.5 `POST /api/fs/move`（权限 `modify`）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `paths` | string[] | 是 | 源路径列表，1–500 项；不允许根目录 |
| `dest` | string | 是 | 目标目录（必须已存在） |
| `password` | string | 管理员代操作时 | 管理员密码 |

响应 `200`：

```json
{ "paths": ["archive/a.pdf", "archive/b.pdf"] }
```

规则与错误：

- 目标目录不存在 → `404 not_found`。
- 目标目录已有同名条目 → `409 already_exists`（移动**不覆盖**、也**不自动改名**）。
- 未选择任何条目 → `400 bad_request`。
- 超过 500 项 → `400 bad_request`。
- 会拒绝"把条目移动到它自身或其子目录"（见 §14 已知实现差异）。
- `428` / `403 reauth_failed` / `429`。

### 3.6 `POST /api/fs/copy`（权限 `modify`）

请求字段与 §3.5 完全相同。

响应 `200`：

```json
{ "paths": ["archive/a - 副本.pdf"] }
```

差异：

- 目标目录已有同名条目时**自动生成副本名**：`a - 副本.pdf` → `a - 副本 (1).pdf` → `a - 副本 (2).pdf` …（最多尝试 9999 次）。
- **复制前校验配额**：目录会递归统计大小；`used + trashUsed + 追加量 > quota` → `507 quota_exceeded`。

### 3.7 `POST /api/fs/delete`（权限 `delete`）

**移入回收站**，不是永久删除。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `paths` | string[] | 是 | 1–500 项；不允许根目录 |
| `password` | string | 管理员代操作时 | 管理员密码 |

响应 `200`：

```json
{ "trashIds": ["t_1a2b3c4d5e6f7a8b9c"], "count": 1 }
```

语义与错误：

- **不做配额校验**：文件只是从用户根挪进回收站，`used + trashUsed` 总额不变；配额已满时也必须能删（否则用户会被自己的数据卡死）。
- 每个条目在 `<space_dir>/trash/<id>/payload` 存放实体，写一条 `action=delete` 审计。
- 保留期取 `trashRetention`（默认 `720h` = 30 天）；为 `0` 时 `expiresAt` 为 `null`，表示永不自动清理。
- 超过 500 项或未选条目 → `400 bad_request`；路径不存在 → `404 not_found`；`428`/`403 reauth_failed`/`429`。

### 3.8 `POST /api/fs/upload?path=<目录>`（权限 `upload`）

小文件快路径，`multipart/form-data`。用于几 MB 级的文件与多选拖拽；大文件请用 §4 分片上传。

| 位置 | 名称 | 说明 |
| --- | --- | --- |
| query | `path` | 目标目录相对路径（缺省 = 用户根） |
| query | `user` | 管理员代操作目标账号 |
| form | `file` | 文件字段，**可重复**；其他字段名会被忽略 |
| header | `X-LD-Password-B64` | 管理员代操作时的密码（base64） |

响应 `200`：

```json
{
  "uploaded": [
    { "name": "a.pdf", "path": "docs/a.pdf", "size": 12345 }
  ]
}
```

规则与错误：

- 目标目录必须已存在，否则 `404 not_found`。
- 请求体不是 multipart → `400 bad_request`。
- 没有任何 `file` 字段 → `400 bad_request`（"没有收到任何文件"）。
- 单文件超过 `maxUploadSize`（非 0 时）→ `413 too_large`。
- 超出配额 → `507 quota_exceeded`（每个文件写入完成时校验）。
- 同名文件**不覆盖**，自动改名 `a.pdf` → `a (1).pdf` → `a (2).pdf`。
- 落盘方式：目标目录内临时文件 → `fsync` → 原子 `rename`。
- 每个成功文件写一条 `action=upload` 审计。

---

## 4. 分片上传全流程（断点续传）

默认分片大小 `chunkSize` = `8388608`（8 MiB），未完成会话默认 `24h` 后过期（`uploadSessionTTL`）。协议要点：

- `PUT` 同一个 `index` **幂等**，可重复上传覆盖。
- `init` 对**同账号 + 同目标目录 + 同文件名 + 同大小**且未过期的会话，**直接返回已有会话**（含 `received`），因此刷新页面后可以续传。
- 只有全部分片到齐才能 `complete`；失败时**保留会话**以便重试。

### 4.1 `POST /api/upload/init`（权限 `upload`）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `dir` | string | 是 | 目标目录（必须已存在） |
| `name` | string | 是 | 文件名（不含路径分隔符） |
| `size` | int64 | 是 | 文件总字节数，`>= 0` |
| `chunkSize` | int64 | 否 | 自定义分片大小；`0`/缺省用运行期设置。会被夹在 `[64KiB, size]` 区间内 |
| `password` | string | 管理员代操作时 | 管理员密码 |

响应 `200`：

```json
{
  "id": "u_6f1c2a9b3d4e5f6789ab",
  "dir": "videos",
  "name": "a.mp4",
  "size": 1073741824,
  "chunkSize": 8388608,
  "chunkCount": 128,
  "received": [],
  "bytesReceived": 0,
  "createdAt": "2026-02-14T10:00:00Z",
  "expiresAt": "2026-02-15T10:00:00Z"
}
```

校验时机与错误：

- `size < 0` → `400 bad_request`。
- `maxUploadSize > 0` 且 `size > maxUploadSize` → `413 too_large`。
- **新建会话时预检配额**：`已用（含未提交分片）+ 回收站 + size > quota` → `507 quota_exceeded`。
  命中同名/同大小的可续传会话时直接复用，不再重复预检（那批字节已经算在"已用"里）。
- 目标目录不存在 → `404 not_found`。
- `size = 0` 时 `chunkCount = 1`，`chunkSize` 被夹到 `size`（即 0）。

### 4.2 `PUT /api/upload/{id}/{index}`（权限 `upload`）

原始字节体（非 JSON）。`index` 从 `0` 开始。

| 位置 | 名称 | 说明 |
| --- | --- | --- |
| path | `{id}` | 会话 ID |
| path | `{index}` | 分片下标 |
| header | `Content-Length` | **必须**，且必须等于该分片的应有长度 |
| header | `X-Chunk-SHA256` | 可选，提供则校验分片的 SHA-256（hex，不区分大小写） |
| header | `X-LD-Password-B64` | 管理员代操作时的密码（base64） |

响应 `200`（返回更新后的会话视图）：

```json
{
  "id": "u_6f1c2a9b3d4e5f6789ab",
  "dir": "videos",
  "name": "a.mp4",
  "size": 1073741824,
  "chunkSize": 8388608,
  "chunkCount": 128,
  "received": [0, 1, 2],
  "bytesReceived": 25165824,
  "createdAt": "2026-02-14T10:00:00Z",
  "expiresAt": "2026-02-15T10:00:00Z"
}
```

分片长度规则：第 `index < chunkCount-1` 个分片必须正好 `chunkSize` 字节；最后一个分片是 `size - chunkSize*(chunkCount-1)` 字节。

错误：

- 缺 `Content-Length` 或为 chunked 传输 → `411 length_required`。
- `index` 不是整数或越界 → `400 bad_request`。
- 实际收到的字节数与应有长度不符 → `400 bad_request`。
- `X-Chunk-SHA256` 不匹配 → `400 bad_request`。
- 会话不存在 / 不属于该用户 → `404 not_found`。**管理员也必须带 `?user=<会话所属账号>`**：
  不带时没有目标存储，返回 `403 admin_no_storage`，不会误碰别人的会话。
- 会话已过期 → `400 bad_request`（"上传会话已过期"）。
- `428` / `403 reauth_failed` / `429`。

### 4.3 查询进度

`GET /api/upload/{id}`（权限 `upload`）→ `200`，返回 §4.2 同样的会话视图。用于刷新页面或断线重连后重建续传列表。

`GET /api/upload`（权限 `upload`）→ `200`：

```json
{
  "sessions": [
    { "id": "u_6f1c…", "dir": "videos", "name": "a.mp4", "size": 1073741824,
      "chunkSize": 8388608, "chunkCount": 128, "received": [0,1,2],
      "bytesReceived": 25165824,
      "createdAt": "2026-02-14T10:00:00Z", "expiresAt": "2026-02-15T10:00:00Z" }
  ]
}
```

- 普通用户只看到自己的会话。
- 管理员**不带** `?user=` 时看到**全部**会话，并额外带 `owner` 字段（账号名）；带 `?user=<账号>` 时只看该用户的会话。

### 4.4 `POST /api/upload/{id}/complete`（权限 `upload`）

请求体可选：

```json
{ "password": "管理员密码（仅管理员代操作时需要）" }
```

也可以整体省略请求体（`Content-Length: 0`）。

响应 `200`：

```json
{ "name": "a.mp4", "path": "videos/a.mp4", "size": 1073741824 }
```

处理顺序与校验时机：

1. 检查所有分片到齐；否则 `400 bad_request`（"仍有 N 个分片未上传"）。
2. 检查会话未过期；否则 `400 bad_request`。
3. **落盘前再校验一次配额**：这里用的是"根目录真实占用 + 回收站 + 最终大小"，
   因为待提交分片本来就已经计入用量，落盘只是把它们从 `uploads/` 搬到 `files/`，不能再重复算一遍 → `507 quota_exceeded`。
4. 目标目录若已不存在 → `404 not_found`。
5. 按序合并分片到目标目录内的临时文件 → 校验合并后总大小等于声明值 → `fsync` → 生成不冲突的最终名（同名自动 `a (1).mp4`）→ 原子 `rename` → 权限置为 `0640`。
6. 清理临时分片目录与会话记录，写 `action=upload` 审计（字节数为文件大小）。

失败时的行为：临时文件被清理，**会话与已上传分片全部保留**，可以重试 `complete`。

### 4.5 `DELETE /api/upload/{id}`（权限 `upload`）

放弃上传并清理临时分片。请求体可选 `{ "password": "..." }`。

响应 `200`：

```json
{ "ok": true }
```

### 4.6 完整 6 步示例

```bash
# 1) 登录，保存 Cookie
curl -s -c cookies.txt -X POST http://127.0.0.1:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"alice-pass-2026"}'

FILE=/tmp/big.bin          # 假设 20 MiB
SIZE=$(stat -c %s "$FILE")

# 2) 初始化会话（服务端返回 chunkSize=8388608，故 chunkCount=3）
curl -s -b cookies.txt -X POST http://127.0.0.1:8080/api/upload/init \
  -H 'Content-Type: application/json' \
  -d "{\"dir\":\"videos\",\"name\":\"big.bin\",\"size\":$SIZE}"
# => {"id":"u_6f1c...","chunkSize":8388608,"chunkCount":3,"received":[],...}
ID=u_6f1c2a9b3d4e5f6789ab

# 3) 上传第 0 个分片（必须有 Content-Length，curl --data-binary @file 会自动带上）
dd if="$FILE" of=/tmp/c0 bs=8388608 skip=0 count=1 2>/dev/null
curl -s -b cookies.txt -X PUT --data-binary @/tmp/c0 \
  -H 'Content-Type: application/octet-stream' \
  "http://127.0.0.1:8080/api/upload/$ID/0"
# => {"received":[0],...}

# 4) 再上传第 1、2 个分片（第 2 个是最后一片，长度不足 8MiB）
dd if="$FILE" of=/tmp/c1 bs=8388608 skip=1 count=1 2>/dev/null
curl -s -b cookies.txt -X PUT --data-binary @/tmp/c1 "http://127.0.0.1:8080/api/upload/$ID/1"
dd if="$FILE" of=/tmp/c2 bs=8388608 skip=2 count=1 2>/dev/null
curl -s -b cookies.txt -X PUT --data-binary @/tmp/c2 "http://127.0.0.1:8080/api/upload/$ID/2"

# 5) 查询进度（断线重连后先查这个，再补 missing 的分片）
curl -s -b cookies.txt "http://127.0.0.1:8080/api/upload/$ID"
# => {"received":[0,1,2],"bytesReceived":20971520,...}

# 6) 合并落盘
curl -s -b cookies.txt -X POST "http://127.0.0.1:8080/api/upload/$ID/complete"
# => {"name":"big.bin","path":"videos/big.bin","size":20971520}
```

重复上传同一个分片（例如第 3 步重复执行）不会报错，服务端直接覆盖并返回相同的 `received`。

---

## 5. 搜索与预览

### 5.1 `GET /api/search`（权限 `read`）

查询参数：

| 参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `q` | string | `""` | 文件名子串，不区分大小写；空表示不按名称过滤 |
| `path` | string | `""` | 限定子树（相对路径）；必须是已存在的目录 |
| `ext` | string | — | 扩展名过滤，逗号分隔，可带点（`pdf,docx`）；小写比较 |
| `kind` | string | — | 类型过滤，逗号分隔：`image` `video` `audio` `pdf` `text` `office` `archive` `other` |
| `minSize` | int64 | `0` | 大小下界（含），字节 |
| `maxSize` | int64 | `0` | 大小上界（含），字节；`0` 表示不限 |
| `from` | string | — | 修改时间下界，接受 `RFC3339`、`2006-01-02 15:04:05`、`2006-01-02` |
| `to` | string | — | 修改时间上界，同上 |
| `includeDirs` | `1` | 否 | `includeDirs=1` 时把目录也作为结果返回 |
| `limit` | int | `100` | 命中上限，最大 `500` |
| `offset` | int | `0` | 跳过前 N 个命中 |
| `user` | string | — | 管理员代操作目标账号 |

响应 `200`（含聚合统计）：

```json
{
  "hits": [
    { "path": "docs/2026/report.pdf", "name": "report.pdf", "isDir": false,
      "size": 524288, "modTime": "2026-02-10T09:30:00+08:00", "ext": "pdf", "kind": "pdf" }
  ],
  "total": 137,
  "scanned": 4821,
  "truncated": false,
  "byKind": [
    { "key": "image", "count": 80, "bytes": 125829120 },
    { "key": "pdf", "count": 40, "bytes": 41943040 },
    { "key": "text", "count": 17, "bytes": 88064 }
  ],
  "byExt": [
    { "key": "png", "count": 80, "bytes": 125829120 },
    { "key": "pdf", "count": 40, "bytes": 41943040 }
  ],
  "bySize": [
    { "key": "< 1 MiB", "count": 120, "bytes": 31457280 },
    { "key": "1–10 MiB", "count": 15, "bytes": 78643200 },
    { "key": "10–100 MiB", "count": 2, "bytes": 62914560 },
    { "key": "100 MiB–1 GiB", "count": 0, "bytes": 0 },
    { "key": "> 1 GiB", "count": 0, "bytes": 0 }
  ],
  "byDay": [
    { "key": "2026-02-10", "count": 30, "bytes": 10485760 },
    { "key": "2026-02-09", "count": 12, "bytes": 3145728 }
  ]
}
```

聚合与上限的实现细节：

- 单次搜索最多扫描 **300000** 个条目；超出后置 `truncated = true` 并停止扫描。
- 单次最多返回 **500** 条命中（`limit` 会被夹到 500）。
- `total` 是**全部命中数**（不受 `limit` 影响），`scanned` 是实际扫描条目数。
- 聚合统计覆盖**所有命中项**，不只是返回的那一页。
- `byKind` 全量返回；`byExt` 最多 15 项；`bySize` 固定 5 个区间、**永远返回全部键**（无命中的键 `count=0`、`bytes=0`）；`byDay` 最多 30 天、按日期倒序。
- 排序规则：类型/扩展名/日期按命中数降序（同数按 key 升序），大小区间按固定顺序。
- 符号链接不参与搜索；单个不可读条目不会中断整个搜索。
- 每次调用写一条 `action=search` 审计（`target` 为查询串）。

错误：`400 bad_request`（`from`/`to` 格式不正确、路径非法）、`404 not_found`（`path` 指定了不存在的目录）、`401 unauthorized`、`403 forbidden`。

### 5.2 `GET /api/preview`（权限 `read`）

回答"这个文件该怎么预览"，不返回文件内容。

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `path` | string | 是 | 文件相对路径 |
| `user` | string | — | 管理员代操作目标账号 |

响应 `200`：

```json
{
  "kind": "office",
  "mode": "convert",
  "mimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  "size": 24576,
  "ext": "docx",
  "builtin": false
}
```

字段含义：

| 字段 | 取值 | 说明 |
| --- | --- | --- |
| `kind` | `image` `video` `audio` `pdf` `text` `office` `other` | 按扩展名归类 |
| `mode` | `inline` | 浏览器直接内联渲染（image / video / audio / pdf / text） |
| | `convert` | 需要服务端转 PDF（office） |
| | `download` | 只能下载（`other`、压缩包等） |
| `builtin` | bool | 为 `true` 表示**不要走服务端转换**：要么没检测到 LibreOffice，要么设置里 `officePreview = "builtin"`。前端应退化为内置渲染/提示下载（此时 `mode` 仍是 `convert`） |

错误：`404 not_found`、`400 bad_request`（目录不支持预览、路径非法）。

### 5.3 `GET /api/preview/office`（权限 `read`）

把 Word / Excel / PPT / ODF / RTF 转成 PDF 并内联返回。

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `path` | string | 是 | 文件相对路径 |
| `user` | string | — | 管理员代操作目标账号 |

响应 `200`：`Content-Type: application/pdf`、`Content-Disposition: inline`，支持 `Range`。

未检测到 LibreOffice 时返回 `409`，该响应体多带一个 `builtin` 字段：

```json
{
  "error": "服务端未安装 LibreOffice，无法转换该格式",
  "code": "office_preview_unavailable",
  "builtin": true
}
```

实现细节：

- LibreOffice 可执行文件查找顺序：环境变量 `HOMEVAULT_SOFFICE` → `soffice` / `libreoffice` / `soffice.exe` / `libreoffice.exe`（`exec.LookPath`）→ Windows 下的 `C:\Program Files\LibreOffice\program\soffice.exe` 与 `C:\Program Files (x86)\...`。
- 探测结果缓存 60 秒；运行期设置 `officePreview = "off"` 时**直接视为不可用**（`auto` 与 `builtin` 都只是探测开关的语义位）。
- 转换结果缓存在 `<space_dir>/cache/office/<sha256前32位>.pdf`，缓存键 = `root|相对路径|文件大小|mtime(纳秒)`，文件一改就会重新转换。
- 同时最多 2 个转换任务；同一文件的并发请求会合并为一次转换；单次转换超时 120 秒。
- 缓存中超过 24 小时的文件由后台每小时清理一次。
- 每次成功转换写一条 `action=preview` 审计（`message = "office→pdf"`）。

错误：`409 office_preview_unavailable`、`404 not_found`、`400 bad_request`、`401`、`403`。

> 明确不做的事：**不做在线编辑**，**不做文件版本历史**。

---

## 6. 回收站

### 6.1 `GET /api/trash`（权限 `trash_view`）

| 参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `limit` | int | `100` | 每页条数，最大 `500`（`<=0` 或 `>500` 都会回落到 100） |
| `offset` | int | `0` | 跳过条数 |
| `user` | string | — | 管理员代操作目标账号 |

响应 `200`：

```json
{
  "items": [
    { "id": "t_1a2b3c4d5e6f7a8b9c", "name": "old.pdf", "originalPath": "docs/old.pdf",
      "root": "alice", "size": 12345, "isDir": false, "deletedBy": "alice",
      "deletedAt": "2026-02-14T10:00:00Z", "expiresAt": "2026-03-16T10:00:00Z" }
  ],
  "total": 1,
  "totalBytes": 12345
}
```

- `total` 与 `totalBytes` 是**可见条目的全量统计**，不受 `limit`/`offset` 影响。
- `expiresAt` 缺失（`null`）表示永不自动清理（`trashRetention = 0`）。
- 普通用户只看到自己 `root` 下的条目；管理员看到**全部**条目。
- 后台**每 15 分钟**清理一次过期条目，并写一条聚合的 `action=cleanup` 审计。

错误：`403 forbidden`（缺少 `trash_view`）、`401 unauthorized`。

### 6.2 `POST /api/trash/restore`（权限 `trash_manage`）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `ids` | string[] | 是 | 回收站条目 ID |
| `password` | string | 管理员代操作时 | 管理员密码 |

响应 `200`（部分成功是正常结果，不会整体报错）：

```json
{
  "restored": ["docs/old.pdf"],
  "failed": [
    { "id": "t_9f3c...", "error": "原位置已有同名条目" },
    { "id": "t_0000...", "error": "记录不存在" },
    { "id": "t_1111...", "error": "无权操作该回收站条目" }
  ]
}
```

单项失败原因包括：记录不存在、无权操作、原位置已有同名条目、路径解析失败。

> 恢复**不做额外配额校验**（条目大小本来就算在回收站占用里）；真正做配额校验的是上传、复制与删除。

错误：`400 bad_json`、`428 reauth_required`、`403 reauth_failed`、`429`、`401`。

### 6.3 `POST /api/trash/delete`（权限 `trash_manage`）

永久删除，**不可恢复**。请求同 `ids` + `password`。

响应 `200`：

```json
{ "deleted": ["t_1a2b3c4d5e6f7a8b9c"] }
```

错误：`404 not_found`（某个 ID 不存在；此时已删除的不会回滚）、`403 forbidden`（无权操作该条目）、`428`、`403 reauth_failed`、`429`。

### 6.4 `POST /api/trash/empty`（权限 `trash_manage`）

清空**调用者可见的**全部回收站条目（普通用户 = 自己 `root` 的；管理员 = 全部）。请求体可省略，或 `{ "password": "..." }`。

响应 `200`：

```json
{ "count": 12, "bytes": 987654 }
```

写一条 `action=trash_empty` 审计。

错误：`428 reauth_required`、`403 reauth_failed`、`429`、`401`、`500 internal_error`。

---

## 7. 审计日志

### `GET /api/logs`（权限 `logs_view`）

| 参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `limit` | int | `100` | 条数，服务端夹到 `[1, 500]`；`<=0` 时用 50 |
| `offset` | int | `0` | 跳过条数 |
| `user` | string | — | **管理员**：按账号精确匹配（不区分大小写），空表示全部。**普通用户**：忽略该参数，强制只看自己 |
| `action` | string | — | 按动作精确匹配 |
| `search` | string | — | 对 `target` 与 `message` 做不区分大小写的子串匹配 |
| `from` | string | — | 时间下界，`RFC3339` / `2006-01-02 15:04:05` / `2006-01-02` |
| `to` | string | — | 时间上界，同上 |
| `format` | `csv` | — | `format=csv` 时导出 CSV |

响应 `200`（JSON 模式）：

```json
{
  "entries": [
    { "ts": "2026-02-14T10:00:00Z", "user": "admin", "actingAs": "alice",
      "ip": "10.0.0.5", "ua": "Mozilla/5.0 …", "action": "upload",
      "target": "docs/a.pdf", "status": "ok", "bytes": 12345, "message": "" }
  ],
  "total": 1234,
  "limit": 100,
  "offset": 0
}
```

- `entries` 按时间**倒序**（最新在前），跨多个日志文件合并后依然保持倒序。
- 日志文件按天命名：`audit-YYYY-MM-DD.jsonl`（一行一个 JSON 对象）。
- 单行损坏会被跳过，不会让整个日志页报错。
- `actingAs` 仅在管理员代操作时出现，值为被操作的账号。
- 保留期 `logRetention` 默认 `2160h`（90 天），后台每小时清理一次；清理时按"文件日期的次日仍早于截止时间"判断，不会删掉当天仍在写入的文件。

CSV 模式（`format=csv`）响应头：

```
Content-Type: text/csv; charset=utf-8
Content-Disposition: attachment; filename="homevault-logs.csv"
```

CSV 内容带 **UTF-8 BOM**（便于 Excel 正确显示中文），列顺序：

```
时间,用户,动作,目标,状态,字节,IP,客户端,说明
2026-02-14 18:00:00,alice,upload,docs/a.pdf,ok,12345,10.0.0.5,Mozilla/5.0 …,
```

CSV 中的时间为**服务器本地时区** `2006-01-02 15:04:05`；JSON 中的 `ts` 为 UTC RFC3339。CSV 导出同样受 `limit`/`offset` 影响。

动作枚举（`action`）：

| 值 | 含义 |
| --- | --- |
| `login` / `login_failed` / `logout` | 登录、登录失败、登出 |
| `upload` / `download` | 上传、下载 |
| `mkdir` / `rename` / `move` / `copy` / `delete` | 目录与文件操作 |
| `restore` / `purge` / `trash_empty` | 回收站恢复、永久删除、清空 |
| `user_create` / `user_update` / `user_delete` | 账号管理（含用户自行改凭据） |
| `settings_set` | 运行期设置被修改 |
| `config_reload` | 保留的动作名（当前代码中的常量，未在业务路径使用） |
| `cleanup` | 后台清理（回收站过期、日志过期等） |
| `search` / `preview` | 搜索、预览 |
| `reauth` | 二次密码校验失败 |
| `admin_access` | 管理员访问/代操作他人文件；也包括"缺少权限被拒绝" |

`status`：`ok` | `denied` | `error`。

补充：

- `download` 覆盖**所有真实读取**（普通下载、`inline=1` 预览、Range 请求），`detail` 为
  `attachment` / `inline` / `attachment +download=1` 等，配合 `bytes` 与来源 `ip`/`ua` 可以还原"谁在什么时候读了哪个文件"。
- `actingAs` 表示**被操作的目标账号**：管理员对 `alice` 的文件执行 `mkdir` 时，
  `user=admin`、`actingAs=alice`、`target=<路径>`。

错误：`400 bad_request`（`from`/`to` 格式不正确）、`403 forbidden`（缺少 `logs_view`）、`401`。

---

## 8. 用户管理

所有用户管理接口（`/api/users*`）与运行期设置接口（`/api/settings*`）、磁盘列表（`/api/system/disks`）
**只允许管理员账号**：非管理员一律 `403 forbidden`，即使他拥有 `users_manage` 权限位。
管理员每次调用写操作都要二次密码（`adminPassword`），**没有任何免密时间窗**。

> 为什么不再让 `users_manage` 单独放行：否则一个被授予该权限的普通用户可以自己创建一个
> `isAdmin: true` 的账号，等于直接提权。`users_manage` 现在只影响管理端的界面展示。

### 8.1 `GET /api/users`

响应 `200`：

```json
{
  "users": [
    { "username": "admin", "displayName": "系统管理员", "isAdmin": true,
      "quota": 0, "rateLimitMbps": 0, "disabled": false,
      "permissions": ["read","upload","modify","delete","trash_view","trash_manage","users_manage","logs_view"],
      "mustChangeCredentials": false,
      "createdAt": "2026-02-01T08:00:00Z", "updatedAt": "2026-02-01T08:00:00Z" },
    { "username": "alice", "displayName": "Alice", "isAdmin": false, "root": "alice",
      "quota": 10737418240, "rateLimitMbps": 50, "disabled": false,
      "permissions": ["read","upload","modify","delete","trash_view","trash_manage"],
      "mustChangeCredentials": false, "used": 1234567, "trashUsed": 4242,
      "createdAt": "2026-02-02T08:00:00Z", "updatedAt": "2026-02-10T08:00:00Z" }
  ],
  "permissionNames": ["read","upload","modify","delete","trash_view","trash_manage","users_manage","logs_view"],
  "defaults": { "quota": 10737418240, "rateLimitMbps": 0 }
}
```

- `users` 按账号名升序；`used` / `trashUsed` 会对每个普通用户遍历其根目录（管理员不出现这两个字段）。
- `defaults` 来自运行期设置 `defaultQuota` / `defaultRateLimitMbps`。

### 8.2 `POST /api/users`

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `username` | string | 是 | `^[A-Za-z0-9_-]{3,32}$` |
| `password` | string | 是 | 8–200 字符 |
| `displayName` | string | 否 | 显示名 |
| `root` | string | 普通用户否 | 存储空间标识，可多级（`shared/team`）；省略时等于**小写账号名** |
| `quota` | int64 | 否 | 字节数；省略时用 `defaultQuota`。`0` = 不限 |
| `rateLimitMbps` | int | 否 | 双向限速 Mbps；省略时用 `defaultRateLimitMbps`。`0` = 不限 |
| `isAdmin` | bool | 否 | 创建为管理员 |
| `permissions` | string[] | 否 | 权限位；省略时给普通用户默认六项（`read` `upload` `modify` `delete` `trash_view` `trash_manage`） |
| `adminPassword` | string | 是 | 管理员当前密码 |

响应 `200`：

```json
{
  "user": { "username": "bob", "displayName": "Bob", "isAdmin": false, "root": "bob",
            "quota": 10737418240, "rateLimitMbps": 0, "disabled": false,
            "permissions": ["read","upload","modify","delete","trash_view","trash_manage"],
            "mustChangeCredentials": false, "createdAt": "2026-02-14T10:00:00Z" }
}
```

规则与错误：

- **管理员没有存储空间**：`isAdmin=true` 时服务端强制 `root=""`、`quota=0`、`rateLimitMbps=0`，忽略传入值。
- **普通用户必须有存储空间**：`root` 省略时用账号名；`root` 非法 → `400 invalid_root`。
- 账号已存在 → `409 already_exists`；账号名非法 → `400 invalid_username`；密码太弱 → `400 weak_password`；未知权限位 → `400 invalid_permission`；用户记录校验失败 → `400 invalid_user`。
- `428 reauth_required`（没给 `adminPassword`）、`403 reauth_failed`、`429 too_many_attempts`。
- 创建成功后会创建其存储空间目录（`0750`），并写 `action=user_create` 审计。
- **存储根必须唯一且不能互相嵌套**：与其它账号的 `root` 相同、或一个落在另一个里面（大小写不敏感，`\` 与 `/` 等价），
  一律 `400 root_conflict`。这样任何两个账号都不会共享同一份物理目录，也不会出现"我的根是别人的子目录"。

### 8.3 `PATCH /api/users/{username}`

字段全部**可选**，给谁改谁：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `displayName` | string | 显示名（`TrimSpace`） |
| `newPassword` | string | 新密码（非空才生效，8–200 字符）；**会立即让该用户所有会话失效** |
| `root` | string | 存储空间标识；改成 `""` 会被 `400 invalid_root` 拒绝 |
| `quota` | int64 | 配额字节数，`0` = 不限 |
| `rateLimitMbps` | int | 双向限速 Mbps，`0` = 不限 |
| `disabled` | bool | 是否禁用（禁用后无法登录，且视为无任何权限） |
| `isAdmin` | bool | 是否管理员。置为 `true` 时同时清空 `root`/`quota`/`rateLimitMbps` |
| `permissions` | string[] | 覆盖式设置权限位（传 `[]` 即清空） |
| `adminPassword` | string | 管理员当前密码（必填） |

响应 `200`：

```json
{
  "user": { "username": "alice", "displayName": "Alice", "isAdmin": false, "root": "alice",
            "quota": 21474836480, "rateLimitMbps": 100, "disabled": false,
            "permissions": ["read","upload","modify","delete","trash_view","trash_manage"],
            "mustChangeCredentials": false, "used": 1234567, "trashUsed": 4242,
            "createdAt": "2026-02-02T08:00:00Z", "updatedAt": "2026-02-14T10:00:00Z" }
}
```

保护规则（都会返回 `400`）：

| code | 触发条件 |
| --- | --- |
| `self_lockout` | 试图取消或禁用**当前登录的管理员自己** |
| `last_admin` | 试图移除/禁用管理员，而系统中**可用的管理员只剩 1 个** |

其他错误：`404 user_not_found`、`400 weak_password`、`400 invalid_root`、`400 invalid_permission`、`400 invalid_user`、`428 reauth_required`、`403 reauth_failed`、`429`。

### 8.4 `DELETE /api/users/{username}?deleteFiles=1`

| 参数 | 说明 |
| --- | --- |
| `deleteFiles` | `1` 时**同时删除该用户的文件与回收站内容**；缺省只删账号记录 |
| 二次密码 | 通过头 `X-LD-Password-B64`（或 `X-LD-Password`）传管理员密码 —— **该接口不读请求体** |

响应 `200`：

```json
{ "ok": true }
```

行为：

- 缺省（不删文件）：账号记录被删除，**文件保留在磁盘上**；因为没有账号指向该 `root`，会变成孤儿数据，需要管理员手动处理。
- `deleteFiles=1`：递归删除其 `root` 目录，并清理该 `root` 下的全部回收站条目。
- 无论如何都会清理该账号未完成的上传会话（记录 + 临时分片）。
- 保护规则：不能删除当前登录账号（`400 self_delete`）；管理员只剩一个时不能删（`400 last_admin`）。
- 写 `action=user_delete` 审计（`message` 说明是否同时删除了文件）。

错误：`404 user_not_found`、`400 self_delete`、`400 last_admin`、`428 reauth_required`、`403 reauth_failed`、`429`。

---

## 9. 系统与设置

### 9.1 `GET /api/settings`（仅管理员）

响应 `200`：

```json
{
  "settings": {
    "spaceDir": "",
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
    "officePreview": "auto"
  },
  "restartRequired": [],
  "effective": {
    "configFile": "C:\\homevault\\config.ini",
    "listen": "0.0.0.0:8080",
    "spaceDir": "C:\\HomeVaultSpace",
    "defaultSpaceDir": "C:\\HomeVaultSpace",
    "tlsEnabled": false,
    "officeAvailable": true,
    "version": "0.1.1",
    "permissionNames": ["read","upload","modify","delete","trash_view","trash_manage","users_manage","logs_view"]
  }
}
```

- `settings` 是持久化在 `<space_dir>/state.json` 里的运行期设置。
- `settings.spaceDir` **为空表示沿用 config.ini / 系统默认**；非空表示"已被写入 config.ini 但尚未重启生效"的新位置。
- `restartRequired` 在 `settings.spaceDir` 非空且与当前生效值不同时包含 `"spaceDir"`。
- `effective` 是**当前进程真正生效**的引导配置（来自 config.ini），只读。

### 9.2 `PATCH /api/settings`（仅管理员，每次都要二次密码）

请求体是 `settings` 的任意子集，额外的 `adminPassword` 用于二次校验。**未知字段会被拒绝（`400 bad_json`）**。

| 字段 | 类型 | 默认 | 作用 | 是否需要重启 |
| --- | --- | --- | --- | --- |
| `spaceDir` | string | `""` | 存储位置（绝对路径），写进 config.ini 并提示重启 | **是**（`restartRequired: ["spaceDir"]`） |
| `spaceQuota` | 字节数或 `"5GiB"` | `5368709120`（5 GiB） | 整个空间目录的容量上限（所有用户 + 回收站 + 未提交分片）；`0` = 不限。所有账号配额之和不能超过它 | 否 |
| `defaultQuota` | 字节数或 `"1GiB"` | `1073741824`（1 GiB） | 新建用户的默认配额；不能超过 `spaceQuota` | 否（只影响之后新建的用户） |
| `defaultRateLimitMbps` | int | `0` | 新建用户的默认双向限速 Mbps；`0` = 不限 | 否 |
| `trashRetention` | 时长串 | `"720h"` | 回收站保留期；`"0"` = 永不自动清理 | 否（改后**只影响之后删除的文件**，不追溯已存在条目） |
| `logRetention` | 时长串 | `"2160h"` | 审计日志保留期；`"0"` = 不清理 | 否 |
| `uploadSessionTTL` | 时长串（必须 > 0） | `"24h"` | 未完成分片上传会话的过期时间 | 否 |
| `maxUploadSize` | 字节数或 `"10GiB"` | `0` | 单文件上限；`0` = 不限（仅受配额约束）。必须 `>= chunkSize` | 否 |
| `chunkSize` | 字节数 | `8388608`（8 MiB） | 分片大小，范围 `[64KiB, 1GiB]` | 否（进行中的会话沿用创建时的值） |
| `sessionTTL` | 时长串（必须 > 0） | `"168h"` | 登录会话有效期 | 否（只影响之后登录产生的会话） |
| `trustProxy` | bool | `false` | 是否采信 `X-Forwarded-For` / `X-Real-IP` 作为客户端 IP | 否 |
| `accessLogDisabled` | bool | `false` | 是否关闭 HTTP 访问日志（审计日志不受影响） | 否 |
| `officePreview` | `auto` / `off` / `builtin` | `"auto"` | `off` 时**不使用** LibreOffice（Office 预览退化为下载提示） | 否（探测结果 60 秒内刷新） |

容量值与时长值的写法：

- 容量：字节数字（`10737418240`）或字符串 `"10GiB"` / `"500MB"` / `"0"` / `"unlimited"`。单位大小写不敏感，`KB` 与 `KiB` 都按 1024 计算。**不接受空字符串**（"不限请显式写 0"）。
- 时长：`"720h"` / `"30d"` / `"2w"` / `"90m"` / `"0"`，也支持 Go 标准时长串组合（如 `"1h30m"`）。

响应 `200`：

```json
{
  "settings": { "spaceDir": "", "defaultQuota": 21474836480, "defaultRateLimitMbps": 100,
                "trashRetention": "30d", "logRetention": "2160h", "uploadSessionTTL": "24h",
                "maxUploadSize": 0, "chunkSize": 8388608, "sessionTTL": "168h",
                "trustProxy": true, "accessLogDisabled": false, "officePreview": "auto" },
  "restartRequired": []
}
```

规则与错误：

- `spaceDir` 的额外校验：必须是绝对路径（否则 `400 invalid_setting`）；目标位置必须可写（创建并删除一个探针文件）；**如果目标里没有 `state.json` 且当前空间里已有数据（存在 `state.json`）**，返回 `400 migration_required`，提示先执行 `homevault migrate-space --to "<目录>"`，避免"切过去就丢数据"。
- 校验失败 → `400 invalid_setting`（消息里指明字段与原因，例如 `chunkSize 至少 64KiB`、`maxUploadSize (…) 不能小于 chunkSize (…)`）。
- 每次成功修改会重置限速器状态，并写 `action=settings_set` 审计。
- `428 reauth_required` / `403 reauth_failed` / `429 too_many_attempts`。

### 9.3 `GET /api/system`

响应 `200`（普通用户）：

```json
{
  "version": "0.1.1",
  "uptimeSeconds": 11532,
  "spaceDir": "/var/lib/homevault/HomeVaultSpace",
  "diskTotal": 107374182400,
  "diskFree": 85899345920,
  "officeAvailable": true,
  "isAdmin": false
}
```

管理员额外字段：

```json
{
  "version": "0.1.1",
  "uptimeSeconds": 11532,
  "spaceDir": "/var/lib/homevault/HomeVaultSpace",
  "diskTotal": 107374182400,
  "diskFree": 85899345920,
  "officeAvailable": true,
  "isAdmin": true,
  "userCount": 4,
  "adminCount": 1,
  "trashCount": 3,
  "uploadSessionCount": 1,
  "configFile": "/etc/homevault/config.ini",
  "tlsEnabled": false
}
```

- `diskTotal` / `diskFree` 来自空间目录所在卷（Windows 用 `GetDiskFreeSpaceEx`，Linux 用 `statfs`）；取不到时为 `0`。
- 该接口只要登录即可访问（没有权限位要求）。

### 9.4 `GET /api/system/disks`（仅管理员）

响应 `200`：

```json
{
  "disks": [
    { "path": "C:\\", "total": 107374182400, "free": 85899345920, "remarks": "本地磁盘" },
    { "path": "D:\\", "total": 214748364800, "free": 200000000000, "remarks": "本地磁盘" }
  ],
  "currentSpaceDir": "C:\\HomeVaultSpace",
  "defaultSpaceDir": "C:\\HomeVaultSpace"
}
```

- Windows：枚举盘符，`remarks` 为 `可移动磁盘` / `本地磁盘` / `网络驱动器` / `光驱`（不带 `fsType`）。
- Linux：读 `/proc/mounts`，只列出设备以 `/dev/` 开头的可写文件系统（排除 `proc` / `sysfs` / `tmpfs` / `overlay` / `squashfs` 等），带 `fsType`；一个都没有时回落到 `/`。
- 磁盘枚举失败 → `500 internal_error`。

---

## 10. 路径与权限规则

### 10.1 路径规则

1. 用户根 = `<space_dir>/files/<user.root>`；`root` 由管理员指定，可多级（`shared/team`）。
2. 所有路径参数先做 **"以 `/` 为根 Clean"**：`CleanRel` 会把 `\` 统一成 `/`、把 `a/b/../../c` 归一化为 `c`，因此 `..` **无法**逃出根目录；`""` / `"."` / `"/"` 都归一化为根目录自身。
3. **不允许绝对路径**（以 `/` 开头直接拒绝）；不允许 NUL 字节；长度上限 **4096 字节**。
4. 强制"拒绝符号链接"：从存储根开始**逐段 `Lstat`**，任一段是符号链接即拒绝（`403 forbidden`）；列目录时符号链接**不展示**、搜索时**不进入**、复制时**拒绝复制**。
5. 文件名（`name` / `newName`）：不含 `/` 与 `\`、不含 NUL、不等于 `.` / `..`、长度 ≤ 255 字节。
6. **根目录不可操作**：不能重命名根、不能删除根、不能把根作为移动/复制源、不能下载根、不能预览根。
7. 单次批量操作上限 **500 项**；单目录条目上限 **100000 条**（超出报错，需要拆分目录）。
8. 所有写文件都是**目标目录内的临时文件 + `fsync` + 原子 `rename`**，不会出现半个文件。

### 10.2 权限位与管理员

| 权限位 | 含义 |
| --- | --- |
| `read` | 列目录、下载、搜索、预览 |
| `upload` | 简单上传、分片上传（init / chunk / complete / status / cancel） |
| `modify` | 新建目录、重命名、移动、复制 |
| `delete` | 移入回收站 |
| `trash_view` | 查看回收站列表 |
| `trash_manage` | 恢复、永久删除、清空回收站 |
| `users_manage` | 管理端界面入口（**不再单独授予后端权限**：`/api/users*`、`/api/settings*`、`/api/system/disks` 只允许管理员账号） |
| `logs_view` | 查看/导出审计日志 |

- **管理员隐含拥有全部权限**（`Can()` 恒为真），不受 `permissions` 字段影响。
- **账号与设置类接口只认"是不是管理员"**，不认 `users_manage`：非管理员即使有这个权限位也会 `403 forbidden`（防止自造管理员提权）。
- 被禁用的账号视为无任何权限（`Can()` 恒为假），且无法登录（`403 disabled`）。
- 管理员通过 `?user=<账号>` 代操作时，**目标用户的权限位被跳过**（这是"管理员代办"的语义），但请求仍会留审计。

### 10.3 管理员没有自己的存储空间

- 管理员记录的 `root` 必须为空，否则写入时会报"账号是管理员，不能拥有存储空间（root 必须为空）"。
- 管理员**不带** `?user=` 调用文件类接口时，服务端返回 `403 forbidden`，消息为"管理员没有自己的存储空间，请在管理页面选择目标用户"。
- 因此管理员**不能上传自己的文件**：要管理文件必须先选择某个用户，再以该用户身份操作。

### 10.4 存储根的唯一性与嵌套规则

`root` 是"存储空间标识"，不是账号名，但它**必须唯一**：

- 两个账号不能使用同一个 `root`（大小写不敏感：`Shared` 与 `shared` 视为同一个）；
- 也不能一个套一个（`shared` 与 `shared/team` 互为父子），否则子账号会直接落在父账号的目录里，
  既能看见别人的文件，用量还会互相计入；
- 违反以上任一条，创建/修改用户时返回 `400 root_conflict`。

> 想要"一个目录给多个账号用"时，正确做法是**只给一个账号分配该根目录**，
> 需要多账号访问就共享这个账号的凭据，或者等后续版本加共享链接/多用户挂载。

---

## 11. 用 curl 走完一遍

以下示例假定服务运行在 `http://127.0.0.1:8080`，账号 `alice` / 密码 `alice-pass-2026`，且 `alice` 已通过强制改凭据阶段。

```bash
BASE=http://127.0.0.1:8080
J='Content-Type: application/json'

# 0) 健康检查（不需要登录）
curl -s $BASE/api/healthz
# {"status":"ok","version":"0.1.1","time":"..."}

# 1) 登录并保存 Cookie
curl -s -c cookies.txt -X POST $BASE/api/auth/login -H "$J" \
  -d '{"username":"alice","password":"alice-pass-2026"}'
# {"user":{...},"home":"/"}

# 1b) 确认当前身份与权限
curl -s -b cookies.txt $BASE/api/auth/me
# {"user":{...},"home":"/","officeAvailable":false,"sessionTTL":"168h",...}

# 2) 建目录：docs → docs/2026
curl -s -b cookies.txt -X POST $BASE/api/fs/mkdir -H "$J" \
  -d '{"dir":"","name":"docs"}'
# {"path":"docs"}
curl -s -b cookies.txt -X POST $BASE/api/fs/mkdir -H "$J" \
  -d '{"dir":"docs","name":"2026"}'
# {"path":"docs/2026"}

# 3) 分片上传一个 20MiB 文件到 docs/2026
FILE=/tmp/big.bin
SIZE=$(stat -c %s "$FILE")          # Linux；Windows 下可用 wc -c < "$FILE"
INIT=$(curl -s -b cookies.txt -X POST $BASE/api/upload/init -H "$J" \
  -d "{\"dir\":\"docs/2026\",\"name\":\"big.bin\",\"size\":$SIZE}")
echo "$INIT"
# {"id":"u_...","chunkSize":8388608,"chunkCount":3,"received":[],...}
ID=$(echo "$INIT" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')

for i in 0 1 2; do
  dd if="$FILE" of=/tmp/part bs=8388608 skip=$i count=1 2>/dev/null
  curl -s -b cookies.txt -X PUT --data-binary @/tmp/part \
    -H 'Content-Type: application/octet-stream' \
    "$BASE/api/upload/$ID/$i" > /dev/null
done

# 3b) 查询进度 / 列出未完成会话
curl -s -b cookies.txt "$BASE/api/upload/$ID"
curl -s -b cookies.txt "$BASE/api/upload"

# 3c) 合并落盘
curl -s -b cookies.txt -X POST "$BASE/api/upload/$ID/complete"
# {"name":"big.bin","path":"docs/2026/big.bin","size":20971520}

# 4) 列表
curl -s -b cookies.txt "$BASE/api/fs/list?path=docs/2026"
# {"path":"docs/2026","parent":"docs","entries":[{...}],"usage":{...}}

# 5) 下载（带审计记录；加 inline=1 则是内联预览）
curl -s -b cookies.txt -o out.bin \
  "$BASE/api/fs/download?path=docs/2026/big.bin&download=1"
ls -l out.bin

# 5b) 断点续传下载：只要最后 1024 字节
curl -s -b cookies.txt -r -1024 -o tail.bin \
  "$BASE/api/fs/download?path=docs/2026/big.bin&download=1"

# 6) 删除（移入回收站）
curl -s -b cookies.txt -X POST $BASE/api/fs/delete -H "$J" \
  -d '{"paths":["docs/2026/big.bin"]}'
# {"trashIds":["t_..."],"count":1}

# 6b) 查看回收站
curl -s -b cookies.txt "$BASE/api/trash?limit=10&offset=0"

# 7) 恢复
TID=$(curl -s -b cookies.txt "$BASE/api/trash?limit=1" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
curl -s -b cookies.txt -X POST $BASE/api/trash/restore -H "$J" \
  -d "{\"ids\":[\"$TID\"]}"
# {"restored":["docs/2026/big.bin"],"failed":[]}

# 7b) 再次删除并永久清空（谨慎）
curl -s -b cookies.txt -X POST $BASE/api/fs/delete -H "$J" \
  -d '{"paths":["docs/2026/big.bin"]}' > /dev/null
curl -s -b cookies.txt -X POST $BASE/api/trash/empty -H "$J" -d '{}'
# {"count":1,"bytes":20971520}

# 8) 查审计日志
curl -s -b cookies.txt \
  "$BASE/api/logs?limit=20&action=upload&search=big.bin"
# {"entries":[...],"total":2,"limit":20,"offset":0}

# 8b) 导出 CSV（带 BOM，Excel 可直接打开）
curl -s -b cookies.txt -o logs.csv \
  "$BASE/api/logs?format=csv&limit=500"

# 9) 登出
curl -s -b cookies.txt -c cookies.txt -X POST $BASE/api/auth/logout
# {"ok":true}
```

管理员以某个普通用户身份代办（注意：**每个写操作都要带管理员密码**）：

```bash
# 管理员登录
curl -s -c admin.txt -X POST $BASE/api/auth/login -H "$J" \
  -d '{"username":"admin","password":"admin-pass-2026"}'

# 查看 alice 的文件（读操作，不需要二次密码）
curl -s -b admin.txt "$BASE/api/fs/list?path=&user=alice"

# 以 alice 身份建目录（写操作，必须带管理员密码）
curl -s -b admin.txt -X POST "$BASE/api/fs/mkdir?user=alice" -H "$J" \
  -d '{"dir":"","name":"from-admin","password":"admin-pass-2026"}'
# {"path":"from-admin"}

# 不带密码 → 428
curl -s -b admin.txt -X POST "$BASE/api/fs/mkdir?user=alice" -H "$J" \
  -d '{"dir":"","name":"x"}'
# {"error":"该操作需要再次输入管理员密码","code":"reauth_required"}

# 分片上传的分片请求用请求头传密码（base64）
#   printf %s 'admin-pass-2026' | base64   →  YWRtaW4tcGFzcy0yMDI2
curl -s -b admin.txt -X PUT --data-binary @/tmp/part \
  -H 'X-LD-Password-B64: YWRtaW4tcGFzcy0yMDI2' \
  "$BASE/api/upload/$ID/0?user=alice"
```

创建用户与改设置：

```bash
# 新建普通用户 bob（默认配额/限速取自运行期设置）
curl -s -b admin.txt -X POST $BASE/api/users -H "$J" -d '{
  "username":"bob","password":"bob-pass-2026","displayName":"Bob",
  "root":"bob","quota":"20GiB","rateLimitMbps":50,
  "permissions":["read","upload","modify","delete","trash_view","trash_manage"],
  "adminPassword":"admin-pass-2026"
}'

# 改运行期设置
curl -s -b admin.txt -X PATCH $BASE/api/settings -H "$J" -d '{
  "defaultQuota":"10GiB","defaultRateLimitMbps":0,
  "trashRetention":"30d","logRetention":"2160h",
  "uploadSessionTTL":"24h","maxUploadSize":0,"chunkSize":"8MiB",
  "sessionTTL":"168h","trustProxy":false,"accessLogDisabled":false,
  "officePreview":"auto","adminPassword":"admin-pass-2026"
}'
```
