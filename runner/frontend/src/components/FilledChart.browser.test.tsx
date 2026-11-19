import {cleanup, fireEvent, render, waitFor} from '@testing-library/react'
import {afterEach, expect, it, vi} from 'vitest'
import {page} from 'vitest/browser'
import VisualBlock from '../../../../packages/chat/src/components/VisualBlock'
import '../index.css'
import '@blerglab/chat/blerg.css'

afterEach(()=>{cleanup();vi.restoreAllMocks()})
it.each([390,3840])('fits the native chart and its accessible footer at %i px without replaying entrance on resize',async width=>{
 await page.viewport(width,width===390?844:1600)
 const animate=vi.spyOn(Element.prototype,'animate')
 const source=JSON.stringify({mark:'bar',data:{values:[{suite:'Unit',seconds:12},{suite:'Integration',seconds:45}]},encoding:{x:{field:'suite',type:'nominal'},y:{field:'seconds',type:'quantitative',axis:{title:'Wall time (s)'}}}})
 const view=render(<div className="blerg-chat" style={{width:'100%',height:500,display:'flex',flexDirection:'column'}}><VisualBlock kind="vega-lite" source={source} hostedTab stretch/></div>)
 const surface=()=>view.container.querySelector<HTMLElement>('.visual-surface')!
 await waitFor(()=>expect(surface()?.dataset.sceneState).toBe('exact'))
 expect(surface().querySelector('svg[data-chart-renderer="blerg"]')).not.toBeNull()
 expect(surface().scrollHeight).toBeLessThanOrEqual(surface().clientHeight+1)
 const footer=surface().querySelector<HTMLElement>('.visual-blerg-table > summary')!
 expect(footer.getBoundingClientRect().bottom).toBeLessThanOrEqual(surface().getBoundingClientRect().bottom+1)
 const entrances=animate.mock.calls.length
 await page.viewport(width-20,width===390?844:1600)
 await waitFor(()=>expect(Number(surface().dataset.measuredWidth)).toBeLessThan(width-20))
 expect(surface().scrollHeight).toBeLessThanOrEqual(surface().clientHeight+1)
 expect(animate.mock.calls).toHaveLength(entrances)
 fireEvent.click(footer)
 expect(surface().querySelector('details')!.open).toBe(true)
 expect(surface().querySelector('table')).toBeVisible()
})
