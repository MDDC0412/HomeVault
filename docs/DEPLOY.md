# HomeVault 部署指南

本文覆盖四种部署方式：**直接跑二进制**、**Docker**、**systemd**、**反向代理**（Caddy / Nginx）。全部产物都在仓库的 `deploy/` 目录里。

HomeVault 的部署特点：

- **单二进制、零依赖**：`CGO_ENABLED=0` 静态编译，不需要运行时、不需要 Node、不需要数据库。
- **零配置启动**：不带配置文件也能起来（监听 `0.0.0.0:8080`，空间目录用系统默认）。生产环境建议显式写 `config.ini`。
- **只有 6 项引导配置**：其余参数在管理页面里改。
- **前端已内嵌进二进制**（`//go:embed all:web`），不需要额外的静态目录或 CDN。

---

## 1. 直接运行二进制

### 1.1 编译

```bash
git clone <仓库地址> homevault
cd homevault

# 本机平台
go build -trimpath -ldflags "-s -w" -o homevault .

# 交叉编译（示例）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/homevault-linux-amd64 .
```

Windows 上一键出全部产物：

```powershell
pwsh -File .\build.ps1
```

产物在 `dist\` 下（`windows/amd64`、`linux/amd64`、`linux/arm64`、`linux/arm/v7`、`darwin/amd64`、`darwin/arm64`），脚本最后会打印每个产物的路径与大小。脚本**不做任何联网操作**（本模块没有任何第三方依赖，不需要 `go mod download`）。

### 1.2 首次启动

```bash
mkdir -p /var/lib/homevault
cd /var/lib/homevault

# 首次运行会在当前目录生成 config.ini，并创建管理员 admin/admin
/usr/local/bin/homevault serve
```

控制台输出：

```
  homevault 0.1.0 已启动
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

浏览器打开 `http://<主机>:8080/`，用 `admin` / `admin` 登录，界面会**强制**要求改账号或密码；在改之前，除"改凭据/登出/健康检查"外的接口一律返回 `403 must_change_credentials`。

> 管理员登录后会被引导到**管理端 `/admin/`**（创建/禁用用户、选一个用户去管理他的文件、改运行期设置、看审计日志与磁盘用量）。
> 用户端在 `/`。管理员**没有自己的存储空间**，所有文件操作都要先在管理端选中目标用户。

> **注意**：空间目录默认值在 Linux 上是 `/root/HomeVaultSpace`。用非 root 用户运行时必须显式设置 `space_dir`，否则会报"创建空间目录失败"。

---

## 2. Docker

`deploy/` 下有两个 Dockerfile，以及一个包含两个服务的 `docker-compose.yml`：

| 文件 | 基础镜像 | 用途 | 镜像体积 |
| --- | --- | --- | --- |
| `deploy/Dockerfile.lite` | `alpine` | 只有 HomeVault 本体；Office 文件退化为"提示下载查看" | 约 25–40 MB |
| `deploy/Dockerfile.full` | `debian:stable-slim` + LibreOffice | 带服务端 Office→PDF 预览（Writer / Calc / Impress + 中文字体） | 约 700 MB–1 GB |

### 2.1 构建

在**仓库根目录**执行（Dockerfile 里需要 `go.mod`、`main.go`、`internal/`、`web/`）：

```bash
# 精简版
docker build -f deploy/Dockerfile.lite -t homevault:lite .

# 完整版（含 LibreOffice，构建与拉取都比较慢）
docker build -f deploy/Dockerfile.full -t homevault:full .
```

`.dockerignore` 已经排除了 `dist/`、`.git/`、`.gocache/`、`.gopath/`、`.gotmp/`、`data/` 等不需要进构建上下文的内容。

### 2.2 运行

```bash
mkdir -p ./data

# 精简版
docker run -d --name homevault \
  -p 127.0.0.1:8080:8080 \
  -v "$PWD/data:/data" \
  --restart unless-stopped \
  homevault:lite

# 完整版（Office 预览）
docker run -d --name homevault-full \
  -p 127.0.0.1:8081:8080 \
  -v "$PWD/data-full:/data" \
  --restart unless-stopped \
  homevault:full
```

镜像内的约定：

