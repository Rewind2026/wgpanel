import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import { api, setAccessToken, setUnauthorizedHandler } from '@/lib/api'
import type { Me, Session } from '@/types/api'

interface AuthState {
  user: Me | null
  loading: boolean
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthState | null>(null)

// —— 会话生命周期(借鉴 DDNS 的主动会话管理)——
//
// 旧实现的问题:refresh 只在页面加载时用一次,会话中途 access token 15 分钟
// 绝对过期后,活跃用户也会被下一个请求 401 踢回登录页——「每 15 分钟硬踢」,
// 与活跃度无关。三层机制修复它:
//   1. 主动续期:到期前 60s 触发 refresh,活跃用户全程无感;
//   2. 401 单飞重试:漏网的过期请求先续期再重试(见 api.ts retryAfterRefresh);
//   3. 空闲踢出:本地活动跟踪 + 服务端空闲窗口(created_at 滑动)双保险,
//      被遗忘的标签页不会永久吊住会话。
const ACTIVITY_TRACK_GAP = 15_000
const IDLE_CHECK_INTERVAL = 15_000

// 登出原因标记,给登录页展示「为什么被送回来」。
const LOGOUT_REASON_KEY = 'wgp.logout_reason'

function setLogoutReason(reason: string): void {
  try {
    sessionStorage.setItem(LOGOUT_REASON_KEY, reason)
  } catch {
    // 存不了就算了,提示语只是 UX。
  }
}

/** 取走登录原因(读后即焚)。由 LoginPage 调用。 */
export function consumeLogoutReason(): string {
  try {
    const reason = sessionStorage.getItem(LOGOUT_REASON_KEY)
    sessionStorage.removeItem(LOGOUT_REASON_KEY)
    return reason ?? ''
  } catch {
    return ''
  }
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)

  // 会话元数据放 ref 而不是 state:定时器要读它们,但不该触发重渲染。
  const sessionRef = useRef<Session | null>(null)
  const expiryAtRef = useRef(0) // access token 过期时刻(ms epoch)
  const idleMinutesRef = useRef(0)
  const lastActivityRef = useRef(Date.now())
  const refreshTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const clearSession = useCallback(() => {
    if (refreshTimerRef.current) {
      clearTimeout(refreshTimerRef.current)
      refreshTimerRef.current = null
    }
    sessionRef.current = null
    expiryAtRef.current = 0
    idleMinutesRef.current = 0
    setAccessToken(null)
    setUser(null)
  }, [])

  const isIdleBeyondWindow = useCallback(() => {
    const idleMin = idleMinutesRef.current
    if (idleMin <= 0) return false
    return Date.now() - lastActivityRef.current > idleMin * 60_000
  }, [])

  const scheduleProactiveRefresh = useCallback(() => {
    if (refreshTimerRef.current) clearTimeout(refreshTimerRef.current)
    const s = sessionRef.current
    if (!s) return
    // 到期前 60s 续期;刚登录就贴着过期线的极端情况至少留 5s。
    const delayMs = Math.max((s.expiresIn - 60) * 1000, 5_000)
    refreshTimerRef.current = setTimeout(() => {
      void (async () => {
        const s2 = sessionRef.current
        if (!s2) return
        // 后台标签页不续命:别让一个被遗忘的标签页把会话永久吊住;
        // 每 5s 重排一次,回到前台由 visibilitychange 分支立即续期。
        if (document.hidden) {
          scheduleProactiveRefresh()
          return
        }
        // 本地已判空闲超窗:不发续期请求(服务端也会拒),让空闲踢出收尾。
        if (isIdleBeyondWindow()) return
        try {
          const session = await api.refresh()
          applySession(session)
          setUser(await api.me())
        } catch {
          // refresh 失败 = 服务端已判会话死亡(空闲/吊销/绝对过期)。
          setLogoutReason('expired')
          clearSession()
        }
      })()
    }, delayMs)
  }, [clearSession, isIdleBeyondWindow])

  // applySession 记录会话元数据并排下一次主动续期。
  const applySession = useCallback((session: Session) => {
    sessionRef.current = session
    expiryAtRef.current = Date.now() + session.expiresIn * 1000
    idleMinutesRef.current = session.idleMinutes ?? 0
    scheduleProactiveRefresh()
  }, [scheduleProactiveRefresh])

  // 401 时统一清理会话,由路由守卫把用户送回登录页。
  // 兜底路径说明会话已真的失效(access 过期且 refresh 拒绝),标为 expired。
  useEffect(() => {
    setUnauthorizedHandler(() => {
      setLogoutReason('expired')
      clearSession()
    })
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
        applySession(session)
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
  }, [applySession, clearSession])

  // 用户活动跟踪:节流采样,空闲窗口的本地判定与续期决策都以它为基准。
  useEffect(() => {
    let lastTrack = 0
    const onActivity = () => {
      const now = Date.now()
      if (now - lastTrack < ACTIVITY_TRACK_GAP) return
      lastTrack = now
      lastActivityRef.current = now
    }
    const events = ['pointerdown', 'keydown', 'focus', 'visibilitychange'] as const
    for (const ev of events) window.addEventListener(ev, onActivity, { passive: true })
    return () => {
      for (const ev of events) window.removeEventListener(ev, onActivity)
    }
  }, [])

  // 空闲踢出:服务端空闲窗口会拒绝下一次 refresh,这里在前端就主动收尾,
  // 不用等用户点按钮撞上 401 才发现被登出了。
  useEffect(() => {
    const iv = setInterval(() => {
      if (!sessionRef.current) return
      if (isIdleBeyondWindow()) {
        setLogoutReason('idle')
        clearSession()
      }
    }, IDLE_CHECK_INTERVAL)
    return () => clearInterval(iv)
  }, [clearSession, isIdleBeyondWindow])

  // 回到前台时若 access token 快过期/已过期,立即续期,
  // 避免后台标签页回来后的第一个请求要走 401 重试。
  useEffect(() => {
    const onVisible = () => {
      if (document.hidden || !sessionRef.current) return
      if (expiryAtRef.current - Date.now() < 60_000 && !isIdleBeyondWindow()) {
        void (async () => {
          try {
            const session = await api.refresh()
            applySession(session)
            setUser(await api.me())
          } catch {
            setLogoutReason('expired')
            clearSession()
          }
        })()
      }
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [applySession, clearSession, isIdleBeyondWindow])

  const login = useCallback(async (username: string, password: string) => {
    const session: Session = await api.login(username, password)
    setAccessToken(session.accessToken)
    applySession(session)
    setUser(await api.me())
    setSessionHint(true)
    consumeLogoutReason() // 新会话开始,清掉上一次的登出原因
  }, [applySession])

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
