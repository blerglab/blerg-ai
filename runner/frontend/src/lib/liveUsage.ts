// The token chip on a Running now row: what a session (Main, or a started session of any engine) has
// used so far, from the recorded turn usage Insights reads (GET /api/sessions/{id}/live: `usage` for
// the session itself, `children[].usage` for each started one). The server sends it only to the
// session's owner or an administrator, as Insights; anyone else gets no chip. Pure.
import { tokenLabel } from '@blerglab/chat'
import { formatUSD } from './insights'

export interface LiveTurnUsage { tokens: number; input: number; output: number; cache_read: number; cache_write: number; in_flight: boolean }
export interface LiveUsage {
  total: number; input: number; output: number; cache_read: number; cache_write: number
  /** The part of total the session's subagents used. */
  subagents: number
  turns: number
  /** The running turn's usage so far (in_flight) or the last finished turn's. */
  turn?: LiveTurnUsage
  cost_usd?: number
  /** The context meter's reading (card #191); absent when no turn reported one. */
  context?: LiveContext
}

/** How full the main agent's context window was at the end of its latest turn that reported it, as
 *  the engine said. A part it did not report is absent: nothing is estimated. */
export interface LiveContext {
  /** The conversation's size then; absent when a compaction came after the last model call. */
  tokens?: number
  window?: number
  /** The size at which the engine compacts by itself, when it says. */
  compact_at?: number
  /** The engine compacted the conversation in that turn, from compacted_from tokens. */
  compacted: boolean
  compacted_from?: number
}

/** A subagent's usage so far by part (GET /live `subagents[].usage`, or a subagent event's usage). */
export interface UsageParts { input: number; output: number; cache_read: number; cache_write: number }

const count = (v: unknown): number | null => (typeof v === 'number' && Number.isSafeInteger(v) && v >= 0 ? v : null)

/** Reads a usage object from the server, or null when it is missing or malformed. */
export function readUsage(value: unknown): LiveUsage | null {
  if (typeof value !== 'object' || value === null) return null
  const u = value as Record<string, unknown>
  const fields = ['total', 'input', 'output', 'cache_read', 'cache_write'] as const
  const nums = fields.map(f => count(u[f]))
  if (nums.some(n => n === null)) return null
  const [total, input, output, cacheRead, cacheWrite] = nums as number[]
  const out: LiveUsage = { total, input, output, cache_read: cacheRead, cache_write: cacheWrite, subagents: count(u.subagents) ?? 0, turns: count(u.turns) ?? 0 }
  const t = typeof u.turn === 'object' && u.turn !== null ? u.turn as Record<string, unknown> : null
  if (t) {
    const tn = ['tokens', 'input', 'output', 'cache_read', 'cache_write'].map(f => count(t[f]))
    if (tn.every(n => n !== null)) {
      const [tokens, ti, to, tr, tw] = tn as number[]
      out.turn = { tokens, input: ti, output: to, cache_read: tr, cache_write: tw, in_flight: t.in_flight === true }
    }
  }
  if (typeof u.cost_usd === 'number' && Number.isFinite(u.cost_usd) && u.cost_usd >= 0) out.cost_usd = u.cost_usd
  const context = readContext(u.context)
  if (context) out.context = context
  return out
}

/** Reads the server's context reading; null when it is missing, malformed or says nothing. */
export function readContext(value: unknown): LiveContext | null {
  if (typeof value !== 'object' || value === null) return null
  const c = value as Record<string, unknown>
  const out: LiveContext = { compacted: c.compacted === true }
  for (const f of ['tokens', 'window', 'compact_at', 'compacted_from'] as const) {
    if (c[f] === undefined) continue
    const n = count(c[f])
    if (n === null) return null
    if (n > 0) out[f] = n
  }
  return out.tokens !== undefined || out.compacted ? out : null
}

/** Above this share of the compaction threshold (the window when the engine does not say one) the
 *  meter warns, quietly: the engine will summarize the conversation soon. */
export const CONTEXT_WARN = 0.85

export interface ContextMeter {
  /** The share of the window in use, 0–100; null when it is not known (only a compaction is). */
  percent: number | null
  /** The meter's text ("62%"); empty without a percent. */
  text: string
  /** The sizes ("124k of 200k"); empty without a percent. */
  size: string
  /** Its tooltip and accessible name, first line "62% context". */
  label: string
  warn: boolean
  compacted: boolean
}

/** The context meter of a Running now row and the Session section: the share of the model's window
 *  in use, a warning near compaction and the compacted marker. Null (no meter) when the engine
 *  reported neither a size against a window nor a compaction. */