| 项 | 值 |
| --- | --- |
| 工作目录 | `/data`（`WORKDIR /data`，因此 `config.ini` 默认落在 `/data/config.ini`） |
| 启动命令 | `/homevault serve -config /data/config.ini` |
| 空间目录 | `/data/HomeVaultSpace`（`Dockerfile` 里写好了 `space_dir = "/data/HomeVaultSpace"`） |
| 数据卷 | `VOLUME /data` |
| 端口 | `EXPOSE 8080`（容器内监听 `0.0.0.0:8080`） |
| 运行用户 | 非 root，uid / gid 均为 `10001`（用户 `homevault`） |
| 时区与证书 | `tzdata`（审计日志按天分文件、日志时间显示）+ `ca-certificates`（需要访问外网时） |

关于 `read_only: true`：**不要**给这个容器开只读根文件系统。HomeVault 的整个空间目录（`files/`、`trash/`、`audit/`、`uploads/`、`cache/`、`state.json`）都在 `/data` 下持续写入，虽然 `/data` 是卷、理论上可以在 `read_only: true` + `tmpfs` 下跑，但 `/data/config.ini` 也需要能被改写（管理页面改"存储位置"时会回写它），一旦某个路径写不进去就会以 `500 internal_error` 的形式暴露在界面上。**保持默认（可写）最省事**。

> `--user 10001:10001` 不需要额外指定，镜像里已经 `USER homevault`。宿主机的 `./data` 目录需要让 uid 10001 可写：`sudo chown -R 10001:10001 ./data`。

### 2.3 docker-compose

```bash
cd deploy

# 只起精简版
docker compose --profile lite up -d

# 只起完整版（带 Office 预览）
docker compose --profile full up -d

# 查看日志
docker compose logs -f homevault-lite
```

数据落在 `deploy/data/` 与 `deploy/data-full/`（可在 compose 里改成绝对路径）。两个服务都绑定 `127.0.0.1:8080` / `127.0.0.1:8081`，**建议前面挂一层反向代理**（见 §4），不要直接暴露端口。

关于健康检查：镜像里是 `alpine` / `distroless` 风格的最小根文件系统，**不一定有 `wget` 或 `curl`**，所以 compose 里的 `healthcheck` 默认是注释掉的。三种可选做法：

1. **不配健康检查**（默认），靠 `restart: unless-stopped` 兜底；
2. 自己扩展镜像加一个静态 `wget`（`apk add --no-cache wget`），再用 `wget -qO- http://127.0.0.1:8080/api/healthz`；
3. 在**宿主机**上用 `curl -fsS http://127.0.0.1:8080/api/healthz` 做外部探活（`/api/healthz` 不需要登录，是最合适的探活端点）。

### 2.4 在容器里使用管理命令

```bash
# 重置管理员密码（随机生成并打印）
docker exec -it homevault /homevault setpass -user admin -config /data/config.ini

# 新增用户
docker exec -it homevault /homevault adduser \
  -user alice -password 'alice-pass-2026' -root alice -quota 10GiB \
  -config /data/config.ini

# 换盘迁移（需要把新盘也挂进容器）
docker exec -it homevault /homevault migrate-space \
  --to /data/new/HomeVaultSpace --config /data/config.ini
```

注意 `setpass` / `adduser` 会读同一个 `state.json`，**操作前最好停掉容器**（`docker stop homevault`），避免两个进程同时写元数据。

---

## 3. systemd

`deploy/homevault.service` 是一个已经加固过的单元文件。

### 3.1 完整安装步骤

```bash
# 1) 创建专用系统用户与组（无登录、无 home）
sudo useradd --system --no-create-home --shell /usr/sbin/nologin homevault

# 2) 安装二进制
sudo install -m 0755 -o root -g root homevault /usr/local/bin/homevault

# 3) 创建配置目录与空间目录
sudo mkdir -p /etc/homevault
sudo mkdir -p /var/lib/homevault/HomeVaultSpace

# 4) 目录属主：配置目录给 root（进程只读），空间目录给 homevault，服务需要写
sudo chown -R root:root /etc/homevault
sudo chown -R homevault:homevault /var/lib/homevault
sudo chmod 0750 /etc/homevault /var/lib/homevault

# 5) 写配置（space_dir 必须是绝对路径，且必须是 homevault 可写的目录）
sudo tee /etc/homevault/config.ini >/dev/null <<'EOF'
listen = "127.0.0.1:8080"
space_dir = "/var/lib/homevault/HomeVaultSpace"
tls_cert = ""
tls_key = ""
admin_user = "admin"
admin_password = "改成你自己的强密码"
EOF
sudo chown root:root /etc/homevault/config.ini
sudo chmod 0600 /etc/homevault/config.ini

# 6) 安装单元文件
sudo install -m 0644 deploy/homevault.service /etc/systemd/system/homevault.service

# 7) 启用并启动
sudo systemctl daemon-reload
sudo systemctl enable --now homevault

# 8) 查看状态与日志
sudo systemctl status homevault
sudo journalctl -u homevault -f
```

