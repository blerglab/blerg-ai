import {expect, it, vi} from 'vitest'
import {cdp} from 'vitest/browser'
import {renderVisual} from '../../../../packages/chat/src/visual/render'
import {readVisualTheme} from '../../../../packages/chat/src/visual/theme'
import {exportPng} from '../../../../packages/chat/src/visual/visualExport'
import '../index.css'
import '@blerglab/chat/blerg.css'

for (const reduced of [false,true]) it(`native entrance and final PNG export, reduced motion ${reduced}`, async () => {
  const client = cdp() as unknown as {send:(method:string,params:object)=>Promise<unknown>}
  await client.send('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:reduced?'reduce':'no-preference'}]})
  const host = document.createElement('div'); host.className = 'blerg-chat'; document.body.append(host)
  const theme = readVisualTheme(host), animate = vi.spyOn(Element.prototype,'animate')
  try {
    for (const mark of ['bar','line','area','point','diagram']) {
      const spec = {title:`Entrance export ${mark} ${reduced}`,width:1024,height:360,mark,data:{values:[{x:mark==='bar'?'A':0,y:2},{x:mark==='bar'?'B':5,y:3}]},encoding:{x:{field:'x',type:mark==='bar'?'nominal':'quantitative'},y:{field:'y',type:'quantitative'}}}
      const source = mark==='diagram' ? `flowchart LR\n A[Entrance ${reduced}] --> B[Complete]` : JSON.stringify(spec)
      const count = animate.mock.calls.length
      const visual = await renderVisual(mark==='diagram'?'mermaid':'vega-lite',source,host,theme,undefined,{available:{width:1024,height:360},fill:false})
      const root = host.querySelector<SVGSVGElement>('svg')!
      expect(root.getAttribute(mark==='diagram'?'data-diagram-renderer':'data-chart-renderer')).toBeTruthy()
      if (reduced) expect(animate.mock.calls).toHaveLength(count)
      else expect(animate.mock.calls.length).toBeGreaterThan(count)
      const blob = await exportPng(visual,theme)
      const image = await createImageBitmap(blob)
      expect(image.width).toBeGreaterThan(0)
      if (mark!=='diagram') expect(root.getAnimations({subtree:true})).toHaveLength(0)
      image.close(); visual.dispose()
    }
  } finally {
    animate.mockRestore(); host.remove()
    await client.send('Emulation.setEmulatedMedia',{features:[]})
  }
},30000)

it('dims authored translucent marks without making them more opaque', async () => {
  const host = document.createElement('div'); document.body.append(host)
  const spec = {mark:{type:'bar',opacity:0.25},data:{values:[{x:'A',y:2},{x:'B',y:3}]},encoding:{x:{field:'x',type:'nominal'},y:{field:'y',type:'quantitative'}}}
  const visual = await renderVisual('vega-lite',JSON.stringify(spec),host,readVisualTheme(host))
  try {
    const root = host.querySelector('svg')!
    root.dispatchEvent(new FocusEvent('focus'))
    const inactive = root.querySelector<SVGElement>('[data-datum-key]:not([data-chart-active])')!
    expect(getComputedStyle(inactive).opacity).toBe('0.25')
    expect(getComputedStyle(inactive).filter).toBe('opacity(0.6)')
  } finally {visual.dispose(); host.remove()}
})
