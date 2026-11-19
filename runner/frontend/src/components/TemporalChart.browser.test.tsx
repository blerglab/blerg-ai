import {render,waitFor} from '@testing-library/react'
import {VisualBlock} from '@blerglab/chat'
import {expect,it} from 'vitest'
import {page,commands} from 'vitest/browser'
import nvda from '../test/nvdaOriginalSpec.json'
import '../../../../packages/chat/src/theme/brand.css'
import '../../../../packages/chat/src/theme/tokens.css'
import '../index.css'
import '@blerglab/chat/blerg.css'
import {renderBlergChart} from '../../../../packages/chat/src/visual/blerg/render'
import {classifyChart} from '../../../../packages/chat/src/visual/blerg/classify'
import {readVisualTheme} from '../../../../packages/chat/src/visual/theme'
import {compileVegaViewForDifferentialTesting} from '../../../../packages/chat/src/visual/vegaRuntime'
import {positionDomain,type ScaleOptions} from '../../../../packages/chat/src/visual/blerg/scaleOptions'

for(const width of [390,3840])for(const themeId of ['dark','colorblind-dark'])it(`original NVDA native parity and retained updates: ${width} ${themeId}`,async()=>{
 await page.viewport(width,900)
 document.documentElement.dataset.theme=themeId
 const host=document.createElement('div');host.style.cssText=`width:${width}px;height:500px;background:var(--chat-surface);color:var(--chat-fg)`;document.body.append(host)
 const theme=readVisualTheme(host),spec=structuredClone(nvda)
 expect(classifyChart(spec)).toBe('blerg')
 const visual=renderBlergChart(spec,theme,host,width,500)
 try{
  const root=host.querySelector<SVGSVGElement>('svg')!,legend=root.querySelector('[data-scene-key="legend"]')!
  const {view}=await compileVegaViewForDifferentialTesting({...spec,width:640,height:360},theme)
  try{
   const vx=view.scale('x') as ((value:number)=>number)&{domain:()=>Date[];ticks:(count:number)=>Date[]},vy=view.scale('y') as ((value:number)=>number)&{domain:()=>number[]},vc=view.scale('color') as (value:string)=>string
   const times=spec.layer.flatMap(l=>l.data.values.map(r=>+new Date(r.time))),prices=spec.layer.flatMap(l=>l.data.values.map(r=>r.value))
   expect(vx.domain().map(Number)).toEqual(positionDomain(times,undefined,false,true))
   expect(vy.domain()).toEqual(positionDomain(prices,{zero:false},true))
   const [px,py,pw,ph]=JSON.parse(root.dataset.plotBounds!) as number[]
   const path=root.querySelector('[data-datum-key]')!,numbers=path.getAttribute('d')!.match(/-?\d+(?:\.\d+)?/g)!.map(Number)
   const point=spec.layer[1].data.values[0],xd=vx.domain().map(Number),yd=vy.domain()
   expect((numbers[2]-numbers[0])/pw).toBeCloseTo((+new Date(spec.layer[1].data.values[1].time)-+new Date(point.time))/(xd[1]-xd[0]),5)
   expect(px).toBeGreaterThan(0)
   // Local path coordinates omit the heading, legend and title translations. Relative
   // movement still proves the actual path uses Vega's price domain.
   const second=spec.layer[1].data.values[1]
   expect((numbers[3]-numbers[1])/ph).toBeCloseTo(-(second.value-point.value)/(yd[1]-yd[0]),5)
   expect(py).toBeGreaterThan(0)
   const pointInRoot=(element:SVGGraphicsElement,x:number,y:number)=>new DOMPoint(x,y).matrixTransform(element.getScreenCTM()!).matrixTransform(root.getScreenCTM()!.inverse())
   const anchor=pointInRoot(path as SVGGraphicsElement,numbers[0],numbers[1])
   expect((anchor.x-px)/pw).toBeCloseTo(vx(+new Date(point.time))/Number(view.signal('width')),5)
   expect((anchor.y-py)/ph).toBeCloseTo(vy(point.value)/Number(view.signal('height')),5)
   const rules=[...root.querySelectorAll<SVGLineElement>('[data-reference-rule="field"]')]
   for(const [index,rule] of rules.entries()){
    const row=spec.layer[0].data.values[index]
    const from=pointInRoot(rule,Number(rule.getAttribute('x1')),Number(rule.getAttribute('y1')))
    const to=pointInRoot(rule,Number(rule.getAttribute('x2')),Number(rule.getAttribute('y2')))
    expect((from.x-px)/pw).toBeCloseTo(vx(+new Date(row.time))/Number(view.signal('width')),5)
    expect((from.y-py)/ph).toBeCloseTo(vy(row.value)/Number(view.signal('height')),5)
    expect((to.y-py)/ph).toBeCloseTo(1,5)
   }
   const stroke=path.getAttribute('stroke')
   expect(stroke).toBe(vc('NVDA'))
   expect([...legend.querySelectorAll('text')].map(x=>x.textContent)).toEqual(['NVDA','Previous close'])
   const ticks=[...root.querySelectorAll('[data-temporal-axis] [data-scene-key^="tick:"]')].map(x=>Number(x.getAttribute('data-scene-key')!.slice(5)))
   expect(ticks).toEqual(vx.ticks(Math.min(12,Math.max(2,Math.ceil(pw/80)))).map(Number))
   expect(root.querySelectorAll('[data-reference-rule="field"]')).toHaveLength(2)
   expect(root.innerHTML).not.toMatch(/NaN|Infinity/)
  }finally{view.finalize()}
  await commands.startChartCapture(`temporal-original-${themeId}-${width}`,false)
  const preview=window.frameElement!.parentElement as HTMLElement,priorTransform=preview.style.transform
  preview.style.transform='none'
  try{
   expect(host.getBoundingClientRect().width).toBeCloseTo(width,0)
   await commands.chartScreenshot(`temporal-original-${themeId}-${width}`,width,600)
  }finally{preview.style.transform=priorTransform}
  await commands.stopChartCapture([])
  const before=root.querySelector('[data-datum-key]')!.getAttribute('d'),next=structuredClone(spec)
  next.layer[1].data.values[0].value=180
  expect(visual.applyData!(next,1)).toBe(true)
  root.dispatchEvent(new Event('blerg-chart-before-snapshot'))
  expect(host.querySelector('svg')).toBe(root)
  expect(root.querySelector('[data-scene-key="legend"]')).toBe(legend)
  expect(root.querySelector('[data-datum-key]')!.getAttribute('d')).not.toBe(before)
  expect(root.dataset.updateState).toBe('idle')
  for(const el of root.querySelectorAll('[fill],[stroke]'))for(const attr of ['fill','stroke']){
   const color=el.getAttribute(attr)
   if(color && !['none','transparent'].includes(color))expect([theme.fg,theme.muted,theme.accent,theme.border,...theme.palette]).toContain(color)
  }
  const mark=root.querySelector('[data-datum-key]')!
  document.documentElement.dataset.theme=themeId==='dark' ? 'colorblind-dark' : 'dark'
  const repaint=readVisualTheme(host);visual.setTheme!(repaint)
  expect(root.querySelector('[data-datum-key]')).toBe(mark)
  expect(mark.getAttribute('stroke')).toBe(repaint.palette[0])


 }finally{visual.dispose();host.remove();delete document.documentElement.dataset.theme}
},30000)

