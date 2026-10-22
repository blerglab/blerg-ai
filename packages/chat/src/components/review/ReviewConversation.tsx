// ReviewConversation: the Conversation tab of a diff review, like a pull request's page. At the
// top the body (the agent's ask when it requested the review); beneath it the timeline: each
// version published (its files, +/− and commits), each review requested and decided, each review
// the person submitted with the threads it started, each thread with the few lines of code it is
// attached to (marked Outdated when a later version changed them). A thread's code jumps to it in
// Files. Read from what is stored: the diff's versions, the person's review uploads, the review
// requests (model/reviewConversation.ts).
import { useEffect, useMemo, useRef, type ReactNode } from 'react'
import { useChat } from '../context'
import { useReviewRequests } from '../reviewRequestContext'
import Markdown from '../Markdown'
import { CommitChips } from '../CommitChips'
import { TimeLabel } from '../TimeLabel'
import { parsePatch, patchSummary, patchSummaryText, type Patch } from '../../model/patch'
import type { ReviewRequest } from '../../model/review'
import { reviewOutcomeLine } from '../../model/reviewRequests'
import {
  reviewTimeline,
  submittedRounds,
  threadExcerpt,
  threadFreshness,
  type Excerpt,
  type RevisionInfo,
  type SubmittedRound,
} from '../../model/reviewConversation'
import type { ArtifactInfo } from '../../transport/types'
import type { ReviewRequestPayload } from '../../types'
import type { ReviewHistory } from './useReviewHistory'
import '../diff.css'
import './review.css'

