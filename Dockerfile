# syntax=docker/dockerfile:1

########## Stage 1: 前端构建 ##########
FROM node:22-alpine AS web

WORKDIR /src/web

# 依赖层单独缓存：只有 lockfile 变化时才重装依赖
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund || npm install --no-audit --no-fund

COPY web/ ./
RUN npm run build


########## Stage 2: 后端静态编译 ##########
# 基础镜像版本必须 >= go.mod 的 go 指令。go.mod 要求 1.26 时，
# 用 1.24 的镜像会因 GOTOOLCHAIN=local 直接报
# "go.mod requires go >= 1.26.0"，连 go mod download 都过不去。
FROM golang:1.26-alpine AS api

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 把 web 阶段的前端产物复制进 Go 的 embed 目录。
# 必须在 COPY . . 之后执行，否则会被上下文中的占位目录覆盖。
COPY --from=web /src/internal/web/dist ./internal/web/dist

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG TARGETVARIANT=

# CGO_ENABLED=0 是关键：产出纯静态二进制，运行时可用最小基础镜像。
# 前端产物已在上面的 COPY 中就位，由 go:embed 编译进二进制。
# 交叉编译在此显式指定 GOOS/GOARCH，让 arm64 与 armv7 走同一份 Dockerfile。
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM="${TARGETVARIANT#v}" \
    go build \
      -trimpath \
      -ldflags "-s -w \
        -X github.com/Rewind2026/wgpanel/internal/version.Version=${VERSION} \
        -X github.com/Rewind2026/wgpanel/internal/version.Commit=${COMMIT} \
        -X github.com/Rewind2026/wgpanel/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/wgpanel ./cmd/wgpanel


########## Stage 3: 运行时 ##########
FROM alpine:3.20

# iptables/nft 供 netdriver 下发 NAT/MSS 规则，iproute2 供链路诊断；
# wireguard-tools 仅在需要 wg-quick 互操作时使用，面板自身不依赖。
RUN apk add --no-cache \
      ca-certificates \
      tzdata \
      iptables \
      ip6tables \
      nftables \
      iproute2 \
      wireguard-tools \
    && addgroup -g 10001 wg \
    && adduser -u 10001 -G wg -D -H wg \
    && mkdir -p /data \
    && chown -R 10001:10001 /data

COPY --from=api /out/wgpanel /usr/local/bin/wgpanel

ENV WGP_DATA_DIR=/data \
    WGP_LISTEN_ADDR=0.0.0.0:8080 \
    TZ=UTC

# /data 持久化：SQLite、主密钥、运行时密钥。
# 命名卷会继承镜像内该目录的属主，因此属主固定为 10001:10001，
# 需要以非 root 身份运行的使用方无需额外准备。
VOLUME ["/data"]

# 面板管理端口（HTTP）与 WireGuard 隧道端口（UDP）
EXPOSE 8080/tcp
EXPOSE 51820/udp

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

# 以 root 运行，但能力集被压到最小。
#
# 为什么不是非 root：实测（Docker 28.5，Linux 6.18）`--cap-add NET_ADMIN`
# 配合 `--user 10001` 时，容器内 CapEff 为 0——Docker 只把能力放进 bounding
# 集合，非 root 进程不继承，wg 与 iptables 操作一律 EPERM。
# 非 root + cap_add 在实践中并不可靠，root + cap_drop ALL 才是真正有效的收窄。
#
# 因此使用方必须显式收窄能力集，缺一不可：
#   NET_ADMIN     创建/配置 wg 接口、地址、路由与 NAT 规则
#   DAC_OVERRIDE  以 root 身份读写属主为 10001 的数据卷（SQLite 需要）
# 少任何一个，面板都会在启动阶段失败。
USER 0:0

ENTRYPOINT ["/usr/local/bin/wgpanel"]