// One card per kind of event: how each thing the transcript holds is drawn. renderEvent is the
// switch; the compact timeline and the legacy (one card per call) view both go through it.
import { memo, useState, type ReactNode } from 'react'
import type { CardRegistry } from '../../cards/registry'
import { isHarnessText } from '../../model/harnessText'
import { parseTs, turnDuration } from '../../model/timeLabel'
import type {
  AgentEvent,
  ArtifactPayload,
  AssistantTextPayload,
  CheckInPayload,
  CompactionPayload,
  ErrorPayload,
  MessagingPayload,
  ModelChangedPayload,
  StatusPayload,
  SubagentPayload,
  ToolCallPayload,
  ToolResultPayload,
  TurnDonePayload,
  UserMessagePayload,
} from '../../types'
import ArtifactCard from '../ArtifactCard'
import { MessageText } from '../AttachmentChips'
import Markdown from '../Markdown'
import { TimeLabel } from '../TimeLabel'

/** What the host may add under a user or assistant card (ChatView's `slots.messageFooter`). */
export type MessageFooter = (ev: AgentEvent) => ReactNode

/** How the host wants one user message drawn (ChatView's `describeUserMessage`). */
export interface UserMessageLook {
  /** Who it is from, in place of "You": a host whose automation also messages the session says so. */
  author?: string
  /** Draw it as one collapsed line that expands (the long prompt a session was started with). The
   *  string is the line's label; `true` uses "session brief". */
  brief?: boolean | string
}

/** Decides how a user message is drawn; null or undefined leaves it as the person's own. */
export type DescribeUserMessage = (ev: AgentEvent, payload: UserMessagePayload) => UserMessageLook | null | undefined

export interface RenderOptions {
  cards?: CardRegistry
  messageFooter?: MessageFooter
  describeUserMessage?: DescribeUserMessage
}

// The prompt a session was started with: metadata more than conversation, so one dim line that
// opens on demand. Collapsed on every mount; opening it changes nothing else.
function BriefLine({ label, text, cards }: { label: string; text: string; cards?: CardRegistry }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="agent-card brief" data-testid="message-brief">
      <button type="button" className="brief-toggle" aria-expanded={open} onClick={() => setOpen(o => !o)}>
        <span aria-hidden="true">{open ? '▾' : '▸'}</span> {label} · {text.length.toLocaleString()} chars
      </button>
      {open && <div className="brief-body"><Markdown text={text} cards={cards} /></div>}
    </div>
  )
}

// publishedURL extracts the URL from a push_mockup / push_screenshot result.
function publishedURL(result?: ToolResultPayload): string | null {
  if (!result || result.is_error) return null
  const m = result.output.match(/published:\s*(\S+)/i)
  return m ? m[1] : null
}

export function ToolCard({ call, result, ts }: { call: ToolCallPayload; result?: ToolResultPayload; ts?: string }) {
  const [open, setOpen] = useState(false)
  const ok = result ? !result.is_error : undefined
  const url = (call.tool === 'push_mockup' || call.tool === 'push_screenshot') ? publishedURL(result) : null
  return (
    <div className={`agent-card tool ${ok === false ? 'tool-error' : ''}`} data-testid="tool-card">
      <button className="tool-header" onClick={() => setOpen(o => !o)}>
        <span className="tool-name">{call.tool}</span>
        <span className="tool-summary">{summarizeInput(call.input)}</span>
        <TimeLabel ts={ts} />
        <span className="tool-status">{result ? (ok ? '✓' : '✗') : '…'}</span>
      </button>
      {url && call.tool === 'push_mockup' && (
        <a className="publish-link" href={url} target="_blank" rel="noreferrer noopener" data-testid="mockup-link">
          🖼 Open mockup ↗
        </a>
      )}
      {url && call.tool === 'push_screenshot' && (
        <a href={url} target="_blank" rel="noreferrer noopener" data-testid="screenshot-link">
          <img className="publish-screenshot" src={url} alt="screenshot" loading="lazy" />
        </a>
      )}
      {open && (
        <pre className="tool-detail">
          {JSON.stringify(call.input, null, 2)}
          {result ? `\n─── result (${result.duration_ms}ms) ───\n${result.output}` : ''}
        </pre>
      )}
    </div>
  )
}

function summarizeInput(input: unknown): string {
  if (input && typeof input === 'object') {
    const o = input as Record<string, unknown>
    const v = o.command ?? o.path ?? o.pattern ?? o.name ?? o.prompt ?? o.question ?? o.body ?? ''
    if (typeof v === 'string') return v.length > 80 ? v.slice(0, 80) + '…' : v
  }
  return ''
}

function usageCost(p: TurnDonePayload): string {
  const u = p.usage
  return `${u.input_tokens} in · ${u.output_tokens} out${u.cache_read_tokens ? ` · ${u.cache_read_tokens} cached` : ''}`
}

