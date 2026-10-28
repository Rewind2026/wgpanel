export type Role = 'owner' | 'admin' | 'operator' | 'viewer'

export type Permission =
  | 'server:read' | 'server:write'
  | 'peer:read' | 'peer:create' | 'peer:update' | 'peer:delete' | 'peer:revoke'
  | 'metrics:read'
  | 'config:export' | 'config:import'
  | 'audit:read'
  | 'settings:write' | 'system:shutdown'

export interface User {
  id: number
  username: string
  email?: string
  role: Role
  enabled: boolean
  lastLoginAt?: string
  createdAt: string
  updatedAt: string
}

export interface Session {
  accessToken: string
  expiresIn: number
  user: User
}

export interface Me {
  userId: number
  username: string
  role: Role
  viaApi: boolean
  permissions: Permission[]
  /** 部署网络形态：host | ipvlan | macvlan | managed（自管），与后端 netdriver.Mode 对应。 */
  netMode?: string
}

export interface ServerConfig {
  interface: string
  listenPort: number
  privateKey?: string
  publicKey: string
  presharedKey?: string
  addressCidr: string
  mtu: number
  fwmark: number
  table: string
  allowedIps: string
  dns: string
  listenIpv4: string
  listenIpv6: string
  replacePeers: boolean
  saveConfig: boolean
  enableIpv6: boolean
  persistentKeepalive: number
  revision: number
}

export type QuotaPeriod = 'daily' | 'weekly' | 'monthly' | 'never'

export interface PeerStats {
  peerId: number
  publicKey: string
  online: boolean
  allowedIps: string[]
  rxBytes: number
  txBytes: number
  rxRateBps: number
  txRateBps: number
  lastHandshake: string
  connectedSince: string
  connectedSeconds: number
}

export interface Peer {
  id: number
  name: string
  publicKey: string
  privateKey?: string
  presharedKey?: string
  address: string
  allowedIps: string
  endpoint?: string
  persistentKeepalive: number
  note?: string
  tags: string[]
  trafficQuotaBytes: number
  quotaUsedBytes: number
  quotaPeriod: QuotaPeriod
  rateLimitMbps: number
  maxConnections: number
  enabled: boolean
  /** 停用来源：空串为人工停用，quota/expired 为策略自动停用。 */
  suspendedBy?: 'quota' | 'expired' | ''
  expiresAt?: string
  lastSeenAt?: string
  createdAt: string
  updatedAt: string
  stats?: PeerStats
}

export interface SystemMetrics {
  cpuPercent: number
  memUsedBytes: number
  memPercent: number
  diskUsedBytes: number
  diskPercent: number
  netRxBps: number
  netTxBps: number
  netRxBytes: number
  netTxBytes: number
  uptimeSeconds: number
  goroutines: number
}

export interface TunnelMetrics {
  interface: string
  listenPort: number
  publicEndpoint: string
  rxBytes: number
  txBytes: number
  rxRateBps: number
  txRateBps: number
  onlinePeers: number
  totalPeers: number
  interfaceUp: boolean
  collectedAt: string
}

export interface MetricsSnapshot {
  system: SystemMetrics
  tunnel: TunnelMetrics
  peers: PeerStats[]
}

export interface AuditEvent {
  id: number
  actorId?: number
  actorName: string
  action: string
  resource?: string
  resourceId?: string
  detail?: string
  ip?: string
  userAgent?: string
  result: 'success' | 'failure'
  createdAt: string
}

export interface PageResult<T> {
  items: T[]
  total: number
  page: number
  pageSize: number
}

/** 后端统一响应包络。 */
export interface Envelope<T> {
  code: number
  message: string
  data: T
}

/* ---------- 设置、通知、备份、批量 ---------- */

export interface Settings {
  sampleIntervalSeconds: number
  reconcileIntervalSeconds: number
  quotaEnforcement: boolean
  expiryEnforcement: boolean
  expiryWarnDays: number
  backupEnabled: boolean
  backupIntervalHours: number
  backupRetentionCount: number
  backupDir: string
  publicEndpoint: string
  updatedAt: string
}

/** 单个网络接口的地址快照（/api/system/network）。 */
export interface NetInterface {
  name: string
  addrs: string[]
  v4: string[]
  v6Global: string[]
}

/** 上行接口的 IPv6 RA/SLAAC 链路状态；数值 -1 表示读取失败。 */
export interface RAStatus {
  interface: string
  disableIPv6: number
  acceptRA: number
  forwarding: number
  writable: boolean
  /** false 表示当前模式下 RA 链路不归面板管（host/managed 归宿主网络栈）。 */
  applicable: boolean
  globalV6: string[]
  problems?: string[]
  applied?: string[]
}

/** 网络形态诊断（/api/system/network）。 */
export interface NetworkInfo {
  mode: string
  uplink: string
  interfaces: NetInterface[]
  ra: RAStatus
  endpoint: string
}

export interface BackupRecord {
  id: number
  filename: string
  sizeBytes: number
  sha256: string
  schemaVersion: number
  peerCount: number
  userCount: number
  kind: string
  operatorId?: number
  createdAt: string
}

export interface ImportRowError {
  line: number
  name?: string
  reason: string
}

export interface ImportResult {
  created: number
  skipped: number
  failed: number
  errors: ImportRowError[]
}

export interface SubscriptionLink {
  peerId: number
  token: string
  url: string
  issuedAt: string
}

/** 服务端配置的历史版本摘要，后端只返回这几项。 */
export interface ServerRevision {
  revision: number
  interface: string
  listenPort: number
  mtu: number
  updatedAt: string
}

export type TrafficRange = '1h' | '24h' | '7d' | '30d'

export interface TrafficPoint {
  /** 桶起点，Unix 秒。 */
  ts: number
  rxBytes: number
  txBytes: number
  rxRate: number
  txRate: number
}

export interface TrafficSeries {
  range: TrafficRange
  bucket: string
  from: number
  to: number
  points: TrafficPoint[]
}

export interface MetricsOverview {
  system: SystemMetrics
  tunnel: TunnelMetrics
  interval: string
}

/** SSE 推送的领域事件，与后端 domain.AllEventTypes() 对应。 */
export type DomainEventType =
  | 'peer.created' | 'peer.updated' | 'peer.deleted' | 'peer.key_rotated'
  | 'peer.online' | 'peer.offline' | 'peer.quota_exceeded' | 'peer.expiring'
  | 'peer.expired' | 'peer.restored' | 'server.updated'
  | 'backup.created' | 'restore.applied'

/**
 * 事件名全集，SSE 监听器的注册清单。
 *
 * 单点维护是这个列表唯一可靠的维护方式：后端新增事件后，
 * 这里漏一个就是「后端在推、浏览器不收」，界面毫无异常，只是更新静默消失。
 */
export const DOMAIN_EVENT_TYPES: readonly DomainEventType[] = [
  'peer.created',
  'peer.updated',
  'peer.deleted',
  'peer.key_rotated',
  'peer.online',
  'peer.offline',
  'peer.quota_exceeded',
  'peer.expiring',
  'peer.expired',
  'peer.restored',
  'server.updated',
  'backup.created',
  'restore.applied',
] as const

export interface DomainEvent {
  type: DomainEventType
  peerId?: number
  peerName?: string
  message: string
  occurredAt: string
  data?: Record<string, unknown>
}