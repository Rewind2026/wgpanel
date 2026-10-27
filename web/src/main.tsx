import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App } from '@/App'
import { ApiError } from '@/lib/api'
import { AuthProvider } from '@/components/providers/AuthProvider'
import { MetricsProvider } from '@/components/providers/MetricsProvider'
import { ToastProvider } from '@/components/providers/ToastProvider'
import '@/index.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // 监控面板有 SSE 推送，列表请求降到低频即可，避免无谓轮询。
      refetchOnWindowFocus: false,
      staleTime: 5_000,
      retry: (failureCount, error) => {
        // 鉴权与权限错误重试没有意义。
        if (error instanceof ApiError && error.status < 500) return false
        return failureCount < 2
      },
    },
  },
})

const container = document.getElementById('root')
if (!container) throw new Error('root container not found')

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <ToastProvider>
          <AuthProvider>
            {/*
              MetricsProvider 必须在 AuthProvider 之内：
              SSE 连接需要当前登录态判断是否持有 metrics:read 权限。
              它挂在路由之外，SSE 连接的生命周期跟随会话而不是跟随路由，
              切菜单不再销毁重建 EventSource。
            */}
            <MetricsProvider>
              <App />
            </MetricsProvider>
          </AuthProvider>
        </ToastProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)