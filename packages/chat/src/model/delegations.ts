// delegations.ts — what the delegation card says. A session asks to start another session
// (delegation_request); the person's decision comes back as delegation_decided. Pure functions.
import type { AgentEvent, DelegationDecidedPayload, DelegationRequestPayload } from '../types'
import { parseTs } from './timeLabel'

export interface DelegationState {
  decided: DelegationDecidedPayload | null
  /** The decided event's time. */
  decidedAt: string | null
}

/** By request_id: the decision for every request seen (a decision with no request in the window too). */
export function delegationStates(events: readonly AgentEvent[]): ReadonlyMap<string, DelegationState> {
  const out = new Map<string, DelegationState>()
  for (const ev of events) {
    if (ev.kind !== 'delegation_request' && ev.kind !== 'delegation_decided') continue
    const p = ev.payload as { request_id?: unknown } | null
    const id = typeof p?.request_id === 'string' ? p.request_id : ''
    if (!id) continue
    if (ev.kind === 'delegation_request') {
      if (!out.has(id)) out.set(id, { decided: null, decidedAt: null })
    } else {
      out.set(id, { decided: ev.payload as DelegationDecidedPayload, decidedAt: ev.ts })
    }
  }
  return out
}

const ENGINES: Record<string, string> = { claude: 'Claude', codex: 'Codex', hermes: 'Hermes', openclaw: 'OpenClaw' }

export function engineLabel(engine: string): string {
  return ENGINES[engine] ?? engine
}

export function runtimeWords(runtime: string): string {
  if (runtime === 'cluster') return 'on the cluster'
  return runtime === 'daemon' ? 'on your desktop, unsandboxed' : 'in the desktop sandbox'
}

/** The "Always allow …" button's label and title for a request's engine and runtime. */
export function alwaysAllowWords(engine: string, runtime: string, daemonName: string): { label: string; title: string } {
  if (runtime === 'cluster') return { label: `Always allow ${engine} on the cluster`, title: `Applies to ${engine} on the cluster` }
  if (runtime === 'daemon') return { label: `Always allow ${engine} on this desktop (unsandboxed)`, title: `Applies to ${engine} on ${daemonName}` }
  return { label: `Always allow ${engine} in the desktop sandbox`, title: `Applies to ${engine} through ${daemonName}` }
}

function clock(ts: string | number | null | undefined): string {
  const t = parseTs(ts)
  return t === null ? '' : new Date(t).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
}

export function budgetLine(b: DelegationRequestPayload['budget'], _now: number): string {
  if (!b) return ''
  if (b.local) return 'local (runs on your hardware)'
  if (!b.windows || b.windows.length === 0) return b.note ?? ''
  return b.windows
    .map((w, i) => {
      const pct = Math.round(w.used_percent)
      if (i === 0) {
        const at = clock(w.resets_at)
        return `${w.id} ${pct}% used${at ? `, resets ${at}` : ''}`
      }
      return `${w.id} ${pct}%`
    })
    .join(' · ')
}

export function decidedLine(s: DelegationState, expired: boolean): string {
  const d = s.decided
  if (!d) return expired ? 'Expired, nobody answered' : ''
  if (d.status === 'expired') return 'Expired, nobody answered'
  const at = clock(s.decidedAt)
  const tail = at ? ` · ${at}` : ''
  if (d.status === 'denied') return `Denied${tail}`
  if (d.choice === 'session') return `Allowed for this session${tail}`
  if (d.choice === 'always') return `Always allowed${tail}`
  return `Allowed once${tail}`
}
