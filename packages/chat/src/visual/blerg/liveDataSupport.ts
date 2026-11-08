import {usesContinuousLine,isFieldRuleLayer} from './continuousSpec'
import {classifyChart} from './classify'

export function nativeDataMark(model: Record<string,unknown>): string | undefined {
 if(Array.isArray(model.layer)){
  const marks=(model.layer as Record<string,unknown>[]).map(unit=>typeof unit.mark==='string'?unit.mark:(unit.mark as {type?:string})?.type).sort().join('+')
  return (marks==='line+rule'||marks==='line+rule+text')?'line':(marks==='area+rule'||marks==='area+rule+text')?'area':undefined
 }
 const mark=typeof model.mark==='string'?model.mark:(model.mark as {type?:string})?.type
 return ['bar','line','area','point','circle','square','rect'].includes(mark ?? '')?mark:undefined
}
export function supportsNativeDataUpdates(model:Record<string,unknown>):boolean {
 if(classifyChart(model)!=='blerg' || nativeDataMark(model)===undefined)return false
 const primary=Array.isArray(model.layer)?(model.layer as Record<string,unknown>[]).find(unit=>['line','area'].includes(String(typeof unit.mark==='string'?unit.mark:(unit.mark as {type?:string})?.type))):undefined
 const encoding={...(model.encoding as object),...(primary?.encoding as object)} as {x?:{field?:unknown};y?:{field?:unknown};color?:unknown}
 if(typeof encoding.x?.field!=='string'||typeof encoding.y?.field!=='string')return false
 if(primary && encoding.color && !usesContinuousLine(encoding) && !isFieldRuleLayer(model.layer as unknown[],(model.encoding ?? {}) as Record<string,unknown>))return false
 return true
}
