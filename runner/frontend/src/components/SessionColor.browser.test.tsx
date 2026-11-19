import { afterEach, it, expect, vi } from 'vitest'
import { render, screen, within, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import SessionList from './SessionList'
import SessionRail from './SessionRail'
import CommandPaletteProvider from './CommandPalette'
import { apiFetch } from '../apiFetch'
import SessionCard from './SessionCard'
import SessionTiles from './SessionTiles'
import { SESSION_COLORS } from '../lib/sessionColors'
import { SessionColorPicker } from './SessionColor'
import { makeSession, makeDaemon } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import { useSessionStore } from '../hooks/useSessionStore'
import { THEMES } from '../../../../packages/settings/theme'
import { contrast, renderedBackground, simulatedLab, deltaE, visionMatrices } from '../test/colorVision'
import '../index.css'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async () => ({ok:true,status:200,json:async()=>({models:[{id:"claude-sonnet-4-6",name:"Sonnet",efforts:["high"]}],can_grant:true,reason:"",grants:[],mcp_tools:[],boards:[]})})) }))
afterEach(()=>{document.documentElement.removeAttribute('data-theme');localStorage.clear()})
for (const {id:theme} of THEMES.filter(t=>t.id!=='system')) {
 it.each([390,3840])(`real list and Workspace panel in ${theme} at %i px`, async width=>{
  await page.viewport(width,width===390?844:2160)
  document.documentElement.dataset.theme=theme
  localStorage.clear()
  const sessions=['blue','orange','purple','teal'].map((color,i)=>makeSession({id:color,title:['Review workspace colors','Waiting for a decision','Build the next release','Document the workflow'][i],color,runtime:'daemon',model:'claude-sonnet-4-6',effort:'high',status:i===1?'waiting':'idle'}))
  useSessionStore.setState({sessions,daemons:[makeDaemon()]})
  render(<MemoryRouter initialEntries={['/sessions/blue']}><div style={{display:'flex',width:'100%',height:width===390?844:1000}}><div style={{width:width===390?'100%':300,flexShrink:0}}><SessionList variant="sidebar"/></div>{width!==390 && <SessionRail id="colors" session={sessions[0]} live={makeLive()} tasks={[]} childSessions={[]} toolChanges={0} mode="panel" open onClose={()=>{}} onOpenArtifact={()=>{}}/>}</div></MemoryRouter>)
  const bars=[...document.querySelectorAll<HTMLElement>('.session-card')]
  expect(bars).toHaveLength(4)
  const paints=bars.map(bar=>{
   const pseudo=getComputedStyle(bar,'::before')
   expect(pseudo.borderTopLeftRadius).toBe('8px')
   expect(pseudo.backgroundImage).not.toBe('none')
   const probe=document.createElement('span');probe.style.color=bar.style.getPropertyValue('--workspace-color');bar.append(probe)
   const paint=getComputedStyle(probe).color;probe.remove()
   expect(contrast(paint,renderedBackground(bar))).toBeGreaterThanOrEqual(3)
   return paint
  })
  const backgrounds=bars.map(renderedBackground)
  for(const paint of paints) for(const background of backgrounds) expect(contrast(paint,background)).toBeGreaterThanOrEqual(3)
  // Retain the original four-color separation contract as the palette expands. The color-blind
  // and high-contrast themes promise strong separation under each vision simulation; every other
  // theme (the defaults and bundled schemes) keeps the defaults' bar.
  for(const [mode,matrix] of Object.entries(visionMatrices)) for(let i=0;i<4;i++) for(let j=i+1;j<4;j++) expect(deltaE(simulatedLab(paints[i],matrix),simulatedLab(paints[j],matrix)),`${theme} ${mode} ${i}/${j}`).toBeGreaterThanOrEqual(['colorblind-dark','colorblind-light','high-contrast'].includes(theme)?15:5)
  if(width===390) {
   // The real panel is a drawer on phones; capture the list first.
   await page.screenshot({path:`./__screenshots__/blg413-${theme}-${width}-list.png`})
   render(<MemoryRouter><SessionRail id="phone-colors" session={sessions[0]} live={makeLive()} tasks={[]} childSessions={[]} toolChanges={0} mode="drawer" open onClose={()=>{}} onOpenArtifact={()=>{}}/></MemoryRouter>)
  }
  await userEvent.click(screen.getByRole('button',{name:'Session color: Blue'}))
  const group=screen.getByRole('radiogroup',{name:'Session color'})
  expect(within(group).getAllByRole('radio')).toHaveLength(13)
  for(const radio of within(group).getAllByRole('radio')) {
   expect(contrast(getComputedStyle(radio).color,backgrounds[0])).toBeGreaterThanOrEqual(3)
   expect(contrast(getComputedStyle(radio).color,renderedBackground(group))).toBeGreaterThanOrEqual(3)
   const bounds=radio.getBoundingClientRect()
   const top=document.elementFromPoint(bounds.left+bounds.width/2,bounds.top+bounds.height/2)
   expect(top===radio || radio.contains(top),`${theme} palette must paint above neighboring controls`).toBe(true)
  }
  // The contrasting gap and outer ring remain distinct even on pale swatches.
  const ring=getComputedStyle(within(group).getByRole('radio',{name:'Blue'}).firstElementChild!).boxShadow
  expect(ring).not.toBe('none')
  expect(group.getBoundingClientRect().width).toBeLessThan(200)
  group.scrollIntoView()
  await Promise.all(document.getAnimations().map(animation=>animation.finished.catch(()=>{})))
  await page.screenshot({path:`./__screenshots__/blg413-${theme}-${width}-panel.png`})
  const orange=within(group).getByRole('radio',{name:'Orange'})
  await userEvent.click(orange)
  await waitFor(()=>expect(apiFetch).toHaveBeenCalledWith('/api/sessions/blue',expect.objectContaining({body:JSON.stringify({color:'orange'})})))
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
 })
}
it.each(SESSION_COLORS)('sets $name through Ctrl+K',async item=>{
 const width=390
 await page.viewport(width,844)
 vi.mocked(apiFetch).mockClear()
 render(<MemoryRouter><CommandPaletteProvider><SessionColorPicker sessionId="colored" commandsOnly/></CommandPaletteProvider></MemoryRouter>)
 await userEvent.keyboard('{Control>}k{/Control}')
 await userEvent.type(screen.getByRole('combobox',{name:/^Search commands/}),`Set color: ${item.name}`)
 await userEvent.click(await screen.findByRole('option',{name:`Set color: ${item.name}`} ))
 expect(apiFetch).toHaveBeenCalledWith('/api/sessions/colored',expect.objectContaining({method:'PATCH',body:JSON.stringify({color:item.key})}))
})
it.each(THEMES.filter(t=>t.id!=='system').flatMap(t=>[390,3840].map(width=>({theme:t.id,width}))))('collapsed real list in $theme at $width px uses the same rounded bar',async({theme,width})=>{
 document.documentElement.dataset.theme=theme
 await page.viewport(width,width===390?844:2160)
 useSessionStore.setState({sessions:['blue','orange','purple','teal'].map(color=>makeSession({id:color,title:color,color})),daemons:[makeDaemon()]})
 render(<MemoryRouter><SessionList variant="sidebar"/></MemoryRouter>)
 await userEvent.click(screen.getByRole('button',{name:'Collapse workspace list'}))
 const tile=document.querySelector<HTMLElement>('[data-session-id="purple"]')!
 expect(tile).toHaveAccessibleName(/Session color: Purple/)
 for(const tile of document.querySelectorAll<HTMLElement>('.session-tile')) {
  const probe=document.createElement('span');probe.style.color=tile.style.getPropertyValue('--workspace-color');tile.append(probe)
  const paint=getComputedStyle(probe).color;probe.remove()
  expect(contrast(paint,renderedBackground(tile))).toBeGreaterThanOrEqual(3)
 }
 expect(getComputedStyle(tile,'::before').borderTopLeftRadius).toBe('4px')
 expect(getComputedStyle(tile,'::before').backgroundImage).not.toBe('none')
 await page.screenshot({path:`./__screenshots__/blg413-${theme}-${width}-tiles.png`})
})

