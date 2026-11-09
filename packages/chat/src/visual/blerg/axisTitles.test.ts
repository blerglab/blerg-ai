import {expect,it,vi} from 'vitest'
import {classifyChart} from './classify'
import {renderBlergChart} from './render'
import {readVisualTheme} from '../theme'
const spec={mark:'line',data:{values:[{second:0,latency:2},{second:30,latency:4}]},encoding:{x:{field:'second',type:'quantitative',axis:{title:'Elapsed time (s)'}},y:{field:'latency',type:'quantitative',axis:{title:'Latency (ms)'}}}}
for(const width of [390,3840])it(`keeps authored units native and retained on data updates at ${width}`,()=>{
 expect(classifyChart(spec)).toBe('blerg')
 const host=document.createElement('div'),theme=readVisualTheme(host),visual=renderBlergChart(spec,theme,host,width,500)
 const root=host.querySelector('svg'),titles=[...host.querySelectorAll('[data-axis-title]')]
 expect(titles.map(t=>t.textContent)).toEqual(['Elapsed time (s)','Latency (ms)'])
 for(const title of titles){expect(title.getAttribute('fill')).toBe(theme.fg);expect(title).toHaveAttribute('font-weight','bold')}
 const next={...spec,data:{values:[{second:0,latency:3},{second:30,latency:5}]}}
 expect(visual.applyData!(next,1)).toBe(true)
 expect(host.querySelector('svg')).toBe(root)
 expect([...host.querySelectorAll('[data-axis-title]')]).toEqual(titles)
 visual.dispose()
})
it('keeps other axis options on the fallback',()=>{
 for(const axis of [{title:'Units',labelAngle:45},{title:'Units',grid:'false'},null])expect(classifyChart({...spec,encoding:{...spec.encoding,x:{...spec.encoding.x,axis}}})).toBe('fallback')
})
it('classifies large immutable models without serializing them on each call',()=>{
 const model=Object.freeze({...spec,data:{values:Array.from({length:10000},(_,i)=>({second:i,latency:2,note:'padding'.repeat(5)}))}})
 const stringify=vi.spyOn(JSON,'stringify')
 try{expect(classifyChart(model)).toBe('blerg');expect(classifyChart(model)).toBe('blerg');expect(stringify).not.toHaveBeenCalled()}finally{stringify.mockRestore()}
})

it('rechecks a shallow-frozen caller model when its nested encoding changes',()=>{
 const model=Object.freeze({...spec,encoding:structuredClone(spec.encoding)})
 expect(classifyChart(model)).toBe('blerg')
 Object.assign(model.encoding.x.axis,{labelAngle:45})
 expect(classifyChart(model)).toBe('fallback')
})
it('draws shared layer titles once and rejects conflicting authored guides',()=>{
 const layer={data:spec.data,layer:[{mark:'line',encoding:spec.encoding},{mark:'rule',encoding:{y:{datum:3}}}]}
 expect(classifyChart(layer)).toBe('blerg')
 const host=document.createElement('div'),visual=renderBlergChart(layer,readVisualTheme(host),host,390,500)
 expect(host.querySelectorAll('[data-axis-title]')).toHaveLength(2)
 visual.dispose()
 expect(classifyChart({...layer,layer:[{mark:'line',encoding:spec.encoding},{mark:'rule',encoding:{y:{...spec.encoding.y,axis:{title:'Other units'}}}}]})).toBe('fallback')
})
