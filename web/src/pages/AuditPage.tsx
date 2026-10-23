import { Fragment, useEffect, useMemo, useState } from 'react'
import {
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronUp,
  Download,
  Filter,
  Loader2,
  RotateCcw,
  ScrollText,
  Search,
} from 'lucide-react'
import { ApiError, api } from '@/lib/api'
import { roleCan } from '@/lib/permissions'
import { cn, formatTime } from '@/lib/utils'
import { useAuth } from '@/components/providers/AuthProvider'
import { useToast } from '@/components/providers/ToastProvider'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/Button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/Card'
import { Field, Input, Select } from '@/components/ui/Input'
import { EmptyState, TableSkeleton } from '@/components/ui/Skeleton'
import type { AuditEvent, PageResult } from '@/types/api'

type ResultFilter = '' | 'success' | 'failure'

interface Filters {
  action: string
  resource: string
  result: ResultFilter
  from: string
  to: string
}

const EMPTY_FILTERS: Filters = { action: '', resource: '', result: '', from: '', to: '' }

const PAGE_SIZE_OPTIONS = [20, 50, 100]

/**
 * 已知审计动作。既用于动作输入框的下拉提示，也用于中文标签映射；
 * 后端新增动作不在此列表时，输入框仍可直接填写原始动作代码。
 */
const KNOWN_ACTIONS: { value: string; label: string }[] = [
  { value: 'auth.login', label: '登录' },
  { value: 'auth.login_failed', label: '登录失败' },
  { value: 'auth.logout', label: '登出' },
  { value: 'user.password_change', label: '修改密码' },
  { value: 'server.update', label: '更新服务端' },
  { value: 'server.reset_key', label: '轮换服务端密钥' },
  { value: 'peer.create', label: '创建客户端' },
  { value: 'peer.update', label: '更新客户端' },
  { value: 'peer.delete', label: '删除客户端' },
  { value: 'peer.rotate_key', label: '轮换客户端密钥' },
  { value: 'peer.bulk_revoke', label: '批量吊销客户端' },
  { value: 'peer.import', label: '导入客户端' },
  { value: 'config.export', label: '导出配置' },
  { value: 'settings.update', label: '更新设置' },
  { value: 'backup.create', label: '创建备份' },
  { value: 'restore.apply', label: '恢复备份' },
]

const ACTION_LABELS = new Map<string, string>(
  KNOWN_ACTIONS.map((a): [string, string] => [a.value, a.label]),
)

type PageItem = number | 'gap'

