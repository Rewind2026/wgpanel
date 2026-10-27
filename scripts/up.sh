#!/usr/bin/env bash
# up.sh —— 按网络模式启动 wgpanel
#
# 四种模式 = 四份完整独立的 compose 文件（互不叠加，任何 Compose 版本可用）：
#   bridge   docker-compose.yml            默认（`docker compose up -d` 零改动）
#   host     docker-compose.host.yml
#   ipvlan   docker-compose.ipvlan.yml
#   macvlan  docker-compose.macvlan.yml
#
# 用法：
#   ./scripts/up.sh                                  # bridge（默认）
#   WGP_NETWORK_MODE=macvlan ./scripts/up.sh         # macvlan
#   WGP_NETWORK_MODE=ipvlan  ./scripts/up.sh         # ipvlan
#   WGP_NETWORK_MODE=host    ./scripts/up.sh         # host
#
# 模式来源优先级：shell 环境变量 > .env 的 WGP_NETWORK_MODE > bridge。
# L2（ipvlan/macvlan）参数（WGP_L2_PARENT/SUBNET/GATEWAY/IPV4）同样支持
# shell 环境变量与 .env；两处都没写时，compose 文件顶部的 x-l2-* 三行兜底。
#
# 本脚本在 docker 之前完成三类前置校验（daemon 拦不住或报错难懂的问题）：
#   1. L2 必填参数齐全、合法（parent≠lo、子网是 IPv4 CIDR、网关落在子网内）
#   2. 固定 IPv4 不与宿主已有地址冲突（撞地址会抢 ARP，宿主入站全断）
#   3. ipvlan parent 独占性预检（同网卡上已有 ipvlan 网络时明确报错）

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "$PROJECT_DIR"

if [ -t 1 ]; then
  GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
else
  GREEN=''; YELLOW=''; RED=''; NC=''
fi
log()  { echo -e "[up.sh] $*" >&2; }
ok()   { echo -e "${GREEN}[up.sh] ✓${NC} $*" >&2; }
warn() { echo -e "${YELLOW}[up.sh] !${NC} $*" >&2; }
die()  { echo -e "${RED}[up.sh] ✗ ${*}${NC}" >&2; exit 1; }

# ---------- 参数 ----------
for arg in "$@"; do
  case "$arg" in
    -h|--help)
      sed -n '2,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *)
      die "未知参数：$arg（多余参数请直接用 docker compose 传递）"
      ;;
  esac
done

# ---------- 读 .env（只 grep 需要的键，不整体 source，避免注入） ----------
env_get() {
  [ -f ./.env ] || return 0
  sed -nE "s/^[[:space:]]*(export[[:space:]]+)?$1[[:space:]]*=[[:space:]]*(.*)$/\2/p" ./.env \
    | tail -n1 \
    | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' \
           -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'\$/\1/"
}

[ -n "${WGP_NETWORK_MODE:-}" ] || WGP_NETWORK_MODE="$(env_get WGP_NETWORK_MODE)"
NETWORK_MODE="$(echo "${WGP_NETWORK_MODE:-bridge}" | tr '[:upper:]' '[:lower:]' | xargs)"
[ -n "$NETWORK_MODE" ] || NETWORK_MODE="bridge"

# L2 变量：读到就 export，让 compose 的 ${...} 插值与本脚本取值一致。
for _k in WGP_L2_PARENT WGP_L2_SUBNET WGP_L2_GATEWAY WGP_L2_IPV4 \
         WGP_L2_NETWORK_NAME WGP_MACVLAN_MODE WGP_L2_EXTERNAL; do
  if [ -z "${!_k:-}" ]; then
    declare "$_k=$(env_get "$_k")"
    export "$_k"
  fi
done

case "$NETWORK_MODE" in
  bridge)  COMPOSE_FILE="docker-compose.yml" ;;
  host)    COMPOSE_FILE="docker-compose.host.yml" ;;
  ipvlan)  COMPOSE_FILE="docker-compose.ipvlan.yml" ;;
  macvlan) COMPOSE_FILE="docker-compose.macvlan.yml" ;;
  *) die "WGP_NETWORK_MODE=${NETWORK_MODE} 不合法：仅支持 bridge | host | ipvlan | macvlan" ;;
esac
[ -f "$COMPOSE_FILE" ] || die "$COMPOSE_FILE 不存在（请确认在项目根目录执行）"

log "网络模式: ${NETWORK_MODE}（${COMPOSE_FILE}）"

