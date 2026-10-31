import { notifyUnauthorized } from './session'

export interface SetupStatus {
  initialized: boolean
  setupAllowed: boolean
  requiresToken: boolean
  tlsEnabled: boolean
  localRequest: boolean
}

export interface Health {
  status: string
  version: string
  initialized: boolean
}

export interface Admin {
  username: string
  createdAt: string
}

export interface SystemInfo {
  version: string
  commit: string
  buildTime: string
  goVersion: string
  tlsEnabled: boolean
  schemaVersion: number
  sessions: number
  users: number
  profiles: number
  activeCertificates: number
  revokedCertificates: number
  uptimeSeconds: number
  dataStore: string
}

export interface Operation {
  id: string
  kind: string
  state: 'pending' | 'running' | 'succeeded' | 'failed'
  progress: number
  message: string
  errorCode?: string
  startedAt: string
  finishedAt?: string
}

export type AuthType = 'certificate' | 'certificate_password' | 'password'
export type UserStatus = 'active' | 'disabled'
export type RouteMode = 'inherit' | 'split' | 'global'
export type AccessMode = 'inherit' | 'allow' | 'deny'
export type EffectiveStatus =
  | 'active'
  | 'user_disabled'
  | 'profile_disabled'
  | 'certificate_missing'
  | 'certificate_revoked'
  | 'certificate_expired'
  | 'expired'
  | 'quota_exhausted'

export interface User {
  id: string
  displayName: string
  notes: string
  tags: string[]
  status: UserStatus
  profileCount: number
  activeProfileCount: number
  usageBytes: number
  onlineSessions: number
  createdAt: string
  updatedAt: string
}

export interface CertificateInfo {
  id: string
  serialNumber: string
  fingerprint: string
  commonName: string
  status: string
  issuedAt: string
  expiresAt: string
  revokedAt?: string
  revokeReason?: string
}

export interface Profile {
  id: string
  userId: string
  userDisplayName: string
  name: string
  authType: AuthType
  username?: string
  status: UserStatus
  effectiveStatus: EffectiveStatus
  statusReason: string
  expiresAt?: string
  routeMode: RouteMode
  accessMode: AccessMode
  allowedInternal: string[]
  concurrentLimit: number
  quotaBytes: number
  uploadBps: number
  downloadBps: number
  quotaEpoch: number
  usageBytes: number
  usagePercent: number
  usageGaps: number
  usageKnown: boolean
  onlineSessions: number
  quotaResetAt?: string
  certificate?: CertificateInfo
  createdAt: string
  updatedAt: string
}

export interface UserDetail {
  user: User
  profiles: Profile[]
}

export interface ProfileInput {
  name: string
  authType: AuthType
  username?: string
  password?: string
  expiresAt?: string
}

export interface CreateUserInput {
  displayName: string
  notes: string
  tags: string[]
  profile?: ProfileInput
}

export interface CreateUserResult {
  user: User
  profile?: Profile
  password?: string
}

export interface AddProfileResult {
  profile: Profile
  password?: string
}

export interface PKIStatus {
  caExists: boolean
  activeCertificates: number
  revokedCertificates: number
  expiredCertificates: number
  users: number
  profiles: number
  ca?: {
    subject: string
    serial: string
    fingerprint: string
    notBefore: string
    notAfter: string
  }
}

export interface ExportSettings {
  configured: boolean
  host: string
  port: number
  proto: 'udp' | 'tcp'
}

export interface ServerConfig {
  vpnNetwork: string
  protocol: 'udp' | 'tcp'
  port: number
  dns: string[]
  routeMode: 'split' | 'global'
  allowedInternal: string[]
  allowInterClient: boolean
  blockIpv6: boolean
  extraDirectives: string[]
}

export interface ServerConfigRevision {
  revision: number
  state: string
  config: ServerConfig
  rendered: string
  hash: string
  createdAt: string
  appliedAt?: string
}

export interface AgentRuntimeStatus {
  configured: boolean
  stopped?: boolean
  openvpnRunning: boolean
  managementConnected: boolean
  configHash?: string
  sessions: number
  lastError?: string
  rolledBack?: boolean
  rollbackReason?: string
  appliedAt?: string
}

export interface ServerConfigResponse {
  defaults: ServerConfig
  supportedDirectives: string[]
  active: ServerConfigRevision | null
  draft: ServerConfigRevision | null
  agentStatus?: AgentRuntimeStatus
  agentStatusPresent: boolean
}

export interface UserPatch {
  displayName?: string
  notes?: string
  tags?: string[]
  status?: UserStatus
}

export interface ProfilePatch {
  name?: string
  status?: UserStatus
  routeMode?: RouteMode
  accessMode?: AccessMode
  allowedInternal?: string[]
}