export default function ReviewConversation({
  artifact,
  text,
  threads,
  placedInFiles,
  renderThread,
  onJump,
  onOpenCommit,
  composer,
  history,
  loading = false,
  onRetry,
  focus = null,
  id,
  labelledBy,
  hidden = false,
}: {
  artifact: ArtifactInfo
  text: string
  /** The review's threads as they stand now (the person's unsent ones included). */
  threads: readonly ReviewRequest[]
  /** Threads drawn in this version's Files view: their code jumps there. */
  placedInFiles: ReadonlySet<string>
  /** A thread's body: its comment, suggestion, replies, state and Reopen. */
  renderThread: (request: ReviewRequest) => ReactNode
  onJump: (id: string) => void
  /** Opens this version's Files at a commit's section. */
  onOpenCommit: (sha: string) => void
  /** The general-comment composer, when one is open. */
  composer?: ReactNode
  /** The diff's versions and the person's submitted reviews (useReviewHistory). */
  history: ReviewHistory | null
  /** The history is still being read: the timeline waits, so it is drawn once, in order. */
  loading?: boolean
  /** Read again what could not be read (history.error). */
  onRetry?: () => void
  /** The tabpanel's id, and the tab that labels it. */
  id?: string
  labelledBy?: string
  /** Kept mounted (and what it read) while Files shows. */
  hidden?: boolean
  /** A thread to bring into view (Files' "N outdated conversations · view"). */
  focus?: { id: string; at: number } | null
}) {
  const chat = useChat()
  const reviewRequests = useReviewRequests()

  const rootRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    if (!focus) return
    const card = [...(rootRef.current?.querySelectorAll<HTMLElement>('[data-thread]') ?? [])].find(el => el.dataset.thread === focus.id)
    card?.scrollIntoView?.({ block: 'center' })
  }, [focus, history])
  const versions = useMemo(() => {
    const list = history?.versions ?? []
    return list.some(a => a.id === artifact.id)
      ? list
      : [...list, artifact].sort((a, b) => (a.version ?? 0) - (b.version ?? 0))
  }, [history, artifact])
  const patches = useMemo(() => {
    const out = new Map<string, Patch>([[artifact.id, parsePatch(text)]])
    for (const [id, body] of history?.texts ?? []) out.set(id, parsePatch(body))
    return out
  }, [history, artifact.id, text])
  const byId = useMemo(() => new Map(versions.map(a => [a.id, a])), [versions])
  const newestArtifact = versions[versions.length - 1]
  const newest = newestArtifact && patches.has(newestArtifact.id)
    ? { id: newestArtifact.id, patch: patches.get(newestArtifact.id)! }
    : null
  const requests = useMemo(
    () => versions.flatMap(a => {
      const r = reviewRequests?.forArtifact(a.id)
      return r ? [r] : []
    }),
    [versions, reviewRequests],
  )
  const revisions = useMemo<RevisionInfo[]>(
    () => versions.map(a => {
      const patch = patches.get(a.id)
      return { artifact: a, summary: patch ? patchSummary(patch) : undefined, commits: patch?.commits ?? [] }
    }),
    [versions, patches],
  )
  const rounds = useMemo(() => submittedRounds(history?.uploads ?? []), [history])
  const timeline = useMemo(
    () => reviewTimeline({ revisions, rounds, threads, requests }),
    [revisions, rounds, threads, requests],
  )
  const threadById = useMemo(() => new Map(threads.map(t => [t.id, t])), [threads])
  // The body: the newest ask the agent wrote when it requested a review of this diff.
  const body = [...requests].reverse().find(r => r.ask.trim())

  const versionOf = (id: string | undefined) => (id ? byId.get(id)?.version : undefined)
  const label = (a: ArtifactInfo) => `v${a.version ?? 1}`

  function openVersion(a: ArtifactInfo, sha?: string) {
    if (a.id === artifact.id) {
      if (sha) onOpenCommit(sha)
      return
    }
    const open = chat?.viewDiff?.(a.id)
    if (open) open(sha)
    else chat?.view(a)
  }

  function threadCard(id: string) {
    const r = threadById.get(id)
    if (!r) return null
    const range = r.anchor.diff
    const own = range?.artifactId ? byId.get(range.artifactId) : undefined
    const patch = range?.artifactId ? patches.get(range.artifactId) ?? null : patches.get(artifact.id) ?? null
    const excerpt = threadExcerpt(patch, r.anchor)
    const fresh = threadFreshness(r.anchor, newest)
    const placed = placedInFiles.has(r.id)
    const jump = placed ? () => onJump(r.id) : own && own.id !== artifact.id ? () => openVersion(own) : undefined
    const where = range ? `${range.file}:${range.start === range.end ? range.start : `${range.start}–${range.end}`}` : null
    return (
      <article key={r.id} className={`review-conv-thread status-${r.status}`} data-testid="conversation-thread" data-thread={r.id}>
        <header className="review-conv-thread-head">
          {where ? (
            <button
              type="button"
              className="review-conv-where"
              disabled={!jump}
              title={placed ? 'Show in Files' : own && own.id !== artifact.id ? `Open ${label(own)}` : undefined}
              aria-label={placed ? `Show in Files: ${where}` : own && own.id !== artifact.id ? `Open ${label(own)}: ${where}` : where}
              onClick={jump}
            >
              {where}
            </button>
          ) : (
            <span className="review-conv-where">{r.anchor.kind === 'general' ? 'General comment' : 'Passage'}</span>
          )}
          {own && <span className="review-conv-meta">on {label(own)}</span>}
          {fresh === 'outdated' && <span className="review-conv-outdated" title="The code changed in a later version">Outdated</span>}
          {fresh === 'absent' && newestArtifact && <span className="review-conv-outdated review-conv-absent" title="This file is not in the newest version of the diff">Not in {label(newestArtifact)}</span>}
        </header>
        {excerpt ? (
          <ExcerptView excerpt={excerpt} onClick={jump} />
        ) : r.anchor.quote ? (
          <blockquote className="review-quote">{r.anchor.quote}</blockquote>
        ) : null}
        <div className="review-conv-thread-body">{renderThread(r)}</div>
      </article>
    )
  }

  function round(entry: SubmittedRound) {
    const on = entry.artifactId ? byId.get(entry.artifactId) : undefined
    const version = on ? label(on) : entry.version ? `v${entry.version}` : null
    // Hide resolved hides some of the round's threads: the count says what is drawn.
    const n = entry.started.filter(id => threadById.has(id)).length
    const hidden = entry.started.length - n
    return (
      <>
        <p className="review-conv-line">
          <span className="review-conv-icon" aria-hidden="true">✎</span>
          <strong>Review submitted</strong>
          {version && <> on {version}</>}
          {n > 0 && <> · {n} {n === 1 ? 'comment' : 'comments'}</>}
          {hidden > 0 && <> · {hidden} hidden</>}
          {entry.reopened.length > 0 && <> · {entry.reopened.length} reopened</>}
          {(['reply', 'resolve', 'reject'] as const).map(kind => {
            const k = entry.actions.filter(x => x.action === kind).length
            return k > 0 ? <span key={kind}> · {k} {kind === 'reply' ? (k === 1 ? 'reply' : 'replies') : kind === 'resolve' ? 'resolved' : 'rejected'}</span> : null
          })}
        </p>
        {[
          ...entry.reopened.map(id => ({ id, word: 'reopened', key: `reopened:${id}`, at: undefined as string | undefined })),
          ...entry.actions.map((x, i) => ({ id: x.id, word: x.action === 'reply' ? 'replied to' : x.action === 'resolve' ? 'resolved' : 'rejected', key: `${x.action}:${x.id}:${i}`, at: x.at })),
        ].map(({ id, word, key, at }) => {
          const t = threadById.get(id)
          return t ? (
            <p key={key} className="review-conv-subline" data-testid="round-action">
              The person {word}{' '}
              <button type="button" className="artifact-link" onClick={() => [...(rootRef.current?.querySelectorAll<HTMLElement>('[data-thread]') ?? [])].find(el => el.dataset.thread === id)?.scrollIntoView({ block: 'center' })}>{t.text.split('\n')[0]}</button>
              {at && <> <TimeLabel ts={at} /></>}
            </p>
          ) : null
        })}
        <div className="review-conv-threads">{entry.started.map(threadCard)}</div>
      </>
    )
  }

  function requestLine(r: ReviewRequestPayload, decided: boolean) {
    const on = byId.get(r.artifact_id)
    const version = on ? label(on) : `v${r.artifact_version}`
    if (!decided) {
      return (
        <>
          <p className="review-conv-line">
            <span className="review-conv-icon" aria-hidden="true">?</span>
            <strong>Review requested</strong> on {version}
          </p>
          {r.ask && r !== body && <p className="review-conv-subline">{r.ask}</p>}
        </>
      )
    }
    const icon = r.state === 'approved' ? '✓' : r.state === 'changes_requested' ? '±' : '·'
    return (
      <>
        <p className={`review-conv-line review-conv-decision state-${r.state}`}>
          <span className="review-conv-icon" aria-hidden="true">{icon}</span>
          <strong>{reviewOutcomeLine(r)}</strong> on {version}
        </p>
        {r.note && r.state !== 'withdrawn' && <p className="review-conv-subline">{r.note}</p>}
      </>
    )
  }

  function revision(info: RevisionInfo) {
    const a = info.artifact
    const current = a.id === artifact.id
    return (
      <>
        <p className="review-conv-line">
          <span className="review-conv-icon" aria-hidden="true">↑</span>
          <strong>{label(a)}</strong>
          {info.summary && <> · {patchSummaryText(info.summary)}</>}
          {current ? (
            <span className="review-conv-meta">(open)</span>
          ) : (
            <button type="button" className="artifact-link" onClick={() => openVersion(a)}>View {label(a)}</button>
          )}
        </p>
        {info.commits.length > 0 && (
          <ul className="review-conv-commits">
            {info.commits.map(c => (
              <li key={c.sha}>
                <CommitChips commits={[{ sha: c.sha }]} onOpen={sha => openVersion(a, sha)} />
                <span className="review-conv-commit-subject">{c.subject || '(no message)'}</span>
              </li>
            ))}
          </ul>
        )}
      </>
    )
  }

  const open = threads.filter(t => t.status === 'open').length
  const outdatedCount = threads.filter(t => threadFreshness(t.anchor, newest) === 'outdated').length
  return (
    <div
      ref={rootRef}
      className="review-conversation"
      data-testid="review-conversation"
      role="tabpanel"
      id={id}
      aria-labelledby={labelledBy}
      hidden={hidden}
      style={hidden ? { display: 'none' } : undefined}
    >
      <div className="review-conv-main">
        <section className="review-conv-body" aria-label="Description">
          <header className="review-conv-body-head">
            <strong>{body ? 'The agent' : 'Description'}</strong>
            {body && <span className="review-conv-meta">asked for a review of v{versionOf(body.artifact_id) ?? body.artifact_version}</span>}
          </header>
          {body ? (
            <div className="review-conv-body-text" data-testid="review-conversation-body">
              <Markdown text={body.ask} />
            </div>
          ) : (
            <p className="review-conv-placeholder" data-testid="review-conversation-body-empty">
              No description. When the agent asks for a review, its words show here.
            </p>
          )}
        </section>
        {history?.error && (
          <p className="review-conv-error" role="alert" data-testid="review-conversation-error">
            {history.error} Some versions, summaries or rounds may be missing.{' '}
            {onRetry && <button type="button" className="artifact-btn" onClick={onRetry}>Retry</button>}
          </p>
        )}
        {loading ? (
          <p className="review-conv-placeholder" role="status">Loading the conversation…</p>
        ) : (
        <ol className="review-conv-timeline" aria-label="Timeline">
          {timeline.map(entry => (
            <li key={entry.key} className={`review-conv-entry review-conv-${entry.kind}`} data-testid="timeline-entry" data-kind={entry.kind}>
              {entry.at && entry.kind !== 'thread' && <TimeLabel ts={entry.at} className="review-conv-time" />}
              {entry.kind === 'revision'
                ? revision(entry.revision)
                : entry.kind === 'round'
                  ? round(entry.round)
                  : entry.kind === 'thread'
                    ? threadCard(entry.id)
                    : requestLine(entry.request, entry.kind === 'decision')}
            </li>
          ))}
        </ol>
        )}
        {composer}
      </div>
      <aside className="review-conv-side" aria-label="Summary">
        <h3>Threads</h3>
        <p>{open} open · {threads.length - open} settled{outdatedCount ? ` · ${outdatedCount} outdated` : ''}</p>
        <h3>Versions</h3>
        <ul>
          {revisions.map(info => (
            <li key={info.artifact.id}>
              {info.artifact.id === artifact.id ? <strong>{label(info.artifact)}</strong> : (
                <button type="button" className="artifact-link" onClick={() => openVersion(info.artifact)}>{label(info.artifact)}</button>
              )}
              {info.summary && <span className="review-conv-meta"> {patchSummaryText(info.summary)}</span>}
            </li>
          ))}
        </ul>
      </aside>
    </div>
  )
}

