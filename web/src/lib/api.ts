import type {
  AuditEvent,
  BackupRecord,
  Envelope,
  ImportResult,
  Me,
  MetricsOverview,
  NetworkInfo,
  PageResult,
  Peer,
  PeerStats,
  ServerConfig,
  ServerRevision,
  Session,
  Settings,
  SubscriptionLink,
  TrafficRange,
  TrafficSeries,
  User,
} from '@/types/api'

/** access token 保存在内存而非 localStorage，降低 XSS 后的令牌泄露风险。 */
let accessToken: string | null = null

export function setAccessToken(token: string | null) {
  accessToken = token
}

export function getAccessToken() {
  return accessToken
}

/** ApiError 携带 HTTP 状态与后端业务错误码，便于上层区分处理。 */
export class ApiError extends Error {
  readonly status: number
  readonly code: number

  constructor(status: number, code: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

/** 令牌失效时的回调，由 AuthProvider 注册以触发跳转登录页。 */
let onUnauthorized: (() => void) | null = null

export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn
}

type RequestOptions = {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  body?: unknown
  signal?: AbortSignal
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  if (accessToken) headers['Authorization'] = `Bearer ${accessToken}`

  const res = await fetch(`/api/v1${path}`, {
    method: opts.method ?? 'GET',
    headers,
    credentials: 'include',
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
    signal: opts.signal,
  })

  if (res.status === 204) return undefined as T

  const text = await res.text()
  let payload: Envelope<T> | null = null
  if (text) {
    try {
      payload = JSON.parse(text) as Envelope<T>
    } catch {
      payload = null
    }
  }

  if (!res.ok) {
    if (res.status === 401) onUnauthorized?.()
    throw new ApiError(
      res.status,
      payload?.code ?? res.status,
      payload?.message || `请求失败（HTTP ${res.status}）`,
    )
  }
  return (payload?.data ?? (undefined as unknown as T)) as T
}

/**
 * download 拉取二进制/文本附件并触发浏览器保存。
 *
 * 这些端点（CSV 导出、审计导出、备份文件）返回的不是 JSON 包络，
 * 走 request() 会因为解析失败拿不到内容，故单独一条路径。
 */
async function download(path: string, fallbackName: string): Promise<void> {
  const headers: Record<string, string> = { Accept: '*/*' }
  if (accessToken) headers['Authorization'] = `Bearer ${accessToken}`

  const res = await fetch(`/api/v1${path}`, {
    headers,
    credentials: 'include',
  })
  if (!res.ok) {
    if (res.status === 401) onUnauthorized?.()
    throw new ApiError(res.status, res.status, `下载失败（HTTP ${res.status}）`)
  }

  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = res.headers.get('Content-Disposition')?.match(/filename="?([^";]+)"?/)?.[1]
    ?? fallbackName
  document.body.appendChild(a)
  a.click()
  a.remove()
  // 立刻回收会让部分浏览器的下载中断，故延后释放。
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
}

/** 把 CSV 原文作为文件保存，用于导入前的模板下载。 */
export function saveTextFile(name: string, content: string, mime = 'text/csv;charset=utf-8') {
  const blob = new Blob(['﻿' + content], { type: mime })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') q.set(k, String(v))
  }
  const s = q.toString()
  return s ? `?${s}` : ''
}