it('re-resolves saved keys when the theme changes without saving paint', async()=>{
 render(<SessionColorPicker sessionId="saved" color="blue"/>)
 await userEvent.click(screen.getByRole('button',{name:'Session color: Blue'}))
 const blue=screen.getByRole('radio',{name:'Blue'})
 const paints=[]
 const nonSystemThemes=THEMES.filter(t=>t.id!=='system')
 for(const {id} of nonSystemThemes) {
  document.documentElement.dataset.theme=id
  paints.push(getComputedStyle(blue).color)
  expect(blue).toHaveAttribute('aria-checked','true')
 }
 // Every theme — the defaults, the accessibility presets and each bundled scheme — resolves
 // "blue" to its own distinct paint.
 expect(new Set(paints).size).toBe(nonSystemThemes.length)
 vi.mocked(apiFetch).mockClear()
 await userEvent.click(screen.getByRole('radio',{name:'None'}))
 await waitFor(()=>expect(apiFetch).toHaveBeenCalledWith('/api/sessions/saved',expect.objectContaining({body:JSON.stringify({color:''})})))
})

it('supports arrows inside the real card action dialog without navigating the card',async()=>{
 useSessionStore.setState({sessions:[makeSession({id:'colors',title:'Review colors',color:'blue'})],daemons:[makeDaemon()]})
 render(<MemoryRouter><SessionList variant="sidebar"/></MemoryRouter>)
 await userEvent.click(screen.getByRole('button',{name:'Actions for Review colors'}))
 await userEvent.click(screen.getByRole('button',{name:'Session color: Blue'}))
 const group=screen.getByRole('radiogroup',{name:'Session color'})
 await userEvent.click(within(group).getByRole('radio',{name:'Blue'}))
 await waitFor(()=>expect(group).toHaveAttribute('aria-busy','false'))
 await userEvent.keyboard('{ArrowRight}')
 expect(within(group).getByRole('radio',{name:'Lime'})).toHaveFocus()
 await waitFor(()=>expect(apiFetch).toHaveBeenCalledWith('/api/sessions/colors',expect.objectContaining({body:JSON.stringify({color:'lime'})})))
 await userEvent.keyboard('{Escape}')
 expect(screen.queryByRole('radiogroup',{name:'Session color'})).toBeNull()
 expect(screen.getByRole('button',{name:'Session color: Blue'})).toHaveFocus()
 expect(screen.getByRole('dialog',{name:'Actions for Review colors'})).toBeInTheDocument()
})

