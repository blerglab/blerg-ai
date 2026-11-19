// A precomputed interval is the unit: gaps stay gaps and an open interval's effective
// end must be supplied by the source's observation clock, never the renderer's clock.
import {bandScale, linearScale} from '../scales'
import {plotLayout, measureText} from '../layout'
import {group,line,rect,text} from '../svg'
import {categoricalColorScale,categoricalStyleIndex,isImplementedSort,sortedDomain,type EncodingSort} from '../color'
import {drawCategoricalLegend,legendWidth} from '../legend'
import {datumKey} from '../dataIdentity'
import {tooltipRegionText,type TooltipFieldDef} from '../tooltip'
import {chartLabelColor} from '../../theme'
import type {VisualTheme} from '../../theme'
import {NotShippedYet} from './rect'

interface Field {field?:string;type?:string;sort?:EncodingSort;bin?:unknown}
export interface TimelineEncoding {x?:Field;x2?:Field;y?:Field;color?:Field;tooltip?:TooltipFieldDef|TooltipFieldDef[]}
export function drawStatusTimeline(rows:Record<string,unknown>[], encoding:TimelineEncoding, width:number,height:number,theme:VisualTheme) {
 const {x,x2,y,color}=encoding
 if(!x?.field || !x2?.field || !y?.field || !color?.field || x.type!=='temporal' || x2.type!==x.type || !['nominal','ordinal'].includes(y.type ?? '') || !['nominal','ordinal'].includes(color.type ?? '') || x.bin || x2.bin || y.bin || !isImplementedSort(y.sort) || !isImplementedSort(color.sort) || x.sort!==undefined)throw new NotShippedYet('timeline: unsupported interval encoding')
 const xf=x.field,ef=x2.field,yf=y.field,cf=color.field
 const temporal=x.type==='temporal'
 const value=(v:unknown)=>temporal && typeof v==='string' && /T.*(?:Z|[+-]\d{2}:\d{2})$/i.test(v) ? Date.parse(v) : typeof v==='number' ? v : NaN
 const intervals=rows.map(row=>({row,start:value(row[xf]),end:value(row[ef]),entity:String(row[yf]),status:String(row[cf]),key:datumKey(row,xf,yf)}))
 if(intervals.some(interval=>!Number.isFinite(interval.start)||!Number.isFinite(interval.end)||interval.end<interval.start))throw new NotShippedYet('timeline: invalid interval')
 const entities=sortedDomain(intervals.map(i=>i.entity),y.sort),statuses=sortedDomain(intervals.map(i=>i.status),color.sort)
 const colors=categoricalColorScale(statuses,theme.palette)
 const reserve=legendWidth(statuses,cf,theme),plot=plotLayout(width-reserve,height-theme.fontSize,entities,theme)
 const lo=Math.min(...intervals.map(i=>i.start)),hi=Math.max(...intervals.map(i=>i.end))
 const domain:[number,number]=rows.length ? [lo,hi===lo?lo+1:hi] : [0,1]
 const xs=linearScale(domain,[plot.x,plot.x+plot.width]),ys=bandScale(entities,[plot.y,plot.y+plot.height],.15,.075)
 const marks=intervals.map(i=>{
  const left=xs(i.start),top=ys(i.entity)!,span=xs(i.end)-left,index=categoricalStyleIndex(i.status,statuses)
  const label=i.status+(i.row.open===true?' · open':'')
  return group([
   rect(left,top,span,ys.bandwidth,{fill:colors(i.status),stroke:theme.fg,'stroke-width':1,'stroke-dasharray':index ? `${index+1} 2` : undefined}),
   ...(i.row.open===true ? [group([line(left+span-Math.min(5,span),top+2,left+span,top+ys.bandwidth/2,{stroke:theme.fg,'stroke-width':2}),line(left+span,top+ys.bandwidth/2,left+span-Math.min(5,span),top+ys.bandwidth-2,{stroke:theme.fg,'stroke-width':2})],{'data-open-interval':'true'})]:[]),
   ...(span>measureText(theme,label)+8 ? [text(left+span/2,top+ys.bandwidth/2+theme.fontSize*.3,label,{fill:chartLabelColor(theme,colors(i.status)),'font-size':theme.fontSize,'font-family':theme.fontFamily,'text-anchor':'middle'})] : []),
  ],{'data-datum-key':i.key,'data-scene-key':i.key,'aria-label':`${i.entity}, ${i.status}${i.row.open===true?' (open)':''}, ${String(i.row[xf])} – ${String(i.row[ef])}`})
 })
 const bottom=plot.y+plot.height,count=Math.max(2,Math.floor(plot.width/(temporal?theme.fontSize*13:theme.fontSize*5)))
 const duration=(domain[1]-domain[0])/Math.max(1,count-1)
 const step=[1000,5000,15000,30000,60000,300000,900000,1800000,3600000,10800000,21600000,43200000,86400000].find(step=>step>=duration) ?? Math.ceil(duration/86400000)*86400000
 const ticks=lo===hi?[lo]:[...new Set([domain[0],...Array.from({length:Math.max(0,Math.floor(domain[1]/step)-Math.ceil(domain[0]/step)+1)},(_,i)=>(Math.ceil(domain[0]/step)+i)*step),domain[1]])].sort((a,b)=>a-b)
 const sameDay=new Date(domain[0]).toISOString().slice(0,10)===new Date(domain[1]).toISOString().slice(0,10)
 const labels=ticks.map(t=>new Date(t).toISOString().slice(sameDay?11:5,step<60000 || domain[1]-domain[0]<60000 || ((t===lo||t===hi)&&t%60000!==0)?19:16).replace('T',' ')+'Z')
 const bounds=(i:number)=>{const px=xs(ticks[i]),size=measureText(theme,labels[i]);const anchor=i===0?'start':i===ticks.length-1?'end':'middle';return {px,size,anchor,left:anchor==='start'?px:anchor==='end'?px-size:px-size/2}}
 const last=bounds(ticks.length-1),stagger=bounds(0).size+last.size+6>plot.width
 let right=-Infinity
 const xTicks=ticks.flatMap((t,i)=>{
  const {px,size,anchor,left}=bounds(i),endpoint=i===0||i===ticks.length-1
  if(!endpoint && (left<right+6 || left+size+6>last.left))return []
  right=left+size
  return [group([line(px,bottom,px,bottom+4,{stroke:theme.border}),text(px,bottom+theme.fontSize+6+(stagger && i===ticks.length-1?theme.fontSize:0),labels[i],{fill:theme.muted,'text-anchor':anchor,'font-size':theme.fontSize,'font-family':theme.fontFamily})],{'data-scene-key':`tick:${t}`})]
 })
 const yLabels=entities.map(entity=>text(plot.x-9,ys(entity)!+ys.bandwidth/2+theme.fontSize*.3,entity,{fill:theme.muted,'text-anchor':'end','font-size':theme.fontSize,'font-family':theme.fontFamily}))
 const axes=group([line(plot.x,bottom,plot.x+plot.width,bottom,{stroke:theme.border}),...xTicks,...yLabels],{'data-scene-key':'axes'})
 const legend=drawCategoricalLegend(statuses,colors,plot.x+plot.width+12,plot.y,theme,cf,'interval')
 const accessiblePoints=intervals.map(i=>({key:i.key,label:`${i.entity}, ${i.status}${i.row.open===true?" (open)":""}`,value:`${String(i.row[xf])} – ${String(i.row[ef])}`}))
 const tooltipRegions=intervals.map(i=>({key:i.key,x1:xs(i.start),x2:xs(i.end),y1:ys(i.entity)!,y2:ys(i.entity)!+ys.bandwidth,text:tooltipRegionText(encoding.tooltip,i.row)+(i.row.open===true?'\nOpen interval (through observation time)':'')}))
 return {svg:group([group(marks,{'data-scene-key':'marks'}),axes,legend]),accessiblePoints,tooltipRegions}
}
