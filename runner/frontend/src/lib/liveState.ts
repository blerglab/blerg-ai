// The session panel's Live tab, read from the server: the reply of GET /api/sessions/{id}/live
// (runner/internal/livestate) and the events that came after it, folded in so the panel moves at
// once between two reads. Only what is trivial to fold is: a subagent's start, update and end (and a
// person's message or the turn's end ending a foreground one), a background task's start and end, an
// ask and its answer. Anything else (a turn's end, a status
// change) brings a new read. Pure.
import { STALE_RUNNING_MS, parseTs, type AgentEvent, type AskLive, type BackgroundTask, type SubagentLive } from '@blerglab/chat'

export interface LiveAskReply { event_id: string; message_id: string; question: string; asked_at: string; seq: number }

export interface LiveSubagentReply {
  key: string
  title: string
  type: string
  model: string
  tokens: number | null
  doing: string
  started_at: string
  /** Its newest sign of life: it stops counting STALE_RUNNING_MS after it. */
  active_at: string
  background: boolean
  /** A finished subagent the main agent resumed: it runs until its own end, past the turn's. */
  resumed?: boolean
  seq: number
}

export interface LiveTaskReply { task_id: string; call_id: string; kind: string; description: string; started_at: string; seq: number }

export interface LiveReply {
  session_id: string
  through_seq: number
  generated_at: string
  tokens_hidden: boolean
  asks: LiveAskReply[]
  subagents: LiveSubagentReply[]
  tasks: LiveTaskReply[]
  tool_changes: number
  counts: { needs_you: number; running: number }
}

/** The events that change the live state: each brings a new read (folded meanwhile when it can be).
 *  A subagent_update is not one (it comes every few seconds while a subagent works): its model,
 *  tokens and sign of life are folded, and the doing line it would refresh waits for the next read. */
export const LIVE_TRIGGER_KINDS: ReadonlySet<string> = new Set([
  'ask', 'subagent_started', 'subagent_done', 'background_task_started', 'background_task_done', 'turn_done',
])

/** The kinds foldLive reads: the triggers, a subagent's updates and a person's message (it can
 *  answer an ask, and it ends a foreground subagent). */
export const LIVE_FOLD_KINDS: ReadonlySet<string> = new Set([...LIVE_TRIGGER_KINDS, 'subagent_update', 'user_message'])

/** What the panel draws: the lists oldest first, and its counts (the header badge's). */
export interface LiveView {
  asks: AskLive[]
  subagents: SubagentLive[]
  /** Background tasks still running (a background subagent's own task left out). */
  tasks: BackgroundTask[]
  toolChanges: number
  counts: { needsYou: number; running: number }
}

type Rec = Record<string, unknown>
const rec = (v: unknown): Rec => (typeof v === 'object' && v !== null && !Array.isArray(v) ? v as Rec : {})
const str = (v: unknown): string => (typeof v === 'string' ? v : '')

function firstLine(s: string): string {
  for (const line of s.split('\n')) {
    const t = line.trim()
    if (t) return t
  }
  return ''
}

// The chat's subagentTokens: every token of a usage object, or null when there is none.
function usageTotal(u: unknown): number | null {
  if (typeof u !== 'object' || u === null || Array.isArray(u)) return null
  const r = u as Rec
  let n = 0
  for (const k of ['input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_write_tokens']) {
    const v = r[k]
    if (typeof v === 'number' && Number.isFinite(v)) n += v
  }
  return n
}

interface SubAcc extends SubagentLive { activeAt: number | null }

/** The index of the first event after the reply (the store holds them in seq order, live ones that
 *  carry no seq yet at the end). */
function firstAfter(events: readonly AgentEvent[], through: number): number {
  let i = events.length
  while (i > 0) {
    const s = events[i - 1].seq
    if (s !== undefined && s <= through) break
    i--
  }
  return i
}

/** foldLive: the reply with the events after it folded in, at server time now (ms): a subagent or
 *  task with no sign of life for STALE_RUNNING_MS is left out, as the server would. */
