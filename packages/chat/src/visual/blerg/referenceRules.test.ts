import {expect,it} from 'vitest'
import {supportsNativeDataUpdates} from './liveDataSupport'
import {classifyChart} from './classify'
import {renderBlergChart} from './render'
import {readVisualTheme} from '../theme'

export const referenceSpec = {
 data:{values:[{minute:0,price:182},{minute:30,price:186},{minute:60,price:184}]},
 layer:[{mark:'line',encoding:{x:{field:'minute',type:'quantitative',axis:{title:'Minutes',format:'.0f',tickCount:4,grid:true,labelAngle:-45}},y:{field:'price',type:'quantitative',axis:{title:'Price (USD)',format:'$.2f',tickCount:4,grid:false,labelAngle:0}}}},
 {mark:'rule',encoding:{y:{datum:183}}},{mark:'text',encoding:{y:{datum:183},text:{value:'Previous close'}}}],
}
it('draws labeled references and authored axes natively, retaining data motion',()=>{
 expect(classifyChart(referenceSpec)).toBe('blerg')
 expect(supportsNativeDataUpdates(referenceSpec)).toBe(true)
 const host=document.createElement('div'),theme=readVisualTheme(host)
 const visual=renderBlergChart(referenceSpec,theme,host,390,400)
 expect(host.querySelector('[data-reference-label]')?.textContent).toBe('Previous close')
 expect(host.querySelector('[data-reference-rule]')?.getAttribute('stroke')).toBe(theme.muted)
 expect(host.querySelector('[data-axis="y"]')?.textContent).toContain('$')
 const root=host.querySelector('svg'),rule=host.querySelector('[data-reference-rule]')
 expect(visual.applyData!({...referenceSpec,data:{values:[{minute:0,price:181},{minute:30,price:187},{minute:60,price:185}]}},1)).toBe(true)
 expect(host.querySelector('svg')).toBe(root)
 expect(host.querySelector('[data-reference-rule]')).toBe(rule)
 expect(root?.dataset.updateState).toBe('animating')
 visual.dispose()
})
it('keeps unsupported guide keys and angles on Vega',()=>{
 for(const axis of [{labelAngle:45},{tickCount:0},{tickCount:Infinity},{labelExpr:'datum.value'},{formatType:'custom'}]){
 const spec=structuredClone(referenceSpec)
 Object.assign(spec.layer[0].encoding.x!.axis!,axis)
 expect(classifyChart(spec)).toBe('fallback')
 }
})
it('draws standard Vega-Lite text layers labeling either reference orientation',()=>{
 for(const axis of ['x','y']){
 const datum=axis==='x'?30:183
 const spec={...referenceSpec,layer:[referenceSpec.layer[0],{mark:'rule',encoding:{[axis]:{datum}}},{mark:'text',encoding:{[axis]:{datum},text:{value:'Reference'}}}]}
 expect(classifyChart(spec)).toBe('blerg')
 const host=document.createElement('div'),visual=renderBlergChart(spec,readVisualTheme(host),host,390,400)
 expect(host.querySelector('[data-reference-label]')?.textContent).toBe('Reference')
 expect(visual.applyData!({...spec,data:{values:[{minute:0,price:183},{minute:30,price:188},{minute:60,price:185}]}},1)).toBe(true)
 visual.dispose()
 }
})
it('keeps a reference label offset clear of its rule',()=>{
 const spec={...referenceSpec,layer:[...referenceSpec.layer.slice(0,2),{mark:{type:'text',dy:-8},encoding:{y:{datum:183},text:{value:'Previous close'}}}]}
 expect(classifyChart(spec)).toBe('blerg')
 const host=document.createElement('div'),visual=renderBlergChart(spec,readVisualTheme(host),host,390,400)
 expect(Number(host.querySelector('[data-reference-label]')?.getAttribute('y'))).toBe(Number(host.querySelector('[data-reference-rule]')?.getAttribute('y1'))-8)
 visual.dispose()
})
it('bounds numeric formats and preserves fallback for specialized guides',()=>{
 const base=referenceSpec.layer[0]
 for(const format of ['1000000000f','.1000000f','invalid'])expect(classifyChart({...base,data:referenceSpec.data,encoding:{...base.encoding,y:{...base.encoding.y,axis:{format}}}})).toBe('fallback')
 const rect={mark:'rect',data:{values:[{x:'a',y:'b',n:1}]},encoding:{x:{field:'x',type:'nominal',axis:{grid:false}},y:{field:'y',type:'nominal'},color:{field:'n',type:'quantitative'}}}
 expect(classifyChart(rect)).toBe('fallback')
})
