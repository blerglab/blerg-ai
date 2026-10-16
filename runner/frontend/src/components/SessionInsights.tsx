// SessionInsights: one session's figures (time, work, subagents, tokens and estimated cost) in a dialog
// opened from the session header. Fetched on open, and every 30 s while the session is live. The dialog
// shell is the Skills & plugins one (a modal on desktop, a full-screen sheet on narrow screens); the body
// is a row of headline tiles over one card per subject, each with a single figure (a stacked bar or a
// bar list) and the numbers beside it.
import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useBackdropClose } from '@blerglab/chat'
import { apiFetch } from '../apiFetch'
import {
  formatDuration, formatPercent, formatTokens, formatUSD,
  type CountRow, type SessionInsightsResponse, type TokenCounts,
} from '../lib/insights'
import './CapabilitiesPanel.css'
import './SessionInsights.css'

export const SESSION_INSIGHTS_REFRESH_MS = 30_000
const TOP_TOOLS = 5
// a stacked segment carries its percent inside only when it is at least this wide
const LABEL_MIN_SHARE = 0.15

interface Props {
  sessionId: string
  title: string
  // A live session's figures move: refresh them while the dialog is open.
  live: boolean
  /** Idle to the engine with background work running (lib/displayState): the strip says so. */
  background?: boolean
  onClose: () => void
}

const FOCUSABLE = 'button:not([disabled]), input:not([disabled]), [href], [tabindex]:not([tabindex="-1"])'

