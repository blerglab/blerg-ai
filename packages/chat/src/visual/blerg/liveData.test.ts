import {afterEach, beforeEach, expect, it, vi} from 'vitest'
import {renderBlergChart} from './render'
import {getChartModel, type RenderedVisual} from '../render'
import {readVisualTheme} from '../theme'
type Live = RenderedVisual & {applyData: (model: Record<string, unknown>, generation: number) => boolean}
let frames: Map<number, FrameRequestCallback>, nextId: number, reduced: boolean
beforeEach(()=>{
 frames=new Map();nextId=0;reduced=false
 vi.spyOn(performance,'now').mockReturnValue(0)
 vi.stubGlobal('requestAnimationFrame',(fn:FrameRequestCallback)=>{frames.set(++nextId,fn);return nextId})
 vi.stubGlobal('cancelAnimationFrame',(id:number)=>frames.delete(id))
 vi.stubGlobal('matchMedia',()=>({matches:reduced,addEventListener:()=>{},removeEventListener:()=>{}}))
})
afterEach(()=>{vi.restoreAllMocks();vi.unstubAllGlobals()})
function tick(time:number){const callbacks=[...frames.values()];frames.clear();callbacks.forEach(fn=>fn(time))}
function model(mark:string){return getChartModel(JSON.stringify({title:'Live sample',width:640,height:360,data:{values:[{id:'a',x:mark==='bar'?'A':1,n:1,g:'First'},{id:'b',x:mark==='bar'?'B':2,n:3,g:'First'},{id:'c',x:mark==='bar'?'C':3,n:2,g:'First'}]},mark,encoding:{x:{field:'x',type:mark==='bar'?'nominal':'quantitative'},y:{field:'n',type:'quantitative'},...(mark==='point'?{}:{color:{field:'g',type:'nominal'}}),tooltip:[{field:'id',type:'nominal'},{field:'n',type:'quantitative'}]}}))}
function changed(spec:Record<string,unknown>){const next=structuredClone(spec);const rows=(next.data as {values:{n:number}[]}).values;rows[0].n=7;rows.reverse();return next}
function geometry(root:Element, mark:string){const node=[...root.querySelectorAll('[data-datum-key]')].sort((a,b)=>a.getAttribute('data-datum-key')!.localeCompare(b.getAttribute('data-datum-key')!))[0]!;return node.getAttribute(mark==='bar'?'height':mark==='point'?'cy':'d')}
for(const mark of ['bar','line','area','point']) {
 it(`updates keyed ${mark} data with eased geometry and persistent chrome`,()=>{
  const host=document.createElement('div'), theme=readVisualTheme(host), spec=model(mark)
  const visual=renderBlergChart(spec,theme,host,400,300) as Live
  expect(visual).toHaveProperty('applyData')
  const root=host.querySelector('svg')!, axis=root.querySelector('[data-scene-key="axes"]'), legend=root.querySelector('[data-scene-key="legend"]')
  const marks=new Map([...root.querySelectorAll('[data-datum-key]')].map(node=>[node.getAttribute('data-datum-key'),node])), before=geometry(root,mark), next=changed(spec)
  expect(visual.applyData(next,1)).toBe(true)
  tick(0);expect(geometry(root,mark)).toBe(before)
  tick(125);const middle=geometry(root,mark);expect(middle).not.toBe(before)
  tick(250);expect(geometry(root,mark)).not.toBe(middle)
  expect(host.querySelector('svg')).toBe(root)
  for(const [key,node] of marks) expect(root.querySelector(`[data-datum-key='${key}']`)).toBe(node)
  expect(axis).toBeTruthy();expect(root.querySelector('[data-scene-key="axes"]')).toBe(axis)
  if(legend)expect(root.querySelector('[data-scene-key="legend"]')).toBe(legend)
  const fresh=document.createElement('div'), final=renderBlergChart(next,theme,fresh,400,300)
  expect(geometry(root,mark)).toBe(geometry(fresh.querySelector('svg')!,mark))
  expect(root.getAttribute('data-update-state')).toBe('idle');expect(frames.size).toBe(0)
  final.dispose();visual.dispose()
 })
 it(`updates ${mark} immediately under reduced motion`,()=>{
  reduced=true;const host=document.createElement('div'), spec=model(mark)
  const visual=renderBlergChart(spec,readVisualTheme(host),host,400,300) as Live
  expect(visual).toHaveProperty('applyData')
  const root=host.querySelector('svg')!,before=geometry(root,mark)
  expect(visual.applyData(changed(spec),1)).toBe(true)
  expect(geometry(root,mark)).not.toBe(before);expect(frames.size).toBe(0)
  expect(root.getAttribute('data-update-state')).toBe('idle');visual.dispose()
 })
}
it('rejects structural changes and stale generations without mutating a scene',()=>{
 const host=document.createElement('div'),spec=model('bar'),visual=renderBlergChart(spec,readVisualTheme(host),host,400,300) as Live
 expect(visual).toHaveProperty('applyData');const before=visual.svg
 expect(visual.applyData({...changed(spec),title:'Different spec'},1)).toBe(false);expect(visual.svg).toBe(before)
 expect(visual.applyData(changed(spec),3)).toBe(true);tick(250)
 const current=visual.svg;expect(visual.applyData(spec,2)).toBe(false);expect(visual.svg).toBe(current)
 visual.dispose();expect(frames.size).toBe(0)
})

