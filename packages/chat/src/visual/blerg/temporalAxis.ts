import {temporalTicks,formatTemporalTick} from './scaleOptions'
import {recordBottomOverflow} from './axisOptions'
import {measureText,measureTextBounds} from './layout'
import {group,line,text} from './svg'
import type {LinearScale} from './scales'
import type {AxisLayout} from './axes'
import type {VisualTheme} from '../theme'

export function drawTemporalAxisBottom(scale:LinearScale,layout:AxisLayout,theme:VisualTheme,options:{format?:string;tickCount?:number;grid?:boolean;labelAngle?:number}={}):string {
 const y=layout.y+layout.height
 const values=temporalTicks(scale.domain,options.tickCount ?? Math.min(12,Math.max(2,Math.ceil(layout.width/80))))
 let right=-Infinity
 const ticks=values.map(value=>{
  const x=scale(value),label=formatTemporalTick(value,options.format),width=measureText(theme,label)
  const visible=x-width/2>right+6
  if(options.labelAngle===-45){const bounds=measureTextBounds(theme,label),bottom=bounds.y+bounds.height;recordBottomOverflow(Math.max(0,(width-bounds.x+bottom)*Math.SQRT1_2-bottom))}
  if(visible)right=x+width/2
  const ly=y+9+theme.fontSize*0.8
  return group([
   options.grid ? line(x,layout.y,x,y,{stroke:theme.border,'stroke-opacity':0.35,'data-grid':'x'}) : '',
   line(x,y,x,y+5,{stroke:theme.border}),
   text(x,ly,label,{fill:theme.muted,'font-size':theme.fontSize,'font-family':theme.fontFamily,'text-anchor':options.labelAngle ? 'end' : 'middle',transform:options.labelAngle ? `rotate(${options.labelAngle} ${x} ${ly})` : undefined,visibility:visible ? undefined : 'hidden','aria-hidden':visible ? undefined : 'true'}),
  ],{'data-scene-key':`tick:${value}`})
 })
 return group([line(layout.x,y,layout.x+layout.width,y,{stroke:theme.border}),...ticks],{'data-temporal-axis':'x'})
}
