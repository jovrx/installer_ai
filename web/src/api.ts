import { LocalizedFailure, text } from './i18n'

const knownCodes = new Set(['unauthorized', 'invalid_response', 'network_unavailable', 'session_expired', 'not_found', 'internal_error', 'invalid_request', 'unavailable', 'origin_rejected', 'session_limit', 'csrf_rejected', 'invalid_config', 'operator_confirmation_required', 'cleanup_not_pending', 'invalid_scope', 'pairing_unavailable', 'invalid_task', 'plan_expired', 'approval_inactive', 'invalid_idempotency_key', 'session_inactive', 'idempotency_conflict', 'approval_required', 'approval_mismatch', 'invalid_cursor', 'invalid_limit'])
export class ApiFailure extends Error {
  constructor(readonly status: number, readonly code: string, message: string, readonly requestID = '') {
    super()
    // Server message bodies can contain private or untrusted data. Only stable
    // categories and the diagnostic request ID enter browser feedback.
    void message
    Object.defineProperty(this, 'message', { get: () => {
      const explanation = knownCodes.has(code) ? text('web.api.' + code) : text('web.api.unknown', status)
      return requestID ? text('web.api.request', explanation, requestID) : explanation
    } })
    this.name = 'ApiFailure'
  }
}

// Only same-origin browser cookies authorize this client. CSRF stays in memory.
export class ApiClient {
  private csrf = ''
  constructor(readonly base = '') {}
  clear() { this.csrf = '' }
  async login(token: string): Promise<void> {
    const response = await this.request<{ csrf_token: string }>('/auth/login', 'POST', { token })
    if (!response.csrf_token) throw new LocalizedFailure('web.api.missing_csrf')
    this.csrf = response.csrf_token
  }
  get<T>(path: string) { return this.request<T>(path, 'GET') }
  post<T>(path: string, body: unknown) { return this.request<T>(path, 'POST', body) }
  put<T>(path: string, body: unknown) { return this.request<T>(path, 'PUT', body) }
  private async request<T>(path: string, method: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = { Accept: 'application/json' }
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    if (method !== 'GET' && this.csrf) headers['X-CSRF-Token'] = this.csrf
    let response: Response
    try {
      response = await fetch(`${this.base}/api/v1${path}`, { method, headers, credentials: 'same-origin', cache: 'no-store', body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(15000) })
    } catch {
      throw new LocalizedFailure('web.api.connection')
    }
    const text = await response.text()
    let payload: unknown
    try { payload = text ? JSON.parse(text) : undefined } catch { throw new ApiFailure(response.status, 'invalid_response', '') }
    if (!response.ok) {
      const envelope = payload as { error?: { code?: string; message?: string; request_id?: string } } | undefined
      throw new ApiFailure(response.status, envelope?.error?.code ?? 'request_failed', '', envelope?.error?.request_id)
    }
    return payload as T
  }
}
