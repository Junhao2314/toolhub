export type Dict = Record<string, unknown>

export interface User {
  id: string
  email: string
  displayName: string
  roles: string[]
  disabled?: boolean
  createdAt?: string
}

export interface Session {
  user: User
  csrfToken?: string
  expiresAt?: string
}

export class APIError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message)
  }
}

class ToolHubClient {
  private csrf = sessionStorage.getItem('toolhub.csrf') ?? ''

  async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers)
    if (init.body && !(init.body instanceof FormData)) headers.set('Content-Type', 'application/json')
    if (this.csrf && init.method && !['GET', 'HEAD'].includes(init.method)) headers.set('X-CSRF-Token', this.csrf)
    const response = await fetch(`/api/v1${path}`, { ...init, headers, credentials: 'same-origin' })
    if (response.status === 204) return undefined as T
    const payload = await response.json().catch(() => ({})) as Dict
    if (!response.ok) {
      const error = (payload.error ?? {}) as Dict
      throw new APIError(response.status, String(error.code ?? 'request_failed'), String(error.message ?? `HTTP ${response.status}`))
    }
    return payload as T
  }

  async login(email: string, password: string): Promise<Session> {
    const session = await this.request<Session>('/auth/login', { method: 'POST', body: JSON.stringify({ email, password }) })
    this.setCSRF(session.csrfToken ?? '')
    return session
  }

  async bootstrap(): Promise<Session> {
    const session = await this.request<Session & { authenticated: boolean }>('/auth/session')
    if (!session.authenticated) throw new APIError(401, 'unauthenticated', 'Authentication is required')
    this.setCSRF(session.csrfToken ?? '')
    return session
  }

  async logout(): Promise<void> {
    await this.request('/auth/logout', { method: 'POST' })
    this.setCSRF('')
  }

  list<T>(path: string): Promise<{ items: T[] }> { return this.request(path) }
  get<T>(path: string): Promise<T> { return this.request(path) }
  post<T>(path: string, body: unknown = {}): Promise<T> { return this.request(path, { method: 'POST', body: JSON.stringify(body) }) }
  patch<T>(path: string, body: unknown): Promise<T> { return this.request(path, { method: 'PATCH', body: JSON.stringify(body) }) }
  delete(path: string): Promise<void> { return this.request(path, { method: 'DELETE' }) }

  async uploadSkill(file: File): Promise<Dict> {
    const form = new FormData()
    form.append('file', file)
    return this.request('/skills/upload', { method: 'POST', body: form })
  }

  private setCSRF(value: string) {
    this.csrf = value
    if (value) sessionStorage.setItem('toolhub.csrf', value)
    else sessionStorage.removeItem('toolhub.csrf')
  }
}

export const api = new ToolHubClient()
