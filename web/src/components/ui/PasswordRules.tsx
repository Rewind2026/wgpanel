import { Check, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { checkPasswordRules } from '@/lib/password'

/**
 * 密码规则清单：实时标出每一条是否达标。
 *
 * 存在的理由是后端会拒绝不合格的密码，但那时用户已经填完并提交了。
 * 把规则前置到输入时���，能省掉一次「填 → 提交 → 被拒 → 改」的往返。
 *
 * 规则定义在 lib/password.ts，与后端 ValidatePasswordStrength 对齐。
 */
export function PasswordRules({
  password,
  username,
  className,
}: {
  password: string
  username?: string
  className?: string
}) {
  // 空输入时不显示任何状态：用户还没开始打字就先亮一排红叉是噪音。
  if (password.length === 0) return null

  const rules = checkPasswordRules(password, username)

  return (
    <ul className={cn('space-y-1 pt-1', className)} aria-label="密码要求">
      {rules.map((r) => (
        <li
          key={r.id}
          className={cn(
            'flex items-center gap-1.5 font-mono text-[11px] transition-colors',
            r.ok ? 'text-primary/80' : 'text-muted-foreground',
          )}
        >
          {r.ok ? (
            <Check className="h-3 w-3 shrink-0" aria-hidden />
          ) : (
            <X className="h-3 w-3 shrink-0 text-destructive/70" aria-hidden />
          )}
          <span>{r.label}</span>
        </li>
      ))}
    </ul>
  )
}
