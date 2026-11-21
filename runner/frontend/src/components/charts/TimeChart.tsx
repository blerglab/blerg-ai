import { useEffect, useId, useRef, useState } from 'react'
import '../IconButton.css'
import './TimeChart.css'

export interface TimeSeries { key: string; label: string; description?: string }
export interface TimeBucket { at: string; values: Record<string, number> }
export interface TimeEvent { at: string; label: string }
const COLORS = ['var(--chart-git)', 'var(--chart-read)', 'var(--chart-build)', 'var(--chart-edit)', 'var(--chart-model)', 'var(--chart-other)', 'var(--chart-waiting)', 'var(--chart-idle)', 'var(--fog-dim)']

/** One time axis shared by Insights and workspace planning. Missing dates must
 * be explicit zero buckets; events occupy the same slots as their bars. */
export default function TimeChart({ buckets, series, events = [], label, format = String, share = false, now = new Date().toISOString(), hourly = false, onSelectCategory, selectedCategory }: {
 buckets: TimeBucket[]; series: TimeSeries[]; events?: TimeEvent[]; label: string
 format?: (value: number) => string; share?: boolean; now?: string; hourly?: boolean
 onSelectCategory?:(key:string)=>void;selectedCategory?:string|null
}) {
 const [hidden, setHidden] = useState<string[]>([])
 const [selected,setSelected]=useState('')
 const [eventDay,setEventDay]=useState<string|null>(null)
 const id = useId()
 const ref=useRef<HTMLElement>(null)
 const [width,setWidth]=useState(700)
 useEffect(()=>{const el=ref.current;if(!el)return;const update=()=>setWidth(Math.max(280,el.clientWidth));update();const observer=new ResizeObserver(update);observer.observe(el);return()=>observer.disconnect()},[])
 const available = series.filter(s => buckets.some(b => b.values[s.key] > 0))
 const visible = available.filter(s => !hidden.includes(s.key))
 const totals = buckets.map(b => visible.reduce((n, s) => n + (b.values[s.key] || 0), 0))
 const rawMaximum=Math.max(1e-9,...totals),magnitude=10**Math.floor(Math.log10(rawMaximum))
 const maximum = share ? 100 : Math.ceil(rawMaximum/magnitude*1.15/2)*2*magnitude
 const left = 56, right = 14
 const slot = (width - left - right) / Math.max(1, buckets.length)
 // A visibility change can make totals longer. Rotated totals need their
 // own space below the event rail, without shrinking the plotting area.
 const baseTop=buckets.length>14?86:60
 const rotationRoom=Math.max(0,...totals.map(total=>{
  if(total<=0||slot>=Math.max(32,format(total).length*7))return 0
  const barHeight=(share?1:total/maximum)*(264-baseTop)
  const labelTop=264-barHeight-7-format(total).length*7
  return Math.max(0,(events.length?46:12)-labelTop)
 }))
 const top=baseTop+rotationRoom,bottom=264+rotationRoom,height=300+rotationRoom
 const compactEvents=slot<120
 const step = Math.max(1, Math.ceil(buckets.length / (width<500?4:7)))
 const dateLabel = (at: string) => new Date(at).toLocaleString([], hourly ? { hour: 'numeric', timeZone: 'UTC' } : { month: 'short', day: 'numeric', timeZone: 'UTC' })
 const bucketEnd = (i: number) => i + 1 < buckets.length ? Date.parse(buckets[i+1].at) : Date.parse(buckets[i].at) + (hourly ? 3600_000 : buckets.length > 1 ? Date.parse(buckets[i].at)-Date.parse(buckets[i-1].at) : 86400_000)
 let today = buckets.findIndex((b, i) => Date.parse(now) >= Date.parse(b.at) && Date.parse(now) < bucketEnd(i))
 if(today<0&&buckets.length&&Date.parse(now)===bucketEnd(buckets.length-1))today=buckets.length-1
 return <figure ref={ref} className="time-chart" aria-label={label}>
  <div className="time-chart-key" role="group" aria-label={`${label} categories`}>{available.map(s => {const drill=onSelectCategory&&(s.key==='gates'||s.key==='building'),action=`${selectedCategory===s.key?'Hide':'Show'} ${s.label} breakdown`;return <span className="time-chart-key-item" key={s.key}><button type="button" className="insights-sort" aria-pressed={!hidden.includes(s.key)} title={s.description || `Show or hide ${s.label}`} onClick={() => setHidden(h => h.includes(s.key) ? h.filter(k => k!==s.key) : [...h,s.key])}><i style={{background:COLORS[series.indexOf(s)%COLORS.length]}}/>{s.label}</button>{drill&&<button type="button" className="icon-btn" aria-label={action} title={action} aria-expanded={selectedCategory===s.key} onClick={()=>onSelectCategory(s.key)}><span aria-hidden="true">{selectedCategory===s.key?'▾':'▸'}</span></button>}</span>})}</div>
  <svg viewBox={`0 0 ${width} ${height}`} role="group" aria-labelledby={id}>
   <title id={id}>{label}. {share ? 'Share of recorded time.' : 'Totals by date.'} Dates in UTC.</title>
   {[0, .5, 1].map(f => <g key={f}><line className="time-chart-grid" x1={left} x2={width-right} y1={bottom-(bottom-top)*f} y2={bottom-(bottom-top)*f}/><text x={left-10} y={bottom-(bottom-top)*f+4} textAnchor="end">{share ? `${Math.round(maximum*f)}%` : format(maximum*f)}</text></g>)}
   {today>=0 && <g><line className="time-chart-today" x1={left+(today+1)*slot} x2={left+(today+1)*slot} y1={18} y2={bottom}/><text x={Math.min(width-right,left+(today+.5)*slot)} y={height-4} textAnchor="middle">Today</text></g>}
   {buckets.map((b,i) => {
    let used=0
    const barWidth=Math.min(48,slot*.6),x=left+i*slot+(slot-barWidth)/2
    const dayEvents=events.filter(e => Date.parse(e.at)>=Date.parse(b.at) && Date.parse(e.at)<bucketEnd(i))
    return <g key={b.at}>
     {dayEvents.length>0 && (()=>{
      // A hover summarizes; the day's events are listed only when the day is chosen, capped.
      const name=`${dateLabel(b.at)}: ${dayEvents.length} ${dayEvents.length===1?'event':'events'}`
      const shown=dayEvents.slice(0,3).map(e=>e.label)
      const summary=[name,...shown,...(dayEvents.length>3?[`and ${dayEvents.length-3} more`]:[])].join(' · ')
      const toggle=()=>setEventDay(eventDay===b.at?null:b.at)
      // button-guard-ignore: SVG event markers use their own dot and the shared focus ring, without a native button box.
      return <g className="time-chart-event" tabIndex={0} role="button" aria-label={name} aria-pressed={eventDay===b.at} onClick={toggle} onKeyDown={e=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();toggle()}}}><circle cx={x+barWidth/2} cy={18} r={4} fill="var(--lichen)"/>{(!compactEvents||dayEvents.length>1)&&<text x={x+barWidth/2} y={39} textAnchor="middle">{compactEvents?dayEvents.length:dayEvents.length>1 ? `${dayEvents.length} events` : dayEvents[0].label.slice(0,18)}</text>}<title>{summary}</title></g>
     })()}
     {visible.map(s=>{
      const value=b.values[s.key]||0,n=share?(totals[i]?100*value/totals[i]:0):value,h=n/maximum*(bottom-top);used+=h
      const description=`${dateLabel(b.at)} · ${s.label}: ${format(value)}${share&&totals[i]?` (${Math.round(100*value/totals[i])}%)`:''}`
      const activate=()=>{setSelected(description);if(onSelectCategory&&(s.key==='gates'||s.key==='building'))onSelectCategory(s.key)}
      // button-guard-ignore: SVG bars use their series fill and shared focus outline, without a native button box.
      return value>0?<rect key={s.key} tabIndex={0} role="button" aria-label={description} onFocus={()=>setSelected(description)} onClick={activate} onKeyDown={e=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();activate()}}} x={x} width={barWidth} y={bottom-used} height={h} fill={COLORS[series.indexOf(s)%COLORS.length]}><title>{description}</title></rect>:null
     })}
     {totals[i]>0 && <text x={x+barWidth/2} y={bottom-used-7} transform={slot<Math.max(32,format(totals[i]).length*7)?`rotate(-90 ${x+barWidth/2} ${bottom-used-7})`:undefined} textAnchor={slot<Math.max(32,format(totals[i]).length*7)?'start':'middle'}>{format(totals[i])}</text>}
     {(i===buckets.length-1 || (i%step===0&&buckets.length-1-i>=Math.ceil(step/2)+1)) && <text x={x+barWidth/2} y={bottom+20} textAnchor="middle">{dateLabel(b.at)}</text>}
    </g>
   })}
  </svg>
  <figcaption>{hourly?'Hourly':buckets.length>1&&Date.parse(buckets[1].at)-Date.parse(buckets[0].at)>=604800_000?'Weekly':'Daily'} trend · UTC. Select a category to show or hide it. Select or focus a bar for its value{onSelectCategory?'; select a Gates or Building bar or arrow for its breakdown.':'.'}<span role="status">{selected&&` ${selected}`}</span></figcaption>
  {/* A narrow chart has no room to label its dots, so a few events are listed once below it. */}
  {!eventDay&&compactEvents&&events.length>0&&events.length<=5&&<ul className="time-chart-events">{events.map((e,i)=><li key={`${e.at}-${i}`}>{dateLabel(e.at)} · {e.label}</li>)}</ul>}
  {eventDay&&(()=>{
   const i=buckets.findIndex(b=>b.at===eventDay)
   const day=i<0?[]:events.filter(e=>Date.parse(e.at)>=Date.parse(eventDay)&&Date.parse(e.at)<bucketEnd(i))
   return day.length>0&&<><ul className="time-chart-events" aria-label={`${dateLabel(eventDay)} events`}>{day.slice(0,10).map((e,j)=><li key={`${e.at}-${j}`}>{e.label}</li>)}</ul>{day.length>10&&<p className="time-chart-events-more">Showing 10 of {day.length}</p>}</>
  })()}
 </figure>
}
