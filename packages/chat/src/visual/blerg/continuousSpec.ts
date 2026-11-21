import {isScaleOptions} from './scaleOptions'
import {isLegendOptions} from './seriesLegend'
import {isAxisOptions} from './axisOptions'
import {isImplementedSort} from './color'
import {format as numberFormat,formatSpecifier} from 'd3-format'

const object=(value:unknown):value is Record<string,unknown>=>!!value && typeof value==='object' && !Array.isArray(value)
export function isTemporalFormat(value:unknown):value is string {
 return typeof value==='string' && value.length<=80 && !/%(?:[-_0])?[^aAbBcdeHIjmMpqQsSuUVwWxXyYZfLgG%]/.test(value) && !/%[-_0]?$/.test(value)
}
function numericFormat(value:unknown):boolean {
 try{if(typeof value!=='string' || value.length>80)return false;const spec=formatSpecifier(value);return (spec.width ?? 0)<=64 && (spec.precision ?? 0)<=20 && !!numberFormat(value)}catch{return false}
}
export function continuousAxis(value:unknown,temporal:boolean):boolean {
 if(!object(value))return false
 const {format,...rest}=value
 return isAxisOptions(rest) && (format===undefined || (temporal ? isTemporalFormat(format) : numericFormat(format)))
}
/** Admission is shared with drawing: only unbinned continuous lines consume these options. */
export function usesContinuousLine(encoding:Record<string,unknown>):boolean {
 return Object.values(encoding).some(value=>Array.isArray(value) ? value.some(v=>object(v) && ('title' in v || 'format' in v)) : object(value) && (value.type==='temporal' || ['scale','legend','title','format'].some(key=>key in value)))
}
export function isContinuousEncoding(encoding:Record<string,unknown>):boolean {
 if(!object(encoding.x) || !object(encoding.y))return false
 return Object.entries(encoding).every(([channel,value])=>{
  if(!['x','y','color','tooltip'].includes(channel))return false
  const defs=Array.isArray(value) && channel==='tooltip' ? value : [value]
  return defs.every(def=>{
   if(!object(def) || typeof def.field!=='string')return false
   const temporal=def.type==='temporal'
   if(channel==='x' && !['quantitative','temporal'].includes(String(def.type)))return false
   if(channel==='y' && def.type!=='quantitative')return false
   if(channel==='color' && !['nominal','ordinal'].includes(String(def.type)))return false
   if(channel==='tooltip' && !['quantitative','temporal','nominal','ordinal'].includes(String(def.type)))return false
   return Object.entries(def).every(([key,v])=>{
    if(key==='field'||key==='type')return true
    if(key==='title')return v===null || typeof v==='string'
    if(key==='scale')return (channel==='x'||channel==='y') && isScaleOptions(v,temporal)
    if(key==='legend')return channel==='color' && isLegendOptions(v)
    if(key==='sort')return channel==='color' && isImplementedSort(v)
    if(key==='axis')return (channel==='x'||channel==='y') && continuousAxis(v,temporal)
    if(key==='format')return channel==='tooltip' && (temporal ? isTemporalFormat(v) : def.type==='quantitative' && numericFormat(v))
    return false
   })
  })
 })
}
export function continuousGuides<T extends {x?:{field?:string;title?:string|null;axis?:{title?:string|null}};y?:{field?:string;title?:string|null;axis?:{title?:string|null}}}>(encoding:T):T {
 const result={...encoding}
 for(const axis of ['x','y'] as const){const def=encoding[axis];if(def)result[axis]={...def,axis:{title:def.title ?? (def.title===null ? null : def.field),...def.axis}}}
 return result
}

/** The bounded field-rule overlay used by the published NVDA snapshot. */
export function isFieldRuleLayer(children:unknown[],inherited:Record<string,unknown>):boolean {
 const kind=(child:Record<string,unknown>)=>typeof child.mark==='string' ? child.mark : object(child.mark) ? child.mark.type : undefined
 if(children.length!==2 || !children.every(object))return false
 const units=children as Record<string,unknown>[],line=units.find(c=>kind(c)==='line'),rule=units.find(c=>kind(c)==='rule')
 if(!line || !rule)return false
 if(object(line.mark) && Object.keys(line.mark).some(k=>!['type','opacity'].includes(k)))return false
 if(object(rule.mark) && Object.keys(rule.mark).some(k=>k!=='type'))return false
 const le={...inherited,...(object(line.encoding)?line.encoding:{})},re={...inherited,...(object(rule.encoding)?rule.encoding:{})}
 if(!isContinuousEncoding(le) || !isContinuousEncoding(re))return false
 if(!Object.keys(re).every(k=>['x','y','color'].includes(k)))return false
 for(const axis of ['x','y']){
  const ld=le[axis] as Record<string,unknown>,rd=re[axis] as Record<string,unknown>
  if(rd.type!==ld.type || Object.keys(rd).some(k=>!['field','type'].includes(k)))return false
 }
 if(Boolean(le.color)!==Boolean(re.color))return false
 if(le.color && JSON.stringify(le.color)!==JSON.stringify(re.color))return false
 return true
}