export function AuditPage() {
  const { user } = useAuth()
  const toast = useToast()
  const canExport = roleCan(user?.role, 'config:export')

  // draft 是表单里正在编辑的条件，applied 是真正发给后端的条件。
  // 两者分离是刻意的：每敲一个字符就发一次请求既吵又容易打爆后端。
  const [draft, setDraft] = useState<Filters>(EMPTY_FILTERS)
  const [applied, setApplied] = useState<Filters>(EMPTY_FILTERS)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [data, setData] = useState<PageResult<AuditEvent> | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [exporting, setExporting] = useState(false)
  const [expanded, setExpanded] = useState<number | null>(null)

  const params = useMemo(
    () => ({
      action: applied.action.trim() || undefined,
      resource: applied.resource || undefined,
      result: applied.result || undefined,
      from: toRFC3339(applied.from, false),
      to: toRFC3339(applied.to, true),
    }),
    [applied],
  )

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError(null)

    api.audit({ ...params, page, pageSize }).then(
      (res) => {
        if (controller.signal.aborted) return
        setData(res)
        setLoading(false)
      },
      (err: ApiError) => {
        if (controller.signal.aborted) return
        setData(null)
        setError(err.message)
        setLoading(false)
        toast.error(err.message)
      },
    )

    // api.audit 没有暴露 signal 参数，故拿 AbortController 当失效令牌：
    // 条件变化或组件卸载后丢弃迟到响应，避免旧结果盖掉新结果。
    return () => controller.abort()
  }, [params, page, pageSize, toast])

  const items = data?.items ?? []
  const totalPages = Math.max(1, Math.ceil((data?.total ?? 0) / pageSize))

  // 缩小每页条数后当前页可能越界，退回最后一页而不是显示一张空表。
  // data 为 null 说明请求失败了，此时不参与纠偏，避免错误态触发重试循环。
  useEffect(() => {
    if (data && page > totalPages) setPage(totalPages)
  }, [data, page, totalPages])

  const hasFilter = Boolean(
    applied.action.trim() || applied.resource || applied.result || applied.from || applied.to,
  )

  const submit = () => {
    setPage(1)
    setExpanded(null)
    setApplied({ ...draft })
  }

  const reset = () => {
    setDraft(EMPTY_FILTERS)
    setPage(1)
    setExpanded(null)
    setApplied(EMPTY_FILTERS)
  }

  const exportCsv = async () => {
    setExporting(true)
    try {
      // params 已经是应用后的 RFC3339 条件，直接透传，避免二次转换。
      await api.exportAudit(params)
      toast.success('审计日志 CSV 已开始下载')
    } catch (err) {
      toast.error((err as ApiError).message)
    } finally {
      setExporting(false)
    }
  }

  return (
    <div className="space-y-5">
      <header className="relative border-b border-border pb-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <p className="hud-label text-primary">// LOG-05</p>
            <h1 className="mt-1.5 font-display text-xl font-bold uppercase tracking-[0.1em] sm:text-2xl">
              审计日志
            </h1>
            <p className="mt-1 text-xs text-muted-foreground">
              记录登录、配置变更与密钥操作，用于事后追溯
              {data ? ` · 共 ${data.total} 条` : ''}
            </p>
          </div>
          {canExport && (
            <Button variant="outline" onClick={exportCsv} disabled={exporting}>
              {exporting ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <Download className="h-4 w-4" />
              )}
              导出 CSV
            </Button>
          )}
        </div>
        <div
          className="hazard-stripes absolute -bottom-px left-0 h-[2px] w-24 opacity-80"
          aria-hidden="true"
        />
      </header>

      <Card>
        <CardHeader>
          <CardTitle>筛选条件</CardTitle>
          <CardDescription>修改后点击「查询」才会请求，不会随输入自动刷新</CardDescription>
        </CardHeader>
        <CardContent>
          <form
            className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3"
            onSubmit={(e) => {
              e.preventDefault()
              submit()
            }}
          >
            <Field label="操作类型" htmlFor="audit-action" hint="可下拉选择，也可直接输入动作代码">
              <Input
                id="audit-action"
                list="audit-action-options"
                value={draft.action}
                onChange={(e) => setDraft((p) => ({ ...p, action: e.target.value }))}
                placeholder="全部"
                className="font-mono"
              />
              <datalist id="audit-action-options">
                {KNOWN_ACTIONS.map((a) => (
                  <option key={a.value} value={a.value}>
                    {a.label}
                  </option>
                ))}
              </datalist>
            </Field>

            <Field label="资源类型" htmlFor="audit-resource">
              <Select
                id="audit-resource"
                value={draft.resource}
                onChange={(e) => setDraft((p) => ({ ...p, resource: e.target.value }))}
              >
                <option value="">全部</option>
                <option value="peer">peer · 客户端</option>
                <option value="user">user · 账号</option>
                <option value="server">server · 服务端</option>
                <option value="settings">settings · 设置</option>
                <option value="backup">backup · 备份</option>
              </Select>
            </Field>

            <Field label="结果" htmlFor="audit-result">
              <Select
                id="audit-result"
                value={draft.result}
                onChange={(e) =>
                  setDraft((p) => ({ ...p, result: e.target.value as ResultFilter }))
                }
              >
                <option value="">全部</option>
                <option value="success">成功</option>
                <option value="failure">失败</option>
              </Select>
            </Field>

            <Field label="起始日期" htmlFor="audit-from" hint="含当天 00:00">
              <Input
                id="audit-from"
                type="date"
                value={draft.from}
                onChange={(e) => setDraft((p) => ({ ...p, from: e.target.value }))}
              />
            </Field>

            <Field label="结束日期" htmlFor="audit-to" hint="含当天 23:59">
              <Input
                id="audit-to"
                type="date"
                value={draft.to}
                onChange={(e) => setDraft((p) => ({ ...p, to: e.target.value }))}
              />
            </Field>

            <div className="flex items-end gap-2">
              <Button type="submit" className="flex-1">
                <Search className="h-4 w-4" />
                查询
              </Button>
              <Button type="button" variant="outline" onClick={reset}>
                <RotateCcw className="h-4 w-4" />
                重置
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>记录</CardTitle>
          <CardDescription>点击任意一行展开完整字段，含 User-Agent 等原始信息</CardDescription>
        </CardHeader>
        <CardContent>
          {loading ? (
            <TableSkeleton rows={8} />
          ) : error ? (
            <div className="flex items-start gap-3 border border-destructive/50 bg-destructive/10 p-4 text-sm">
              <span className="hud-label shrink-0 pt-0.5 text-destructive">ERR</span>
              <div className="min-w-0">
                <p className="font-display text-xs font-semibold uppercase tracking-[0.14em] text-destructive">
                  审计日志加载失败
                </p>
                <p className="mt-1 break-words text-muted-foreground">{error}</p>
              </div>
            </div>
          ) : items.length === 0 ? (
            <EmptyState
              icon={hasFilter ? Filter : ScrollText}
              title={hasFilter ? '当前筛选条件下没有记录' : '还没有审计记录'}
              description={
                hasFilter
                  ? '放宽条件或直接重置后再试一次。'
                  : '登录、配置变更等操作发生后会自动写入这里。'
              }
              action={
                hasFilter ? (
                  <Button variant="outline" onClick={reset}>
                    <RotateCcw className="h-4 w-4" />
                    重置筛选
                  </Button>
                ) : undefined
              }
            />
          ) : (
            <>
              {/* 桌面端表格视图 */}
              <div className="hidden overflow-x-auto border border-border md:block">
                <table className="hud-table min-w-[880px]">
                  <caption className="sr-only">审计日志记录列表</caption>
                  <thead>
                    <tr>
                      <th scope="col">时间</th>
                      <th scope="col">操作者</th>
                      <th scope="col">动作</th>
                      <th scope="col">资源</th>
                      <th scope="col">详情</th>
                      <th scope="col">来源 IP</th>
                      <th scope="col">结果</th>
                      <th scope="col" className="text-right">
                        <span className="sr-only">展开详情</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {items.map((event) => {
                      const open = expanded === event.id
                      return (
                        <Fragment key={event.id}>
                          <tr
                            onClick={() => setExpanded(open ? null : event.id)}
                            className={cn('cursor-pointer', open && 'bg-primary/[0.07]')}
                          >
                            <td className="whitespace-nowrap font-mono text-xs tabular-nums text-muted-foreground">
                              {formatTime(event.createdAt)}
                            </td>
                            <td className="font-display text-sm font-semibold tracking-wide">
                              {event.actorName || '—'}
                            </td>
                            <td>
                              <Badge variant={actionVariant(event.action)}>
                                {actionLabel(event.action)}
                              </Badge>
                              <p className="mt-1 font-mono text-[11px] text-muted-foreground">
                                {event.action}
                              </p>
                            </td>
                            <td className="font-mono text-xs text-neon-cyan">
                              {resourceText(event)}
                            </td>
                            <td className="max-w-[18rem] text-xs text-muted-foreground">
                              <span className="line-clamp-2 break-words">
                                {event.detail || '—'}
                              </span>
                            </td>
                            <td className="font-mono text-xs">{event.ip || '—'}</td>
                            <td>
                              <Badge variant={event.result === 'success' ? 'success' : 'destructive'}>
                                {event.result === 'success' ? '成功' : '失败'}
                              </Badge>
                            </td>
                            <td className="text-right">
                              <Button
                                variant="ghost"
                                size="icon-sm"
                                aria-label={`${open ? '收起' : '展开'} ${formatTime(event.createdAt)} 的详情`}
                                aria-expanded={open}
                                onClick={(e) => {
                                  e.stopPropagation()
                                  setExpanded(open ? null : event.id)
                                }}
                              >
                                {open ? (
                                  <ChevronUp className="h-4 w-4" />
                                ) : (
                                  <ChevronDown className="h-4 w-4" />
                                )}
                              </Button>
                            </td>
                          </tr>
                          {open && (
                            <tr className="bg-muted/40">
                              <td colSpan={8} className="px-3 py-3">
                                <EventFields event={event} />
                              </td>
                            </tr>
                          )}
                        </Fragment>
                      )
                    })}
                  </tbody>
                </table>
              </div>

              {/* 移动端卡片视图 */}
              <div className="space-y-2 md:hidden">
                {items.map((event) => {
                  const open = expanded === event.id
                  return (
                    <div
                      key={event.id}
                      className={cn(
                        'hud-slot p-3',
                        open && 'border-primary/50',
                      )}
                    >
                      <div className="flex items-start justify-between gap-2">
                        <div className="min-w-0">
                          <p className="truncate text-sm font-medium">
                            {event.actorName || '—'}
                          </p>
                          <p className="mt-0.5 font-mono text-xs text-muted-foreground">
                            {formatTime(event.createdAt)}
                          </p>
                        </div>
                        <Badge variant={event.result === 'success' ? 'success' : 'destructive'}>
                          {event.result === 'success' ? '成功' : '失败'}
                        </Badge>
                      </div>
                      <div className="mt-2 flex flex-wrap items-center gap-2">
                        <Badge variant={actionVariant(event.action)}>
                          {actionLabel(event.action)}
                        </Badge>
                        <span className="font-mono text-xs text-muted-foreground">
                          {resourceText(event)}
                        </span>
                        {event.ip && (
                          <span className="font-mono text-xs text-muted-foreground">
                            {event.ip}
                          </span>
                        )}
                      </div>
                      <p className="mt-2 break-words text-xs text-muted-foreground">
                        {event.detail || '—'}
                      </p>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="mt-2 w-full"
                        aria-expanded={open}
                        onClick={() => setExpanded(open ? null : event.id)}
                      >
                        {open ? (
                          <ChevronUp className="h-3.5 w-3.5" />
                        ) : (
                          <ChevronDown className="h-3.5 w-3.5" />
                        )}
                        {open ? '收起详情' : '展开详情'}
                      </Button>
                      {open && (
                        <div className="mt-3 border-t border-border/60 pt-3">
                          <EventFields event={event} />
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>

              <div className="mt-4 flex flex-wrap items-center justify-between gap-3 border-t border-border/60 pt-4">
                <p className="text-xs text-muted-foreground">
                  共{' '}
                  <span className="font-mono tabular-nums text-foreground">
                    {data?.total ?? 0}
                  </span>{' '}
                  条 · 第 {page} / {totalPages} 页
                </p>
                <Select
                  value={String(pageSize)}
                  onChange={(e) => {
                    setPageSize(Number(e.target.value))
                    setPage(1)
                  }}
                  className="h-8 w-28 text-xs"
                  aria-label="每页条数"
                >
                  {PAGE_SIZE_OPTIONS.map((n) => (
                    <option key={n} value={n}>
                      {n} 条/页
                    </option>
                  ))}
                </Select>
                <div className="flex items-center gap-1">
                  <Button
                    variant="outline"
                    size="icon-sm"
                    aria-label="上一页"
                    disabled={page <= 1}
                    onClick={() => setPage((p) => Math.max(1, p - 1))}
                  >
                    <ChevronLeft className="h-4 w-4" />
                  </Button>
                  {paginationItems(page, totalPages).map((item, i) =>
                    item === 'gap' ? (
                      <span key={`gap-${i}`} className="px-1 text-xs text-muted-foreground">
                        …
                      </span>
                    ) : (
                      <Button
                        key={item}
                        variant={item === page ? 'default' : 'ghost'}
                        size="icon-sm"
                        aria-label={`第 ${item} 页`}
                        aria-current={item === page ? 'page' : undefined}
                        onClick={() => setPage(item)}
                      >
                        {item}
                      </Button>
                    ),
                  )}
                  <Button
                    variant="outline"
                    size="icon-sm"
                    aria-label="下一页"
                    disabled={page >= totalPages}
                    onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                  >
                    <ChevronRight className="h-4 w-4" />
                  </Button>
                </div>
              </div>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

/** 展开后的完整字段，桌面端表格与移动端卡片共用。 */
function EventFields({ event }: { event: AuditEvent }) {
  const rows: [string, string][] = [
    ['记录 ID', String(event.id)],
    ['时间', `${formatTime(event.createdAt)}（${event.createdAt}）`],
    ['操作者', event.actorName || '—'],
    ['操作者 ID', String(event.actorId ?? '—')],
    ['动作', `${actionLabel(event.action)}（${event.action}）`],
    ['资源', resourceText(event)],
    ['详情', event.detail || '—'],
    ['来源 IP', event.ip || '—'],
    ['User-Agent', event.userAgent || '—'],
    ['结果', event.result === 'success' ? '成功' : '失败'],
  ]

  return (
    <dl className="grid gap-x-6 gap-y-2 text-xs sm:grid-cols-2">
      {rows.map(([label, value]) => (
        <div key={label} className="min-w-0">
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="mt-0.5 break-words font-mono">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

function actionLabel(action: string): string {
  return ACTION_LABELS.get(action) ?? action
}

/** 删除、吊销、轮换、恢复会直接影响运行状态，用红色标出风险。 */
function actionVariant(action: string): 'destructive' | 'outline' | 'default' {
  if (/(delete|revoke|restore|reset_key|rotate_key)/.test(action)) return 'destructive'
  if (action.startsWith('auth.')) return 'outline'
  return 'default'
}

function resourceText(event: AuditEvent): string {
  if (event.resource && event.resourceId) return `${event.resource} #${event.resourceId}`
  return event.resource || event.resourceId || '—'
}

/**
 * 把 <input type="date"> 的 YYYY-MM-DD 转成 RFC3339。
 *
 * 必须补全时分秒：只传日期后端会当成 00:00:00，
 * 导致「到 3 月 5 日」实际只查到 3 月 5 日零点那一瞬间。
 */
function toRFC3339(value: string, endOfDay: boolean): string | undefined {
  const parts = value.split('-')
  if (parts.length < 3) return undefined
  const date = new Date(
    Number(parts[0]),
    Number(parts[1]) - 1,
    Number(parts[2]),
    endOfDay ? 23 : 0,
    endOfDay ? 59 : 0,
    endOfDay ? 59 : 0,
  )
  if (Number.isNaN(date.getTime())) return undefined
  return date.toISOString()
}

/** 页码序列：保留首尾与当前页两侧，其余折叠成省略号。 */
function paginationItems(current: number, total: number): PageItem[] {
  if (total <= 7) return Array.from({ length: total }, (_, i) => i + 1)
  const items: PageItem[] = []
  for (let p = 1; p <= total; p++) {
    if (Math.abs(p - current) <= 1 || p === 1 || p === total) {
      items.push(p)
    } else if (items[items.length - 1] !== 'gap') {
      items.push('gap')
    }
  }
  return items
}
