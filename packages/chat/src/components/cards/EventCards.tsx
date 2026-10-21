import { modelDisplayName } from '../../model/modelDisplay'
// One card per kind of event: how each thing the transcript holds is drawn. renderEvent is the
// switch; the compact timeline and the legacy (one card per call) view both go through it.
import { memo, useState, type ReactNode } from 'react'
import type { CardRegistry } from '../../cards/registry'
import { isRetryNotice } from '../../model/notices'
import { topicDividerText } from '../../model/topicMarker'
import { isReviewStateEvent } from '../../model/reviewRounds'
import { automationAuthor, isFromParentSession, userMessageShape } from '../../model/userMessages'
import { parseTs, turnDuration } from '../../model/timeLabel'
import type {
  AgentEvent,
  ArtifactPayload,
  AssistantTextPayload,
  AssistantThinkingPayload,
  CheckInPayload,
  CompactionPayload,
  ErrorPayload,
  MessagingPayload,
  ModelChangedPayload,
  StatusPayload,
  SessionUploadPayload,
  SubagentPayload,
  ToolCallPayload,
  ToolResultPayload,
  TopicPayload,
  TurnDonePayload,
  UserMessagePayload,
} from '../../types'
import ArtifactCard from '../ArtifactCard'
import { ReviewRoundLine } from '../ReviewRoundLine'
import AskReply from '../AskReply'
import DelegationCard from './DelegationCard'
import ProposalCard from './ProposalCard'
import ReviewRequestCard from './ReviewRequestCard'
import { useAsks } from '../askContext'
import { MessageText } from '../AttachmentChips'
import CopyMessage from '../CopyMessage'
import Markdown from '../Markdown'
import { TimeLabel } from '../TimeLabel'
import { ChecklistRow, TicketCard } from './WorkCards'

/** What the host may add under a user or assistant card (ChatView's `slots.messageFooter`). */
export type MessageFooter = (ev: AgentEvent) => ReactNode

/** How the host wants one user message drawn (ChatView's `describeUserMessage`). */
export interface UserMessageLook {
  /** Who it is from, in place of "You": a host whose automation also messages the session says so. */
  author?: string
  /** Where the author lives in the host (the parent session's page): the author becomes a link
   *  there. With onAuthorOpen the host opens it itself (inside the app, no page load). */
  authorHref?: string
  onAuthorOpen?: () => void
  /** Draw it as one collapsed line that expands (the long prompt a session was started with). The
   *  string is the line's label; `true` uses "session brief". */
  brief?: boolean | string
}

/** Where a user message sits in its session. */
export interface UserMessageContext {
  /** It is the session's first message from the person's side (its brief, when an app started the
   *  session with one): the oldest user message held, with nothing older left on the server. Text
   *  nobody typed (model/userMessages.ts) never counts. */
  first: boolean
}

/** Decides how a user message is drawn; null or undefined leaves the chat's own look: the person's
 *  ("You"), or the automation that wrote it (a board, a parent session, a cron: automationAuthor). */
export type DescribeUserMessage = (ev: AgentEvent, payload: UserMessagePayload, context: UserMessageContext) => UserMessageLook | null | undefined

export interface RenderOptions {
  cards?: CardRegistry
  messageFooter?: MessageFooter
  describeUserMessage?: DescribeUserMessage
  /** The client_event_id of the session's first user message, when it is known (see
   *  UserMessageContext.first). */
  firstUserMessage?: string | null
  /** Which subagent cards the person opened or closed, by call id: kept by the transcript so a card
   *  drawn again (scrolled out of the window and back) keeps it. */
  subagentFolds?: Map<string, boolean>
}

