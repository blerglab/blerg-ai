// ReviewMode: the review of one published markdown or PDF file, inside the artifact viewer's
// dialog (docs/design/feedback-tools.md, "The viewer"). Markdown: the source on the left (edited in
// place, kept in localStorage until submitted), with passage-linked requests in a margin; PDF: the pages and the margin. Select a passage and press Ctrl+Alt+M (or Add comment) to
// anchor a request to it; Submit uploads the edited source and the review file and sends the
// composed message through the chat's feedback path.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useChat } from '../context'
import { anchorSelection } from '../../model/anchor'
import { unifiedDiff } from '../../model/diff'
import { composeReviewMessage } from '../../model/feedback'
import { emptyReview, newRequestId, reviewFileName, reopenReviewRequest, type Anchor, type ReviewFile, type ReviewRequest } from '../../model/review'
import type { ArtifactInfo } from '../../transport/types'
import MarkdownPage from './MarkdownPage'
import DiffView from '../DiffView'
import DiffReview, { type ReviewTab } from './DiffReview'
import PdfPages from './PdfPages'
import DocumentMargin from './DocumentMargin'
import DocumentEditor, { richDocumentSupported } from './DocumentEditor'
import CommentEditor from './CommentEditor'
import SourcePane from './SourcePane'
import './review.css'

export interface ReviewModeProps {
  artifact: ArtifactInfo
  /** The markdown source (markdown view). */
  text?: string
  /** The PDF bytes (pdf view). */
  blob?: Blob
  /** Back to the plain viewer. */
  onClose: () => void
  embedded?: boolean
  commit?: string | null
  /** A diff review's first tab, and who is told when it changes (DiffReview). */
  initialTab?: ReviewTab
  onTabChange?: (tab: ReviewTab) => void
}

const EDIT_KEY = (id: string) => `chat.review.edit.${id}`
const SENT_FOR_MS = 2000
const QUOTE_HEAD = 30

function readEdit(id: string): string | null {
  try {
    return localStorage.getItem(EDIT_KEY(id))
  } catch {
    return null
  }
}

function writeEdit(id: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(EDIT_KEY(id))
    else localStorage.setItem(EDIT_KEY(id), value)
  } catch {
    // storage is a convenience: the edit stays in memory for the sitting
  }
}

function isEditable(target: EventTarget | null): boolean {
  return target instanceof HTMLElement && (target.tagName === 'TEXTAREA' || target.tagName === 'INPUT' || target.isContentEditable)
}

export default function ReviewMode(props: ReviewModeProps) {
  const chat = useChat()
  // Review state and pending selections belong to these exact source bytes.
  if(props.artifact.view==='diff'&&props.text!==undefined)return <DiffReview key={`${chat?.sessionId??''}:${props.artifact.id}:${props.text}`} artifact={props.artifact} text={props.text} onClose={props.onClose} embedded={props.embedded} commit={props.commit} initialTab={props.initialTab} onTabChange={props.onTabChange}/>
  return <ReviewSession key={`${chat?.sessionId ?? ''}:${props.artifact.id}:${props.artifact.version ?? ''}:${props.artifact.view}:${props.text ?? ''}`} {...props} />
}

