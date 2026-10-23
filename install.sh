#!/usr/bin/env bash
#
# WGPanel 一键安装脚本
# 用途：生成安全的初始账号密码、启动容器、打印访问入口。
#
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/Rewind2026/wgpanel/main/install.sh | sudo bash
#   ./install.sh --image ghcr.io/Rewind2026/wgpanel:v0.1.0 --panel-port 8080 --wg-port 51820
#
set -euo pipefail

IMAGE="ghcr.io/Rewind2026/wgpanel:latest"
CONTAINER_NAME="wgpanel"
VOLUME_NAME="wgpanel-data"
PANEL_PORT="8080"
WG_PORT="51820"
PANEL_BIND="0.0.0.0"

usage() {
  cat <<'EOF'
用法: install.sh [选项]

选项:
  --image <镜像>       容器镜像，默认 ghcr.io/Rewind2026/wgpanel:latest
  --name <容器名>      容器名称，默认 wgpanel
  --panel-port <端口>  面板端口映射，默认 8080
  --wg-port <端口>     WireGuard 端口映射，默认 51820
  --bind <地址>        面板绑定地址，默认 0.0.0.0
  -h, --help           显示帮助
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --image)      IMAGE="$2"; shift 2 ;;
    --name)       CONTAINER_NAME="$2"; shift 2 ;;
    --panel-port) PANEL_PORT="$2"; shift 2 ;;
    --wg-port)    WG_PORT="$2"; shift 2 ;;
    --bind)       PANEL_BIND="$2"; shift 2 ;;
    -h|--help)    usage; exit 0 ;;
    *) echo "未知参数: $1" >&2; usage; exit 1 ;;
  esac
done

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m警告:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m错误:\033[0m %s\n' "$*" >&2; exit 1; }

require_docker() {
  command -v docker >/dev/null 2>&1 || die "未找到 docker，请先安装 Docker Engine。"
  docker info >/dev/null 2>&1 || die "无法连接 Docker 守护进程，请确认当前用户有 docker 权限。"
}

preflight() {
  require_docker

  if [[ ! -c /dev/net/tun ]]; then
    die "/dev/net/tun 不存在，无法创建 WireGuard 接口。
       请确认宿主内核已加载 wireguard 模块：modprobe wireguard"
  fi

  if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    die "容器 $CONTAINER_NAME 已存在。升级请先执行：docker rm -f $CONTAINER_NAME"
  fi
}

detect_panel_url() {
  local ip
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [[ -z "$ip" ]] && ip="127.0.0.1"
  echo "http://${ip}:${PANEL_PORT}"
}

generate_password() {
  # 18 位大小写字母 + 数字 + 符号，满足服务端的强度校验要求。
  LC_ALL=C tr -dc 'A-Za-z0-9@#%^_+=' </dev/urandom | head -c 18
}

start() {
  log "拉取镜像 $IMAGE"
  docker pull "$IMAGE"

  local password
  password="$(generate_password)"

  log "创建数据卷 $VOLUME_NAME"
  docker volume create "$VOLUME_NAME" >/dev/null

  # SYS_MODULE 等价于宿主 root（可加载任意内核模块），因此只在宿主确实没加载
  # wireguard 模块时才授予。已加载的环境不应拿到这个权限。
  local mod_cap=()
  if ! lsmod 2>/dev/null | grep -q '^wireguard'; then
    mod_cap+=(--cap-add SYS_MODULE)
  fi

  log "启动容器 $CONTAINER_NAME"
  docker run -d \
    --name "$CONTAINER_NAME" \
    --restart unless-stopped \
    --cap-drop ALL \
    --cap-add NET_ADMIN \
    ${mod_cap[@]+"${mod_cap[@]}"} \
    --device /dev/net/tun \
    --sysctl net.ipv4.ip_forward=1 \
    --sysctl net.ipv4.conf.all.src_valid_mark=1 \
    -p "${WG_PORT}:51820/udp" \
    -p "${PANEL_BIND}:${PANEL_PORT}:8080/tcp" \
    -v "${VOLUME_NAME}:/data" \
    -e WGP_ADMIN_PASSWORD="${password}" \
    "$IMAGE" >/dev/null

  log "等待面板就绪"
  local i
  for i in $(seq 1 30); do
    if docker exec "$CONTAINER_NAME" wget -q -O- http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done

  local url
  url="$(detect_panel_url)"

  cat <<EOF

$(printf '\033[1;32m')安装完成$(printf '\033[0m')

  面板地址:   ${url}
  VPN 端点:   <你的公网IP>:${WG_PORT}
  初始账号:   admin
  初始密码:   ${password}

$(printf '\033[1;33m')请立即保存该密码——它只在此处显示一次。$(printf '\033[0m')
如需重新登录，可查看启动日志：docker logs ${CONTAINER_NAME} | grep -i password

常用命令:
  查看日志   docker logs -f ${CONTAINER_NAME}
  重启服务   docker restart ${CONTAINER_NAME}
  停止服务   docker stop ${CONTAINER_NAME}
  备份数据   docker run --rm -v ${VOLUME_NAME}:/data -v "\$PWD:/backup" alpine \\
               tar czf /backup/wgpanel-backup.tar.gz -C /data .

EOF
}

preflight
start