export function foldLive(reply: LiveReply, events: readonly AgentEvent[], now: number): LiveView {
  const subs = new Map<string, SubAcc>()
  for (const s of reply.subagents ?? []) {
    subs.set(s.key, {
      key: s.key, title: s.title, type: s.type, model: s.model, tokens: s.tokens, doing: s.doing,
      startedAt: parseTs(s.started_at), background: s.background, activeAt: parseTs(s.active_at),
      ...(s.resumed ? { resumed: true } : {}),
    })
  }
  const tasks = new Map<string, BackgroundTask>()
  for (const t of reply.tasks ?? []) {
    tasks.set(t.task_id, { taskId: t.task_id, callId: t.call_id, kind: t.kind, description: t.description, startedAt: parseTs(t.started_at), done: null })
  }
  const asks: AskLive[] = (reply.asks ?? []).map(a => ({ eventId: a.event_id, messageId: a.message_id, question: a.question, askedAt: parseTs(a.asked_at) }))

  for (let i = firstAfter(events, reply.through_seq); i < events.length; i++) {
    const ev = events[i]
    const p = rec(ev.payload)
    const at = parseTs(ev.ts)
    switch (ev.kind) {
      case 'subagent_started': {
        const key = str(p.child_id)
        if (!key || subs.has(key)) break
        // A resumed run (a finished subagent resumed with SendMessage) is not tied to a call: it
        // runs until its own end, in the background when its original did.
        const resumed = p.resumed === true
        subs.set(key, {
          key, title: firstLine(str(p.summary)) || 'Subagent · started earlier', type: str(p.agent_type), model: str(p.model),
          tokens: null, doing: '', startedAt: at, background: resumed && p.background === true, activeAt: at,
          ...(resumed ? { resumed: true } : {}),
        })
        break
      }
      case 'subagent_update':
      case 'subagent_done': {
        const s = subs.get(str(p.child_id))
        if (!s) break
        if (ev.kind === 'subagent_done' && p.background !== true) {
          subs.delete(s.key)
          break
        }
        if (ev.kind === 'subagent_done') s.background = true
        if (str(p.model)) s.model = str(p.model)
        const tokens = usageTotal(p.usage)
        if (tokens !== null && !reply.tokens_hidden) s.tokens = tokens
        s.activeAt = at ?? s.activeAt
        break
      }
      case 'background_task_started': {
        const id = str(p.task_id)
        if (!id || tasks.has(id)) break
        tasks.set(id, { taskId: id, callId: str(p.call_id), kind: str(p.kind), description: str(p.description), startedAt: parseTs(str(p.started_at)) ?? at, done: null })
        break
      }
      case 'turn_done':
        // The turn is over: no call of it is in flight, a foreground subagent's included.
        for (const sub of [...subs.values()]) if (!sub.background && !sub.resumed) subs.delete(sub.key)
        break
      case 'background_task_done':
        tasks.delete(str(p.task_id))
        break
      case 'ask':
        if (!asks.some(a => a.eventId === ev.client_event_id)) {
          asks.push({ eventId: ev.client_event_id, messageId: str(p.message_id), question: str(p.body), askedAt: at })
        }
        break
      case 'user_message': {
        // An answer closes its ask (by message id, else the newest); a person's message closes the
        // newest ask that has no message id.
        const source = str(p.source)
        const id = str(p.message_id)
        let k = -1
        if (source === 'ask_answer') k = id ? asks.findIndex(a => a.messageId === id) : asks.length - 1
        else if (source !== 'system') k = asks.map(a => a.messageId === '').lastIndexOf(true)
        if (k >= 0) asks.splice(k, 1)
        // The chat's liveCalls (and the server's rule): a message that is not the system's ends the
        // calls in flight, so a foreground subagent stops counting; a background one runs on.
        if (source !== 'system') for (const sub of [...subs.values()]) if (!sub.background && !sub.resumed) subs.delete(sub.key)
        break
      }
    }
  }

  const stale = (t: number | null) => t !== null && Number.isFinite(now) && now - t > STALE_RUNNING_MS
  const subagents: SubagentLive[] = []
  for (const s of subs.values()) {
    if (stale(s.activeAt)) continue
    subagents.push({
      key: s.key, title: s.title, type: s.type, model: s.model, tokens: s.tokens, doing: s.doing, startedAt: s.startedAt, background: s.background,
      ...(s.resumed ? { resumed: true } : {}),
    })
  }
  const listed = new Set(subagents.map(s => s.key))
  const running = [...tasks.values()].filter(t => !stale(t.startedAt) && !(t.kind === 'agent' && listed.has(t.callId)))
  const toolChanges = reply.tool_changes ?? 0
  return {
    asks, subagents, tasks: running, toolChanges,
    counts: { needsYou: asks.length + toolChanges, running: subagents.length + running.length },
  }
}
