import { useEffect, useRef, useState } from 'react'
import { api } from '@/lib/api'
import { DOMAIN_EVENT_TYPES } from '@/types/api'
import type { DomainEvent, MetricsSnapshot } from '@/types/api'

/** SSE 帧结构，与后端 httpapi.eventFrame 对应。 */
interface Frame {
  type: string
  payload: unknown
  sentAt: number
}

/** 重连退避上限。与 EventSource 自身的重连间隔叠加，避免疯狂打服务端。 */
const RETRY_MAX_MS = 15_000

/**
 * 订阅后端 SSE 推送的监控快照与领域事件。
 *
 * 连接建立方式是「先换票、再连流」：浏览器 EventSource 不能设置
 * 自定义请求头，直接拿访问令牌连流必然 401——这不是理论风险，
 * 而是曾经真实发生过的缺陷：监控页一直在静默重连，界面上只显示
 * 「未连接」，却没有任何报错。
 *
 * 票据一次性消费，因此每次重连都必须重新换票；
 * 退避交给 EventSource 之外的这一层，避免票据刚换就被打满。
 */
export function useMetricsStream(enabled: boolean, onDomainEvent?: (ev: DomainEvent) => void) {
  const [snapshot, setSnapshot] = useState<MetricsSnapshot | null>(null)
  const [connected, setConnected] = useState(false)
  const [lastEvent, setLastEvent] = useState<DomainEvent | null>(null)
  // 回调放在 ref 里，避免调用方每次渲染都传新函数导致重建 EventSource。
  const handlerRef = useRef(onDomainEvent)
  handlerRef.current = onDomainEvent

  useEffect(() => {
    if (!enabled) return

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
            const frame = JSON.parse((event as MessageEvent<string>).data) as Frame
            setSnapshot(frame.payload as MetricsSnapshot)
            setConnected(true)
          } catch {
            // 单帧解析失败不应中断整个连接。
          }
        })

        for (const type of DOMAIN_EVENT_TYPES) {
          es.addEventListener(type, (event) => {
            try {
              const frame = JSON.parse((event as MessageEvent<string>).data) as Frame
              const domain = frame.payload as DomainEvent
              setLastEvent(domain)
              handlerRef.current?.(domain)
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
  }, [enabled])

  return { snapshot, connected, lastEvent }
}