export function contextMeter(context: LiveContext | null | undefined, engine: string): ContextMeter | null {
  if (!context) return null
  const { tokens, window, compact_at: compactAt, compacted } = context
  const percent = tokens !== undefined && window !== undefined ? Math.min(100, Math.round((tokens / window) * 100)) : null
  if (percent === null && !compacted) return null
  const lines: string[] = []
  let warn = false
  const size = percent === null ? '' : `${compactTokens(tokens!)} of ${compactTokens(window!)}`
  if (percent !== null) {
    lines.push(`${percent}% context: ${size} tokens`)
    lines.push(compactAt !== undefined
      ? `${engine} compacts at ${compactTokens(compactAt)} (${Math.round((compactAt / window!) * 100)}%)`
      : `Compaction threshold: not reported by ${engine}`)
    warn = tokens! >= CONTEXT_WARN * (compactAt ?? window!)
    if (warn) lines.push(`Near compaction: ${engine} will summarize the conversation soon`)
  }
  if (compacted) {
    lines.push(`Compacted in its latest turn${context.compacted_from !== undefined ? ` (from ${compactTokens(context.compacted_from)} tokens)` : ''}`)
    if (percent === null) lines.unshift('Context compacted: its size since then is known after the next turn')
  }
  lines.push('As of the end of its latest turn')
  return { percent, text: percent === null ? '' : `${percent}%`, size, label: lines.join('\n'), warn, compacted }
}

/** The token chip's tooltip with what the engine said about its context: nothing, said so. */
export function withContextNote(chip: { text: string; label: string }, usage: LiveUsage | null | undefined, meter: ContextMeter | null, engine: string): { text: string; label: string } {
  if (!usage || meter || usage.turns === 0) return chip
  return { ...chip, label: `${chip.label}\nContext: not reported by ${engine}` }
}

/** Reads the server's parts ({input, output, cache_read, cache_write}) or an event's usage
 *  ({input_tokens, …}); null when neither shape is complete. */
export function readParts(value: unknown): UsageParts | null {
  if (typeof value !== 'object' || value === null) return null
  const u = value as Record<string, unknown>
  // As the server reads an event's usage: a part it leaves out is zero; one that is not a count is refused.
  const names = [['input', 'input_tokens'], ['output', 'output_tokens'], ['cache_read', 'cache_read_tokens'], ['cache_write', 'cache_write_tokens']]
  if (!names.flat().some(k => k in u)) return null
  const parts = names.map(([a, b]) => (a in u ? count(u[a]) : b in u ? count(u[b]) : 0))
  if (parts.some(n => n === null)) return null
  const [input, output, cacheRead, cacheWrite] = parts as number[]
  return { input, output, cache_read: cacheRead, cache_write: cacheWrite }
}

/** A subagent's chip: its run so far is both its total and its "turn" (it runs inside Main's turn,
 *  or past it in the background). Without parts (an older server) the total alone, said so. */
export function subagentChip(tokens: number | null, parts: UsageParts | null | undefined, running: boolean): { text: string; label: string } {
  if (tokens === null) return usageChip(null)
  if (!parts) return { text: compactTokens(tokens), label: `Tokens so far: ${tokenLabel(tokens)}\nBreakdown unavailable: only this subagent's total was reported` }
  const run = parts.input + parts.output + parts.cache_read + parts.cache_write
  return usageChip({ total: tokens, ...parts, subagents: 0, turns: 0, turn: { tokens: run, ...parts, in_flight: running } }, running ? 'This run so far' : 'This run')
}

/** "205k", "1.4M", "950": the chip's text. */
export const compactTokens = (n: number): string => tokenLabel(n).replace(/ tokens?$/, '')

/** The chip's text and its tooltip: the total, this turn, input against cached, and the cost
 *  estimate when every model has a price. No usage yet: a dash, and why. */
export function usageChip(usage: LiveUsage | null, turnLabel?: string): { text: string; label: string } {
  if (!usage) return { text: '–', label: 'Tokens: none recorded yet (an engine reports a turn’s usage when the turn ends)' }
  const lines = [`Tokens so far: ${tokenLabel(usage.total)}`]
  if (usage.turn) lines.push(`${turnLabel ?? (usage.turn.in_flight ? 'This turn so far' : 'Last turn')}: ${tokenLabel(usage.turn.tokens)}`)
  const cached = usage.cache_read + usage.cache_write
  lines.push(`Input ${compactTokens(usage.input)} · cached ${compactTokens(cached)}${usage.cache_write > 0 ? ` (${compactTokens(usage.cache_read)} read, ${compactTokens(usage.cache_write)} written)` : ''} · output ${compactTokens(usage.output)}`)
  if (usage.subagents > 0) lines.push(`Subagents: ${tokenLabel(usage.subagents)} of the total`)
  if (usage.cost_usd !== undefined) lines.push(`Estimated cost: ${formatUSD(usage.cost_usd)}`)
  return { text: compactTokens(usage.total), label: lines.join('\n') }
}
