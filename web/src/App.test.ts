// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import App from './App.vue'
import { approval, catalog, config, device, pairing, plan, session } from './test-fixtures'

describe('management UI', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })
  it('renders cleanup explanations from session state and switches existing rows', async () => {
    localStorage.setItem('remoteops.language', 'en-US')
    const sessions = ['Node cleanup queued', 'Tailscale node cleanup is not configured', 'unrecognized diagnostic'].map((cleanup_error, i) => ({ ...session, id: `cleanup_${i}`, state: 'cleanup_pending', cleanup_error }))
    const original = JSON.stringify(sessions)
    const requests: string[] = []
    vi.stubGlobal('fetch', async (url: string) => {
      requests.push(url)
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'proof' }, '/sessions': sessions, '/devices': [device], '/tasks': [], '/plans': [], '/approvals': [], '/catalog': catalog, '/audit-events': [], '/config': config }
      return new Response(JSON.stringify(values[url.replace('/api/v1', '')]))
    })
    const wrapper = mount(App)
    try {
      await wrapper.get('input[type=password]').setValue('test-token')
      await wrapper.get('form').trigger('submit'); await flushPromises()
      await wrapper.findAll('nav button').find(b => b.text().includes('Sessions'))!.trigger('click')
      const rows = wrapper.findAll('tbody tr')
      expect(rows).toHaveLength(3)
      for (const row of rows) expect(row.text()).toContain('Session authorization has ended; node cleanup is pending. Retry cleanup if needed.')
      const count = requests.length
      await wrapper.get('.topbar .language-select select').setValue('zh-CN')
      for (const row of wrapper.findAll('tbody tr')) expect(row.text()).toContain('会话授权已结束，节点清理尚未完成。可重试清理。')
      expect(wrapper.text()).not.toContain('Node cleanup queued')
      expect(wrapper.text()).not.toContain('Tailscale node cleanup is not configured')
      expect(wrapper.text()).not.toContain('unrecognized diagnostic')
      expect(requests).toHaveLength(count)
      expect(JSON.stringify(sessions)).toBe(original)
    } finally { wrapper.unmount() }
  })
  it('loads English on login and switches existing errors without exposing server text', async () => {
    localStorage.setItem('remoteops.language', 'en-US')
    vi.stubGlobal('fetch', async () => new Response('{"error":{"code":"unauthorized","message":"private-secret","request_id":"req_1"}}', { status: 401 }))
    const wrapper = mount(App)
    try {
      expect(wrapper.text()).toContain('Sign in to workspace')
      expect(document.documentElement.lang).toBe('en-US')
      expect(document.title).toContain('Remote operations workspace')
      expect(wrapper.get('input[type=password]').attributes('placeholder')).toBe('Enter management token')
      await wrapper.get('input[type=password]').setValue('admin-secret')
      await wrapper.get('form').trigger('submit'); await flushPromises()
      expect(wrapper.get('[role=alert]').text()).toContain('Invalid management token')
      expect(wrapper.get('[role=alert]').text()).toContain('req_1')
      await wrapper.get('.language-select select').setValue('zh-CN')
      expect(wrapper.get('[role=alert]').text()).toContain('管理令牌无效')
      expect(wrapper.text()).not.toContain('private-secret')
      expect(localStorage.getItem('remoteops.language')).toBe('zh-CN')
    } finally { wrapper.unmount() }
  })
  it('presents all five English views and preserves local state while switching', async () => {
    localStorage.setItem('remoteops.language', 'en-US')
    vi.stubGlobal('fetch', async (url: string) => {
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'proof' }, '/sessions': [session], '/devices': [device], '/tasks': [], '/plans': [{ ...plan, steps: ['validate_authorization', 'verify_artifact', 'install_pinned_package', 'verify_version'] }], '/approvals': [approval], '/catalog': catalog, '/audit-events': [], '/config': config, '/network-status': { state: 'connected', addresses: ['100.64.0.1'] } }
      return new Response(JSON.stringify(values[url.replace('/api/v1', '')]))
    })
    const wrapper = mount(App)
    try {
      await wrapper.get('input[type=password]').setValue('test-token')
      await wrapper.get('form').trigger('submit'); await flushPromises()
      const headings = ['Devices', 'Sessions', 'Tasks and approvals', 'Configuration', 'Audit']
      for (const heading of headings) {
        await wrapper.findAll('nav button').find(b => b.text().includes(heading))!.trigger('click')
        expect(wrapper.get('.page-heading h1').text()).toBe(heading)
        expect(wrapper.get('.content').text().replace('简体中文', '')).not.toMatch(/[\u4e00-\u9fff]/)
      }
      await wrapper.findAll('nav button').find(b => b.text().includes('Configuration'))!.trigger('click')
      await wrapper.findAll('input[type=number]')[0]!.setValue(4)
      await wrapper.get('.topbar .language-select select').setValue('zh-CN')
      expect((wrapper.findAll('input[type=number]')[0]!.element as HTMLInputElement).value).toBe('4')
      expect(wrapper.get('.network-card').text()).toContain('100.64.0.1')
      expect(wrapper.get('.page-heading h1').text()).toBe('配置')
    } finally { wrapper.unmount() }
  })
  it('translates administrator warnings in installation scope and authorized package selection', async () => {
    localStorage.setItem('remoteops.language', 'en-US')
    vi.stubGlobal('fetch', async (url: string) => {
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'proof' }, '/sessions': [session], '/devices': [device], '/tasks': [], '/plans': [], '/approvals': [], '/catalog': catalog, '/audit-events': [], '/config': config }
      return new Response(JSON.stringify(values[url.replace('/api/v1', '')]))
    })
    const wrapper = mount(App)
    try {
      await wrapper.get('input[type=password]').setValue('test-token')
      await wrapper.get('form').trigger('submit'); await flushPromises()
      await wrapper.findAll('nav button').find(b => b.text().includes('Sessions'))!.trigger('click')
      await wrapper.get('input[value=install_package]').setValue(true)
      const packageLabel = wrapper.get('input[value=git]').element.closest('label')!
      expect(packageLabel.textContent).toContain('Requires administrator permissions')
      expect(packageLabel.textContent).not.toMatch(/[\u4e00-\u9fff]/)
      await wrapper.findAll('nav button').find(b => b.text().includes('Tasks and approvals'))!.trigger('click')
      await wrapper.get('.task-form select').setValue(device.id)
      const packageOption = wrapper.get('.task-form option[value=git]')
      expect(packageOption.text()).toContain('Administrator permissions')
      expect(packageOption.text()).not.toMatch(/[\u4e00-\u9fff]/)
    } finally { wrapper.unmount() }
  })
  it('localizes a successful unavailable-network DTO and updates it on runtime switching', async () => {
    localStorage.setItem('remoteops.language', 'en-US')
    vi.stubGlobal('fetch', async (url: string) => {
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'proof' }, '/sessions': [], '/devices': [], '/tasks': [], '/plans': [], '/approvals': [], '/catalog': [], '/audit-events': [], '/config': config, '/network-status': { state: 'unavailable', addresses: [], error: '无法读取本机 Tailscale 状态，token=private-secret' } }
      return new Response(JSON.stringify(values[url.replace('/api/v1', '')]))
    })
    const wrapper = mount(App)
    try {
      await wrapper.get('input[type=password]').setValue('test-token')
      await wrapper.get('form').trigger('submit'); await flushPromises()
      await wrapper.findAll('nav button').find(b => b.text().includes('Configuration'))!.trigger('click')
      expect(wrapper.get('.network-card .warning').text()).toContain('Unable to read local Tailscale status')
      expect(wrapper.get('.network-card').text()).not.toMatch(/[\u4e00-\u9fff]/)
      await wrapper.get('.topbar select').setValue('zh-CN')
      expect(wrapper.get('.network-card .warning').text()).toContain('无法读取本机 Tailscale 状态')
      expect(wrapper.get('.network-card').text()).not.toContain('private-secret')
      expect(wrapper.findAll('input[type=number]')).toHaveLength(7)
    } finally { wrapper.unmount() }
  })
  it('switches cached success feedback, saves the browser preference and reloads it', async () => {
    vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
      const path = url.replace('/api/v1', '')
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'proof' }, '/sessions': [], '/devices': [], '/tasks': [], '/plans': [], '/approvals': [], '/catalog': [], '/audit-events': [], '/config': config, '/network-status': { state: 'connected', addresses: [] } }
      return new Response(JSON.stringify(path === '/config' && init.method === 'PUT' ? JSON.parse(String(init.body)) : values[path]))
    })
    const wrapper = mount(App)
    try {
      await wrapper.get('input[type=password]').setValue('test-token')
      await wrapper.get('form').trigger('submit'); await flushPromises()
      await wrapper.findAll('nav button').find(b => b.text().includes('配置'))!.trigger('click')
      await wrapper.findAll('input[type=number]')[0]!.setValue(4)
      await wrapper.get('form').trigger('submit'); await flushPromises()
      expect(wrapper.get('.alert.success').text()).toContain('配置已保存')
      await wrapper.get('.language-card select').setValue('en-US')
      expect(wrapper.get('.alert.success').text()).toContain('Configuration saved')
      expect(localStorage.getItem('remoteops.language')).toBe('en-US')
    } finally { wrapper.unmount() }
    const reloaded = mount(App)
    expect(reloaded.text()).toContain('Sign in to workspace')
    expect(document.documentElement.lang).toBe('en-US')
    reloaded.unmount()
  })
  it('clears an unsubmitted token and shows a recoverable warning on failed preference saves', async () => {
    const wrapper = mount(App)
    try {
      await wrapper.get('input[type=password]').setValue('private-secret')
      vi.stubGlobal('localStorage', { setItem: () => { throw new Error('private-secret') } })
      await wrapper.get('.language-select select').setValue('en-US')
      expect((wrapper.get('input[type=password]').element as HTMLInputElement).value).toBe('')
      expect(wrapper.get('[role=status]').text()).toContain('This switch still applies')
      expect(wrapper.text()).not.toContain('private-secret')
      expect(wrapper.text()).toContain('Sign in to workspace')
    } finally { wrapper.unmount() }
  })
  it('localizes task diagnostics and audit actions while preserving external details and IDs', async () => {
    localStorage.setItem('remoteops.language', 'en-US')
    vi.stubGlobal('fetch', async (url: string) => {
      const path = url.replace('/api/v1', '')
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'proof' }, '/sessions': [session], '/devices': [device], '/tasks': [{ id: 'task_1', spec: plan.spec, state: 'failed', created_at: plan.created_at, error_code: 'permission_denied', error_message: 'token=private-secret OS details' }], '/plans': [], '/approvals': [], '/catalog': catalog, '/audit-events': [{ id: 'audit_1', timestamp: plan.created_at, actor: 'system', action: 'task_finished', task_id: 'task_1', result: 'failed' }], '/config': config }
      return new Response(JSON.stringify(path.startsWith('/tasks/') ? { entries: [], next_after: 0, truncated: false } : values[path]))
    })
    const wrapper = mount(App)
    try {
      await wrapper.get('input[type=password]').setValue('test-token')
      await wrapper.get('form').trigger('submit'); await flushPromises()
      await wrapper.findAll('nav button').find(b => b.text().includes('Tasks and approvals'))!.trigger('click')
      await wrapper.findAll('button').find(b => b.text().includes('Status and logs'))!.trigger('click'); await flushPromises()
      expect(wrapper.text()).toContain('Local administrator permission was declined or unavailable')
      expect(wrapper.text()).toContain('OS details')
      expect(wrapper.text()).not.toContain('private-secret')
      await wrapper.findAll('nav button').find(b => b.text().includes('Audit'))!.trigger('click')
      expect(wrapper.get('tbody').text()).toContain('Task finished')
      expect(wrapper.get('tbody').text()).toContain('System')
      expect(wrapper.get('tbody').text()).toContain('task_1')
      expect(wrapper.get('tbody').text()).not.toContain('task_finished')
    } finally { wrapper.unmount() }
  })
  it('clears the management token after login and presents empty device state', async () => {
    // Fetch is the external HTTP boundary. Components and ApiClient remain real.
    vi.stubGlobal('fetch', async (url: string) => new Response(JSON.stringify(url.endsWith('/auth/login') ? { csrf_token: 'proof' } : url.endsWith('/config') ? { max_concurrency: 3, task_timeout_seconds: 1800, pairing_ttl_seconds: 600, session_ttl_seconds: 3600, heartbeat_ttl_seconds: 45, log_max_bytes: 1048576, retention_days: 30 } : []), { headers: { 'Content-Type': 'application/json' } }))
    const wrapper = mount(App)
    await wrapper.get('input[type=password]').setValue('admin-secret')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('暂无已连接设备')
    expect(wrapper.find('input[type=password]').exists()).toBe(false)
    expect(localStorage.length).toBe(0)
    wrapper.unmount()
  })
  it('shows an authentication error without exposing the entered token', async () => {
    vi.stubGlobal('fetch', async () => new Response('{"error":{"code":"unauthorized","message":"管理令牌无效"}}', { status: 401 }))
    const wrapper = mount(App)
    await wrapper.get('input[type=password]').setValue('private-secret')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role=alert]').text()).toContain('管理令牌无效')
    expect(wrapper.get('[role=alert]').text()).not.toContain('private-secret')
    wrapper.unmount()
  })
  it('requires explicit operator approval and executes the frozen plan with a stable retry key', async () => {
    let status = 'pending', failFirstTask = true
    const writes: { path: string; body: any; csrf: unknown }[] = []
    const confirm = vi.fn((_message?: string) => true)
    vi.stubGlobal('confirm', confirm)
    vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
      const path = url.replace('/api/v1', '')
      if (init.method !== 'GET') {
        writes.push({ path, body: JSON.parse(String(init.body)), csrf: (init.headers as Record<string, string>)['X-CSRF-Token'] })
        if (path.endsWith('/approve')) status = 'approved'
        if (path === '/tasks' && failFirstTask) { failFirstTask = false; throw new Error('Connection lost') }
      }
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'operator-csrf' }, '/sessions': [session], '/devices': [device], '/tasks': [], '/plans': [plan], '/approvals': [{ ...approval, status }], '/catalog': catalog, '/audit-events': [], '/config': config, '/approvals/approval_1/approve': { ...approval, status } }
      const value = path.startsWith('/tasks/') ? { entries: [], next_after: 0, truncated: false } : path === '/tasks' && init.method === 'POST' ? { id: 'task_1' } : values[path]
      return new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } })
    })
    const wrapper = mount(App)
    await wrapper.get('input[type=password]').setValue('test-token')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    const button = (text: string) => wrapper.findAll('button').find(b => b.text().includes(text))!
    await button('任务与审批').trigger('click')
    expect(wrapper.text()).toContain('2.50.1')
    expect(wrapper.text()).toContain('需要本地确认')
    expect(button('创建任务并执行')).toBeUndefined()
    await button('确认批准此计划').trigger('click'); await flushPromises()
    expect(confirm.mock.calls[0]![0]).toContain('会话：session_1')
    expect(confirm.mock.calls[0]![0]).toContain('设备：device_1')
    expect(confirm.mock.calls[0]![0]).toContain('软件：git 2.50.1')
    expect(writes.find(r => r.path.endsWith('/approve'))?.csrf).toBe('operator-csrf')
    // Change the unrelated task draft; execution must still use the approved plan.
    await wrapper.get('input[type=number]').setValue(30)
    await button('创建任务并执行').trigger('click'); await flushPromises()
    expect(wrapper.get('[role=alert]').text()).toContain('无法连接 Controller')
    await button('创建任务并执行').trigger('click'); await flushPromises()
    const taskWrites = writes.filter(r => r.path === '/tasks')
    expect(taskWrites).toHaveLength(2)
    expect(taskWrites[0]!.body.spec).toEqual({ session_id: 'session_1', device_id: 'device_1', type: 'install_package', package: { id: 'git', version: '2.50.1' }, timeout_seconds: 1800, requires_admin: true })
    expect(taskWrites[1]!.body.idempotency_key).toBe(taskWrites[0]!.body.idempotency_key)
    expect(taskWrites[1]!.body.approval_id).toBe('approval_1')
    wrapper.unmount()
  })
  it('hides execution when an approval was consumed even if its status remains approved', async () => {
    vi.stubGlobal('fetch', async (url: string) => {
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'proof' }, '/sessions': [session], '/devices': [device], '/tasks': [], '/plans': [plan], '/approvals': [{ ...approval, status: 'approved', consumed_at: '2026-10-11T03:01:00Z' }], '/catalog': catalog, '/audit-events': [], '/config': config }
      return new Response(JSON.stringify(values[url.replace('/api/v1', '')]))
    })
    const wrapper = mount(App)
    await wrapper.get('input[type=password]').setValue('test-token')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.findAll('button').find(b => b.text().includes('任务与审批'))!.trigger('click')
    expect(wrapper.text()).toContain('已使用')
    expect(wrapper.findAll('button').some(b => b.text().includes('创建任务并执行'))).toBe(false)
    wrapper.unmount()
  })
  it('shows the precise pairing authorization duration independently of current configuration', async () => {
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:test-pairing')
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
      const path = url.replace('/api/v1', '')
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'proof' }, '/sessions': [session], '/devices': [], '/tasks': [], '/plans': [], '/approvals': [], '/catalog': catalog, '/audit-events': [], '/config': { ...config, session_ttl_seconds: 7200 } }
      return new Response(JSON.stringify(path === '/sessions' && init.method === 'POST' ? { session, pairing } : values[path]))
    })
    const wrapper = mount(App)
    await wrapper.get('input[type=password]').setValue('test-token')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.findAll('button').find(b => b.text().includes('会话'))!.trigger('click')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    const bundleCard = wrapper.get('.pairing-card')
    expect(bundleCard.text()).toContain('连接后授权时长')
    expect(bundleCard.text()).toContain('3661 秒')
    expect(bundleCard.text()).not.toContain('7200 秒')
    expect(bundleCard.text()).not.toContain('pairing-secret')
    expect(bundleCard.findAll('button').some(b => b.text().includes('下载配对包 JSON'))).toBe(true)
    wrapper.unmount()
  })
  it('focuses the pairing download after creation and offers it on the matching session row', async () => {
    const pendingSession = { ...session, state: 'pending_pair', device_id: undefined }
    let created = false
    vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
      const path = url.replace('/api/v1', '')
      if (path === '/sessions' && init.method === 'POST') {
        created = true
        return new Response(JSON.stringify({ session: pendingSession, pairing }))
      }
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'proof' }, '/sessions': created ? [pendingSession, { ...pendingSession, id: 'another-session' }] : [], '/devices': [], '/tasks': [], '/plans': [], '/approvals': [], '/catalog': catalog, '/audit-events': [], '/config': config }
      return new Response(JSON.stringify(values[path]))
    })
    const scroll = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
    const objectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:test-pairing')
    const anchorClick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const wrapper = mount(App, { attachTo: document.body })
    try {
      await wrapper.get('input[type=password]').setValue('test-token')
      await wrapper.get('form').trigger('submit'); await flushPromises()
      await wrapper.findAll('nav button').find(b => b.text().includes('会话'))!.trigger('click')
      await wrapper.get('form').trigger('submit'); await flushPromises()
      const download = wrapper.get('.pairing-download button')
      expect(anchorClick).toHaveBeenCalledOnce()
      expect(document.activeElement).toBe(download.element)
      expect(scroll).toHaveBeenCalled()
      const rows = wrapper.findAll('tbody tr')
      expect(rows[0]!.findAll('button').some(b => b.text().includes('下载配对文件'))).toBe(true)
      expect(rows[1]!.findAll('button').some(b => b.text().includes('下载配对文件'))).toBe(false)
      await rows[0]!.findAll('button').find(b => b.text().includes('下载配对文件'))!.trigger('click')
      expect(anchorClick).toHaveBeenCalledTimes(2)
      expect((anchorClick.mock.contexts[0] as HTMLAnchorElement).download).toBe(`remoteops-pairing-${pairing.session_id}.json`)
      expect(JSON.parse(await (objectURL.mock.calls[0]![0] as Blob).text())).toEqual(pairing)
      await wrapper.findAll('button').find(b => b.text().includes('清除页面中的配对包'))!.trigger('click')
      expect(wrapper.findAll('tbody button').some(b => b.text().includes('下载配对文件'))).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
  it('offers regeneration for an existing pending session after reload and downloads only after confirmation', async () => {
    const pendingSession = { ...session, state: 'pending_pair', device_id: undefined, pairing_expires_at: pairing.expires_at }
    const writes: { path: string; body: unknown; csrf: unknown }[] = []
    const confirm = vi.fn((_message?: string) => false)
    vi.stubGlobal('confirm', confirm)
    const objectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:regenerated')
    const anchorClick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
      const path = url.replace('/api/v1', '')
      if (path.endsWith('/pairing')) {
        writes.push({ path, body: JSON.parse(String(init.body)), csrf: (init.headers as Record<string, string>)['X-CSRF-Token'] })
        return new Response(JSON.stringify(pairing))
      }
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'proof' }, '/sessions': [pendingSession, { ...pendingSession, id: 'expired-session', pairing_expires_at: '2000-01-01T00:00:00Z' }, session], '/devices': [], '/tasks': [], '/plans': [], '/approvals': [], '/catalog': catalog, '/audit-events': [], '/config': config }
      return new Response(JSON.stringify(values[path]))
    })
    const wrapper = mount(App)
    try {
      await wrapper.get('input[type=password]').setValue('test-token')
      await wrapper.get('form').trigger('submit'); await flushPromises()
      await wrapper.findAll('nav button').find(b => b.text().includes('会话'))!.trigger('click')
      expect(wrapper.find('.pairing-download').exists()).toBe(false)
      const buttons = wrapper.findAll('tbody button').filter(b => b.text().includes('重新生成并下载'))
      expect(buttons).toHaveLength(1)
      await buttons[0]!.trigger('click'); await flushPromises()
      expect(writes).toHaveLength(0)
      expect(anchorClick).not.toHaveBeenCalled()
      confirm.mockReturnValue(true)
      await buttons[0]!.trigger('click'); await flushPromises()
      expect(confirm.mock.calls[0]![0]).toContain('旧配对文件将立即失效')
      expect(writes).toEqual([{ path: `/sessions/${session.id}/pairing`, body: {}, csrf: 'proof' }])
      expect(anchorClick).toHaveBeenCalledOnce()
      expect((anchorClick.mock.contexts[0] as HTMLAnchorElement).download).toBe(`remoteops-pairing-${pairing.session_id}.json`)
      expect(JSON.parse(await (objectURL.mock.calls[0]![0] as Blob).text())).toEqual(pairing)
      expect(localStorage.length).toBe(0)
    } finally { wrapper.unmount() }
  })
  it('shows authenticated Tailscale connection state and addresses in configuration', async () => {
    vi.stubGlobal('fetch', async (url: string) => {
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'proof' }, '/sessions': [], '/devices': [], '/tasks': [], '/plans': [], '/approvals': [], '/catalog': [], '/audit-events': [], '/config': config, '/network-status': { state: 'connected', addresses: ['100.64.0.1'] } }
      return new Response(JSON.stringify(values[url.replace('/api/v1', '')]))
    })
    const wrapper = mount(App)
    await wrapper.get('input[type=password]').setValue('test-token')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.findAll('button').find(b => b.text().includes('配置'))!.trigger('click')
    const networkCard = wrapper.get('.network-card')
    expect(networkCard.text()).toContain('Tailscale 连接状态')
    expect(networkCard.text()).toContain('已连接')
    expect(networkCard.text()).toContain('100.64.0.1')
    wrapper.unmount()
  })
  it('keeps configuration usable when the network status endpoint is unavailable', async () => {
    vi.stubGlobal('fetch', async (url: string) => {
      if (url.endsWith('/network-status')) return new Response('{"error":{"code":"network_unavailable","message":"Tailscale 未运行"}}', { status: 503 })
      const values: Record<string, unknown> = { '/auth/login': { csrf_token: 'proof' }, '/sessions': [], '/devices': [], '/tasks': [], '/plans': [], '/approvals': [], '/catalog': [], '/audit-events': [], '/config': config }
      return new Response(JSON.stringify(values[url.replace('/api/v1', '')]))
    })
    const wrapper = mount(App)
    await wrapper.get('input[type=password]').setValue('test-token')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.findAll('button').find(b => b.text().includes('配置'))!.trigger('click')
    expect(wrapper.get('.network-card').text()).toContain('状态不可用')
    expect(wrapper.get('.network-card').text()).toContain('Tailscale 未运行')
    expect(wrapper.findAll('input[type=number]')).toHaveLength(7)
    expect(wrapper.find('[role=alert]').exists()).toBe(false)
    wrapper.unmount()
  })
})
