import type { AuthType, EffectiveStatus } from './api'

export function authLabel(type: AuthType): string {
  switch (type) {
    case 'certificate':
      return '证书'
    case 'certificate_password':
      return '证书 + 密码'
    case 'password':
      return '仅密码'
    default:
      return type
  }
}

export function authRequiresCertificate(type: AuthType): boolean {
  return type !== 'password'
}

export function authRequiresPassword(type: AuthType): boolean {
  return type !== 'certificate'
}

export function statusLabel(status: EffectiveStatus | 'active' | 'disabled'): string {
  switch (status) {
    case 'active':
      return '可连接'
    case 'user_disabled':
      return '用户已停用'
    case 'profile_disabled':
      return '配置已停用'
    case 'certificate_missing':
      return '证书已吊销或缺失'
    case 'certificate_revoked':
      return '证书已吊销'
    case 'certificate_expired':
      return '证书已过期'
    case 'expired':
      return '已到期'
    case 'quota_exhausted':
      return '流量已用尽'
    case 'disabled':
      return '已停用'
    default:
      return status
  }
}

export function statusTone(status: EffectiveStatus | 'active' | 'disabled'): 'ok' | 'warn' | 'danger' | 'neutral' {
  switch (status) {
    case 'active':
      return 'ok'
    case 'certificate_missing':
    case 'certificate_revoked':
    case 'certificate_expired':
      return 'danger'
    case 'user_disabled':
    case 'profile_disabled':
    case 'expired':
    case 'quota_exhausted':
      return 'warn'
    case 'disabled':
      return 'neutral'
    default:
      return 'neutral'
  }
}

export function formatDate(value?: string): string {
  if (!value) {
    return '—'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'medium',
    timeStyle: 'short',
    hour12: false,
  }).format(date)
}

export function shortFingerprint(value?: string): string {
  if (!value) {
    return '—'
  }
  return value.length > 20 ? `${value.slice(0, 12)}…${value.slice(-8)}` : value
}

export function formatFingerprint(value?: string): string {
  if (!value) {
    return '—'
  }
  return value.toUpperCase().replace(/(.{2})(?=.)/g, '$1:')
}

export function profileValidationMessage(name: string, authType: AuthType, username: string): string {
  if (!name.trim()) {
    return '请输入配置名称'
  }
  if (authType !== 'certificate' && !/^[A-Za-z0-9._-]{3,64}$/.test(username.trim())) {
    return '登录名需为 3 到 64 位字母、数字、点、下划线或连字符'
  }
  return ''
}

export function suggestUsername(): string {
  const bytes = new Uint8Array(4)
  crypto.getRandomValues(bytes)
  return 'u-' + Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
}

export function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : '操作失败'
}

// 界面额度与流量使用十进制单位（1 GB = 10^9 B），与文档口径一致。
export function formatBytes(value?: number): string {
  const bytes = Math.max(0, value ?? 0)
  if (bytes < 1000) {
    return `${Math.round(bytes)} B`
  }
  const units = ['KB', 'MB', 'GB', 'TB', 'PB']
  let scaled = bytes / 1000
  let index = 0
  while (scaled >= 1000 && index < units.length - 1) {
    scaled /= 1000
    index++
  }
  return `${scaled >= 100 ? scaled.toFixed(0) : scaled.toFixed(1)} ${units[index]}`
}

export function formatRate(bytesPerSecond?: number): string {
  return `${formatBytes(bytesPerSecond)}/s`
}

export function formatDuration(seconds?: number): string {
  const total = Math.max(0, Math.floor(seconds ?? 0))
  const days = Math.floor(total / 86400)
  const hours = Math.floor((total % 86400) / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const secs = total % 60
  if (days > 0) return `${days} 天 ${hours} 小时`
  if (hours > 0) return `${hours} 小时 ${minutes} 分`
  if (minutes > 0) return `${minutes} 分 ${secs} 秒`
  return `${secs} 秒`
}

export function gbToBytes(gb: number): number {
  return Math.round(Math.max(0, gb) * 1_000_000_000)
}

export function bytesToGb(bytes: number): number {
  return Math.round((Math.max(0, bytes) / 1_000_000_000) * 1000) / 1000
}

export function mbpsToBps(mbps: number): number {
  return Math.round(Math.max(0, mbps) * 1_000_000)
}

export function bpsToMbps(bps: number): number {
  return Math.round((Math.max(0, bps) / 1_000_000) * 100) / 100
}
