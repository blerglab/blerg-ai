import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useChat } from '../context'
import DiffView from '../DiffView'
import Markdown from '../Markdown'
import CommentEditor from './CommentEditor'
import ReopenThread from './ReopenThread'
import ThreadHistory from './ThreadHistory'
import { parsePatch } from '../../model/patch'
import { projectReviewAnchor } from '../../model/reviewProjection'
import { composeReviewMessage } from '../../model/feedback'
import {
  emptyReview,
  newRequestId,
  reviewFileName,
  reopenReviewRequest,
  type Anchor,
  type ReviewFile,
  type ReviewRequest,
} from '../../model/review'
import type { ArtifactInfo } from '../../transport/types'
import './review.css'

type Draft = {
  anchor: Anchor
  text: string
  suggestion?: { original: string; replacement: string }
}
const statusLabel = { open: 'Open', done: 'Resolved', declined: 'Rejected' }

export default function DiffReview({
  artifact,
  text,
  onClose,
  embedded = false,
  commit,
}: {
  artifact: ArtifactInfo
  text: string
  onClose: () => void
  embedded?: boolean
  commit?: string | null
}) {
  const chat = useChat()
  const [requests, setRequests] = useState<ReviewRequest[]>([])
  const [selection, setSelection] = useState<Anchor | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [unsent, setUnsent] = useState<ReadonlySet<string>>(new Set())
  const unsentRef = useRef(new Set<string>())
  const [submitting, setSubmitting] = useState(false)
  const [notice, setNotice] = useState('')
  const [focused, setFocused] = useState<string | null>(null)
  const [hideResolved, setHideResolved] = useState(false)
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const visible = useMemo(
    () => requests.filter((r) => !hideResolved || r.status === 'open'),
    [requests, hideResolved],
  )
  const patch = useMemo(() => parsePatch(text), [text])
  const projections = useMemo(
    () =>
      new Map(
        requests.map((r) => [
          r.id,
          projectReviewAnchor(patch, r.anchor, artifact.id),
        ]),
      ),
    [patch, requests, artifact.id],
  )
  const mapped = useMemo(
    () =>
      visible.flatMap((r) => {
        const anchor = projections.get(r.id)
        return anchor ? [{ ...r, anchor }] : []
      }),
    [visible, projections],
  )
  const draftInPatch = draft?.anchor.diff
    ? projectReviewAnchor(
        patch,
        {
          ...draft.anchor,
          diff: { ...draft.anchor.diff, artifactId: artifact.id },
        },
        artifact.id,
      )
    : null
  const openComment = useCallback(
    (anchor: Anchor) => {
      if (draft && (draft.text || draft.suggestion)) {
        setNotice('Save or cancel your current comment first')
        return
      }
      setDraft({ anchor, text: '' })
      setSelection(anchor)
      setNotice('')
    },
    [draft],
  )
  useEffect(() => {
    if (!chat) return
    let cancelled = false
    void chat.reviewFor(artifact.name).then(
      (review) => {
        if (cancelled || !review) return
        setRequests((current) => {
          const local = new Map(current.map((r) => [r.id, r]))
          const remoteIds = new Set(review.requests.map((r) => r.id))
          return [
            ...review.requests.map((r) => {
              const own = local.get(r.id)
              return own &&
                (unsentRef.current.has(r.id) ||
                  (own.revision ?? 0) > (r.revision ?? 0))
                ? own
                : r
            }),
            ...current.filter((r) => !remoteIds.has(r.id)),
          ]
        })
      },
      () => {},
    )
    return () => {
      cancelled = true
    }
  }, [chat, artifact.name])
  function save() {
    if (!draft || (!draft.text.trim() && !draft.suggestion)) return
    if (
      draft.suggestion &&
      (draft.suggestion.original.includes('\0') ||
        draft.suggestion.replacement.includes('\0'))
    ) {
      setNotice('Suggestions cannot contain NUL characters')
      return
    }
    const anchor = draft.anchor.diff
      ? {
          ...draft.anchor,
          diff: { ...draft.anchor.diff, artifactId: artifact.id },
        }
      : draft.anchor
    const request: ReviewRequest = {
      id: newRequestId(),
      anchor,
      text: draft.text.trim() || (draft.suggestion ? 'Suggested change' : ''),
      status: 'open',
      createdAt: new Date().toISOString(),
      ...(draft.suggestion ? { suggestion: draft.suggestion } : {}),
    }
    unsentRef.current.add(request.id)
    setUnsent(new Set(unsentRef.current))
    setRequests((rs) => [...rs, request])
    setDraft(null)
    setSelection(null)
  }
  function remove(id: string) {
    unsentRef.current.delete(id)
    setUnsent(new Set(unsentRef.current))
    setRequests((rs) => rs.filter((r) => r.id !== id))
  }
  function reopen(id: string, explanation: string) {
    const request = requests.find(r => r.id === id)
    if (submitting || !request || reopenReviewRequest(request, explanation) === request) return
    if (requests.some((r) => r.id === id && (r.revision ?? 0) >= 1_000_000)) {
      setNotice('This thread has reached its revision limit')
      return
    }
    unsentRef.current.add(id)
    setUnsent(new Set(unsentRef.current))
    setRequests(rs => rs.map(r => r.id === id ? reopenReviewRequest(r, explanation) : r))
  }

  async function submit() {
    if (!chat || submitting || !unsent.size) return
    const sentIds = new Set(unsent)
    setSubmitting(true)
    setNotice('')
    const review: ReviewFile = {
      ...emptyReview({
        name: artifact.name,
        artifactId: artifact.id,
        artifactVersion: artifact.version,
      }),
      requests,
      submittedAt: new Date().toISOString(),
    }
    try {
      const feedback = {
        ...review,
        requests: requests.filter((r) => sentIds.has(r.id)),
      }
      const ok = await chat.submitFeedback(
        composeReviewMessage(feedback, {
          name: artifact.name,
          version: artifact.version,
        }),
        [
          new File(
            [JSON.stringify(review, null, 2)],
            reviewFileName(artifact.name),
            { type: 'application/json' },
          ),
        ],
      )
      if (!ok) {
        setNotice('Not sent — not connected')
        return
      }
      for (const id of sentIds) unsentRef.current.delete(id)
      setUnsent(
        (current) => new Set([...current].filter((id) => !sentIds.has(id))),
      )
      setNotice('Sent')
    } catch {
      setNotice('Not sent — please retry')
    } finally {
      setSubmitting(false)
    }
  }
  function composer() {
    if (!draft) return null
    return (
      <form
        className="review-form review-inline-composer"
        aria-label={
          draft.anchor.kind === 'general' ? 'General comment' : 'Inline comment'
        }
        onSubmit={(event) => {
          event.preventDefault()
          save()
        }}
        onKeyDown={(event) => {
          if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
            event.preventDefault()
            save()
          }
        }}
      >
        <strong>
          {draft.anchor.kind === 'general'
            ? 'General comment'
            : 'Comment on selected code'}
        </strong>
        <CommentEditor
          label="Request"
          value={draft.text}
          onChange={(value) => setDraft((d) => (d ? { ...d, text: value } : d))}
          autoFocus
          onSuggest={
            draft.anchor.diff?.side === 'new'
              ? () =>
                  setDraft((d) =>
                    d
                      ? {
                          ...d,
                          suggestion: {
                            original: d.anchor.quote ?? '',
                            replacement: d.anchor.quote ?? '',
                          },
                        }
                      : d,
                  )
              : undefined
          }
        />
        {draft.suggestion && (
          <div className="review-suggestion-editor">
            <strong>Suggested change</strong>
            <pre className="review-suggestion-before">
              {draft.suggestion.original}
            </pre>
            <label>
              Replacement code
              <textarea
                aria-label="Suggested replacement"
                className="review-form-text"
                rows={Math.min(
                  12,
                  Math.max(3, draft.suggestion.replacement.split('\n').length),
                )}
                value={draft.suggestion.replacement}
                maxLength={32768}
                onChange={(event) =>
                  setDraft((d) =>
                    d?.suggestion
                      ? {
                          ...d,
                          suggestion: {
                            ...d.suggestion,
                            replacement: event.target.value,
                          },
                        }
                      : d,
                  )
                }
              />
            </label>
            <button
              type="button"
              className="artifact-btn"
              onClick={() =>
                setDraft((d) => (d ? { ...d, suggestion: undefined } : d))
              }
            >
              Remove suggestion
            </button>
          </div>
        )}
        <div className="review-form-actions">
          <button
            type="submit"
            className="artifact-btn"
            disabled={!draft.text.trim() && !draft.suggestion}
          >
            Save
          </button>
          <button
            type="button"
            className="artifact-btn"
            onClick={() => {
              setDraft(null)
              setSelection(null)
            }}
          >
            Cancel
          </button>
        </div>
      </form>
    )
  }
  function thread(projected: ReviewRequest, outdated = false) {
    const r = requests.find((item) => item.id === projected.id) ?? projected
    const isCollapsed = r.status !== 'open' && collapsed.has(r.id)
    return (
      <article
        className={`review-request review-inline-thread status-${r.status}`}
        data-testid="review-request"
      >
        <header className="review-request-head">
          <span className={`review-badge review-badge-${r.status}`}>
            {statusLabel[r.status]}
          </span>
          {r.status !== 'open' && (
            <button
              type="button"
              className="artifact-btn"
              aria-expanded={!isCollapsed}
              aria-label={`${isCollapsed ? 'Expand' : 'Collapse'} thread: ${r.text}`}
              onClick={() => {
                setCollapsed((current) => {
                  const next = new Set(current)
                  if (next.has(r.id)) next.delete(r.id)
                  else next.add(r.id)
                  return next
                })
              }}
            >
              {isCollapsed ? 'Expand thread' : 'Collapse thread'}
            </button>
          )}
          {unsent.has(r.id) && (
            <span className="review-pending">Pending review</span>
          )}
          {outdated && (
            <span className="review-outdated">
              Outdated · original code changed or is ambiguous
            </span>
          )}
          {!outdated &&
            r.anchor.diff?.artifactId &&
            r.anchor.diff.artifactId !== artifact.id && (
              <span className="review-outdated">Earlier revision</span>
            )}
          {unsent.has(r.id) && r.revision === undefined && !submitting && (
            <button
              type="button"
              className="review-delete"
              aria-label={`Delete request: ${r.text}`}
              onClick={() => remove(r.id)}
            >
              ✕
            </button>
          )}
        </header>
        {isCollapsed ? (
          <p className="review-thread-summary">{r.text}</p>
        ) : (
          <>
            {outdated && (
              <>
                <p className="review-where">
                  {r.anchor.diff?.file} · {r.anchor.diff?.side} lines{' '}
                  {r.anchor.diff?.start}–{r.anchor.diff?.end}
                </p>
                <pre className="review-original-code">{r.anchor.quote}</pre>
              </>
            )}
            <Markdown text={r.text} />
            {r.suggestion && (
              <div className="review-suggestion">
                <strong>Suggested change</strong>
                <pre className="review-suggestion-before">
                  {r.suggestion.original}
                </pre>
                <pre className="review-suggestion-after">
                  {r.suggestion.replacement || '(delete selected lines)'}
                </pre>
              </div>
            )}
            <ThreadHistory request={r}/>
            {!outdated && r.anchor.diff && (
              <button
                type="button"
                className="artifact-btn"
                aria-label={`Show lines for request: ${r.text}`}
                onClick={() => setFocused(r.id)}
              >
                Show code
              </button>
            )}
            {r.status !== 'open' && <ReopenThread request={r} busy={submitting} onReopen={explanation => reopen(r.id, explanation)}/>}
          </>
        )}
      </article>
    )
  }
  const general = visible.filter((r) => r.anchor.kind === 'general')
  const outdated = visible.filter(
    (r) => r.anchor.kind !== 'general' && !projections.get(r.id),
  )
  return (
    <div
      className="review-mode review-diff-mode review-inline-mode"
      data-testid="review-mode"
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          if (draft) {
            event.stopPropagation()
            setDraft(null)
          } else if (!embedded) {
            event.stopPropagation()
            onClose()
          }
        }
      }}
    >
      <div className="review-bar">
        {!embedded && (
          <button type="button" className="artifact-btn" onClick={onClose}>
            Back
          </button>
        )}
        <button
          type="button"
          className="artifact-btn"
          onClick={() => openComment({ kind: 'general' })}
        >
          Add general comment
        </button>
        {selection && !draft && (
          <button
            type="button"
            className="artifact-btn"
            onClick={() => openComment(selection)}
          >
            Request change
          </button>
        )}
        <label className="review-resolved-toggle" title="Hide resolved and rejected threads">
          <input
            type="checkbox"
            checked={hideResolved}
            onChange={(event) => setHideResolved(event.target.checked)}
          />
          Hide resolved
        </label>
        <span className="review-spacer" />
        <span className="review-summary">
          {requests.filter((r) => r.status === 'open').length} open ·{' '}
          {requests.filter((r) => r.status === 'done').length} resolved ·{' '}
          {requests.filter((r) => r.status === 'declined').length} rejected
        </span>
        {notice && (
          <span role={notice === 'Sent' ? 'status' : 'alert'}>{notice}</span>
        )}
        <button
          type="button"
          className="artifact-btn review-submit"
          disabled={submitting || !unsent.size}
          onClick={() => void submit()}
        >
          {submitting ? 'Sending…' : 'Submit'}
        </button>
      </div>
      <div className="review-inline-scroll">
        <DiffView
          text={text}
          commit={commit}
          selection={selection}
          onSelection={setSelection}
          requests={mapped}
          focusedId={focused}
          onComment={openComment}
          draftAnchor={draftInPatch}
          renderDraft={() => composer()}
          renderThread={(r) => thread(r)}
        />
        {draft && !draftInPatch && composer()}
        {!!general.length && (
          <section className="review-general">
            <h3>General comments</h3>
            {general.map((r) => (
              <div key={r.id}>{thread(r)}</div>
            ))}
          </section>
        )}
        {!!outdated.length && (
          <section className="review-general">
            <h3>Earlier revision comments</h3>
            {outdated.map((r) => (
              <div key={r.id}>{thread(r, true)}</div>
            ))}
          </section>
        )}
      </div>
    </div>
  )
}