### 3.2 单元文件要点

| 指令 | 值 | 说明 |
| --- | --- | --- |
| `User` / `Group` | `homevault` | 绝不 root 运行 |
| `WorkingDirectory` | `/var/lib/homevault` | `-config` 给的是绝对路径，这里只是给相对路径兜底 |
| `ExecStart` | `/usr/local/bin/homevault serve -config /etc/homevault/config.ini` | 前台运行，交给 systemd 管生命周期 |
| `Restart` / `RestartSec` | `on-failure` / `3` | 崩溃 3 秒后重启 |
| `NoNewPrivileges` | `true` | 禁止通过 setuid/setgid 提权 |
| `PrivateTmp` | `true` | 独立的 `/tmp` |
| `ProtectSystem` | `strict` | 整个文件系统只读，只有 `ReadWritePaths` 例外 |
| `ProtectHome` | `true` | 看不到 `/home`、`/root` |
| `ReadWritePaths` | `/var/lib/homevault` | 唯一允许写入的路径 |
| `ProtectKernelTunables` / `ProtectControlGroups` / `ProtectKernelModules` | `true` | 不允许碰内核参数、cgroup、模块 |
| `RestrictAddressFamilies` | `AF_INET AF_INET6 AF_UNIX` | 只允许必要协议族 |
| `SystemCallFilter` | `@system-service` | 只用 systemd 预设的服务类系统调用白名单 |
| `LimitNOFILE` | `65535` | 大文件并发上传/下载需要较多 fd |

`ProtectSystem=strict` + `ReadWritePaths=/var/lib/homevault` 意味着**空间目录必须落在 `/var/lib/homevault` 下面**。如果你把空间目录放到 `/mnt/data/HomeVaultSpace`，必须同步修改 `ReadWritePaths`，否则启动时会因为无法创建目录而失败。

### 3.3 常用运维命令

```bash
sudo systemctl start homevault      # 启动
sudo systemctl stop homevault       # 停止（会优雅退出并保存 state.json）
sudo systemctl restart homevault    # 重启
sudo systemctl status homevault     # 状态
sudo systemctl enable homevault     # 开机自启
sudo journalctl -u homevault -n 200 --no-pager    # 最近 200 行日志
sudo journalctl -u homevault --since "1 hour ago" # 最近一小时
```

调日志级别（临时）：

```bash
sudo systemctl edit homevault
# 在打开的编辑器里写：
#   [Service]
#   ExecStart=
#   ExecStart=/usr/local/bin/homevault serve -config /etc/homevault/config.ini -log-level debug
sudo systemctl daemon-reload && sudo systemctl restart homevault
```

注意 `ExecStart=` 空行是**必须**的，用来清掉原有定义。

---

## 4. 反向代理

服务端本身不做 TLS 终结（除非你配了 `tls_cert` / `tls_key`），生产上推荐由 Caddy 或 Nginx 反代。两个示例文件都在 `deploy/` 下。

### 4.1 反代必须注意的四件事

| 事项 | 配置 | 为什么 |
| --- | --- | --- |
| **不要缓冲请求体** | `proxy_request_buffering off;` | 否则分片上传会被 Nginx 先落盘到临时目录再转发，磁盘写两遍、进度条失真、大文件直接翻倍占用磁盘 |
| **请求体不设上限** | `client_max_body_size 0;` | 分片上传的单片默认 8 MiB，但小文件直传可能很大；上限应该由 HomeVault 的 `maxUploadSize` / 配额来控制，而不是由 Nginx 返回 413 |
| **拉长超时** | `proxy_read_timeout 3600s;` `proxy_send_timeout 3600s;` | 上传/下载一个几十 GB 的文件可能持续很久；下载还依赖长连接做 Range 续传 |
| **不要缓存 `/api/`** | `proxy_no_cache` / 不配 `proxy_cache` | `/api/` 全部是带上限状态的数据，缓存会导致列表、配额、回收站显示错乱 |

