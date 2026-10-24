#!/bin/sh
# =============================================================================
# homevault 容器入口脚本
#
# 解决两个真实的部署陷阱：
#
# 1) Dockerfile 里在构建阶段就把 /data/config.ini 写好了，但 `VOLUME /data` +
#    宿主机 bind mount 会在运行时**用宿主目录覆盖整个 /data**，构建期写进去的
#    config.ini 就被盖掉了。此时二进制发现配置不存在，会自己写一份模板——而模板
#    里 `space_dir = ""` 会解析成 /root/HomeVaultSpace。容器以非 root 用户运行，
#    无权在 /root 下建目录，直接启动失败。所以这里先补一份"容器场景正确"的配置。
#
# 2) 容器内监听端口必须与 compose 里 ports 的**右侧**一致。两边对不上时，
#    浏览器看到的是"拒绝连接"，而日志里一切正常，极难排查。所以只要 compose
#    通过 HOMEVAULT_PORT 传了端口，这里就把 config.ini 的 listen 对齐过去——
#    映射端口是唯一权威，config.ini 里的旧值不再能造成事故。
#
# 已经存在 config.ini 时**只改 listen 这一行**，其余内容（含管理员账号）不动。
# =============================================================================
set -eu

CONFIG=/data/config.ini
SPACE=/data/HomeVaultSpace
UID_NOW=$(id -u)
GID_NOW=$(id -g)

# 读 config.ini 里某个 key 的值，去掉引号与行尾注释。取不到就输出空串。
conf_value() {
    [ -f "$CONFIG" ] || return 0
    sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//p" "$CONFIG" \
        | head -n 1 \
        | sed 's/^"//; s/"[[:space:]]*$//; s/[[:space:]]*[#;].*$//'
}

if [ ! -f "$CONFIG" ]; then
    echo "[entrypoint] 未找到 $CONFIG，按容器默认值创建（space_dir=$SPACE）"
    cat > "$CONFIG" <<'EOF'
# homevault 引导配置（容器内自动生成）
# 绝大多数参数不需要在这里配置：启动后在管理页面修改并立即生效。
# 这里只放"改了就换进程形态"的必须项。
# 含 # 的值请用双引号包起来。改完需要重启容器。

# HTTP 监听地址。容器里必须是 0.0.0.0，否则宿主机端口映射进不来。
# 对外端口由 docker-compose.yml 的 ports 左侧（或 .env 里的 HOST_PORT）决定，
# 容器内端口由 HOMEVAULT_PORT 决定，本文件的值会在启动时被自动对齐。
listen = "0.0.0.0:18090"

# 空间目录：用户文件、回收站、审计日志、上传分片、缓存与元数据都在其中。
# 固定放在数据卷内，容器重建不丢数据。要换到别的盘，请在管理页面改，
# 或把新目录挂进容器后在这里改（改完重启）。
space_dir = "/data/HomeVaultSpace"

# 同时设置下面两项才启用内置 HTTPS；否则建议由反向代理（Caddy / Nginx）终结 TLS。
tls_cert = ""
tls_key = ""

# 首次启动创建管理员用。默认 admin / admin，登录后会强制要求改账号或密码。
# 只有第一次启动（空间目录里还没有 state.json）时读取这两项。
admin_user = "admin"
admin_password = "admin"
EOF
    chmod 0600 "$CONFIG" 2>/dev/null || true

    # ---------------------------------------------------------------------
    # 关键告警：走到这里说明 /data 里没有 config.ini。
    #
    # 对**真正的首次部署**这是正常的。但如果用户以为数据早就在这儿，那说明
    # 挂载的宿主机目录不是原来那个（换过目录名、换过 profile、或在别的路径下
    # compose 过），继续启动会生成一个全新的空库：管理员被重置成 admin/admin，
    # 用户、配额、回收站索引全部"消失"。数据其实还在旧目录里，只是没挂上。
    #
    # 所以这里必须把话说透，让人有机会停下来检查，而不是默默跑起来。
    # ---------------------------------------------------------------------
    if [ ! -e /data/HomeVaultSpace/state.json ]; then
        echo "==================================================================" >&2
        echo "[entrypoint] 注意：当前数据卷里没有 state.json，将被视为**首次部署**。" >&2
        echo "[entrypoint] 将用下面的管理员账号初始化：admin / admin（登录后强制改密）。" >&2
        echo "" >&2
        echo "[entrypoint] 如果你原本已经有用户、文件和配额设置，那就说明容器" >&2
        echo "[entrypoint] **没有挂到原来的数据目录**，现在跑的是一个全新的空库。" >&2
        echo "[entrypoint] 旧数据不会丢，但请立刻按下面几步确认，别在这个空库上继续操作：" >&2
        echo "[entrypoint]   1) 宿主机上执行 docker inspect $(hostname 2>/dev/null || echo '<容器名>') 看 .Mounts 的实际路径" >&2
        echo "[entrypoint]   2) 宿主机上执行 find / -name state.json 2>/dev/null 找旧库" >&2
        echo "[entrypoint]   3) 把正确的宿主机目录挂到 /data（改 compose 的 volumes 或 .env）" >&2
        echo "[entrypoint] 确认就是全新部署的话，忽略本段继续使用即可。" >&2
        echo "==================================================================" >&2
    fi
else
    echo "[entrypoint] 使用已存在的 $CONFIG（不覆盖内容）"
fi

# ---------------------------------------------------------------------------
# 端口对齐：compose 通过 HOMEVAULT_PORT 传来"容器内该监听的端口"。
# 只在文件里那一行确实不同的时候才改写，并明确打印改了什么。
# ---------------------------------------------------------------------------
PORT="${HOMEVAULT_PORT:-}"
if [ -n "$PORT" ]; then
    CUR=$(conf_value listen)
    WANT="0.0.0.0:$PORT"
    if [ "$CUR" != "$WANT" ]; then
        if sed -i "s|^[[:space:]]*listen[[:space:]]*=.*|listen = \"$WANT\"|" "$CONFIG" 2>/dev/null; then
            echo "[entrypoint] listen 已对齐到 $WANT（原值：${CUR:-未设置}），由 HOMEVAULT_PORT 决定"
        else
            echo "[entrypoint] 警告：无法改写 $CONFIG 的 listen（权限不足？）" >&2
        fi
    fi
else
    echo "[entrypoint] 未提供 HOMEVAULT_PORT，沿用 config.ini 里的 listen（非 compose 启动属于正常）"
fi

LISTEN=$(conf_value listen)
SPACE_CONF=$(conf_value space_dir)
[ -n "$SPACE_CONF" ] && SPACE=$SPACE_CONF

echo "[entrypoint] 运行用户 uid=$UID_NOW gid=$GID_NOW，listen=${LISTEN:-<默认>}，space_dir=$SPACE"

# 数据卷属主可能是宿主机上别的 uid（bind mount 常见），尽量保证可写。
# 失败不阻塞启动——多数情况下目录已经是对的，只是没法 chown。
mkdir -p "$SPACE" 2>/dev/null || true
if [ ! -w "$SPACE" ]; then
    echo "[entrypoint] 警告：$SPACE 当前用户不可写。" >&2
    echo "[entrypoint] 请在宿主机执行：sudo chown -R $UID_NOW:$GID_NOW <宿主机数据目录>" >&2
fi

exec /homevault serve -config "$CONFIG" "$@"