export default function SessionInsights({ sessionId, title, live, background = false, onClose }: Props) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const backdrop = useBackdropClose(onClose)
  // The last report, the session it is about and when it arrived: another session's figures are never shown.
  const [report, setReport] = useState<{ sessionId: string; d: SessionInsightsResponse; at: number } | null>(null)
  const data = report?.sessionId === sessionId ? report.d : null
  // what the last answer was for: loading is "no answer yet for the request now wanted"
  const [answered, setAnswered] = useState<{ key: string; failed: boolean } | null>(null)
  const [tick, setTick] = useState(0)
  const [now, setNow] = useState(() => Date.now())
  const reload = useCallback(() => setTick((k) => k + 1), [])
  // live is in the key so a session that ends while the dialog is open is read once more
  const key = `${sessionId}|${live}|${tick}`
  const loading = answered?.key !== key
  const error = !loading && answered?.failed === true
  const hasData = data !== null

  useEffect(() => {
    let cancelled = false
    apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/insights`)
      .then((r) => (r.ok ? (r.json() as Promise<SessionInsightsResponse>) : Promise.reject(new Error(String(r.status)))))
      .then((d) => {
        if (cancelled) return
        const at = Date.now()
        setReport({ sessionId, d, at })
        setNow(at)
        setAnswered({ key, failed: false })
      })
      .catch(() => { if (!cancelled) setAnswered({ key, failed: true }) })
    return () => { cancelled = true }
  }, [sessionId, key])

  useEffect(() => {
    if (!live) return
    const t = setInterval(reload, SESSION_INSIGHTS_REFRESH_MS)
    return () => clearInterval(t)
  }, [live, reload])

  // "updated 12 s ago" keeps counting while there is a report on screen
  useEffect(() => {
    if (!hasData) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [hasData])

  // Focus moves into the dialog on open and back to whatever had it on close.
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    dialogRef.current?.querySelector<HTMLElement>(FOCUSABLE)?.focus()
    return () => opener?.focus?.()
  }, [])

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onClose()
      return
    }
    if (e.key !== 'Tab' || !dialogRef.current) return
    const nodes = Array.from(dialogRef.current.querySelectorAll<HTMLElement>(FOCUSABLE))
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

  const name = title || data?.title || sessionId.slice(0, 8)

  return createPortal(
    <div className="caps-overlay" data-testid="session-insights-overlay" {...backdrop}>
      <div
        ref={dialogRef}
        className="caps-dialog session-insights-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        data-testid="session-insights"
        onKeyDown={onKeyDown}
      >
        <header className="caps-header">
          <h2 id={titleId} className="si-title" title={name}>
            <span className="si-title-name">{name}</span>{' '}
            <span className="si-title-suffix">· Insights</span>
          </h2>
          <button type="button" className="caps-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        {data && report && <MetaStrip d={data} ago={now - report.at} refreshing={loading} background={background} />}
        <div className="caps-body">
          <div className="si-body" aria-busy={loading}>
            {error && (
              <div role="alert" className="si-card si-error">
                <span className="si-error-msg">
                  <span aria-hidden="true">!</span>
                  {data ? 'Could not refresh these figures.' : 'Could not load insights for this session.'}
                </span>
                <button type="button" className="si-btn" onClick={reload}>Retry</button>
              </div>
            )}
            {loading && !data && <Skeleton />}
            {data && <Report d={data} />}
          </div>
        </div>
      </div>
    </div>,
    document.body,
  )
}

// ── header strip ────────────────────────────────────────────────────────────────────────────────────────

function sessionState(d: SessionInsightsResponse, background: boolean): { id: 'live' | 'background' | 'idle' | 'ended'; label: string; icon: string } {
  if (d.ended_at || d.status === 'ended' || d.status === 'stopped' || d.status === 'error') return { id: 'ended', label: 'ended', icon: '■' }
  // Its turn is over but its work is not: not idle.
  if (d.status === 'idle' && background) return { id: 'background', label: 'working in background', icon: '◐' }
  if (d.status === 'idle' || d.status === 'disconnected') return { id: 'idle', label: 'idle', icon: '○' }
  return { id: 'live', label: 'live', icon: '●' }
}

function formatAgo(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 5) return 'just now'
  if (s < 60) return `${s} s ago`
  return `${Math.floor(s / 60)} min ago`
}

function MetaStrip({ d, ago, refreshing, background }: { d: SessionInsightsResponse; ago: number; refreshing: boolean; background: boolean }) {
  const state = sessionState(d, background)
  const model = d.model.replace(/^claude-/, '')
  return (
    <div className="si-strip" data-testid="session-insights-meta">
      <ul className="si-chips" aria-label="About this session">
        <li className={`si-chip si-state-${state.id}`} data-testid="si-state">
          <span aria-hidden="true">{state.icon}</span>{state.label}
        </li>
        {d.runtime && <li className="si-chip">{d.runtime}</li>}
        {d.engine && <li className="si-chip">{d.engine}</li>}
        {model && <li className="si-chip si-mono" title={d.model}>{model}</li>}
      </ul>
      <span className="si-updated" data-testid="si-updated">
        updated {formatAgo(ago)}{refreshing ? ' · refreshing' : ''}
      </span>
    </div>
  )
}

// ── loading ────────────────────────────────────────────────────────────────────────────────────────────

function Skeleton() {
  return (
    <div role="status" className="si-skeleton">
      <span className="si-sr">Loading insights…</span>
      <div className="si-tiles" aria-hidden="true">
        {[0, 1, 2, 3].map((i) => <div key={i} className="si-ghost si-ghost-tile" />)}
      </div>
      {[0, 1, 2].map((i) => <div key={i} className="si-ghost si-ghost-card" aria-hidden="true" />)}
    </div>
  )
}

// ── the report ─────────────────────────────────────────────────────────────────────────────────────────

const tokenSum = (c: TokenCounts) => c.input + c.output + c.cache_read + c.cache_write
const count = (n: number) => n.toLocaleString('en-US')
const plural = (n: number, one: string, many: string) => `${count(n)} ${n === 1 ? one : many}`

function Report({ d }: { d: SessionInsightsResponse }) {
  const isAgent = d.kind === 'agent'
  return (
    <>
      <KpiTiles d={d} isAgent={isAgent} />
      {isAgent ? (
        <>
          <TimeCard d={d} />
          <TokensCard d={d} />
          <WorkCard d={d} isAgent />
          <SubagentsCard d={d} />
        </>
      ) : (
        <>
          <p className="si-empty" data-testid="session-insights-terminal">
            A terminal session runs the engine&rsquo;s own CLI, which does not report time, turns, tool calls,
            subagents or tokens.
          </p>
          <WorkCard d={d} isAgent={false} />
        </>
      )}
    </>
  )
}

function KpiTiles({ d, isAgent }: { d: SessionInsightsResponse; isAgent: boolean }) {
  const lifeSub = d.ended_at ? shortWhen(d.ended_at, 'ended') : shortWhen(d.started_at, 'since')
  return (
    <div className="si-tiles" data-testid="si-kpis">
      <Tile id="lifetime" label="Lifetime" value={formatDuration(d.lifetime_seconds)} sub={lifeSub} />
      {isAgent && <Tile id="turns" label="Turns" value={count(d.turns)} />}
      {isAgent && (
        <Tile id="tools" label="Tool calls" value={count(d.tool_calls)}
          sub={d.subagents.tool_calls ? `+${count(d.subagents.tool_calls)} by subagents` : undefined} />
      )}
      {isAgent && <TokensTile d={d} />}
    </div>
  )
}

// shortWhen renders "since Oct 14, 9:00 AM": short enough for one line under a tile's figure.
function shortWhen(iso: string, prefix: string): string {
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return ''
  return `${prefix} ${t.toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })}`
}

function TokensTile({ d }: { d: SessionInsightsResponse }) {
  const t = d.tokens
  if (!t) return <Tile id="tokens" label="Tokens" value="—" sub="owner and admins only" />
  // the figure a person reasons about is what went in and came out; cache reads ride in the subtitle
  const cost = t.totals.cost_usd
  const costText = cost === null ? 'unpriced' : `est. ${formatUSD(cost)}${t.unpriced_models.length > 0 ? ' + unpriced' : ''}`
  // two parts: on a narrow tile the cache figure drops to a second line (losing its separator on a
  // phone), and nothing is clipped
  const sub = <><span>{costText}</span> <span><span className="si-sep">· </span>cache {formatTokens(t.totals.cache_read)}</span></>
  return <Tile id="tokens" label="Tokens" value={formatTokens(t.totals.input + t.totals.output)} sub={sub} wrap />
}

function Tile({ id, label, value, sub, wrap }: { id: string; label: string; value: string; sub?: React.ReactNode; wrap?: boolean }) {
  return (
    <div className="si-tile" data-testid={`si-tile-${id}`}>
      <div className="si-tile-label">{label}</div>
      <div className="si-tile-value"><Figure text={value} /></div>
      <div className={`si-tile-sub${wrap ? ' si-tile-sub-parts' : ''}`}>{sub || '\u00a0'}</div>
    </div>
  )
}

// Figure sets the digits of a headline at full size and its units ("h", "min", "k", "M") smaller.
function Figure({ text }: { text: string }) {
  return (
    <>
      {text.split(/(\d[\d.,]*)/).filter(Boolean).map((part, i) =>
        /^\d/.test(part) ? <span key={i}>{part}</span> : <span key={i} className="si-unit">{part}</span>)}
    </>
  )
}

function Card({ testId, title, aside, children }: { testId: string; title: string; aside?: React.ReactNode; children: React.ReactNode }) {
  const id = useId()
  return (
    <section className="si-card" data-testid={testId} aria-labelledby={id}>
      <div className="si-card-head">
        <h3 id={id} className="si-card-title">{title}</h3>
        {aside && <span className="si-card-aside">{aside}</span>}
      </div>
      {children}
    </section>
  )
}

interface Part { id: string; label: string; value: number; text: string; extra?: string; series: 1 | 2 | 3 }

// StackedBar is one part-to-whole bar: segments in series order, a 2px surface gap between them, the
// percent inside a segment only when it is wide enough, and a legend beneath carrying every value.
function StackedBar({ parts, testId, legendTestId }: { parts: Part[]; testId: string; legendTestId?: string }) {
  const total = parts.reduce((n, p) => n + p.value, 0)
  return (
    <>
      <div className="si-stack" role="img" data-testid={testId} aria-label={parts.map((p) => `${p.label} ${p.text}`).join(', ')}>
        {parts.filter((p) => p.value > 0).map((p) => {
          const share = p.value / total
          return (
            <span key={p.id} className={`si-seg si-s-${p.series}`} data-testid={`${testId}-${p.id}`} style={{ width: `${share * 100}%` }}>
              {share >= LABEL_MIN_SHARE ? formatPercent(share) : null}
            </span>
          )
        })}
      </div>
      <ul className="si-legend" data-testid={legendTestId}>
        {parts.map((p) => (
          <li key={p.id}>
            <i className={`si-sw si-s-${p.series}`} aria-hidden="true" />
            <span className="si-legend-label">{p.label}</span>
            <span className="si-legend-value">{p.text}</span>
            {p.extra && <span className="si-legend-extra">{p.extra}</span>}
          </li>
        ))}
      </ul>
    </>
  )
}

function TimeCard({ d }: { d: SessionInsightsResponse }) {
  const parts: Part[] = [
    { id: 'running', label: 'Working', value: d.running_seconds, text: formatDuration(d.running_seconds), series: 1 },
    { id: 'waiting', label: 'Waiting on you', value: d.waiting_seconds, text: formatDuration(d.waiting_seconds), series: 2 },
    { id: 'idle', label: 'Idle', value: d.idle_seconds, text: formatDuration(d.idle_seconds), series: 3 },
  ]
  const total = parts.reduce((n, p) => n + p.value, 0)
  return (
    <Card testId="si-time" title="Where the time went">
      {total > 0
        ? <StackedBar parts={parts} testId="si-time-bar" />
        : <p className="si-empty">No working, waiting or idle time recorded yet.</p>}
    </Card>
  )
}

interface BarRow { key: string; value: number; text: string; tag?: string; mono?: boolean }

// BarList is one series of magnitudes: name, a bar scaled to the largest row, the number right-aligned.
function BarList({ rows, testId, rowTestId, narrow, children }: {
  rows: BarRow[]; testId: string; rowTestId: string; narrow?: boolean; children?: React.ReactNode
}) {
  const max = rows.reduce((m, r) => Math.max(m, r.value), 0)
  return (
    <ul className={`si-bars${narrow ? ' si-bars-narrow' : ''}`} data-testid={testId}>
      {rows.map((r) => (
        <li key={r.key} className="si-bar-row" data-testid={rowTestId}>
          <span className="si-bar-name" title={r.key}>
            <span className={`si-name${r.mono ? ' si-mono' : ''}`}>{r.key}</span>
            {r.tag && <span className="si-tag">{r.tag}</span>}
          </span>
          <span className="si-bar-track" aria-hidden="true">
            <span className="si-bar" style={{ width: `${max > 0 ? (r.value / max) * 100 : 0}%` }} />
          </span>
          <span className="si-bar-value">{r.text}</span>
        </li>
      ))}
      {children}
    </ul>
  )
}

const costNote = 'Estimates from the per-model prices an administrator sets on the Insights page'

function TokensCard({ d }: { d: SessionInsightsResponse }) {
  const t = d.tokens
  if (!t) {
    return (
      <Card testId="si-tokens" title="Tokens by model">
        <p className="si-empty" data-testid="si-tokens-hidden">Token figures are visible to the session&rsquo;s owner and administrators.</p>
      </Card>
    )
  }
  const aside = t.totals.cost_usd === null ? undefined : <span title={costNote}>est. {formatUSD(t.totals.cost_usd)}</span>
  if (t.by_model.length === 0) {
    return (
      <Card testId="si-tokens" title="Tokens by model">
        <p className="si-empty">No turns completed yet.</p>
      </Card>
    )
  }
  const rows: BarRow[] = [...t.by_model]
    .sort((a, b) => tokenSum(b) - tokenSum(a))
    .map((m) => ({ key: m.model, value: tokenSum(m), text: formatTokens(tokenSum(m)), tag: m.priced ? undefined : 'no price', mono: true }))
  const subTotal = tokenSum(t.subagents)
  const costText = (c: TokenCounts) => (c.cost_usd === null ? 'unpriced' : `est. ${formatUSD(c.cost_usd)}`)
  return (
    <Card testId="si-tokens" title="Tokens by model" aside={aside}>
      <BarList rows={rows} testId="si-models" rowTestId="si-model-row" />
      {subTotal > 0 && (
        <div className="si-block">
          <div className="si-subhead">Main agent and subagents</div>
          <StackedBar
            testId="si-token-split"
            legendTestId="si-token-split-legend"
            parts={[
              { id: 'main', label: 'Main agent', value: tokenSum(t.main), text: formatTokens(tokenSum(t.main)), extra: costText(t.main), series: 1 },
              { id: 'subagents', label: 'Subagents', value: subTotal, text: formatTokens(subTotal), extra: costText(t.subagents), series: 2 },
            ]}
          />
        </div>
      )}
      <dl className="si-figs" data-testid="si-token-figs">
        <div><dt>Input</dt><dd>{formatTokens(t.totals.input)}</dd></div>
        <div><dt>Output</dt><dd>{formatTokens(t.totals.output)}</dd></div>
        <div><dt>Cache read / write</dt><dd>{formatTokens(t.totals.cache_read)} / {formatTokens(t.totals.cache_write)}</dd></div>
        <div><dt>Cache hit rate</dt><dd>{formatPercent(t.cache_hit_rate)}</dd></div>
      </dl>
    </Card>
  )
}

function WorkCard({ d, isAgent }: { d: SessionInsightsResponse; isAgent: boolean }) {
  const line = [isAgent ? plural(d.messages_from_person, 'message from you', 'messages from you') : '', plural(d.artifacts, 'file published', 'files published')]
    .filter(Boolean).join(' · ')
  if (!isAgent) {
    return (
      <Card testId="si-work" title="Work">
        <p className="si-foot si-foot-solo" data-testid="si-work-line">{line}</p>
      </Card>
    )
  }
  const top = d.tool_calls_by_tool.slice(0, TOP_TOOLS)
  const rest = d.tool_calls_by_tool.slice(TOP_TOOLS).reduce((n, r) => n + r.count, 0)
  const p = d.proposals
  const proposals: CountRow[] = [
    { key: 'pending', count: p.pending }, { key: 'approved', count: p.approved },
    { key: 'declined', count: p.declined }, { key: 'expired', count: p.expired },
  ].filter((r) => r.count > 0)
  return (
    <Card testId="si-work" title="Work">
      <div className="si-work-grid">
        <div>
          <div className="si-subhead">Top tools</div>
          {top.length === 0 ? <p className="si-empty">No tool calls yet.</p> : (
            <BarList narrow testId="si-top-tools" rowTestId="si-tool-row"
              rows={top.map((r) => ({ key: r.key, value: r.count, text: count(r.count), mono: true }))}>
              {rest > 0 && <li className="si-bars-other" data-testid="si-tools-other">other tools · {count(rest)}</li>}
            </BarList>
          )}
        </div>
        <div className="si-work-side">
          <StatGroup title="MCP calls" total={d.mcp_calls.total} rows={d.mcp_calls.by_outcome} testId="si-mcp" />
          <StatGroup title="Proposals" total={p.pending + p.approved + p.declined + p.expired} rows={proposals} testId="si-proposals" />
        </div>
      </div>
      <p className="si-foot" data-testid="si-work-line">{line}</p>
    </Card>
  )
}

function StatGroup({ title, total, rows, testId }: { title: string; total: number; rows: CountRow[]; testId: string }) {
  return (
    <div className="si-stat" data-testid={testId}>
      <div className="si-stat-head">
        <span className="si-subhead">{title}</span>
        <span className="si-stat-n">{count(total)}</span>
      </div>
      {rows.length > 0 && (
        <ul className="si-chips">
          {rows.map((r) => <li key={r.key} className="si-chip"><span>{r.key}</span> <b>{count(r.count)}</b></li>)}
        </ul>
      )}
    </div>
  )
}

function SubagentsCard({ d }: { d: SessionInsightsResponse }) {
  const s = d.subagents
  const t = d.tokens?.subagents
  const aside = s.count > 0
    ? [plural(s.tool_calls, 'tool call', 'tool calls'), t ? `${formatTokens(tokenSum(t))} tokens` : ''].filter(Boolean).join(' · ')
    : undefined
  return (
    <Card testId="si-subagents" title={`Subagents · ${count(s.count)}`} aside={aside}>
      {s.count === 0 ? <p className="si-empty">No subagents</p> : (
        <div className="si-two">
          <CountTable head="Type" rows={s.by_type} testId="si-sub-types" />
          <CountTable head="Model" rows={s.by_model} testId="si-sub-models" mono />
        </div>
      )}
    </Card>
  )
}

function CountTable({ head, rows, testId, mono }: { head: string; rows: CountRow[]; testId: string; mono?: boolean }) {
  return (
    <table className="si-table" data-testid={testId}>
      <thead><tr><th scope="col">{head}</th><th scope="col" className="n">Count</th></tr></thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.key}><td className={mono ? 'si-mono' : undefined}>{r.key}</td><td className="n">{count(r.count)}</td></tr>
        ))}
      </tbody>
    </table>
  )
}
