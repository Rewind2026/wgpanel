import { useEffect, useMemo, useRef, useState } from 'react'

/**
 * 窗口化列表 hook：只渲染视口内的行。
 *
 * 客户端数量可能上千，一次性渲染会让表格滚动掉帧、
 * 并把 DOM 撑到几十 MB。这里用固定行高做窗口计算，
 * 代价是不能有行内换行——所以列表项必须保持单行高度。
 */
export function useVirtualList<T>(items: T[], rowHeight = 56, overscan = 6) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [viewport, setViewport] = useState({ scrollTop: 0, height: 480 })

  useEffect(() => {
    const el = containerRef.current
    if (!el) return

    const sync = () => setViewport({ scrollTop: el.scrollTop, height: el.clientHeight })
    sync()
    el.addEventListener('scroll', sync, { passive: true })

    const observer = new ResizeObserver(sync)
    observer.observe(el)

    return () => {
      el.removeEventListener('scroll', sync)
      observer.disconnect()
    }
  }, [])

  return useMemo(() => {
    const total = items.length
    const start = Math.max(0, Math.floor(viewport.scrollTop / rowHeight) - overscan)
    const visible = Math.ceil(viewport.height / rowHeight) + overscan * 2
    const end = Math.min(total, start + visible)

    return {
      containerRef,
      total,
      start,
      end,
      paddingTop: start * rowHeight,
      paddingBottom: Math.max(0, (total - end) * rowHeight),
      visible: items.slice(start, end),
      onScrollToIndex: (index: number) => {
        const el = containerRef.current
        if (!el) return
        el.scrollTop = Math.max(0, index * rowHeight - rowHeight * 2)
      },
    }
  }, [items, viewport, rowHeight, overscan])
}