// What the transcript knows about a subagent, in words: its status, its title, model and tokens,
// what it is doing now and how it went. Pure; the subagent card and the side panel's "Running now"
// rows draw from it.
import { describeInput, formatDuration, type SubagentEntry } from './toolGroups'
import { staleSince } from './stale'
import { parseTs } from './timeLabel'
import type { AssistantTextPayload, SubagentPayload, TokenUsage, ToolCallPayload } from '../types'

export type SubagentStatus = 'running' | 'background' | 'done' | 'error' | 'stopped'

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

export function payloadOf(ev: SubagentEntry['done']): Partial<SubagentPayload> {
  return ev && isRecord(ev.payload) ? (ev.payload as Partial<SubagentPayload>) : {}
}

/** The server-time ms of the newest event the subagent holds (its start, update, own rows, call,
 *  or launch), or null when none has a time. */
function newestTs(sub: SubagentEntry): number | null {
  let newest: number | null = null
  for (const ev of [sub.anchor, sub.started, sub.update, sub.latest, sub.done, sub.call?.ev]) {
    const t = ev ? parseTs(ev.ts) : null
    if (t !== null && (newest === null || t > newest)) newest = t
  }
  return newest
}

/** resumedSubagent: the card is a resumed run of a finished subagent: the daemon said so (its start,
 *  or its end, carries resumed). Never read from the call being a SendMessage: a session recorded by
 *  an older daemon has SendMessage calls with no such run. That call's result is not its end: only
 *  its own subagent_done is. */
export function resumedSubagent(sub: SubagentEntry): boolean {
  return sub.info.resumed === true || payloadOf(sub.done).resumed === true
}

/** The JSON object an output starts with, and the text after it; null when it starts with none. */
function leadingJSON(output: string): { head: Record<string, unknown>; rest: string } | null {
  const start = output.search(/\S/)
  if (start < 0 || output[start] !== '{') return null
  let depth = 0
  let inString = false
  for (let i = start; i < output.length; i++) {
    const c = output[i]
    if (inString) {
      if (c === '\\') i++
      else if (c === '"') inString = false
    } else if (c === '"') inString = true
    else if (c === '{') depth++
    else if (c === '}' && --depth === 0) {
      try {
        const head: unknown = JSON.parse(output.slice(start, i + 1))
        return isRecord(head) ? { head, rest: output.slice(i + 1) } : null
      } catch {
        return null
      }
    }
  }
  return null
}

/** handBackReport: the report a SendMessage result returns when it waited for the run (the CLI's
 *  {"message":"Resumed agent. …"} then the report), else the output as it is. */
export function handBackReport(output: string): string {
  const j = leadingJSON(output)
  return j && typeof j.head.message === 'string' && j.head.message.startsWith('Resumed agent.') ? j.rest.trim() : output
}

/** resumeAck: a SendMessage result that only says the run was resumed (or the message queued), as
 *  opposed to one that returns the run's report. */
