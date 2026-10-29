import {act,cleanup,render,screen} from '@testing-library/react'
import {useEffect,useState} from 'react'
import {afterEach,expect,it,vi} from 'vitest'
import DesktopScopeBoundary from './DesktopScopeBoundary'

afterEach(cleanup)

it.each(['blerg-desktop-scope-changed','blerg-identity-changed'])('drops mounted source snapshots and old reads on %s',async event=>{
 let privateSource='private-origin'
 let finish!:()=>void
 const read=new Promise<void>(resolve=>{finish=resolve})
 let first=true
 const dropped=vi.fn()
 function Reader(){
  const [source,setSource]=useState(privateSource)
  useEffect(()=>{let alive=true;const pending=first?read:new Promise<void>(()=>{});first=false;void pending.then(()=>{if(alive)setSource('late-private-response')});return()=>{alive=false;dropped()}},[])
  return <span>{source}</span>
 }
 render(<DesktopScopeBoundary><Reader/></DesktopScopeBoundary>)
 expect(screen.getByText('private-origin')).toBeTruthy()
 privateSource='source unavailable'
 act(()=>window.dispatchEvent(new Event(event)))
 expect(screen.queryByText('private-origin')).toBeNull()
 expect(screen.getByText('source unavailable')).toBeTruthy()
 expect(dropped).toHaveBeenCalledOnce()
 // Old requests are cancelled by the reader's own unmount cleanup.
 await act(async()=>{finish();await read})
 expect(screen.queryByText('late-private-response')).toBeNull()
 expect(screen.getByText('source unavailable')).toBeTruthy()
})
