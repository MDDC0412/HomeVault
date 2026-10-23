# HomeVault 升级、备份与恢复

HomeVault 的运维面很小：**一个二进制 + 一个配置文件 + 一个空间目录**。备份就是抄目录，升级就是换二进制，回滚就是把旧二进制换回来。本文把这三件事写清楚。

> 一句话结论：**数据全在 `<space_dir>` 里**（配置、用户、配额、回收站、审计日志都在其中），唯一在空间目录外面的东西是 `config.ini`（它本身也可能只有 6 行）。备份这两样就完整了。

---

## 1. 备份

### 1.1 需要备份什么

| 对象 | 路径 | 必需？ | 说明 |
| --- | --- | --- | --- |
| 空间目录 | `<space_dir>`（默认 `C:\HomeVaultSpace` 或 `/root/HomeVaultSpace`） | **必须** | 用户文件、回收站、审计日志、分片、缓存、全部元数据 |
| 配置文件 | `config.ini`（或 `-config` 指定的路径） | **建议** | 只有 6 行，但丢了就得重建（并可能写错） |
| 二进制 | `homevault` | 不必 | 随时可重新编译；但**升级前留着旧版本**便于回滚 |

空间目录里各子目录的备份价值：

```
<space_dir>/
├── files/         ★★★ 用户数据，必背
├── state.json     ★★★ 账号、配额、权限、回收站索引、上传会话、运行期设置
├── trash/         ★★  回收站实体（不背就恢复不了误删文件）
├── audit/         ★★  审计日志（合规场景必需）
├── uploads/       ☆   未完成的分片上传，备份价值低（会话过期即作废）
└── cache/         ☆   Office 预览缓存，完全是派生物，**可以排除**
```

`cache/` 和 `uploads/` 可以排除以加快备份速度；`state.json` 与 `files/`、`trash/`、`audit/` 必须保持一致（同一次快照里）。

### 1.2 为什么不能只抄 `files/`

- `state.json` 里有**账号与权限**：没有它，服务启动时会认为"首次启动"，重新创建 `admin/admin` 并进入强制改密状态，而原来的账号、配额、权限全部消失（文件还在磁盘上，但没人能通过界面访问到）。更糟的是：如果 `files/` 里的目录名与原来的 `root` 对不上，还会变成孤儿数据。
- `trash/` 没有的话，回收站里的东西就真的找不回来了。
- `audit/` 是合规与追责的唯一来源，丢了不可重建。

### 1.3 Linux：rsync

```bash
# 冷备份（推荐：先停服务，保证 state.json 与 files 是同一时刻的快照）
sudo systemctl stop homevault
sudo rsync -aHAX --delete --info=progress2 \
  --exclude 'cache/' --exclude 'uploads/' \
  /var/lib/homevault/HomeVaultSpace/ /mnt/backup/homevault/HomeVaultSpace/
sudo cp -a /etc/homevault/config.ini /mnt/backup/homevault/config.ini
sudo systemctl start homevault
```

`-aHAX` 分别对应：递归+保留属性、保留硬链接、保留 ACL、保留扩展属性。HomeVault 本身不创建硬链接，但保留属性总是安全的。

**不停服务的热备份**也可以（服务运行中 `rsync`），但要注意 `state.json` 在写入时是"临时文件 + 原子 rename"，所以读到的永远是一个**完整**的 JSON，不会读到半个文件；风险主要是 `files/` 与 `state.json` 之间可能有秒级不一致（例如刚上传完文件但元数据还没落），对一般场景可以接受。**不能接受的话就停服务冷备。**

### 1.4 Windows：robocopy

```powershell
# 冷备份：先停服务（如果是 NSSM 等服务包装器，用 net stop）
Stop-Service homevault -ErrorAction SilentlyContinue

robocopy "E:\HomeVaultSpace" "\\nas\backup\homevault\HomeVaultSpace" /MIR `
  /XD cache uploads /COPY:DAT /DCOPY:DAT /R:2 /W:2 /NP /TEE /LOG+:backup.log

Copy-Item "C:\homevault\config.ini" "\\nas\backup\homevault\config.ini" -Force

