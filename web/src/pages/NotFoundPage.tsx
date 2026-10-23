import { Link } from 'react-router-dom'
import { Compass } from 'lucide-react'
import { buttonVariants } from '@/components/ui/Button'

/** 404：与登录页同级，不套 AppLayout，避免未登录时泄漏导航结构。 */
export function NotFoundPage() {
  return (
    <div className="flex min-h-full items-center justify-center px-4 py-16">
      <div className="glass-card hud-corners w-full max-w-md space-y-4 p-8 text-center">
        <div className="mx-auto w-fit border border-primary/40 bg-primary/10 p-3 [clip-path:polygon(8px_0,100%_0,100%_calc(100%-5px),calc(100%-5px)_100%,0_100%,0_8px)]">
          <Compass className="h-6 w-6 text-primary" aria-hidden="true" />
        </div>
        <div className="space-y-2">
          <p className="hud-value text-4xl text-primary">404</p>
          <p className="hud-label">// 路由未命中</p>
          <p className="text-xs text-muted-foreground">页面不存在，可能是链接过期或地址输入有误。</p>
        </div>
        <Link to="/" className={buttonVariants()}>
          返回仪表盘
        </Link>
      </div>
    </div>
  )
}