// Live reasoning metadata, separate from reasoning text and billing totals.
import type { AgentEvent } from '../types'
import { nextThinking } from './workingActivity'
import { formatElapsed } from './timeLabel'

export interface Reasoning { since: number; tokens: number | null; active?: boolean }
export type ReasoningScopes = Readonly<Record<string, Reasoning>>
export const NO_REASONING: ReasoningScopes = {}

export function readReasoning(value: unknown): Reasoning | null {
  if (!value || typeof value !== 'object') return null
  const p = value as Partial<Reasoning>
  if (typeof p.since !== 'number' || !Number.isFinite(p.since)) return null
  const tokens = typeof p.tokens === 'number' && Number.isSafeInteger(p.tokens) && p.tokens >= 0 ? p.tokens : null
  return { since: p.since, tokens }
}

export function reasoningLabel(value: Reasoning, now: number): string {
  return `Thinking… ${value.tokens === null ? formatElapsed(Math.max(0, now - value.since)) : `${value.tokens.toLocaleString('en-US')} tokens`}`
}

export function nextReasoning(scopes: ReasoningScopes, ev: AgentEvent): ReasoningScopes {
  const p = (ev.payload ?? {}) as { parent_call_id?: string; child_id?: string; background?: boolean; tokens?: number }
  const key = p.parent_call_id || ''
  const previous = scopes[key]
  const bounded = (target: string, value: Reasoning) => {
    const entries = Object.entries(scopes)
    if (!(target in scopes) && entries.length >= 256) { entries.sort((a, b) => a[1].since - b[1].since); entries.shift() }
    return { ...Object.fromEntries(entries), [target]: value }
  }
  if (ev.transient && (ev.kind === 'thinking_started' || ev.kind === 'thinking_progress')) {
    const stamp = Date.parse(ev.ts)
    if (!Number.isFinite(stamp)) return scopes
    const tokens = typeof p.tokens === 'number' && Number.isSafeInteger(p.tokens) && p.tokens >= 0 ? p.tokens : null
    if (ev.kind === 'thinking_progress' && (tokens === null || previous?.active === false)) return scopes
    const next = ev.kind === 'thinking_started' ? { since: stamp, tokens } : { since: previous?.since ?? stamp, tokens: Math.max(previous?.tokens ?? 0, tokens!) }
    if (previous?.active !== false && previous?.since === next.since && previous.tokens === next.tokens) return scopes
    return bounded(key, next)
  }
  let clear = !nextThinking(true, { ...ev, payload: { ...p, parent_call_id: '' } })
  let target = key
  if (ev.kind === 'subagent_done' && !p.background && p.child_id) { target = p.child_id; clear = true }
  if (!clear || scopes[target]?.active === false) return scopes
  return bounded(target, { since: scopes[target]?.since ?? Date.parse(ev.ts), tokens: null, active: false })
}