it('enters and removes datum keys while preserving surviving nodes and table focus',()=>{
 const host=document.createElement('div'),spec=model('bar'),visual=renderBlergChart(spec,readVisualTheme(host),host,400,300) as Live
 document.body.append(host)
 const root=host.querySelector('svg')!, nodes=[...root.querySelectorAll('[data-datum-key]')],details=host.querySelector('details')!
 details.open=true;const item=host.querySelector<HTMLElement>('[role=listitem]')!;item.focus()
 const next=structuredClone(spec), rows=(next.data as {values:Record<string,unknown>[]}).values
 rows.splice(1,1);rows[0].n=5;rows.push({id:'d',x:'D',n:4,g:'First'})
 expect(visual.applyData(next,1)).toBe(true);tick(125)
 expect(root.contains(nodes[0])).toBe(true);expect(root.contains(nodes[1])).toBe(true)
 expect(nodes[1].getAttribute('data-exiting')).toBe('true')
 tick(250);expect(root.contains(nodes[1])).toBe(false);expect(root.querySelectorAll('[data-datum-key]')).toHaveLength(3)
 expect(document.activeElement).toBe(item);expect(details.open).toBe(true);expect(host.querySelector('table')).toHaveTextContent('D')
 visual.dispose();host.remove()
})
it('interrupts a transition from its current geometry and cancels work on disposal',()=>{
 const host=document.createElement('div'),spec=model('point'),visual=renderBlergChart(spec,readVisualTheme(host),host,400,300) as Live, root=host.querySelector('svg')!
 visual.applyData(changed(spec),1);tick(125);const middle=geometry(root,'point')
 vi.mocked(performance.now).mockReturnValue(125)
 const next=changed(spec);((next.data as {values:{n:number}[]}).values[2]).n=11
 visual.applyData(next,2);expect(geometry(root,'point')).toBe(middle);expect(frames.size).toBe(1)
 tick(250);visual.dispose();expect(frames.size).toBe(0);expect(visual.applyData(spec,3)).toBe(false)
})
it('preserves categorical colors when a series disappears or a new series sorts earlier',()=>{
 reduced=true;const spec=model('bar'),rows=(spec.data as {values:Record<string,unknown>[]}).values
 const initial=structuredClone(spec);(initial.data as {values:Record<string,unknown>[]}).values=rows.map((row,i)=>({...row,g:i?'Zebra':'Middle'}))
 const host=document.createElement('div'),visual=renderBlergChart(initial,readVisualTheme(host),host,400,300) as Live
 const key=host.querySelector('[data-datum-key]')!.getAttribute('data-datum-key')!,before=host.querySelector('[data-datum-key]')!.getAttribute('fill')
 const next=structuredClone(initial);(next.data as {values:Record<string,unknown>[]}).values=[(next.data as {values:Record<string,unknown>[]}).values[0],{id:'d',x:'D',n:5,g:'Aardvark'}]
 expect(visual.applyData(next,1)).toBe(true)
 expect(host.querySelector(`[data-datum-key='${key}']`)!.getAttribute('fill')).toBe(before)
 visual.dispose()
})
it('handles empty and restored data without replacing the mounted surface',()=>{
 reduced=true;const spec=model('point'),host=document.createElement('div'),visual=renderBlergChart(spec,readVisualTheme(host),host,400,300) as Live,root=host.querySelector('svg')!
 const empty={...spec,data:{values:[]}}
 expect(visual.applyData(empty,1)).toBe(true);expect(root).toHaveTextContent('No data');expect(root.querySelector('[data-datum-key]')).toBeNull()
 expect(host.querySelector('caption')).toHaveTextContent('0 rows')
 expect(visual.applyData(spec,2)).toBe(true);expect(host.querySelector('svg')).toBe(root);expect(root.querySelectorAll('[data-datum-key]')).toHaveLength(3)
 visual.dispose()
})
it('refuses ambiguous point identities and accepts keyed layered line revisions',()=>{
 reduced=true;const host=document.createElement('div'),spec=model('point'),visual=renderBlergChart(spec,readVisualTheme(host),host,400,300) as Live
 const duplicate=structuredClone(spec),rows=(duplicate.data as {values:Record<string,unknown>[]}).values;rows[1].id=rows[0].id
 const warn=vi.spyOn(console,'warn').mockImplementation(()=>{})
 expect(visual.applyData(duplicate,1)).toBe(false);expect(warn).toHaveBeenCalledWith('Native data update refused',{cause:'ambiguous_datum_identity'});visual.dispose()
 const line=model('line'),{encoding,...base}=line;delete base.mark
 const layered={...base,layer:[{mark:'line',encoding:{...(encoding as object),color:undefined}},{mark:'rule',encoding:{y:{datum:2}}}]}
 const other=renderBlergChart(layered,readVisualTheme(host),host,400,300) as Live,root=host.querySelector('svg')!
 expect(other.applyData(changed(layered),1)).toBe(true);expect(host.querySelector('svg')).toBe(root);other.dispose()
})

