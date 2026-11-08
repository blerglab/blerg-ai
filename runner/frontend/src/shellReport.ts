import type { ShellInfo } from './shellTransport'

export type ShellReportVersions = { runner?: string; commit?: string; daemon?: string; desktop?: string }
let context: (session: string) => ShellReportVersions = () => ({})
export function setShellReportContext(provider: typeof context) { context = provider }
export function shellReportVersions(session: string) { return context(session) }

// A random browser-profile label helps identify handoffs without collecting machine or network data.
export function shellBrowserDeviceId(): string | undefined {
  try {
    const key = 'blerg.shell-browser-device'
    const saved = localStorage.getItem(key)
    if (saved && /^[a-f0-9]{8}$/.test(saved)) return saved
    const id = crypto.randomUUID().replaceAll('-', '').slice(0, 8)
    localStorage.setItem(key, id)
    return id
  } catch { return undefined }
}

const reasons = new Set(['requested', 'cleanup_uncertain', 'revoked', 'unavailable', 'slow_consumer', 'rate_limit', 'idle', 'detached', 'runtime_closed', 'shutdown', 'taken_over', 'lifetime', 'consent_withdrawn'])
const closeReasons = new Set(['shell ended', 'taken over'])
export type ShellEvent = 'connecting' | 'syncing' | 'running' | 'disconnected' | 'closed' | 'attach' | 'detach' | 'reconnect' | 'take_back' | 'taken_over' | 'hidden' | 'shown' | 'online' | 'offline' | 'wake' | 'socket_close' | 'probe_free' | 'probe_attached' | 'probe_gone' | 'probe_unknown'
const events = new Set<ShellEvent>(['connecting', 'syncing', 'running', 'disconnected', 'closed', 'attach', 'detach', 'reconnect', 'take_back', 'taken_over', 'hidden', 'shown', 'online', 'offline', 'wake', 'socket_close', 'probe_free', 'probe_attached', 'probe_gone', 'probe_unknown'])
const version = (value?: string) => value && /^[a-zA-Z0-9.+_-]{1,80}$/.test(value) ? value : 'unknown'
// Free-form transport errors, terminal titles, paths, screen bytes and input have no slot here.
export class ShellReport {
  private timeline: { at: number; event: ShellEvent; device?: string }[] = []
  private controller = 'unknown'
  device(value: unknown) { if (typeof value === 'string' && /^(Windows|macOS|Linux|Android|iOS|unknown)( browser [a-f0-9]{8})?$/.test(value)) this.controller = value }
  private serverReason = 'unknown'
  private closeCode: number | undefined
  private closeReason = 'unknown'
  private readonly started: number
  private readonly info: ShellInfo
  private readonly session: string
  constructor(info: ShellInfo, session: string) { this.info = info; this.session = session; this.started = info.opened_at ? info.opened_at * 1000 : Date.now() }
  event(event: ShellEvent) {
    if (!events.has(event)) return
    this.timeline.push({ at: Date.now(), event, ...(event === 'taken_over' ? { device: this.controller } : {}) }); this.timeline = this.timeline.slice(-20)
  }
  reason(reason: unknown) { if (typeof reason === 'string' && reasons.has(reason)) this.serverReason = reason }
  close(code: number, reason: string) {
    this.closeCode = Number.isInteger(code) && code >= 1000 && code <= 4999 ? code : undefined
    this.closeReason = closeReasons.has(reason) ? reason : 'unrecognized (omitted)'
    this.event('socket_close')
  }
  text(versions: ShellReportVersions = {}) {
    const browser = navigator.userAgent.match(/(Firefox|Edg)\/([\d.]+)/) ?? navigator.userAgent.match(/(Chrome|Version)\/([\d.]+)/)
    const platform = /Windows/.test(navigator.userAgent) ? 'Windows' : /Android/.test(navigator.userAgent) ? 'Android' : /iPhone|iPad/.test(navigator.userAgent) ? 'iOS' : /Mac/.test(navigator.userAgent) ? 'macOS' : /Linux/.test(navigator.userAgent) ? 'Linux' : 'unknown'
    const identifier = (value?: string) => value && /^[a-zA-Z0-9_-]{1,128}$/.test(value) ? value : 'unknown'
    return [
      'Blerg shell report — please diagnose:',
      `Shell: ${identifier(this.info.shell_id)}`,
      `Session: ${identifier(this.session)}`,
      `Runtime: ${['host', 'sandbox', 'cluster'].includes(this.info.runtime) ? this.info.runtime : 'unknown'}`,
      `Desktop: ${identifier(versions.desktop)}`,
      `Age: ${Math.max(0, Math.floor((Date.now() - this.started) / 1000))} seconds`,
      `Server reason: ${this.serverReason}`,
      `Controller device: ${this.controller}`,
      'Exit code / signal: unavailable from this runtime',
      `WebSocket close: ${this.closeCode ?? 'unknown'}; ${this.closeReason}`,
      `Runner: ${version(versions.runner)}; commit: ${version(versions.commit)}`,
      `Daemon: ${version(versions.daemon)}; commit: ${version(versions.daemon?.match(/(?:^|-)g([a-f0-9]{7,40})(?:-dirty)?$/i)?.[1])}`,
      `Protocol: ${this.info.persistent ? 2 : 1}`,
      `Browser profile: ${shellBrowserDeviceId() ?? 'unknown'}`,
      `Browser: ${browser ? `${browser[1] === 'Version' ? 'Safari' : browser[1] === 'Edg' ? 'Edge' : browser[1]} ${browser[2]}` : 'unknown'}; platform: ${platform}`,
      'Timeline (UTC / local):',
      ...this.timeline.map(({ at, event, device }) => `${new Date(at).toISOString()} / ${new Date(at).toLocaleString('en-US', { timeZoneName: 'short' })} ${event}${device ? ` (${device})` : ''}`),
    ].join('\n')
  }
}
