import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/** 合并 Tailwind 类名，后者覆盖前者的同类属性。 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

/** 把字节数格式化为人类可读字符串。 */
export function formatBytes(bytes: number, decimals = 1): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / Math.pow(1024, i)
  return `${value.toFixed(i === 0 ? 0 : decimals)} ${units[i]}`
}

/** 把字节每秒格式化为速率字符串。 */
export function formatRate(bytesPerSecond: number): string {
  if (!Number.isFinite(bytesPerSecond) || bytesPerSecond <= 0) return '0 B/s'
  return `${formatBytes(bytesPerSecond)}/s`
}

/** 把秒数格式化为时长字符串。 */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '0s'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

/** 把 ISO 字符串或时间戳格式化为本地时间。 */
export function formatTime(input: string | number | undefined | null): string {
  if (!input) return '—'
  const date = new Date(input)
  if (Number.isNaN(date.getTime())) return '—'
  // 同 formatRelative：Go 零值 time.Time 是「从未」，不是「公元 1 年」。
  // 放过去会渲染成 01/01 00:00:00，看起来像一条真实的记录时间。
  if (date.getUTCFullYear() <= 1) return '—'
  return date.toLocaleString(undefined, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  })
}

/** 返回相对时间描述，例如「3 分钟前」。 */
export function formatRelative(input: string | number | undefined | null): string {
  if (!input) return '从未'
  const date = new Date(input)
  if (Number.isNaN(date.getTime())) return '从未'
  // Go 的零值 time.Time 序列化成 "0001-01-01T00:00:00Z"，它表示
  // 「从未发生」而不是「公元 1 年」。
  //
  // 这个字符串是 truthy，所以调用方的 `a || b` 回退会选中它而不是回退到
  // 另一个字段，于是「最后握手」列显示成「739903 天前」。
  // 把零值在这里统一收敛成「从未」，调用方不必每个字段都记得判一次。
  if (date.getUTCFullYear() <= 1) return '从未'
  const diff = Date.now() - date.getTime()
  if (diff < 0) return '刚刚'
  if (diff < 60_000) return '刚刚'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`
  return `${Math.floor(diff / 86_400_000)} 天前`
}

/** 把 Unix 秒转换为 Date；空值返回 null。 */
export function fromUnix(sec?: number | null): Date | null {
  if (sec === undefined || sec === null || sec <= 0) return null
  return new Date(sec * 1000)
}

/**
 * 从一组候选时间戳里取第一个真正有值的那个，全是零值时返回 undefined。
 *
 * 用于「实时数据优先，数据库回退」这类取值：内核给的 lastHandshake 更准，
 * 但 Peer 从未握手过时它是 Go 零值（truthy 的 "0001-01-01T00:00:00Z"），
 * 普通的 `a || b` 会误选它，把明明有 lastSeenAt 的行显示成「从未」。
 */
export function firstMeaningful<T>(...values: (T | undefined | null)[]): T | undefined {
  for (const v of values) {
    if (v === undefined || v === null) continue
    if (typeof v === 'string') {
      const d = new Date(v)
      if (Number.isNaN(d.getTime()) || d.getUTCFullYear() <= 1) continue
      return v
    }
    return v
  }
  return undefined
}

/**
 * 复制文本到剪贴板。
 *
 * navigator.clipboard 只在安全上下文（HTTPS 或 localhost）可用，
 * 而面板默认就部署在普通 HTTP 上——直接调用会抛 NotAllowedError，
 * 「复制配置」「复制订阅链接」两个按钮全都不动。
 * 这里先试现代 API，失败再退回 execCommand 的临时 textarea 方案。
 */
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // 权限被拒或策略限制：继续走回退路径。
    }
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}