另外必须透传这几个头：`X-Forwarded-For`、`X-Real-IP`、`X-Forwarded-Proto`。**只有在管理页面把「信任反向代理」打开之后，服务端才会采信 `X-Forwarded-For` / `X-Real-IP`**；没打开时记录的客户端 IP 会是代理的地址。

### 4.2 Caddy

`deploy/Caddyfile`：

```caddyfile
drive.example.com {
    encode zstd gzip

    # Caddy 自动申请并续期 Let's Encrypt 证书，无需手写 TLS 配置。
    # gzip/zstd 会顺带处理静态资源；/api/ 的响应体很小，压缩不影响上传。
    reverse_proxy 127.0.0.1:8080 {
        # 大文件上传/下载可能持续很久，放宽超时（Caddy 默认也是长超时，这里显式写出）
        transport http {
            read_timeout 3600s
            write_timeout 3600s
        }
        # 透传真实客户端信息（服务端需在设置里打开"信任反向代理"才会采信）
        header_up X-Real-IP {remote_host}
        header_up X-Forwarded-For {remote_host}
        header_up X-Forwarded-Proto {scheme}
    }

    # 不要缓存 API：/api/ 下的响应都带上限状态（列表、配额、回收站、日志）
    @api path /api/*
    header @api Cache-Control "no-store"
}
```

Caddy 的关键优势：**自动 HTTPS**（自动申请、自动续期、自动 HTTP→HTTPS 跳转），以及**默认不缓冲请求体**（Caddy 的 `reverse_proxy` 是流式转发，不需要像 Nginx 那样显式关缓冲）。如果你确实想让 Caddy 先缓冲（极少数场景），才需要加 `request_buffers`。

启动：

```bash
sudo cp deploy/Caddyfile /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

### 4.3 Nginx

`deploy/nginx.conf` 的核心片段：

```nginx
server {
    listen 443 ssl http2;
    server_name drive.example.com;

    ssl_certificate     /etc/letsencrypt/live/drive.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/drive.example.com/privkey.pem;

    # 上传上限交给 homevault 自己控制（配额 / maxUploadSize），Nginx 不设限
    client_max_body_size 0;
    client_body_timeout  3600s;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # 关键：不缓冲请求体，否则分片上传会被先写到磁盘再转发
        proxy_request_buffering off;
        proxy_buffering         off;

        # 长传输
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
        proxy_connect_timeout 60s;

        # WebSocket / 长连接兼容（当前版本未用到，保留无害）
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection "upgrade";
    }

    # 不要缓存 API
    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_request_buffering off;
        proxy_buffering         off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
        proxy_no_cache     1;
        proxy_cache_bypass 1;
        add_header Cache-Control "no-store" always;
    }
}

server {
    listen 80;
    server_name drive.example.com;
    return 301 https://$host$request_uri;
}
```

`client_max_body_size 0;` 在 Nginx 里表示**不限大小**（注意不是"0 字节"）。

### 4.4 反代后的检查清单

```bash
# 1) 服务端监听的是 127.0.0.1（不直接对外）
grep '^listen' /etc/homevault/config.ini      # listen = "127.0.0.1:8080"

# 2) 管理页面打开"信任反向代理"（系统设置 → 信任反向代理 → 保存）
#    否则审计日志里的 IP 是代理地址

# 3) 健康检查能通
curl -fsS https://drive.example.com/api/healthz

# 4) 审计日志里的 IP 是真实客户端
curl -fsS -b admin.txt 'http://127.0.0.1:8080/api/logs?limit=5'
```

---

## 5. HTTPS 的两种方式

| 方式 | 配置 | 适用 |
| --- | --- | --- |
| **内置 HTTPS** | `config.ini` 里同时设 `tls_cert` 与 `tls_key` | 已经有证书文件、不想引入反代 |
| **反向代理终结 TLS** | `tls_cert` / `tls_key` 留空 + Caddy / Nginx | 生产推荐；可以顺带做压缩、限流、访问控制 |

启用内置 HTTPS 时，登录 Cookie 会自动加 `Secure` 标记（HTTP 下不加，否则浏览器不会回传）。两种方式都不要混用：反代后面再套一层内置 TLS 只会浪费 CPU。

常见的证书路径：

```ini
# Let's Encrypt（certbot）
tls_cert = "/etc/letsencrypt/live/drive.example.com/fullchain.pem"
tls_key  = "/etc/letsencrypt/live/drive.example.com/privkey.pem"

