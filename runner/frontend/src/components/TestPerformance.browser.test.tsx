import {act,cleanup,fireEvent,render,screen,waitFor} from '@testing-library/react'
import {afterEach,expect,it,vi} from 'vitest'
import {page,commands,cdp} from 'vitest/browser'
import {MemoryRouter} from 'react-router-dom'
import InsightsPage from './InsightsPage'
import TestPerformance from './TestPerformance'
import {setTheme} from '../../../../packages/settings/theme'
import '../index.css'
import '@blerglab/chat/blerg.css'
vi.mock('../ws',()=>import('../test/wsMock'))
const apiFetch=vi.hoisted(()=>vi.fn())
vi.mock('../apiFetch',()=>({apiFetch}))
const runs=Array.from({length:12},(_,i)=>({workspace_id:'example-workspace',member_id:'api',run_id:`run-${i}`,commit:'0123456789abcdef0123456789abcdef01234567',finished_at:new Date(Date.now()-(11-i)*86400000).toISOString(),seconds:100+i*3,complete:i!==4,region:null,suites:[{suite:'unit',flakes:i===11?1:0,layer:'2',budget_seconds:10,seconds:12+i*.3,outcome:'pass',counts:{pass:180+i,fail:0,skip:2},slowest:[]},{suite:'integration',layer:'3b',budget_seconds:60,seconds:45+i*2+(i===8?18:0),outcome:i===8?'fail':'pass',counts:null,slowest:[]}]})).reverse()
afterEach(()=>{cleanup();localStorage.clear();vi.restoreAllMocks()})
it.each([390,3840])('opens the real tab and filters suite trends at %i px',async width=>{
 await page.viewport(width,width===390?844:1600)
 setTheme('dark');localStorage.clear()
 apiFetch.mockImplementation(async(url:string)=>url.startsWith('/api/test-performance')?new Response(JSON.stringify({runs,truncated:false})):new Promise(()=>{}))
 render(<div style={{position:"fixed",inset:0,display:"flex",flexDirection:"column"}}><MemoryRouter initialEntries={['/insights']}><InsightsPage/></MemoryRouter></div>)
 fireEvent.click(await screen.findByRole('button',{name:'Test performance'}))
 await waitFor(()=>expect(document.querySelector('.test-performance .visual-surface[data-scene-state="exact"] svg[data-chart-renderer="blerg"]')).not.toBeNull(),{timeout:15000})
 const bounds=JSON.parse(document.querySelector<SVGSVGElement>('.test-performance svg[data-chart-renderer="blerg"]')!.dataset.plotBounds!) as number[]
 expect(bounds[2]).toBeGreaterThan(width===390?150:1000)
 const surface=document.querySelector<HTMLElement>('.test-performance .visual-surface')!
 expect(surface.scrollHeight).toBeLessThanOrEqual(surface.clientHeight+1)
 if(width===3840)expect(bounds[3]).toBeGreaterThan(600)
 expect(screen.getByRole('heading',{name:'integration is 49% slower than 11 days ago'})).toBeVisible()
 expect(screen.getByRole('group',{name:'Test run overview'})).toHaveTextContent('Latest run133.0 s')
 expect(screen.getByText('12 recorded runs · Wall time per suite; parallel suites can overlap.')).toBeVisible()
 expect(screen.queryByText('Loading…')).toBeNull()
 expect(document.querySelector('.test-performance')!.scrollWidth).toBeLessThanOrEqual(width)
 expect(document.querySelector('.test-performance')!.getBoundingClientRect().height).toBeGreaterThan(400)
 const shot=async(name:string)=>{if(import.meta.env.VITE_BLG402_SHOTS_DIR){await document.fonts.ready;await new Promise<void>(resolve=>requestAnimationFrame(()=>requestAnimationFrame(()=>resolve())));await commands.testPerformanceScreenshot(width,name)}}
 await shot('trends')
 const findings=screen.getByRole('region',{name:'Suite findings'})
 expect(findings).toHaveTextContent('Reported flakes1')
 expect(findings.querySelector('article h3')).toHaveTextContent('integration')
 // The chart can become exact during the meter's first scale-from-zero frame.
 const meter=screen.getByRole('meter',{name:'15.3 s of 10 s budget, 5.3 s over'})
 await Promise.all(meter.getAnimations().map(animation=>animation.finished))
 expect(meter).toBeVisible()
 expect(findings).toHaveTextContent('7.0 s over')
 expect(findings).toHaveTextContent('+48.9%')
 findings.scrollIntoView({block:'start'})
 expect(document.querySelector('.test-performance')!.scrollWidth).toBeLessThanOrEqual(width)
 await shot('findings')
 document.querySelector('.test-performance')!.scrollTop=0
 fireEvent.change(screen.getByRole('combobox',{name:'Suite'}),{target:{value:'unit'}})
 await waitFor(()=>expect(document.querySelector('.test-performance .visual-surface[data-scene-state="exact"] svg[data-chart-renderer="blerg"]')?.textContent).not.toContain('integration'))
 const recorded=screen.getByText('Recorded runs · 12').closest('details')!
 await waitFor(()=>expect(recorded.open).toBe(width===3840))
 if(!recorded.open)fireEvent.click(screen.getByText('Recorded runs · 12'))
 await waitFor(()=>expect(screen.getAllByText(/passed · 0 failed/)).toHaveLength(12))
 await waitFor(()=>expect(document.querySelector('.test-performance .visual-surface[data-scene-state="exact"] svg[data-chart-renderer="blerg"]')).not.toBeNull())
 recorded.scrollIntoView({block:'start'})
 await shot('details')
 document.querySelector('.test-performance')!.scrollTop=0
 const generation=Number(document.querySelector<HTMLElement>('.test-performance .visual-surface')!.dataset.sceneGeneration)
 setTheme('colorblind-dark')
 await waitFor(()=>expect(Number(document.querySelector<HTMLElement>('.test-performance .visual-surface')!.dataset.sceneGeneration)).toBeGreaterThan(generation))
 await waitFor(()=>expect(document.querySelector('.test-performance .visual-surface[data-scene-state="exact"] svg[data-chart-renderer="blerg"]')).not.toBeNull(),{timeout:15000})
 await waitFor(()=>expect(document.querySelector('.visual-loading')).toBeNull())
 await shot('colorblind')
})
it('uses a skeleton until ready and an actionable empty or error state',async()=>{
 await page.viewport(390,844)
 let resolve!:(value:Response)=>void
 apiFetch.mockImplementation(()=>new Promise<Response>(r=>{resolve=r}))
 render(<TestPerformance/>)
 expect(screen.getByLabelText('Fetching test history')).toHaveAttribute('aria-busy','true')
 expect(screen.queryByText(/0 of 0|Loading/)).toBeNull()
 await act(async()=>resolve(new Response(JSON.stringify({runs:[],truncated:false}))))
 expect(await screen.findByText('Give your tests a history')).toBeVisible()
 apiFetch.mockResolvedValue(new Response(null,{status:503}))
 fireEvent.click(screen.getByRole('button',{name:'Refresh test history'}))
 expect(await screen.findByRole('alert')).toHaveTextContent('Could not read test history.')
})
it('keeps a narrow pane readable inside a wide window',async()=>{
 await page.viewport(3840,1600)
 apiFetch.mockResolvedValue(new Response(JSON.stringify({runs,truncated:false})))
 render(<div style={{width:390,height:844}}><TestPerformance/></div>)
 await waitFor(()=>expect(document.querySelector('.test-performance .visual-surface[data-scene-state="exact"] svg[data-chart-renderer="blerg"]')).not.toBeNull())
 const bounds=JSON.parse(document.querySelector<SVGSVGElement>('.test-performance svg[data-chart-renderer="blerg"]')!.dataset.plotBounds!) as number[]
 expect(bounds[2]).toBeGreaterThan(150)
 expect(document.querySelector('.test-performance')!.scrollWidth).toBeLessThanOrEqual(390)
 expect(screen.getByText('Recorded runs · 12').closest('details')!.open).toBe(false)
})

