import './Insights.css'
import './ActivityInsights.css'
import { useState } from 'react'
import { formatDuration, formatTokens, formatTick, timeAxis, type CountRow } from '../lib/insights'
import type { ActivitySpread } from '../lib/insightsActivity'
import { ShowAllButton } from './InsightsExpand'
import { useExpand } from '../hooks/useExpand'

export function BarChart({rows,duration=false,tokens=false,records}:{rows:(CountRow&{id?:string;href?:string})[];duration?:boolean;tokens?:boolean;records?:(key:string)=>string[]}){
 const [order,setOrder]=useState<'name'|'value'>('value')
 const [selected,setSelected]=useState<string|null>(null)
 rows=[...rows].filter(r=>!selected||(r.id??r.key)===selected).sort((a,b)=>order==='name'?a.key.localeCompare(b.key):b.count-a.count)
 const rowsExpand=useExpand(rows.length,50)
 const recordList=selected&&records?records(selected):null
 const recordsExpand=useExpand(recordList?.length??0,50,selected)

 if(!rows.length)return <p className="insights-note">No records in this range.</p>
 const max=Math.max(1,...rows.map(r=>r.count))
 return <><div className="insights-list-sort"><button className="insights-sort" onClick={()=>setOrder('name')}>Name{order==='name'?' ↑':''}</button><button className="insights-sort" onClick={()=>setOrder('value')}>Value{order==='value'?' ↓':''}</button>{selected&&<button className="insights-sort" onClick={()=>setSelected(null)}>Show all</button>}</div><ul id={rowsExpand.id} className="activity-bars">{rows.slice(0,rowsExpand.shown).map(r=><li key={r.id??r.key}><span title={r.key}>{r.href?<a href={r.href}>{r.key}</a>:r.key}</span><button className="activity-bar-track" aria-label={`Show ${r.key}`} onClick={()=>setSelected(r.id??r.key)}><i style={{width:`${r.count/max*100}%`}}/></button><button className="insights-sort" aria-label={`Show ${r.key}: ${duration?formatDuration(r.count):tokens?formatTokens(r.count):r.count}`} onClick={()=>setSelected(r.id??r.key)}>{duration?formatDuration(r.count):tokens?formatTokens(r.count):r.count}</button></li>)}</ul>{rowsExpand.hasMore&&<ShowAllButton id={rowsExpand.id} total={rows.length} limit={50} expanded={rowsExpand.expanded} onToggle={rowsExpand.toggle}/>}{recordList&&<ul id={recordsExpand.id} className="activity-records" aria-label={`Records for ${selected}`}>{recordList.slice(0,recordsExpand.shown).map((record,i)=><li key={i}>{record}</li>)}</ul>}{recordsExpand.hasMore&&<ShowAllButton id={recordsExpand.id} total={recordList!.length} limit={50} expanded={recordsExpand.expanded} onToggle={recordsExpand.toggle}/>}</>
}
export function SpreadChart({rows,rounds=false,records,caption}:{rows:ActivitySpread[];rounds?:boolean;caption?:string;records?:(key:string)=>string[]}){
 const [selected,setSelected]=useState<string|null>(null)
 const [descending,setDescending]=useState(false)
 rows=[...rows].filter(r=>!selected||r.step===selected).sort((a,b)=>descending?b.p50-a.p50:a.step.localeCompare(b.step))
 const recordList=selected&&records?records(selected):null
 const recordsExpand=useExpand(recordList?.length??0,50,selected)
 const value=(n:number)=>rounds?`${n} review round${n===1?'':'s'}`:formatDuration(n)
 const ceiling=Math.max(1,...rows.map(r=>r.max))
 const axis=rounds?{kind:'linear',pos:(n:number)=>n/ceiling,ticks:[...new Set([0,Math.ceil(ceiling/2),ceiling])]}:timeAxis(rows),pct=(n:number)=>`${axis.pos(n)*100}%`
 return <figure className="activity-spreads"><figcaption>{caption && `${caption} `}Middle half (25th–75th percentile), median, fastest–slowest. Fewer than five observations: raw-value dots only. {axis.kind==='log'?'Logarithmic':'Linear'} scale.</figcaption>
  <div className="insights-list-sort"><button className="insights-sort" onClick={()=>setDescending(false)}>Name</button><button className="insights-sort" onClick={()=>setDescending(true)}>Median ↓</button>{selected&&<button className="insights-sort" onClick={()=>setSelected(null)}>Show all</button>}</div><ul>{rows.map(r=><li key={r.step}><button className="insights-sort" onClick={()=>setSelected(r.step)}>{r.step}</button><div onClick={()=>setSelected(r.step)} className="sb-plot" role="img" aria-label={r.count<5?`${r.step}: n ${r.count}, observed values ${r.samples.map(value).join(', ')}`:`${r.step}: n ${r.count}, fastest ${value(r.min)}, 25th ${value(r.p25)}, median ${value(r.p50)}, 75th ${value(r.p75)}, slowest ${value(r.max)}`}>
   <i className="sb-whisker" style={{left:pct(r.min),width:`${(axis.pos(r.max)-axis.pos(r.min))*100}%`}}/>
   {r.count>=5?<><i className="sb-box" style={{left:pct(r.p25),width:`${(axis.pos(r.p75)-axis.pos(r.p25))*100}%`}}/><i className="sb-median" style={{left:pct(r.p50)}}/></>:r.samples.map((v,i)=><i key={i} className="sb-dot" style={{left:pct(v)}}/>)}
  </div><button className="insights-sort" onClick={()=>setSelected(r.step)}>n {r.count} · median {value(r.p50)}{r.count<5?' · small group, raw dots only':''}</button></li>)}<li className="activity-axis"><span/><div className="sb-plot">{axis.ticks.map(t=><span key={t} className="sb-tick" style={{left:pct(t)}}>{rounds?t:formatTick(t)}</span>)}</div></li></ul>
 {recordList&&<ul id={recordsExpand.id} className="activity-records" aria-label={`Records for ${selected}`}>{recordList.slice(0,recordsExpand.shown).map((record,i)=><li key={i}>{record}</li>)}</ul>}{recordsExpand.hasMore&&<ShowAllButton id={recordsExpand.id} total={recordList!.length} limit={50} expanded={recordsExpand.expanded} onToggle={recordsExpand.toggle}/>}</figure>
}
