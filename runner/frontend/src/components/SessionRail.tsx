// SessionRail: the session page's side panel, in four tabs. Live: what needs the person (the agent's
// open questions, with a box that answers each; tool changes waiting on a decision), what is running
// (subagents and background tasks, each with a way to its place in the conversation), the session's
// facts, and the chat's settings (Show thinking, Compact tool calls). History (HistoryTab.tsx): the pieces of work the session did. Files
// (FilesTab.tsx): the session's files. Capabilities (CapabilitiesTab.tsx): what it loaded. A tab other than Live
// reads the server only while it is on screen. The tab chosen is kept per browser. On a wide screen it is a panel beside the chat; on a narrow one a drawer over it (modal:
// Esc and the backdrop close it, focus stays inside, the page does not scroll behind it).
//
// It stays mounted while closed (hidden), so the background tasks' state — which failures were
// dismissed, since when it watches — lives as long as the page shows the session. Render it under
// a key of the session.
import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Link } from 'react-router-dom'
import {
  AskReply,
  KindIcon,
  StatusGlyph,
  elapsedOf,
  scrollToCall,
  scrollToEvent,
  taskFailed,
  titleOf,
  tokenLabel,
  useBackdropClose,
  useBackgroundTasks,
  useTranscriptStore,
  useTicker,
  clockLabel,
  fileCount,
  type AskLive,
  type BackgroundTask,
  type LiveState,
  type SubagentLive,
} from '@blerglab/chat'
import { ENGINE_LABELS, RUNTIME_LABELS } from '../lib/runtimes'
import { RAIL_TABS, lifetimeLabel, readRailTab, waitLabel, writeRailTab, type RailTab } from '../lib/sessionRail'
import FilesTab from './FilesTab'
import HistoryTab from './HistoryTab'
import CapabilitiesTab from './CapabilitiesTab'
import { useCapabilities } from '../hooks/useCapabilities'
import { enabledCapabilityCount, latestCapabilitiesEvent } from '../lib/capabilities'
import ShowLink from './RailShowLink'
import type { SessionInfo } from '../types'
import StatusPill from './StatusPill'
import UsageLimitResume from './UsageLimitResume'
import { backgroundCount, displayState } from '../lib/displayState'
import './SessionRail.css'

/** How long a "Show in conversation" that found nothing says so. */
const MISSING_MS = 4000

const FOCUSABLE = 'button:not([disabled]), textarea:not([disabled]), input:not([disabled]), [href], [tabindex]:not([tabindex="-1"])'

export interface SessionRailProps {
  /** The element's id (the header toggle's aria-controls). */
  id: string
  session: SessionInfo
  /** Where it runs, by name (the daemon's), for the Session facts. */
  machine?: string
  /** What is live in the session, read once by the page (useSessionLive: the server's whole log, or
   *  the chat's useLiveState on an older server); its header badge counts from it too. */
  live: LiveState
  /** The background tasks still running, by the server; absent or null: the chat's list (what the
   *  transcript holds). Finished ones always come from the chat's list (they linger, a failure stays). */
  tasks?: BackgroundTask[] | null
  /** What runs live (subagents and tasks not done) by the server's whole log, for the Status row;
   *  null when there is no such read (the server's per-session count is used). The header's too. */
  liveRunning?: number | null
  /** Tool changes waiting on a decision on this session. */
  toolChanges: number
  /** A panel beside the chat (a wide screen) or a drawer over it. */
  mode: 'panel' | 'drawer'
  open: boolean
  onClose: () => void
  /** The Capabilities tab came on screen: the page reads the tool changes again. */
  onCapabilitiesShown?: () => void
  /** The chat's settings, held by the page (the chat draws what they say) and kept per browser. */
  settings: RailSettings
  /** Opens a file in the chat's viewer (History's version chips). */
  onOpenArtifact: (id: string) => void
  /** The panel opened by itself for a question: show Live (not saved as the choice), then say so. */
  showLive?: boolean
  /** Called once Live was shown for showLive; the page clears its request. */
  onLiveShown?: () => void
}

