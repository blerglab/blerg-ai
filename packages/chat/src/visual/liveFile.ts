import {BoundedCache} from './boundedCache'
import {parseVegaLite} from './policy'
import {parseDashboard} from './dashboard'
import {supportsNativeDataUpdates} from './blerg/liveDataSupport'
import type {VisualKind} from './policy'

export type LiveFileSupport='native'|'unsupported-live'|'file'
/** Native data-update capability only. Unsupported charts still follow file revisions via staged rendering. */
const supports=new BoundedCache<LiveFileSupport>(512 << 10,16)
export function liveFileSupport(kind:VisualKind|null,source:string):LiveFileSupport {
 const key=kind+'\n'+source,cached=supports.get(key)
 if(cached)return cached
 const result=computeLiveFileSupport(kind,source)
 supports.set(key,result,16)
 return result
}
function computeLiveFileSupport(kind:VisualKind|null,source:string):LiveFileSupport {
 try {
  if(kind==='vega-lite')return supportsNativeDataUpdates(parseVegaLite(source))?'native':'unsupported-live'
  if(kind==='dashboard'){
   const dashboard=parseDashboard(source)
   return dashboard.cards.every(card=>card.kind==='chart'
    ? card.data.kind==='inline' && supportsNativeDataUpdates({...card.spec,data:{values:card.data.rows}})
    : !card.view?.sparkline)?'native':'unsupported-live'
  }
  return 'file'
 }catch{return 'unsupported-live'}
}
