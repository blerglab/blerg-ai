// ReviewMode: the review of one published markdown or PDF file, inside the artifact viewer's
// dialog (docs/design/feedback-tools.md, "The viewer"). Markdown: the source on the left (edited in
// place, kept in localStorage until submitted), the rendered page on the right with the requests
// in a margin; PDF: the pages and the margin. Select a passage and press N (or Request change) to
// anchor a request to it; Submit uploads the edited source and the review file and sends the
// composed message through the chat's feedback path.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useChat } from '../context'
import { anchorSelection } from '../../model/anchor'
import { unifiedDiff } from '../../model/diff'
import { composeReviewMessage } from '../../model/feedback'
import { emptyReview, newRequestId, reviewFileName, type Anchor, type ReviewFile, type ReviewRequest } from '../../model/review'
import type { ArtifactInfo } from '../../transport/types'
import MarkdownPage from './MarkdownPage'
import PdfPages from './PdfPages'
import RequestList from './RequestList'
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

export default function ReviewMode({ artifact, text, blob, onClose }: ReviewModeProps) {
  const chat = useChat()
  const rootRef = useRef<HTMLDivElement | null>(null)
  const pageRef = useRef<HTMLDivElement | null>(null)
  const isMarkdown = text !== undefined
  // What the edits are measured against: the published text, then whatever was last submitted
  // (a submitted edit is the session's to apply; pressing Submit again must not resend it).
  const [published, setPublished] = useState(text ?? '')

  const [requests, setRequests] = useState<ReviewRequest[]>([])
  const [edited, setEdited] = useState(() => (isMarkdown ? readEdit(artifact.id) ?? (text ?? '') : ''))
  const [focusedId, setFocusedId] = useState<string | null>(null)
  const [showDiff, setShowDiff] = useState(false)
  const [pane, setPane] = useState<'source' | 'page'>('page')
  // Requests made in this sitting and not yet submitted: the only ones that can be deleted, and
  // (with an edit) what makes Submit worth pressing.
  const [unsent, setUnsent] = useState<ReadonlySet<string>>(() => new Set())
  const [draft, setDraft] = useState<{ anchor: Anchor; text: string } | null>(null)
  const [pdfAnchor, setPdfAnchor] = useState<Anchor | null>(null)
  const [notice, setNotice] = useState<'sent' | 'not-sent' | 'select' | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const changed = isMarkdown && edited !== published
  const canSubmit = !submitting && (changed || unsent.size > 0)

  useEffect(() => rootRef.current?.focus(), [])

  // The existing review (the person's requests merged with the session's replies).
  useEffect(() => {
    if (!chat) return
    let cancelled = false
    void chat.reviewFor(artifact.name).then(
      review => { if (!cancelled && review) setRequests(review.requests) },
      () => {},
    )
    return () => { cancelled = true }
  }, [chat, artifact.name])

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

  function changeSource(value: string) {
    setEdited(value)
    writeEdit(artifact.id, value === published ? null : value)
  }

  function discardEdits() {
    setEdited(published)
    writeEdit(artifact.id, null)
  }

  function openRequest() {
    const sel = window.getSelection()
    const anchor = isMarkdown
      ? (pageRef.current && sel ? anchorSelection(pageRef.current, sel, { sourceLineOf }) : null)
      : pdfAnchor
    if (!anchor) {
      setNotice('select')
      return
    }
    setNotice(null)
    setDraft({ anchor, text: '' })
  }

  function saveRequest() {
    if (!draft || !draft.text.trim()) return
    const r: ReviewRequest = { id: newRequestId(), anchor: draft.anchor, text: draft.text.trim(), status: 'open', createdAt: new Date().toISOString() }
    setRequests(rs => [...rs, r])
    setUnsent(s => new Set(s).add(r.id))
    setDraft(null)
  }

  function deleteRequest(id: string) {
    setRequests(rs => rs.filter(r => r.id !== id))
    setUnsent(s => { const n = new Set(s); n.delete(id); return n })
  }

  async function submit() {
    if (!chat || !canSubmit) return
    setSubmitting(true)
    setNotice(null)
    try {
      const review: ReviewFile = {
        ...emptyReview({ name: artifact.name, artifactId: artifact.id, artifactVersion: artifact.version }),
        requests,
        submittedAt: new Date().toISOString(),
      }
      const files: File[] = []
      if (changed) {
        files.push(new File([edited], artifact.name, { type: 'text/markdown' }))
        // The uploaded copy's id is not known before the upload; the message names the file.
        review.edit = { artifactId: '', diff: unifiedDiff(published, edited, artifact.name) }
      }
      files.push(new File([JSON.stringify(review, null, 2)], reviewFileName(artifact.name), { type: 'application/json' }))
      const ok = await chat.submitFeedback(composeReviewMessage(review, { name: artifact.name, version: artifact.version }), files)
      if (!ok) {
        setNotice('not-sent')
        return
      }
      writeEdit(artifact.id, null)
      setPublished(edited)
      setUnsent(new Set())
      setNotice('sent')
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
    if ((e.key === 'n' || e.key === 'N') && !e.ctrlKey && !e.metaKey && !e.altKey && !draft && !isEditable(e.target)) {
      e.preventDefault()
      openRequest()
    }
  }

  const deletable = useMemo(() => unsent, [unsent])

  return (
    <div
      ref={rootRef}
      className={`review-mode${isMarkdown ? ' review-markdown' : ' review-pdf-mode'}`}
      data-testid="review-mode"
      data-pane={pane}
      tabIndex={-1}
      onKeyDown={onKeyDown}
    >
      <div className="review-bar">
        <button type="button" className="artifact-btn" onClick={onClose}>Back</button>
        {isMarkdown && (
          <div className="review-pane-toggle" role="group" aria-label="Pane">
            <button type="button" className="artifact-btn" aria-pressed={pane === 'source'} onClick={() => setPane('source')}>Source</button>
            <button type="button" className="artifact-btn" aria-pressed={pane === 'page'} onClick={() => setPane('page')}>Page</button>
          </div>
        )}
        {isMarkdown && (
          <button type="button" className="artifact-btn" aria-pressed={showDiff} onClick={() => setShowDiff(d => !d)}>Changes</button>
        )}
        <button type="button" className="artifact-btn" onClick={openRequest}>Request change</button>
        <span className="review-spacer" />
        {notice === 'sent' && <span className="review-notice review-notice-ok" role="status">Sent</span>}
        {notice === 'not-sent' && <span className="review-notice review-notice-bad" role="alert">Not sent — not connected</span>}
        {notice === 'select' && <span className="review-notice" role="status">Select some text on the page first.</span>}
        <button type="button" className="artifact-btn review-submit" disabled={!canSubmit} onClick={() => void submit()}>Submit</button>
      </div>

      {draft && (
        <form
          className="review-form"
          onSubmit={e => { e.preventDefault(); saveRequest() }}
        >
          {draft.anchor.quote && <blockquote className="review-quote">{draft.anchor.quote}</blockquote>}
          <textarea
            className="review-form-text"
            aria-label="Request"
            autoFocus
            rows={2}
            placeholder="What should change here?"
            value={draft.text}
            onChange={e => setDraft(d => (d ? { ...d, text: e.target.value } : d))}
            onKeyDown={e => {
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); saveRequest() }
            }}
          />
          <div className="review-form-actions">
            <button type="submit" className="artifact-btn" disabled={!draft.text.trim()}>Save</button>
            <button type="button" className="artifact-btn" onClick={() => setDraft(null)}>Cancel</button>
          </div>
        </form>
      )}

      <div className="review-panes">
        {isMarkdown && (
          <SourcePane value={edited} published={published} onChange={changeSource} onDiscard={discardEdits} />
        )}
        <div className="review-page-col">
          {isMarkdown ? (
            <MarkdownPage
              source={edited}
              published={published}
              showDiff={showDiff}
              requests={requests}
              focusedId={focusedId}
              rootRef={pageRef}
            />
          ) : blob ? (
            <PdfPages blob={blob} requests={requests} focusedId={focusedId} onHover={setFocusedId} onSelection={setPdfAnchor} />
          ) : null}
        </div>
        <aside className="review-margin" aria-label="Requests">
          <RequestList requests={requests} focusedId={focusedId} onHover={setFocusedId} deletable={deletable} onDelete={deleteRequest} />
        </aside>
      </div>
    </div>
  )
}
