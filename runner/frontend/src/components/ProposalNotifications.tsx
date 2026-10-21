import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTicker } from '@blerglab/chat'
import { useMessageStore } from '../hooks/useMessageStore'
import { useSessionStore } from '../hooks/useSessionStore'
import { sessionLabel } from '../notify'
import { useProposalInbox, type ProposalInbox } from '../hooks/useProposalInbox'
import { useToastStore } from '../hooks/useToastStore'
import { PendingProposal } from './ProposalAttention'
import { proposalExpiring, proposalsWaiting, warnedProposalDeadlines as warned } from '../lib/proposalAttention'
import type { MessageInfo, ProposalInfo } from '../types'


// notifyExpiring tells the person that proposals enter their last 24 hours. It is in-app only, by
// the owner's ruling (0.4.4): no push notification, with or without contents. Sessions are
// semi-ephemeral, and a proposal open long enough to need one means something is wrong.
function notifyExpiring(due: ProposalInfo[]) {
  useToastStore.getState().addToast({ title: due.length === 1 ? 'Proposal expires soon' : 'Proposals expire soon', body: `${due.length} pending ${due.length === 1 ? 'request expires' : 'requests expire'} within 24 hours. Review or extend before the deadline.`, url: `/proposals?id=${encodeURIComponent(due[0].id)}` })
}

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
    <Link to={`/sessions/${encodeURIComponent(question.session_id)}`}>Answer in the session</Link>
  </article>
}

// In-app notifications are owner scoped.
export function ProposalBell({ inbox }: { inbox: ProposalInbox }) {
  const [open, setOpen] = useState(false)
  const now = useTicker(true)
  const messages = useMessageStore(s => s.messages)
  const questions = useMemo(() => openQuestions(messages), [messages])
  // A proposal past its deadline is listed (as expired) but not counted: it no longer needs you.
  const serverNow = now - (inbox.clockOffset ?? 0)
  const waiting = proposalsWaiting(inbox.pending, serverNow).length
  const total = waiting + questions.length
  const questionWords = questions.length ? `, ${questions.length} ${questions.length === 1 ? 'question' : 'questions'}` : ''
  const due = inbox.pending.filter(p => proposalExpiring(p, serverNow))
  useEffect(() => {
    const newlyDue = inbox.pending.filter(p => proposalExpiring(p, serverNow) && !warned.has(`${p.id}:${p.expires_at}`))
    for (const p of newlyDue) warned.add(`${p.id}:${p.expires_at}`)
    if (newlyDue.length) notifyExpiring(newlyDue)
  }, [inbox.pending, serverNow])
  useEffect(() => {
    if (!open) return
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape') setOpen(false) }
    window.addEventListener('keydown', escape)
    return () => window.removeEventListener('keydown', escape)
  }, [open])
  return <div className="proposal-notifications">
    <button type="button" className={due.length ? 'proposal-bell urgent' : 'proposal-bell'} aria-label={`Notifications (${waiting} pending${due.length ? `, ${due.length} expiring soon` : ''}${questionWords})`} title={`${waiting} proposals${questionWords} need you${due.length ? `; ${due.length} expire soon` : ''}`} aria-expanded={open} onClick={() => setOpen(v => !v)}>
      <span aria-hidden="true">🔔</span>{total > 0 && <span aria-hidden="true">{total}</span>}
    </button>
    {open && <section className="proposal-notification-list" aria-label="Notifications">
      <div className="proposal-attention-actions"><h2>Needs you</h2><button type="button" onClick={() => setOpen(false)}>Close notifications</button></div>
      {inbox.error && <p role="alert">{inbox.error} <button type="button" onClick={() => void inbox.refresh()}>Retry</button></p>}
      {questions.map(q => <QuestionRow key={q.id} question={q} />)}
      {!inbox.error && inbox.pending.length === 0 && <p>No proposals waiting.</p>}
      {inbox.pending.map(p => <PendingProposal key={p.id} proposal={p} refresh={() => void inbox.refresh()} />)}
    </section>}
  </div>
}
export default function ProposalNotifications() { return <ProposalBell inbox={useProposalInbox()} /> }
