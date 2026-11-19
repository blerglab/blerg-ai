import {useEffect} from 'react'
import {act, fireEvent, render, screen, waitFor} from '@testing-library/react'
import {expect, it, vi} from 'vitest'
import ArtifactViewer from './ArtifactViewer'
import VisualViewer from './VisualViewer'
import {createFakeFiles} from '../test/fakeTransport'
import {saveBlob} from '../transport/files'
import type {RenderedVisual} from '../visual/render'

vi.mock('../visual/visualExport',async load=>({...await load<typeof import('../visual/visualExport')>(),exportPng:vi.fn(async()=>new Blob(['185.25']))}))
vi.mock('../transport/files', async load => ({...await load<typeof import('../transport/files')>(),saveBlob:vi.fn()}))
// Supply a stale/misbehaving renderer and bypass the DOM disabled attribute.
// This exercises the real handlers below presentation, not a copy of the rule.
vi.mock('./VisualBlock',()=>({default:function StaleVisual({onRendered,source:input}:{onRendered?:(visual:RenderedVisual)=>void;source?:string}){
 useEffect(()=>{if(input?.includes('Hold-render'))return;onRendered?.({svg:'<svg xmlns="http://www.w3.org/2000/svg" width="80" height="40"><text>185.25</text></svg>',width:80,height:40,dispose:()=>{}})},[onRendered,input])
 return <div>Simulated private quote: 185.25</div>
}}))
vi.mock('./ViewerToolbar',async load=>({...await load<typeof import('./ViewerToolbar')>(),ViewerAction:({label,onClick,disabled}:{label:string;onClick?:()=>void;disabled?:boolean})=><button data-export-disabled={String(disabled)} onClick={onClick}>{label}</button>}))
const source=JSON.stringify({format:'blerg-dashboard',version:1,title:'Simulated market',cards:[{id:'quote',kind:'stat',title:'Quote',span:1,data:{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},view:{value:'value',selection:'latest'}}]})
it('refuses both snapshot export handlers even with retained private SVG and bypassed controls',async()=>{
 const saved=vi.mocked(saveBlob);saved.mockClear()
 const heldSource=source.replace('Simulated market','Hold-render')
 const view=render(<VisualViewer visualKind="mermaid" source="flowchart LR; A-->B" label="Private quotes" onClose={()=>{}} embedded />)
 await screen.findByText('Simulated private quote: 185.25')
 view.rerender(<VisualViewer visualKind="dashboard" source={heldSource} label="Private quotes" onClose={()=>{}} embedded />)
 fireEvent.click(screen.getByRole('button',{name:'Download SVG'}))
 await act(async()=>{fireEvent.click(screen.getByRole('button',{name:'Download PNG'}))})
 expect(saved).not.toHaveBeenCalled()
 fireEvent.click(screen.getByRole('button',{name:'Download source'}))
 expect(saved).toHaveBeenCalledOnce()
 const blob=saved.mock.calls[0][0]
 const text=await new Promise<string>((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(String(reader.result));reader.onerror=reject;reader.readAsText(blob)})
 expect(text).toBe(heldSource)
 expect(text).not.toContain('185.25')
})
it('published MCP dashboard exposes only original source, never payload controls',async()=>{
 const saved=vi.mocked(saveBlob);saved.mockClear()
 const files=createFakeFiles();files.serve(source)
 render(<ArtifactViewer sessionId="session" files={files} artifact={{id:'mcp',name:'quotes.dash.json',size:source.length,content_type:'application/json',view:'json'}} onClose={()=>{}} />)
 await screen.findByText('Simulated private quote: 185.25')
 expect(screen.queryByRole('button',{name:'Download SVG'})).toBeNull()
 expect(screen.queryByRole('button',{name:'Download PNG'})).toBeNull()
 fireEvent.click(screen.getByRole('button',{name:'Download source'}))
 await waitFor(()=>expect(saved).toHaveBeenCalledOnce())
 expect(files.downloads).toEqual([])
 expect(saved.mock.calls[0][1]).toBe('quotes.dash.json')
 const text=await new Promise<string>((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(String(reader.result));reader.onerror=reject;reader.readAsText(saved.mock.calls[0][0])})
 expect(text).toBe(source)
 expect(text).not.toContain('185.25')
})

it('cannot export a dashboard drawing retained while another visual kind is still pending',async()=>{
 const saved=vi.mocked(saveBlob);saved.mockClear()
 const view=render(<VisualViewer visualKind="dashboard" source={source} label="Private quotes" onClose={()=>{}} embedded />)
 await screen.findByText('Simulated private quote: 185.25')
 view.rerender(<VisualViewer visualKind="mermaid" source="flowchart LR; Hold-render-->Wait" label="New diagram" onClose={()=>{}} embedded />)
 fireEvent.click(screen.getByRole('button',{name:'Download SVG'}))
 await act(async()=>{fireEvent.click(screen.getByRole('button',{name:'Download PNG'}))})
 expect(saved).not.toHaveBeenCalled()
})
it('cannot carry a dashboard drawing into a different published artifact export',async()=>{
 const saved=vi.mocked(saveBlob);saved.mockClear()
 const files=createFakeFiles();files.serve(source)
 const view=render(<ArtifactViewer sessionId="session" files={files} artifact={{id:'mcp',name:'quotes.dash.json',size:source.length,content_type:'application/json',view:'json'}} onClose={()=>{}} />)
 await screen.findByText('Simulated private quote: 185.25')
 files.serve('flowchart LR; Hold-render-->Wait')
 view.rerender(<ArtifactViewer sessionId="session" files={files} artifact={{id:'new',name:'diagram.mmd',size:32,content_type:'text/plain',view:'text'}} onClose={()=>{}} />)
 fireEvent.click(await screen.findByRole('button',{name:'Download SVG'}))
 await act(async()=>{fireEvent.click(screen.getByRole('button',{name:'Download PNG'}))})
 expect(saved).not.toHaveBeenCalled()
})
