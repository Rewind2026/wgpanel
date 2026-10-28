import { createContext, useContext, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '@/lib/api'
import { roleCan } from '@/lib/permissions'
import { useAuth } from '@/components/providers/AuthProvider'
import { DOMAIN_EVENT_TYPES } from '@/types/api'
import type { DomainEvent, MetricsSnapshot } from '@/types/api'

/** 重连退避上限。与 EventSource 自身的重连间隔叠加，避免疯狂打服务端。 */
const RETRY_MAX_MS = 15_000

interface MetricsApi {
  snapshot: MetricsSnapshot | null
  connected: boolean
  lastEvent: DomainEvent | null
  /** 注册领域事件回调，返回注销函数。 */
  subscribe: (fn: (ev: DomainEvent) => void) => () => void
}

const MetricsContext = createContext<MetricsApi | null>(null)

/**
 * 全局唯一的 SSE 连接。
 *
 * 为什么必须提到 Provider 层：仪表盘与实时监控都需要实时快照，
 * 而路由切换会卸载页面。此前每个页面各自调 useMetricsStream(true)，
 * 于是切一次菜单就销毁并重建一条 EventSource：
 *
 *   - 每次重建都要重新换票（票据一次性、60 秒 TTL），多一次 POST；
 *   - 被 close() 掉的连接在浏览器控制台留下一条 net::ERR_ABORTED 红色报错，
 *     每次切菜单都刷一条，看起来像故障，实际是正常卸载；
 *   - 服务端白跑一个订阅（collector's channel 与 eventbus 的订阅都要建再撤）。
 *
 * 提到 Provider 后，连接的生命周期跟登录态绑定，而不是跟路由绑定：
 * 登录后建立，登出或刷新页面才关闭。切菜单不再产生任何请求与报错。
 */
export function MetricsProvider({ children }: { children: React.ReactNode }) {
  const { user } = useAuth()
  // 换票接口本身就要求 metrics:read，这里再判一次是防止无权限者
  // 白白建立一条注定被服务端拒绝的流。
  const canReadMetrics = roleCan(user?.role, 'metrics:read')

  const [snapshot, setSnapshot] = useState<MetricsSnapshot | null>(null)
  const [connected, setConnected] = useState(false)
  const [lastEvent, setLastEvent] = useState<DomainEvent | null>(null)

  // 领域事件回调登记表。页面卸载时自行注销，
  // 否则会向已销毁的组件派发事件。
  const handlersRef = useRef(new Set<(ev: DomainEvent) => void>())

  const subscribe = useMemo(() => {
    return (fn: (ev: DomainEvent) => void) => {
      handlersRef.current.add(fn)
      return () => {
        handlersRef.current.delete(fn)
      }
    }
  }, [])

  useEffect(() => {
    if (!canReadMetrics) return

    let disposed = false
    let source: EventSource | null = null
    let retryTimer: ReturnType<typeof setTimeout> | null = null
    let attempt = 0
    // 换票是异步的，连接建立前必须有这个闸门：
    // 否则连续失败时会并发发起多个 EventSource。
    let connecting = false

    const scheduleReconnect = () => {
      if (disposed || retryTimer) return
      setConnected(false)
      attempt += 1
      // 指数退避。票据可能是因为 401 被拒（会话过期），
      // 这时退避再久也没用，但 AuthProvider 会同时把用户送去登录页。
      const delay = Math.min(1000 * 2 ** (attempt - 1), RETRY_MAX_MS)
      retryTimer = setTimeout(() => {
        retryTimer = null
        void connect()
      }, delay)
    }

    const connect = async () => {
      if (disposed || connecting) return
      connecting = true
      try {
        const { ticket } = await api.streamTicket()
        if (disposed) return

        // 票据是一次性的，连上之后立刻作废；这里不保留引用。
        const es = new EventSource(
          `/api/v1/metrics/stream?ticket=${encodeURIComponent(ticket)}`,
        )
        source = es

        es.addEventListener('hello', () => {
          attempt = 0
          setConnected(true)
        })

        es.addEventListener('metrics', (event) => {
          try {
            // 帧 payload 是完整快照，而不是帧本身。
            const frame = JSON.parse((event as MessageEvent<string>).data) as {
              type: string
              payload: unknown
              sentAt: number
            }
            setSnapshot(frame.payload as MetricsSnapshot)
            setConnected(true)
          } catch {
            // 单帧解析失败不应中断整个连接。
          }
        })

        for (const type of DOMAIN_EVENT_TYPES) {
          es.addEventListener(type, (event) => {
            try {
              const frame = JSON.parse((event as MessageEvent<string>).data) as {
                type: string
                payload: unknown
                sentAt: number
              }
              const domain = frame.payload as DomainEvent
              setLastEvent(domain)
              for (const fn of handlersRef.current) {
                try {
                  fn(domain)
                } catch {
                  // 单个订阅者处理失败不影响其他订阅者与这条连接。
                }
              }
            } catch {
              // 同上：坏帧不拖垮连接。
            }
          })
        }

        es.onerror = () => {
          // EventSource 会自己重连，但重连用的是同一个、已被消费掉的
          // 票据，必然 401；必须自己关掉重连，换票后再连。
          es.close()
          if (source === es) source = null
          scheduleReconnect()
        }
      } catch {
        // 换票失败通常是会话过期。仍然退避重试：
        // AuthProvider 会并行把界面切回登录页。
        scheduleReconnect()
      } finally {
        connecting = false
      }
    }

    void connect()

    return () => {
      disposed = true
      if (retryTimer) clearTimeout(retryTimer)
      source?.close()
      source = null
      setConnected(false)
    }
  }, [canReadMetrics])

  const value = useMemo(
    () => ({ snapshot, connected, lastEvent, subscribe }),
    [snapshot, connected, lastEvent, subscribe],
  )

  return <MetricsContext.Provider value={value}>{children}</MetricsContext.Provider>
}

/** 读取全局 SSE 状态。必须在 MetricsProvider 内使用。 */
export function useMetricsApi(): MetricsApi {
  const ctx = useContext(MetricsContext)
  if (!ctx) throw new Error('useMetricsApi must be used inside MetricsProvider')
  return ctx
}
