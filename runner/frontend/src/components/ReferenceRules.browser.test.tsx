import {render, waitFor} from '@testing-library/react'
import {VisualBlock} from '@blerglab/chat'
import {afterEach, expect, it} from 'vitest'
import {page} from 'vitest/browser'
import {renderBlergChart} from '../../../../packages/chat/src/visual/blerg/render'
import {classifyChart} from '../../../../packages/chat/src/visual/blerg/classify'
import {axisLabels} from '../../../../packages/chat/src/visual/blerg/differential'
import {compileVegaViewForDifferentialTesting} from '../../../../packages/chat/src/visual/vegaRuntime'
import {readVisualTheme} from '../../../../packages/chat/src/visual/theme'
import '../index.css'
import '@blerglab/chat/blerg.css'

const rows=[{minute:0,price:182},{minute:30,price:186},{minute:60,price:184}]
function spec(axis:'x'|'y'='y') {
 const datum=axis==='x'?30:183
 return {data:{values:rows},layer:[
  {mark:'line',encoding:{x:{field:'minute',type:'quantitative',axis:{title:'Minutes since open',format:'.0f',tickCount:4,grid:true,labelAngle:-45}},y:{field:'price',type:'quantitative',axis:{title:'Price (USD)',format:'$.2f',tickCount:4,grid:false,labelAngle:0}}}},
  {mark:'rule',encoding:{[axis]:{datum}}},
  {mark:{type:'text',dy:16},encoding:{[axis]:{datum},text:{value:'Previous close'}}},
 ]}
}
afterEach(()=>{document.body.replaceChildren();document.documentElement.removeAttribute('data-theme')})
for(const axis of ['x','y'] as const)it(`matches Vega guides and normalized ${axis} reference geometry`,async()=>{
 const model=spec(axis),host=document.createElement('div')
 document.body.append(host)
 const theme=readVisualTheme(host),visual=renderBlergChart(model,theme,host,640,400)
 expect(classifyChart(model)).toBe('blerg')
 const {view}=await compileVegaViewForDifferentialTesting({...model,width:640,height:400},theme)
 try{
  for(const edge of ['x','y'] as const){
   const labels=[...host.querySelectorAll(`[data-axis="${edge}"] text`)].map(el=>el.textContent)
   expect(labels).toEqual(axisLabels(view,edge))
  }
  expect(host.querySelectorAll('[data-grid="y"]')).toHaveLength(0)
  expect(host.querySelectorAll('[data-grid="x"]').length).toBeGreaterThan(0)
  expect(host.querySelector('[data-axis="x"] text')?.getAttribute('transform')).toContain('rotate(-45')
  const [x,y,w,h]=JSON.parse(host.querySelector('svg')!.dataset.plotBounds!) as number[]
  const rule=host.querySelector<SVGLineElement>('[data-reference-rule]')!
  const scale=view.scale(axis) as ((value:number)=>number)
  const point=new DOMPoint(Number(rule.getAttribute('x1')),Number(rule.getAttribute('y1'))).matrixTransform(rule.getScreenCTM()!).matrixTransform(host.querySelector('svg')!.getScreenCTM()!.inverse())
  const position=((axis==='x'?point.x:point.y)-(axis==='x'?x:y))/(axis==='x'?w:h)
  // Compare in the shared plot frame: guide/title margins differ between renderers.
  expect(position).toBeCloseTo(scale(axis==='x'?30:183)/Number(view.signal(axis==='x'?'width':'height')),5)
 }finally{visual.dispose();view.finalize()}
},15000)
for(const width of [390,3840])for(const themeName of ['dark','colorblind-dark'])it(`renders and repaints the native NVDA-shaped chart at ${width} in ${themeName}`,async()=>{
 await page.viewport(width,width===3840?2160:900)
 document.documentElement.setAttribute('data-theme',themeName)
 const host=document.createElement('div');host.style.width='100%';document.body.append(host)
 const theme=readVisualTheme(host),model={...spec(),title:'NVDA · simulated prices'},visual=renderBlergChart(model,theme,host,width,Math.min(700,width),true)
 try{
  const root=host.querySelector('svg')!,rule=host.querySelector('[data-reference-rule]')!
  expect(root.getBoundingClientRect().width).toBeLessThanOrEqual(width)
  expect(rule.getAttribute('stroke')).toBe(theme.muted)
  for(const el of root.querySelectorAll('[fill],[stroke]'))for(const key of ['fill','stroke']){
   const color=el.getAttribute(key)
   if(color && !['none','transparent'].includes(color))expect([theme.fg,theme.muted,theme.accent,theme.border,...theme.palette]).toContain(color)
  }
  expect(visual.applyData!({...model,data:{values:rows.map(row=>({...row,price:row.price+2}))}},1)).toBe(true)
  expect(host.querySelector('svg')).toBe(root)
  expect(root.dataset.updateState).toBe('animating')
  visual.snapshot!()
  if(import.meta.env.VITE_REFERENCE_SHOTS_DIR){
   const preview=window.frameElement!.parentElement as HTMLElement,transform=preview.style.transform
   preview.style.transform='none'
   try{
    const result=await page.screenshot({path:`${import.meta.env.VITE_REFERENCE_SHOTS_DIR}/reference-${width}-${themeName}.png`,base64:true})
    const image=new Image();image.src='data:image/png;base64,'+(typeof result==='string'?result:result.base64);await image.decode()
    expect(image.naturalWidth).toBe(width)
   }finally{preview.style.transform=transform}
  }
  document.documentElement.setAttribute('data-theme',themeName==='dark'?'colorblind-dark':'dark')
  const changed=readVisualTheme(host);visual.setTheme!(changed)
  expect(host.querySelector('[data-reference-rule]')).toBe(rule)
  expect(rule.getAttribute('stroke')).toBe(changed.muted)
  expect(root.querySelector('path[data-datum-key]')?.getAttribute('stroke')).toBe(changed.accent)
 }finally{visual.dispose()}
})
for(const tickCount of [3,7])it(`matches Vega format, ticks, grid toggles and unrotated labels with ${tickCount} requested ticks`,async()=>{
 const model=spec()
 Object.assign(model.layer[0].encoding.x!.axis!,{tickCount,format:'.1f',grid:false,labelAngle:0})
 Object.assign(model.layer[0].encoding.y!.axis!,{tickCount,format:'.0%',grid:true})
 const host=document.createElement('div');document.body.append(host)
 const theme=readVisualTheme(host),visual=renderBlergChart(model,theme,host,640,400)
 const {view}=await compileVegaViewForDifferentialTesting({...model,width:640,height:400},theme)
 try{
  for(const axis of ['x','y'] as const)expect([...host.querySelectorAll(`[data-axis="${axis}"] text`)].map(el=>el.textContent)).toEqual(axisLabels(view,axis))
  expect(host.querySelectorAll('[data-grid="x"]')).toHaveLength(0)
  const reference=new DOMParser().parseFromString(await view.toSVG(),'image/svg+xml')
  expect(host.querySelectorAll('[data-grid="y"]')).toHaveLength(reference.querySelectorAll('.role-axis-grid line').length)
  expect(host.querySelector('[data-axis="x"] text')?.hasAttribute('transform')).toBe(false)
 }finally{visual.dispose();view.finalize()}
},15000)