function ExcerptView({ excerpt, onClick }: { excerpt: Excerpt; onClick?: () => void }) {
  // A click anywhere on the code jumps (a pointer shortcut). The code stays a readable table for
  // screen readers; the keyboard and assistive path is the card's file:line button.
  return (
    <div
      className={`review-conv-excerpt${onClick ? ' is-link' : ''}`}
      data-testid="conversation-excerpt"
      onClick={onClick}
    >
      <table className="diff-table diff-unified" aria-label={`Code at ${excerpt.file}:${excerpt.start === excerpt.end ? excerpt.start : `${excerpt.start}–${excerpt.end}`}`}>
        <colgroup><col className="diff-col-num" /><col className="diff-col-num" /><col /></colgroup>
        <tbody>
          {excerpt.rows.map((row, i) => {
            const tint = row.kind === 'add' ? ' diff-add' : row.kind === 'del' ? ' diff-del' : ''
            const mark = row.marked ? ' diff-requested' : ''
            return (
              <tr key={i} className={`diff-row${tint}`} data-marked={row.marked || undefined}>
                <td className={`diff-num${mark}`}>{row.old ?? ''}</td>
                <td className={`diff-num${mark}`}>{row.new ?? ''}</td>
                <td className={`diff-code${mark}`}>
                  <span className="diff-mark" aria-hidden="true">{row.kind === 'add' ? '+' : row.kind === 'del' ? '-' : ' '}</span>
                  {row.text}
                </td>
              </tr>
            )
          })}
          {excerpt.more > 0 && (
            <tr className="diff-note-row"><td colSpan={3} className="diff-note">… {excerpt.more} more selected {excerpt.more === 1 ? 'line' : 'lines'}</td></tr>
          )}
        </tbody>
      </table>
    </div>
  )
}