it('matches Vega domains and authored time tick formats for scale options',async()=>{
 const host=document.createElement('div'),theme=readVisualTheme(host)
 const rows=[{time:'2026-11-05T09:30:00',price:185.37},{time:'2026-11-05T12:21:00',price:190.1}]
 for(const scale of [{zero:false},{zero:true},{domain:[181,199]},{domain:[181,199],zero:true},{domain:[181,199],nice:true},{domain:[199,181],nice:false},{domain:[199,181],zero:true}] satisfies ScaleOptions[]){
  const spec={data:{values:rows},mark:'line',encoding:{x:{field:'time',type:'temporal',axis:{format:'%H:%M',tickCount:6}},y:{field:'price',type:'quantitative',scale,axis:{format:'$.2f',tickCount:4}}}}
  expect(classifyChart(spec)).toBe('blerg')
  const {view}=await compileVegaViewForDifferentialTesting({...spec,width:640,height:360},theme)
  const visual=renderBlergChart(spec,theme,host,640,360)
  try{
   const vy=view.scale('y') as ((value:number)=>number)&{domain:()=>number[]},vx=view.scale('x') as ((value:number)=>number)&{ticks:(count:number)=>Date[]}
   expect(vy.domain()).toEqual(positionDomain(rows.map(r=>r.price),scale,true))
   const labels=[...host.querySelectorAll('[data-temporal-axis] text')].map(x=>x.textContent)
   expect(labels).toEqual(vx.ticks(6).map(date=>`${String(date.getHours()).padStart(2,'0')}:${String(date.getMinutes()).padStart(2,'0')}`))
  }finally{visual.dispose();view.finalize()}
 }
})


it('uses the original spec through VisualBlock in a narrow pane inside a wide window',async()=>{
 await page.viewport(3840,900)
 const {container,unmount}=render(<div style={{width:390}}><VisualBlock kind="vega-lite" source={JSON.stringify(nvda)} /></div>)
 try{
  await waitFor(()=>expect(container.querySelector('[data-chart-renderer="blerg"]')).not.toBeNull())
  const root=container.querySelector<SVGSVGElement>('svg[data-chart-renderer="blerg"]')!
  expect(root.getBoundingClientRect().width).toBeLessThanOrEqual(390)
  expect(root.querySelector('[data-temporal-axis]')).not.toBeNull()
  expect(root.querySelector('[data-scene-key="legend"]')?.textContent).toContain('NVDA')
 }finally{unmount()}
})


it('matches Vega midpoint mapping for a single temporal observation',async()=>{
 const host=document.createElement('div'),theme=readVisualTheme(host),time='2026-11-05T09:30:00'
 const spec={data:{values:[{time,price:185}]},mark:'line',encoding:{x:{field:'time',type:'temporal'},y:{field:'price',type:'quantitative',scale:{zero:false}}}}
 const visual=renderBlergChart(spec,theme,host,640,360)
 const {view}=await compileVegaViewForDifferentialTesting({...spec,width:640,height:360},theme)
 try{
  expect((view.scale('x') as {domain:()=>Date[]}).domain().map(Number)).toEqual([+new Date(time),+new Date(time)])
  expect((view.scale('y') as {domain:()=>number[]}).domain()).toEqual([185,185])
  const root=host.querySelector<SVGSVGElement>('svg')!,mark=root.querySelector<SVGGraphicsElement>('[data-datum-key]')!,numbers=mark.getAttribute('d')!.match(/-?\d+(?:\.\d+)?/g)!.map(Number)
  // Include the title reservation when comparing in the published plot frame.
  document.body.append(host)
  const anchor=new DOMPoint(numbers[0],numbers[1]).matrixTransform(mark.getScreenCTM()!).matrixTransform(root.getScreenCTM()!.inverse())
  const [x,y,w,h]=JSON.parse(root.dataset.plotBounds!) as number[]
  expect((anchor.x-x)/w).toBeCloseTo(0.5,5);expect((anchor.y-y)/h).toBeCloseTo(0.5,5)
 }finally{visual.dispose();view.finalize();host.remove()}
})
