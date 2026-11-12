import {expect,it,vi} from 'vitest'
import nvda from '../../../../../runner/frontend/src/test/nvdaOriginalSpec.json'
import {classifyChart} from './classify'
import {renderBlergChart} from './render'
import {supportsNativeDataUpdates} from './liveDataSupport'
import {readVisualTheme} from '../theme'

it('renders the original NVDA snapshot natively with a top series legend and data-only updates',()=>{
 expect(classifyChart(nvda)).toBe('blerg')
 expect(supportsNativeDataUpdates(nvda)).toBe(true)
 const container=document.createElement('div')
 const rendered=renderBlergChart(nvda,readVisualTheme(container),container,390,300)
 expect(container.querySelector('[data-chart-renderer="blerg"]')).not.toBeNull()
 expect(container.querySelector('[data-scene-key="legend"]')?.textContent).toContain('Previous close')
 expect(container.querySelector('[data-scene-key="legend"]')?.textContent).toContain('NVDA')
 expect(container.querySelector('[data-axis-title="x"]')?.textContent).toBe('Time (ET)')
 expect(container.innerHTML).not.toMatch(/NaN|Infinity/)
 rendered.dispose()
})

it('keeps price movements visible with scale.zero false and exact explicit domains',()=>{
 const container=document.createElement('div')
 const spec={data:{values:[{x:1,y:185},{x:2,y:190}]},mark:'line',encoding:{x:{field:'x',type:'quantitative'},y:{field:'y',type:'quantitative',scale:{zero:false,nice:false,domain:[180,200]}}}}
 expect(classifyChart(spec)).toBe('blerg')
 const result=renderBlergChart(spec,readVisualTheme(container),container,390,300)
 const path=container.querySelector('[data-scene-key="marks"] path')!
 const coords=path.getAttribute('d')!.match(/-?\d+(?:\.\d+)?/g)!.map(Number)
 expect(Math.abs(coords[3]-coords[1])).toBeGreaterThan(40)
 result.dispose()
})

it('keeps unsupported options and combinations on Vega',()=>{
 const line={data:{values:[{x:'2026-11-05T09:30:00',y:185,series:'NVDA'}]},mark:'line',encoding:{x:{field:'x',type:'temporal'},y:{field:'y',type:'quantitative'}}}
 for(const x of [{...line.encoding.x,timeUnit:'month'},{...line.encoding.x,scale:{type:'utc'}},{...line.encoding.x,axis:{format:'%K'}},{...line.encoding.x,scale:{zero:true}}])expect(classifyChart({...line,encoding:{...line.encoding,x}})).toBe('fallback')
 for(const legend of [{orient:'left'},{orient:'top',columns:2},{labelColor:'red'}])expect(classifyChart({...line,encoding:{...line.encoding,color:{field:'series',type:'nominal',legend}}})).toBe('fallback')
 expect(classifyChart({...line,mark:'bar'})).toBe('fallback')
})

it('formats the original tooltip titles, local clock and dollar amount',async()=>{
 const {tooltipRegionText}=await import('./tooltip')
 expect(tooltipRegionText(nvda.layer[1].encoding.tooltip,nvda.layer[1].data.values[0])).toBe('Time (ET): 09:30\nPrice (USD): $195.18')
})


