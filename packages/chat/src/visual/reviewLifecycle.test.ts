import {expect, it, vi} from 'vitest'
import {measureText} from './blerg/layout'
import {renderBlergChart} from './blerg/render'
import {renderVisual} from './render'
import {readVisualTheme} from './theme'
import {attachTooltip} from './blerg/tooltip'
import {rememberSurface, transferSurface} from './surfaceSnapshots'
const spec = {width: 640, height: 360, title: 'A heading that wraps into many readable lines in a very narrow pane', data: {values: [{x: 'A', n: 1}]}, mark: 'bar', encoding: {x: {field: 'x', type: 'nominal'}, y: {field: 'n', type: 'quantitative'}}}
it('keeps dashboard stretch text at its resolved font size', async () => {
 const host=document.createElement('div'), theme=readVisualTheme(host)
 const visual=await renderVisual('vega-lite',JSON.stringify(spec),host,theme,undefined,{available:{width:1800,height:400},fill:true,stretch:true})
 expect(Number(host.querySelector('[data-scene-key="heading"] text')!.getAttribute('font-size'))).toBe(theme.fontSize+1)
 visual.dispose()
})
it('reserves a usable plot below a wrapped narrow title', () => {
 const host=document.createElement('div'); const visual=renderBlergChart(spec,readVisualTheme(host),host,180,100)
 expect(visual.height).toBeGreaterThan(100)
 const bounds=JSON.parse(host.querySelector('[data-plot-bounds]')!.getAttribute('data-plot-bounds')!)
 expect(bounds[3]).toBeGreaterThan(30)
 visual.dispose()
})
it('measures identical glyph strings only once per font and weight', () => {
 const theme={...readVisualTheme(document.body),fontFamily:'Review font'}
 const measure=vi.fn(()=>42)
 Object.defineProperty(SVGElement.prototype,'getComputedTextLength',{configurable:true,value:measure})
 expect(measureText(theme,'cached metric')).toBe(42);expect(measureText(theme,'cached metric')).toBe(42)
 expect(measure).toHaveBeenCalledTimes(1)
 measureText(theme,'cached metric','bold');expect(measure).toHaveBeenCalledTimes(2)
 delete (SVGElement.prototype as unknown as {getComputedTextLength?:unknown}).getComputedTextLength
})
it('renames every snapshot id and its gradient references',()=>{
 const host=document.createElement('div');host.innerHTML='<svg id="scope"><defs><linearGradient id="gradient"><stop offset="0"/></linearGradient></defs><rect fill="url(#gradient)"/></svg>'
 rememberSurface('gradient-review',host,100,100)
 const a=transferSurface('gradient-review')!, b=transferSurface('gradient-review')!
 expect(a.querySelector('linearGradient')!.id).not.toBe('gradient')
 expect(a.querySelector('linearGradient')!.id).not.toBe(b.querySelector('linearGradient')!.id)
 expect(a.querySelector('rect')!.getAttribute('fill')).toBe(`url(#${a.querySelector('linearGradient')!.id})`)
})
it('hit-tests a uniformly scaled SVG in scene coordinates',()=>{
 const host=document.createElement('div');host.innerHTML='<svg width="200" height="100"></svg>';const root=host.firstElementChild as SVGSVGElement
 vi.spyOn(root,'getBoundingClientRect').mockReturnValue({left:10,top:20,width:100,height:50} as DOMRect)
 const t=attachTooltip(root,host,[{x1:140,y1:40,x2:160,y2:60,text:'target'}])
 root.dispatchEvent(new MouseEvent('pointermove',{clientX:85,clientY:45}))
 expect(t.tooltip.hidden).toBe(false);t.dispose()
})

it('bounds retained values by source plus decoded bytes', async () => {
 const {BoundedCache}=await import('./boundedCache')
 const cache=new BoundedCache<string>(24)
 cache.set('a','first',16);cache.set('b','second',16)
 expect(cache.get('a')).toBeUndefined();expect(cache.get('b')).toBe('second')
 cache.set('oversized','large',24);expect(cache.get('oversized')).toBeUndefined()
})
it('snapshots the current provider lazily at transfer', async()=>{
 const {offerSurface}=await import('./surfaceSnapshots')
 const capture=vi.fn(()=>{const host=document.createElement('div');host.innerHTML='<svg width="100" height="100"><rect width="20" height="20"/></svg>';rememberSurface('lazy-review',host,100,100)})
 const release=offerSurface('lazy-review',capture)
 expect(capture).not.toHaveBeenCalled();expect(transferSurface('lazy-review')).toBeTruthy();expect(capture).toHaveBeenCalledOnce()
 release();transferSurface('lazy-review');expect(capture).toHaveBeenCalledOnce()
})

it('keeps every legend row inside a narrow wrapped chart', () => {
 const host=document.createElement('div'), theme=readVisualTheme(host)
 const model={...spec,data:{values:Array.from({length:8},(_,i)=>({x:`Team ${i}`,n:i+1,series:`Series ${i}`}))},encoding:{...spec.encoding,color:{field:'series',type:'nominal'}}}
 const visual=renderBlergChart(model,theme,host,240,130)
 const titleHeight=Number(host.querySelector('[data-scene-key="plot-body"]')!.getAttribute('transform')!.match(/translate\(0,([\d.]+)\)/)![1])
 const bottom=Math.max(...[...host.querySelectorAll('[data-legend-swatch]')].map(el=>Number(el.getAttribute('y'))+Number(el.getAttribute('height'))))
 expect(titleHeight+bottom).toBeLessThanOrEqual(visual.height)
 visual.dispose()
})