const cardStates = [
 {name:'Idle', status:'idle'}, {name:'Starting',status:'starting'}, {name:'Running',status:'running'},
 {name:'Background',status:'idle',background_tasks:2},
 {name:'Selected',status:'idle',active:true}, {name:'Hover',status:'idle'}, {name:'Focus',status:'idle'},
 {name:'Waiting on you',status:'waiting'}, {name:'Error',status:'error'},
 {name:'Unread',status:'idle',unread:true}, {name:'Paused',status:'idle',paused_reason:'restart'},
 {name:'Starred',status:'idle',starred:true}, {name:'Ended',status:'ended'},
 {name:'Selected + waiting',status:'waiting',active:true},
 {name:'Waiting + unread',status:'waiting',unread:true},
 {name:'Selected + error',status:'error',active:true},
 {name:'Selected + waiting + unread',status:'waiting',active:true,unread:true},
 {name:'Selected + paused',status:'idle',active:true,paused_reason:'restart'},
 {name:'Stopped',status:'stopped'}, {name:'Disconnected',status:'disconnected'},
] as const
for (const {id:theme} of THEMES.filter(t=>t.id!=='system')) {
 it(`keeps the selected ring visible on every swatch in ${theme}`,async()=>{
  document.documentElement.dataset.theme=theme
  const {rerender}=render(<SessionColorPicker sessionId="ring" color="blue"/>)
  await userEvent.click(screen.getByRole('button',{name:'Session color: Blue'}))
  for(const color of SESSION_COLORS) {
   rerender(<SessionColorPicker sessionId="ring" color={color.key}/>)
   const radio=screen.getByRole('radio',{name:color.name})
   expect(radio).toBeChecked()
   const shadow=getComputedStyle(radio.firstElementChild!).boxShadow
   expect(shadow).toContain('2px')
   expect(shadow).toContain('4px')
   const probe=document.createElement('span');probe.style.color='var(--chalk)';radio.append(probe)
   expect(contrast(getComputedStyle(probe).color,renderedBackground(radio))).toBeGreaterThanOrEqual(3)
   probe.remove()
  }
 })
 it(`identity paints above every card state border and contrasts in ${theme}`,async()=>{
  document.documentElement.dataset.theme=theme
  await page.viewport(3840,2160)
  useSessionStore.setState({sessions:SESSION_COLORS.map((color,i)=>makeSession({id:color.key || 'none',title:color.name,color:color.key,started_at:new Date(Date.UTC(2026,5,13,0,0,13-i)).toISOString()})),daemons:[makeDaemon()]})
  const {container}=render(<MemoryRouter initialEntries={['/sessions/blue']}><div className="color-state-grid" style={{background:'var(--basalt)',padding:12,display:'flex',gap:12}}>
   <style>{'.color-state-grid .session-tiles { gap: 78px; padding: 14px 0 0; overflow: visible; flex: none; }'}</style>
   <div style={{flex:1,minWidth:0}}>
   {SESSION_COLORS.map(color=><div key={color.key} style={{display:'grid',gridTemplateColumns:`70px repeat(${cardStates.length}, minmax(0, 1fr))`,gap:8,height:110}}>
    <strong style={{color:'var(--chalk)',alignSelf:'center'}}>{color.name}</strong>
    {cardStates.map((state,index)=><div key={state.name} data-state={state.name} data-color={color.key}>
     <small style={{color:'var(--fog)'}}>{state.name}</small>
     <SessionCard session={makeSession({...state,id:`${color.key}-${index}`,title:'Review release',color:color.key,runtime:'daemon',error_reason:state.status==='error'?'Build failed':undefined})} active={'active' in state && state.active} daemon={makeDaemon()}/>
    </div>)}
   </div>)}
   </div><div><small style={{color:'var(--fog)'}}>Tile</small><SessionTiles/></div>
  </div></MemoryRouter>)
  for(const tile of container.querySelectorAll<HTMLElement>('.session-tile')) {
   const bar=getComputedStyle(tile,'::before')
   expect(Number(bar.zIndex)).toBeGreaterThanOrEqual(1)
   expect(parseFloat(bar.left)).toBe(-parseFloat(getComputedStyle(tile).borderLeftWidth))
  }
  expect(container.querySelectorAll('.session-tile')).toHaveLength(13)
  const paints: string[]=[]
  let minimum=Infinity
  // One real hover establishes the shared state paint. Freeze that paint across
  // the color rows so a single screenshot can show every hovered card together.
  // Repeating pointer travel for each row adds browser scheduling, not proof.
  const hoverCard=container.querySelector<HTMLElement>('[data-state="Hover"] .session-card')!
  await userEvent.hover(hoverCard)
  const hoverOutline=getComputedStyle(hoverCard).outline
  const hoverOffset=getComputedStyle(hoverCard).outlineOffset
  expect(parseFloat(getComputedStyle(hoverCard,'::before').left)).toBe(-parseFloat(getComputedStyle(hoverCard).borderLeftWidth))
  await userEvent.unhover(hoverCard)
  for(const cell of container.querySelectorAll<HTMLElement>('[data-state]')) {
   const card=cell.querySelector<HTMLElement>('.session-card')!
   if(cell.dataset.state==='Hover') {
    card.style.outline=hoverOutline
    card.style.outlineOffset=hoverOffset
   }
   if(cell.dataset.state==='Focus') {
    card.querySelector<HTMLButtonElement>('button')!.focus()
    card.style.outline=getComputedStyle(card).outline
    card.style.outlineOffset=getComputedStyle(card).outlineOffset
   }
   const style=getComputedStyle(card), bar=getComputedStyle(card,'::before')
   expect(bar.position).toBe('absolute')
   expect(Number(bar.zIndex)).toBeGreaterThanOrEqual(1)
   expect(parseFloat(bar.left)).toBe(-parseFloat(style.borderLeftWidth))
   expect(parseFloat(bar.top)).toBe(-parseFloat(style.borderTopWidth))
   expect(bar.pointerEvents).toBe('none')
   if(cell.dataset.color) {
    expect(bar.backgroundImage).not.toContain('rgba(0, 0, 0, 0) 0px')
    const probe=document.createElement('span');probe.style.color=card.style.getPropertyValue('--workspace-color');card.append(probe)
    const paint=getComputedStyle(probe).color;probe.remove()
    const background=renderedBackground(card)
    expect(contrast(paint,background),`${theme} ${cell.dataset.color} ${cell.dataset.state}`).toBeGreaterThanOrEqual(3)
    if(card.classList.contains('blerg-runner-waiting-flash')) {
     // Worst point in the animated sweep: 22% blaze over the card fill.
     const probe=document.createElement('span');probe.style.color='var(--blaze)';card.append(probe)
     const blaze=getComputedStyle(probe).color.match(/[\d.]+/g)!.map(Number);probe.remove()
     const base=background.match(/[\d.]+/g)!.map(Number)
     const peak=`rgb(${base.slice(0,3).map((v,i)=>v*.78+blaze[i]*.22).join(',')})`
     expect(contrast(paint,peak),`${theme} ${cell.dataset.color} sweep peak`).toBeGreaterThanOrEqual(3)
    }
    if(cell.dataset.state==='Idle') paints.push(paint)
   } else expect(card.style.getPropertyValue('--workspace-color')).toBe('')
   if(cell.dataset.state!.startsWith('Selected')) {
    expect(style.borderTopColor).toBe(style.borderRightColor)
    expect(style.borderRightColor).toBe(style.borderBottomColor)
    expect(card.querySelector('[data-testid="status-slot"]')).not.toBeNull()
   }
  }
  // Adjacent means horizontal/vertical neighbors in the actual four-column grid,
  // plus sequential keyboard neighbors and wrap. None is an action, not an ink.
  for(let i=0;i<paints.length;i++) for(const step of [1,4]) {
   const j=(i+step)%paints.length
   for(const [mode,matrix] of Object.entries(visionMatrices)) {
    const distance=deltaE(simulatedLab(paints[i],matrix),simulatedLab(paints[j],matrix))
    minimum=Math.min(minimum,distance)
    expect(distance,`${theme} ${mode} ${SESSION_COLORS[i+1].name}/${SESSION_COLORS[j+1].name}`).toBeGreaterThanOrEqual(10)
   }
  }
  container.dataset.minimumDeltaE=minimum.toFixed(2)
  if(['dark','light','colorblind-dark'].includes(theme)) await page.screenshot({path:`./__screenshots__/blg413-${theme}-state-grid.png`})
 }, 10000)
}
