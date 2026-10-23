import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import { api, setAccessToken, setUnauthorizedHandler } from '@/lib/api'
import type { Me, Session } from '@/types/api'

interface AuthState {
  user: Me | null
  loading: boolean
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)

  const clearSession = useCallback(() => {
    setAccessToken(null)
    setUser(null)
  }, [])

  // 401 时统一清理会话，由路由守卫把用户送回登录页。
  useEffect(() => {
    setUnauthorizedHandler(clearSession)
    return () => setUnauthorizedHandler(null)
  }, [clearSession])

  // 应用启动时用 HttpOnly Refresh Cookie 换取访问令牌，实现免登录刷新。
  useEffect(() => {
    let cancelled = false

    ;(async () => {
      // 刷新 Cookie 是 HttpOnly，JS 读不到，没登录时无法判断它是否存在。
      // 用一个本地标记避免每次打开登录页都白发一次 refresh：
      // 那次请求必然 401，浏览器会在控制台留一条红色报错，
      // 看起来像故障，实际只是「尚未登录」的正常返回。
      if (!hasSessionHint()) {
        if (!cancelled) {
          clearSession()
          setLoading(false)
        }
        return
      }
      try {
        const session = await api.refresh()
        if (cancelled) return
        setAccessToken(session.accessToken)
        setUser(await api.me())
        setSessionHint(true)
      } catch {
        // 刷新失败说明会话已失效，清掉标记，下次启动不再试探。
        if (!cancelled) {
          setSessionHint(false)
          clearSession()
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()

    return () => {
      cancelled = true
    }
  }, [clearSession])

  const login = useCallback(async (username: string, password: string) => {
    const session: Session = await api.login(username, password)
    setAccessToken(session.accessToken)
    setUser(await api.me())
    setSessionHint(true)
  }, [])

  const logout = useCallback(async () => {
    try {
      await api.logout()
    } finally {
      setSessionHint(false)
      clearSession()
    }
  }, [clearSession])

  const value = useMemo(() => ({ user, loading, login, logout }), [user, loading, login, logout])

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider')
  return ctx
}
/**
 * 会话存在性标记。
 *
 * 存 localStorage 而不是 Cookie：刷新令牌本身是 HttpOnly，
 * 页面脚本读不到，这里只需要一个「大概有会话」的提示。
 * 真正的安全判定仍在服务端——标记存在但令牌失效时，
 * refresh 一样会 401，登录页照常显示。
 */
const SESSION_HINT_KEY = 'wgp.session'

function hasSessionHint(): boolean {
  try {
    return localStorage.getItem(SESSION_HINT_KEY) === '1'
  } catch {
    // 隐私模式下 localStorage 可能不可用：保守地当作无会话，
    // 代价是刷新页面需要重新登录，而不是白白发一次 401。
    return false
  }
}

function setSessionHint(present: boolean): void {
  try {
    if (present) localStorage.setItem(SESSION_HINT_KEY, '1')
    else localStorage.removeItem(SESSION_HINT_KEY)
  } catch {
    // 存不了就算了，不影响功能。
  }
}