it('keeps the real VisualBlock entry native through a data-only source revision',async()=>{
 const model=spec(),source=JSON.stringify(model)
 const mounted=render(<div style={{width:390}}><VisualBlock kind="vega-lite" source={source} hostedTab /></div>)
 try{
  const root=await waitFor(()=>{const svg=mounted.container.querySelector<SVGSVGElement>('svg[data-chart-renderer="blerg"]');expect(svg).toBeTruthy();return svg!})
  const path=root.querySelector('path[data-datum-key]')!,before=path.getAttribute('d')
  mounted.rerender(<div style={{width:390}}><VisualBlock kind="vega-lite" source={JSON.stringify({...model,data:{values:rows.map(row=>({...row,price:row.price+5}))}})} hostedTab /></div>)
  await waitFor(()=>expect(path.getAttribute('d')).not.toBe(before))
  expect(mounted.container.querySelector('svg[data-chart-renderer="blerg"]')).toBe(root)
  expect(root.querySelector('[data-reference-label]')?.textContent).toBe('Previous close')
 }finally{mounted.unmount()}
},15000)
for(const width of [390,3840])it(`keeps the title-to-label gap unchanged when rotating x labels at ${width}`,()=>{
 for(const format of ['.0f','.2f','$.2f']){
 const gaps:number[]=[]
 for(const labelAngle of [0,-45]){
  const model=spec();Object.assign(model.layer[0].encoding.x!.axis!,{labelAngle,format})
  const host=document.createElement('div');document.body.append(host)
  const visual=renderBlergChart(model,readVisualTheme(host),host,width,400)
  try{
   const labels=[...host.querySelectorAll('[data-axis="x"] text')].map(el=>el.getBoundingClientRect())
   const title=host.querySelector('[data-axis-title="x"]')!.getBoundingClientRect()
   const gap=title.top-Math.max(...labels.map(box=>box.bottom))
   expect(gap).toBeGreaterThanOrEqual(6)
   expect(gap).toBeLessThanOrEqual(14)
   gaps.push(gap)
  }finally{visual.dispose();host.remove()}
 }
 expect(Math.abs(gaps[1]-gaps[0])).toBeLessThan(1)
 }
})
