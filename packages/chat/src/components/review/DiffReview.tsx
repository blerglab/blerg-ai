import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { useChat } from '../context'
import DiffView from '../DiffView'
import Markdown from '../Markdown'
import CommentEditor from './CommentEditor'
import ReopenThread from './ReopenThread'
import ThreadActions from './ThreadActions'
import ThreadHistory from './ThreadHistory'
import ReviewConversation from './ReviewConversation'
import { useReviewHistory } from './useReviewHistory'
import { parsePatch } from '../../model/patch'
import { projectReviewAnchor } from '../../model/reviewProjection'
import { threadFreshness } from '../../model/reviewConversation'
import { composeReviewMessage } from '../../model/feedback'
import {
  emptyReview,
  newRequestId,
  reviewFileName,
  reopenReviewRequest,
  replyToReviewRequest,
  settleReviewRequest,
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
/** A thread's text for a control's accessible name: its first line, at most 80 characters. */
export function shortLabel(text: string): string {
  const line = text.split('\n')[0].trim()
  return line.length > 80 ? `${line.slice(0, 79)}…` : line
}
/** Who set a settled thread's status, in the words the bar uses. */
const settler = (r: ReviewRequest) => (r.settledBy === 'person' ? 'the person' : 'the agent')
export type ReviewTab = 'conversation' | 'files'
const TABS: readonly ReviewTab[] = ['conversation', 'files']

export default function DiffReview({
  artifact,
  text,
  onClose,
  embedded = false,
  commit,
  initialTab = 'files',
  onTabChange,
}: {
  artifact: ArtifactInfo
  text: string
  onClose: () => void
  embedded?: boolean
  commit?: string | null
  /** The viewer opens on Conversation, as GitHub does, unless it was opened at a commit. */
  initialTab?: ReviewTab
  /** Told which tab shows (the viewer's review-request bar drops its ask on Conversation). */
  onTabChange?: (tab: ReviewTab) => void
}) {
  const chat = useChat()
  const [requests, setRequests] = useState<ReviewRequest[]>([])
  const [selection, setSelection] = useState<Anchor | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [unsent, setUnsent] = useState<ReadonlySet<string>>(new Set())
  const unsentRef = useRef(new Set<string>())
  const [submitting, setSubmitting] = useState(false)
  const [notice, setNotice] = useState('')
  // Entries of the review copies left out as malformed (say so; the rest of the review stands).
  const [ignored, setIgnored] = useState(0)
  const [focused, setFocused] = useState<string | null>(null)
  // Conversation (the pull request page) or Files (the diff, with its threads inline).
  const [tab, setTabState] = useState<ReviewTab>(initialTab)
  // The Conversation mounts the first time it shows and is then kept (what it read stays).
  const [conversationSeen, setConversationSeen] = useState(initialTab === 'conversation')
  const setTab = useCallback((next: ReviewTab) => {
    setTabState(next)
    if (next === 'conversation') setConversationSeen(true)
  }, [])
  useEffect(() => { onTabChange?.(tab) }, [tab, onTabChange])
  const tabsId = useId()
  const [jump, setJump] = useState<{ id: string; at: number } | null>(null)
  // A commit chosen from the Conversation; a new `commit` from the host replaces it.
  const [chosenCommit, setChosenCommit] = useState<{ sha: string; at: number } | null>(null)
  const [hostCommit, setHostCommit] = useState(commit)
  if (hostCommit !== commit) {
    setHostCommit(commit)
    setChosenCommit(null)
    // A commit asked for while the Conversation shows: show it, in Files.
    if (commit) setTabState('files')
  }
  const commitTarget = chosenCommit?.sha ?? commit ?? null
  const [submitted, setSubmitted] = useState(0)
  const { history, retry: retryHistory } = useReviewHistory(artifact, submitted)
  const historyLoading = !!chat?.files && !history
  const versionOf = useMemo(
    () => new Map((history?.versions ?? []).map((a) => [a.id, a.version ?? 1])),
    [history],
  )
  const [conversationFocus, setConversationFocus] = useState<{ id: string; at: number } | null>(null)
  const rootRef = useRef<HTMLDivElement | null>(null)
  const [hideResolved, setHideResolved] = useState(false)
  // Settled threads start collapsed to one bar on their line; these are the ones opened.
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const visible = useMemo(
    () => requests.filter((r) => !hideResolved || r.status === 'open'),
    [requests, hideResolved],
  )
  const patch = useMemo(() => parsePatch(text), [text])
  // Where each thread sits in this patch: on its code (this version's, or the same code found
  // unchanged in it). An Outdated thread (its code changed in a later version) is not drawn in
  // Files, as on GitHub: it lives in the Conversation with its original code, and opening the
  // version it was written on shows it on its line.
  const placements = useMemo(
    () => new Map(requests.map((r) => [r.id, projectReviewAnchor(patch, r.anchor, artifact.id)])),
    [patch, requests, artifact.id],
  )
  const mapped = useMemo(
    () =>
      visible.flatMap((r) => {
        const anchor = placements.get(r.id)
        return anchor ? [{ ...r, anchor }] : []
      }),
    [visible, placements],
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
        setIgnored(review.ignored ?? 0)
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
    remember(request)
    unsentRef.current.add(id)
    setUnsent(new Set(unsentRef.current))
    setRequests(rs => rs.map(r => r.id === id ? reopenReviewRequest(r, explanation) : r))
  }

  // Each thread as it was before this sitting's pending changes: Undo puts it back, before Submit.
  const [beforePending, setBeforePending] = useState<ReadonlyMap<string, ReviewRequest>>(new Map())
  function remember(request: ReviewRequest) {
    setBeforePending(m => (m.has(request.id) ? m : new Map(m).set(request.id, request)))
  }
  function undo(id: string) {
    const before = beforePending.get(id)
    if (submitting || !before) return
    setBeforePending(m => {
      const next = new Map(m)
      next.delete(id)
      return next
    })
    unsentRef.current.delete(id)
    setUnsent(new Set(unsentRef.current))
    setRequests(rs => rs.map(r => r.id === id ? before : r))
  }
  function undoButton(r: ReviewRequest) {
    if (!unsent.has(r.id) || !beforePending.has(r.id) || submitting) return null
    return (
      <button type="button" className="artifact-btn review-undo" aria-label={`Undo pending change: ${shortLabel(r.text)}`} onClick={() => undo(r.id)}>
        Undo
      </button>
    )
  }

  /** A person's action on an existing thread, queued for the next Submit like a new comment. */
  function act(id: string, change: (request: ReviewRequest) => ReviewRequest) {
    const request = requests.find(r => r.id === id)
    if (submitting || !request) return
    if ((request.revision ?? 0) >= 1_000_000) {
      setNotice('This thread has reached its revision limit')
      return
    }
    const next = change(request)
    if (next === request) return
    remember(request)
    unsentRef.current.add(id)
    setUnsent(new Set(unsentRef.current))
    setRequests(rs => rs.map(r => r.id === id ? next : r))
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
      setBeforePending(m => new Map([...m].filter(([id]) => !sentIds.has(id))))
      setUnsent(
        (current) => new Set([...current].filter((id) => !sentIds.has(id))),
      )
      setNotice('Sent')
      setSubmitted((n) => n + 1)
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
  function toggle(id: string) {
    setExpanded((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }
  /** A thread jumped to from the Conversation: Files, opened, its lines marked and in view. */
  function jumpTo(id: string) {
    setTab('files')
    setExpanded((current) => new Set(current).add(id))
    setFocused(id)
    setJump({ id, at: Date.now() })
  }
  // The same commit chosen again (after scrolling away) scrolls to it again.
  useEffect(() => {
    if (!chosenCommit) return
    rootRef.current?.querySelector<HTMLElement>('.diff-commit.target')?.scrollIntoView?.({ block: 'start' })
  }, [chosenCommit])
  useEffect(() => {
    if (!jump) return
    const row = [...(rootRef.current?.querySelectorAll<HTMLElement>('[data-inline-request]') ?? [])]
      .find((el) => el.dataset.inlineRequest === jump.id)
    row?.scrollIntoView?.({ block: 'center' })
  }, [jump])
  /** where: 'conversation' draws the body only; the Conversation's card has the place and code. */
  function thread(projected: ReviewRequest, outdated = false, where: 'files' | 'conversation' = 'files') {
    const inFiles = where === 'files'
    const r = requests.find((item) => item.id === projected.id) ?? projected
    const settled = r.status !== 'open'
    // A settled thread stays where it was, as one bar (GitHub's "resolved conversation").
    if (settled && !expanded.has(r.id)) {
      return (
        <article
          className={`review-request review-inline-thread review-thread-collapsed status-${r.status}`}
          data-testid="review-request"
          data-collapsed="true"
        >
          <div className="review-thread-bar">
            <span
              className={`review-thread-bar-state status-${r.status}`}
              title={`${r.status === 'declined' ? 'Rejected' : 'Resolved'}: conversation by ${settler(r)}`}
            >
              {r.status === 'declined' ? '✕ Rejected' : '✓ Resolved'}
            </span>{' '}
            <span className="review-thread-bar-who">
              <span aria-hidden="true">· </span>conversation by {settler(r)}
            </span>{' '}
            {outdated && inFiles && <><span className="review-outdated"><span aria-hidden="true">· </span>Outdated</span>{' '}</>}
            <span className="review-thread-summary">{r.text}</span>{' '}
            {unsent.has(r.id) && <span className="review-pending">Pending</span>}
            {undoButton(r)}
            <button
              type="button"
              className="artifact-btn review-thread-bar-show"
              aria-expanded={false}
              aria-label={`Show thread: ${shortLabel(r.text)}`}
              onClick={() => toggle(r.id)}
            >
              Show
            </button>
          </div>
        </article>
      )
    }
    return (
      <article
        className={`review-request review-inline-thread status-${r.status}`}
        data-testid="review-request"
      >
        <header className="review-request-head">
          <span className={`review-badge review-badge-${r.status}`}>
            {statusLabel[r.status]}
          </span>
          {settled && (
            <button
              type="button"
              className="artifact-btn"
              aria-expanded
              aria-label={`Hide thread: ${shortLabel(r.text)}`}
              onClick={() => toggle(r.id)}
            >
              Hide
            </button>
          )}
          {unsent.has(r.id) && (
            <span className="review-pending">Pending review</span>
          )}
          {undoButton(r)}
          {outdated && inFiles && (
            <span className="review-outdated">
              Outdated · original code changed or is ambiguous
            </span>
          )}
          {!outdated && inFiles &&
            r.anchor.diff?.artifactId &&
            r.anchor.diff.artifactId !== artifact.id && (
              <span className="review-outdated">Earlier revision</span>
            )}
          {unsent.has(r.id) && r.revision === undefined && !submitting && (
            <button
              type="button"
              className="review-delete"
              aria-label={`Delete request: ${shortLabel(r.text)}`}
              onClick={() => remove(r.id)}
            >
              ✕
            </button>
          )}
        </header>
        {outdated && inFiles && (
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
        {!outdated && inFiles && r.anchor.diff && (
          <button
            type="button"
            className="artifact-btn"
            aria-label={`Show lines for request: ${shortLabel(r.text)}`}
            onClick={() => setFocused(r.id)}
          >
            Show code
          </button>
        )}
        {!(unsent.has(r.id) && r.revision === undefined) && (
          <div className="review-thread-controls">
            <ThreadActions
              request={r}
              busy={submitting}
              onReply={text => act(r.id, request => replyToReviewRequest(request, text))}
              onSettle={status => act(r.id, request => settleReviewRequest(request, status))}
            />
            {settled && <ReopenThread request={r} busy={submitting} onReopen={explanation => reopen(r.id, explanation)}/>}
          </div>
        )}
      </article>
    )
  }
  const general = visible.filter((r) => r.anchor.kind === 'general')
  const unplaced = visible.filter(
    (r) => r.anchor.kind !== 'general' && !placements.get(r.id),
  )
  // A thread from a later version than the open one is not Outdated: its code is not written yet.
  const openVersion = artifact.version ?? 0
  const isLater = (r: ReviewRequest) => {
    const v = r.anchor.diff?.artifactId ? versionOf.get(r.anchor.diff.artifactId) : undefined
    return v !== undefined && openVersion > 0 && v > openVersion
  }
  // The same words as the Conversation's labels, from the same freshness against the newest
  // version: Outdated (its code changed), not in vN (its file left the diff), from a later version,
  // else not in this version. While the versions are still being read, nothing is called outdated.
  const versionList = history?.versions.some((a) => a.id === artifact.id) ? history.versions : [...(history?.versions ?? []), artifact]
  const newestArtifact = versionList.reduce((a, b) => ((b.version ?? 0) >= (a.version ?? 0) ? b : a), artifact)
  const newestText = newestArtifact.id === artifact.id ? text : history?.texts.get(newestArtifact.id)
  const newest = newestText === undefined ? null : { id: newestArtifact.id, patch: newestArtifact.id === artifact.id ? patch : parsePatch(newestText) }
  const bucket = (r: ReviewRequest) => {
    if (historyLoading) return 'checking'
    if (isLater(r)) return 'later'
    const fresh = threadFreshness(r.anchor, newest)
    return fresh === 'outdated' ? 'outdated' : fresh === 'absent' ? 'absent' : 'elsewhere'
  }
  const buckets = { outdated: 0, absent: 0, later: 0, elsewhere: 0, checking: 0 }
  for (const r of unplaced) buckets[bucket(r)]++
  const unplacedOpen = unplaced.filter((r) => r.status === 'open').length
  const plural = (n: number) => (n === 1 ? 'conversation' : 'conversations')
  const noticeParts = [
    buckets.outdated && `${buckets.outdated} outdated ${plural(buckets.outdated)}`,
    buckets.absent && `${buckets.absent} ${plural(buckets.absent)} not in v${newestArtifact.version ?? 1}`,
    buckets.later && `${buckets.later} ${plural(buckets.later)} from a later version`,
    (buckets.elsewhere + buckets.checking) && `${buckets.elsewhere + buckets.checking} ${plural(buckets.elsewhere + buckets.checking)} not in this version`,
  ].filter(Boolean) as string[]
  const placedInFiles = new Set(mapped.map((r) => r.id))
  return (
    <div
      ref={rootRef}
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
        <span
          className="review-tabs"
          role="tablist"
          aria-label="Review view"
          onKeyDown={(event) => {
            if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
            event.preventDefault()
            const next = event.key === 'Home' ? TABS[0] : event.key === 'End' ? TABS[TABS.length - 1] : TABS[(TABS.indexOf(tab) + 1) % TABS.length]
            setTab(next)
            document.getElementById(`${tabsId}-tab-${next}`)?.focus()
          }}
        >
          {TABS.map((name) => (
            <button
              key={name}
              id={`${tabsId}-tab-${name}`}
              type="button"
              role="tab"
              className="artifact-btn"
              aria-selected={tab === name}
              aria-controls={`${tabsId}-panel-${name}`}
              tabIndex={tab === name ? 0 : -1}
              onClick={() => setTab(name)}
            >
              {name === 'conversation' ? 'Conversation' : 'Files'}
            </button>
          ))}
        </span>
        <button
          type="button"
          className="artifact-btn"
          onClick={() => openComment({ kind: 'general' })}
        >
          Add general comment
        </button>
        {selection && !draft && tab === 'files' && (
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
        {(() => {
          const open = requests.filter((r) => r.status === 'open').length
          const done = requests.filter((r) => r.status === 'done').length
          const declined = requests.filter((r) => r.status === 'declined').length
          return (
            <span className="review-summary">
              <span className="review-summary-words">{open} open · {done} resolved · {declined} rejected</span>
              <span className="review-summary-chips">
                <span title={`${open} open`} aria-label={`${open} open`} role="img">● {open}</span>
                <span title={`${done} resolved`} aria-label={`${done} resolved`} role="img">✓ {done}</span>
                <span title={`${declined} rejected`} aria-label={`${declined} rejected`} role="img">✕ {declined}</span>
              </span>
            </span>
          )
        })()}
        {notice && (
          <span role={notice === 'Sent' ? 'status' : 'alert'}>{notice}</span>
        )}
        {ignored > 0 && (
          <span className="review-pending" role="status" data-testid="review-ignored" title="An agent reopen with an empty or too long reason, or a bad revision, was left out; the rest of the review is shown.">
            {ignored === 1 ? '1 malformed entry left out' : `${ignored} malformed entries left out`}
          </span>
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
      {conversationSeen && (
        <ReviewConversation
          id={`${tabsId}-panel-conversation`}
          labelledBy={`${tabsId}-tab-conversation`}
          hidden={tab !== 'conversation'}
          history={history}
          loading={historyLoading}
          onRetry={retryHistory}
          artifact={artifact}
          text={text}
          threads={visible}
          placedInFiles={placedInFiles}
          renderThread={(r) => thread(r, false, 'conversation')}
          onJump={jumpTo}
          onOpenCommit={(sha) => {
            setTab('files')
            setChosenCommit({ sha, at: Date.now() })
          }}
          composer={draft && !draftInPatch && tab === 'conversation' ? composer() : null}
          focus={conversationFocus}
        />
      )}
      {/* Files stays mounted (its scroll, folds and selection) while the Conversation shows. */}
      <div
        className="review-inline-scroll"
        role="tabpanel"
        id={`${tabsId}-panel-files`}
        aria-labelledby={`${tabsId}-tab-files`}
        style={tab === 'files' ? undefined : { display: 'none' }}
      >
        {!!unplaced.length && (
          <p className="review-files-notice" data-testid="review-unplaced-notice">
            {noticeParts.join(' · ')}
            {!!unplacedOpen && <> ({unplacedOpen} open)</>}
            {!!buckets.checking && <span className="review-pending"> · checking the versions…</span>}
            {' · '}
            <button
              type="button"
              className="artifact-link"
              aria-label={`View ${unplaced.length} ${buckets.outdated === unplaced.length ? 'outdated ' : ''}${plural(unplaced.length)} in the Conversation`}
              onClick={() => {
                setTab('conversation')
                setConversationFocus({ id: unplaced[0].id, at: Date.now() })
              }}
            >
              view
            </button>
          </p>
        )}
        <DiffView
          text={text}
          commit={commitTarget}
          selection={selection}
          onSelection={setSelection}
          requests={mapped}
          focusedId={focused}
          onComment={openComment}
          draftAnchor={draftInPatch}
          renderDraft={() => composer()}
          renderThread={(r) => thread(r)}
        />
        {draft && !draftInPatch && tab === 'files' && composer()}
        {!!general.length && (
          <section className="review-general">
            <h3>General comments</h3>
            {general.map((r) => (
              <div key={r.id}>{thread(r)}</div>
            ))}
          </section>
        )}
      </div>
    </div>
  )
}
