import { useEffect, useRef, useState } from 'react'
import { withCsp } from '../model/artifacts'
import { answerResult } from '../model/answerResult'
import type { AnswerRequest, TransportFiles } from '../transport/types'

/** Only published artifact viewers mount this bridge; workspace and inline previews stay inert. */
export default function AnswerPage({sessionId, artifactId, name, text, files}: {
  sessionId: string; artifactId: string; name: string; text: string; files: TransportFiles
}) {
  const frame = useRef<HTMLIFrameElement>(null)
  const [request, setRequest] = useState<AnswerRequest | null>(null)
  const [candidate, setCandidate] = useState<string | null>(null)
  const [snapshot, setSnapshot] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [sent, setSent] = useState(false)
  const [accepted, setAccepted] = useState(false)
  const frozen = useRef(false)
  const [error, setError] = useState('')
  const sending = useRef(false)
  const completed = useRef(false)
  const loaded = useRef(false)
  useEffect(() => {
    let active = true
    files.answerRequest?.(sessionId, artifactId).then(r => {
      if (active) {
        setRequest(r); setSent(r?.delivered ?? false); completed.current = r?.delivered ?? false
        if (r && 'result' in r) {setSnapshot(JSON.stringify(r.result)); setAccepted(true); frozen.current = true}
      }
    }, () => { /* No bridge for an ordinary file, an older server, or a nonowner. */ })
    return () => { active = false }
  }, [files, sessionId, artifactId])
  useEffect(() => {
    const receive = (event: MessageEvent) => {
      if (event.source !== frame.current?.contentWindow || completed.current || sending.current) return
      // Invalid updates revoke the candidate instead of leaving an older answer enabled.
      setCandidate(answerResult(event.data))
    }
    window.addEventListener('message', receive)
    return () => window.removeEventListener('message', receive)
  }, [])
  const clear = () => {
    // A srcdoc script may post before its first load event reaches the parent.
    if (loaded.current && !frozen.current) {setCandidate(null); setSnapshot(null)}
    loaded.current = true
  }
  const submit = async () => {
    if (snapshot === null || sending.current || completed.current || !files.submitAnswer) return
    sending.current = true; setBusy(true); setError('')
    try {
      await files.submitAnswer(sessionId, artifactId, snapshot)
      completed.current = true; setSent(true)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not send the answer. Try again.')
      // An ambiguous failure may have saved the answer before delivery. Restore that exact
      // value for retry; another tab cannot make this viewer submit a different answer.
      try {
        const r = await files.answerRequest?.(sessionId, artifactId)
        if (r && 'result' in r) {setSnapshot(JSON.stringify(r.result)); setAccepted(true); frozen.current = true}
        if (r?.delivered) {completed.current = true; setSent(true)}
      } catch { /* Keep the reviewed snapshot available for an identical retry. */ }
    }
    finally {sending.current = false; setBusy(false)}
  }
  return <div className="answer-page">
    <iframe ref={frame} className="artifact-frame artifact-html" title={name} sandbox="allow-scripts" referrerPolicy="no-referrer" srcDoc={withCsp(text)} onLoad={clear}/>
    {request && files.submitAnswer && <section className="answer-controls" aria-label="Page answer">
      <p>{request.ask}</p>
      {sent ? <p role="status">Answer sent.</p> : <>
        {snapshot === null ? <>
          <p>{candidate === null ? 'Use the page, then review your result.' : 'Your result is ready to review.'}</p>
          <button type="button" className="artifact-btn" disabled={candidate === null} onClick={() => {setSnapshot(candidate); setError('')}}>Review result</button>
        </> : <>
          <p>This JSON will be sent to the workspace:</p>
          <pre tabIndex={0}>{snapshot}</pre>
          <div className="answer-actions">
            <button type="button" className="artifact-btn" disabled={busy} onClick={() => {void submit()}}>{busy ? 'Sending…' : 'Submit'}</button>
            {!accepted && <button type="button" className="artifact-btn" disabled={busy} onClick={() => {setSnapshot(null); setError('')}}>Back to editing</button>}
          </div>
        </>}
        {error && <p role="alert">{error}</p>}
      </>}
    </section>}
  </div>
}