Start-Service homevault
```

参数说明：`/MIR` = 镜像（会删除目标端多余文件，用之前确认目标路径！）、`/XD cache uploads` = 排除这两个目录、`/COPY:DAT` = 复制数据+属性+时间戳、`/DCOPY:DAT` = 目录也保留时间戳。

> `robocopy` 的退出码 0–7 都表示成功（1 = 有文件被复制，2 = 有额外文件，3 = 两者都有），**只有 ≥ 8 才是失败**。写脚本时别把 1 当错误。

### 1.5 一致性快照（进阶）

如果文件系统支持快照（ZFS / Btrfs / LVM / Windows VSS / 云盘快照），快照是最省心的方式：

```bash
# ZFS
sudo zfs snapshot tank/homevault@$(date +%Y%m%d-%H%M)

# LVM（先确认卷组名与挂载点）
sudo lvcreate -L 10G -s -n homevault-snap /dev/vg0/homevault
```

快照是**瞬间**完成的，因此 `state.json` 与 `files/` 天然一致，服务甚至不需要停。

### 1.6 导出"配置清单"

除了文件本身，建议定期把"当前生效的设置"导出留档（出问题时能对比）：

```bash
# 管理员 Cookie 存成 admin.txt 后
curl -s -b admin.txt http://127.0.0.1:8080/api/settings > settings-$(date +%F).json

# 账号清单（含配额、权限、用量）
curl -s -b admin.txt http://127.0.0.1:8080/api/users > users-$(date +%F).json

# 审计日志 CSV
curl -s -b admin.txt -o logs-$(date +%F).csv \
  'http://127.0.0.1:8080/api/logs?format=csv&limit=500'
```

`config.ini` 与 `state.json` 里**没有任何明文密码**（只有 `pbkdf2-sha256$210000$…` 哈希），所以这些留档可以直接放保险柜，不必加密（但审计日志与账号清单涉及隐私，仍建议加密存储）。

### 1.7 备份策略建议

| 场景 | 建议 |
| --- | --- |
| 个人 / 家庭 | 每天一次 rsync/robocopy 到另一块盘或 NAS；每周一次异地 |
| 小团队 | 每天冷备 + 保留 7 天日备、4 周周备、12 月月备 |
| 合规场景 | 审计日志单独长期归档（`audit/` 是纯追加的 JSONL，非常适合追加式归档） |
| 大容量 | 快照 + 增量同步；`cache/`、`uploads/` 一律排除 |

**"3-2-1" 仍然适用**：3 份副本、2 种介质、1 份异地。

---

## 2. 恢复

### 2.1 完整恢复（换机器 / 灾难恢复）

```bash
# 1) 装好同版本（或更新版本）的 homevault 二进制
sudo install -m 0755 -o root -g root ./homevault /usr/local/bin/homevault

# 2) 准备用户与目录
sudo useradd --system --no-create-home --shell /usr/sbin/nologin homevault
sudo mkdir -p /var/lib/homevault /etc/homevault

# 3) 还原空间目录（注意结尾斜杠：源和目标都要有）
sudo rsync -aHAX /mnt/backup/homevault/HomeVaultSpace/ /var/lib/homevault/HomeVaultSpace/
sudo chown -R homevault:homevault /var/lib/homevault

# 4) 还原配置（如果目标机器路径不同，改一下 space_dir）
sudo cp /mnt/backup/homevault/config.ini /etc/homevault/config.ini
sudo chown root:root /etc/homevault/config.ini
sudo chmod 0600 /etc/homevault/config.ini

# 5) 关键一步：确认空间目录里确实有 state.json
ls -l /var/lib/homevault/HomeVaultSpace/state.json

# 6) 启动并验证
sudo systemctl start homevault
curl -fsS http://127.0.0.1:8080/api/healthz
```

如果第 5 步发现**没有** `state.json`，千万不要直接启动：服务会当成首次启动，重新创建 `admin/admin` 并强制改密，同时原来的账号配置在界面上"消失"。此时应该去备份里把 `state.json` 找回来。

### 2.2 只恢复账号配置

场景：文件都还在，但 `state.json` 丢了。

```bash
# 停服务，把备份里的 state.json 放回去
sudo systemctl stop homevault
sudo cp /mnt/backup/homevault/HomeVaultSpace/state.json /var/lib/homevault/HomeVaultSpace/state.json
sudo chown homevault:homevault /var/lib/homevault/HomeVaultSpace/state.json
sudo systemctl start homevault
```

如果连 `state.json` 的备份都没有，那只能手工重建：启动服务（会用 `admin/admin` 建一个新管理员，登录后强制改密），然后在管理页面按记忆逐个重建账号，`root` 填成与 `files/` 下已有目录一致的值，文件就能重新看到。

### 2.3 只恢复单个文件

```bash
# 从备份里找回某个文件（注意备份里的路径是 <space_dir>/files/<root>/<相对路径>）
sudo cp /mnt/backup/homevault/HomeVaultSpace/files/alice/docs/report.pdf \
        /var/lib/homevault/HomeVaultSpace/files/alice/docs/report.pdf
