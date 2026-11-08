import {useScalarMotion} from './useScalarMotion'
import {dashboardChart} from '../visual/dashboardChart'
import type {TransportFiles} from '../transport/types'
import {useDashboardDataset} from './useDashboardDataset'
import {useMemo, useEffect, useLayoutEffect, useRef, useCallback, useState, type CSSProperties} from 'react'
import {parseDashboard, type DashboardCard} from '../visual/dashboard'
import VisualBlock from './VisualBlock'
import CodeBlock from './CodeBlock'
import {ViewerAction} from './ViewerToolbar'
import './dashboard.css'

function Card({card, fileContext, onCommitted}: {card: DashboardCard; fileContext?: {sessionId: string; files: TransportFiles}; onCommitted:(card:DashboardCard)=>void}) {
  const {ref, rows, status, observed, nextAttempt} = useDashboardDataset(card, fileContext)
  const chartSource=useMemo(()=>card.kind==='chart'?dashboardChart(card.spec!,rows):'', [card,rows])
  const committedSource=useRef<string | undefined>(undefined)
  const current=useRef({card,chartSource,onCommitted})
  useLayoutEffect(()=>{current.current={card,chartSource,onCommitted};if(card.kind!=='chart' || committedSource.current===chartSource)onCommitted(card)},[card,chartSource,onCommitted])
  const rendered=useCallback((visual:unknown,source?:string)=>{if(visual)queueMicrotask(()=>{if(source===current.current.chartSource){committedSource.current=source;current.current.onCommitted(current.current.card)}})},[])
  const v = card.view
  const row = rows.at(v?.selection === 'latest' ? -1 : 0)
  const value = v && row?.[v.value]
  const market = card.data.kind === 'dataset' && card.data.name === 'blerg.mcp' && row && ('market_status' in row || 'entitlement' in row)
  const marketLabel = row?.market_status === 'closed' ? 'Market closed' : row?.market_status === 'halted' ? 'Trading halted' : row?.market_status === 'open' ? 'Market open' : 'Market status unknown'
  const entitlementLabel = row?.entitlement === 'delayed' ? 'Delayed entitlement' : row?.entitlement === 'real-time' ? 'Real-time entitlement' : 'Entitlement unknown'
  const number = typeof value === 'number' ? value : null
  const numberRef = useScalarMotion(number)
  const shown = number === null ? 'Unknown' : new Intl.NumberFormat('en-US', {maximumFractionDigits: 2}).format(number)
  const points = v?.sparkline ? rows.map(r => r[v.sparkline!]) : []
  const numbers = points.filter((n): n is number => typeof n === 'number')
  const low = Math.min(...numbers), high = Math.max(...numbers)
  const ratio = (n: number) => Math.max(0, Math.min(1, (n - v!.min!) / (v!.max! - v!.min!)))
  return <article ref={ref} className="dashboard-card" data-kind={card.kind} style={{'--chat-dashboard-span': card.span} as CSSProperties} aria-label={card.title}>
    <h3>{card.title}</h3>
    {card.kind === 'chart' ? <div className="dashboard-chart"><VisualBlock kind="vega-lite" source={chartSource} onRendered={rendered} hostedTab stretch dashboardGuides /></div>
      : card.kind === 'text' ? <p className="dashboard-text">{typeof value === 'string' ? value : 'Unknown'}</p>
      : <>
        <p className="dashboard-value" aria-label={shown}><span ref={numberRef} className="dashboard-number" />{v?.unit && <span className="dashboard-unit"> {v.unit}</span>}</p>
        {v?.delta && <p className="dashboard-detail">{row?.[v.delta] == null ? 'Comparison unavailable' : String(row[v.delta])}</p>}
        {card.kind === 'gauge' && <>
          <div className="dashboard-meter" role="meter" aria-label={card.title} aria-valuemin={v!.min} aria-valuemax={v!.max} aria-valuenow={number === null ? undefined : Math.max(v!.min!, Math.min(v!.max!, number))} aria-valuetext={`${shown} ${v?.unit ?? ''}`}>
            <span className="dashboard-meter-fill" style={{width: `${number === null ? 0 : ratio(number) * 100}%`, visibility: number === null ? 'hidden' : 'visible'}} />
            {v!.thresholds?.map(n => <i key={n} style={{left: `${ratio(n) * 100}%`}} title={`${n} ${v?.unit ?? ''}`} />)}
          </div>
          <p className="dashboard-detail dashboard-range"><span>{v!.min} {v?.unit}</span><span>{v!.max} {v?.unit}</span></p>
          {typeof row?.reset_at === 'string' && !row.reset_at.startsWith('0001') && Number.isFinite(Date.parse(row.reset_at)) && <p className="dashboard-detail">Resets {new Date(row.reset_at).toLocaleString('en-US')}</p>}
          {!!v!.thresholds?.length && <p className="dashboard-detail">Thresholds: {v!.thresholds.join(' / ')} {v?.unit}</p>}
          {number !== null && (number < v!.min! || number > v!.max!) && <p className="dashboard-detail">Outside declared range</p>}
        </>}
        {numbers.length > 0 && <svg className="dashboard-sparkline" viewBox="0 0 300 48" preserveAspectRatio="none" role="img" aria-label={`${card.title} trend: ${numbers.length} observations`}>
          {points.map((n, i) => typeof n === 'number' && <circle key={i} cx={points.length === 1 ? 150 : i * 296 / (points.length - 1) + 2} cy={46 - (n - low) / (high - low || 1) * 44} r="2" />)}
          {points.map((n, i) => i > 0 && typeof n === 'number' && typeof points[i - 1] === 'number' && <line key={i} x1={(i - 1) * 296 / (points.length - 1) + 2} x2={i * 296 / (points.length - 1) + 2} y1={46 - (Number(points[i - 1]) - low) / (high - low || 1) * 44} y2={46 - (n - low) / (high - low || 1) * 44} />)}
        </svg>}
      </>}
    {market && <p className="dashboard-detail">{marketLabel} · {entitlementLabel}</p>}
    {status && <p className="dashboard-detail" role="status">{status}{observed && <> · <time dateTime={observed} title={observed}>{new Date(observed).toLocaleTimeString('en-US')}</time></>}{nextAttempt && <> · Retry <time dateTime={nextAttempt} title={nextAttempt}>{new Date(nextAttempt).toLocaleTimeString('en-US')}</time></>}</p>}
  </article>
}
export default function DashboardBlock({source, fileContext}: {source: string; fileContext?: {sessionId: string; files: TransportFiles}}) {
  const [showSource, setShowSource] = useState(false)
  const root=useRef<HTMLElement>(null)
  const [committed,setCommitted]=useState<Map<string,DashboardCard>>(()=>new Map())
  const cardCommitted=useCallback((card:DashboardCard)=>setCommitted(previous=>previous.get(card.id)===card?previous:new Map(previous).set(card.id,card)),[])
  const files = fileContext?.files, sessionId = fileContext?.sessionId
  const result = useMemo(() => {
    try { return {doc: parseDashboard(source)} } catch { return {error: 'Dashboard refused: invalid document, unsupported version, or disallowed settings.'} }
  }, [source])
  const [lastGood, setLastGood] = useState(result.doc)
  if (result.doc && result.doc !== lastGood) setLastGood(result.doc)
  const dashboard = result.doc ?? lastGood
  useEffect(()=>{
    if(result.doc && result.doc.cards.every(card=>committed.get(card.id)===card))root.current?.dispatchEvent(new CustomEvent('blerg-dashboard-committed',{bubbles:true,detail:{source}}))
  },[result,committed,source])
  useEffect(() => {
    if (!result.error) return
    console.warn('dashboard_refused', {cause: 'invalid_document'})
    if (sessionId) files?.reportVisualFailure?.(sessionId, 'invalid_document')
  }, [result, files, sessionId])
  return <section ref={root} className="dashboard-block" aria-label="Dashboard">
    <div className="dashboard-toolbar"><ViewerAction icon="raw" label={showSource ? 'Show dashboard' : 'Show source'} pressed={showSource} onClick={() => setShowSource(x => !x)} /></div>
    {result.error && <p role="status">{result.error}</p>}
    {showSource ? <CodeBlock closed>{source}</CodeBlock> : dashboard && <>
      <h2 className="dashboard-title">{dashboard.title}</h2>
      <div className="dashboard-grid">{dashboard.cards.map(card => <Card key={card.id} card={card} fileContext={fileContext} onCommitted={cardCommitted} />)}</div>
    </>}
  </section>
}
