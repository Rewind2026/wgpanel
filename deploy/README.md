# 部署形态

WGPanel 是单二进制 + 单数据卷，因此各种环境都能落。
选哪种只取决于你已经有了什么，不是取决于功能差异——功能完全一致。

| 形态 | 目录 | 适用场景 |
|---|---|---|
| Docker Compose | [`../docker-compose.host.yml`](../docker-compose.host.yml)（另有 ipvlan / macvlan 两份 L2 文件） | 单机自建，最常见 |
| Helm | [`helm/wgpanel`](helm/wgpanel) | 已有 K8s 集群 |
| Kustomize | [`kustomize/`](kustomize) | 已有裸 manifest + 不想引入 Helm |
| systemd | [`systemd/wgpanel.service`](systemd/wgpanel.service) | 裸机 / 无容器环境 |
| 反代与 TLS | [`reverse-proxy/README.md`](reverse-proxy/README.md) | 面板要暴露到公网 |

K8s 与 systemd 形态使用 `WGP_NATIVE_MODE=managed`（自管）：进程在自己的
netns 内完成转发、路由与 NAT。Docker 的 bridge 部署模式已移除
（bridge 网络拿不到路由器 RA，容器 IPv6 无法直连）。

## 所有形态共通的三件事

1. **节点要有 `wireguard` 内核模块**：`sudo modprobe wireguard`。
   没有模块时面板能启动、能改配置，但建不出任何隧道，`/readyz` 会返回 503。
2. **进程要有 `NET_ADMIN`**。容器靠 `cap_add`，systemd 靠 `AmbientCapabilities`。
   除此之外的能力一个都不给：`SYS_ADMIN`/`SYS_MODULE` 等价于宿主 root。
3. **数据目录要持久化**。里面三个文件缺一不可：

   ```
   wgpanel.db       SQLite（WAL 模式，含 WAL 与 SHM 副文件）
   master.key       私钥加密主密钥
   runtime.secret   JWT 签名密钥
   ```

   `master.key` 丢了，库里所有私钥都无法解密，只能重建所有客户端。

## 副本数

**只能跑一个。** WireGuard 接口是宿主唯一资源，两个实例会互相删除对方的接口，
路由表和 NAT 规则也会打架。Helm 的 `values.schema.json` 把 `replicaCount` 限制为 1，
Kustomize 里也写死 `replicas: 1` —— 这是硬约束，不是默认建议。

## 备份

面板内「系统运维」页可以一键备份，产物是 AES-256-GCM 加密的逻辑备份，
包含数据库、密钥与配置，且**不含私钥明文**。也可以在宿主机上做物理备份：

```bash
docker run --rm \
  -v wgpanel-data:/data:ro \
  -v "$PWD:/backup" \
  alpine tar czf /backup/wgpanel-$(date +%F).tar.gz -C /data .
```

SQLite 开了 WAL，直接 `cp /data` 可能拿到撕裂的快照。
上面的 `tar` 在只读挂载下同样有这个风险，严谨做法是停容器后再打包，
或用 `sqlite3 /data/wgpanel.db ".backup /backup/wgpanel.db"`。

## 升级

```bash
# compose（按实际使用的模式文件替换 -f 参数）
docker compose -f docker-compose.host.yml pull && docker compose -f docker-compose.host.yml up -d

# helm
helm upgrade wgpanel deploy/helm/wgpanel --set image.tag=v0.11.0

# systemd
systemctl stop wgpanel && cp wgpanel /usr/local/bin/ && systemctl start wgpanel
```

数据库迁移在启动时自动执行，不需要手动干预。
升级前建议先做一次备份——迁移一旦写入新 schema，回退旧版本会读不了。