export function resumeAck(output: unknown): boolean {
  return typeof output === 'string' && /"resumedAgentId"\s*:|^\s*(\{"success":\s*true,\s*"message":\s*")?(Resuming agent |Message queued for delivery to )/.test(output)
}

/** staleSubagent: it has no final end and its newest event is older than STALE_RUNNING_MS at server
 *  time now (a background subagent's launch is not its end, nor a resumed run's call's result). */
export function staleSubagent(sub: SubagentEntry, now: number): boolean {
  const ended = resumedSubagent(sub) ? !!sub.done : !sub.background && (sub.done || sub.call?.result)
  return !ended && staleSince(newestTs(sub), now)
}

/** subagentStatus: done or error once it ended or its call returned; running in the background
 *  while a background subagent's launch is all that ended; stopped when a Claude Code subagent's
 *  call is no longer in flight and never returned, or when it has had no event for
 *  STALE_RUNNING_MS and no end (now: server-time ms). A call's result says whether it failed; only
 *  a card with no call (the native loop's) reads it from the summary. */
export function subagentStatus(sub: SubagentEntry, live: ReadonlySet<string>, now: number, sessionOver = false): SubagentStatus {
  if (staleSubagent(sub, now)) return 'stopped'
  if (resumedSubagent(sub)) {
    // A resumed run is not tied to its call: it runs until its own end, in the background when its
    // original did; in a session that is over, a run with no end recorded is stopped.
    if (!sub.done) return sessionOver ? 'stopped' : sub.info.background === true ? 'background' : 'running'
    const p = payloadOf(sub.done)
    return p.is_error === true || (typeof p.summary === 'string' && p.summary.startsWith('error:')) ? 'error' : 'done'
  }
  if (sub.background) return 'background'
  const result = sub.call?.result
  if (sub.done || result) {
    if (result?.is_error) return 'error'
    // A background subagent's call returned its launch: its real end says how it went.
    if (!sub.call || sub.launched) {
      const summary = payloadOf(sub.done).summary
      if (typeof summary === 'string' && summary.startsWith('error:')) return 'error'
    }
    return 'done'
  }
  if (sub.call && !live.has(sub.call.call.call_id)) return 'stopped'
  return 'running'
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

/** countLine: "3 tool calls · 1 message". */
export function countLine(sub: SubagentEntry): string {
  return `${plural(sub.toolCalls, 'tool call', 'tool calls')} · ${plural(sub.messages, 'message', 'messages')}`
}

function cut(s: string, n: number): string {
  const chars = Array.from(s)
  return chars.length > n ? chars.slice(0, n).join('') + '…' : s
}

/** s within n characters, the last of them "…" when it is cut (as liveCalls callDetail cuts). */
function cutTo(s: string, n: number): string {
  const chars = Array.from(s)
  return chars.length > n ? chars.slice(0, n - 1).join('') + '…' : s
}

function firstLine(s: string): string {
  for (const line of s.split('\n')) {
    const t = line.trim()
    if (t) return t
  }
  return ''
}

export function inputField(input: unknown, name: string): string {
  const v = isRecord(input) ? input[name] : undefined
  return typeof v === 'string' ? v : ''
}

/** subagentTitle: what the subagent is — its task's description, else the start of its prompt. */
export function subagentTitle(sub: SubagentEntry): string {
  const input = sub.call?.call.input
  const description = inputField(input, 'description').trim()
  if (description) return description
  const prompt = firstLine(inputField(input, 'prompt'))
  if (prompt) return cut(prompt, 80)
  if (sub.started && sub.info.summary) return sub.info.summary
  return 'Subagent · started earlier'
}

/** The model it runs on: as its API calls report it, else as it was asked for. */
export function subagentModel(sub: SubagentEntry): string {
  if (typeof sub.model === 'string') return sub.model
  return payloadOf(sub.done).model || payloadOf(sub.update).model || sub.info.model || inputField(sub.call?.call.input, 'model')
}

/** Last child-reported configured effort, never the root setting or a requested tool input. */
export function subagentEffort(sub: SubagentEntry): string {
  if (typeof sub.effort === 'string') return sub.effort
  let effort = '', model = ''
  for (const p of [sub.info, payloadOf(sub.update), payloadOf(sub.done)]) {
    if (p.model) {
      if (p.model !== model) effort = ''
      model = p.model
    }
    if (typeof p.effort === 'string') effort = p.effort
    else if (p.effort === null) effort = ''
  }
  return effort
}

function usageTotal(u: TokenUsage | undefined): number | null {
  if (!u || !isRecord(u)) return null
  let n = 0
  for (const k of ['input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_write_tokens'] as const) {
    const v = u[k]
    if (typeof v === 'number' && Number.isFinite(v)) n += v
  }
  return n
}

/** subagentTokens: every token its API calls used so far (input, output and cache), or null. */
export function subagentTokens(sub: SubagentEntry): number | null {
  return usageTotal(payloadOf(sub.done).usage) ?? usageTotal(payloadOf(sub.update).usage)
}

/** "950 tokens", "12.3k tokens", "1.2M tokens". */
export function tokenLabel(n: number): string {
  if (n < 1000) return `${n} tokens`
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k tokens`
  return `${(n / 1_000_000).toFixed(1)}M tokens`
}

function fileName(path: string): string {
  const parts = path.split(/[\\/]/)
  return parts[parts.length - 1] || path
}

/** doingLine: what it is doing now — its newest tool call ("running Bash · npm test") or the first
 *  line of its newest message. */
export function doingLine(sub: SubagentEntry): string {
  const ev = sub.latest
  if (!ev) return ''
  if (ev.kind === 'assistant_text') return cut(firstLine(String((ev.payload as AssistantTextPayload | null)?.text ?? '')), 100)
  const call = (isRecord(ev.payload) ? ev.payload : {}) as Partial<ToolCallPayload>
  const tool = typeof call.tool === 'string' && call.tool ? call.tool : 'tool'
  let hint: string
  switch (tool) {
    case 'Bash': {
      // What the call says it does, else the start of its command.
      const description = firstLine(inputField(call.input, 'description'))
      hint = description ? cutTo(description, 48) : cut(firstLine(inputField(call.input, 'command')), 60)
      break
    }
    case 'Read':
    case 'Edit':
    case 'Write':
    case 'NotebookEdit':
      hint = fileName(inputField(call.input, 'file_path') || inputField(call.input, 'notebook_path'))
      break
    case 'Agent':
    case 'Task':
      hint = inputField(call.input, 'description')
      break
    default:
      hint = cut(describeInput(call.input).summary, 60)
  }
  return hint ? `running ${tool} · ${hint}` : `running ${tool}`
}

/** How long it ran: as reported, else from its start to its end. */
function durationMs(sub: SubagentEntry): number | null {
  const d = payloadOf(sub.done).duration_ms
  if (typeof d === 'number' && d > 0) return d
  const from = parseTs((sub.started ?? sub.call?.ev)?.ts)
  const to = parseTs(sub.done?.ts)
  return from !== null && to !== null && to >= from ? to - from : null
}

/** endLine: "done · 3 tool calls · 1 message · 12k tokens · 1m 4s". */
export function endLine(sub: SubagentEntry, status: SubagentStatus): string {
  const parts = [status === 'error' ? 'error' : 'done', countLine(sub)]
  const tokens = subagentTokens(sub)
  if (tokens !== null && tokens > 0) parts.push(tokenLabel(tokens))
  const d = durationMs(sub)
  if (d !== null) parts.push(formatDuration(d))
  return parts.join(' · ')
}