sudo chown homevault:homevault /var/lib/homevault/HomeVaultSpace/files/alice/docs/report.pdf
```

如果服务正在运行，建议**通过界面重新上传**同名文件，而不是直接往磁盘里塞：直接写入不会更新 `usage` 缓存（缓存 5 秒）也不写审计。两种都行，前者留痕更完整。

### 2.4 从回收站恢复误删

界面里就能做（回收站 → 勾选 → 恢复），前提是条目还在保留期内（`trashRetention` 默认 720h = 30 天）。如果条目已经被清理，只能走 §2.3 从备份恢复。

### 2.5 恢复演练清单

恢复**必须演练**，否则真出事时会发现备份是坏的：

- [ ] 在一台干净的机器（或一个新目录）上完整还原
- [ ] 用备份里的账号密码能登录（注意：如果备份后改过密码，密码是备份时刻的旧密码）
- [ ] 管理员能看到所有用户与他们的用量
- [ ] 抽查几个文件能下载，内容与预期一致（比对大小或哈希）
- [ ] 回收站里有备份时刻的条目，能恢复一条
- [ ] 审计日志页面能查到备份时刻之前的记录
- [ ] `GET /api/settings` 返回的设置与备份时刻一致
- [ ] 记录一次"备份 → 恢复"实际耗时（这才是 RTO）

---

## 3. 升级

### 3.1 升级前的准备

```bash
# 1) 记录当前版本与运行状态
homevault version
curl -fsS http://127.0.0.1:8080/api/healthz

# 2) 备份（见 §1，至少是 config.ini + state.json + files/）
sudo systemctl stop homevault
sudo rsync -aHAX --exclude 'cache/' --exclude 'uploads/' \
  /var/lib/homevault/HomeVaultSpace/ /mnt/backup/pre-upgrade-$(date +%F)/HomeVaultSpace/
sudo cp -a /etc/homevault/config.ini /mnt/backup/pre-upgrade-$(date +%F)/config.ini

# 3) 留一份旧二进制（回滚用）
sudo cp /usr/local/bin/homevault /usr/local/bin/homevault-$(homevault version | awk '{print $2}')
```

### 3.2 替换二进制

```bash
# 编译新版本
git pull
go build -trimpath -ldflags "-s -w" -o homevault .

# 原子替换（install 会先写临时文件再改名，避免"替换到一半进程被信号打断"）
sudo systemctl stop homevault
sudo install -m 0755 -o root -g root ./homevault /usr/local/bin/homevault
sudo systemctl start homevault
sudo systemctl status homevault
```

Docker 场景：

```bash
docker build -f deploy/Dockerfile.lite -t homevault:lite .
docker stop homevault && docker rm homevault
docker run -d --name homevault -p 127.0.0.1:8080:8080 \
  -v "$PWD/data:/data" --restart unless-stopped homevault:lite
```

`docker compose` 场景：`docker compose up -d --build` 会重建并滚动替换。

### 3.3 升级后检查

```bash
homevault version                                   # 版本号
curl -fsS http://127.0.0.1:8080/api/healthz         # 版本 + 时间
sudo journalctl -u homevault -n 50 --no-pager       # 有没有警告/错误

# 用管理员 Cookie 抽查
curl -s -b admin.txt http://127.0.0.1:8080/api/system
curl -s -b admin.txt http://127.0.0.1:8080/api/settings | head -c 400
curl -s -b admin.txt 'http://127.0.0.1:8080/api/logs?limit=5'
```

逐项确认：

- [ ] 能登录（用户端与管理端各一次）
- [ ] 目录列表、搜索、下载、预览正常
- [ ] 上传一个文件（小文件走 `fs/upload`，大文件走分片）并确认配额扣减
- [ ] 回收站能列出、恢复、清空
- [ ] 审计日志在写（重新登录一次，日志里应出现新的 `login` 记录）
- [ ] 运行期设置没有被重置（对比 §1.6 留档的 `settings.json`）

### 3.4 回滚

```bash
sudo systemctl stop homevault

# 换回旧二进制
sudo install -m 0755 -o root -g root /usr/local/bin/homevault-0.1.1 /usr/local/bin/homevault

