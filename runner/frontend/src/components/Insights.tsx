import { signedInAccount } from '../lib/liveBoardCard'
import { QUESTIONS, type QuestionView } from '../lib/insightsQuestions'
import { createPortal } from 'react-dom'
import { cachedInsights, clearInsightsCache, insightsCacheKey, rememberInsights } from '../lib/insightsCache'
import InsightsTable from './InsightsTable'
import { useInsightsPreference } from '../hooks/useInsightsPreferences'
import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { useLocation, useSearchParams } from 'react-router-dom'
import ActivityInsights from './ActivityInsights'
import InsightsDetails from './InsightsDetails'
import { apiFetch } from '../apiFetch'
import { mark } from '../lib/releasePerfMarks'
import {
  RANGES,
  type InsightsResponse, type ModelPrice, type RangeLabel,
} from '../lib/insights'
import './Insights.css'
import {openTestPerformance} from '../lib/testPerformance'


// At this width and under, the wide tables become one stacked entry per row (Insights.css uses the same width).
export const NARROW_QUERY = '(max-width: 640px)'

export type InsightsSection = 'overview' | 'flow' | 'reviews' | 'usage' | 'live'

const subscribeVisibility = (notify: () => void) => { document.addEventListener('visibilitychange', notify); return () => document.removeEventListener('visibilitychange', notify) }

export function InsightsDashboard({ active = true, workspaceId }: { active?: boolean; workspaceId?: string }) {
  const pageVisible = useSyncExternalStore(subscribeVisibility, () => document.visibilityState !== 'hidden')
  const location = useLocation()
  const [search] = useSearchParams()
  const workspace = workspaceId ?? (location.pathname.startsWith('/insights') ? search.get('workspace') : null)
  const candidate = location.pathname.split('/')[2] || 'health'
  const [section, setSection] = useState<QuestionView>(candidate === 'flow' ? 'cost' : candidate === 'usage' ? 'spending' : candidate === 'skills' || candidate === 'time' || candidate === 'cost' || candidate === 'spending' ? candidate : 'health')
  const [identity, setIdentity] = useState(0)
  useEffect(() => { const changed = () => setIdentity(n => n + 1); window.addEventListener('blerg-identity-changed', changed); return () => window.removeEventListener('blerg-identity-changed', changed) }, [])
  const contentRef = useRef<HTMLDivElement>(null)
  const [toolbar, setToolbar] = useState<HTMLDivElement | null>(null)
  useEffect(() => { if (contentRef.current) contentRef.current.scrollTop = 0 }, [section, workspace])
  return <div className="insights-shell insights-tab" ref={contentRef}>
    <div className="insights-toolbar"><select className="insights-select insights-mobile-view" aria-label="Insights view" value={section} onChange={e=>setSection(e.target.value as QuestionView)}>{QUESTIONS.map(q=><option key={q.id} value={q.id}>{q.label}</option>)}</select><div className="insights-section-tabs seg" role="group" aria-label="Insights sections">
      {QUESTIONS.map(({id, label, question}) => <button type="button" key={id} title={question} className={section === id ? 'on' : ''} aria-pressed={section === id} onClick={() => setSection(id)}>{label}</button>)}
    </div>
    <button className="btn small" title="Open test performance in its own tab" onClick={()=>openTestPerformance(workspace || undefined)}>Test performance</button><div className="insights-controls" ref={setToolbar} /></div>
    <GlobalInsights enabled={active && pageVisible} toolbar={toolbar} key={`${identity}|${workspace || 'own'}`} workspace={workspace || undefined} section={section} />
  </div>
}

