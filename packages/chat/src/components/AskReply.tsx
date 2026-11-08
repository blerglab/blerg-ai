// AskReply: the box that answers one of the agent's questions — under its ask card in the chat, and
// beside the chat in a host's side panel. Ctrl/Cmd+Enter answers; Enter is a new line; a send that does
// not go out keeps the text and says so.
import { useContext, useRef, useState, type ReactNode } from 'react'

import Composer from './Composer'
import type { useAttachments } from '../hooks/useAttachments'
import { QuestionAskerContext, QuestionDiscussionContext } from './questionThreads'

export interface AskReplyProps {
  /** Sends the answer (messageId: the ask's messages API id, when it has one); false when nothing
   *  was sent. */
  answer: (text: string, messageId?: string) => Promise<boolean>
  reply?: (text: string, clientId: string) => Promise<boolean>
  binary?: boolean
  /** A one-line composer that expands when focused, for embedded question cards. */
  compact?: boolean
  messageId?: string
  /** The box's accessible name (default "Your answer"). */
  label?: string
  className?: string
  actions?: ReactNode
  /** The text in the box, when the host keeps it (so it outlives the box: a drawer that closes);
   *  with onDraft. Absent: the box keeps its own. */
  draft?: string
  onDraft?: (text: string) => void
}

export default function AskReply({ answer, reply, messageId, label = 'Your answer', binary = false, compact = false, className, actions, ...held }: AskReplyProps) {
  const asker = useContext(QuestionAskerContext)
  const askLabel = `Ask ${asker.match(/\b[A-Z][A-Z0-9]*-\d+\b/)?.[0] ?? asker}`
  const askTitle = `Ask ${asker} about this question; keep it open`
  const discussion = useContext(QuestionDiscussionContext)
  const protectedApproval = discussion?.data?.question.owner_only && !discussion.data.question.no_wait && !discussion.data.question.decision
  binary = binary || !!protectedApproval
  const followUp = protectedApproval ? undefined : reply ?? discussion?.reply
  const pendingReply = useRef<{ text: string; id: string } | null>(null)
  const [own, setOwn] = useState('')
  const draft = held.draft ?? own
  const setDraft = held.onDraft ?? setOwn
  const [writing, setWriting] = useState(false)
  const [sending, setSending] = useState(false)
  const [failed, setFailed] = useState(false)
  const [revised, setRevised] = useState(false)
  async function submit(value = draft, asReply = false) {
    const text = value.trim()
    if (!text || sending) return
    setSending(true)
    setFailed(false)
    try {
      if (asReply && pendingReply.current?.text !== text) pendingReply.current = { text, id: crypto.randomUUID() }
      const ok = asReply && followUp ? await followUp(text, pendingReply.current!.id) : discussion?.client && messageId ? await discussion.client.answer(messageId, text, discussion.data?.question.question_revision ?? 0) : await answer(text, messageId || undefined)
      if (ok) { setDraft(''); pendingReply.current = null; if (!asReply) await discussion?.reload() }
      else setFailed(true)
    } catch (error) {
      if (error instanceof Error && error.message === 'Main revised this question') { setRevised(true); await discussion?.reload() } else setFailed(true)
    } finally {
      setSending(false)
    }
  }
  return (
    <form className={`ask-reply${compact ? ' ask-reply-compact' : ''}${className ? ` ${className}` : ''}`} onSubmit={e => { e.preventDefault(); void submit() }}>
      {binary && <div className="ask-reply-decisions">
        <button type="button" title="Approve this exact decision" className="artifact-btn primary" disabled={sending} onClick={() => void submit('yes', false)}>Approve</button>
        <button type="button" title="Decline this exact decision" className="artifact-btn" disabled={sending} onClick={() => void submit('no', false)}>Decline</button>
        {!protectedApproval && !writing && <button type="button" title={askTitle} className="ask-reply-question" aria-expanded={writing} onClick={() => setWriting(true)}>{askLabel}</button>}
      </div>}
      {!protectedApproval && (!binary || writing || !!draft) && <Composer
        autoFocus={!compact} draft={draft} onDraft={setDraft} inputLabel={label} placeholder="Your answer…" locked={sending}
        attachments={emptyAttachments} submitLabel={binary && followUp ? askLabel : 'Answer'}
        submitTitle={binary ? askTitle : 'Send this as your answer and close the question'}
        submitOn="mod-enter" onSubmit={() => void submit(draft, binary && !!followUp)}
        actions={<>{!discussion?.data && actions}{followUp && !binary && <button type="button" className="artifact-btn" title={askTitle} disabled={sending || !draft.trim()} onClick={() => void submit(draft, true)}>{askLabel}</button>}</>}
      />}
      {revised && <span className="ask-reply-error" role="status">Main revised this question</span>}
      {failed && <span className="ask-reply-error" role="status">{followUp ? 'Your reply was not sent. Try again.' : 'Not connected — your answer was not sent.'}</span>}
    </form>
  )
}

const emptyAttachments: ReturnType<typeof useAttachments> = { items: [], done: [], uploading: false, waitingBytes: 0, waitingCount: 0, waitingForSpace: false, space: null, notice: null, retryWaiting: async () => {}, addFiles: async () => {}, remove: () => {}, clear: () => {} }
