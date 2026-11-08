import {afterEach, expect, it, vi} from 'vitest'
import {entrance} from './polish'

afterEach(() => vi.unstubAllGlobals())
it('animates a new drawing once, cancels at snapshot and never animates cached content', () => {
  const animate = vi.fn(() => ({cancel: vi.fn()}))
  const root = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  root.innerHTML = '<g data-scene-key="marks"><rect data-datum-key="a" x="1" y="2" width="3" height="4"/></g>'
  Object.defineProperty(root.firstElementChild!.firstElementChild, 'animate', {value: animate})
  vi.stubGlobal('matchMedia', () => ({matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn()}))
  const stop = entrance(root, 'bar', false)
  expect(animate).toHaveBeenCalledOnce()
  expect(animate.mock.calls[0]).toBeDefined()
  stop()
  entrance(root, 'bar', true)
  expect(animate).toHaveBeenCalledOnce()
})
it('does not animate reduced motion', () => {
  vi.stubGlobal('matchMedia', () => ({matches: true}))
  const root = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  const animate = vi.fn(); Object.defineProperty(root, 'animate', {value: animate})
  entrance(root, 'diagram', false)
  expect(animate).not.toHaveBeenCalled()
})

it('stops display effects for transfer snapshots and preference changes', () => {
  const cancel = vi.fn(), animate = vi.fn(() => ({cancel}))
  const listeners = new Map<string, () => void>()
  const media = {matches:false, addEventListener:(name:string, fn:()=>void) => listeners.set(name,fn), removeEventListener:vi.fn()}
  vi.stubGlobal('matchMedia', () => media)
  const root = document.createElementNS('http://www.w3.org/2000/svg','svg')
  Object.defineProperty(root,'animate',{value:animate})
  entrance(root,'diagram',false)
  root.dispatchEvent(new Event('blerg-chart-before-snapshot'))
  expect(cancel).toHaveBeenCalledOnce()
  entrance(root,'diagram',false)
  media.matches = true; listeners.get('change')!()
  expect(cancel).toHaveBeenCalledTimes(2)
})

it('keeps entrances out of cached renders, resize, theme repaint, and data updates', async () => {
  const {renderVisual, getChartModel} = await import('./render')
  const {readVisualTheme} = await import('./theme')
  vi.stubGlobal('matchMedia', () => ({matches:false, addEventListener:vi.fn(), removeEventListener:vi.fn()}))
  const cancel = vi.fn()
  const animate = vi.fn(() => ({cancel}))
  Object.defineProperty(Element.prototype, 'animate', {value:animate, configurable:true})
  const host = document.createElement('div'), theme = readVisualTheme(host)
  const spec = {title:'Entrance lifecycle fixture',mark:'bar',data:{values:[{x:'a',y:2},{x:'b',y:3}]},encoding:{x:{field:'x',type:'nominal'},y:{field:'y',type:'quantitative'},tooltip:[{field:'y',type:'quantitative'}]}}
  try {
    const first = await renderVisual('vega-lite',JSON.stringify(spec),host,theme)
    expect(animate).toHaveBeenCalledTimes(2)
    first.resize!({width:500,height:300},1)
    first.setTheme!(theme)
    expect(animate).toHaveBeenCalledTimes(2)
    host.querySelector('svg')!.dispatchEvent(new FocusEvent('focus'))
    expect(host.querySelector('[data-chart-active]')).toBeTruthy()
    expect(first.svg).not.toContain('data-chart-active=')
    expect(first.snapshot!().svg).not.toContain('data-chart-emphasis=')
    first.dispose()
    const cached = await renderVisual('vega-lite',JSON.stringify(spec),host,theme)
    expect(animate).toHaveBeenCalledTimes(2)
    const updatedSource = JSON.stringify({...spec, data:{values:[{x:'a',y:4},{x:'b',y:5}]}})
    expect(cached.applyData!(getChartModel(updatedSource), 1)).toBe(true)
    expect(animate).toHaveBeenCalledTimes(2)
    cached.dispose()
    const updatedCached = await renderVisual('vega-lite',updatedSource,host,theme)
    expect(animate).toHaveBeenCalledTimes(2)
    updatedCached.dispose()
    expect(cancel).toHaveBeenCalled()
  } finally {delete (Element.prototype as {animate?:unknown}).animate}
})

it.each(['bar','point','line','area','rect','boxplot'])('emphasizes %s observations with keyboard focus', async mark => {
  const {renderBlergChart} = await import('./blerg/render')
  const {readVisualTheme} = await import('./theme')
  const host = document.createElement('div')
  const categorical = ['bar','rect','boxplot'].includes(mark)
  const spec = {mark,data:{values:[{x:categorical?'A':0,y:2,c:1},{x:categorical?'B':1,y:3,c:2}]},encoding:{x:{field:'x',type:categorical?'nominal':'quantitative'},y:{field:'y',type:mark==='rect'?'nominal':'quantitative'},...(mark==='rect'?{color:{field:'c',type:'quantitative'}}:{}),tooltip:[{field:'y',type:'quantitative'}]}}
  const visual = renderBlergChart(spec,readVisualTheme(host),host,mark==='area'?1024:640,360)
  try {
    const root = host.querySelector('svg')!
    root.dispatchEvent(new FocusEvent('focus'))
    expect(root.querySelector('[data-chart-active]')).toBeTruthy()
    if (mark==='line' || mark==='area') expect(root.querySelector('[data-chart-indicator]')).toBeTruthy()
    expect(host.querySelector('.visual-tooltip')!.textContent).not.toBe('')
    root.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight'}))
    expect(root.querySelector('[data-chart-active]')).toBeTruthy()
    root.dispatchEvent(new FocusEvent('blur'))
    expect(root.querySelector('[data-chart-active]')).toBeNull()
  } finally {visual.dispose()}
})
