import {expect,it,vi} from 'vitest'
import {renderBlergChart} from './render'
import {readVisualTheme} from '../theme'
import {drawStatusTimeline} from './marks/timeline'
import {drawHistogram} from './marks/histogram'
import {tooltipFieldValue} from './tooltip'

const encoding={x:{field:'start',type:'temporal'},x2:{field:'end',type:'temporal'},y:{field:'worker',type:'ordinal'},color:{field:'status',type:'nominal'}}
const rows=[{worker:'One',status:'Working',start:'2026-11-04T09:00:01Z',end:'2026-11-04T09:00:46Z',open:true}]
for(const width of [390,1024,1920,3840])it(`unique UTC second labels and both endpoints at ${width}`,()=>{
 const host=document.createElement('div'),theme=readVisualTheme(host)
 host.innerHTML=drawStatusTimeline(rows,encoding,width,300,theme).svg
 const ticks=[...host.querySelectorAll('[data-scene-key^="tick:"] text')].map(n=>n.textContent)
 expect(new Set(ticks).size).toBe(ticks.length)
 expect(ticks[0]).toContain('09:00:01');expect(ticks.at(-1)).toContain('09:00:46')
 expect(host.textContent).toContain('open')
 expect(host.querySelector('[data-open-interval]')).toBeTruthy()
})
it('formats temporal tooltip fields in UTC with date, seconds and zone',()=>{
 vi.stubEnv('TZ','America/Los_Angeles')
 try{expect(tooltipFieldValue({field:'start',type:'temporal'},'2026-11-04T01:00:01-08:00')).toBe('2026-11-04 09:00:01 UTC')}finally{vi.unstubAllEnvs()}
})
it('shows null histogram counts as unknown, interval notation and a zero hit region at the baseline',()=>{
 const host=document.createElement('div'),theme=readVisualTheme(host)
 const result=drawHistogram([{lower:0,upper:10,count:0},{lower:10,upper:20,count:null}],{x:{field:'lower',type:'quantitative',bin:'binned'},x2:{field:'upper',type:'quantitative'},y:{field:'count',type:'quantitative'},tooltip:[{field:'lower'},{field:'upper'},{field:'count'}]},{},390,300,theme)
 expect(result.accessiblePoints[0].label).toBe('[0, 10)');expect(result.accessiblePoints[1].value).toBe('Unknown')
 expect(result.tooltipRegions[0].text).toBe('[0, 10)\nCount: 0')
 expect(result.tooltipRegions[0].y2-result.tooltipRegions[0].y1).toBe(4)
 expect(result.tooltipRegions[1].text).toContain('Unknown')
})
it('uses the title for the root chart accessible name and caps wide label scale',()=>{
 const host=document.createElement('div'),theme=readVisualTheme(host)
 const visual=renderBlergChart({mark:'rect',title:'Status of workers',data:{values:rows},encoding},theme,host,3840,1000)
 expect(host.querySelector('svg')).toHaveAttribute('aria-label','Status of workers')
 const sizes=[...host.querySelectorAll('[data-scene-key="axes"] text')].map(n=>Number(n.getAttribute('font-size')))
 expect(Math.max(...sizes)).toBeLessThanOrEqual(theme.fontSize*1.2)
 visual.dispose()
})

it('uses integer heatmap legend ticks for integer observations and fractional ticks for fractional values',()=>{
 for(const fractional of [false,true]){
  const host=document.createElement('div'),theme=readVisualTheme(host)
  const spec={mark:'rect',data:{values:[0,1,2].map((v)=>({x:String(v),y:'One',count:fractional?v/2:v}))},encoding:{x:{field:'x',type:'nominal'},y:{field:'y',type:'nominal'},color:{field:'count',type:'quantitative'}}}
  const visual=renderBlergChart(spec,theme,host,390,300)
  const labels=[...host.querySelectorAll('[data-scene-key="legend"] text')].map(n=>Number(n.textContent)).filter(Number.isFinite)
  expect(labels.length).toBeGreaterThan(1)
  expect(labels.every(Number.isInteger)).toBe(!fractional)
  visual.dispose()
 }
})

it('preserves endpoint seconds across a multi-hour domain and emits one tick for an instant',()=>{
 const host=document.createElement('div'),theme=readVisualTheme(host)
 host.innerHTML=drawStatusTimeline([{...rows[0],start:'2026-11-04T09:00:07Z',end:'2026-11-04T12:00:09Z'}],encoding,390,300,theme).svg
 const ticks=()=>[...host.querySelectorAll('[data-scene-key^="tick:"] text')].map(node=>node.textContent)
 expect(ticks()[0]).toBe('09:00:07Z');expect(ticks().at(-1)).toBe('12:00:09Z')
 host.innerHTML=drawStatusTimeline([{...rows[0],start:'2026-11-04T09:00:07Z',end:'2026-11-04T09:00:07Z'}],encoding,390,300,theme).svg
 expect(ticks()).toEqual(['09:00:07Z'])
})
it('derives the histogram y title from its field and honors an explicit units title',()=>{
 const host=document.createElement('div'),theme=readVisualTheme(host)
 const encoding={x:{field:'start',type:'quantitative',bin:'binned'},x2:{field:'end',type:'quantitative'},y:{field:'gate_count',type:'quantitative'}}
 const spec={mark:'bar',data:{values:[{start:0,end:10,gate_count:2},{start:10,end:20,gate_count:null}]},encoding}
 const chart=renderBlergChart(spec,theme,host,390,300)
 expect(host.querySelector('svg')).toHaveTextContent('Gate count')
 expect(host.querySelector('table')).toHaveTextContent('[10, 20)')
 chart.dispose()
 const titled=renderBlergChart({...spec,encoding:{...encoding,y:{...encoding.y,axis:{title:'Completed gates (count)'}}}},theme,host,390,300)
 expect(host.querySelectorAll('[data-axis-title="y"]')).toHaveLength(1)
 expect(host.querySelector('[data-axis-title="y"]')).toHaveTextContent('Completed gates (count)')
 titled.dispose()
})