export interface RailSettings {
  thinking: boolean
  onThinking: (on: boolean) => void
  compact: boolean
  onCompact: (on: boolean) => void
}

const NO_EVENTS: never[] = []

export default function SessionRail(props: SessionRailProps) {
  const { session, live, toolChanges, mode, open, onClose } = props
  const bg = useBackgroundTasks(session.id)
  // A background subagent is also a background task: its subagent row says more, so the task row goes.
  const subKeys = new Set(live.subagents.map(s => s.key))
  const ended = new Set(bg.recent.map(t => t.taskId))
  const running = (props.tasks ?? bg.running).filter(t => !ended.has(t.taskId))
  const tasks = [...running, ...bg.recent].filter(t => !(t.kind === 'agent' && subKeys.has(t.callId)))
  const runningTasks = tasks.filter(t => !t.done).length
  // The clock runs only while something timed is on screen.
  const timed = open && (live.subagents.length > 0 || live.openAsks.length > 0 || tasks.length > 0)
  const now = useTicker(timed) - live.clockOffset
  // Lifetime moves by the minute: a slow clock while the panel is open.
  const minute = useMinute(open)

  // "Show in conversation" that found nothing (the row is above what the transcript has loaded).
  const [missing, setMissing] = useState<string | null>(null)
  useEffect(() => {
    if (missing === null) return
    const t = setTimeout(() => setMissing(null), MISSING_MS)
    return () => clearTimeout(t)
  }, [missing])

  // The chat is on the page under the drawer too: look first, and close the drawer only when the
  // row was found, so a row that is not loaded says so where the person is looking.
  const jump = (rowKey: string, go: () => boolean) => {
    if (!go()) {
      setMissing(rowKey)
      return
    }
    setMissing(null)
    if (mode === 'drawer') onClose()
  }

  // Live (what needs the person, what runs, the session) or History; the person's choice is kept.
  const [tab, setTab] = useState<RailTab>(readRailTab)
  // Opened by itself for a question: show it, without making Live the person's choice; then tell
  // the page, which clears the request, so the next one is taken again.
  const wantLive = props.showLive ?? false
  const [liveTaken, setLiveTaken] = useState(false)
  if (wantLive && !liveTaken) {
    setLiveTaken(true)
    setTab('live')
  } else if (!wantLive && liveTaken) {
    setLiveTaken(false)
  }
  const { onLiveShown } = props
  useEffect(() => { if (liveTaken) onLiveShown?.() }, [liveTaken, onLiveShown])
  const pick = (t: RailTab) => {
    setTab(t)
    writeRailTab(t)
  }
  const capabilitiesShown = open && tab === 'capabilities'
  const { onCapabilitiesShown } = props
  // Only a switch to Capabilities while the panel is open: opening the panel (on Capabilities, or on mount)
  // already has the page read the tool changes again.
  const wasOpen = useRef(false)
  useEffect(() => {
    const opened = open && !wasOpen.current
    wasOpen.current = open
    if (capabilitiesShown && !opened) onCapabilitiesShown?.()
  }, [capabilitiesShown, open, onCapabilitiesShown])
  // What the session loaded: the newest report in the transcript, and the server's once the tab has
  // been on screen. The tab shows it; the tab's header badge counts what is enabled in it.
  const capEvent = useTranscriptStore(s => latestCapabilitiesEvent(s.sessions[session.id]?.events))
  const capEvents = useMemo(() => (capEvent ? [capEvent] : NO_EVENTS), [capEvent])
  const capabilities = useCapabilities(session.id, capEvents, capabilitiesShown)
  const enabled = enabledCapabilityCount(capabilities?.payload)
  // On a phone the drawer goes first, so the file opens over the chat the person is looking at.
  const openFile = (id: string) => {
    if (mode === 'drawer') onClose()
    props.onOpenArtifact(id)
  }
  const attach = live.files?.attach
  const attachFiles = attach ? () => {
    if (mode === 'drawer') onClose()
    attach()
  } : undefined
  const { settings } = props

  // What was typed in an ask's box, by ask: it outlives the drawer closing (the box goes with it).
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const draftFor = (eventId: string) => ({
    draft: drafts[eventId] ?? '',
    onDraft: (text: string) => setDrafts(d => ({ ...d, [eventId]: text })),
  })

  const needsCount = live.openAsks.length + (toolChanges > 0 ? 1 : 0)
  const runningCount = live.subagents.length + runningTasks

  const liveBody = (
    <>
      <Section title="Needs you" count={live.openAsks.length + toolChanges} tone="attention" testId="rail-needs" empty="Nothing needs you.">
        {needsCount > 0 && (
          <ul className="rail-list">
            {live.openAsks.map(a => (
              <AskRow
                key={a.eventId}
                ask={a}
                now={now}
                answer={live.answer}
                {...draftFor(a.eventId)}
                missing={missing === a.eventId}
                onShow={() => jump(a.eventId, () => scrollToEvent(a.eventId))}
              />
            ))}
            {toolChanges > 0 && (
              <li className="rail-item" data-testid="rail-tool-changes">
                <div className="rail-item-title">
                  {toolChanges} tool {toolChanges === 1 ? 'change waits' : 'changes wait'} on your decision
                </div>
                <div className="rail-item-line">An MCP server changed or added a tool this session uses; it is off until you decide.</div>
                <div className="rail-actions">
                  <Link className="rail-link" to={`/proposals?session=${encodeURIComponent(session.id)}`}>Review on the Proposals page</Link>
                </div>
              </li>
            )}
          </ul>
        )}
      </Section>

      <Section title="Running now" count={runningCount} tone="running" testId="rail-running" empty="Nothing is running.">
        {live.subagents.length + tasks.length > 0 && (
          <ul className="rail-list">
            {live.subagents.map(s => (
              <SubagentRow
                key={s.key}
                sub={s}
                now={now}
                missing={missing === `sub:${s.key}`}
                onShow={() => jump(`sub:${s.key}`, () => scrollToCall(s.key))}
              />
            ))}
            {tasks.map(t => (
              <TaskRow
                key={t.taskId}
                t={t}
                now={now}
                missing={missing === `task:${t.taskId}`}
                onShow={() => jump(`task:${t.taskId}`, () => scrollToCall(t.callId))}
                onDismiss={() => bg.dismiss(t.taskId)}
              />
            ))}
          </ul>
        )}
      </Section>

      <Section title="Session" testId="rail-session">
        <SessionFacts session={session} machine={props.machine} now={minute} liveRunning={props.liveRunning ?? null} />
      </Section>

      <Section title="Settings" testId="rail-settings">
        <div className="rail-settings">
          <label className="rail-check" title="Show the model's thinking in the transcript">
            <input type="checkbox" checked={settings.thinking} onChange={e => settings.onThinking(e.target.checked)} />
            <span>Show thinking</span>
          </label>
          <label className="rail-check" title="Fold runs of tool calls into one summary row">
            <input type="checkbox" checked={settings.compact} onChange={e => settings.onCompact(e.target.checked)} />
            <span>Compact tool calls</span>
          </label>
          {(!session.engine || session.engine === 'claude' || session.engine === 'codex') &&
            <UsageLimitResume key={session.id} sessionId={session.id} />}
        </div>
      </Section>
    </>
  )

  const body = (
    <>
      <RailTabs idBase={props.id} tab={tab} onPick={pick} needs={live.openAsks.length + toolChanges} files={live.files ? fileCount(live.files.items) : 0} enabled={enabled} />
      <div className="rail-tabpanel" role="tabpanel" id={`${props.id}-panel-live`} aria-labelledby={`${props.id}-tab-live`} hidden={tab !== 'live'}>
        {liveBody}
      </div>
      {/* Kept mounted while Live shows, so its list, opened cards and read state survive a switch;
          it reads the server and moves its clock only while on screen. */}
      <div className="rail-tabpanel" role="tabpanel" id={`${props.id}-panel-history`} aria-labelledby={`${props.id}-tab-history`} hidden={tab !== 'history'}>
        <HistoryTab sessionId={session.id} active={open && tab === 'history'} filesKey={live.filesKey} missing={missing} onJump={jump} onOpenArtifact={openFile} />
      </div>
      {/* Files and Capabilities, kept mounted the same way: opened versions, a filter, opened groups. */}
      <div className="rail-tabpanel" role="tabpanel" id={`${props.id}-panel-files`} aria-labelledby={`${props.id}-tab-files`} hidden={tab !== 'files'}>
        <FilesTab sessionId={session.id} active={open && tab === 'files'} files={live.files} onOpenArtifact={openFile} onAttach={attachFiles} />
      </div>
      <div className="rail-tabpanel" role="tabpanel" id={`${props.id}-panel-capabilities`} aria-labelledby={`${props.id}-tab-capabilities`} hidden={tab !== 'capabilities'}>
        <CapabilitiesTab report={capabilities} pendingToolChanges={toolChanges} />
      </div>
    </>
  )

  if (mode === 'drawer') return open ? <Drawer id={props.id} onClose={onClose}>{body}</Drawer> : null
  return (
    <aside id={props.id} className="session-rail panel" aria-label="Session panel" data-testid="session-rail" hidden={!open}>
      {/* No close button of its own: the header's panel toggle, right above, opens and closes it. */}
      <header className="rail-header">
        <h2 className="rail-heading">Session panel</h2>
      </header>
      <div className="rail-body">{body}</div>
    </aside>
  )
}