it.each([390,3840])('offers a continuation rather than empty history after a bounded scan at %i px',async width=>{
 await page.viewport(width,width===390?844:1600)
 setTheme('dark')
 apiFetch.mockResolvedValue(new Response(JSON.stringify({runs:[],truncated:false,scan_limited:true,next_cursor:'scan',projects:[]})))
 render(<div style={{position:'fixed',inset:0}}><TestPerformance/></div>)
 expect(await screen.findByRole('button',{name:'Load more'})).toBeVisible()
 expect(screen.getByText('More runs may follow. Load more to continue checking your history.')).toBeVisible()
 expect(screen.queryByText('Give your tests a history')).toBeNull()
 expect(screen.queryByText(/0 of 0|Loading…/)).toBeNull()
 if(import.meta.env.VITE_BLG402_SHOTS_DIR)await commands.testPerformanceScreenshot(width,'scan')
})

it.each([false,true])('animates budget meters only on first appearance, reduced motion %s',async reduced=>{
 const client=cdp() as unknown as {send:(method:string,params:object)=>Promise<unknown>}
 await client.send('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:reduced?'reduce':'no-preference'}]})
 const animate=vi.spyOn(HTMLMeterElement.prototype,'animate')
 try {
  await page.viewport(390,844)
  apiFetch.mockImplementation(async()=>new Response(JSON.stringify({runs,truncated:false})))
  render(<div style={{width:'100%',height:844}}><TestPerformance/></div>)
  const exact=()=>waitFor(()=>expect(document.querySelector('.test-performance .visual-surface[data-scene-state="exact"] svg')).not.toBeNull())
  await exact()
  expect(animate).toHaveBeenCalledTimes(reduced?0:2)
  const meters=screen.getAllByRole('meter')
  // Inspect the actual entrance: zero width is transient, with no forwards fill.
  // Reduced motion skips it and must be visible immediately.
  for(const node of meters){
   const animations=animate.mock.results
    .filter(result=>result.type==='return')
    .map(result=>result.value as Animation)
    .filter(animation=>(animation.effect as KeyframeEffect).target===node)
   for(const animation of animations){
    expect(animation.effect!.getTiming()).toMatchObject({duration:400,fill:'auto'})
    animation.pause()
    animation.currentTime=0
    expect(node.getBoundingClientRect().width).toBe(0)
    animation.play()
   }
   await Promise.all(animations.map(animation=>animation.finished))
   expect(node).toBeVisible()
   expect(node.getBoundingClientRect().width).toBeGreaterThan(0)
  }
  fireEvent.change(screen.getByRole('combobox',{name:'Suite'}),{target:{value:'unit'}})
  await exact()
  fireEvent.change(screen.getByRole('combobox',{name:'Suite'}),{target:{value:''}})
  await exact()
  fireEvent.click(screen.getByRole('button',{name:'Refresh test history'}))
  await exact()
  await page.viewport(3840,1600)
  setTheme('colorblind-dark')
  await exact()
  expect(animate).toHaveBeenCalledTimes(reduced?0:2)
 }finally{animate.mockRestore();await client.send('Emulation.setEmulatedMedia',{features:[]})}
})
