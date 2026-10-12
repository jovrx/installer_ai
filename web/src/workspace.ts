import { computed, onUnmounted, reactive, ref } from 'vue'
import { ApiClient, ApiFailure } from './api'
import { language, LocalizedFailure, number, text } from './i18n'
import type { Approval, AuditEvent, CatalogEntry, Config, CreateSessionResponse, Device, LogEntry, LogsPage, NetworkStatus, PairingBundle, Plan, Scope, Session, Task, TaskSpec, TaskType } from './types'

export const stateNames: Record<string, string> = { pending_pair: 'web.state.pending_pair', connecting: 'web.state.connecting', online: 'web.state.online', running: 'web.state.running', revoking: 'web.state.revoking', closed: 'web.state.closed', cleanup_pending: 'web.state.cleanup_pending', expired: 'web.state.expired', failed: 'web.state.failed', pending_approval: 'web.state.pending_approval', pending: 'web.state.pending', approved: 'web.state.approved', rejected: 'web.state.rejected', consumed: 'web.state.consumed', queued: 'web.state.queued', succeeded: 'web.state.succeeded', cancelled: 'web.state.cancelled', timed_out: 'web.state.timed_out', unknown: 'web.state.unknown' }
export const date = (value?: string) => !value || value.startsWith('0001-') ? '—' : new Date(value).toLocaleString(language.value, { hour12: false })
export const size = (bytes: number) => bytes >= 1073741824 ? `${number(bytes / 1073741824, { minimumFractionDigits: 1, maximumFractionDigits: 1 })} GB` : `${number(bytes / 1048576, { maximumFractionDigits: 0 })} MB`
export const active = (session?: Session) => !!session && ['online', 'running'].includes(session.state) && new Date(session.expires_at).getTime() > Date.now()
const alive = (expires: string) => new Date(expires).getTime() > Date.now()

