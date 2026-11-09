import type {VisualTheme} from '../theme'
import {measureText} from './layout'
import {drawCategoricalLegend,legendWidth} from './legend'
import {group,line,text} from './svg'
import type {ColorScale} from './color'

export interface LegendOptions {orient?:'top'|'right'}
export function isLegendOptions(value:unknown):boolean {
 return value===null || !!value && typeof value==='object' && !Array.isArray(value) && Object.entries(value).every(([key,v])=>key==='orient' && (v==='top'||v==='right'))
}
/** Reserve a measured legend rectangle before computing the plot, wrapping top rows. */
export function seriesLegend(domain:readonly string[],color:ColorScale,width:number,theme:VisualTheme,title:string|undefined,options:LegendOptions|null|undefined):{right:number;top:number;svg:(x:number,y:number)=>string} {
 if(options===null)return {right:0,top:0,svg:()=>''}
 if(options?.orient!=='top')return {right:legendWidth(domain,title,theme),top:0,svg:(x,y)=>drawCategoricalLegend(domain,color,x,y,theme,title,'stroke')}
 const rowHeight=theme.fontSize+10,padding=12
 let x=padding,y=title ? rowHeight : 0
 const labels=domain.map(key=>{
  const span=22+measureText(theme,key)+16
  if(x>padding && x+span>width-padding){x=padding;y+=rowHeight}
  const px=x,py=y+theme.fontSize
  x+=span
  return group([
   line(px,py-4,px+10,py-4,{stroke:color(key),'stroke-width':2,'data-legend-swatch':'true'}),
   text(px+16,py,key,{fill:theme.fg,'font-size':theme.fontSize,'font-family':theme.fontFamily}),
  ],{'data-scene-key':`series:${key}`})
 })
 const heading=title ? text(padding,theme.fontSize,title,{fill:theme.fg,'font-size':theme.fontSize,'font-family':theme.fontFamily,'font-weight':'bold'}) : ''
 return {right:0,top:y+rowHeight,svg:()=>group([heading,...labels],{'data-scene-key':'legend'})}
}
