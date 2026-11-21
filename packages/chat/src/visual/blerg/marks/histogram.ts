// Histogram input is already binned. No renderer transform silently changes bin edges,
// counts or the source's inclusive-edge rule.
import {linearScale,niceLinearDomain} from '../scales'
import {continuousTicks,densityTickCount,preciseDensityTickCount,drawLinearAxisBottom,drawLinearAxisLeft,formatTicks} from '../axes'
import {plotLayout} from '../layout'
import {group,rect,line,text,recordPlotBounds} from '../svg'
import {datumKey} from '../dataIdentity'
import {markStyle} from '../color'
import {tooltipRegionText,type TooltipFieldDef} from '../tooltip'
import type {VisualTheme} from '../../theme'
import {NotShippedYet} from './bar'
interface Field {field?:string;type?:string;bin?:unknown;aggregate?:string;sort?:unknown;stack?:unknown;axis?:{title?:string|null}}
export interface HistogramEncoding {x?:Field;x2?:Field;y?:Field;color?:Field;xOffset?:Field;size?:Field;tooltip?:TooltipFieldDef|TooltipFieldDef[]}
export function supportsHistogram(encoding:Record<string,unknown>):boolean {
 const {x,x2,y,color,xOffset,size}=encoding as HistogramEncoding
 return !!x?.field && !!x2?.field && !!y?.field && x.type==='quantitative' && x2.type==='quantitative' && y.type==='quantitative' && x.bin==='binned' && !x2.bin && !y.bin && !x.aggregate && !x2.aggregate && !y.aggregate && x.sort===undefined && x2.sort===undefined && y.sort===undefined && y.stack===undefined && !color && !xOffset && !size
}
export function drawHistogram(rows:Record<string,unknown>[],encoding:HistogramEncoding,mark:unknown,width:number,height:number,theme:VisualTheme) {
 if(!supportsHistogram(encoding as Record<string,unknown>))throw new NotShippedYet('histogram: precomputed bounds and counts required')
 const xf=encoding.x!.field!,ef=encoding.x2!.field!,yf=encoding.y!.field!
 const bins=rows.map(row=>({row,lower:row[xf] as number,upper:row[ef] as number,count:row[yf] == null ? null : row[yf] as number,key:datumKey(row,xf,ef)}))
 if(bins.some(bin=>[bin.lower,bin.upper].some(value=>typeof value!=='number'||!Number.isFinite(value)) || (bin.count!==null && (typeof bin.count!=='number'||!Number.isFinite(bin.count)||bin.count<0)) || bin.upper<=bin.lower))throw new NotShippedYet('histogram: invalid bounds or count')
 const sorted=[...bins].sort((a,b)=>a.lower-b.lower)
 if(sorted.some((bin,i)=>i>0 && bin.lower<sorted[i-1].upper))throw new NotShippedYet('histogram: overlapping bins')
 const xDomain:[number,number]=bins.length?[Math.min(...bins.map(b=>b.lower)),Math.max(...bins.map(b=>b.upper))]:[0,1]
 const yDomain=niceLinearDomain([0,Math.max(0,...bins.map(b=>b.count ?? 0)) || 1])
 const count=densityTickCount(plotLayout(width,height,[],theme).height),ticks=continuousTicks(yDomain,count)
 const title=encoding.y?.axis?.title===null?'':encoding.y?.axis?.title ?? yf.replace(/[_-]+/g,' ').replace(/^./,value=>value.toUpperCase())
 const titleReserve=title?theme.fontSize*2:0
 const base=plotLayout(width,height,formatTicks(ticks),theme),plot={...base,x:base.x+titleReserve,width:Math.max(1,base.width-titleReserve)},xs=linearScale(xDomain,[plot.x,plot.x+plot.width]),ys=linearScale(yDomain,[plot.y+plot.height,plot.y])
 recordPlotBounds(plot)
 const style=markStyle(mark)
 const geometry=bins.map(bin=>({...bin,x:xs(bin.lower),y:ys(bin.count ?? 0)-(bin.count===null?3:0),width:xs(bin.upper)-xs(bin.lower),height:bin.count===null?3:ys(0)-ys(bin.count ?? 0)}))
 const marks=geometry.map(bin=>rect(bin.x,bin.y,bin.width,bin.height,{'data-entrance':'bar','data-datum-key':bin.key,'data-scene-key':bin.key,fill:bin.count===null?theme.surface2:style.color ?? theme.palette[0] ?? theme.accent,opacity:style.opacity,stroke:bin.count===null?theme.muted:theme.surface2,'stroke-width':1,'stroke-dasharray':bin.count===null?'2 2':undefined}))
 const grid=group(ticks.map(value=>line(plot.x,ys(value),plot.x+plot.width,ys(value),{stroke:theme.border,'stroke-opacity':.35})))
 const axes=group([...(title?[text(theme.fontSize,plot.y+plot.height/2,title,{'font-weight':'bold',fill:theme.fg,'font-size':theme.fontSize,'font-family':theme.fontFamily,'text-anchor':'middle',transform:`rotate(-90 ${theme.fontSize} ${plot.y+plot.height/2})`})]:[]),drawLinearAxisBottom(xs,xDomain,plot,theme,preciseDensityTickCount(plot.width)),drawLinearAxisLeft(ys,yDomain,plot,theme,count)],{'data-scene-key':'axes'})
 const accessiblePoints=bins.map(bin=>({key:bin.key,label:`[${bin.lower}, ${bin.upper})`,value:bin.count===null?'Unknown':String(bin.count)}))
 const tooltip=Array.isArray(encoding.tooltip)?encoding.tooltip.filter(def=>![xf,ef,yf].includes(def.field ?? '')):encoding.tooltip && ![xf,ef,yf].includes(encoding.tooltip.field ?? '')?encoding.tooltip:undefined
 const tooltipRegions=geometry.map(bin=>({key:bin.key,x1:bin.x,x2:bin.x+bin.width,y1:bin.y-(bin.height===0?4:0),y2:bin.y+bin.height,text:`[${bin.lower}, ${bin.upper})\nCount: ${bin.count===null?'Unknown':bin.count}${tooltip && (!Array.isArray(tooltip)||tooltip.length)?'\n'+tooltipRegionText(tooltip,bin.row):''}`}))
 return {svg:group([grid,group(marks,{'data-scene-key':'marks'}),axes]),accessiblePoints,tooltipRegions}
}
