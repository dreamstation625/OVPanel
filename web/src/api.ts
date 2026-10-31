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

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body !== undefined && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  let response: Response
  try {
    response = await fetch(path, { ...init, headers, credentials: 'same-origin' })
  } catch {
    throw new ApiError('无法连接面板服务', 'network_error', 0)
  }

  const text = await response.text()
  let payload: unknown = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = null
    }
  }

  if (!response.ok) {
    const envelope = payload as ErrorEnvelope | null
    throw new ApiError(
      envelope?.error?.message ?? `请求失败（HTTP ${response.status}）`,
      envelope?.error?.code ?? 'http_error',
      response.status,
      envelope?.error?.requestId ?? '',
    )
  }

  return payload as T
}

function post<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, { method: 'POST', body: JSON.stringify(body) })
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
  operations: () => request<{ operations: Operation[] }>('/api/operations'),
}
