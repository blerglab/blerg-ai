import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Link } from 'react-router-dom'
import { useTicker } from '@blerglab/chat'
import { useMessageStore } from '../hooks/useMessageStore'
import { useSessionStore } from '../hooks/useSessionStore'
import { sessionLabel } from '../notify'
import { useProposalInbox, type ProposalInbox } from '../hooks/useProposalInbox'
import { useProposalExpiryWarnings } from '../hooks/useProposalExpiryWarnings'
import { PendingProposal } from './ProposalAttention'
import { proposalExpiring, proposalsWaiting } from '../lib/proposalAttention'
import type { MessageInfo } from '../types'
import { BellIcon } from './IconButton'
import { dismissWithUndo, useDismissed } from '../lib/needsYouDismiss'
import { useMyReviewRequests, type MyReviewRequest } from '../hooks/useMyReviewRequests'
import { disableNotifications, enableNotifications, pushState, type PushState } from '../lib/push'


// The questions agents asked without waiting (`blerg-runner ask --no-wait`) that are still open and
// this person answers (the server marks them answerable for the session's owner only): the bell
// lists them beside the proposals, each opening its session, where Needs you answers it. Other
// viewers see them read-only in the session, never here.
function openQuestions(messages: readonly MessageInfo[]): MessageInfo[] {
  return messages.filter(m => m.kind === 'ask' && m.no_wait === true && m.answerable === true && m.status === 'open')
}

function QuestionRow({ question }: { question: MessageInfo }) {
  const session = useSessionStore(s => s.sessions.find(x => x.id === question.session_id))
  return <article className="proposal-question" data-testid="bell-question">
    <div className="proposal-question-kicker">Question · {session ? sessionLabel(session) : 'a session'}</div>
    <p className="proposal-question-text">{question.body}</p>
    <div className="proposal-question-actions">
      <Link to={`/sessions/${encodeURIComponent(question.session_id)}`}>Answer in the session</Link>
      <button type="button" data-testid="bell-dismiss" aria-label={`Dismiss question: ${question.body.slice(0, 80)}`}
        title="Dismiss: you answered elsewhere, or it no longer needs you. The agent is told."
        onClick={() => dismissWithUndo(`ask:${question.id}`, `Question: ${question.body.slice(0, 80)}`)}>Dismiss</button>
    </div>
  </article>
}

// A review a session asked of this person: open the file in its session (Approve or Request changes
// there), or dismiss it.
function ReviewRow({ review }: { review: MyReviewRequest }) {
  const session = useSessionStore(s => s.sessions.find(x => x.id === review.session_id))
  return <article className="proposal-question" data-testid="bell-review">
    <div className="proposal-question-kicker">Review · {session ? sessionLabel(session) : 'a session'}</div>
    <p className="proposal-question-text"><strong>{review.artifact_name}</strong>{review.artifact_version > 1 ? ` v${review.artifact_version}` : ''}{review.ask ? `: ${review.ask}` : ''}</p>
    <div className="proposal-question-actions">
      <Link to={review.url}>Review in the session</Link>
      <button type="button" data-testid="bell-dismiss" aria-label={`Dismiss review request: ${review.artifact_name}`}
        title="Dismiss: you answered elsewhere, or it no longer needs you. The agent is told."
        onClick={() => dismissWithUndo(`review:${review.request_id}`, `Review: ${review.artifact_name}`)}>Dismiss</button>
    </div>
  </article>
}

const PUSH_WORDS: Record<PushState, string> = {
  unsupported: 'This browser cannot receive push notifications (it has no service worker or Push API support). Toasts in an open page still come.',
  blocked: 'Blocked in this browser\'s site settings. Allow notifications there to turn them on.',
  on: 'On for this browser.',
  off: 'Off for this browser.',
}

// Push to this browser (phone or desktop), folded into the bell: the one place to turn it on or off.
export function PushSetting() {
  const [state, setState] = useState<PushState>(pushState)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const change = (action: () => Promise<void>, failed: string) => {
    setBusy(true); setError('')
    action().catch(err => { console.error(err); setError(failed) }).finally(() => { setState(pushState()); setBusy(false) })
  }
  return <div className="proposal-push" data-testid="push-setting">
    <div className="proposal-push-text">
      <strong>Push notifications</strong>
      <span data-testid="push-state">{PUSH_WORDS[state]}</span>
      {error && <span role="alert">{error}</span>}
    </div>
    {state === 'off' && <button type="button" disabled={busy} onClick={() => change(enableNotifications, 'Could not turn push on. Try again.')}>Turn on</button>}
    {state === 'on' && <button type="button" disabled={busy} onClick={() => change(disableNotifications, 'Could not turn push off. Try again.')}>Turn off</button>}
  </div>
}