it('animates original NVDA data revisions on the retained surface and repaints theme tokens',()=>{
 const frames=new Map<number,FrameRequestCallback>();let id=0
 vi.spyOn(performance,'now').mockReturnValue(0)
 vi.stubGlobal('requestAnimationFrame',(fn:FrameRequestCallback)=>{frames.set(++id,fn);return id})
 vi.stubGlobal('cancelAnimationFrame',(key:number)=>frames.delete(key))
 vi.stubGlobal('matchMedia',()=>({matches:false,addEventListener:()=>{},removeEventListener:()=>{}}))
 const container=document.createElement('div'),theme=readVisualTheme(container)
 const visual=renderBlergChart(nvda,theme,container,390,300),root=container.querySelector('svg')!,mark=root.querySelector('[data-datum-key]')!,legend=root.querySelector('[data-scene-key="legend"]')
 const tick=(at:number)=>{const callbacks=[...frames.values()];frames.clear();for(const fn of callbacks)fn(at)}
 try{
  const before=mark.getAttribute('d'),next=structuredClone(nvda);next.layer[1].data.values[0].value=180
  expect(visual.applyData!(next,1)).toBe(true)
  tick(0);expect(mark.getAttribute('d')).toBe(before)
  tick(125);const middle=mark.getAttribute('d');expect(middle).not.toBe(before)
  tick(250);expect(mark.getAttribute('d')).not.toBe(middle)
  expect(container.querySelector('svg')).toBe(root);expect(root.querySelector('[data-datum-key]')).toBe(mark)
  expect(root.querySelector('[data-scene-key="legend"]')).toBe(legend)
  expect(root.getAttribute('data-update-state')).toBe('idle')
  visual.setTheme!({...theme,palette:[theme.accent,theme.muted]})
  expect(mark.getAttribute('stroke')).toBe(theme.accent)
 }finally{visual.dispose();vi.restoreAllMocks();vi.unstubAllGlobals()}
})

it('combines temporal price scales and legends with BLG-542 datum reference labels',()=>{
 const line=structuredClone(nvda.layer[1])
 const spec={data:line.data,layer:[{mark:line.mark,encoding:line.encoding},{mark:'rule',encoding:{y:{datum:200.13}}},{mark:{type:'text',dy:-10},encoding:{y:{datum:200.13},text:{value:'Previous close'}}}]}
 expect(classifyChart(spec)).toBe('blerg')
 const host=document.createElement('div'),visual=renderBlergChart(spec,readVisualTheme(host),host,390,300)
 expect(host.querySelector('[data-reference-rule="y"]')).not.toBeNull()
 expect(host.querySelector('[data-reference-label="y"]')?.textContent).toBe('Previous close')
 expect(host.innerHTML).not.toMatch(/NaN|Infinity/)
 visual.dispose()
})


it('keeps a labeled horizontal demo reference native and live',()=>{
 const line=structuredClone(nvda.layer[1]),value=nvda.layer[0].data.values[0].value
 const corrected={...nvda,layer:[{data:{values:[{}]},mark:{type:'rule'},encoding:{y:{datum:value}}},line,{data:{values:[{}]},mark:{type:'text',dy:-10},encoding:{y:{datum:value},text:{value:'Previous close'}}}]}
 expect(classifyChart(corrected)).toBe('blerg')
 expect(supportsNativeDataUpdates(corrected)).toBe(true)
 const host=document.createElement('div'),visual=renderBlergChart(corrected,readVisualTheme(host),host,390,500)
 try{
  const rule=host.querySelector('[data-reference-rule="y"]')!
  expect(rule).not.toBeNull();expect(rule.getAttribute('y1')).toBe(rule.getAttribute('y2'))
  expect(Number(rule.getAttribute('x2'))).toBeGreaterThan(Number(rule.getAttribute('x1')))
  const next=structuredClone(corrected);const revised=structuredClone(line);revised.data.values[0].value=180;next.layer[1]=revised
  expect(visual.applyData!(next,1)).toBe(true)
 }finally{visual.dispose()}
})

it('the visible NVDA fixture authors a horizontal previous-close rule',async()=>{
 const {default:demo}=await import('../../../../../runner/frontend/src/test/nvdaSpec.json')
 expect(demo.layer[0].encoding).toEqual({y:{datum:200.13}})
 const host=document.createElement('div'),visual=renderBlergChart(demo,readVisualTheme(host),host,390,500)
 try{expect(host.querySelectorAll('[data-reference-rule="y"]')).toHaveLength(1)}finally{visual.dispose()}
})