it('uses a 250 ms eased transition and supports named dataset rows',()=>{
 const host=document.createElement('div'),spec=model('bar'),visual=renderBlergChart(spec,readVisualTheme(host),host,400,300) as Live,root=host.querySelector('svg')!
 const before=Number(geometry(root,'bar')),next=changed(spec),fresh=document.createElement('div'),target=renderBlergChart(next,readVisualTheme(fresh),fresh,400,300)
 const end=Number(geometry(fresh.querySelector('svg')!,'bar'))
 visual.applyData(next,1);tick(62.5)
 expect((Number(geometry(root,'bar'))-before)/(end-before)).toBeCloseTo(.15625,5)
 tick(249);expect(root.dataset.updateState).toBe('animating');tick(250);expect(root.dataset.updateState).toBe('idle')
 visual.dispose();target.dispose()
 const values=(spec.data as {values:Record<string,unknown>[]}).values
 const named={...spec,data:{name:'rows'},datasets:{rows:values}},other=renderBlergChart(named,readVisualTheme(host),host,400,300) as Live
 expect(other.applyData({...named,datasets:{rows:values.map((row,i)=>({...row,n:i+4}))}},1)).toBe(true)
 other.dispose()
})

it('keeps datum vertices attached to their identities when event-time order changes',()=>{
 const host=document.createElement('div'),spec=model('line'),visual=renderBlergChart(spec,readVisualTheme(host),host,400,300) as Live,root=host.querySelector('svg')!
 const vertices=()=>{
  const path=root.querySelector('[data-datum-key]')!,keys=JSON.parse(path.getAttribute('data-vertex-keys')!) as string[],values=(path.getAttribute('d')!.match(/[-+]?(?:\d*\.)?\d+(?:e[-+]?\d+)?/gi) ?? []).map(Number)
  return new Map(keys.map((key,index)=>[key,values.slice(index*2,index*2+2)]))
 }
 const before=vertices(),next=structuredClone(spec);(next.data as {values:Record<string,unknown>[]}).values[0].x=4
 expect(visual.applyData(next,1)).toBe(true)
 for(const [key,point] of before)expect(vertices().get(key)).toEqual(point)
 expect(root.querySelector('[data-retained-geometry]')).toBeTruthy()
 tick(250);expect(root.querySelector('[data-retained-geometry]')).toBeNull();visual.dispose()
})
it('restores the default palette context after preparation, including an exception',async()=>{
 const {withSeriesDomain,categoricalColorScale}=await import('./color'),palette=readVisualTheme(document.body).palette
 withSeriesDomain(['B','A'],()=>expect(categoricalColorScale(['A','B'],palette)('A')).toBe(palette[1]))
 expect(()=>withSeriesDomain(['B','A'],()=>{throw new Error('sentinel')})).toThrow('sentinel')
 expect(categoricalColorScale(['A','B'],palette)('A')).toBe(palette[0])
})