function ReviewSession({ artifact, text, blob, onClose }: ReviewModeProps) {
  const chat = useChat()
  const rootRef = useRef<HTMLDivElement | null>(null)
  const mounted = useRef(false)
  const [anchorSources, setAnchorSources] = useState<ReadonlyMap<string, string>>(new Map())
  const resolvedAnchor = useRef<((id: string, anchor: Anchor) => Range | null) | null>(null)
  const rootRefForLayout = useRef<HTMLDivElement | null>(null)
  const pageRef = useRef<HTMLDivElement | null>(null)
  const isDiff = artifact.view === 'diff' && text !== undefined
  const isMarkdown = !isDiff && text !== undefined
  // What the edits are measured against: the published text, then whatever was last submitted
  // (a submitted edit is the session's to apply; pressing Submit again must not resend it).
  const [published, setPublished] = useState(text ?? '')

  const [invalidIds, setInvalidIds] = useState<ReadonlySet<string>>(new Set())
  const [requests, setRequests] = useState<ReviewRequest[]>([])
  const [edited, setEdited] = useState(() => (isMarkdown ? readEdit(artifact.id) ?? (text ?? '') : ''))
  const editedRef = useRef(edited)
  const unsentRef = useRef(new Set<string>())
  const [diffAnchor, setDiffAnchor] = useState<Anchor | null>(null)
  const [pinnedId] = useState<string | null>(null)
  const [focusedId, setFocusedId] = useState<string | null>(null)
  const [hideResolved, setHideResolved] = useState(false)
  const visibleRequests = requests.filter(r => !hideResolved || r.status === 'open')
  const [showDiff, setShowDiff] = useState(true)
  const [sourceMode, setSourceMode] = useState(false)
  // The rendered document hides words its source holds (MarkdownPage): say so above the page.
  const [hiddenContent, setHiddenContent] = useState(false)
  const safeRich = richDocumentSupported(edited) && richDocumentSupported(published)
  // Requests made in this sitting and not yet submitted: the only ones that can be deleted, and
  // (with an edit) what makes Submit worth pressing.
  const [unsent, setUnsent] = useState<ReadonlySet<string>>(() => new Set())
  const [draft, setDraft] = useState<{ id: string; anchor: Anchor; text: string } | null>(null)
  const [pdfAnchor, setPdfAnchor] = useState<Anchor | null>(null)
  const [notice, setNotice] = useState<'sent' | 'not-sent' | 'select' | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const changed = isMarkdown && edited !== published
  const canSubmit = !submitting && (changed || unsent.size > 0)

  useEffect(() => {
    mounted.current = true
    rootRef.current?.focus()
    return () => { mounted.current = false }
  }, [])

  // The existing review (the person's requests merged with the session's replies).
  useEffect(() => {
    if (!chat) return
    let cancelled = false
    void chat.reviewFor(artifact.name).then(
      review => {
        if (cancelled || !review) return
        setInvalidIds(current => new Set([...current, ...review.requests.filter(r => r.anchor.unavailable || (r.anchor.sourceArtifactId ?? review.file.artifactId) !== artifact.id).map(r => r.id)]))
        setAnchorSources(current => {const next = new Map(current);for (const r of review.requests) if (!next.has(r.id)) next.set(r.id, text ?? '');return next})
        setRequests(current => {
          const local = new Map(current.map(r => [r.id, r]))
          const remoteIds = new Set(review.requests.map(r => r.id))
          return [...review.requests.map(r => (unsentRef.current.has(r.id) || (local.get(r.id)?.revision ?? 0) > (r.revision ?? 0)) ? local.get(r.id) ?? r : r.anchor.kind === 'text' && !r.anchor.diff ? {...r, anchor: {...r.anchor, sourceArtifactId: r.anchor.sourceArtifactId ?? review.file.artifactId}} : r),
            ...current.filter(r => !remoteIds.has(r.id))]
        })
      },
      () => {},
    )
    return () => { cancelled = true }
  }, [chat, artifact.name, artifact.id, text])

  useEffect(() => {
    if (notice !== 'sent') return
    const t = setTimeout(() => setNotice(null), SENT_FOR_MS)
    return () => clearTimeout(t)
  }, [notice])

  const sourceLineOf = useCallback((quote: string): number | undefined => {
    const head = quote.slice(0, QUOTE_HEAD)
    const lines = edited.split('\n')
    const at = lines.findIndex(l => l.includes(head))
    return at < 0 ? undefined : at + 1
  }, [edited])

  const invalidate = useCallback((ids: string[]) => {
    setInvalidIds(current => ids.every(id => current.has(id)) ? current : new Set([...current, ...ids]))
  }, [])

  // Shows the source (with the page) and puts the person in it: every byte the document holds.
  function showSource() {
    setSourceMode(true)
    requestAnimationFrame(() => (rootRef.current?.querySelector('.review-textarea') as HTMLTextAreaElement | null)?.focus())
  }

  function changeSource(value: string) {
    if (sourceMode || !safeRich) invalidate([...requests.map(r => r.id), ...(draft ? [draft.id] : [])])
    setEdited(value)
    editedRef.current = value
    writeEdit(artifact.id, value === published ? null : value)
  }

  function discardEdits() {
    setEdited(published)
    editedRef.current = published
    writeEdit(artifact.id, null)
  }

  function openRequest() {
    if (draft) return
    const sel = window.getSelection()
    const anchor = isDiff ? diffAnchor : isMarkdown
      ? (pageRef.current && sel ? anchorSelection(pageRef.current, sel, { sourceLineOf }) : null)
      : pdfAnchor
    if (!anchor) {
      setNotice('select')
      return
    }
    setNotice(null)
    const id = newRequestId()
    setAnchorSources(current => new Map(current).set(id, edited))
    setDraft({ id, anchor: anchor.kind === 'text' && !anchor.diff ? {...anchor, sourceArtifactId: artifact.id} : anchor, text: '' })
  }

  function saveRequest() {
    if (!draft || !draft.text.trim()) return
    const anchor = draft.anchor.diff ? {...draft.anchor, diff:{...draft.anchor.diff, artifactId:artifact.id}} : draft.anchor
    const r: ReviewRequest = { id: draft.id, anchor, text: draft.text.trim(), status: 'open', createdAt: new Date().toISOString() }
    unsentRef.current.add(r.id)
    setRequests(rs => [...rs, r])
    setUnsent(s => new Set(s).add(r.id))
    setDraft(null)
    setDiffAnchor(null)
  }

  function deleteRequest(id: string) {
    unsentRef.current.delete(id)
    setRequests(rs => rs.filter(r => r.id !== id))
    setUnsent(s => { const n = new Set(s); n.delete(id); return n })
  }

  function reopen(id: string, explanation: string) {
    if (submitting) return
    const request = requests.find(r => r.id === id)
    if (!request || reopenReviewRequest(request, explanation) === request) return
    unsentRef.current.add(id)
    setUnsent(new Set(unsentRef.current))
    setRequests(current => current.map(r => r.id === id ? reopenReviewRequest(r, explanation) : r))
  }

  async function submit() {
    if (!chat || !canSubmit) return
    const sentIds = new Set(unsent)
    setSubmitting(true)
    setNotice(null)
    try {
      const review: ReviewFile = {
        ...emptyReview({ name: artifact.name, artifactId: artifact.id, artifactVersion: artifact.version }),
        requests: requests.map(r => r.anchor.kind === 'text' && !r.anchor.diff && invalidIds.has(r.id) ? {...r, anchor: {...r.anchor, unavailable: true}} : r),
        submittedAt: new Date().toISOString(),
      }
      const files: File[] = []
      if (changed) {
        files.push(new File([edited], artifact.name, { type: 'text/markdown' }))
        // The uploaded copy's id is not known before the upload; the message names the file.
        review.edit = { artifactId: '', diff: unifiedDiff(published, edited, artifact.name) }
      }
      files.push(new File([JSON.stringify(review, null, 2)], reviewFileName(artifact.name), { type: 'application/json' }))
      const ok = await chat.submitFeedback(composeReviewMessage({...review, requests: review.requests.filter(r => sentIds.has(r.id))}, { name: artifact.name, version: artifact.version }), files)
      if (!mounted.current) return
      if (!ok) {
        setNotice('not-sent')
        return
      }
      if (isMarkdown) writeEdit(artifact.id, editedRef.current === edited ? null : editedRef.current)
      setPublished(edited)
      for (const id of sentIds) unsentRef.current.delete(id)
      setUnsent(current => new Set([...current].filter(id => !sentIds.has(id))))
      setNotice('sent')
    } catch {
      setNotice('not-sent')
    } finally {
      setSubmitting(false)
    }
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation()
      if (draft) setDraft(null)
      else onClose()
      return
    }
    if (((e.key.toLowerCase() === 'm' && e.altKey && (e.ctrlKey || e.metaKey)) || ((e.key === 'n' || e.key === 'N') && !e.ctrlKey && !e.metaKey && !e.altKey && !isEditable(e.target))) && !draft) {
      e.preventDefault()
      openRequest()
    }
  }

  const deletable = useMemo(() => submitting ? new Set<string>() : unsent, [unsent, submitting])

  return (
    <div
      ref={rootRef}
      className={`review-mode document-review${sourceMode || !safeRich ? ' document-source-mode' : ''}${isDiff ? ' review-diff-mode' : isMarkdown ? ' review-markdown' : ' review-pdf-mode'}`}
      data-testid="review-mode"
      tabIndex={-1}
      onKeyDown={onKeyDown}
    >
      <div className="review-bar">
        <button type="button" className="artifact-btn" onClick={onClose}>Back</button>
        {isMarkdown && (
          <button type="button" className="artifact-btn" aria-pressed={showDiff} onClick={() => setShowDiff(d => !d)}>{showDiff ? 'Show changes' : 'Clean view'}</button>
        )}
        {isMarkdown && <button type="button" className="artifact-btn" aria-pressed={sourceMode} onClick={() => setSourceMode(value => !value)}>{sourceMode ? 'Document' : 'Source'}</button>}
        {isMarkdown && changed && !sourceMode && safeRich && <button type="button" className="artifact-btn" onClick={discardEdits}>Discard edits</button>}
        <button type="button" className="artifact-btn" onMouseDown={e => e.preventDefault()} onClick={openRequest} disabled={!!draft} title="Add comment (Ctrl+Alt+M)">Add comment</button>
        <label className="review-resolved-toggle" title="Hide resolved and rejected threads"><input type="checkbox" checked={hideResolved} onChange={event => setHideResolved(event.target.checked)}/>Hide resolved</label>
        <span className="review-summary">{requests.filter(r => r.status === 'open').length} open · {requests.filter(r => r.status === 'done').length} resolved · {requests.filter(r => r.status === 'declined').length} rejected</span>
        <span className="review-spacer" />
        {notice === 'sent' && <span className="review-notice review-notice-ok" role="status">Sent</span>}
        {notice === 'not-sent' && <span className="review-notice review-notice-bad" role="alert">Not sent — not connected</span>}
        {notice === 'select' && <span className="review-notice" role="status">{isDiff ? 'Select a line number in the patch first.' : 'Select some text on the page first.'}</span>}
        <button type="button" className="artifact-btn review-submit" disabled={!canSubmit} onClick={() => void submit()}>Submit</button>
      </div>


      {isDiff && <p className="review-diff-hint">Select a line number, or choose Select range and tap its first and last lines; then Request change. Shift-click also selects a range on a desktop.</p>}
      {isMarkdown && !safeRich && <p role="status">This document contains Markdown that needs source editing to preserve it. The preview remains available.</p>}
      {isMarkdown && hiddenContent && (
        <p className="review-hidden-banner" role="status" data-testid="review-hidden-content">
          This document has hidden content (an HTML comment, a closed section or text the page does not show).{' '}
          <button type="button" className="artifact-link" onClick={showSource}>Show source</button>
        </p>
      )}
      <div className="review-panes document-scroll" ref={rootRefForLayout}>
        {isMarkdown && (sourceMode || !safeRich) && (
          <SourcePane value={edited} published={published} onChange={changeSource} onDiscard={discardEdits} />
        )}
        <div className="review-page-col">
          {isDiff ? (
            <DiffView text={text} selection={diffAnchor} onSelection={setDiffAnchor}
              requests={requests.filter(r => r.anchor.diff?.artifactId === artifact.id)} focusedId={focusedId ?? pinnedId} />
          ) : isMarkdown ? (
            safeRich && !sourceMode ? <DocumentEditor key={published} onInvalid={invalidate} invalidIds={invalidIds} eligibleIds={new Set([...requests.map(r => r.id), ...(draft ? [draft.id] : [])].filter(id => anchorSources.get(id) === edited))} requests={draft ? [...requests, {id: draft.id, anchor: draft.anchor, text: draft.text, status: 'open', createdAt: ''}] : requests} resolveRef={resolvedAnchor} value={edited} published={published} showChanges={showDiff} onChange={changeSource} rootRef={pageRef}/> : <MarkdownPage
              source={edited} published={published} showDiff={showDiff} requests={visibleRequests.filter(r => !invalidIds.has(r.id) && (anchorSources.get(r.id) ?? published) === edited)}
              focusedId={focusedId ?? pinnedId} rootRef={pageRef} onHiddenContent={setHiddenContent}
            />
          ) : blob ? (
            <PdfPages blob={blob} requests={visibleRequests.filter(r => !invalidIds.has(r.id))} focusedId={focusedId ?? pinnedId} onHover={setFocusedId} onSelection={setPdfAnchor} />
          ) : null}
        </div>
        <DocumentMargin onReopen={reopen} busy={submitting} pending={unsent} draftId={draft?.id} rich={isMarkdown && safeRich && !sourceMode} canLocate={id => !invalidIds.has(id) && (anchorSources.get(id) ?? published) === edited} resolveRef={resolvedAnchor} root={rootRefForLayout} requests={visibleRequests} draftAnchor={draft?.anchor} draft={draft && (
        <form
          className="review-form"
          onSubmit={e => { e.preventDefault(); saveRequest() }}
          onKeyDown={e => {if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {e.preventDefault();saveRequest()}}}
        >
          {draft.anchor.quote && <blockquote className="review-quote">{draft.anchor.quote}</blockquote>}
          <CommentEditor value={draft.text} onChange={value => setDraft(d => d ? {...d, text: value} : d)} label="Request" autoFocus />
          <div className="review-form-actions">
            <button type="submit" className="artifact-btn" disabled={!draft.text.trim()}>Save</button>
            <button type="button" className="artifact-btn" onClick={() => setDraft(null)}>Cancel</button>
          </div>
        </form>
      )}
          focusedId={focusedId ?? pinnedId} onFocus={setFocusedId} deletable={deletable} onDelete={deleteRequest} updateKey={edited + showDiff + sourceMode}/>

      </div>
    </div>
  )
}
