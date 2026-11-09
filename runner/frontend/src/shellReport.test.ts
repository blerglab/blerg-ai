import { expect, it, vi } from 'vitest'
import { ShellReport, shellBrowserDeviceId } from './shellReport'

it('reports only structural diagnostics and bounds the timeline', () => {
 const report = new ShellReport({ shell_id: 'shell-one', runtime: 'host', folder: 'SCREEN_SECRET', handle: 'TOKEN_SECRET' }, 'session-one')
 for (let i = 0; i < 25; i++) report.event('reconnect')
 report.close(1006, 'Bearer TOKEN_SECRET https://user:password@example.com ENV_SECRET SCREEN_SECRET')
 const text = report.text()
 expect(text).toContain('Blerg shell report — please diagnose:')
 expect(text).toContain('shell-one')
 expect(text).toContain('session-one')
 expect(text).toContain('1006')
 for (const secret of ['TOKEN_SECRET', 'password', 'ENV_SECRET', 'SCREEN_SECRET']) expect(text).not.toContain(secret)
 expect(text.match(/reconnect/g)).toHaveLength(19)
})
it('keeps recognized close reasons and rejects arbitrary metadata', () => {
 const report = new ShellReport({ shell_id: 'shell-one', runtime: 'cluster', folder: '', handle: '' }, 'session-one')
 report.close(1000, 'shell ended')
 report.reason('idle')
 expect(report.text()).toContain('idle')
 expect(report.text()).toContain('shell ended')
 report.reason('TOKEN_SECRET')
 expect(report.text()).not.toContain('TOKEN_SECRET')
})

it('uses a stable random browser label without machine or network identifiers', () => {
 vi.stubGlobal('crypto', { randomUUID: () => '12345678-0000-4000-8000-000000000000' })
 try {
  localStorage.removeItem('blerg.shell-browser-device')
  expect(shellBrowserDeviceId()).toBe('12345678')
  expect(shellBrowserDeviceId()).toBe('12345678')
  const report = new ShellReport({ shell_id: 'shell', runtime: 'host', folder: '', handle: '' }, 'session')
  report.device('Linux browser 12345678')
  report.device('Bearer TOKEN_SECRET')
  expect(report.text()).toContain('Linux browser 12345678')
  expect(report.text()).not.toContain('TOKEN_SECRET')
 } finally { vi.unstubAllGlobals() }
})

it('keeps each handoff device with its own timeline entry', () => {
 const report = new ShellReport({ shell_id: 'shell', runtime: 'host', folder: '', handle: '' }, 'session')
 report.device('Linux browser 12345678'); report.event('taken_over')
 report.device('Windows browser aabbccdd'); report.event('taken_over')
 expect(report.text()).toContain('taken_over (Linux browser 12345678)')
 expect(report.text()).toContain('taken_over (Windows browser aabbccdd)')
})

it('identifies Edge before the compatibility Chrome version in its user agent', () => {
 const agent = vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue('Mozilla/5.0 (Windows NT 10.0) Chrome/120.0.0.0 Safari/537.36 Edg/121.0.0.0')
 try {
  const report = new ShellReport({ shell_id: 'shell', runtime: 'host', folder: '', handle: '' }, 'session')
  expect(report.text()).toContain('Browser: Edge 121.0.0.0; platform: Windows')
 } finally { agent.mockRestore() }
})

it('never includes names in reports, regardless of their contents', () => {
 for (const name of ['ghp_secret', 'sk-ant-secret', 'AKIASECRET', 'eyJhbGci.secret.signature', 'password=secret', 'Basic secret', 'postgres://secret', '?token=secret', 'private-host', '192.0.2.1', 'pass\u200bword=secret']) {
  const report = new ShellReport({ shell_id: 'shell', runtime: 'host', folder: '', handle: '', name }, 'session')
  expect(report.text()).not.toContain(name)
  expect(report.text()).not.toContain('Name:')
 }
})
