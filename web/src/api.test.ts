import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { createServer, type Server } from 'node:http'
import { ApiClient } from './api'

// Real HTTP boundary: these checks catch missing CSRF, leaked bearer auth,
// incorrect DTO envelopes, and discarded server diagnostics.
describe('operator API', () => {
  let server: Server
  let url = ''
  const requests: { path: string; headers: Record<string, unknown>; body: string }[] = []
  beforeAll(async () => {
    server = createServer(async (req, res) => {
      let body = ''
      for await (const chunk of req) body += chunk
      requests.push({ path: req.url!, headers: req.headers, body })
      res.setHeader('Content-Type', 'application/json')
      if (req.url === '/api/v1/auth/login') res.end('{"csrf_token":"csrf-proof"}')
      else if (req.url?.endsWith('/approve')) res.end('{"id":"approval_1","status":"approved"}')
      else if (req.url === '/api/v1/tasks') res.end('[{"id":"task_1"}]')
      else { res.statusCode = 409; res.end('{"error":{"code":"session_expired","message":"会话已过期","request_id":"req_1"}}') }
    })
    await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
    const address = server.address()
    if (!address || typeof address === 'string') throw new Error('Missing server address')
    url = `http://127.0.0.1:${address.port}`
  })
  afterAll(() => new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve())))
  it('uses the login CSRF proof for operator approval without bearer credentials', async () => {
    const client = new ApiClient(url)
    await client.login('admin-secret')
    const approval = await client.post<{ status: string }>('/approvals/approval_1/approve', {})
    expect(approval.status).toBe('approved')
    const request = requests.find(r => r.path.endsWith('/approve'))!
    expect(request.headers['x-csrf-token']).toBe('csrf-proof')
    expect(request.headers.authorization).toBeUndefined()
    expect(request.body).toBe('{}')
  })
  it('returns collection DTO arrays directly', async () => {
    expect(await new ApiClient(url).get('/tasks')).toEqual([{ id: 'task_1' }])
  })
  it('preserves server error code and request ID for actionable errors', async () => {
    await expect(new ApiClient(url).post('/sessions/expired/close', {})).rejects.toMatchObject({ code: 'session_expired', requestID: 'req_1', status: 409 })
  })
})