/** What can take the focus in the drawer, the tabs not on screen left out. */
function shown(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(n => !n.closest('[hidden]'))
}

/** A narrow screen: the panel slides over the chat from the right, over a dimmed page. */
function Drawer({ id, onClose, children }: { id: string; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLElement | null>(null)
  const titleId = useId()
  const backdrop = useBackdropClose(onClose)

  // Focus moves in on open and back to whatever had it (the toggle) on close; the page behind does
  // not scroll while it is open.
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    if (ref.current) shown(ref.current)[0]?.focus()
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = overflow
      opener?.focus?.({ preventScroll: true })
    }
  }, [])

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onClose()
      return
    }
    if (e.key !== 'Tab' || !ref.current) return
    const nodes = shown(ref.current)
    if (nodes.length === 0) return
    const first = nodes[0]
    const last = nodes[nodes.length - 1]
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault()
      first.focus()
    }
  }

  return createPortal(
    <div className="rail-overlay" data-testid="session-rail-backdrop" {...backdrop}>
      <aside
        ref={ref}
        id={id}
        className="session-rail drawer"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        data-testid="session-rail"
        onKeyDown={onKeyDown}
      >
        <header className="rail-header">
          <h2 className="rail-heading" id={titleId}>Session panel</h2>
          <button type="button" className="rail-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        <div className="rail-body">{children}</div>
      </aside>
    </div>,
    document.body,
  )
}

