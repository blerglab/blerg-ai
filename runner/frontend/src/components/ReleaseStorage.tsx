import {useRef, useState} from 'react'
import {ConfirmPopover} from '@blerglab/chat'
import {apiFetch} from '../apiFetch'
import './ReleaseStorage.css'
import './IconButton.css'
import '@blerglab/chat/blerg.css'

interface Preview { digest: string; file_count?: number; commit_count?: number; truncated?: boolean; files: string[]; commits: string[]; released: boolean }

export default function ReleaseStorage({sessionId}: {sessionId: string}) {
 const anchor=useRef<HTMLButtonElement>(null)
 const [preview,setPreview]=useState<Preview|null>(null)
 const [busy,setBusy]=useState(false)
 const [error,setError]=useState('')
 const [released,setReleased]=useState(false)
 async function request(confirm?: string) {
  setBusy(true);setError('')
  try {
   const res=await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/workspace/storage`,confirm?{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({confirm})}:undefined)
   const body=await res.json()
   if(!res.ok)throw new Error(typeof body.error==='string'?body.error:'Could not read session storage. Try again.')
   if(body.released===true){setReleased(true);setPreview(null)}
   else if(typeof body.digest==='string' && body.digest.length===64 && Array.isArray(body.files) && Array.isArray(body.commits)){setPreview(body)}
   else throw new Error('The desktop returned an incomplete storage preview. Try again.')
  }catch(e){setError(e instanceof Error?e.message:'Could not release storage. Try again.')}
  finally{setBusy(false)}
 }
 return <>
  <button ref={anchor} type="button" className="icon-btn danger" title={released?'Storage released':'Release storage'} aria-label={released?'Storage released':'Release storage'} aria-busy={busy||undefined} disabled={busy||released} onClick={()=>void request()}>
   <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true"><ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v12c0 4 16 4 16 0V5M4 11c0 4 16 4 16 0M9 15l6 6m0-6-6 6"/></svg>
  </button>
  {error&&!preview&&<span role="status" className="release-storage-error">{error}</span>}
  {preview&&<ConfirmPopover anchor={anchor} title="Release this session’s storage?" confirmLabel="Release storage" busy={busy} closeOnConfirm={false} onConfirm={()=>void request(preview.digest)} onCancel={()=>{setPreview(null);setError('')}} description={<>
   <span className="release-storage-paragraph">This permanently deletes the session’s local copy and frees its reserved disk space. History and commits already saved to the source repositories stay available. You cannot resume this copy. The reflog is not kept. Commits reachable only from the reflog will be lost, including commits removed by git reset --hard.</span>
   {preview.truncated&&<span className="release-storage-paragraph" role="status">Showing {preview.files.length} of {preview.file_count ?? preview.files.length} file or folder entries and {preview.commits.length} of {preview.commit_count ?? preview.commits.length} references. Release deletes the entire copy, including entries not shown.</span>}
   <span className="release-storage-losses">
    <span className="release-storage-paragraph">Commits or references that may not be saved:</span>
    {preview.commits.length?<span role="list">{preview.commits.map(commit=><span role="listitem" key={commit}><code>{commit}</code></span>)}</span>:<span className="release-storage-paragraph">{(preview.commit_count ?? 0)>0?'Not shown in this preview.':'None.'}</span>}
    <span className="release-storage-paragraph">Ignored, untracked, or modified files that will be lost:</span>
    {preview.files.length?<span role="list">{preview.files.map(file=><span role="listitem" key={file}><code>{file}</code></span>)}</span>:<span className="release-storage-paragraph">{(preview.file_count ?? 0)>0?'Not shown in this preview.':'None.'}</span>}
   </span>
   {error&&<span role="alert" className="release-storage-paragraph">{error}</span>}
  </>}/>}
 </>
}
