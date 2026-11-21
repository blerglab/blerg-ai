import {timeTicks, timeTickInterval} from 'd3-time'
import {timeFormat} from 'd3-time-format'
import {niceLinearDomain,linearScale,type LinearScale} from './scales'

export interface ScaleOptions {zero?:boolean; domain?:[number,number] | [string,string]; nice?:boolean}
export function temporalValue(value:unknown):number {
 return typeof value==='number' ? value : typeof value==='string' ? +new Date(value) : NaN
}
export function isScaleOptions(value:unknown, temporal=false):boolean {
 if(!value || typeof value!=='object' || Array.isArray(value))return false
 return Object.entries(value).every(([key,v])=>{
  if(key==='zero')return typeof v==='boolean' && (!temporal || v===false)
  if(key==='nice')return typeof v==='boolean'
  if(key==='domain')return Array.isArray(v) && v.length===2 && v.every(x=>temporal ? Number.isFinite(temporalValue(x)) : typeof x==='number' && Number.isFinite(x)) && (temporal ? temporalValue(v[0])!==temporalValue(v[1]) : v[0]!==v[1])
  return false
 })
}
/** Time scales use local calendar boundaries, matching Vega's `time` scale. */
export function positionDomain(values:readonly number[], options:ScaleOptions|undefined, zero:boolean, temporal=false):[number,number] {
 const explicit=options?.domain
 let domain:[number,number]=explicit ? [temporalValue(explicit[0]),temporalValue(explicit[1])] : [Math.min(...values),Math.max(...values)]
 if((options?.zero ?? (!explicit && zero)) && !temporal)domain=[Math.min(0,domain[0]),Math.max(0,domain[1])]
 // A degenerate domain is owned here, before numeric/calendar nicening.
 if(domain[0]===domain[1])return domain
 if(options?.nice ?? (!temporal && !explicit)){
  if(!temporal)return niceLinearDomain(domain)
  const reverse=domain[1]<domain[0],lo=Math.min(...domain),hi=Math.max(...domain)
  const interval=timeTickInterval(new Date(lo),new Date(hi),10)
  if(interval){const nice:[number,number]=[+interval.floor(new Date(lo)),+interval.ceil(new Date(hi))];domain=reverse ? [nice[1],nice[0]] : nice}
 }
 return domain
}
export function temporalTicks(domain:readonly [number,number],count:number):number[]{
 return timeTicks(new Date(domain[0]),new Date(domain[1]),count).map(Number)
}
// d3's multi-resolution local time labels: enough context at every calendar boundary.
export function formatTemporalTick(value:number, format?:string):string {
 const date=new Date(value)
 const spec=format ?? (date.getMilliseconds() ? '.%L' : date.getSeconds() ? ':%S' : date.getMinutes() ? '%I:%M' : date.getHours() ? '%I %p' : date.getDate()!==1 ? '%a %d' : date.getMonth() ? '%B' : '%Y')
 return timeFormat(spec)(date)
}

/** d3 continuous scales map a degenerate domain to the range midpoint. */
export function positionScale(domain:[number,number],range:[number,number]):LinearScale {
 if(domain[0]!==domain[1])return linearScale(domain,range)
 return Object.assign(()=> (range[0]+range[1])/2,{domain})
}