const TABS = RAIL_TABS
const TAB_LABEL: Record<RailTab, string> = { live: 'Live', history: 'History', files: 'Files', capabilities: 'Capabilities' }

/** Live · History · Files · Capabilities, with the arrow keys, Home and End moving between them (the tabs pattern). */
function RailTabs({ idBase, tab, onPick, needs, files, enabled }: { idBase: string; tab: RailTab; onPick: (t: RailTab) => void; needs: number; files: number; enabled: number }) {
  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    const at = TABS.indexOf(tab)
    const to: Record<string, number> = { ArrowRight: (at + 1) % TABS.length, ArrowLeft: (at + TABS.length - 1) % TABS.length, Home: 0, End: TABS.length - 1 }
    if (!(e.key in to)) return
    e.preventDefault()
    const next = TABS[to[e.key]]
    onPick(next)
    document.getElementById(`${idBase}-tab-${next}`)?.focus()
  }
  return (
    <div className="rail-tabs" role="tablist" aria-label="Session panel views" onKeyDown={onKeyDown}>
      {TABS.map(t => (
        <button
          key={t}
          type="button"
          role="tab"
          id={`${idBase}-tab-${t}`}
          className={`rail-tab${tab === t ? ' on' : ''}`}
          aria-selected={tab === t}
          aria-controls={`${idBase}-panel-${t}`}
          tabIndex={tab === t ? 0 : -1}
          onClick={() => onPick(t)}
        >
          {TAB_LABEL[t]}
          {/* What needs the person, on Live: a question shows from History too. */}
          {t === 'live' && needs > 0 && (
            <>
              <span className="rail-count attention rail-tab-count" aria-hidden="true" data-testid="rail-tab-count">{needs}</span>
              <span className="rail-sr">, {needs} {needs === 1 ? 'needs' : 'need'} you</span>
            </>
          )}
          {/* How many files the session holds (names, not versions), on Files, to draw the eye there. */}
          {t === 'files' && files > 0 && (
            <>
              <span className="rail-count rail-tab-count" aria-hidden="true" data-testid="rail-files-tab-count">{files}</span>
              <span className="rail-sr">, {files} {files === 1 ? 'file' : 'files'}</span>
            </>
          )}
          {/* What the session has switched on, on Capabilities. */}
          {t === 'capabilities' && enabled > 0 && (
            <>
              <span className="rail-count rail-tab-count" aria-hidden="true" data-testid="rail-capabilities-tab-count">{enabled}</span>
              <span className="rail-sr">, {enabled} enabled</span>
            </>
          )}
        </button>
      ))}
    </div>
  )
}