// The panel sits under the bell, inside the screen: the bell is at the sidebar's right edge (the
// panel opens rightward over the page) or at a phone's (it spans the screen).
function panelPlace(button: HTMLElement | null) {
  const vw = window.innerWidth
  const width = Math.min(vw - 24, Math.max(640, vw * 0.4))
  const rect = button?.getBoundingClientRect()
  const top = (rect?.bottom ?? 48) + 6
  const left = Math.max(12, Math.min(rect?.left ?? 12, vw - 12 - width))
  return { top, left, width, maxHeight: `calc(100dvh - ${Math.round(top) + 12}px)` }
}

// In-app notifications are owner scoped. The app has one bell, in the sessions header beside + New.
export function ProposalBell({ inbox }: { inbox: ProposalInbox }) {
  const [open, setOpen] = useState(false)
  const now = useTicker(true)
  const messages = useMessageStore(s => s.messages)
  const dismissed = useDismissed()
  const questions = useMemo(() => openQuestions(messages), [messages]).filter(q => !dismissed(`ask:${q.id}`))
  const reviews = useMyReviewRequests(open).filter(r => !dismissed(`review:${r.request_id}`))
  // A proposal past its deadline is listed (as expired) but not counted: it no longer needs you.
  const serverNow = now - (inbox.clockOffset ?? 0)
  const waiting = proposalsWaiting(inbox.pending, serverNow).length
  const total = waiting + questions.length + reviews.length
  const questionWords = (questions.length ? `, ${questions.length} ${questions.length === 1 ? 'question' : 'questions'}` : '') +
    (reviews.length ? `, ${reviews.length} ${reviews.length === 1 ? 'review' : 'reviews'}` : '')
  const due = inbox.pending.filter(p => proposalExpiring(p, serverNow))
  useProposalExpiryWarnings(inbox)
  const button = useRef<HTMLButtonElement>(null)
  const [place, setPlace] = useState<ReturnType<typeof panelPlace> | null>(null)
  useLayoutEffect(() => {
    if (!open) return
    const update = () => setPlace(panelPlace(button.current))
    update()
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [open])
  useEffect(() => {
    if (!open) return
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') setOpen(false) }
    window.addEventListener('keydown', escape)
    return () => window.removeEventListener('keydown', escape)
  }, [open])
  return <div className="proposal-notifications">
    <button ref={button} type="button" className={due.length ? 'icon-btn proposal-bell urgent' : 'icon-btn proposal-bell'} data-testid="notifications-bell" aria-label={`Notifications (${waiting} pending${due.length ? `, ${due.length} expiring soon` : ''}${questionWords})`} title={`Notifications: ${waiting} proposals${questionWords} need you${due.length ? `; ${due.length} expire soon` : ''}`} aria-expanded={open} onClick={() => setOpen(v => !v)}>
      <BellIcon />{total > 0 && <span className={due.length ? 'icon-btn-count attention' : 'icon-btn-count'} aria-hidden="true">{total}</span>}
    </button>
    {open && createPortal(<section className="proposal-notification-list" aria-label="Notifications" style={place ?? undefined}>
      <div className="proposal-attention-actions"><h2>Needs you</h2><button type="button" onClick={() => setOpen(false)}>Close notifications</button></div>
      {inbox.error && <p role="alert">{inbox.error} <button type="button" onClick={() => void inbox.refresh()}>Retry</button></p>}
      {questions.map(q => <QuestionRow key={q.id} question={q} />)}
      {reviews.map(r => <ReviewRow key={r.request_id} review={r} />)}
      {!inbox.error && inbox.pending.length === 0 && <p>No proposals waiting.</p>}
      {inbox.pending.map(p => <PendingProposal key={p.id} proposal={p} refresh={() => void inbox.refresh()} />)}
      <PushSetting />
    </section>, document.body)}
  </div>
}
export default function ProposalNotifications() { return <ProposalBell inbox={useProposalInbox()} /> }
