import {expect,it,vi,afterEach} from 'vitest'
import {classifyChart} from './classify'
import {renderBlergChart} from './render'
import {readVisualTheme} from '../theme'
afterEach(()=>vi.unstubAllGlobals())
const histogram={mark:'bar',title:'Gate durations (s) · count · simulated',data:{values:[{lower:0,upper:10,count:2},{lower:10,upper:30,count:4},{lower:30,upper:40,count:0}]},encoding:{x:{field:'lower',type:'quantitative',bin:'binned'},x2:{field:'upper',type:'quantitative'},y:{field:'count',type:'quantitative'},tooltip:[{field:'lower',type:'quantitative'},{field:'upper',type:'quantitative'},{field:'count',type:'quantitative'}]}}
it('renders precomputed bins with their actual widths and zero-based counts natively',()=>{
 expect(classifyChart(histogram)).toBe('blerg')
 const host=document.createElement('div'),visual=renderBlergChart(histogram,readVisualTheme(host),host,390,300),marks=[...host.querySelectorAll('[data-datum-key]')]
 expect(marks).toHaveLength(3)
 expect(Number(marks[1].getAttribute('width'))).toBeCloseTo(2*Number(marks[0].getAttribute('width')),5)
 expect(Number(marks[1].getAttribute('height'))).toBeCloseTo(2*Number(marks[0].getAttribute('height')),5)
 expect(Number(marks[2].getAttribute('height'))).toBe(0)
 expect(host.querySelector('table')).toHaveTextContent('30')
 expect(visual.svg).not.toMatch(/NaN|Infinity/)
 visual.dispose()
})
it('retains bin identity across reordering and count corrections, exporting current counts',()=>{
 vi.stubGlobal('matchMedia',()=>({matches:true,addEventListener(){},removeEventListener(){}}))
 const host=document.createElement('div'),visual=renderBlergChart(histogram,readVisualTheme(host),host,390,300),mark=host.querySelector('[data-datum-key]')!,key=mark.getAttribute('data-datum-key')
 const next={...histogram,data:{values:[...histogram.data.values].reverse().map(row=>({...row,count:row.count+1}))}}
 expect(visual.applyData?.(next,1)).toBe(true)
 expect(host.querySelector(`[data-datum-key='${key}']`)).toBe(mark)
 const fresh=document.createElement('div'),target=renderBlergChart(next,readVisualTheme(host),fresh,390,300)
 const exported=document.createElement('div');exported.innerHTML=visual.snapshot!().svg
 const shapes=(root:Element)=>[...root.querySelectorAll('[data-datum-key]')].map(node=>Object.fromEntries(['data-datum-key','x','y','width','height','fill'].map(name=>[name,node.getAttribute(name)])))
 expect(shapes(exported)).toEqual(shapes(fresh))
 visual.dispose();target.dispose()
})
it('refuses overlapping bins, negative counts and unsupported aggregate or bin requests',()=>{
 for(const values of [[{lower:0,upper:10,count:1},{lower:5,upper:15,count:2}],[{lower:0,upper:10,count:-1}]]){
  const host=document.createElement('div')
  expect(()=>renderBlergChart({...histogram,data:{values}},readVisualTheme(host),host,390,300)).toThrow(/histogram:/)
 }
 expect(classifyChart({...histogram,encoding:{...histogram.encoding,x:{...histogram.encoding.x,bin:true}}})).toBe('fallback')
 expect(classifyChart({...histogram,encoding:{...histogram.encoding,y:{...histogram.encoding.y,aggregate:'sum'}}})).toBe('fallback')
})