// The turn footer line: stop reason, model, usage, when, and how long it took.
export function turnDoneText(ev: AgentEvent, turnStart?: number) {
  const p = ev.payload as TurnDonePayload
  const d = turnDuration(turnStart ?? null, parseTs(ev.ts))
  return (
    <>
      {p.stop_reason} · {p.model} · {usageCost(p)}
      {p.check_in_missing ? ' · ⚠ no completion check-in' : ''}
      {' · '}<TimeLabel ts={ev.ts} />
      {d ? ` · ${d}` : ''}
    </>
  )
}

export const NO_RESULTS = new Map<string, ToolResultPayload>()

// A card for an event that is neither a tool call nor a group of them. Memoized
// on the event so appending to the transcript re-renders only what is new.
export const EventRow = memo(function EventRow({ ev, turnStart, footer, footerStart, options }: {
  ev: AgentEvent
  turnStart?: number
  footer?: AgentEvent
  footerStart?: number
  options: RenderOptions
}) {
  return renderEvent(ev, NO_RESULTS, turnStart, footer, footerStart, options)
})

export function renderEvent(
  ev: AgentEvent,
  results: Map<string, ToolResultPayload>,
  turnStart?: number,
  footer?: AgentEvent,
  footerStart?: number,
  options: RenderOptions = {},
) {
  const key = ev.client_event_id
  switch (ev.kind) {
    case 'user_message': {
      const p = ev.payload as UserMessagePayload
      // Harness-injected text (a subagent's completion, a reminder): recorded as "system" by a
      // current pod, recognised by its shape for older pods and rows.
      if (p.source === 'system' || isHarnessText(p.text)) return null
      const look = options.describeUserMessage?.(ev, p)
      if (look?.brief) {
        return <BriefLine key={key} label={typeof look.brief === 'string' ? look.brief : 'session brief'} text={p.text} cards={options.cards} />
      }
      return (
        <div key={key} className={`agent-card user source-${p.source}${look?.author ? ' other-author' : ''}`}>
          <span className="card-author">{look?.author ?? 'You'}</span>
          <MessageText text={p.text} cards={options.cards} />
          <div className="card-foot"><TimeLabel ts={ev.ts} /></div>
          {options.messageFooter?.(ev)}
        </div>
      )
    }
    case 'assistant_text': {
      const p = ev.payload as AssistantTextPayload
      return (
        <div key={key} className="agent-card assistant">
          <Markdown text={p.text} cards={options.cards} />
          <div className="card-foot"><TimeLabel ts={ev.ts} /></div>
          {footer && <div className="turn-foot" data-testid="turn-done">{turnDoneText(footer, footerStart)}</div>}
          {options.messageFooter?.(ev)}
        </div>
      )
    }
    case 'tool_call': {
      const p = ev.payload as ToolCallPayload
      return <ToolCard key={key} call={p} result={results.get(p.call_id)} ts={ev.ts} />
    }
    case 'tool_result':
      return null // rendered inside its ToolCard
    case 'check_in': {
      const p = ev.payload as CheckInPayload
      return (
        <div key={key} className={`agent-card checkin phase-${p.phase}`} data-testid="checkin">
          <span className="checkin-phase">{p.phase.replace('_', ' ')}</span>
          <span>{p.summary}</span>
          <TimeLabel ts={ev.ts} className="card-time-end" />
        </div>
      )
    }
    case 'ask':
    case 'update':
    case 'note': {
      const p = ev.payload as MessagingPayload
      return (
        <div key={key} className={`agent-card message kind-${ev.kind}`}>
          <span className="message-kind">{ev.kind}</span>
          <Markdown text={p.body} className="message-body" cards={options.cards} />
          {ev.kind === 'ask' && <span className="ask-hint">answer in the composer</span>}
          <TimeLabel ts={ev.ts} className="card-time-end" />
        </div>
      )
    }
    case 'model_changed': {
      const p = ev.payload as ModelChangedPayload
      return (
        <div key={key} className="agent-card marker">
          model → {p.model}{p.effort ? ` (${p.effort})` : ''}
        </div>
      )
    }
    case 'subagent_started': {
      const p = ev.payload as SubagentPayload
      return (
        <div key={key} className="agent-card subagent">
          ↳ {p.agent_type} [{p.model}]: {p.summary}
        </div>
      )
    }
    case 'subagent_done': {
      const p = ev.payload as SubagentPayload
      return (
        <div key={key} className="agent-card subagent done">
          ↲ {p.agent_type}: {p.summary}
        </div>
      )
    }
    case 'turn_done': {
      return (
        <div key={key} className="agent-card marker turn-done" data-testid="turn-done">
          {turnDoneText(ev, turnStart)}
        </div>
      )
    }
    case 'error': {
      const p = ev.payload as ErrorPayload
      return (
        <div key={key} className="agent-card error">⚠ {p.message} <TimeLabel ts={ev.ts} className="card-time-end" /></div>
      )
    }
    case 'compaction': {
      const p = ev.payload as CompactionPayload
      return (
        <div key={key} className="agent-card marker">✂ context compacted ({p.summary.length} char summary)</div>
      )
    }
    case 'artifact':
      return <ArtifactCard key={key} payload={ev.payload as ArtifactPayload} ts={ev.ts} />
    default: {
      const s = ev.payload as StatusPayload
      void s
      return null
    }
  }
}
