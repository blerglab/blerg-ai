import { useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { useChat } from '../context'
import type { TransportFiles } from '../../transport/types'
import { newRequestId, reviewFileName, reviewReplyHistory, type ReviewFile } from '../../model/review'
import { composeReviewMessage } from '../../model/feedback'
import { createWorkspaceAnchor, projectWorkspaceAnchor, validWorkspacePath, workspaceContentHash } from '../../model/workspaceAnchor'
import { WORKSPACE_REVIEW_NAME, workspaceReviewStore, type WorkspaceReviewStore } from '../../model/workspaceReview'
import './workspaceReview.css'

interface WorkspaceReviewProps {
  sessionId:string; files:TransportFiles; path:string; text?:string; blob:Blob; onClose:()=>void
}
export default function WorkspaceReview(props:WorkspaceReviewProps) {
  const store = workspaceReviewStore(props.files, props.sessionId)
  return <WorkspaceReviewSession key={store.key} {...props} store={store} />
}
function WorkspaceReviewSession({sessionId, path, text, blob, onClose, store}:WorkspaceReviewProps & {store:WorkspaceReviewStore}) {
  const chat = useChat()
  const state = useSyncExternalStore(store.subscribe, store.snapshot)
  const [selection, setSelection] = useState<{start:number;end:number} | null>(null)
  const [request, setRequest] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [shown, setShown] = useState(5000)
  const lines = useMemo(()=>text?.split('\n'), [text])
  const [binaryHash, setBinaryHash] = useState<string | null>(null)
  useEffect(()=>{
    if (text !== undefined) return
    let live = true
    void workspaceContentHash(blob).then(hash=>{if(live)setBinaryHash(hash)},()=>{if(live)setError('Could not check file contents.')})
    return ()=>{live=false}
  },[blob,text])
  const [placements, setPlacements] = useState(new Map<string, {start:number;end:number;moved:boolean} | null>())
  useEffect(()=>{
    let live = true
    if (!chat || chat.sessionId !== sessionId) return
    void chat.reviewFor(WORKSPACE_REVIEW_NAME).then(review=>{if(live && review)store.merge(review)}, ()=>{if(live)setError('Could not read earlier replies. Reopen Review to try again.')})
    return ()=>{live=false}
  },[chat, store, sessionId])
  useEffect(()=>{
    let live = true
    void Promise.all(state.requests.filter(r=>r.anchor.workspace?.path===path).map(async r=>[
      r.id, text === undefined ? null : await projectWorkspaceAnchor(r.anchor,text),
    ] as const)).then(entries=>{if(live)setPlacements(new Map(entries))}, ()=>{if(live)setError('Could not check anchor positions.')})
    return ()=>{live=false}
  },[state.requests, path, text])
  function select(line:number, extend:boolean) {
    setSelection(current=>extend && current ? {start:Math.min(current.start,line),end:Math.max(current.start,line)} : {start:line,end:line})
    setError('')
  }
  async function save() {
    if (!request.trim() || saving || (text !== undefined && !selection)) return
    setSaving(true)
    setError('')
    try {
      if (!validWorkspacePath(path)) throw new Error('Invalid workspace path')
      const anchor = text !== undefined && selection
        ? await createWorkspaceAnchor(path,text,selection.start,selection.end)
        : {kind:'text' as const, quote:'', workspace:{path,start:0,end:0,contentHash:await workspaceContentHash(blob)}}
      store.add({id:newRequestId(),anchor,text:request.trim(),status:'open',createdAt:new Date().toISOString()})
      setRequest('')
      setSelection(null)
    } catch (e) { setError(e instanceof Error ? e.message : 'Could not anchor this request.') }
    finally { setSaving(false) }
  }
  async function submit() {
    const current = store.snapshot()
    if (!chat || chat.sessionId !== sessionId || current.sending || !current.pending.size) return
    const sent = new Set(current.pending)
    const review:ReviewFile = {version:1,file:{name:WORKSPACE_REVIEW_NAME,artifactId:'workspace'},requests:current.requests,submittedAt:new Date().toISOString()}
    store.update({sending:true,notice:''})
    try {
      const ok = await chat.submitFeedback(
        composeReviewMessage({...review,requests:review.requests.filter(r=>sent.has(r.id))}, {name:WORKSPACE_REVIEW_NAME}),
        [new File([JSON.stringify(review,null,2)],reviewFileName(WORKSPACE_REVIEW_NAME),{type:'application/json'})],
      )
      if (ok) store.update({pending:new Set([...store.snapshot().pending].filter(id=>!sent.has(id))),notice:'Sent'})
      else store.update({notice:'Could not send. Your requests are still here.'})
    } catch { store.update({notice:'Could not send. Your requests are still here.'}) }
    finally { store.update({sending:false}) }
  }
  return <section className="workspace-review" aria-label="Workspace review">
    <div className="workspace-review-bar">
      <button type="button" className="artifact-btn" onClick={onClose}>Back to file</button>
      <span>Requests across workspace files</span>
      <button type="button" className="artifact-btn" disabled={!state.pending.size || state.sending || !chat || chat.sessionId !== sessionId} onClick={()=>void submit()}>
        {state.sending ? 'Sending…' : `Send ${state.pending.size} request${state.pending.size===1?'':'s'}`}
      </button>
      {state.notice && <span role="status">{state.notice}</span>}
    </div>
    <div className="workspace-review-scroll">
      <p className="artifact-status">{lines ? 'Select a line number. Shift-click another or set the last line to select a range.' : 'This file has no text preview. Add a request about the whole file.'}</p>
      {lines && <div className="workspace-review-source" aria-label={path}>
        {lines.slice(0,shown).map((line,i)=><div className={selection && i+1>=selection.start && i+1<=selection.end?'workspace-review-line selected':'workspace-review-line'} key={i}>
          <button type="button" className="artifact-btn workspace-review-number" aria-label={`Select line ${i+1}`} aria-pressed={!!selection && i+1>=selection.start && i+1<=selection.end} onClick={event=>select(i+1,event.shiftKey)}>{i+1}</button>
          <pre>{line || '\u200b'}</pre>
        </div>)}
        {lines.length>shown && <button type="button" className="artifact-btn" onClick={()=>setShown(n=>n+5000)}>Show more lines</button>}
      </div>}
      <form className="review-form workspace-review-form" onSubmit={event=>{event.preventDefault();void save()}}>
        <p>{selection ? `Lines ${selection.start}–${selection.end}` : lines ? 'Select lines to add a request.' : path}</p>
        {selection && lines && <label>Last line<input type="number" aria-label="Last line" min={selection.start} max={lines.length} value={selection.end} onChange={event=>{
          const end = Number(event.target.value)
          if (Number.isSafeInteger(end) && end>=selection.start && end<=lines.length) setSelection({...selection,end})
        }} /></label>}
        <label>Request<textarea className="review-form-text" aria-label="Request" value={request} maxLength={32768} onChange={event=>setRequest(event.target.value)} /></label>
        <button type="submit" className="artifact-btn" disabled={saving || !request.trim() || (!!lines && !selection)}>{saving?'Saving…':'Save request'}</button>
        {error && <p role="alert">{error}</p>}
      </form>
      <div className="workspace-review-requests">
        {state.requests.map(r=>{
          const place = r.anchor.workspace!
          const projected = placements.get(r.id)
          const currentFile = place.path===path
          return <article className="workspace-review-request" key={r.id}>
            <strong>{r.status==='open'?'Open':r.status==='done'?'Done':'Declined'}</strong>{' '}
            <span>{chat?.openWorkspaceFile ? <button type="button" className="artifact-btn" onClick={()=>chat.openWorkspaceFile?.(place.path,projected?.start ?? (place.start || undefined),{folder:true})}>{place.path}</button> : place.path}{place.start>0 ? `, lines ${projected?.start ?? place.start}–${projected?.end ?? place.end}` : ', whole file'}</span>
            {currentFile && place.start>0 && placements.has(r.id) && <span className="workspace-review-location">{projected ? projected.moved?' · Moved':'' : ' · Outdated: selected lines changed or are ambiguous'}</span>}
            {currentFile && place.start===0 && binaryHash && binaryHash!==place.contentHash && <span className="workspace-review-location"> · Outdated: file contents changed</span>}
            <p>{r.text}</p>
            {reviewReplyHistory(r).map((reply,i)=><p key={i}><strong>{reply.status==='done'?'Done':'Declined'}:</strong> {reply.text}</p>)}
            {state.pending.has(r.id) && <button type="button" className="artifact-btn" disabled={state.sending} onClick={()=>store.remove(r.id)} aria-label={`Remove request: ${r.text}`}>Remove</button>}
          </article>
        })}
      </div>
    </div>
  </section>
}