export const api = {
  login: (username: string, password: string) =>
    request<Session>('/auth/login', { method: 'POST', body: { username, password } }),

  refresh: () => request<Session>('/auth/refresh', { method: 'POST' }),

  logout: () => request<{ loggedOut: boolean }>('/auth/logout', { method: 'POST' }),

  me: () => request<Me>('/auth/me'),

  version: (signal?: AbortSignal) =>
    request<{ version: string; commit?: string; builtAt?: string }>('/version', { signal }),

  getServer: (signal?: AbortSignal) => request<ServerConfig>('/server', { signal }),

  updateServer: (payload: Partial<ServerConfig>) =>
    request<{ revision: number }>('/server', { method: 'PUT', body: payload }),

  resetServerKey: () =>
    request<{ publicKey: string; revision: number }>('/server/reset-key', { method: 'POST' }),

  /**
   * 服务端 wg0.conf 全文。该端点返回的是 text/plain 而非 JSON 包络，
   * 走 request() 会解析失败，故单独按文本读取。
   */
  serverConfig: async (signal?: AbortSignal) => {
    const headers: Record<string, string> = { Accept: 'text/plain' }
    if (accessToken) headers['Authorization'] = `Bearer ${accessToken}`
    const res = await fetch('/api/v1/server/config', { headers, credentials: 'include', signal })
    if (!res.ok) {
      if (res.status === 401) onUnauthorized?.()
      throw new ApiError(res.status, res.status, `读取服务端配置失败（HTTP ${res.status}）`)
    }
    return res.text()
  },

  serverConfigDownload: () => download('/server/config?download=1', 'wgpanel.conf'),

  serverRevisions: (limit = 20, signal?: AbortSignal) =>
    request<ServerRevision[]>(`/server/revisions${query({ limit })}`, { signal }),

  listPeers: (signal?: AbortSignal) => request<Peer[]>('/peers', { signal }),

  createPeer: (payload: Partial<Peer>) => request<Peer>('/peers', { method: 'POST', body: payload }),

  updatePeer: (id: number, payload: Partial<Peer>) =>
    request<Peer>(`/peers/${id}`, { method: 'PUT', body: payload }),

  deletePeer: (id: number) => request<{ deleted: number }>(`/peers/${id}`, { method: 'DELETE' }),

  rotatePeerKey: (id: number) =>
    request<{ peerId: number; publicKey: string }>(`/peers/${id}/rotate-key`, { method: 'POST' }),

  peerConfig: async (id: number, endpoint?: string) => {
    const res = await request<{ config: string }>(`/peers/${id}/config${query({ endpoint })}`)
    return res.config
  },

  issueSubscription: (id: number) =>
    request<SubscriptionLink>(`/peers/${id}/subscription`, { method: 'POST' }),

  revokeSubscription: (id: number) =>
    request<{ revoked: number }>(`/peers/${id}/subscription`, { method: 'DELETE' }),

  bulkRevoke: (ids: number[]) =>
    request<{ revoked: number }>('/peers/bulk-revoke', { method: 'POST', body: { ids } }),

  /** 全量吊销：服务端遍历库内全部 Peer，不依赖前端勾选态。 */
  revokeAllPeers: () =>
    request<{ revoked: number }>('/peers/bulk-revoke', { method: 'POST', body: { all: true } }),

  bulkTag: (ids: number[], tag: string, remove: boolean) =>
    request<{ changed: number }>('/peers/bulk-tag', {
      method: 'POST', body: { ids, tag, remove },
    }),

  bulkEnable: (ids: number[], enabled: boolean) =>
    request<{ changed: number }>('/peers/bulk-enable', {
      method: 'POST', body: { ids, enabled },
    }),

  exportPeersCSV: () => download('/peers/export', 'wgpanel-peers.csv'),

  /** CSV 导入走 text/plain 直传，不能包 JSON 包络。 */
  importPeersCSV: async (csv: string) => {
    const headers: Record<string, string> = { 'Content-Type': 'text/csv' }
    if (accessToken) headers['Authorization'] = `Bearer ${accessToken}`
    const res = await fetch('/api/v1/peers/import', {
      method: 'POST', headers, credentials: 'include', body: csv,
    })
    const payload = (await res.json().catch(() => null)) as Envelope<ImportResult> | null
    if (!res.ok) {
      if (res.status === 401) onUnauthorized?.()
      throw new ApiError(res.status, payload?.code ?? res.status,
        payload?.message || `导入失败（HTTP ${res.status}）`)
    }
    return payload?.data as ImportResult
  },

  audit: (params: {
    page?: number; pageSize?: number; action?: string
    resource?: string; result?: string; from?: string; to?: string
  } = {}) => request<PageResult<AuditEvent>>(`/audit${query({ ...params })}`),

  exportAudit: (params: {
    action?: string; resource?: string; result?: string; from?: string; to?: string
  } = {}) => download(`/audit/export${query({ ...params })}`, 'wgpanel-audit.csv'),

  metricsOverview: (signal?: AbortSignal) => request<MetricsOverview>('/metrics/overview', { signal }),

  peerMetrics: (signal?: AbortSignal) => request<PeerStats[]>('/metrics/peers', { signal }),

  traffic: (range: TrafficRange, peerId?: number, signal?: AbortSignal) =>
    request<TrafficSeries>(`/metrics/traffic${query({ range, peerId })}`, { signal }),

  getSettings: (signal?: AbortSignal) => request<Settings>('/settings', { signal }),

  updateSettings: (payload: Partial<Settings>) =>
    request<Settings>('/settings', { method: 'PUT', body: payload }),

  /** 网络形态诊断：模式、接口地址、上行接口与 IPv6 RA 链路状态。 */
  getNetwork: (signal?: AbortSignal) => request<NetworkInfo>('/system/network', { signal }),

  listBackups: (signal?: AbortSignal) => request<BackupRecord[]>('/backups', { signal }),

  createBackup: () => request<BackupRecord>('/backups', { method: 'POST' }),

  restoreBackup: (filename: string) =>
    request<{ restored: string; notice: string }>(`/backups/${encodeURIComponent(filename)}/restore`, {
      method: 'POST',
    }),

  deleteBackup: (filename: string) =>
    request<{ deleted: string }>(`/backups/${encodeURIComponent(filename)}`, { method: 'DELETE' }),

  downloadBackup: (filename: string) =>
    download(`/backups/${encodeURIComponent(filename)}`, filename),

  /**
   * 账号接口只管「我自己」。
   *
   * 面板是单人部署，没有建号、删号与角色分配，
   * 所以这里没有 userId 参数——目标账号一律取自登录态。
   */
  getProfile: (signal?: AbortSignal) => request<User>('/account/profile', { signal }),

  changePassword: (password: string) =>
    request<{ changed: boolean }>('/account/password', { method: 'PUT', body: { password } }),

  /**
   * streamTicket 换取 SSE 连接票据。
   *
   * EventSource 构造器不接受自定义请求头，所以监控页没法像其他
   * 接口那样直接带 Authorization 换取推送流。票据是 60 秒有效、
   * 一次性消费的随机串，泄露到访问日志里也无法重放。
   */
  streamTicket: () => request<{ ticket: string; expiresIn: number }>('/auth/stream-ticket', {
    method: 'POST',
  }),
}