#!/bin/sh
# =============================================================================
# homevault 容器入口脚本
#
# 解决一个真实的部署陷阱：
#
# Dockerfile 里在构建阶段就把 /data/config.ini 写好了，但 `VOLUME /data` +
# 宿主机 bind mount 会在运行时**用宿主目录覆盖整个 /data**，构建期写进去的
# config.ini 就被盖掉了。
#
# 此时二进制发现 config.ini 不存在，会自动写出一份模板——而模板里
# `space_dir = ""` 会解析成 /root/HomeVaultSpace。容器以非 root 用户
# （uid 10001 或自定义的 APP_UID）运行，无权在 /root 下建目录，直接启动失败。
#
# 所以这里在启动前补一份"容器场景正确"的配置：space_dir 指向数据卷内的
# /data/HomeVaultSpace，保证数据一定落在挂载卷里，且属主是当前用户。
#
# 已经存在 config.ini 时**绝不覆盖**，你自己改过的端口、管理员账号都会保留。
# =============================================================================
set -eu

CONFIG=/data/config.ini
SPACE=/data/HomeVaultSpace

if [ ! -f "$CONFIG" ]; then
    echo "[entrypoint] 未找到 $CONFIG，按容器默认值创建（space_dir=$SPACE）"
    cat > "$CONFIG" <<'EOF'
# homevault 引导配置（容器内自动生成）
# 绝大多数参数不需要在这里配置：启动后在管理页面修改并立即生效。
# 这里只放"改了就换进程形态"的必须项。
# 含 # 的值请用双引号包起来。改完需要重启容器。

# HTTP 监听地址。容器里必须是 0.0.0.0，否则宿主机端口映射进不来。
# 要改对外端口请改 docker-compose.yml 的 ports 左侧，不必改这里。
listen = "0.0.0.0:8080"

# 空间目录：用户文件、回收站、审计日志、上传分片、缓存与元数据都在其中。
# 固定放在数据卷内，容器重建不丢数据。
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
else
    echo "[entrypoint] 使用已存在的 $CONFIG（不覆盖）"
fi

# 数据卷属主可能是宿主机上别的 uid（bind mount 常见），尽量保证可写。
# 失败不阻塞启动——多数情况下目录已经是对的，只是没法 chown。
mkdir -p "$SPACE" 2>/dev/null || true
if [ ! -w "$SPACE" ]; then
    echo "[entrypoint] 警告：$SPACE 当前用户不可写。" >&2
    echo "[entrypoint] 请在宿主机执行：sudo chown -R $(id -u):$(id -g) <宿主机数据目录>" >&2
fi

exec /homevault serve -config "$CONFIG" "$@"
