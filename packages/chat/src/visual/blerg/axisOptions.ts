import {format as numberFormat, formatSpecifier} from 'd3-format'

export interface AxisOptions {title?: string | null; format?: string; tickCount?: number; grid?: boolean; labelAngle?: 0 | -45}
export type AxisName = 'x' | 'y'
// Drawing is synchronous. Scope guides to one unit, restoring the parent even on fallback.
let onBottomOverflow: ((overflow:number)=>void) | undefined
export function recordBottomOverflow(overflow:number):void {onBottomOverflow?.(overflow)}
let guides: Partial<Record<AxisName, AxisOptions>> = {}
export function withAxisOptions<T>(encoding: {x?: {axis?: AxisOptions}; y?: {axis?: AxisOptions}}, draw: () => T, measureBottom?: (overflow:number)=>void): T {
 const previous=guides, previousMeasure=onBottomOverflow
 onBottomOverflow=measureBottom
 guides={x:encoding.x?.axis,y:encoding.y?.axis}
 try{return draw()}finally{guides=previous;onBottomOverflow=previousMeasure}
}
export function axisOptions(axis:AxisName):AxisOptions {return guides[axis] ?? {}}
export function axisTickCount(axis:AxisName, fallback:number):number {return axisOptions(axis).tickCount ?? fallback}
export function formatAxisValues(values:readonly number[], fallback:()=>string[], axis:AxisName='y'):string[] {
 const format=axisOptions(axis).format
 return format===undefined ? fallback() : values.map(numberFormat(format))
}
export function isAxisOptions(value:unknown):boolean {
 if(!value || typeof value!=='object' || Array.isArray(value))return false
 return Object.entries(value).every(([key,v])=>{
  switch(key){
   case 'title':return v===null || typeof v==='string'
   case 'format':try{
    if(typeof v!=='string' || v.length>80)return false
    const parsed=formatSpecifier(v)
    if((parsed.width ?? 0)>64 || (parsed.precision ?? 0)>20)return false
    numberFormat(v);return true
   }catch{return false}
   case 'tickCount':return typeof v==='number' && Number.isFinite(v) && v>=1 && v<=100
   case 'grid':return typeof v==='boolean'
   case 'labelAngle':return v===0 || v===-45
   default:return false
  }
 })
}