function Section({ title, count, tone, testId, empty, children }: {
  title: string
  count?: number
  /** How a count above zero is coloured: something waits on the person, or something runs. */
  tone?: 'attention' | 'running'
  testId: string
  empty?: string
  children?: ReactNode
}) {
  const headId = useId()
  const hasContent = !!children
  return (
    <section className="rail-section" aria-labelledby={headId} data-testid={testId}>
      <h3 className="rail-section-head" id={headId}>
        <span>{title}</span>
        {count !== undefined && <span className={`rail-count${count > 0 && tone ? ` ${tone}` : ''}`} data-testid={`${testId}-count`}>{count}</span>}
      </h3>
      {hasContent ? children : empty && <p className="rail-empty">{empty}</p>}
    </section>
  )
}

function AskRow({ ask, now, answer, draft, onDraft, missing, onShow }: {
  ask: AskLive
  now: number
  answer?: LiveState['answer']
  draft: string
  onDraft: (text: string) => void
  missing: boolean
  onShow: () => void
}) {
  return (
    <li className="rail-item rail-ask" data-testid="rail-ask">
      <div className="rail-item-kicker">
        <span>Question</span>
        <span className="rail-wait" data-testid="rail-ask-wait">{waitLabel(ask.askedAt, now)}</span>
      </div>
      <div className="rail-ask-text">{ask.question}</div>
      {answer
        ? <AskReply answer={answer} messageId={ask.messageId || undefined} label="Your answer" className="rail-reply" draft={draft} onDraft={onDraft} />
        : <div className="rail-item-line">Answer it from the message box once the session can take a message.</div>}
      <div className="rail-actions"><ShowLink onShow={onShow} missing={missing} /></div>
    </li>
  )
}

function SubagentRow({ sub, now, missing, onShow }: { sub: SubagentLive; now: number; missing: boolean; onShow: () => void }) {
  const model = sub.model.replace(/^claude-/, '')
  return (
    // A background subagent is drawn dashed, like its card in the transcript.
    <li className={`rail-item rail-sub${sub.background ? ' background' : ''}`} data-testid="rail-subagent" data-key={sub.key} data-status={sub.background ? 'background' : 'running'}>
      <div className="rail-item-head">
        <span className="rail-sub-icon" aria-hidden="true">↳</span>
        <span className="rail-item-title" title={sub.title}>{sub.title}</span>
        {sub.startedAt !== null && <span className="rail-clock" data-testid="rail-elapsed">{clockLabel(now - sub.startedAt)}</span>}
      </div>
      <div className="rail-chips">
        <span className="rail-chip status"><span className="rail-spin" aria-hidden="true" />{sub.background ? 'in the background' : 'running'}</span>
        {sub.resumed && <span className="rail-chip" data-testid="rail-sub-resumed">resumed</span>}
        {sub.type && <span className="rail-chip" data-testid="rail-sub-type">{sub.type}</span>}
        {model && <span className="rail-chip" data-testid="rail-sub-model">{model}</span>}
        {sub.tokens !== null && sub.tokens > 0 && <span className="rail-chip" data-testid="rail-sub-tokens">{tokenLabel(sub.tokens)}</span>}
      </div>
      {sub.doing && <div className="rail-item-line doing" data-testid="rail-sub-doing">{sub.doing}</div>}
      <div className="rail-actions"><ShowLink onShow={onShow} missing={missing} /></div>
    </li>
  )
}