function GlobalInsights({ section, workspace, enabled, toolbar }: { enabled: boolean; toolbar: HTMLElement | null; workspace?: string; section: QuestionView }) {
  const [range, setRange] = useInsightsPreference<RangeLabel>('range', '7d')
  const cacheKey = insightsCacheKey(signedInAccount(), `summary|${workspace || ''}|${range}`)
  const [response, setResponse] = useState<{key:string;data:InsightsResponse|null}>(() => ({key:cacheKey,data:cachedInsights(cacheKey)}))
  const data=response.key===cacheKey?response.data:cachedInsights<InsightsResponse>(cacheKey)
  // BLG-453: a no-op everywhere except the release-perf harness (see
  // src/lib/releasePerfMarks.ts) — the readiness signal the 7-day Insights
  // scenario times against.
  useEffect(() => { if (data) mark('insights-rendered') }, [data])
  const [filterTarget, setFilterTarget] = useState<HTMLDivElement | null>(null)
  const [prices,setPrices]=useState<ModelPrice[]>([])
  const [pricesOpen,setPricesOpen]=useState(false)
  // what the last answer was for: loading is "no answer yet for the request now wanted"
  const [answered, setAnswered] = useState<{ key: string; failed: boolean } | null>(null)
  const [reloadKey, setReloadKey] = useState(0)
  const reload = useCallback(() => setReloadKey((k) => k + 1), [])
  const effectiveScope = 'mine'
  const key = `${range}|${effectiveScope}|${reloadKey}`
  const loading = answered?.key !== key
  const error = !loading && answered?.failed === true

  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    const qs = `range=${range}&scope=mine${workspace ? `&workspace=${encodeURIComponent(workspace)}` : ''}`
    apiFetch(`/api/insights?${qs}`)
      .then((r) => (r.ok ? (r.json() as Promise<InsightsResponse>) : Promise.reject({status:r.status})))
      .then((d) => { if (!cancelled) { rememberInsights(cacheKey, d); setResponse({key:cacheKey,data:d}); setAnswered({ key, failed: false }) } })
      .catch((e: {status?:number}) => { if (!cancelled) { if(e.status && [401,403,404].includes(e.status)){ clearInsightsCache(); setResponse({key:cacheKey,data:null}) }; setAnswered({ key, failed: true }) } })
    return () => { cancelled = true }
  }, [range, effectiveScope, key, workspace, enabled, cacheKey])

  useEffect(() => {
    if (!enabled) return
    const timer = setInterval(() => { if (document.visibilityState === 'visible') reload() }, 30_000)
    return () => clearInterval(timer)
  }, [section, reload, enabled])

  useEffect(()=>{
    if(!enabled||!pricesOpen||section!=='spending'||!data?.is_admin)return
    let cancelled=false
    apiFetch('/api/insights/prices').then(r=>r.ok?r.json():[]).then((rows:ModelPrice[])=>{if(!cancelled)setPrices(rows)}).catch(()=>{if(!cancelled)setPrices([])})
    return()=>{cancelled=true}
  },[enabled,pricesOpen,section,data?.is_admin,reloadKey])

  return (
    <div className="insights">
      {toolbar && createPortal(<>
        <select className="insights-select insights-mobile-range" aria-label="Time range" value={range} onChange={e=>setRange(e.target.value as RangeLabel)}>{RANGES.map(r=><option key={r}>{r}</option>)}</select><div className="seg insights-range-buttons" role="group" aria-label="Time range">
          {RANGES.map(r => <button key={r} type="button" aria-pressed={range === r} className={range === r ? 'on' : ''} onClick={() => setRange(r)}>{r}</button>)}
        </div>
        <div className="insights-controls" ref={setFilterTarget} />
        {data && <span className="insights-updated" title={workspace ? undefined : 'Showing your own workspaces.'}>Updated {new Date(data.generated_at).toLocaleTimeString()}</span>}
      </>, toolbar)}

      {error && !data && (
        <div role="alert" className="insights-note insights-error">
          Could not load insights.{' '}
          <button type="button" className="btn small" onClick={reload}>Try again</button>
        </div>
      )}
      {(!error||data)&&<ActivityInsights questionView={section} summary={data??undefined} workspace={workspace} filterTarget={filterTarget} enabled={enabled} section={section==='health'?'reviews':'flow'} range={range} detailsContent={data&&(section==='health'||section==='spending'||section==='time')?<InsightsDetails d={data} section={section==='spending'?'usage':section==='time'?'live':'overview'} workspace={workspace}/>:undefined} />}
      {data && <div>
          {section==='spending'&&data.is_admin&&<details className="insights-details" onToggle={e=>setPricesOpen(e.currentTarget.open)}><summary>Model prices</summary>{pricesOpen&&<PriceEditor stored={prices} unpriced={data.tokens.unpriced_models} onChange={reload}/>}</details>}
        </div>}
    </div>
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
        <div className="scroll-x" data-testid="prices-scroll">
        <InsightsTable className="grid prices">
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
                    <button type="button" className="btn small" disabled={busy} aria-label={`Save ${m}`} onClick={() => void save(m)}>Save</button>
                    {cur && <button type="button" disabled={busy} className="btn small danger" aria-label={`Remove ${m}`} onClick={() => void remove(m)}>Remove</button>}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </InsightsTable>
        </div>
      )}
    </section>
  )
}
