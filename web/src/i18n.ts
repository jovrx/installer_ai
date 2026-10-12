import { ref, watch } from 'vue'
import zh from '../../internal/i18n/locales/zh-CN.json'
import en from '../../internal/i18n/locales/en-US.json'

export type Language = 'zh-CN' | 'en-US'
const catalogs: Record<Language, Record<string, string>> = { 'zh-CN': zh, 'en-US': en }
export const language = ref<Language>('zh-CN')
export const storageWarning = ref(false)
const storageKey = 'remoteops.language'
export const supported = (value: unknown): value is Language => value === 'zh-CN' || value === 'en-US'
export function loadLanguage(): Language {
  storageWarning.value = false
  try {
    const saved = localStorage.getItem(storageKey)
    return supported(saved) ? saved : 'zh-CN'
  } catch { storageWarning.value = true; return 'zh-CN' }
}
export function initializeLanguage() { language.value = loadLanguage(); updateDocument() }
export function setLanguage(value: Language) {
  if (!supported(value)) return
  language.value = value
  try { localStorage.setItem(storageKey, value); storageWarning.value = false } catch { storageWarning.value = true }
  updateDocument()
}
export function safeText(value: string): string {
  return value
    .replace(/-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----/g, '[REDACTED PRIVATE KEY]')
    .replace(/"(?:authorization|cookie|password|passwd|secret|token|api[_-]?key|auth[_-]?key)"\s*:\s*"(?:\\.|[^"\\])*"/gi, '"redacted":"[REDACTED]"')
    .replace(/\bBearer\s+[^\s,;"']+/gi, 'Bearer [REDACTED]')
    .replace(/(?:authorization\s*:\s*|cookie\s*:\s*)[^\r\n]+|tskey-[a-zA-Z0-9_-]+|(?:password|passwd|secret|token|api[_-]?key|auth[_-]?key)\s*[=:]\s*[^\s,;]+/gi, '[REDACTED]')
    .replace(/[\u0000-\u001f\u007f-\u009f]/g, '')
}
// Catalogs use the same printf placeholders as Go, including precision and zero
// padding. Unsupported formats and missing keys/arguments use a safe fallback.
export function text(key: string, ...args: (string | number)[]): string {
  const catalog = catalogs[language.value]
  const fallback = catalog['common.error.generic']!
  if (!Object.hasOwn(catalog, key)) return fallback
  const template = catalog[key]
  if (!template) return fallback
  const pattern = /%%|%(0?\d+)?(?:\.(\d+))?([sdf])/g
  const placeholders = template.match(pattern) ?? []
  if (template.replace(pattern, '').includes('%') || placeholders.filter(p => p !== '%%').length !== args.length) return fallback
  let index = 0, valid = true
  const result = template.replace(pattern, (p: string, width?: string, precision?: string, kind?: string) => {
    if (p === '%%') return '%'
    const value = args[index++]!
    if (kind !== 's' && (typeof value !== 'number' || !Number.isFinite(value) || kind === 'd' && !Number.isInteger(value))) valid = false
    if (Number(width ?? 0) > 100 || Number(precision ?? 0) > 100) { valid = false; return '' }
    const formatted = kind === 'f' && typeof value === 'number' ? value.toFixed(Number(precision ?? 6)) : safeText(String(value))
    return width ? formatted.padStart(Number(width), width.startsWith('0') ? '0' : ' ') : formatted
  })
  return valid ? result : fallback
}
export const t = text
export function number(value: number, options?: Intl.NumberFormatOptions): string { return new Intl.NumberFormat(language.value, options).format(value) }
export class LocalizedFailure extends Error {
  constructor(readonly key: string, readonly params: (string | number)[] = []) {
    super()
    Object.defineProperty(this, 'message', { get: () => text(key, ...params) })
  }
}
function updateDocument() {
  if (typeof document === 'undefined') return
  document.documentElement.lang = language.value
  document.title = text('web.document.title')
}
watch(language, updateDocument, { flush: 'sync' })