# 如果新版本改动过 state.json 的结构，把升级前的备份也放回去
sudo rsync -aHAX --delete /mnt/backup/pre-upgrade-2026-02-14/HomeVaultSpace/ \
  /var/lib/homevault/HomeVaultSpace/

sudo systemctl start homevault
```

> **重要**：回滚二进制是安全的，但**回滚数据要慎重**。HomeVault 的元数据是单个 JSON 文件，新版本可能写入了旧版本不认识的字段。当前版本（0.1.1）是首个版本，还没有升级路径问题；未来版本升级时，本文档会补充"元数据兼容性"说明。**在不确定的情况下，先只回滚二进制**（旧版本通常能容忍 JSON 里的未知字段，因为 `state.json` 的解析不启用未知字段拒绝；但 `config.ini` 是严格拒绝未知键的，回滚时要确认新版本没往它里面写过新键）。

### 3.5 关于配置文件与运行期设置的兼容性

- `config.ini`：**未知键会让启动失败**。所以升级后如果新版本引入了新的引导配置项，它会带默认值，老文件照常能用（缺键 = 用默认值）。反过来，如果新版本往 `config.ini` 写了新键，回滚到旧版本时**必须**把那一行删掉，否则旧版本会因为"未知配置项"拒绝启动。
- `state.json` 里的 `settings`：JSON 反序列化对未知字段是**宽容**的，所以新版本新增的运行期设置项，在回滚后会被忽略，老版本按自己的默认值运行。请注意这可能导致"设置看起来变了"。

---

## 4. 换盘 / 迁移到更大的磁盘

### 4.1 用 `migrate-space`（推荐）

```bash
sudo systemctl stop homevault

# 目标目录必须为空，或已经包含 state.json
sudo mkdir -p /mnt/big/HomeVaultSpace
sudo chown -R homevault:homevault /mnt/big

# 不加 -yes 会先打印计划并要求输入 yes 确认
sudo -u homevault /usr/local/bin/homevault migrate-space \
  --to /mnt/big/HomeVaultSpace \
  -config /etc/homevault/config.ini

sudo systemctl start homevault
```

它做的事：创建目标目录 → 复制 `files/`、`trash/`、`audit/`、`uploads/`、`cache/`、`state.json` → **校验目标里出现了 `state.json`** → 把新路径写回 `config.ini` 的 `space_dir` → 打印"请重启"。**它不删除旧数据**，验证没问题后再手动清理。

它会拒绝这些情况：

| 情况 | 结果 |
| --- | --- |
| 目标目录等于当前空间目录 | 报错"目标目录与当前空间目录相同" |
| 目标目录在当前空间目录内部 | 报错"目标目录不能位于当前空间目录内部"（防止递归复制） |
| 目标目录非空且没有 `state.json` | 报错"目标目录不是空目录，请换一个位置" |
| 目标目录已有 `state.json` | 只切换配置，**不复制**（用于"数据已经拷过去了"的场景） |
| 复制完发现没有 `state.json` | 报错"复制完成但目标目录缺少 state.json，已放弃切换"（不改写 config.ini） |

### 4.2 systemd 的额外一步

如果空间目录改到了 `/var/lib/homevault` 之外（例如 `/mnt/big/...`），必须同步放宽 systemd 的写权限，否则服务会因为 `ProtectSystem=strict` 无法写入：

```bash
sudo systemctl edit homevault
#   [Service]
#   ReadWritePaths=/mnt/big
sudo systemctl daemon-reload
sudo systemctl restart homevault
```

### 4.3 迁移后的核对

```bash
# 1) config.ini 指向新路径
grep '^space_dir' /etc/homevault/config.ini

# 2) 新路径里元数据在
ls -l /mnt/big/HomeVaultSpace/state.json

# 3) 服务健康
curl -fsS http://127.0.0.1:8080/api/healthz

# 4) 管理员能看到用户与用量（证明 state.json 与 files 都对得上）
curl -s -b admin.txt http://127.0.0.1:8080/api/users

