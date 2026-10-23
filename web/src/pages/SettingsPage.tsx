import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Download, FileText, KeyRound, Loader2, RotateCcw, Save } from 'lucide-react'
import { ApiError, api } from '@/lib/api'
import { roleCan } from '@/lib/permissions'
import { useAuth } from '@/components/providers/AuthProvider'
import { useToast } from '@/components/providers/ToastProvider'
import { Button } from '@/components/ui/Button'
import { Badge } from '@/components/ui/Badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/Card'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Select } from '@/components/ui/Input'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatTime } from '@/lib/utils'
import type { ServerConfig } from '@/types/api'

export function SettingsPage() {
  const { user } = useAuth()
  const toast = useToast()
  const queryClient = useQueryClient()
  const canWrite = roleCan(user?.role, 'server:write')
  const canShutdown = roleCan(user?.role, 'system:shutdown')

  const serverQuery = useQuery({
    queryKey: ['server'],
    queryFn: ({ signal }) => api.getServer(signal),
  })

  const saveMutation = useMutation({
    mutationFn: (payload: ServerConfig) => api.updateServer(payload),
    onSuccess: () => {
      toast.success('配置已保存并立即生效')
      queryClient.invalidateQueries({ queryKey: ['server'] })
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  const resetKeyMutation = useMutation({
    mutationFn: () => api.resetServerKey(),
    onSuccess: () => {
      toast.success('服务端私钥已重置，请重新分发客户端配置')
      queryClient.invalidateQueries({ queryKey: ['server'] })
    },
    onError: (err: ApiError) => toast.error(err.message),
  })

  if (serverQuery.isLoading) {
    return (
      <div className="space-y-5">
        <Skeleton className="h-8 w-40" />
        <Skeleton className="h-72 w-full" />
      </div>
    )
  }

  if (serverQuery.isError || !serverQuery.data) {
    return (
      <Card>
        <CardContent className="p-6 text-sm text-destructive">
          无法加载服务端配置：{(serverQuery.error as ApiError)?.message}
        </CardContent>
      </Card>
    )
  }

  return (
    <div className="space-y-5">
      <header className="relative border-b border-border pb-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <p className="hud-label text-primary">// CONF-06</p>
            <h1 className="mt-1.5 font-display text-xl font-bold uppercase tracking-[0.1em] sm:text-2xl">
              服务端设置
            </h1>
            <p className="mt-1 text-xs text-muted-foreground">
              全部参数均可视化调整，保存后通过 netlink 热更新，无需重启容器
            </p>
          </div>
          <Badge variant="secondary">修订号 r{serverQuery.data.revision}</Badge>
        </div>
        <div
          className="hazard-stripes absolute -bottom-px left-0 h-[2px] w-24 opacity-80"
          aria-hidden="true"
        />
      </header>

      <ServerForm
        key={serverQuery.data.revision}
        initial={serverQuery.data}
        readOnly={!canWrite}
        submitting={saveMutation.isPending}
        onSubmit={(payload) => saveMutation.mutate(payload)}
      />

      <ServerExportCard revision={serverQuery.data.revision} />

      {canShutdown && (
        <Card className="border-destructive/40">
          <CardHeader>
            <CardTitle className="text-destructive">危险操作</CardTitle>
            <CardDescription>这些操作会立即影响所有客户端，请谨慎执行</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div>
                <p className="text-sm font-medium">重置服务端私钥</p>
                <p className="text-sm text-muted-foreground">
                  所有客户端配置的 [Peer] 公钥将失效，需重新生成并分发
                </p>
              </div>
              <Button
                variant="destructive"
                disabled={resetKeyMutation.isPending}
                onClick={() => {
                  if (window.confirm('确认重置服务端私钥？所有客户端需要重新导入配置。')) {
                    resetKeyMutation.mutate()
                  }
                }}
              >
                {resetKeyMutation.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <RotateCcw className="h-4 w-4" />
                )}
                重置私钥
              </Button>
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  )
}

function ServerForm({
  initial,
  readOnly,
  submitting,
  onSubmit,
}: {
  initial: ServerConfig
  readOnly: boolean
  submitting: boolean
  onSubmit: (payload: ServerConfig) => void
}) {
  const [form, setForm] = useState<ServerConfig>(initial)

  // 服务端数据刷新后同步表单（仅在修订号变化时重建表单，避免覆盖用户输入）。
  useEffect(() => setForm(initial), [initial])

  const update = <K extends keyof ServerConfig>(key: K, value: ServerConfig[K]) =>
    setForm((prev) => ({ ...prev, [key]: value }))

  const disabled = readOnly || submitting

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        onSubmit(form)
      }}
      className="space-y-5"
    >
      <Card>
        <CardHeader>
          <CardTitle>监听</CardTitle>
          <CardDescription>接口标识与 UDP 端口</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <Field label="接口名" htmlFor="s-interface" hint="Linux 接口名，最长 15 字符">
            <Input
              id="s-interface"
              value={form.interface}
              disabled={disabled}
              onChange={(e) => update('interface', e.target.value)}
              className="font-mono"
            />
          </Field>
          <Field label="监听端口" htmlFor="s-port" hint="1–65535，0 表示随机">
            <Input
              id="s-port"
              type="number"
              min={0}
              max={65535}
              value={form.listenPort}
              disabled={disabled}
              onChange={(e) => update('listenPort', Number(e.target.value))}
              className="font-mono"
            />
          </Field>
          <Field label="MTU" htmlFor="s-mtu" hint="常用 1420；容器网络建议不超过宿主 MTU − 80">
            <Input
              id="s-mtu"
              type="number"
              min={1280}
              max={9000}
              value={form.mtu}
              disabled={disabled}
              onChange={(e) => update('mtu', Number(e.target.value))}
              className="font-mono"
            />
          </Field>
          <Field label="FwMark" htmlFor="s-fwmark" hint="路由标记，用于策略路由，0 表示不设置">
            <Input
              id="s-fwmark"
              type="number"
              min={0}
              value={form.fwmark}
              disabled={disabled}
              onChange={(e) => update('fwmark', Number(e.target.value))}
              className="font-mono"
            />
          </Field>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>网络与路由</CardTitle>
          <CardDescription>隧道网段、DNS 与路由表行为</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <Field label="接口地址" htmlFor="s-cidr" hint="服务端在隧道内的地址与掩码">
            <Input
              id="s-cidr"
              value={form.addressCidr}
              disabled={disabled}
              onChange={(e) => update('addressCidr', e.target.value)}
              className="font-mono"
            />
          </Field>
          <Field label="路由表" htmlFor="s-table" hint="auto / off / 自定义表名">
            <Select
              id="s-table"
              value={form.table}
              disabled={disabled}
              onChange={(e) => update('table', e.target.value)}
            >
              <option value="auto">auto（默认）</option>
              <option value="off">off（不接管路由）</option>
              <option value="51820">51820（自定义表名）</option>
            </Select>
          </Field>
          <Field label="服务端 AllowedIPs" htmlFor="s-allowed" hint="逗号分隔的 CIDR">
            <Input
              id="s-allowed"
              value={form.allowedIps}
              disabled={disabled}
              onChange={(e) => update('allowedIps', e.target.value)}
              className="font-mono"
            />
          </Field>
          <Field label="DNS" htmlFor="s-dns" hint="下发客户端的 DNS，逗号分隔">
            <Input
              id="s-dns"
              value={form.dns}
              disabled={disabled}
              onChange={(e) => update('dns', e.target.value)}
              className="font-mono"
            />
          </Field>
          <Field label="PersistentKeepalive" htmlFor="s-keepalive" hint="秒，0 表示关闭">
            <Input
              id="s-keepalive"
              type="number"
              min={0}
              max={65535}
              value={form.persistentKeepalive}
              disabled={disabled}
              onChange={(e) => update('persistentKeepalive', Number(e.target.value))}
              className="font-mono"
            />
          </Field>
          <Field label="监听地址" htmlFor="s-listenip" hint="留空则监听全部地址">
            <Input
              id="s-listenip"
              value={form.listenIpv4}
              disabled={disabled}
              onChange={(e) => update('listenIpv4', e.target.value)}
              placeholder="0.0.0.0"
              className="font-mono"
            />
          </Field>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>行为</CardTitle>
          <CardDescription>WireGuard 的密钥算法由内核固定为 Curve25519，无需配置</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <Field label="公钥（只读）" htmlFor="s-pubkey" hint="分发客户端配置时使用">
            <Input id="s-pubkey" readOnly value={form.publicKey} className="font-mono text-xs" />
          </Field>
          <div className="hud-slot flex items-center justify-between px-3 py-2">
            <div>
              <p className="text-sm font-medium">SaveConfig</p>
              <p className="text-xs text-muted-foreground">接口关闭时保存运行配置</p>
            </div>
            <Select
              value={form.saveConfig ? 'on' : 'off'}
              disabled={disabled}
              onChange={(e) => update('saveConfig', e.target.value === 'on')}
              className="w-24"
              aria-label="SaveConfig"
            >
              <option value="on">开启</option>
              <option value="off">关闭</option>
            </Select>
          </div>
          <div className="hud-slot flex items-center justify-between px-3 py-2">
            <div>
              <p className="text-sm font-medium">ReplacePeers</p>
              <p className="text-xs text-muted-foreground">下发时替换而非追加 Peer 列表</p>
            </div>
            <Select
              value={form.replacePeers ? 'on' : 'off'}
              disabled={disabled}
              onChange={(e) => update('replacePeers', e.target.value === 'on')}
              className="w-24"
              aria-label="ReplacePeers"
            >
              <option value="on">开启</option>
              <option value="off">关闭</option>
            </Select>
          </div>
        </CardContent>
      </Card>

      {!readOnly && (
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={() => setForm(initial)}>
            <KeyRound className="h-4 w-4" />
            撤销修改
          </Button>
          <Button type="submit" disabled={submitting}>
            {submitting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
            保存并生效
          </Button>
        </div>
      )}
    </form>
  )
}

/**
 * 服务端配置导出与修订历史。
 *
 * 配置全文包含服务端私钥，属于最高敏感级别：它落在用户浏览器里、
 * 也可能被浏览器同步到别的设备上。因此默认只显示预览，下载要显式点击。
 */
function ServerExportCard({ revision }: { revision: number }) {
  const toast = useToast()
  const [preview, setPreview] = useState<string | null>(null)

  // 配置文本只在用户点开时才拉取。少数 server:read 账号完全可以不碰私钥，
  // 不该因为打开这个页面就把私钥送进浏览器内存。
  const configQuery = useQuery({
    queryKey: ['server-config', revision],
    queryFn: ({ signal }) => api.serverConfig(signal),
    enabled: false,
    staleTime: 30_000,
  })

  const revisionsQuery = useQuery({
    queryKey: ['server-revisions'],
    queryFn: ({ signal }) => api.serverRevisions(20, signal),
    enabled: preview !== null,
    staleTime: 30_000,
  })

  const downloadMutation = useMutation({
    mutationFn: () => api.serverConfigDownload(),
    onSuccess: () => toast.success('服务端配置已下载'),
    onError: (err: ApiError) => toast.error(err.message),
  })

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>服务端配置导出</CardTitle>
          <CardDescription>
            导出 wg-quick 配置全文，用于备份或迁移到另一台机器
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="rounded-sm border border-destructive/40 bg-destructive/5 p-3 text-xs text-destructive shadow-neon-alert">
            该文件包含服务端私钥。任何拿到它的人都能冒充你的服务端接入隧道，
            请只保存在可信位置，不要贴到聊天窗口或工单里。
          </p>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              disabled={configQuery.isFetching}
              onClick={() => {
                setPreview('')
                void configQuery.refetch()
              }}
            >
              {configQuery.isFetching ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <FileText className="h-4 w-4" />
              )}
              查看配置全文
            </Button>
            <Button
              variant="outline"
              disabled={downloadMutation.isPending}
              onClick={() => downloadMutation.mutate()}
            >
              {downloadMutation.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <Download className="h-4 w-4" />
              )}
              下载 wgpanel.conf
            </Button>
          </div>
        </CardContent>
      </Card>

      <Dialog
        open={preview !== null}
        title="服务端配置全文"
        description="仅供核对与备份，注意其中的 PrivateKey 属于敏感信息"
        onClose={() => setPreview(null)}
        className="sm:max-w-3xl"
        footer={
          <>
            <Button
              variant="outline"
              disabled={revisionsQuery.isFetching}
              onClick={() => void revisionsQuery.refetch()}
            >
              {revisionsQuery.isFetching ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
              查看修订历史
            </Button>
            <Button onClick={() => setPreview(null)}>关闭</Button>
          </>
        }
      >
        {configQuery.isError ? (
          <p className="text-sm text-destructive">
            读取失败：{(configQuery.error as ApiError)?.message}
          </p>
        ) : !preview && configQuery.isFetching ? (
          <Skeleton className="h-64 w-full" />
        ) : (
          <pre className="hud-slot max-h-80 overflow-auto p-3 font-mono text-xs leading-relaxed text-neon-cyan">
            {configQuery.data}
          </pre>
        )}

        {revisionsQuery.data && (
          <div className="mt-4 border-t border-border/60 pt-4">
            <p className="mb-2 text-sm font-medium">修订历史</p>
            {revisionsQuery.data.length === 0 ? (
              <p className="text-sm text-muted-foreground">暂无历史修订记录</p>
            ) : (
              <ul className="space-y-1.5">
                {revisionsQuery.data.map((r) => (
                  <li
                    key={r.revision}
                    className="hud-slot flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 font-mono text-xs"
                  >
                    <Badge variant={r.revision === revision ? 'default' : 'secondary'}>
                      r{r.revision}
                    </Badge>
                    <span className="font-mono text-xs text-muted-foreground">
                      {r.interface}:{r.listenPort} · MTU {r.mtu}
                    </span>
                    <span className="ml-auto text-xs text-muted-foreground">
                      {formatTime(r.updatedAt)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
            <p className="mt-2 text-xs text-muted-foreground">
              后端只保留每次保存的关键参数摘要，不存历史完整配置——需要旧值请提前导出。
            </p>
          </div>
        )}
      </Dialog>
    </>
  )
}