# ---------- L2 前置校验 ----------
if [ "$NETWORK_MODE" = "ipvlan" ] || [ "$NETWORK_MODE" = "macvlan" ]; then
  [ -n "${WGP_L2_PARENT:-}" ]   || die "缺少 WGP_L2_PARENT：请填宿主父接口名（如 ens18）"
  [ -n "${WGP_L2_SUBNET:-}" ]   || die "缺少 WGP_L2_SUBNET：请填宿主所在 IPv4 子网（如 192.168.50.0/24）"
  [ -n "${WGP_L2_GATEWAY:-}" ]  || die "缺少 WGP_L2_GATEWAY：请填网关（如 192.168.50.1）"

  [ "${WGP_L2_PARENT}" != "lo" ] \
    || die "WGP_L2_PARENT 不能是 lo"

  # 子网必须是 IPv4 CIDR（IPv6 走 RA/SLAAC，不经 Docker IPAM）。
  echo "$WGP_L2_SUBNET" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]{1,2}$' \
    || die "WGP_L2_SUBNET=${WGP_L2_SUBNET} 不是合法 IPv4 CIDR（如 192.168.50.0/24）"

  # 网关必须落在子网内。
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$WGP_L2_SUBNET" "$WGP_L2_GATEWAY" <<'PY' || die "WGP_L2_GATEWAY 不在 WGP_L2_SUBNET 网段内"
import ipaddress, sys
net = ipaddress.ip_network(sys.argv[1])
gw = ipaddress.ip_address(sys.argv[2])
sys.exit(0 if gw in net else 1)
PY
  fi

  # ipvlan parent 独占：同网卡上已有一条 ipvlan 网络时直接报清楚。
  if [ "$NETWORK_MODE" = "ipvlan" ] && command -v docker >/dev/null 2>&1; then
    for _n in $(docker network ls --filter driver=ipvlan --format '{{.Name}}' 2>/dev/null); do
      _p="$(docker network inspect "$_n" --format '{{index .Options "parent"}}' 2>/dev/null || true)"
      if [ "$_p" = "${WGP_L2_PARENT}" ]; then
        die "parent ${WGP_L2_PARENT} 上已存在 ipvlan 网络『$_n』（ipvlan 的 parent 独占）。复用它：设置 WGP_L2_NETWORK_NAME=$_n"
      fi
    done
  fi

  # 固定 IPv4 冲突预检：daemon 只拒「不在子网内」，不拒「宿主正在用」；
  # 撞上宿主地址容器会抢 ARP，宿主所有入站连接直接断（实测 SSH 被 reset）。
  if [ -n "${WGP_L2_IPV4:-}" ] && command -v ip >/dev/null 2>&1; then
    if ip -o -4 addr show 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | grep -Fxq "${WGP_L2_IPV4}"; then
      die "WGP_L2_IPV4=${WGP_L2_IPV4} 与宿主已有地址冲突，拒绝启动（会抢走宿主 ARP 导致宿主失联）"
    fi
  fi

  if [ -n "${WGP_L2_IPV4:-}" ]; then
    warn "容器固定 IPv4: ${WGP_L2_IPV4}"
  else
    warn "容器固定 IPv4: (未设置，由 Docker 自动分配)"
  fi
  warn "上行接口 IPv6 将由路由器 RA/SLAAC 下发（无需配置 v6 子网）"
  warn "切换网络模式必须重建容器，本次会自动 recreate"
fi

# ---------- 建网（仅当 external 且网络不存在时） ----------
# external=true 表示「网络由本脚本管理」：不存在则按 compose 同款参数创建
# （带 com.docker.compose.network label，保证纯 compose 路径日后可复用）；
# 已存在则只复用不修改 —— 那条网络可能是别的项目正在用的。
if { [ "$NETWORK_MODE" = "ipvlan" ] || [ "$NETWORK_MODE" = "macvlan" ]; } \
   && [ "${WGP_L2_EXTERNAL:-false}" = "true" ]; then
  _netname="${WGP_L2_NETWORK_NAME:-$NETWORK_MODE}"
  _driver="$NETWORK_MODE"
  _extra=()
  [ -n "${WGP_MACVLAN_MODE:-}" ] && [ "$_driver" = "macvlan" ] && _extra+=(--opt "macvlan_mode=${WGP_MACVLAN_MODE}")
  if ! docker network inspect "$_netname" >/dev/null 2>&1; then
    log "创建共享网络 '$_netname'（driver=$_driver parent=${WGP_L2_PARENT} subnet=${WGP_L2_SUBNET} gateway=${WGP_L2_GATEWAY}）"
    # 不带 --ipv6：IPv6 完全交给上游路由器 RA + 容器 SLAAC。
    # 带了 Docker 会自动分一个 fd00::/64 ULA 段，污染选路。
    docker network create --driver "$_driver" \
      --opt "parent=${WGP_L2_PARENT}" "${_extra[@]+"${_extra[@]}"}" \
      --label "com.docker.compose.network=l2" \
      --subnet "${WGP_L2_SUBNET}" --gateway "${WGP_L2_GATEWAY}" "$_netname" \
      || die "创建共享网络 '$_netname' 失败（检查 parent 名、子网是否与既有 L2 网络重叠）"
    ok "共享网络 '$_netname' 已创建"
  else
    ok "共享网络 '$_netname' 已存在（复用，不修改）"
  fi
fi

# ---------- compose up ----------
# 实际命令原样打出来，避免「up.sh 到底跑了什么」的黑盒感。
log "running: docker compose -f ${COMPOSE_FILE} up -d"
exec docker compose -f "$COMPOSE_FILE" up -d
