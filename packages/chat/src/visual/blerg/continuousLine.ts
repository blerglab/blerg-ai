import {decimateLine} from './marks/line'
import {axisOptions,axisTickCount} from './axisOptions'
import {positionDomain,positionScale,temporalValue,type ScaleOptions} from './scaleOptions'
import {drawTemporalAxisBottom} from './temporalAxis'
import {seriesLegend,type LegendOptions} from './seriesLegend'
import {categoricalColorScale,sortedDomain,isImplementedSort,type EncodingSort} from './color'
import {continuousTicks,densityTickCount,formatTicks,drawLinearAxisBottom,drawLinearAxisLeft} from './axes'
import {plotLayoutContinuousX} from './layout'
import {group,path,line,text,recordPlotBounds} from './svg'
import {datumKey} from './dataIdentity'
import {tooltipRegionText,type TooltipFieldDef,type TooltipRegion} from './tooltip'
import {NotShippedYet} from './marks/bar'
import type {VisualTheme} from '../theme'

export interface ContinuousField {field?:string;type?:string;title?:string|null;axis?:{title?:string|null};scale?:ScaleOptions;sort?:EncodingSort;legend?:LegendOptions|null}
export interface ContinuousEncoding {x?:ContinuousField;y?:ContinuousField;color?:ContinuousField;tooltip?:TooltipFieldDef|TooltipFieldDef[]}
export interface DatumReference {axis:'x'|'y';value:number;label?:string;dx?:number;dy?:number}
export interface FieldReference {rows:Record<string,unknown>[];encoding:ContinuousEncoding}
/** Line and field-rule layers share position and color domains, but retain separate data. */
export function drawContinuousLine(rows:Record<string,unknown>[],encoding:ContinuousEncoding,mark:{opacity?:unknown},width:number,height:number,theme:VisualTheme,reference?:FieldReference,annotation?:DatumReference){
 const xf=encoding.x?.field,yf=encoding.y?.field,cf=encoding.color?.field,temporal=encoding.x?.type==='temporal'
 if(!xf || !yf || !isImplementedSort(encoding.color?.sort))throw new NotShippedYet('line: unsupported continuous encoding')
 const xv=(value:unknown)=>temporal ? temporalValue(value) : Number(value)
 const valid=(row:Record<string,unknown>,x:string,y:string)=>row[x]!=null && row[y]!=null && Number.isFinite(xv(row[x])) && Number.isFinite(Number(row[y]))
 if(rows.some(row=>!valid(row,xf,yf)))throw new NotShippedYet('line: invalid position value')
 const re=reference?.encoding,rx=re?.x?.field,ry=re?.y?.field,rc=re?.color?.field
 const refs=reference?.rows ?? []
 if(refs.length && (!rx || !ry || refs.some(row=>!valid(row,rx,ry))))throw new NotShippedYet('line rule: invalid position value')
 const domain=cf ? sortedDomain([...rows.map(row=>String(row[cf])),...refs.flatMap(row=>rc ? [String(row[rc])] : [])],encoding.color?.sort) : []
 const color=categoricalColorScale(domain,theme.palette)
 const legend=cf ? seriesLegend(domain,color,width,theme,encoding.color?.title===null ? undefined : encoding.color?.title ?? cf,encoding.color?.legend) : {right:0,top:0,svg:()=>''}
 const xd=positionDomain([...rows.map(row=>xv(row[xf])),...refs.map(row=>xv(row[rx!])),...(annotation?.axis==='x' ? [annotation.value] : [])],encoding.x?.scale,false,temporal)
 const yd=positionDomain([...rows.map(row=>Number(row[yf])),...refs.map(row=>Number(row[ry!])),...(annotation?.axis==='y' ? [annotation.value] : [])],encoding.y?.scale,true)
 const innerHeight=Math.max(1,height-legend.top),innerWidth=Math.max(1,width-legend.right)
 const yt=axisTickCount('y',densityTickCount(innerHeight-55))
 const ticks=continuousTicks(yd,yt),labels=formatTicks(ticks)
 const plot=plotLayoutContinuousX(innerWidth,innerHeight,labels,theme,encoding.x,encoding.y)
 const xs=positionScale(xd,[plot.x,plot.x+plot.width]),ys=positionScale(yd,[plot.y+plot.height,plot.y])
 const groups=cf ? domain.filter(key=>rows.some(row=>String(row[cf])===key)) : ['']
 const paths=groups.map(key=>{
  const points=rows.filter(row=>!cf || String(row[cf])===key).map(row=>({row,x:xv(row[xf]),y:Number(row[yf])})).sort((a,b)=>a.x-b.x)
  const painted=decimateLine(points,Math.max(32,Math.floor(Math.min(1024,plot.width*2)/Math.max(1,groups.length))))
  return path({'data-entrance':'line',d:'M '+painted.map(p=>`${xs(p.x)},${ys(p.y)}`).join(' L '),fill:'none',stroke:cf ? color(key) : theme.accent,'stroke-width':2,opacity:typeof mark.opacity==='number' ? mark.opacity : undefined,'data-scene-key':'series:'+datumKey({id:key}),'data-datum-key':'series:'+datumKey({id:key}),'data-vertex-keys':JSON.stringify(painted.map(p=>datumKey(p.row,xf,cf)))})
 })
 // A rule with x and y fields uses a vertical segment down to zero, or the lower
 // domain edge when zero is excluded. This is the original NVDA spec's Vega geometry.
 const base=yd[0]<=0 && yd[1]>=0 ? 0 : yd[0]
 const rules=refs.map(row=>line(xs(xv(row[rx!])),ys(Number(row[ry!])),xs(xv(row[rx!])),ys(base),{stroke:rc ? color(String(row[rc])) : theme.muted,'data-reference-rule':'field','data-scene-key':'reference:'+datumKey(row,rx,rc)}))
 const annotationAttrs={stroke:theme.muted,'data-reference-rule':annotation?.axis,'data-scene-key':'reference-rule'}
 const referenceRule=!annotation ? '' : annotation.axis==='y' ? line(plot.x,ys(annotation.value),plot.x+plot.width,ys(annotation.value),annotationAttrs) : line(xs(annotation.value),plot.y,xs(annotation.value),plot.y+plot.height,annotationAttrs)
 const label=annotation?.label ? text((annotation.axis==='y' ? plot.x+plot.width/2 : xs(annotation.value))+(annotation.dx ?? 0),(annotation.axis==='y' ? ys(annotation.value) : plot.y+plot.height/2)+(annotation.dy ?? 0),annotation.label,{fill:theme.muted,'font-size':theme.fontSize,'font-family':theme.fontFamily,'text-anchor':'middle','data-reference-label':annotation.axis,'data-scene-key':'reference-label'}) : ''
 const grid=group((axisOptions('y').grid===false ? [] : ticks).map(value=>line(plot.x,ys(value),plot.x+plot.width,ys(value),{stroke:theme.border,'stroke-opacity':0.35,'data-grid':'y'})))
 const axes=group([temporal ? drawTemporalAxisBottom(xs,plot,theme,axisOptions('x')) : drawLinearAxisBottom(xs,xd,plot,theme),drawLinearAxisLeft(ys,yd,plot,theme,yt)],{'data-scene-key':'axes'})
 const tooltipRegions:TooltipRegion[]=rows.map(row=>({markKey:'series:'+datumKey({id:cf ? String(row[cf]) : ''}),key:datumKey(row,xf,cf),x1:xs(xv(row[xf]))-8,x2:xs(xv(row[xf]))+8,y1:ys(Number(row[yf]))-8,y2:ys(Number(row[yf]))+8,text:tooltipRegionText(encoding.tooltip,row)}))
 recordPlotBounds({...plot,y:plot.y+legend.top})
 return {svg:group([group([grid,group([...rules,...paths,referenceRule,label],{'data-scene-key':'marks'}),axes],{transform:`translate(0,${legend.top})`,'data-scene-key':'line-content'}),legend.svg(plot.x+plot.width+12,plot.y)]),accessiblePoints:rows.map(row=>({key:datumKey(row,xf,cf),label:String(row[xf]),value:String(row[yf])})),tooltipRegions:tooltipRegions.map(r=>({...r,y1:r.y1+legend.top,y2:r.y2+legend.top}))}
}
