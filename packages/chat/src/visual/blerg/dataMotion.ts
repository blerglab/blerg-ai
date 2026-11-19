import {mixHex} from './color'
// Only trusted SVG geometry is interpolated. Preparation/validation happens before this
// transaction; the mounted root, keyed marks and chrome stay owned by the same renderer.
const NUMBER = /[-+]?(?:\d*\.)?\d+(?:[eE][-+]?\d+)?/g
const GEOMETRY = new Set(['x','y','x1','x2','y1','y2','cx','cy','r','width','height','d','transform','opacity'])
type Saved = {parent: Element | null; attrs: Map<string,string>; vertices: string[]}
export function captureGeometry(root: Element): Map<Element, Saved> {
  return new Map([root,...root.querySelectorAll('*')].map(node => [node,{parent:node.parentElement,attrs:new Map([...node.attributes].map(attr=>[attr.name,attr.value])),vertices:JSON.parse(node.getAttribute('data-vertex-keys') ?? '[]') as string[]}]))
}
function tween(from: string, to: string, oldKeys: string[], newKeys: string[]): (t:number)=>string {
  const a=(from.match(NUMBER) ?? []).map(Number), b=(to.match(NUMBER) ?? []).map(Number)
  if (!a.length || !b.length) return () => to
  const positions = new Map(oldKeys.map((key,index)=>[key,index]))
  const starts = newKeys.map((key,index)=>{
    const retained=positions.get(key)
    if(retained !== undefined)return retained
    // New vertices grow from a neighboring retained endpoint on the same area boundary.
    const boundary=key.endsWith(':top') ? ':top' : key.endsWith(':base') ? ':base' : ''
    for(let prior=index-1;prior>=0;prior--)if((!boundary || newKeys[prior].endsWith(boundary)) && positions.has(newKeys[prior]))return positions.get(newKeys[prior])!
    for(let next=index+1;next<newKeys.length;next++)if((!boundary || newKeys[next].endsWith(boundary)) && positions.has(newKeys[next]))return positions.get(newKeys[next])!
    return undefined
  })
  return t => {let i=0;return to.replace(NUMBER, value => {
    const index=i++, vertex=newKeys[Math.floor(index/2)], prior=vertex ? starts[Math.floor(index/2)] : undefined
    const start=prior !== undefined ? a[prior*2+index%2] : a[index] ?? a[Math.max(0,a.length-2+index%2)] ?? Number(value)
    return String(start+(Number(value)-start)*t)
  })}
}
export function animateGeometry(root: SVGSVGElement, before: Map<Element,Saved>, reduced: boolean, onFrame: (t:number)=>void, onDone: ()=>void): (finish?:boolean)=>void {
  const ghosts: Element[]=[]
  const updates: {node:Element; name:string; value:string; at:(t:number)=>string}[]=[]
  // Viewport dimensions commit immediately; only child geometry moves inside that box.
  for (const node of [...root.querySelectorAll('*')]) {
    const saved=before.get(node), keys=JSON.parse(node.getAttribute('data-vertex-keys') ?? '[]') as string[]
    const oldKeys=new Set(saved?.vertices), newKeys=new Set(keys)
    const priorSurvivors=saved?.vertices.filter(key=>newKeys.has(key)) ?? []
    const nextSurvivors=keys.filter(key=>oldKeys.has(key))
    const crossfade=priorSurvivors.some((key,index)=>nextSurvivors[index]!==key)
    if(crossfade && saved) {
      const ghost=node.cloneNode(false) as Element
      for(const attr of [...ghost.attributes])ghost.removeAttribute(attr.name)
      for(const [name,value] of saved.attrs)if(!['id','data-datum-key','data-scene-key','data-vertex-keys'].includes(name))ghost.setAttribute(name,value)
      ghost.setAttribute('aria-hidden','true');ghost.setAttribute('data-retained-geometry','true')
      node.parentElement!.insertBefore(ghost,node);ghosts.push(ghost)
      const opacity=Number(saved.attrs.get('opacity') ?? '1'), target=node.getAttribute('opacity') ?? '1'
      updates.push({node:ghost,name:'opacity',value:'0',at:t=>String(opacity*(1-t))})
      updates.push({node,name:'opacity',value:target,at:t=>String(Number(target)*t)})
    }
    for (const attr of [...node.attributes]) {
      const from=saved?.attrs.get(attr.name)
      // Heatmap values and timeline status corrections can change only token colors.
      // Interpolate canonical, renderer-produced colors on keyed marks through the same
      // transaction clock; URL/pattern fills and unkeyed chrome commit immediately.
      if(from !== undefined && from !== attr.value && ['fill','stroke'].includes(attr.name) && node.closest('[data-datum-key]') && /^#[\da-f]{6}$/i.test(from) && /^#[\da-f]{6}$/i.test(attr.value)){
        const target=attr.value
        updates.push({node,name:attr.name,value:target,at:t=>mixHex(from,target,t)})
      }
      if (from !== undefined && from !== attr.value && GEOMETRY.has(attr.name)) updates.push({node,name:attr.name,value:attr.value,at:tween(from,attr.value,attr.name==='d'?saved!.vertices:[],attr.name==='d'?keys:[])})
    }
    if(saved && !crossfade && saved.attrs.has('opacity') && !node.hasAttribute('opacity')) {
      updates.push({node,name:'opacity',value:'1',at:tween(saved.attrs.get('opacity')!,'1',[],[])})
    }
    if (!saved && node.hasAttribute('data-datum-key')) {
      const value=node.getAttribute('opacity') ?? '1'
      updates.push({node,name:'opacity',value,at:t=>String(Number(value)*t)})
    }
  }
  const exiting: Element[]=[]
  for(const [node,saved] of before) if(node.hasAttribute('data-datum-key') && !root.contains(node) && saved.parent && root.contains(saved.parent)) {
    saved.parent.append(node);node.setAttribute('data-exiting','true');exiting.push(node)
    const opacity=Number(saved.attrs.get('opacity') ?? '1')
    updates.push({node,name:'opacity',value:'0',at:t=>String(opacity*(1-t))})
  }
  let frame=0, stopped=false
  const finish=()=>{
    for(const update of updates) update.node.setAttribute(update.name,update.value)
    for(const node of [...exiting,...ghosts])node.remove()
    root.dataset.updateState='idle';root.dataset.updateProgress='1';onFrame(1);onDone()
    updates.length=0;exiting.length=0;ghosts.length=0
  }
  const cancel=(complete=false)=>{if(stopped)return;stopped=true;cancelAnimationFrame(frame);if(complete)finish();else [...exiting,...ghosts].forEach(node=>node.remove())}
  if (reduced || !updates.length) {finish();stopped=true;return cancel}
  const start=performance.now()
  root.dataset.updateState='animating';root.dataset.updateProgress='0'
  for(const update of updates) update.node.setAttribute(update.name,update.at(0))
  onFrame(0)
  const tick=(time:number)=>{
    if(stopped)return
    try {
      const fraction=Math.min(1,Math.max(0,(time-start)/250)), eased=fraction*fraction*(3-2*fraction)
      for(const update of updates) update.node.setAttribute(update.name,update.at(eased))
      root.dataset.updateProgress=String(fraction);onFrame(eased)
      if(fraction===1){stopped=true;finish()}else frame=requestAnimationFrame(tick)
    } catch(error) {console.warn('Native data transition failed',{cause:error instanceof Error?error.name:'UnknownError'});cancel(true)}
  }
  frame=requestAnimationFrame(tick)
  return cancel
}
