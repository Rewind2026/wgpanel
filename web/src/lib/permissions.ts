import type { Permission, Role } from '@/types/api'

/**
 * 前端权限矩阵，用于路由守卫与控件显隐。
 * 真正的安全边界在后端中间件，这里只做体验层拦截。
 *
 * 与后端 internal/domain/user.go 的 rolePermissions 保持一一对应：
 * 两份清单都是各自语言里唯一的事实来源，改一处必须同步另一处。
 */
const ROLE_PERMISSIONS: Record<Role, Permission[]> = {
  owner: [
    'server:read', 'server:write',
    'peer:read', 'peer:create', 'peer:update', 'peer:delete', 'peer:revoke',
    'metrics:read',
    'config:export', 'config:import',
    'audit:read',
    'settings:write', 'system:shutdown',
  ],
  admin: [
    'server:read', 'server:write',
    'peer:read', 'peer:create', 'peer:update', 'peer:delete', 'peer:revoke',
    'metrics:read',
    'config:export', 'config:import',
    'audit:read',
    'settings:write', 'system:shutdown',
  ],
  operator: [
    'server:read', 'server:write',
    'peer:read', 'peer:create', 'peer:update', 'peer:delete', 'peer:revoke',
    'metrics:read',
    'config:export',
  ],
  viewer: ['server:read', 'peer:read', 'metrics:read'],
}

export function roleCan(role: Role | undefined, permission: Permission): boolean {
  if (!role) return false
  return ROLE_PERMISSIONS[role]?.includes(permission) ?? false
}

export function roleLabel(role: Role): string {
  const labels: Record<Role, string> = {
    owner: '所有者',
    admin: '管理员',
    operator: '运维',
    viewer: '只读',
  }
  return labels[role]
}
