import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { apiFetch } from '../apiFetch'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { useToastStore } from '../hooks/useToastStore'
import { useSignInEpoch } from '../signIn'
import './WhatsNew.css'

interface Release { version: string; title: string; items: string[] }
interface Notes { current: string; since: string; pending: boolean; releases: Release[] }
const released = (version: string) => /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(version)
async function readNotes(method: 'GET' | 'POST', release: string): Promise<Notes> {
  const response = await apiFetch('/api/me/whats-new?release=' + encodeURIComponent(release), {method})
  if (!response.ok) throw new Error('Release notes unavailable')
  return response.json()
}

export default function WhatsNew() {
  const release = useSessionStore(s => s.serverRelease)
  const sessions = useSessionStore(s => s.sessions)
  const messages = useMessageStore(s => s.messages)
  const epoch = useSignInEpoch()
  const location = useLocation()
  const [blocked, setBlocked] = useState(true)
  const [notice, setNotice] = useState<{notes: Notes; manual: boolean} | null>(null)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [request, setRequest] = useState(0)
  const attempted = useRef('')
  const lastRequest = useRef(0)
  const selected = location.pathname.match(/^\/sessions\/([^/]+)/)?.[1]
  const sessionBlocked = sessions.some(s => s.status === 'waiting' || (s.id === selected && s.interaction === 'unattended'))
  const questionBlocked = messages.some(m => m.kind === 'ask' && m.status === 'open' && !m.no_wait)
  useEffect(() => {
    const check = () => setBlocked(document.visibilityState === 'hidden' || !!document.querySelector('[role="dialog"]:not([data-whats-new]), [role="alertdialog"], dialog[open]:not([data-whats-new])'))
    check()
    const observer = new MutationObserver(check)
    observer.observe(document.body, {childList:true, subtree:true, attributes:true, attributeFilter:['open','role']})
    document.addEventListener('visibilitychange',check)
    return () => {observer.disconnect();document.removeEventListener('visibilitychange',check)}
  },[])
  const safe = !blocked && !sessionBlocked && !questionBlocked && released(release)
  const key = release + ':' + epoch
  const identity = useRef('')
  useEffect(() => {
    identity.current=key
    return () => {identity.current=''}
  },[key])
  useEffect(() => {
    const open = () => {
      if (!released(release)) return
      setRequest(value => value+1)
    }
    window.addEventListener('blerg:whats-new',open)
    return () => window.removeEventListener('blerg:whats-new',open)
  },[release])
  useEffect(() => {
    const manual=request>lastRequest.current
    if (!released(release) || (!safe && !manual)) return
    if (!manual && attempted.current===key) return
    if (manual) {lastRequest.current=request;attempted.current=key}
    const generation=request
    readNotes('GET',release).then(notes => {
      if (lastRequest.current!==generation || identity.current!==key) return
      attempted.current=key
      if (manual || notes.pending) setNotice({notes,manual})
      setError('')
    }).catch(() => {
      if (lastRequest.current!==generation || identity.current!==key) return
      attempted.current=key
      if (manual) useToastStore.getState().addToast({title:'What’s New',body:'Could not load release notes. Open What’s New again to retry.'})
    })
  },[safe,release,key,request])
  async function dismiss() {
    if (!notice || saving) return
    setSaving(true)
    try {
      await readNotes('POST',notice.notes.current)
      setNotice(null)
      setError('')
    } catch {
      setError('Could not save your dismissal. Try Done again.')
    } finally {setSaving(false)}
  }
  if (!notice || (!safe && !notice.manual)) return null
  const releases=notice.notes.releases.filter(r => notice.manual || compareVersions(r.version,notice.notes.since)>0)
  return <NotesDialog onClose={() => {void dismiss()}} saving={saving}>
    <h2 id="whats-new-title">What’s New</h2>
    {error && <p role="alert">{error}</p>}
    {releases.map(r => <section key={r.version}><h3>Blerg {r.version} · {r.title}</h3><ul>{r.items.map(item => <li key={item}>{item}</li>)}</ul></section>)}
    {releases.length===0 && <p>No release notes for this version yet.</p>}
  </NotesDialog>
}

function compareVersions(a:string,b:string) {
  const parts=b.replace(/^v/,'').split('.').map(Number)
  for (const [i,value] of a.replace(/^v/,'').split('.').map(Number).entries()) {
    if (value!==parts[i]) return value-(parts[i]??0)
  }
  return 0
}
function NotesDialog({children,onClose,saving}:{children:React.ReactNode;onClose:()=>void;saving:boolean}) {
  const dialog=useRef<HTMLDialogElement>(null)
  const done=useRef<HTMLButtonElement>(null)
  const [maximized,setMaximized]=useState(false)
  useEffect(() => {
    const previous=document.activeElement as HTMLElement|null
    dialog.current?.showModal()
    done.current?.focus()
    return () => {previous?.focus()}
  },[])
  return <dialog ref={dialog} data-whats-new className={'whats-new'+(maximized?' is-maximized':'')} aria-labelledby="whats-new-title" onKeyDown={event => {
    if (event.key!=='Tab') return
    const buttons=Array.from(dialog.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)')??[])
    const first=buttons[0],last=buttons.at(-1)
    if (!first || !last) return
    if (event.shiftKey && document.activeElement===first) {event.preventDefault();last.focus()}
    else if (!event.shiftKey && document.activeElement===last) {event.preventDefault();first.focus()}
  }} onCancel={event => {event.preventDefault();onClose()}}>
    <button
      type="button"
      className="whats-new-maximize"
      aria-label={maximized?'Restore size':'Maximize'}
      aria-pressed={maximized}
      title={maximized?'Restore size':'Maximize'}
      onClick={() => setMaximized(value => !value)}
    ><span aria-hidden="true">{maximized?'⤡':'⤢'}</span></button>
    <div className="whats-new-content">{children}</div>
    <footer>
      <button ref={done} className="btn primary" disabled={saving} onClick={onClose}>{saving?'Saving…':'Done'}</button>
    </footer>
  </dialog>
}
