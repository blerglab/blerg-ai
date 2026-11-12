import { useEffect, useMemo, useState } from 'react'
import { createPortal } from 'react-dom'
import { ViewerAction } from './ViewerToolbar'
import type { ArtifactInfo, TransportFiles } from '../transport/types'
import type { ReviewFile } from '../model/review'
import { TEXT_VIEW_MAX, ARTIFACT_VIEWS } from '../model/artifactViews'
import { focusVersionPatch } from '../model/versionFocus'
import { versionPatch } from '../model/versionPatch'
import { useChat } from './context'
import DiffView from './DiffView'
import { useReviewRequests } from './reviewRequestContext'

// Comparison is read-only: review anchors always belong to the document's immutable version.
export default function VersionComparison({ artifact, files, sessionId, active, onActive, controlsHost, currentText }: {
  artifact: ArtifactInfo; files: TransportFiles; sessionId: string; active: boolean; onActive: (active: boolean) => void; controlsHost?: HTMLElement | null; currentText?: string
}) {
  const chat = useChat()
  const reviewRequests = useReviewRequests()
  const [versions, setVersions] = useState<ArtifactInfo[]>([])
  const [previous, setPrevious] = useState<ArtifactInfo | null>(null)
  const [from, setFrom] = useState('')
  const [to, setTo] = useState(artifact.id)
  const [loadedResult, setResult] = useState<{ key: string; text?: string; error?: string } | null>(null)
  const resultKey = `${from}\0${to}`
  const result = loadedResult?.key === resultKey ? loadedResult : null
  const [review, setReview] = useState<ReviewFile | null>(null)
  const [focus, setFocus] = useState(false)
  const [quote, setQuote] = useState('')
  useEffect(() => {
    if (typeof files.list !== 'function' || currentText === undefined) return
    let live = true
    void files.list(sessionId).then(async items => {
      if (!live) return
      const same = items.filter(a => a.name === artifact.name && (a.origin === 'user') === (artifact.origin === 'user') && typeof a.version === 'number' && typeof artifact.version === 'number').sort((a, b) => (a.version ?? 1) - (b.version ?? 1))
      setVersions(same)
      const comparable = (a: ArtifactInfo) => !a.missing && a.size <= TEXT_VIEW_MAX && ARTIFACT_VIEWS[a.view as keyof typeof ARTIFACT_VIEWS]?.textual
      if (artifact.missing || artifact.size > TEXT_VIEW_MAX || !ARTIFACT_VIEWS[artifact.view as keyof typeof ARTIFACT_VIEWS]?.textual) return
      try {
        const after = currentText
        for (const candidate of same.filter(a => (a.version ?? 1) < (artifact.version ?? 1)).reverse()) {
          if (!comparable(candidate)) continue
          try {
            const blob = await files.raw(sessionId, candidate.id)
            if (!live) return
            if (blob.size > TEXT_VIEW_MAX) continue
            const before = await blob.text()
            if (!live) return
            if (before !== after) {
              setPrevious(candidate)
              setFrom(candidate.id)
              break
            }
          } catch { /* Unavailable bytes cannot establish a difference. */ }
        }
      } catch { /* The document remains available if comparison cannot be verified. */ }
    }, () => {})
    return () => { live = false }
  }, [files, sessionId, artifact.id, artifact.name, artifact.origin, artifact.version, currentText, artifact.missing, artifact.size, artifact.view])
  useEffect(() => {
    if (!chat) return
    let live = true
    void chat.reviewFor(artifact.name).then(r => { if (live) setReview(r) }, () => {})
    return () => { live = false }
  }, [chat, artifact.name])
  useEffect(() => {
    if (!active) return
    let live = true
    const a = versions.find(v => v.id === from), b = versions.find(v => v.id === to)
    if (!a || !b) return
    void (async () => {
      try {
        if ([a, b].some(v => v.missing || v.size > TEXT_VIEW_MAX || !ARTIFACT_VIEWS[v.view as keyof typeof ARTIFACT_VIEWS]?.textual)) throw new Error('These versions cannot be compared as text.')
        const blobs = await Promise.all([files.raw(sessionId, a.id), files.raw(sessionId, b.id)])
        if (blobs.some(blob => blob.size > TEXT_VIEW_MAX)) throw new Error('These versions are too large to compare.')
        const [before, after] = await Promise.all(blobs.map(blob => blob.text()))
        if (live) setResult({ key: resultKey, text: versionPatch(before, after, artifact.name) })
      } catch (e) { if (live) setResult({ key: resultKey, error: e instanceof Error ? e.message : 'Could not compare these versions.' }) }
    })()
    return () => { live = false }
  }, [active, from, to, resultKey, versions, files, sessionId, artifact.name])
  const focusedPatch = useMemo(() => result?.text && quote ? focusVersionPatch(result.text, quote) : null, [result, quote])
  if (!previous) return null
  function startComparison(requested = false) {
    setFrom(previous!.id)
    setTo(artifact.id)
    setQuote('')
    setFocus(requested)
    onActive(true)
  }
  const requests = review?.requests.filter(r => versions.some(v => v.id === (r.anchor.sourceArtifactId ?? r.anchor.diff?.artifactId ?? review.file.artifactId) && (v.version ?? 1) < (artifact.version ?? 1))) ?? []
  const earlierDecisions = versions.filter(v => (v.version ?? 1) < (artifact.version ?? 1)).flatMap(v => {
    const decision = reviewRequests?.forArtifact(v.id)
    return decision?.state === 'changes_requested' && decision.note ? [{ version: v, note: decision.note }] : []
  })
  const hasRequests = requests.length > 0 || earlierDecisions.length > 0
  const controls = <>
    <ViewerAction icon="changes" label={active ? 'Document' : `Changes since v${previous.version ?? 1}`} title={active ? 'Return to document' : `Compare v${previous.version ?? 1} with v${artifact.version ?? 1}`} pressed={active} onClick={() => active ? onActive(false) : startComparison()} />
    {hasRequests && <ViewerAction icon="review" label="What changed for requested items" pressed={active && focus} onClick={() => startComparison(true)} />}
    {active && <span className="viewer-comparison-options">
      <label>From <select aria-label="Compare from" value={from} onChange={e => setFrom(e.target.value)}>{versions.map(v => <option key={v.id} value={v.id}>v{v.version ?? 1}</option>)}</select></label>
      <label>To <select aria-label="Compare to" value={to} onChange={e => setTo(e.target.value)}>{versions.map(v => <option key={v.id} value={v.id}>v{v.version ?? 1}</option>)}</select></label>
    </span>}
  </>
  return <>
    {controlsHost === undefined ? controls : controlsHost ? createPortal(controls, controlsHost) : null}
    {active && <section className="artifact-comparison is-active" aria-label="Version comparison">
      {focus && hasRequests && <aside aria-label="Requested items">{earlierDecisions.map(d => <p key={d.version.id}><button type="button" className="artifact-btn" onClick={() => { setFrom(d.version.id); setTo(artifact.id); setQuote('') }}>{d.note}</button> — v{d.version.version}</p>)}{requests.map(r => <p key={r.id}><button className="artifact-btn" type="button" onClick={() => { setQuote(r.anchor.quote ?? ''); const source = r.anchor.sourceArtifactId ?? r.anchor.diff?.artifactId ?? review?.file.artifactId; if (versions.some(v => v.id === source)) setFrom(source!); setTo(artifact.id) }}>{r.text}</button>{r.anchor.quote && <q>{r.anchor.quote}</q>}{r.reply && <span> — {r.reply}</span>}</p>)}</aside>}
      <p className="artifact-meta">Comparison is read-only. Return to Document to comment on v{artifact.version ?? 1}.</p>
      {!result ? <p role="status">Loading comparison…</p> : result.error ? <p role="alert">{result.error}</p> : result.text ? <>{quote && <p>{focusedPatch ? 'Showing the requested passage and nearby lines.' : 'The requested passage could not be located. Showing all changes.'} <button className="artifact-btn" type="button" onClick={() => setQuote('')}>Show all changes</button></p>}<DiffView text={focusedPatch ?? result.text} /></> : <p>No changes.</p>}
    </section>}
  </>
}