export interface ProfileLimitsInput {
  expiresAt: string
  concurrentLimit: number
  quotaBytes: number
  uploadBps: number
  downloadBps: number
}

export interface Connection {
  id: string
  cid: string
  profileId: string
  profileName: string
  userId: string
  userDisplayName: string
  authType: AuthType | ''
  vpnIp: string
  sourceIp: string
  connectedAt: string
  durationSeconds: number
  upBytes: number
  downBytes: number
  upRate: number
  downRate: number
  blocked: boolean
  blockReason?: string
  knownProfile: boolean
}

export interface ConnectionsResponse {
  agentPresent: boolean
  updatedAt?: string
  connections: Connection[]
  online: number
  upRate: number
  downRate: number
}

export interface ConnectionHistoryItem {
  id: string
  profileId: string
  profileName: string
  userDisplayName: string
  vpnIp: string
  sourceIp: string
  connectedAt: string
  disconnectedAt?: string
  durationSeconds: number
  endReason: string
  upBytes: number
  downBytes: number
}

export interface UsagePoint {
  day: string
  upBytes: number
  downBytes: number
}

export interface Overview {
  agentPresent: boolean
  agentUpdatedAt?: string
  onlineConnections: number
  upRate: number
  downRate: number
  todayUpBytes: number
  todayDownBytes: number
  users: number
  profiles: number
  activeCertificates: number
  adminSessions: number
  trend: UsagePoint[]
  connections: Connection[]
}

export class ApiError extends Error {
  readonly code: string
  readonly status: number
  readonly requestId: string

  constructor(message: string, code: string, status: number, requestId = '') {
    super(message)
    this.name = 'ApiError'
    this.code = code
    this.status = status
    this.requestId = requestId
  }
}

interface ErrorEnvelope {
  error?: {
    code?: string
    message?: string
    requestId?: string
  }
}

function csrfHeader(): Record<string, string> {
  const match = document.cookie.match(/(?:^|;\s*)ovpanel_csrf=([^;]+)/)
  return match?.[1] ? { 'X-CSRF-Token': decodeURIComponent(match[1]) } : {}
}

async function parseError(response: Response): Promise<ApiError> {
  const text = await response.text()
  let envelope: ErrorEnvelope | null = null
  if (text) {
    try {
      envelope = JSON.parse(text) as ErrorEnvelope
    } catch {
      envelope = null
    }
  }
  return new ApiError(
    envelope?.error?.message ?? `请求失败（HTTP ${response.status}）`,
    envelope?.error?.code ?? 'http_error',
    response.status,
    envelope?.error?.requestId ?? '',
  )
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body !== undefined && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  const method = (init.method ?? 'GET').toUpperCase()
  if (method !== 'GET' && method !== 'HEAD') {
    for (const [key, value] of Object.entries(csrfHeader())) {
      headers.set(key, value)
    }
  }

  let response: Response
  try {
    response = await fetch(path, { ...init, headers, credentials: 'same-origin' })
  } catch {
    throw new ApiError('无法连接面板服务', 'network_error', 0)
  }

  if (response.status === 401 && path !== '/api/login' && path !== '/api/setup' && path !== '/api/me') {
    notifyUnauthorized()
  }
  if (!response.ok) {
    throw await parseError(response)
  }

  const text = await response.text()
  if (!text) {
    return null as T
  }
  try {
    return JSON.parse(text) as T
  } catch {
    throw new ApiError('服务返回了无法解析的响应', 'invalid_response', response.status)
  }
}

function post<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, { method: 'POST', body: JSON.stringify(body) })
}

function patch<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, { method: 'PATCH', body: JSON.stringify(body) })
}

function put<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, { method: 'PUT', body: JSON.stringify(body) })
}

