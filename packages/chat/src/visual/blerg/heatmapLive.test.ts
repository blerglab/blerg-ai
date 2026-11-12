import {expect, it, vi, afterEach} from 'vitest'
import {renderBlergChart} from './render'
import {readVisualTheme} from '../theme'
afterEach(()=>vi.unstubAllGlobals())
it('retains heatmap cells by both category coordinates across reordered data revisions',()=>{
 vi.stubGlobal('matchMedia',()=>({matches:true,addEventListener(){},removeEventListener(){}}))
 const spec={mark:'rect',data:{values:[{day:'Monday',hour:'09',count:1},{day:'Tuesday',hour:'09',count:3},{day:'Monday',hour:'10',count:2}]},encoding:{x:{field:'day',type:'ordinal'},y:{field:'hour',type:'ordinal'},color:{field:'count',type:'quantitative'},tooltip:[{field:'count',type:'quantitative'}]}}
 const host=document.createElement('div'),visual=renderBlergChart(spec,readVisualTheme(host),host,390,300)
 const root=host.querySelector('svg'),cells=[...host.querySelectorAll('[data-datum-key]')]
 expect(cells).toHaveLength(3)
 const axes=host.querySelector('[data-scene-key="axes"]'),legend=host.querySelector('[data-scene-key="legend"]')
 expect(visual.applyData?.({...spec,data:{values:[...spec.data.values].reverse().map(row=>({...row,count:row.count+4}))}},1)).toBe(true)
 expect(host.querySelector('svg')).toBe(root)
 for(const cell of cells)expect(host.querySelector(`[data-datum-key='${cell.getAttribute('data-datum-key')}']`)).toBe(cell)
 expect(host.querySelector('[data-scene-key="axes"]')).toBe(axes)
 expect(host.querySelector('[data-scene-key="legend"]')).toBe(legend)
 expect(visual.snapshot?.().svg).not.toMatch(/NaN|Infinity/)
 visual.dispose()
})
it('distinguishes unknown heatmap values from measured zero without invalid geometry',()=>{
 const host=document.createElement('div')
 const visual=renderBlergChart({mark:'rect',data:{values:[{x:'a',y:'a',n:null},{x:'b',y:'a',n:0}]},encoding:{x:{field:'x',type:'nominal'},y:{field:'y',type:'nominal'},color:{field:'n',type:'quantitative'}}},readVisualTheme(host),host,390,300)
 expect(host.querySelector('[data-value-state="unknown"]')).toBeTruthy()
 expect(host.textContent).toContain('Unknown')
 expect(visual.svg).not.toMatch(/NaN|Infinity/)
 visual.dispose()
})
for(const reduced of [false,true])it(`updates heatmap value colors with reduced motion=${reduced}`,()=>{
 const frames=new Map<number,FrameRequestCallback>();let nextId=0
 vi.stubGlobal('requestAnimationFrame',(callback:FrameRequestCallback)=>{frames.set(++nextId,callback);return nextId})
 vi.stubGlobal('cancelAnimationFrame',(id:number)=>frames.delete(id))
 vi.stubGlobal('matchMedia',()=>({matches:reduced,addEventListener(){},removeEventListener(){}}))
 vi.spyOn(performance,'now').mockReturnValue(0)
 const spec={mark:'rect',data:{values:[{x:'a',y:'a',n:0},{x:'b',y:'a',n:2},{x:'c',y:'a',n:8}]},encoding:{x:{field:'x',type:'nominal'},y:{field:'y',type:'nominal'},color:{field:'n',type:'quantitative'}}}
 const host=document.createElement('div'),visual=renderBlergChart(spec,readVisualTheme(host),host,390,300),cell=host.querySelectorAll('[data-datum-key]')[1],before=cell.getAttribute('fill')
 const next=structuredClone(spec);next.data.values[1].n=6
 expect(visual.applyData?.(next,1)).toBe(true)
 const tick=(time:number)=>{const callbacks=[...frames.values()];frames.clear();callbacks.forEach(callback=>callback(time))}
 if(reduced){expect(frames.size).toBe(0);expect(cell.getAttribute('fill')).not.toBe(before)}
 else{
  expect(frames.size).toBe(1);expect(cell.getAttribute('fill')).toBe(before)
  tick(125);const middle=cell.getAttribute('fill');expect(middle).not.toBe(before)
  tick(250);expect(cell.getAttribute('fill')).not.toBe(middle)
 }
 visual.dispose();expect(frames.size).toBe(0)
 vi.restoreAllMocks()
})
it('does not invent a numeric color domain when every heatmap value is unknown',()=>{
 const host=document.createElement('div'),visual=renderBlergChart({mark:'rect',data:{values:[{x:'A',y:'B',n:null}]},encoding:{x:{field:'x',type:'nominal'},y:{field:'y',type:'nominal'},color:{field:'n',type:'quantitative'}}},readVisualTheme(host),host,390,300)
 const legend=host.querySelector('[data-scene-key="legend"]')!
 expect(legend).toHaveTextContent('Unknown')
 expect(legend.textContent).not.toMatch(/\b0\b/)
 visual.dispose()
})
