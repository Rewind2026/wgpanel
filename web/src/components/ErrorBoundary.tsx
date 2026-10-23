import { Component } from 'react'
import { AlertTriangle, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/Button'

interface Props {
  children: React.ReactNode
}

interface State {
  error: Error | null
}

/**
 * 顶层错误边界。
 *
 * 面板的主要价值是「随时能自救」：某个页面渲染抛错时，
 * 如果没有边界，整个白屏，用户只能刷新甚至看不到服务是否还活着。
 * 这里兜住错误并给出重试入口，避免一次局部异常演变成整机不可用。
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    // 保留结构化上下文，便于用户在 issue 里直接贴出来。
    console.error('页面渲染失败', error, info.componentStack)
  }

  private reset = () => this.setState({ error: null })

  render() {
    const { error } = this.state
    if (!error) return this.props.children

    return (
      <div className="glass-card hud-corners space-y-4 p-8 text-center">
        <div className="mx-auto w-fit border border-destructive/40 bg-destructive/10 p-3 [clip-path:polygon(8px_0,100%_0,100%_calc(100%-5px),calc(100%-5px)_100%,0_100%,0_8px)]">
          <AlertTriangle className="h-6 w-6 text-destructive" aria-hidden="true" />
        </div>
        <div className="space-y-2">
          <p className="hud-label text-destructive">// FATAL</p>
          <p className="font-display text-base font-bold uppercase tracking-[0.14em]">页面渲染出错</p>
          <p className="text-xs text-muted-foreground">
            这是前端的展示问题，服务本身仍在运行。可以先重试，若持续出现请把下面的错误信息反馈给维护者。
          </p>
        </div>
        <pre className="max-h-40 overflow-auto rounded-sm border border-border/60 bg-muted/40 p-3 text-left font-mono text-xs text-muted-foreground">
          {error.message}
        </pre>
        <div className="flex justify-center gap-2">
          <Button onClick={this.reset}>
            <RefreshCw className="h-4 w-4" />
            重试
          </Button>
          <Button variant="outline" onClick={() => window.location.reload()}>
            刷新页面
          </Button>
        </div>
      </div>
    )
  }
}