// AskReply: the box that answers one of the agent's questions — under its ask card in the chat, and
// beside the chat in a host's side panel. Enter sends (Shift+Enter is a new line); a send that does
// not go out keeps the text and says so.
import { useState } from 'react'

export interface AskReplyProps {
  /** Sends the answer (messageId: the ask's messages API id, when it has one); false when nothing
   *  was sent. */
  answer: (text: string, messageId?: string) => Promise<boolean>
  messageId?: string
  /** The box's accessible name (default "Your answer"). */
  label?: string
  className?: string
  /** The text in the box, when the host keeps it (so it outlives the box: a drawer that closes);
   *  with onDraft. Absent: the box keeps its own. */
  draft?: string
  onDraft?: (text: string) => void
}

export default function AskReply({ answer, messageId, label = 'Your answer', className, ...held }: AskReplyProps) {
  const [own, setOwn] = useState('')
  const draft = held.draft ?? own
  const setDraft = held.onDraft ?? setOwn
  const [sending, setSending] = useState(false)
  const [failed, setFailed] = useState(false)
  async function submit() {
    const text = draft.trim()
    if (!text || sending) return
    setSending(true)
    setFailed(false)
    try {
      const ok = await answer(text, messageId || undefined)
      if (ok) setDraft('')
      else setFailed(true)
    } finally {
      setSending(false)
    }
  }
  return (
    <form className={`ask-reply${className ? ` ${className}` : ''}`} onSubmit={e => { e.preventDefault(); void submit() }}>
      <textarea
        className="ask-reply-box"
        aria-label={label}
        placeholder="Your answer…"
        rows={1}
        value={draft}
        onChange={e => setDraft(e.target.value)}
        onKeyDown={e => {
          if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault()
            void submit()
          }
        }}
      />
      <button type="submit" className="artifact-btn ask-reply-send" disabled={sending || !draft.trim()}>Answer</button>
      {failed && <span className="ask-reply-error" role="status">Not connected — your answer was not sent.</span>}
    </form>
  )
}