export function useWorkspace() {
  const api = new ApiClient()
  const authenticated = ref(false), token = ref(''), busy = ref(false), loading = ref(false), errorCause = ref<unknown>(), noticeKey = ref('')
  const error = computed({ get: () => errorCause.value ? errorCause.value instanceof LocalizedFailure || errorCause.value instanceof ApiFailure ? errorCause.value.message : text('common.error.generic') : '', set: (_value: string) => { errorCause.value = undefined } })
  const notice = computed({ get: () => noticeKey.value ? text(noticeKey.value) : '', set: (_value: string) => { noticeKey.value = '' } })
  const view = ref('devices'), search = ref(''), lastUpdatedAt = ref<Date>()
  const lastUpdated = computed(() => lastUpdatedAt.value?.toLocaleTimeString(language.value, { hour12: false }) ?? '')
  const networkCause = ref<unknown>()
  const networkError = computed(() => networkCause.value instanceof ApiFailure || networkCause.value instanceof LocalizedFailure ? networkCause.value.message : networkCause.value ? text('common.error.generic') : networkStatus.value.state === 'unavailable' ? text('web.network.unavailable') : '')
  const sessions = ref<Session[]>([]), devices = ref<Device[]>([]), tasks = ref<Task[]>([]), plans = ref<Plan[]>([]), approvals = ref<Approval[]>([]), catalog = ref<CatalogEntry[]>([]), audit = ref<AuditEvent[]>([])
  const config = ref<Config>(), configDraft = ref<Config>(), configDirty = ref(false)
  const networkStatus = ref<NetworkStatus>({ state: 'unavailable', addresses: [] })
  const scope = reactive<Scope>({ allowed_tasks: ['inspect_system'], allowed_packages: [], allow_admin: false })
  const pairing = ref<PairingBundle>(), selectedDeviceID = ref(''), selectedTaskID = ref('')
  const taskDraft = reactive({ device_id: '', type: 'install_package' as TaskType, package_id: '', timeout_seconds: 1800 })
  const logs = ref<LogEntry[]>([]), logAfter = ref(0), logTruncated = ref(false), logLoading = ref(false)
  const executionKeys = new Map<string, string>()
  let poll: ReturnType<typeof setInterval> | undefined
  let stopped = false, generation = 0
  const selectedDevice = computed(() => devices.value.find(d => d.id === selectedDeviceID.value))
  const selectedTask = computed(() => tasks.value.find(t => t.id === selectedTaskID.value))
  const runnableDevices = computed(() => devices.value.filter(d => d.online && active(sessions.value.find(s => s.id === d.session_id))))
  const draftDevice = computed(() => devices.value.find(d => d.id === taskDraft.device_id))
  const draftSession = computed(() => sessions.value.find(s => s.id === draftDevice.value?.session_id))
  const allowedCatalog = computed(() => catalog.value.filter(c => draftSession.value?.scope.allowed_packages.includes(c.id)))
  const filteredDevices = computed(() => devices.value.filter(d => `${d.hostname} ${d.id} ${d.os}`.toLowerCase().includes(search.value.toLowerCase())))
  const pendingApprovals = computed(() => approvals.value.filter(a => ['pending', 'pending_approval'].includes(a.status) && alive(a.expires_at)))

  function showError(cause: unknown) {
    if (cause instanceof ApiFailure && cause.status === 401) { authenticated.value = false; api.clear(); pairing.value = undefined; generation++; if (poll) clearInterval(poll) }
    errorCause.value = cause
  }
  async function action(fn: () => Promise<void>) {
    if (busy.value) return
    busy.value = true; error.value = ''; notice.value = ''
    try { await fn() } catch (cause) { showError(cause) } finally { busy.value = false }
  }
  async function refresh() {
    if (loading.value || !authenticated.value) return
    const currentGeneration = generation
    loading.value = true
    try {
      const result = await Promise.all([api.get<Session[]>('/sessions'), api.get<Device[]>('/devices'), api.get<Task[]>('/tasks'), api.get<Plan[]>('/plans'), api.get<Approval[]>('/approvals'), api.get<CatalogEntry[]>('/catalog'), api.get<AuditEvent[]>('/audit-events'), api.get<Config>('/config'), api.get<NetworkStatus>('/network-status').then(status => ({ status, cause: undefined as unknown })).catch((cause: unknown) => ({ status: { state: 'unavailable', addresses: [] } as NetworkStatus, cause }))])
      if (stopped || currentGeneration !== generation) return
      sessions.value = result[0] ?? []; devices.value = result[1] ?? []; tasks.value = result[2] ?? []; plans.value = result[3] ?? []; approvals.value = result[4] ?? []; catalog.value = result[5] ?? []; audit.value = result[6] ?? []; config.value = result[7]
      if (!configDirty.value) configDraft.value = { ...result[7] }
      networkStatus.value = result[8]?.status ?? { state: 'unavailable', addresses: [] }
      networkCause.value = result[8]?.cause
      lastUpdatedAt.value = new Date()
      if (pairing.value && !alive(pairing.value.expires_at)) pairing.value = undefined
    } catch (cause) { if (currentGeneration === generation) showError(cause) } finally { loading.value = false }
  }
  async function login() {
    await action(async () => {
      const loginToken = token.value; token.value = ''
      await api.login(loginToken); authenticated.value = true; generation++
      await refresh()
      if (authenticated.value) poll = setInterval(() => { if (!busy.value) { void refresh(); if (selectedTaskID.value) void loadLogs() } }, 5000)
    })
  }
  async function logout() {
    await action(async () => {
      await api.post('/auth/logout', {})
      authenticated.value = false; api.clear(); token.value = ''; pairing.value = undefined; generation++
      sessions.value = []; devices.value = []; tasks.value = []; plans.value = []; approvals.value = []; audit.value = []; logs.value = []
      if (poll) clearInterval(poll)
    })
  }
  async function createSession() {
    await action(async () => {
      if (!scope.allowed_tasks.length) throw new LocalizedFailure('web.feedback.select.at.least.one.allowed.task')
      if (scope.allowed_tasks.includes('install_package') && !scope.allowed_packages.length) throw new LocalizedFailure('web.feedback.select.at.least.one.package.for.installation.authorization')
      const result = await api.post<CreateSessionResponse>('/sessions', { scope: { allowed_tasks: [...scope.allowed_tasks], allowed_packages: scope.allowed_tasks.includes('install_package') ? [...scope.allowed_packages] : [], allow_admin: scope.allowed_tasks.includes('install_package') && scope.allow_admin } })
      pairing.value = result.pairing
      exportPairing()
      noticeKey.value = 'web.feedback.session.created.and.pairing.download.started.if.the.browser.did'
      await refresh()
    })
  }
  function exportPairing() {
    if (!pairing.value || !alive(pairing.value.expires_at)) { errorCause.value = new LocalizedFailure('web.feedback.the.pairing.bundle.has.expired.create.a.new.session'); return }
    const blob = new Blob([JSON.stringify(pairing.value, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob), link = document.createElement('a')
    link.href = url; link.download = `remoteops-pairing-${pairing.value.session_id}.json`; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
  function canDownloadPairing(session: Session) {
    return session.state === 'pending_pair' && alive(session.pairing_expires_at)
  }
  async function downloadSessionPairing(session: Session) {
    if (busy.value) return
    if (!canDownloadPairing(session)) { errorCause.value = new LocalizedFailure('web.feedback.this.session.is.paired.or.its.pairing.has.expired.create.a.new.s'); return }
    if (pairing.value?.session_id === session.id && alive(pairing.value.expires_at)) { exportPairing(); return }
    if (!window.confirm(text('web.confirm.regenerate', session.id))) return
    await action(async () => {
      pairing.value = await api.post<PairingBundle>(`/sessions/${encodeURIComponent(session.id)}/pairing`, {})
      exportPairing()
      noticeKey.value = 'web.feedback.pairing.regenerated.and.download.started.the.old.file.is.invalid'
      await refresh()
    })
  }
  async function sessionAction(session: Session, kind: 'close' | 'retry-cleanup') {
    if (kind === 'close' && !window.confirm(text('web.feedback.closing.immediately.revokes.device.authorization.and.cancels.tas'))) return
    await action(async () => { await api.post(`/sessions/${encodeURIComponent(session.id)}/${kind}`, {}); if (pairing.value?.session_id === session.id) pairing.value = undefined; noticeKey.value = kind === 'close' ? 'web.feedback.session.authorization.revoked.see.session.status.for.node.cleanu' : 'web.feedback.node.cleanup.retry.requested'; await refresh() })
  }
  function prepare(device: Device, type: TaskType) {
    taskDraft.device_id = device.id; taskDraft.type = type; taskDraft.package_id = ''; taskDraft.timeout_seconds = config.value?.task_timeout_seconds ?? 1800; view.value = 'tasks'
  }
  async function createPlan() {
    await action(async () => {
      const device = draftDevice.value, session = draftSession.value
      if (!device || !device.online || !active(session)) throw new LocalizedFailure('web.feedback.select.an.authorized.online.device')
      if (!session!.scope.allowed_tasks.includes(taskDraft.type)) throw new LocalizedFailure('web.feedback.this.task.type.is.not.authorized.by.the.current.session')
      const spec: TaskSpec = { session_id: device.session_id, device_id: device.id, type: taskDraft.type, timeout_seconds: taskDraft.timeout_seconds, requires_admin: false }
      if (taskDraft.type === 'install_package') {
        const entry = allowedCatalog.value.find(c => c.id === taskDraft.package_id)
        if (!entry) throw new LocalizedFailure('web.feedback.select.a.package.within.the.authorization.scope')
        if (entry.requires_admin && !session!.scope.allow_admin) throw new LocalizedFailure('web.feedback.the.package.requires.administrator.permissions.but.session.eleva')
        spec.package = { id: entry.id, version: entry.version }; spec.requires_admin = entry.requires_admin
      }
      await api.post<Plan>('/plans', spec); noticeKey.value = 'web.feedback.plan.generated.review.parameters.before.requesting.approval'; await refresh()
    })
  }
  const approvalFor = (plan: Plan) => approvals.value.find(a => a.plan_id === plan.id)
  const planFor = (approval: Approval) => plans.value.find(p => p.id === approval.plan_id)
  function canRequest(plan: Plan) { return alive(plan.expires_at) && active(sessions.value.find(s => s.id === plan.spec.session_id)) && !approvalFor(plan) }
  function canDecide(approval: Approval) { return ['pending', 'pending_approval'].includes(approval.status) && alive(approval.expires_at) && !!planFor(approval) && active(sessions.value.find(s => s.id === approval.session_id)) }
  function canExecute(plan: Plan) { const approval = approvalFor(plan); return !!approval && approval.status === 'approved' && !approval.consumed_at && alive(approval.expires_at) && alive(plan.expires_at) && active(sessions.value.find(s => s.id === plan.spec.session_id)) }
  async function requestApproval(plan: Plan) { await action(async () => { await api.post('/approvals', { plan_id: plan.id }); noticeKey.value = 'web.feedback.approval.requested.execution.requires.operator.confirmation'; await refresh() }) }
  async function decide(approval: Approval, decision: 'approve' | 'reject') {
    const plan = planFor(approval)
    if (!plan) { errorCause.value = new LocalizedFailure('web.feedback.the.plan.bound.to.this.approval.was.not.found.refresh.the.worksp'); return }
    if (decision === 'approve' && !window.confirm(text('web.confirm.approve', plan.spec.device_id, plan.spec.session_id, text('web.task.' + plan.spec.type), plan.spec.package?.id ?? text('web.task.inspect_system'), plan.spec.package?.version ?? '', text(plan.spec.requires_admin ? 'web.permission.required' : 'web.permission.not_required'), plan.spec.timeout_seconds))) return
    await action(async () => { await api.post(`/approvals/${encodeURIComponent(approval.id)}/${decision}`, {}); noticeKey.value = decision === 'approve' ? 'web.feedback.plan.approved.select.execute.to.create.a.task' : 'web.feedback.approval.rejected'; await refresh() })
  }
  async function execute(plan: Plan) {
    await action(async () => {
      if (!canExecute(plan)) throw new LocalizedFailure('web.feedback.a.valid.unconsumed.operator.approval.is.required')
      const key = executionKeys.get(plan.id) ?? crypto.randomUUID(); executionKeys.set(plan.id, key)
      const task = await api.post<Task>('/tasks', { spec: plan.spec, approval_id: approvalFor(plan)!.id, idempotency_key: key })
      noticeKey.value = 'web.feedback.task.created'; await refresh(); await selectTask(task.id)
    })
  }
  async function selectTask(id: string) { selectedTaskID.value = id; logs.value = []; logAfter.value = 0; logTruncated.value = false; await loadLogs() }
  async function loadLogs() {
    if (logLoading.value || !selectedTaskID.value || !authenticated.value) return
    const id = selectedTaskID.value, currentGeneration = generation
    logLoading.value = true
    try {
      const page = await api.get<LogsPage>(`/tasks/${encodeURIComponent(id)}/logs?after=${logAfter.value}&limit=100`)
      if (id !== selectedTaskID.value || currentGeneration !== generation || stopped) return
      const known = new Set(logs.value.map(l => l.sequence))
      logs.value = [...logs.value, ...(page.entries ?? []).filter(l => !known.has(l.sequence))].slice(-3000)
      logAfter.value = page.next_after; logTruncated.value ||= page.truncated
    } catch (cause) { if (id === selectedTaskID.value && currentGeneration === generation) showError(cause) } finally { logLoading.value = false }
  }
  async function cancelTask(task: Task) {
    if (!window.confirm(text('web.feedback.cancel.the.task.installation.changes.already.made.will.not.be.au'))) return
    await action(async () => { await api.post(`/tasks/${encodeURIComponent(task.id)}/cancel`, {}); noticeKey.value = 'web.feedback.cancellation.requested.changes.already.made.will.not.be.automati'; await refresh() })
  }
  async function saveConfig() { await action(async () => { config.value = await api.put<Config>('/config', configDraft.value); configDraft.value = { ...config.value }; configDirty.value = false; noticeKey.value = 'web.feedback.configuration.saved'; await refresh() }) }
  onUnmounted(() => { stopped = true; generation++; if (poll) clearInterval(poll); api.clear(); pairing.value = undefined; token.value = '' })
  return { authenticated, token, busy, loading, error, notice, view, search, lastUpdated, sessions, devices, tasks, plans, approvals, catalog, audit, config, configDraft, configDirty, networkStatus, networkError, scope, pairing, selectedDeviceID, selectedDevice, selectedTaskID, selectedTask, taskDraft, draftSession, allowedCatalog, runnableDevices, filteredDevices, pendingApprovals, logs, logAfter, logTruncated, logLoading, login, logout, refresh, createSession, exportPairing, canDownloadPairing, downloadSessionPairing, sessionAction, prepare, createPlan, approvalFor, planFor, canRequest, canDecide, canExecute, requestApproval, decide, execute, selectTask, loadLogs, cancelTask, saveConfig }
}
