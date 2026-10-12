// @vitest-environment happy-dom
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import * as i18n from './i18n'
import zh from '../../internal/i18n/locales/zh-CN.json'
import en from '../../internal/i18n/locales/en-US.json'
import { date, size } from './workspace'

describe('browser language', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })
  it('loads saved English and defaults invalid preferences to Chinese', () => {
    localStorage.setItem('remoteops.language', 'en-US')
    expect(i18n.loadLanguage()).toBe('en-US')
    localStorage.setItem('remoteops.language', 'en')
    expect(i18n.loadLanguage()).toBe('zh-CN')
  })
  it('keeps the switch effective when storage is unavailable', () => {
    vi.stubGlobal('localStorage', { getItem: () => { throw new Error('private-secret') }, setItem: () => { throw new Error('private-secret') } })
    expect(i18n.loadLanguage()).toBe('zh-CN')
    i18n.setLanguage('en-US')
    expect(i18n.language.value).toBe('en-US')
    expect(i18n.storageWarning.value).toBe(true)
    expect(i18n.text('web.storage.warning')).not.toContain('private-secret')
  })
  it('uses matching catalogs and safely formats printf arguments', () => {
    expect(Object.keys(en).sort()).toEqual(Object.keys(zh).sort())
    for (const key of Object.keys(zh)) {
      expect((en as Record<string, string>)[key]!.match(/%(?:\[[0-9]+\])?[-+# 0]*(?:[0-9]+|\*)?(?:\.(?:[0-9]+|\*))?(?:\[[0-9]+\])?[a-zA-Z%]/g)).toEqual((zh as Record<string, string>)[key]!.match(/%(?:\[[0-9]+\])?[-+# 0]*(?:[0-9]+|\*)?(?:\.(?:[0-9]+|\*))?(?:\[[0-9]+\])?[a-zA-Z%]/g))
    }
    i18n.setLanguage('en-US')
    expect(i18n.text('task.progress', 'download', 50)).toBe('download (50%)')
    expect(i18n.text('common.error.detail', 'raw %! output')).toBe('Error: raw %! output')
    expect(i18n.text('missing.secret', 'private-secret')).toBe(i18n.text('common.error.generic'))
    for (const key of ['constructor', 'toString', '__proto__']) expect(i18n.text(key, 'private-secret')).toBe(i18n.text('common.error.generic'))
    expect(i18n.text('task.progress', 'private-secret')).toBe(i18n.text('common.error.generic'))
  })
  it('formats every shared numeric placeholder and redacts presentation arguments', () => {
    i18n.setLanguage('en-US')
    expect(i18n.text('worker.progress.bytes', 1.25)).toBe('Downloaded 1.3 MB')
    expect(i18n.text('worker.gui.remaining', 2, 5)).toContain('02:05')
    expect(i18n.text('common.error.detail', 'token=private-secret\n\x1b')).not.toContain('private-secret')
    expect(i18n.text('common.error.detail', 'token=private-secret\n\x1b')).not.toContain('\x1b')
  })
  it('updates date and numeric presentation with the selected locale', () => {
    const timestamp = '2026-10-11T03:01:00Z'
    i18n.setLanguage('en-US')
    expect(date(timestamp)).toBe(new Date(timestamp).toLocaleString('en-US', { hour12: false }))
    expect(size(1234 * 1073741824)).toBe('1,234.0 GB')
    i18n.setLanguage('zh-CN')
    expect(date(timestamp)).toBe(new Date(timestamp).toLocaleString('zh-CN', { hour12: false }))
  })
})
