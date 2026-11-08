import {expect,it,vi,afterEach} from 'vitest'
import {classifyChart} from './classify'
import {renderBlergChart} from './render'
import {readVisualTheme} from '../theme'
afterEach(()=>vi.unstubAllGlobals())
export const timeline = {
 title:'Worker status · UTC · simulated',mark:'rect',
 data:{values:[{id:'a',worker:'One',start:'2026-11-04T09:00:00Z',end:'2026-11-04T10:00:00Z',status:'Working'},{id:'b',worker:'One',start:'2026-11-04T11:00:00Z',end:'2026-11-04T12:00:00Z',status:'Waiting'}]},
 encoding:{x:{field:'start',type:'temporal'},x2:{field:'end',type:'temporal'},y:{field:'worker',type:'ordinal'},color:{field:'status',type:'nominal'},tooltip:[{field:'worker',type:'nominal'},{field:'status',type:'nominal'},{field:'start',type:'temporal'},{field:'end',type:'temporal'}]}
}
it('draws native keyed status intervals with gaps and UTC ticks',()=>{
 expect(classifyChart(timeline)).toBe('blerg')
 const host=document.createElement('div'),visual=renderBlergChart(timeline,readVisualTheme(host),host,390,300)
 expect(host.querySelectorAll('[data-datum-key]')).toHaveLength(2)
 const marks=[...host.querySelectorAll('[data-datum-key] rect')]
 expect(Number(marks[1].getAttribute('x'))).toBeGreaterThan(Number(marks[0].getAttribute('x'))+Number(marks[0].getAttribute('width')))
 expect(host.textContent).toContain('UTC')
 expect(host.querySelector('table')).toHaveTextContent('Waiting')
 expect(visual.svg).not.toMatch(/NaN|Infinity/)
 visual.dispose()
})
it('retains interval identity when an open interval end and status change',()=>{
 vi.stubGlobal('matchMedia',()=>({matches:true,addEventListener(){},removeEventListener(){}}))
 const host=document.createElement('div'),visual=renderBlergChart(timeline,readVisualTheme(host),host,390,300),mark=host.querySelector('[data-datum-key]')
 const next=structuredClone(timeline);next.data.values[0].end='2026-11-04T10:30:00Z';next.data.values[0].status='Waiting'
 expect(visual.applyData?.(next,1)).toBe(true)
 expect(host.querySelector('[data-datum-key]')).toBe(mark)
 expect(visual.snapshot?.().svg).toContain('Waiting')
 visual.dispose()
})
it('refuses mismatched bounds and distinguishes nine statuses without color alone',()=>{
 expect(classifyChart({...timeline,encoding:{...timeline.encoding,x2:{field:'end',type:'quantitative'}}})).toBe('fallback')
 const spec={...timeline,data:{values:Array.from({length:9},(_,i)=>({...timeline.data.values[0],id:String(i),worker:String(i),status:'State '+i}))}}
 const host=document.createElement('div'),visual=renderBlergChart(spec,readVisualTheme(host),host,390,400)
 expect(new Set([...host.querySelectorAll('[data-datum-key] rect')].map(node=>node.getAttribute('stroke-dasharray'))).size).toBe(9)
 expect(host.querySelectorAll('[data-scene-key="legend"] [data-legend-swatch]')).toHaveLength(9)
 visual.dispose()
})