# 5) 随便下几个文件比对大小（证明 files/ 完整）
# 6) 确认无误后再删旧目录（先重命名，观察几天再删更稳）
sudo mv /var/lib/homevault/HomeVaultSpace /var/lib/homevault/HomeVaultSpace.old
```

### 4.4 通过管理页面改存储位置

管理端 → 系统设置 → 存储位置。服务端会校验：必须是绝对路径、目标可写、**目标里已有 `state.json`（或当前空间本来就是空的）**。不满足时返回：

```json
{
  "error": "目标位置里没有 state.json。请先执行 `homevault migrate-space --to \"/mnt/big/HomeVaultSpace\"` 完成数据迁移，再修改此设置",
  "code": "migration_required"
}
```

也就是说：**先迁移，再改设置**。通过后 `restartRequired` 会包含 `"spaceDir"`，重启才生效。

---

## 5. 数据完整性检查

没有内置的校验命令（这是刻意保持工具面小），但可以用标准命令确认：

```bash
# state.json 是不是合法 JSON（顺便看结构）
python3 -m json.tool /var/lib/homevault/HomeVaultSpace/state.json > /dev/null && echo "state.json OK"

# 统计用户数、回收站条目数
python3 - <<'PY'
import json
d = json.load(open('/var/lib/homevault/HomeVaultSpace/state.json'))
print('用户:', len(d.get('users') or {}))
print('回收站条目:', len(d.get('trash') or []))
print('未完成上传:', len(d.get('uploads') or {}))
print('运行期设置:', sorted((d.get('settings') or {}).keys()))
PY

# 磁盘占用一览
du -sh /var/lib/homevault/HomeVaultSpace/*
```

Windows：

```powershell
Get-Content 'E:\HomeVaultSpace\state.json' -Raw | ConvertFrom-Json |
  ForEach-Object { "用户: $($_.users.PSObject.Properties.Count)"; "回收站: $($_.trash.Count)" }
Get-ChildItem 'E:\HomeVaultSpace' | ForEach-Object {
  $s = (Get-ChildItem $_.FullName -Recurse -File -ErrorAction SilentlyContinue |
        Measure-Object Length -Sum).Sum
  "{0,-12} {1,12:N0} 字节" -f $_.Name, $s
}
```

`state.json` 的结构说明见 [CONFIG.md](CONFIG.md) §2。

---

## 6. 常见事故与处置

| 现象 | 原因 | 处置 |
| --- | --- | --- |
| 启动后"账号全没了"，只剩 `admin/admin` | `state.json` 缺失或损坏，服务当成首次启动 | 停服务，从备份恢复 `state.json`，再启动 |
| 启动失败："创建空间目录失败" | 运行用户对 `space_dir` 没有写权限（Linux 默认值 `/root/...` 最常见） | 在 `config.ini` 里显式设置 `space_dir`，并把目录 chown 给运行用户 |
| 启动失败："未知配置项 xxx" | `config.ini` 里写了运行期参数（或旧版本不认识的新键） | 删掉那一行；运行期参数请在管理页面配置 |
| 启动失败："tls_cert 与 tls_key 必须同时设置或同时留空" | 只配了一个 | 两个都配或都留空 |
| 上传大文件返回 413 | Nginx 的 `client_max_body_size` 没放开 | 设成 `0`；上限交给 HomeVault 的 `maxUploadSize` / 配额 |
| 上传时进度条不动、磁盘翻倍占用 | Nginx 在缓冲请求体 | `proxy_request_buffering off;` |
| 上传超时中断 | 反代超时太短 | `proxy_read_timeout 3600s;` `proxy_send_timeout 3600s;` |
| Office 文件打不开，提示"未安装 LibreOffice" | 环境里没有 LibreOffice | 安装它 + `fonts-noto-cjk`，或用 `Dockerfile.full`；不装也没关系，会退化为下载 |
| 中文在 Office 预览里变成方块 | 缺中文字体 | 装 `fonts-noto-cjk` |
| 审计日志里的 IP 全是 `127.0.0.1` | 在反代后面但没打开"信任反向代理" | 管理页面打开「信任反向代理」 |
| 改完运行期设置没生效 | 只有 `spaceDir` 需要重启，其余立即生效；如果改了 `spaceDir` | 按界面提示重启 | 
| 密码忘了 | — | `homevault setpass -user admin`（不给 `-password` 会随机生成并打印） |
| 用户删掉了但文件还在磁盘上 | 删除用户时没勾"同时删除文件" | 这是**设计如此**（默认保留文件）；需要时手动清理孤儿目录，或重建账号指向同一 `root` |

---

## 7. 相关文档

- [CONFIG.md](CONFIG.md) — 配置文件、空间目录布局、运行期设置
- [DEPLOY.md](DEPLOY.md) — Docker / systemd / 反向代理部署与检查清单
- [API.md](API.md) — 完整 HTTP 接口契约（含"用 curl 走完一遍"）
