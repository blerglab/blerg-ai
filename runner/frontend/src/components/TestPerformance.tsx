import {useEffect,useMemo,useRef,useState,useSyncExternalStore,type CSSProperties} from 'react'
import {VisualBlock} from '@blerglab/chat'
import {signedInAccount} from '../lib/liveBoardCard'
import {apiFetch} from '../apiFetch'
import {performanceOverview,suiteFindings,suiteTrend,type PerformanceResponse,type PerformanceProject} from '../lib/testPerformance'
import './Insights.css'
import './TestPerformance.css'
import '../../../../packages/settings/InitialLoader.css'

const subscribeIdentity=(notify:()=>void)=>{window.addEventListener('blerg-identity-changed',notify);return()=>window.removeEventListener('blerg-identity-changed',notify)}

export default function TestPerformance({workspaceId,active=true}:{workspaceId?:string;active?:boolean}) {
 const [response,setResponse]=useState<{key:string;data:PerformanceResponse;at:number}|null>(null)
 const [error,setError]=useState(false)
 const [revision,setRevision]=useState(0)
 const [project,setProject]=useState('')
 const [suite,setSuite]=useState('')
 const [days,setDays]=useState('30')
 const identity=useSyncExternalStore(subscribeIdentity,signedInAccount)
 const baseKey=`${identity}|${workspaceId??''}`
 const [inventory,setInventory]=useState<{key:string;projects:PerformanceProject[];truncated:boolean}|null>(null)
 const projectOptions=inventory?.key===baseKey?inventory.projects:[]
 const projects=projectOptions.map(p=>`${p.workspace_id}/${p.member_id}`)
 const selectedProject=projects.includes(project)?project:''
 const key=`${baseKey}|${selectedProject}`
 const [cursorRequest,setCursorRequest]=useState<{key:string;cursor:string}|null>(null)
 const cursor=cursorRequest?.key===key?cursorRequest.cursor:''
 const root=useRef<HTMLElement>(null)
 const [wide,setWide]=useState(false)
 const [detailsOverride,setDetailsOverride]=useState<boolean|null>(null)
 const detailsOpen=detailsOverride??wide
 useEffect(()=>{const node=root.current;if(!node)return;const resized=()=>setWide(node.clientWidth>=1000);const observer=new ResizeObserver(resized);observer.observe(node);return()=>observer.disconnect()},[])
 const [meterEntrances]=useState(()=>new Set<string>())
 const [page,setPage]=useState(0)
 const data=response?.key===key?response.data:null
 const loadingMore=!!cursor&&data?.next_cursor===cursor
 useEffect(()=>{
  if(!active)return
  let cancelled=false
  const qs=new URLSearchParams()
  if(selectedProject){const slash=selectedProject.indexOf('/');qs.set('workspace',selectedProject.slice(0,slash));qs.set('member',selectedProject.slice(slash+1))}else if(workspaceId)qs.set('workspace',workspaceId)
  if(cursor)qs.set('cursor',cursor)
  apiFetch(`/api/test-performance${qs.size?`?${qs}`:''}`)
   .then(r=>{if(!r.ok)throw {status:r.status};return r.json() as Promise<PerformanceResponse>})
   .then(data=>{if(!cancelled){
    if(!selectedProject&&!cursor)setInventory({key:baseKey,truncated:!!data.projects_truncated,projects:data.projects??[...new Map(data.runs.map(r=>[`${r.workspace_id}/${r.member_id}`,{workspace_id:r.workspace_id,member_id:r.member_id}])).values()]})
    setResponse(old=>({key,data:{...data,runs:cursor&&old?.key===key?[...new Map([...old.data.runs,...data.runs].map(r=>[`${r.workspace_id}/${r.member_id}/${r.run_id}`,r])).values()]:data.runs},at:Date.now()}));setError(false)
   }})
   .catch((error:unknown)=>{if(!cancelled){console.warn('Test history request failed',{cause:typeof error==='object'&&error!==null&&'status' in error&&typeof error.status==='number'?`http_${error.status}`:'transport'});if(typeof error==='object'&&error!==null&&'status' in error&&[401,403,404].includes(Number(error.status)))setInventory(null);setResponse(null);setError(true)}})
  return()=>{cancelled=true}
 },[workspaceId,active,revision,key,baseKey,selectedProject,cursor])
 const runs=useMemo(()=>data?.runs.filter(r=>(!selectedProject||`${r.workspace_id}/${r.member_id}`===selectedProject)&&Date.parse(r.finished_at)>=(response?.at??0)-Number(days)*86400000)??[],[data,selectedProject,days,response?.at])
 const suites=[...new Set(runs.flatMap(r=>r.suites.map(s=>s.suite)))].sort()
 const selectedSuite=suites.includes(suite)?suite:''
 const points=useMemo(()=>suiteTrend(runs,selectedSuite),[runs,selectedSuite])
 const overview=useMemo(()=>performanceOverview(runs,selectedSuite),[runs,selectedSuite])
 const findings=useMemo(()=>suiteFindings(runs,selectedSuite),[runs,selectedSuite])
 const observed=runs.reduce((count,r)=>count+r.suites.filter(s=>!selectedSuite||s.suite===selectedSuite).length,0)
 const detailPage=Math.min(page,Math.max(0,Math.ceil(runs.length/20)-1))
 const source=useMemo(()=>JSON.stringify({data:{values:points},mark:'line',encoding:{x:{field:'date',type:'temporal',title:'Run date'},y:{field:'seconds',type:'quantitative',title:'Suite duration (seconds)',scale:{zero:true}},color:{field:selectedProject||projects.length===1?'suite':'series',type:'nominal',title:'Suite',legend:{orient:'top'}},tooltip:[{field:'suite',type:'nominal'},{field:'date',type:'temporal',title:'Run date'},{field:'seconds',type:'quantitative',title:'Duration (seconds)'},{field:'commit',type:'nominal'},{field:'outcome',type:'nominal'},{field:'coverage',type:'nominal',title:'Coverage'}]}}),[points,selectedProject,projects.length])
 return <section ref={root} className="test-performance" aria-label="Test performance">
  <div className="test-performance-toolbar">
   {points.length?<h2 className="test-performance-finding">{overview.finding}</h2>:<p>Find where test time goes.</p>}
   <div className="test-performance-filters">
    <label>Project <select className="insights-select" aria-label="Project" value={selectedProject} onChange={e=>{setProject(e.target.value);setSuite('');setPage(0)}}><option value="">All projects</option>{projectOptions.map(p=><option key={`${p.workspace_id}/${p.member_id}`} value={`${p.workspace_id}/${p.member_id}`}>{p.name??p.member_id} · {p.workspace_id.slice(0,8)}</option>)}</select></label>
    <label>Suite <select className="insights-select" aria-label="Suite" value={selectedSuite} onChange={e=>setSuite(e.target.value)}><option value="">All suites</option>{suites.map(s=><option key={s}>{s}</option>)}</select></label>
    <label>Window <select className="insights-select" aria-label="Time range" value={days} onChange={e=>setDays(e.target.value)}>{['7','30','90','180'].map(d=><option key={d} value={d}>{d} days</option>)}</select></label>
    <button className="icon-btn" type="button" aria-label="Refresh test history" title="Refresh test history" onClick={()=>{setCursorRequest(null);setRevision(r=>r+1)}}>↻</button>
   </div>
  </div>
  {error?<div role="alert" className="insights-note">Could not read test history. <button className="btn small" onClick={()=>{setError(false);setCursorRequest(null);setRevision(r=>r+1)}}>Retry</button></div>:!data?<div className="test-performance-skeleton" aria-label="Fetching test history" aria-busy="true"><div className="test-performance-skeleton-overview" aria-hidden="true">{Array.from({length:4},(_,i)=><span className="initial-skeleton-value" key={i}/>)}</div><div className="test-performance-skeleton-chart" aria-hidden="true">{Array.from({length:6},(_,i)=><span className="initial-skeleton-value" key={i}/>)}</div></div>:<>
   {points.length>0&&<dl className="test-performance-overview" role="group" aria-label="Test run overview">
    <div><dt>{selectedSuite?'Latest suite':'Latest run'}</dt><dd>{overview.latestSeconds===null?'—':`${overview.latestSeconds.toFixed(1)} s`}{overview.partial&&<small>partial</small>}</dd></div>
    <div><dt>Window change</dt><dd>{overview.deltaPercent===null?'—':`${overview.deltaPercent>=0?'+':''}${overview.deltaPercent.toFixed(1)}%`}</dd></div>
    <div><dt>Slowest suite</dt><dd>{overview.slowest?<><span>{overview.slowest.suite}</span><small>{overview.slowest.seconds.toFixed(1)} s</small></>:'—'}</dd></div>
    <div><dt>Recorded runs</dt><dd>{overview.count.toLocaleString('en-US')}</dd></div>
   </dl>}
   {points.length?<><div className="test-performance-chart"><VisualBlock kind="vega-lite" source={source} hostedTab stretch/></div><p className="test-performance-caption">{points.length<observed&&`Chart displays ${points.length} of ${observed} suite observations (up to eight series). Filter to see more. `}{runs.length} recorded {runs.length===1?'run':'runs'} · Wall time per suite; parallel suites can overlap.</p></>:data.scan_limited?null:<div className="test-performance-empty"><span aria-hidden="true">⏱</span><h2>{data.runs.length?'No runs in this window':'Give your tests a history'}</h2><p>{data.runs.length?'Try a wider window or another project.':'Import a gate history to see suite times change with each commit.'}</p><code>test-performance -history .superpowers/gate-runs.jsonl -member MEMBER</code></div>}
   {data.scan_limited&&<p className="insights-note">More runs may follow. Load more to continue checking your history.</p>}
   {data.truncated&&<p className="insights-note">More recorded runs are available.</p>}
   {data.next_cursor&&<button className="btn small" disabled={loadingMore} aria-busy={loadingMore} onClick={()=>setCursorRequest({key,cursor:data.next_cursor!})}>{data.scan_limited?'Load more':'Earlier runs'}</button>}
   {inventory?.key===baseKey&&inventory.truncated&&<p className="insights-note">The project inventory is incomplete. Open a workspace to narrow it.</p>}
   {findings.rows.length>0&&<section className="test-performance-findings" aria-label="Suite findings">
    <h2>Suite findings</h2>
    <p className="test-performance-caption">Latest reported suite measurements. Window changes compare complete passing runs in the same project, region and reported layer; missing baselines and budgets remain unknown.</p>
    <div className="test-performance-suite-grid">{findings.rows.map(row=><article className="test-performance-suite" key={row.key}>
     <h3>{row.suite}</h3><p className="test-performance-caption">{row.member} · {row.workspace.slice(0,8)}{row.region&&` · ${row.region}`}{row.layer&&` · Layer ${row.layer}`}{row.partial&&' · Partial run'} · {row.outcome}</p>
     <dl><div><dt>Latest</dt><dd>{row.seconds.toFixed(1)} s</dd></div><div><dt>Budget</dt><dd>{row.budgetSeconds===null?'Unknown':`${row.budgetSeconds.toFixed(1)} s`}</dd></div><div><dt>Window change</dt><dd title={row.baselineAt?`Compared with ${new Date(row.baselineAt).toLocaleString()}`:undefined}>{row.deltaPercent===null?'Unknown':`${row.deltaPercent>=0?'+':''}${row.deltaPercent.toFixed(1)}%`}</dd></div><div><dt>Reported flakes</dt><dd>{row.flakes===null?'Unknown':row.flakes}</dd></div></dl>
     {row.budgetSeconds!==null&&<BudgetMeter identity={row.key} seconds={row.seconds} budget={row.budgetSeconds} entered={meterEntrances}/>}
    </article>)}</div>
    {findings.rows.length<findings.total&&<p className="test-performance-caption">Showing {findings.rows.length} of {findings.total} suite findings. Filter to see more.</p>}
   </section>}
   {runs.length>0&&<details className="test-performance-details" open={detailsOpen}><summary onClick={e=>{e.preventDefault();setDetailsOverride(!detailsOpen)}}>Recorded runs · {runs.length}</summary>{detailsOpen&&<><div className="test-performance-table"><table><thead><tr><th>Run</th><th>Commit</th><th>Suite</th><th>Seconds</th><th>Outcome</th><th>Tests</th></tr></thead><tbody>{runs.slice(detailPage*20,(detailPage+1)*20).flatMap(r=>r.suites.filter(s=>!selectedSuite||s.suite===selectedSuite).map(s=><tr key={`${r.workspace_id}/${r.member_id}/${r.run_id}/${s.suite}`}><td>{new Date(r.finished_at).toLocaleString()} {!r.complete&&'· incomplete'}</td><td><code>{r.commit?.slice(0,8)??'Uncommitted'}</code></td><td>{s.suite}</td><td>{s.seconds.toFixed(2)}</td><td>{s.outcome}</td><td>{s.counts?`${s.counts.pass} passed · ${s.counts.fail} failed · ${s.counts.skip} skipped`:'Unknown'}</td></tr>))}</tbody></table></div>{runs.length>20&&<div className="test-performance-pagination"><button className="icon-btn" aria-label="Previous runs" disabled={detailPage===0} onClick={()=>setPage(detailPage-1)}>←</button><span>{detailPage+1} of {Math.ceil(runs.length/20)}</span><button className="icon-btn" aria-label="Next runs" disabled={(detailPage+1)*20>=runs.length} onClick={()=>setPage(detailPage+1)}>→</button></div>}</>}</details>}
   <p className="test-performance-caption">Reported measurements · kept for 180 days, up to 2,000 runs per project. Missing samples remain gaps.</p>
  </>}
 </section>
}


