import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { apiFetch } from '../apiFetch'
import {
  RANGES, formatDuration, formatPercent, formatTokens, formatUSD, outcomeLabel, shortDay, stepLabel,
  type InsightsResponse, type ModelPrice, type RangeLabel,
} from '../lib/insights'
import './Insights.css'

type Scope = 'all' | 'mine'

export default function Insights() {
  const [range, setRange] = useState<RangeLabel>('7d')
  const [scope, setScope] = useState<Scope>('all')
  const [data, setData] = useState<InsightsResponse | null>(null)
  const [prices, setPrices] = useState<ModelPrice[]>([])
  // what the last answer was for: loading is "no answer yet for the request now wanted"
  const [answered, setAnswered] = useState<{ key: string; failed: boolean } | null>(null)
  const [reloadKey, setReloadKey] = useState(0)
  const reload = useCallback(() => setReloadKey((k) => k + 1), [])
  const key = `${range}|${scope}|${reloadKey}`
  const loading = answered?.key !== key
  const error = !loading && answered?.failed === true

  useEffect(() => {
    let cancelled = false
    const qs = `range=${range}${scope === 'mine' ? '&scope=mine' : ''}`
    apiFetch(`/api/insights?${qs}`)
      .then((r) => (r.ok ? (r.json() as Promise<InsightsResponse>) : Promise.reject(new Error(String(r.status)))))
      .then((d) => { if (!cancelled) { setData(d); setAnswered({ key, failed: false }) } })
      .catch(() => { if (!cancelled) setAnswered({ key, failed: true }) })
    return () => { cancelled = true }
  }, [range, scope, key])

  const isAdmin = data?.is_admin === true
  useEffect(() => {
    if (!isAdmin) return
    let cancelled = false
    apiFetch('/api/insights/prices')
      .then((r) => (r.ok ? (r.json() as Promise<ModelPrice[]>) : []))
      .then((p) => { if (!cancelled) setPrices(Array.isArray(p) ? p : []) })
      .catch(() => { /* the editor just shows no stored prices */ })
    return () => { cancelled = true }
  }, [isAdmin, reloadKey])

  return (
    <div className="insights">
      <header className="insights-head">
        <div>
          <h1>Insights</h1>
          <p className="insights-sub">
            {data
              ? data.scope === 'all'
                ? 'Every session on this install.'
                : 'Showing your own sessions.'
              : 'Pod startup, sessions, tokens and estimated cost.'}
          </p>
        </div>
        <div className="insights-controls">
          <div className="seg" role="group" aria-label="Time range">
            {RANGES.map((r) => (
              <button key={r} type="button" aria-pressed={range === r} className={range === r ? 'on' : ''} onClick={() => setRange(r)}>{r}</button>
            ))}
          </div>
          {isAdmin && (
            <div className="seg" role="group" aria-label="Whose sessions">
              <button type="button" aria-pressed={scope === 'all'} className={scope === 'all' ? 'on' : ''} onClick={() => setScope('all')}>Everyone</button>
              <button type="button" aria-pressed={scope === 'mine'} className={scope === 'mine' ? 'on' : ''} onClick={() => setScope('mine')}>Mine</button>
            </div>
          )}
        </div>
      </header>

      {loading && !data && <div role="status" className="insights-note">Loading insights…</div>}
      {error && (
        <div role="alert" className="insights-note insights-error">
          Could not load insights.{' '}
          <button type="button" onClick={reload}>Try again</button>
        </div>
      )}
      {data && !error && (
        <div aria-busy={loading} style={{ opacity: loading ? 0.55 : 1, transition: 'opacity 120ms' }}>
          {loading && <div role="status" className="insights-note">Updating…</div>}
          <Report d={data} prices={prices} onPriceChange={reload} />
        </div>
      )}
    </div>
  )
}