// The prompt a session was started with: metadata more than conversation, so one dim line that
// opens on demand. Collapsed on every mount; opening it changes nothing else.
function BriefLine({ label, text, cards, eventId }: { label: string; text: string; cards?: CardRegistry; eventId?: string }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="agent-card brief" data-testid="message-brief" data-event-id={eventId}>
      <button type="button" className="brief-toggle" aria-expanded={open} onClick={() => setOpen(o => !o)}>
        <span aria-hidden="true">{open ? '▾' : '▸'}</span> {label} · {text.length.toLocaleString()} chars
      </button>
      {open && <div className="brief-body"><Markdown text={text} cards={cards} /></div>}
    </div>
  )
}

// The agent's question. Open, it has a reply box (when the transport can address an answer) or
// points at the composer; answered or closed, it says so. Drawn without a chat around it (a host
// rendering events itself), it is a plain card with the composer hint.
function AskCard({ ev, cards }: { ev: AgentEvent; cards?: CardRegistry }) {
  const asks = useAsks()
  const p = (ev.payload ?? {}) as Partial<MessagingPayload>
  const body = typeof p.body === 'string' ? p.body : ''
  const state = asks?.states.get(ev.client_event_id)
  const open = asks ? state?.open === true : true
  const ownerOnly = p.owner_only === true ? asks?.ownerOnly ?? 'elsewhere' : 'yes'
  const blocked = ownerOnly !== 'yes'
  const answer = blocked ? undefined : asks?.answer
  const what = p.no_wait ? 'question' : 'approval'
  const blockedHint = ownerOnly === 'no' ? `Only the session owner can answer this ${what}.`
    : ownerOnly === 'elsewhere' ? `Only the session owner can answer this ${what}, from the session in Runner.`
    : null
  // A question the agent did not wait on is answered here (or in the host's Needs you): the
  // composer's messages go to the agent as themselves.
  const hint = p.no_wait ? 'the agent keeps working; your answer is sent to it' : 'answer in the composer'
  return (
    <div className={`agent-card message kind-ask${open ? ' open' : ' closed'}`} data-testid="ask-card" data-event-id={ev.client_event_id}>
      <div className="ask-head">
        <span className="message-kind">{p.owner_only && !p.no_wait ? 'owner-only approval' : 'question'}</span>
        <Markdown text={body} className="message-body" cards={cards} />
        {open
          ? blocked ? blockedHint && <span className="ask-hint" data-testid="ask-owner-only">{blockedHint}</span> : (!answer || p.no_wait) && <span className="ask-hint">{hint}</span>
          : <span className="ask-hint" data-testid="ask-closed">{state?.answer ? 'answered' : 'closed'}</span>}
        <TimeLabel ts={ev.ts} className="card-time-end" />
      </div>
      {open && answer && <AskReply answer={answer} messageId={p.message_id || undefined} />}
    </div>
  )
}

// Thinking uses the same safe Markdown renderer as answers, with a distinct footer.
function ThinkingRow({ ev, text }: { ev: AgentEvent; text: string }) {
  return (
    <div className="agent-card assistant" data-kind="thinking" data-testid="thinking">
      <Markdown className="thinking-text" text={text} />
      <div className="card-foot"><span className="card-time">thinking · <TimeLabel ts={ev.ts} /></span></div>
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
    <div className={`agent-card tool ${ok === false ? 'tool-failed' : ''}`} data-testid="tool-card" data-call-id={call.call_id}>
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
      {p.stop_reason} · <span title={p.model}>{modelDisplayName(p.model)}</span> · {usageCost(p)}
      {p.check_in_missing ? ' · ⚠ no completion check-in' : ''}
      {' · '}<TimeLabel ts={ev.ts} />
      {d ? ` · ${d}` : ''}
    </>
  )
}

export const NO_RESULTS = new Map<string, ToolResultPayload>()

function tokensShort(n: number): string {
  return n >= 1000 ? `${Math.round(n / 1000).toLocaleString()}k` : String(n)
}

/** The compaction marker's line: why it ran and how big the context was (a Claude Code
 *  compaction), or how long the summary is (the native loop's, which keeps one). */
