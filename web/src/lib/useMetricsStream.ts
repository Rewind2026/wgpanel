import { useEffect, useRef } from 'react'
import { useMetricsApi } from '@/components/providers/MetricsProvider'
import type { DomainEvent } from '@/types/api'

/**
 * 订阅全局 SSE 推送的监控快照与领域事件。
 *
 * 连接本身由 MetricsProvider 持有（全局唯一），这个 hook 只做订阅。
 * 此前每个页面各自建立 EventSource，切一次菜单就销毁重建一条：
 * 多一次换票请求，控制台多一条 net::ERR_ABORTED 红色报错，
 * 服务端多建再撤一个订阅。现在切菜单不再产生任何连接抖动。
 *
 * 领域事件回调用 ref 保持稳定：调用方若传内联函数，
 * 依赖数组也不会因此每渲染重订阅一次。
 */
export function useMetricsStream(enabled: boolean, onDomainEvent?: (ev: DomainEvent) => void) {
  const { snapshot, connected, lastEvent, subscribe } = useMetricsApi()

  // 回调放在 ref 里：订阅只依赖 enabled，不会因调用方传了新函数而反复重订阅。
  const handlerRef = useRef(onDomainEvent)
  handlerRef.current = onDomainEvent

  useEffect(() => {
    if (!enabled) return
    return subscribe((ev) => handlerRef.current?.(ev))
  }, [enabled, subscribe])

  if (!enabled) return { snapshot: null, connected: false, lastEvent: null }
  return { snapshot, connected, lastEvent }
}