# Windows（IIS 导出的 PFX 需要先转成 PEM）
tls_cert = "C:\\homevault\\tls\\fullchain.pem"
tls_key  = "C:\\homevault\\tls\\privkey.pem"
```

`tls_cert` 与 `tls_key` **必须同时设置或同时留空**，只设一个会启动失败。

---

## 6. Office 预览（可选）

Word / Excel / PPT / ODF / RTF 由服务端调用 LibreOffice 转成 PDF 后预览。没有 LibreOffice 时这些文件退化为"提示下载查看"（接口返回 `409 office_preview_unavailable`，前端弹出下载提示）。**其余格式**（图片、PDF、文本、音视频）不依赖 LibreOffice，浏览器直接内联渲染。

| 方式 | 做法 |
| --- | --- |
| Debian / Ubuntu | `sudo apt-get install -y --no-install-recommends libreoffice-writer libreoffice-calc libreoffice-impress fonts-noto-cjk` |
| RHEL / Rocky | `sudo dnf install -y libreoffice-writer libreoffice-calc libreoffice-impress google-noto-sans-cjk-fonts` |
| Docker | 用 `deploy/Dockerfile.full`（已包含上述包，体积约 700 MB–1 GB） |
| Windows | 安装 LibreOffice，或设置 `HOMEVAULT_SOFFICE` 指向 `soffice.exe` 的绝对路径 |

要点：

- 中文字体（`fonts-noto-cjk`）必须装，否则中文内容会显示成方块。
- 转换结果缓存在 `<space_dir>/cache/office/`，默认 24 小时后由后台清理；缓存目录可以随时整体删除。
- 检测结果缓存 60 秒。装完 LibreOffice 后最多等一分钟，或重启服务。
- 自定义路径用环境变量 `HOMEVAULT_SOFFICE`：

  ```ini
  # systemd 覆盖
  sudo systemctl edit homevault
  #   [Service]
  #   Environment=HOMEVAULT_SOFFICE=/opt/libreoffice/program/soffice
  ```

- 不想要这个功能时，把运行期设置 `officePreview` 改成 `off`，服务端会直接跳过转换（省掉一次进程探测）。

服务端转换的资源约束（内置，不需要配置）：同时最多 2 个转换任务、单次转换超时 120 秒、同一文件的并发请求会合并成一次转换。

---

## 7. 生产环境检查清单

- [ ] `admin_password` 已改成强密码（或者首启后立刻通过界面改掉）；确认管理员不再是 `mustChangeCredentials` 状态
- [ ] `space_dir` 是**绝对路径**，且落在持久化磁盘上（不是容器临时层、不是 tmpfs）
- [ ] `space_dir` 所在磁盘的可用空间远大于预期数据总量（回收站会额外占一份）
- [ ] 监听地址是 `127.0.0.1:8080`（有反代）或已配好 TLS（无反代），**不要**把裸 HTTP 暴露到公网
- [ ] 反代已设置 `proxy_request_buffering off`、`client_max_body_size 0`、超时 3600s，且没有缓存 `/api/`
- [ ] 在管理页面打开了「信任反向代理」（如果确实在反代后面）
- [ ] 运行期设置里确认：`defaultQuota`、`trashRetention`（默认 30 天）、`logRetention`（默认 90 天）、`maxUploadSize`、`sessionTTL`
- [ ] 需要 Office 预览的话装好 LibreOffice + 中文字体，或用 `Dockerfile.full`
- [ ] 备份策略已落地（见 [UPGRADE_BACKUP.md](UPGRADE_BACKUP.md)），并**实际演练过一次恢复**
- [ ] 审计日志可查：用管理员登录后在"审计日志"页面按账号/动作/时间过滤一次，导出一份 CSV 验证中文不乱码
- [ ] systemd 加固项里的 `ReadWritePaths` 与实际 `space_dir` 一致
- [ ] 确认 `/api/healthz` 的探活已接入监控

---

## 8. 相关文档

- [CONFIG.md](CONFIG.md) — 配置文件与运行期设置逐项说明
- [API.md](API.md) — 完整 HTTP 接口契约
- [UPGRADE_BACKUP.md](UPGRADE_BACKUP.md) — 升级、备份、恢复、换盘