export const api = {
  health: () => request<Health>('/api/health'),
  setupStatus: () => request<SetupStatus>('/api/setup'),
  setup: (username: string, password: string, token: string) =>
    post<{ username: string }>('/api/setup', { username, password, token }),
  login: (username: string, password: string) =>
    post<{ username: string }>('/api/login', { username, password }),
  logout: () => post<{ ok: boolean }>('/api/logout', {}),
  me: () => request<Admin>('/api/me'),
  system: () => request<SystemInfo>('/api/system'),
  overview: () => request<Overview>('/api/overview'),
  operations: () => request<{ operations: Operation[] }>('/api/operations'),
  connections: () => request<ConnectionsResponse>('/api/connections'),
  connectionHistory: (profileId = '', limit = 100) => {
    const params = new URLSearchParams({ limit: String(limit) })
    if (profileId) params.set('profileId', profileId)
    return request<{ sessions: ConnectionHistoryItem[] }>(`/api/connections/history?${params}`)
  },
  disconnectConnection: (id: string) =>
    post<{ ok: boolean }>(`/api/connections/${encodeURIComponent(id)}/disconnect`, {}),
  usageTrend: (days = 30) => request<{ trend: UsagePoint[] }>(`/api/usage/trend?days=${days}`),

  listUsers: (query = '', status = '') => {
    const params = new URLSearchParams()
    if (query) params.set('query', query)
    if (status) params.set('status', status)
    const suffix = params.toString()
    return request<{ users: User[] }>(`/api/users${suffix ? '?' + suffix : ''}`)
  },
  createUser: (input: CreateUserInput) => post<CreateUserResult>('/api/users', input),
  getUser: (id: string) => request<UserDetail>(`/api/users/${encodeURIComponent(id)}`),
  updateUser: (id: string, patchBody: UserPatch) =>
    patch<UserDetail>(`/api/users/${encodeURIComponent(id)}`, patchBody),
  addProfile: (userId: string, input: ProfileInput) =>
    post<AddProfileResult>(`/api/users/${encodeURIComponent(userId)}/profiles`, input),

  getProfile: (id: string) => request<Profile>(`/api/profiles/${encodeURIComponent(id)}`),
  updateProfile: (id: string, patchBody: ProfilePatch) =>
    patch<Profile>(`/api/profiles/${encodeURIComponent(id)}`, patchBody),
  updateProfileLimits: (id: string, input: ProfileLimitsInput) =>
    put<{ profile: Profile }>(`/api/profiles/${encodeURIComponent(id)}/limits`, input),
  resetQuota: (id: string, adminPassword: string) =>
    post<{ profile: Profile; epoch: number }>(`/api/profiles/${encodeURIComponent(id)}/quota/reset`, {
      adminPassword,
    }),
  resetProfilePassword: (id: string, adminPassword: string, password = '') =>
    post<{ profile: Profile; password: string }>(`/api/profiles/${encodeURIComponent(id)}/password`, {
      adminPassword,
      password,
    }),
  rotateCertificate: (id: string, adminPassword: string) =>
    post<{ profile: Profile }>(`/api/profiles/${encodeURIComponent(id)}/certificate/rotate`, {
      adminPassword,
    }),
  revokeCertificate: (id: string, adminPassword: string, reason = 'manual') =>
    post<{ profile: Profile }>(`/api/profiles/${encodeURIComponent(id)}/certificate/revoke`, {
      adminPassword,
      reason,
    }),
  exportProfile: async (id: string, adminPassword: string) => {
    const headers = new Headers({ 'Content-Type': 'application/json', ...csrfHeader() })
    let response: Response
    try {
      response = await fetch(`/api/profiles/${encodeURIComponent(id)}/export`, {
        method: 'POST',
        headers,
        credentials: 'same-origin',
        body: JSON.stringify({ adminPassword }),
      })
    } catch {
      throw new ApiError('无法连接面板服务', 'network_error', 0)
    }
    if (response.status === 401) {
      notifyUnauthorized()
    }
    if (!response.ok) {
      throw await parseError(response)
    }
    const blob = await response.blob()
    const disposition = response.headers.get('Content-Disposition') ?? ''
    const match = /filename="([^"]+)"/.exec(disposition)
    return { blob, filename: match?.[1] ?? 'ovpanel.ovpn' }
  },

  getServerConfig: () => request<ServerConfigResponse>('/api/server/config'),
  previewServerConfig: (config: ServerConfig) =>
    post<{ config: ServerConfig; rendered: string; hash: string }>('/api/server/config/preview', config),
  saveServerDraft: (config: ServerConfig) =>
    request<{ draft: ServerConfigRevision }>('/api/server/config/draft', {
      method: 'PUT',
      body: JSON.stringify(config),
    }),
  applyServerConfig: (adminPassword: string) =>
    post<{ result: unknown; agentStatus?: AgentRuntimeStatus }>('/api/server/config/apply', {
      adminPassword,
    }),
  serverCommand: (action: 'start' | 'stop' | 'restart', adminPassword: string) =>
    post<{ ok: boolean; action: string }>('/api/server/command', { action, adminPassword }),

  pkiStatus: () => request<PKIStatus>('/api/pki'),
  getExportSettings: () => request<ExportSettings>('/api/settings/export'),
  saveExportSettings: (settings: { host: string; port: number; proto: string }) =>
    request<ExportSettings>('/api/settings/export', {
      method: 'PUT',
      body: JSON.stringify(settings),
    }),
  changeAdminPassword: (currentPassword: string, newPassword: string) =>
    post<{ ok: boolean }>('/api/admin/password', { currentPassword, newPassword }),
}