it('continues an entering mark opacity through a burst without a flash',()=>{
 const host=document.createElement('div'),spec=model('point'),visual=renderBlergChart(spec,readVisualTheme(host),host,400,300) as Live
 const next=structuredClone(spec);(next.data as {values:Record<string,unknown>[]}).values.push({id:'d',x:4,n:2,g:'First'})
 visual.applyData(next,1);tick(125)
 const node=host.querySelectorAll('[data-datum-key]')[3],opacity=Number(node.getAttribute('opacity'))
 expect(opacity).toBeCloseTo(.5)
 vi.mocked(performance.now).mockReturnValue(125)
 const later=structuredClone(next);(later.data as {values:Record<string,unknown>[]}).values[3].n=4
 visual.applyData(later,2);expect(Number(node.getAttribute('opacity'))).toBe(opacity)
 tick(375);expect(Number(node.getAttribute('opacity'))).toBe(1);visual.dispose()
})
it('bounds grown native viewport dimensions before creating a scene',()=>{
 const host=document.createElement('div')
 expect(()=>renderBlergChart(model('bar'),readVisualTheme(host),host,5000,300)).toThrow('Native chart geometry exceeds the drawing budget')
 expect(host.children).toHaveLength(0)
})

it('keeps aggregate bar identity when its contributing row count changes',()=>{
 reduced=true;const spec=structuredClone(model('bar'));(spec.encoding as {y:{aggregate?:string}}).y.aggregate='sum'
 const rows=(spec.data as {values:Record<string,unknown>[]}).values;rows.push({...rows[0],id:'extra',n:2})
 const host=document.createElement('div'),visual=renderBlergChart(spec,readVisualTheme(host),host,400,300) as Live
 const mark=host.querySelector('[data-datum-key]')!,key=mark.getAttribute('data-datum-key')!
 const next=structuredClone(spec);(next.data as {values:Record<string,unknown>[]}).values.pop()
 expect(visual.applyData(next,1)).toBe(true);expect(host.querySelector(`[data-datum-key='${key}']`)).toBe(mark);visual.dispose()
})
it('keeps wide point axis labels readable during a live update',()=>{
 reduced=true;const spec=model('point'),host=document.createElement('div'),visual=renderBlergChart(spec,readVisualTheme(host),host,3840,1800) as Live
 const axis=host.querySelector('[data-scene-key="axes"]')!
 expect(axis.querySelectorAll('text').length).toBeLessThan(35)
 expect(visual.applyData(changed(spec),1)).toBe(true);expect(axis.querySelectorAll('text').length).toBeLessThan(35);visual.dispose()
})

for (const mark of ['line','area']) it(`grows appended ${mark} vertices without a ghost and removes vertices without a crossfade`,()=>{
 const spec=model(mark),host=document.createElement('div'),visual=renderBlergChart(spec,readVisualTheme(host),host,400,300) as Live
 const root=host.querySelector('svg')!,path=root.querySelector('[data-datum-key]')!,numbers=()=>path.getAttribute('d')!.match(/[-+]?(?:\d*\.)?\d+(?:e[-+]?\d+)?/gi)!.map(Number)
 const before=numbers(),next=structuredClone(spec);(next.data as {values:Record<string,unknown>[]}).values.push({id:'d',x:4,n:4,g:'First'})
 expect(visual.applyData(next,1)).toBe(true)
 expect(root.querySelector('[data-retained-geometry]')).toBeNull()
 // The appended top vertex starts at the previous endpoint, not the first baseline vertex.
 expect(numbers().slice(6,8)).toEqual(before.slice(4,6))
 tick(125);expect(root.querySelector('[data-retained-geometry]')).toBeNull()
 tick(250);expect(numbers().slice(6,8)).not.toEqual(before.slice(4,6))
 expect(visual.applyData(spec,2)).toBe(true);expect(root.querySelector('[data-retained-geometry]')).toBeNull()
 tick(500);expect(root.querySelector('[data-datum-key]')).toBe(path);visual.dispose()
})
it('finishes motion before direct and transfer snapshots',async()=>{
 const {rememberSurface,transferSurface}=await import('../surfaceSnapshots')
 const spec=model('bar'),host=document.createElement('div'),visual=renderBlergChart(spec,readVisualTheme(host),host,400,300) as Live,root=host.querySelector('svg')!
 visual.applyData(changed(spec),1);tick(125);const snapshot=visual.snapshot!()
 expect(root.dataset.updateState).toBe('idle');expect(snapshot.svg).not.toContain('data-update-state="animating"');expect(frames.size).toBe(0)
 vi.mocked(performance.now).mockReturnValue(250);visual.applyData(spec,2);tick(375)
 rememberSurface('round-2-transfer',host,visual.width,visual.height)
 expect(root.dataset.updateState).toBe('idle');expect(transferSurface('round-2-transfer')!.querySelector('[data-update-state="animating"]')).toBeNull();expect(frames.size).toBe(0);visual.dispose()
})