export function compactionText(p: Partial<CompactionPayload> | null | undefined): string {
  const parts: string[] = []
  if (p?.trigger) parts.push(p.trigger)
  if (typeof p?.pre_tokens === 'number' && p.pre_tokens > 0) parts.push(`${tokensShort(p.pre_tokens)} tokens before`)
  const summary = typeof p?.summary === 'string' ? p.summary.length : 0
  if (summary > 0) parts.push(`${summary.toLocaleString()} char summary`)
  return `Conversation compacted${parts.length ? ` (${parts.join(', ')})` : ''}`
}

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
      // Text nobody typed: harness text is hidden, the daemon's notes and a command's output are
      // system cards (model/userMessages.ts).
      const shape = userMessageShape(p)
      if (shape.as === 'hidden') return null
      if (shape.as === 'notice') {
        return (
          <div key={key} className="agent-card system-note" data-testid="system-note">
            <span className="system-label">{shape.label ?? 'system'}</span>
            <span className="system-text">{shape.text}</span>
            <TimeLabel ts={ev.ts} className="card-time-end" />
          </div>
        )
      }
      if (shape.as === 'output') {
        return (
          <div key={key} className="agent-card system-output" data-testid="system-output">
            <div className="system-head"><span className="system-label">output</span><TimeLabel ts={ev.ts} className="card-time-end" /></div>
            <pre className="system-pre">{shape.text}</pre>
          </div>
        )
      }
      const look = options.describeUserMessage?.(ev, p, { first: !!options.firstUserMessage && ev.client_event_id === options.firstUserMessage })
      if (look?.brief) {
        return <BriefLine key={key} label={typeof look.brief === 'string' ? look.brief : 'session brief'} text={p.text} cards={options.cards} eventId={key} />
      }
      const answer = p.source === 'ask_answer'
      // A board, a parent session or a cron wrote it, or the parent answered its child's question:
      // never drawn as "You".
      const parent = isFromParentSession(p)
      const author = look?.author ?? automationAuthor(parent ? 'session' : p.source)
      const label = author ?? 'You'
      const open = look?.onAuthorOpen
      return (
        <div key={key} className={`agent-card user source-${p.source}${author ? ' other-author' : ''}${parent ? ' from-parent' : ''}`} data-event-id={key}>
          <span className="card-author" data-testid="message-author">
            {look?.authorHref
              ? <a className="card-author-link" href={look.authorHref} onClick={open ? (e) => { e.preventDefault(); open() } : undefined}>{label}</a>
              : label}
            {answer && <span className="card-author-note"> · Answer</span>}
          </span>
          <MessageText text={p.text} cards={options.cards} at={ev.ts} />
          <CopyMessage text={p.text} />
          <div className="card-foot"><TimeLabel ts={ev.ts} /></div>
          {options.messageFooter?.(ev)}
        </div>
      )
    }
    case 'assistant_text': {
      const p = ev.payload as AssistantTextPayload
      return (
        <div key={key} className="agent-card assistant">
          <Markdown text={p.text} cards={options.cards} workspaceLinks />
          <CopyMessage text={p.text} />
          <div className="card-foot"><TimeLabel ts={ev.ts} /></div>
          {footer && <div className="turn-foot" data-testid="turn-done">{turnDoneText(footer, footerStart)}</div>}
          {options.messageFooter?.(ev)}
        </div>
      )
    }
    case 'assistant_thinking': {
      const p = ev.payload as AssistantThinkingPayload | null
      return <ThinkingRow key={key} ev={ev} text={typeof p?.text === 'string' ? p.text : ''} />
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
    case 'session_upload': {
      const p = ev.payload as Partial<SessionUploadPayload> | null
      const name = typeof p?.name === 'string' ? p.name : 'a file'
      const parent = typeof p?.from_title === 'string' && p.from_title ? p.from_title : 'another session'
      return <div key={key} className="agent-card marker" data-testid="session-upload" data-event-id={key}>📎 {name} · from {parent}{typeof p?.size === 'number' && Number.isFinite(p.size) && p.size >= 0 ? ` · ${p.size.toLocaleString()} bytes` : ''}<TimeLabel ts={ev.ts} className="card-time-end" /></div>
    }
    case 'delegation_request':
      return <DelegationCard key={key} ev={ev} />
    case 'delegation_decided':
      return null // folded into its request's card
    case 'proposal_request':
      return <ProposalCard key={key} ev={ev} />
    case 'proposal_decided':
      return null // folded into its request's card
    case 'review_requested':
      return <ReviewRequestCard key={key} ev={ev} />
    case 'review_decided':
      return null // folded into its request's card
    case 'ask':
      return <AskCard key={key} ev={ev} cards={options.cards} />
    case 'update':
    case 'note': {
      const p = ev.payload as MessagingPayload
      return (
        <div key={key} className={`agent-card message kind-${ev.kind}`} data-testid={`message-${ev.kind}`}>
          <span className="message-kind">{ev.kind}</span>
          <Markdown text={typeof p?.body === 'string' ? p.body : ''} className="message-body" cards={options.cards} workspaceLinks />
          <TimeLabel ts={ev.ts} className="card-time-end" />
        </div>
      )
    }
    case 'model_changed': {
      const p = ev.payload as ModelChangedPayload
      return (
        <div key={key} className="agent-card marker">
          model → <span title={p.model}>{modelDisplayName(p.model)}</span>{p.effort && <span className="model-effort" title="Configured effort">{p.effort}</span>}
        </div>
      )
    }
    case 'subagent_started': {
      const p = ev.payload as SubagentPayload
      return (
        <div key={key} className="agent-card subagent">
          ↳ {p.agent_type} [<span title={p.model}>{modelDisplayName(p.model)}</span>]: {p.summary}{p.resumed ? ' (resumed)' : ''}
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
      if (p?.client_id) {
        // A message the session dropped: its queued bubble is gone, so this row says what happened.
        return (
          <div key={key} className="agent-card error dropped" data-testid="message-dropped">
            ⚠ Your message was not delivered ({p.message}). Send it again.
            <TimeLabel ts={ev.ts} className="card-time-end" />
          </div>
        )
      }
      if (isRetryNotice(ev)) {
        // The engine is retrying the model API: a notice, the turn goes on.
        return <div key={key} className="agent-card marker retry-notice" data-testid="retry-notice">↻ {p.message}</div>
      }
      if (p?.code === 'api_error') {
        return (
          <div key={key} className="agent-card error api-error" data-testid="api-error">
            <span className="error-label">API error</span> {p.message} <TimeLabel ts={ev.ts} className="card-time-end" />
          </div>
        )
      }
      return (
        <div key={key} className="agent-card error">⚠ {p.message} <TimeLabel ts={ev.ts} className="card-time-end" /></div>
      )
    }
    case 'compaction':
      return <div key={key} className="agent-card marker" data-testid="compaction">✂ {compactionText(ev.payload as CompactionPayload)}</div>
    case 'artifact':
      // A review's state is not a document: its round of answers is one line (model/reviewRounds.ts).
      if (isReviewStateEvent(ev)) return <ReviewRoundLine key={key} ev={ev} />
      return <ArtifactCard key={key} payload={ev.payload as ArtifactPayload} ts={ev.ts} eventId={key} />
    case 'topic':
      return (
        <div key={key} className="agent-card marker topic-divider" data-testid="topic-divider" data-event-id={key}>
          {topicDividerText(ev.payload as TopicPayload)}
        </div>
      )
    case 'checklist':
      return <ChecklistRow key={key} ev={ev} />
    case 'ticket':
      return <TicketCard key={key} ev={ev} />
    default: {
      const s = ev.payload as StatusPayload
      void s
      return null
    }
  }
}
