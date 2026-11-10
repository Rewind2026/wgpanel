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
    'user:manage',
  ],
  admin: [
    'server:read', 'server:write',
    'peer:read', 'peer:create', 'peer:update', 'peer:delete', 'peer:revoke',
    'metrics:read',
    'config:export', 'config:import',
    'audit:read',
    'settings:write', 'system:shutdown',
    'user:manage',
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

/**
 * 角色层级，与后端 service.roleRank 一一对应。
 * 数值越大管辖面越大；未知角色按 -1 处理，一律不可管理。
 */
const ROLE_RANK: Record<Role, number> = { owner: 3, admin: 2, operator: 1, viewer: 0 }

/**
 * 判断 actor 能否管理 target 角色的账号，镜像后端 service.canManage。
 *
 * 只用于控件显隐：真正拒绝请求的是后端。两者必须同步，
 * 否则界面会给出「按钮可点、点了必 403」的错误暗示——
 * 权限界面比普通功能更需要「可见即可行」。
 */
export function canManageRole(actor: Role | undefined, target: Role): boolean {
  if (!actor || actor === 'operator' || actor === 'viewer') return false
  if (actor !== 'owner' && ROLE_RANK[actor] <= ROLE_RANK[target]) return false
  return ROLE_RANK[actor] >= ROLE_RANK[target]
}