function Report({ d, prices, onPriceChange }: { d: InsightsResponse; prices: ModelPrice[]; onPriceChange: () => void }) {
  const empty = d.sessions.started === 0 && d.tokens.totals.turns === 0
  // the headline startup figure: the runtime that started the most sessions
  const headline = useMemo(() => {
    const ok = d.startup.groups.filter((g) => g.kind === 'start' && g.seconds.count > 0)
    return ok.sort((a, b) => b.attempts - a.attempts)[0] ?? d.startup.groups.find((g) => g.seconds.count > 0)
  }, [d])
  const stepRuntime = headline?.runtime
  const steps = d.startup.steps.filter((s) => s.runtime === stepRuntime)
  const maxStep = Math.max(1, ...steps.map((s) => s.p90))

  return (
    <>
      <div className="cards">
        <Card id="started" label="Sessions started" value={String(d.sessions.started)} hint={`${d.sessions.alive} alive now`} />
        <Card id="startup" label={headline ? `Median startup (${headline.runtime})` : 'Median startup'} value={headline ? formatDuration(headline.seconds.p50) : '—'}
          hint={headline ? `90% under ${formatDuration(headline.seconds.p90)}` : 'no completed starts'} />
        <Card id="tokens" label="Output tokens" value={formatTokens(d.tokens.totals.output)} hint={`${formatTokens(d.tokens.totals.input)} input · ${d.tokens.totals.turns} turns`} />
        <Card id="cost" label="Estimated cost" value={formatUSD(d.tokens.totals.cost_usd)}
          hint={d.tokens.unpriced_models.length > 0 ? `no price for ${d.tokens.unpriced_models.join(', ')}` : 'at the prices set below'} />
        <Card id="cache" label="Cache hit rate" value={d.tokens.totals.turns ? formatPercent(d.tokens.cache_hit_rate) : '—'} hint={`${formatTokens(d.tokens.totals.cache_read)} read from cache`} />
      </div>

      {empty && <div className="insights-note">No sessions in this range.</div>}

      <section>
        <h2>Pod startup</h2>
        {d.startup.groups.length === 0 ? <p className="insights-note">No starts recorded in this range.</p> : (
          <>
            <table className="grid">
              <thead><tr><th>Runtime</th><th>Kind</th><th className="n">Attempts</th><th className="n">Failed</th><th className="n">Median</th><th className="n">90th</th><th className="n">Slowest</th></tr></thead>
              <tbody>
                {d.startup.groups.map((g) => (
                  <tr key={g.runtime + g.kind}>
                    <td>{g.runtime}</td><td>{g.kind}</td><td className="n">{g.attempts}</td><td className="n">{g.failed}</td>
                    <td className="n">{g.seconds.count ? formatDuration(g.seconds.p50) : '—'}</td>
                    <td className="n">{g.seconds.count ? formatDuration(g.seconds.p90) : '—'}</td>
                    <td className="n">{g.seconds.count ? formatDuration(g.seconds.max) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {steps.length > 0 && (
              <>
                <h3>Where the time goes ({stepRuntime})</h3>
                <ul className="steps" data-testid="start-steps">
                  {steps.map((s) => (
                    <li key={s.step}>
                      <span className="step-name">{stepLabel(s.step)}</span>
                      <span className="step-bar" aria-hidden="true"><i style={{ width: `${Math.max(2, (s.p90 / maxStep) * 100)}%` }} /><b style={{ width: `${Math.max(1, (s.p50 / maxStep) * 100)}%` }} /></span>
                      <span className="step-val">{formatDuration(s.p50)}<small> median · {formatDuration(s.p90)} 90th</small></span>
                    </li>
                  ))}
                </ul>
              </>
            )}
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
      </section>

      <section>
        <h2>Tokens and cost</h2>
        {d.tokens.by_model.length === 0 ? <p className="insights-note">No turns completed in this range.</p> : (
          <>
            <table className="grid">
              <thead><tr><th>Model</th><th className="n">Turns</th><th className="n">Input</th><th className="n">Output</th><th className="n">Cache read</th><th className="n">Cache write</th><th className="n">Est. cost</th></tr></thead>
              <tbody>
                {d.tokens.by_model.map((m) => (
                  <tr key={m.model}>
                    <td>{m.model}</td><td className="n">{m.turns}</td><td className="n">{formatTokens(m.input)}</td><td className="n">{formatTokens(m.output)}</td>
                    <td className="n">{formatTokens(m.cache_read)}</td><td className="n">{formatTokens(m.cache_write)}</td>
                    <td className="n">{m.priced ? formatUSD(m.cost_usd) : <span className="muted" title="No price set for this model">no price</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <DailyChart days={d.tokens.daily} />
            {d.tokens.top_sessions.length > 0 && (
              <>
                <h3>Biggest sessions</h3>
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
      </section>

      <section>
        <h2>Sessions</h2>
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
          <p className="insights-note">Lifetime of ended sessions: median {formatDuration(d.sessions.lifetime_seconds.p50)}, 90th {formatDuration(d.sessions.lifetime_seconds.p90)}, longest {formatDuration(d.sessions.lifetime_seconds.max)}.</p>
        )}
        {d.sessions.running_seconds + d.sessions.idle_seconds + d.sessions.waiting_seconds > 0 && (
          <p className="insights-note">
            Time working {formatDuration(d.sessions.running_seconds)} · waiting on a person {formatDuration(d.sessions.waiting_seconds)} · idle {formatDuration(d.sessions.idle_seconds)}{' '}
            ({formatPercent(d.sessions.busy_fraction)} busy).
          </p>
        )}
      </section>

      {d.cluster && (
        <section>
          <h2>Cluster</h2>
          <p className="insights-note">{d.cluster.active} of {d.cluster.max} session pods in use now; at most {d.cluster.peak_concurrent} at once in this range.</p>
        </section>
      )}

      {d.crons.runs > 0 && (
        <section>
          <h2>Crons</h2>
          <p className="insights-note">
            {d.crons.runs} runs ({d.crons.by_status.map((s) => `${s.key} ${s.count}`).join(', ')}){d.crons.late ? `, ${d.crons.late} late` : ''}
            {d.crons.seconds.count > 0 ? `. A run lasts ${formatDuration(d.crons.seconds.p50)} typically, ${formatDuration(d.crons.seconds.p90)} at the 90th percentile.` : '.'}
          </p>
        </section>
      )}

      {d.is_admin && <PriceEditor stored={prices} unpriced={d.tokens.unpriced_models} onChange={onPriceChange} />}
      {!d.is_admin && <p className="insights-note">Dollar figures are estimates from per-model prices an administrator sets.</p>}
    </>
  )
}

function Card({ id, label, value, hint }: { id: string; label: string; value: string; hint?: string }) {
  return (
    <div className="card" data-testid={`card-${id}`}>
      <div className="card-label">{label}</div>
      <div className="card-value">{value}</div>
      {hint && <div className="card-hint">{hint}</div>}
    </div>
  )
}

// DailyChart draws output + input tokens per day as bars (cache reads are far larger and would flatten them; they are in the table).
function DailyChart({ days }: { days: InsightsResponse['tokens']['daily'] }) {
  if (days.length === 0) return null
  const W = 640, H = 140, pad = 24
  const vals = days.map((x) => x.output + x.input + x.cache_write)
  const max = Math.max(1, ...vals)
  const bw = Math.max(6, (W - pad * 2) / days.length - 6)
  return (
    <figure className="chart" data-testid="daily-chart">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`Tokens per day, ${days.length} days`} preserveAspectRatio="xMidYMid meet">
        {days.map((day, i) => {
          const h = Math.max(2, (vals[i] / max) * (H - pad * 2))
          const x = pad + i * ((W - pad * 2) / days.length) + 3
          return (
            <g key={day.day}>
              <rect x={x} y={H - pad - h} width={bw} height={h} rx={2} className="bar"><title>{`${shortDay(day.day)}: ${formatTokens(vals[i])} tokens (${formatTokens(day.output)} output)`}</title></rect>
              {(days.length <= 10 || i % Math.ceil(days.length / 8) === 0) && <text x={x + bw / 2} y={H - 6} textAnchor="middle" className="axis">{shortDay(day.day)}</text>}
            </g>
          )
        })}
      </svg>
      <figcaption>Tokens per day: output, input and cache writes (cache reads are in the table).</figcaption>
    </figure>
  )
}

interface Draft { input: string; output: string; cacheRead: string; cacheWrite: string }

function PriceEditor({ stored, unpriced, onChange }: { stored: ModelPrice[]; unpriced: string[]; onChange: () => void }) {
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const draftOf = (m: string): Draft => drafts[m] ?? { input: '', output: '', cacheRead: '', cacheWrite: '' }
  const set = (m: string, patch: Partial<Draft>) => setDrafts((d) => ({ ...d, [m]: { ...draftOf(m), ...patch } }))
  const models = [...stored.map((p) => p.model), ...unpriced.filter((m) => !stored.some((p) => p.model === m) && m !== 'unknown')]

  async function save(model: string) {
    const dr = draftOf(model)
    const cur = stored.find((p) => p.model === model)
    const pick = (typed: string, fallback: number | undefined) => (typed.trim() === '' ? fallback : Number(typed))
    const vals = {
      input: pick(dr.input, cur?.input_per_mtok),
      output: pick(dr.output, cur?.output_per_mtok),
      cacheRead: pick(dr.cacheRead, cur?.cache_read_per_mtok ?? 0),
      cacheWrite: pick(dr.cacheWrite, cur?.cache_write_per_mtok ?? 0),
    }
    const all = Object.values(vals)
    if (all.some((v) => v === undefined || !Number.isFinite(v) || v < 0)) {
      setError('Prices are dollars per million tokens, zero or more. Enter at least the input and output price.')
      return
    }
    setError(null)
    setBusy(true)
    try {
      const r = await apiFetch('/api/insights/prices', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          model, input_per_mtok: vals.input, output_per_mtok: vals.output,
          cache_read_per_mtok: vals.cacheRead, cache_write_per_mtok: vals.cacheWrite,
        }),
      })
      if (!r.ok) setError('Could not save the price.')
      else { setDrafts((d) => { const n = { ...d }; delete n[model]; return n }); onChange() }
    } catch {
      setError('Could not save the price.')
    } finally {
      setBusy(false)
    }
  }

  async function remove(model: string) {
    setBusy(true)
    try {
      const r = await apiFetch(`/api/insights/prices?model=${encodeURIComponent(model)}`, { method: 'DELETE' })
      if (!r.ok) setError('Could not remove the price.')
      else onChange()
    } catch {
      setError('Could not remove the price.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <section data-testid="price-editor">
      <h2>Prices for the cost estimate</h2>
      <p className="insights-note">Dollars per million tokens, per model. Only an administrator sets these; the estimates everywhere use them. Models with no price show no cost.</p>
      {error && <div role="alert" className="insights-error">{error}</div>}
      {models.length === 0 ? <p className="insights-note">Every model in this range has a price.</p> : (
        <table className="grid prices">
          <thead><tr><th>Model</th><th className="n">Input</th><th className="n">Output</th><th className="n">Cache read</th><th className="n">Cache write</th><th /></tr></thead>
          <tbody>
            {models.map((m) => {
              const cur = stored.find((p) => p.model === m)
              const dr = draftOf(m)
              const field = (key: keyof Draft, label: string, current: number | undefined) => (
                <td className="n">
                  <input type="number" min="0" step="any" inputMode="decimal" aria-label={`${label} price per million tokens, ${m}`}
                    placeholder={current !== undefined ? String(current) : ''} value={dr[key]} onChange={(e) => set(m, { [key]: e.target.value })} />
                </td>
              )
              return (
                <tr key={m}>
                  <td>{m}</td>
                  {field('input', 'Input', cur?.input_per_mtok)}
                  {field('output', 'Output', cur?.output_per_mtok)}
                  {field('cacheRead', 'Cache read', cur?.cache_read_per_mtok)}
                  {field('cacheWrite', 'Cache write', cur?.cache_write_per_mtok)}
                  <td className="actions">
                    <button type="button" disabled={busy} aria-label={`Save ${m}`} onClick={() => void save(m)}>Save</button>
                    {cur && <button type="button" disabled={busy} className="danger" aria-label={`Remove ${m}`} onClick={() => void remove(m)}>Remove</button>}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
    </section>
  )
}
