import { useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { modelDisplayNames } from '@blerglab/chat'
import InsightsTable from './InsightsTable'
import StartSteps from './StartSteps'
import { useMediaQuery } from '../hooks/useMediaQuery'
import { NARROW_QUERY, type InsightsSection } from './Insights'
import { formatDuration, formatPercent, formatTokens, formatUSD, outcomeLabel, shortDay, stepLabel, type InsightsResponse } from '../lib/insights'
export default function InsightsDetails({ d, section, workspace }: { d: InsightsResponse; section: InsightsSection; workspace?: string }) {
  const viewportNarrow = useMediaQuery(NARROW_QUERY)
  const reportRef = useRef<HTMLDivElement>(null)
  const [panelNarrow, setPanelNarrow] = useState(false)
  useEffect(() => {
    const report = reportRef.current
    if (!report) return
    const observer = new ResizeObserver(() => setPanelNarrow(report.clientWidth <= 960))
    observer.observe(report)
    setPanelNarrow(report.clientWidth > 0 && report.clientWidth <= 640)
    return () => observer.disconnect()
  }, [])
  const narrow = viewportNarrow || panelNarrow
  const modelNames = modelDisplayNames(d.tokens.by_model.map(m => ({ id: m.model })))
  const empty = d.sessions.started === 0 && d.tokens.totals.turns === 0
  // the headline startup figure: the runtime that started the most sessions
  const headline = useMemo(() => {
    const ok = d.startup.groups.filter((g) => g.kind === 'start' && g.seconds.count > 0)
    return ok.sort((a, b) => b.attempts - a.attempts)[0] ?? d.startup.groups.find((g) => g.seconds.count > 0)
  }, [d])
  const stepRuntime = headline?.runtime
  const steps = d.startup.steps.filter((s) => s.runtime === stepRuntime)

  return (
    <div className="insights-report" ref={reportRef}>

      {section === 'overview' && <div className="cards">
        <Card id="started" label={workspace ? 'Channels started' : 'Workspaces started'} value={String(workspace ? d.sessions.channels : d.sessions.started)}
          hint={`${d.sessions.alive} alive now`} />
        <Card id="startup" label={headline ? `Median startup (${headline.runtime})` : 'Median startup'} value={headline ? formatDuration(headline.seconds.p50) : '—'}
          hint={headline ? `90% under ${formatDuration(headline.seconds.p90)}` : 'no completed starts'} />
        <Card id="tokens" label="Output tokens" value={formatTokens(d.tokens.totals.output)} hint={`${formatTokens(d.tokens.totals.input)} input · ${d.tokens.totals.turns} turns`} />
        <Card id="cost" label="Estimated cost" value={d.tokens.unpriced_models.length ? '—' : formatUSD(d.tokens.totals.cost_usd)}
          hint={d.tokens.unpriced_models.length ? 'Price unavailable' : d.tokens.totals.cost_usd === null ? 'No cost recorded' : 'at the prices set below'}
          tooltip={d.tokens.unpriced_models.length ? 'Some models have no price set. A complete cost estimate is unavailable.' : undefined} />
        <Card id="cache" label="Cache hit rate" value={d.tokens.totals.turns ? formatPercent(d.tokens.cache_hit_rate) : '—'} hint={`${formatTokens(d.tokens.totals.cache_read)} read from cache`} />
      </div>}

      {section === 'live' && <p className="insights-note">{d.sessions.alive} {workspace ? 'channels' : 'workspaces'} alive · refreshes every 30 seconds while visible. Updated {new Date(d.generated_at).toLocaleTimeString()}.</p>}
      {empty && <div className="insights-note">No {workspace ? 'channels' : 'workspaces'} in this range.</div>}

      {section === 'overview' && <section>
        <h2>Pod startup</h2>
        {d.startup.groups.length === 0 ? <p className="insights-note">No starts recorded in this range.</p> : (
          <>
            {narrow ? (
              <ul className="stack" data-testid="startup-stack">
                {d.startup.groups.map((g) => (
                  <li key={g.runtime + g.kind}>
                    <div className="stack-head">
                      <span className="stack-name">{g.runtime} · {g.kind}</span>
                      <span className="muted">{g.attempts} {g.attempts === 1 ? 'attempt' : 'attempts'}, {g.failed} failed</span>
                    </div>
                    <dl className="stack-figs three">
                      <Fig k="Median" v={g.seconds.count ? formatDuration(g.seconds.p50) : '—'} />
                      <Fig k="90th" v={g.seconds.count ? formatDuration(g.seconds.p90) : '—'} />
                      <Fig k="Slowest" v={g.seconds.count ? formatDuration(g.seconds.max) : '—'} />
                    </dl>
                  </li>
                ))}
              </ul>
            ) : (
            <InsightsTable>
              <thead><tr><th>Runtime</th><th>Kind</th><th className="n">Attempts</th><th className="n">Failed</th><th className="n">Median</th><th className="n">90th</th><th className="n">Slowest</th></tr></thead>
              <tbody>
                {d.startup.groups.map((g) => (
                  <tr key={g.runtime + g.kind} data-sort={[g.runtime, g.kind, g.attempts, g.failed, g.seconds.p50, g.seconds.p90, g.seconds.max]}>
                    <td>{g.runtime}</td><td>{g.kind}</td><td className="n">{g.attempts}</td><td className="n">{g.failed}</td>
                    <td className="n">{g.seconds.count ? formatDuration(g.seconds.p50) : '—'}</td>
                    <td className="n">{g.seconds.count ? formatDuration(g.seconds.p90) : '—'}</td>
                    <td className="n">{g.seconds.count ? formatDuration(g.seconds.max) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </InsightsTable>
            )}
            {steps.length > 0 && stepRuntime && <StartSteps runtime={stepRuntime} steps={steps} />}
            {d.startup.failures.length > 0 && (
              <p className="insights-note">Failed starts by stage: {d.startup.failures.map((f) => `${stepLabel(f.key)} ${f.count}`).join(' · ')}</p>
            )}
            {d.startup.slowest.length > 0 && (
              <>
                <h3>Slowest starts</h3>
                <ul className="plain">
                  {d.startup.slowest.map((s) => (
                    <li key={s.session_id + s.at}>
                      <Link to={`/sessions/${s.session_id}`}>{s.title || s.session_id.slice(0, 8)}</Link>
                      <span className="muted"> · {s.runtime} · {formatDuration(s.seconds)}</span>
                    </li>
                  ))}
                </ul>
              </>
            )}
          </>
        )}
      </section>}

      {(section === 'overview' || section === 'usage') && <section>
        <h2>Tokens and cost</h2>
        {d.tokens.by_model.length === 0 ? <p className="insights-note">No turns completed in this range.</p> : (
          <>
            {narrow ? (
              <ul className="stack" data-testid="tokens-stack">
                {d.tokens.by_model.map((m) => (
                  <li key={m.model}>
                    <div className="stack-head"><span className="stack-name" title={m.model}>{modelNames.get(m.model)}</span></div>
                    <dl className="stack-figs">
                      <Fig k="Turns" v={String(m.turns)} />
                      <Fig k="Input" v={formatTokens(m.input)} />
                      <Fig k="Output" v={formatTokens(m.output)} />
                      <Fig k="Cache read" v={formatTokens(m.cache_read)} />
                      <Fig k="Cache write" v={formatTokens(m.cache_write)} />
                      <Fig k="Est. cost" v={m.priced ? formatUSD(m.cost_usd) : <span className="muted" title="No price set for this model">no price</span>} />
                    </dl>
                  </li>
                ))}
              </ul>
            ) : (
            <InsightsTable>
              <thead><tr><th>Model</th><th className="n">Turns</th><th className="n">Input</th><th className="n">Output</th><th className="n">Cache read</th><th className="n">Cache write</th><th className="n">Est. cost</th></tr></thead>
              <tbody>
                {d.tokens.by_model.map((m) => (
                  <tr key={m.model} data-sort={[m.model, m.turns, m.input, m.output, m.cache_read, m.cache_write, m.cost_usd]}>
                    <td title={m.model}>{modelNames.get(m.model)}</td><td className="n">{m.turns}</td><td className="n">{formatTokens(m.input)}</td><td className="n">{formatTokens(m.output)}</td>
                    <td className="n">{formatTokens(m.cache_read)}</td><td className="n">{formatTokens(m.cache_write)}</td>
                    <td className="n">{m.priced ? formatUSD(m.cost_usd) : <span className="muted" title="No price set for this model">no price</span>}</td>
                  </tr>
                ))}
              </tbody>
            </InsightsTable>
            )}
            <DailyChart days={d.tokens.daily} narrow={narrow} />
            {d.tokens.top_sessions.length > 0 && (
              <>
                <h3>Biggest {workspace ? 'channels' : 'workspaces'}</h3>
                <ul className="plain">
                  {d.tokens.top_sessions.map((s) => (
                    <li key={s.session_id}>
                      <Link to={`/sessions/${s.session_id}`}>{s.title || s.session_id.slice(0, 8)}</Link>
                      <span className="muted"> · {s.runtime} · {formatTokens(s.output)} out / {formatTokens(s.input + s.cache_write)} in{s.cost_usd !== null ? ` · ${formatUSD(s.cost_usd)}` : ''}</span>
                    </li>
                  ))}
                </ul>
              </>
            )}
          </>
        )}
      </section>}

      {section === 'overview' && <section>
        <h2>{workspace ? 'Channels' : 'Workspaces'}</h2>
        {workspace && <p className="insights-note">Started in this range: Main {d.sessions.main} · Channels {d.sessions.channels} · Workers {d.sessions.workers}.</p>}
        <div className="two">
          <div>
            <h3>By runtime</h3>
            <ul className="plain">{d.sessions.by_runtime.map((r) => <li key={r.key}>{r.key} <span className="muted">· {r.count}</span></li>)}{d.sessions.by_runtime.length === 0 && <li className="muted">none</li>}</ul>
          </div>
          <div>
            <h3>How they ended</h3>
            <ul className="plain">{d.sessions.by_outcome.map((r) => <li key={r.key}>{outcomeLabel(r.key)} <span className="muted">· {r.count}</span></li>)}{d.sessions.by_outcome.length === 0 && <li className="muted">none ended</li>}</ul>
          </div>
        </div>
        {d.sessions.lifetime_seconds.count > 0 && (
          <p className="insights-note">Lifetime of ended {workspace ? 'channels' : 'workspaces'}: median {formatDuration(d.sessions.lifetime_seconds.p50)}, 90th {formatDuration(d.sessions.lifetime_seconds.p90)}, longest {formatDuration(d.sessions.lifetime_seconds.max)}.</p>
        )}
        {d.sessions.running_seconds + d.sessions.idle_seconds + d.sessions.waiting_seconds > 0 && (
          <p className="insights-note">
            Time working {formatDuration(d.sessions.running_seconds)} · waiting on a person {formatDuration(d.sessions.waiting_seconds)} · idle {formatDuration(d.sessions.idle_seconds)}{' '}
            ({formatPercent(d.sessions.busy_fraction)} busy).
          </p>
        )}
      </section>}

      {(section === 'overview' || section === 'live') && d.cluster && (
        <section>
          <h2>Cluster</h2>
          <p className="insights-note">{d.cluster.active} of {d.cluster.max} workspace pods in use now; at most {d.cluster.peak_concurrent} at once in this range.</p>
        </section>
      )}

      {(section === 'overview' || section === 'live') && d.crons.runs > 0 && (
        <section>
          <h2>Crons</h2>
          <p className="insights-note">
            {d.crons.runs} runs ({d.crons.by_status.map((s) => `${s.key} ${s.count}`).join(', ')}){d.crons.late ? `, ${d.crons.late} late` : ''}
            {d.crons.seconds.count > 0 ? `. A run lasts ${formatDuration(d.crons.seconds.p50)} typically, ${formatDuration(d.crons.seconds.p90)} at the 90th percentile.` : '.'}
          </p>
        </section>
      )}

      {(section === 'overview' || section === 'usage') && !d.is_admin && <p className="insights-note">Dollar figures are estimates from per-model prices an administrator sets.</p>}
    </div>
  )
}

function Card({ id, label, value, hint, tooltip }: { id: string; label: string; value: string; hint?: string; tooltip?: string }) {
  return (
    <div className="card" data-testid={`card-${id}`}>
      <div className="card-label" title={label}>{label}</div>
      <div className="card-value" title={tooltip}>{value}</div>
      {hint && <div className="card-hint" title={hint}>{hint}</div>}
    </div>
  )
}

// Fig is one figure of a stacked entry: its name over its value.
function Fig({ k, v }: { k: string; v: React.ReactNode }) {
  return <div><dt>{k}</dt><dd>{v}</dd></div>
}

// DailyChart draws output + input tokens per day as bars (cache reads are far larger and would flatten them; they are in the table).
// On a phone the drawing is narrower, so its day labels stay readable once it is scaled to the screen.
function DailyChart({ days, narrow }: { days: InsightsResponse['tokens']['daily']; narrow: boolean }) {
  if (days.length === 0) return null
  const W = narrow ? 340 : 640, H = 140, pad = narrow ? 12 : 24
  const every = Math.ceil(days.length / (narrow ? 5 : 8))
  const vals = days.map((x) => x.output + x.input + x.cache_write)
  const max = Math.max(1, ...vals)
  const bw = Math.max(narrow ? 3 : 6, (W - pad * 2) / days.length - (narrow ? 3 : 6))
  return (
    <figure className="chart" data-testid="daily-chart">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`Tokens per day, ${days.length} days`} preserveAspectRatio="xMidYMid meet">
        {days.map((day, i) => {
          const h = Math.max(2, (vals[i] / max) * (H - pad * 2))
          const slot = (W - pad * 2) / days.length
          const x = pad + i * slot + (slot - bw) / 2
          return (
            <g key={day.day}>
              <rect x={x} y={H - pad - h} width={bw} height={h} rx={2} className="bar"><title>{`${shortDay(day.day)}: ${formatTokens(vals[i])} tokens (${formatTokens(day.output)} output)`}</title></rect>
              {((!narrow && days.length <= 10) || i % every === 0) && <text x={x + bw / 2} y={H - 6} textAnchor="middle" className="axis">{shortDay(day.day)}</text>}
            </g>
          )
        })}
      </svg>
      <figcaption>Tokens per day: output, input and cache writes (cache reads are in the table).</figcaption>
    </figure>
  )
}