function TaskRow({ t, now, missing, onShow, onDismiss }: {
  t: BackgroundTask
  now: number
  missing: boolean
  onShow: () => void
  onDismiss: () => void
}) {
  const failed = taskFailed(t)
  const state = !t.done ? 'running' : failed ? 'failed' : 'done'
  return (
    <li className={`rail-item rail-task ${state}`} data-testid="rail-task" data-task-id={t.taskId} title={titleOf(t)}>
      <div className="rail-item-head">
        <KindIcon t={t} />
        <span className="rail-item-title">{t.description || t.taskId}</span>
        <span className="rail-clock" data-testid="rail-elapsed">{elapsedOf(t, now)}</span>
        <span className="rail-glyph"><StatusGlyph t={t} /></span>
      </div>
      <div className="rail-item-line">
        {t.startedAt !== null && (
          <time dateTime={new Date(t.startedAt).toISOString()}>
            started {new Date(t.startedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}
          </time>
        )}
        {!t.done && <span className="rail-task-state"> · running</span>}
      </div>
      {t.done && t.done.summary && <div className={`rail-item-line${failed ? ' bad' : ''}`} data-testid="rail-task-summary">{t.done.summary}</div>}
      <div className="rail-actions">
        <ShowLink onShow={onShow} missing={missing} />
        {failed && <button type="button" className="rail-link" data-testid="rail-dismiss" onClick={onDismiss}>Dismiss</button>}
      </div>
    </li>
  )
}

function SessionFacts({ session, machine, now, liveRunning }: { session: SessionInfo; machine?: string; now: number; liveRunning: number | null }) {
  const started = Date.parse(session.started_at)
  const ended = session.ended_at ? Date.parse(session.ended_at) : NaN
  const runtime = RUNTIME_LABELS[session.runtime || 'daemon'] ?? session.runtime
  const engine = ENGINE_LABELS[session.engine || 'claude'] ?? session.engine
  const model = session.model ? session.model.replace(/^claude-/, '') : 'engine default'
  return (
    <dl className="rail-facts">
      <div>
        <dt>Status</dt>
        <dd><StatusPill state={displayState(session, liveRunning)} tasks={backgroundCount(session, liveRunning)} testId="rail-status" /></dd>
      </div>
      <div><dt>Runs on</dt><dd>{runtime}{machine ? ` · ${machine}` : ''}</dd></div>
      <div><dt>Engine</dt><dd>{engine}</dd></div>
      <div><dt>Model</dt><dd>{model}{session.effort ? ` · ${session.effort}` : ''}</dd></div>
      {Number.isFinite(started) && (
        <div>
          <dt>Started</dt>
          <dd><time dateTime={new Date(started).toISOString()}>{new Date(started).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}</time></dd>
        </div>
      )}
      {Number.isFinite(started) && (
        <div>
          <dt>{Number.isFinite(ended) ? 'Ran for' : 'Lifetime'}</dt>
          <dd data-testid="rail-lifetime">{lifetimeLabel((Number.isFinite(ended) ? ended : now) - started)}</dd>
        </div>
      )}
    </dl>
  )
}

function useMinute(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const t = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(t)
  }, [active])
  return now
}

/** The side panel's icon: a window with its right column marked. */
export function PanelIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.4">
      <rect x="1.5" y="2.5" width="13" height="11" rx="2" />
      <line x1="10" y1="2.5" x2="10" y2="13.5" />
      <rect x="10.6" y="3.2" width="3.2" height="9.6" rx="1" fill="currentColor" stroke="none" opacity="0.5" />
    </svg>
  )
}