function BudgetMeter({identity,seconds,budget,entered}:{identity:string;seconds:number;budget:number;entered:Set<string>}) {
 const meter=useRef<HTMLMeterElement>(null)
 useEffect(()=>{
  const node=meter.current
  if(entered.has(identity))return
  entered.add(identity)
  if(!node?.animate||window.matchMedia('(prefers-reduced-motion: reduce)').matches)return
  try {
   const animation=node.animate([{transform:'scaleX(0)'},{transform:'scaleX(1)'}],{duration:400,easing:'ease-out'})
   return()=>animation.cancel()
  }catch(error){console.warn('Test budget animation failed',{cause:error instanceof Error?error.name:'unknown'})}
 },[identity,entered])
 const over=Math.max(0,seconds-budget),max=Math.max(seconds,budget)*1.1
 const status=over>0?`${over.toFixed(1)} s over`:'Within budget'
 const label=`${seconds.toFixed(1)} s of ${budget} s budget, ${status}`
 const style={'--budget-position':`${budget/max*100}%`,'--within-fill':`${Math.min(1,budget/Math.max(seconds,Number.EPSILON))*100}%`} as CSSProperties
 return <div className="test-performance-budget" style={style}><div className="test-performance-meter-track"><meter ref={meter} min={0} max={max} value={seconds} aria-label={label} aria-valuetext={label}/><span className="test-performance-budget-marker" aria-hidden="true" title={`${budget} s budget`}/></div><span className={over>0?'test-performance-overrun':undefined}>{status}</span></div>
}
