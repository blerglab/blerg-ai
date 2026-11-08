import { Children, isValidElement, useMemo, useState, type ComponentProps } from 'react'
import { QuestionCard, ReferenceScope, questionPresentation, type ChatRefs, type Ticket } from '@blerglab/chat'
import { useMessageStore } from '../hooks/useMessageStore'
import type { ReactNode } from 'react'
import AttentionRow from './AttentionRow'
import { useLoadSessionBoard } from '../lib/sessionBoard'
import { boardCardsReader } from '../lib/cardRefs'
import { PinnedTicketModal } from './BoardCardView'

// The full-question row keeps the card's own leading glyph (? or ↩), so it lines up with review rows.
function leadingGlyph(children: ReactNode): ReactNode {
  if (!isValidElement<{ children?: ReactNode }>(children)) return null
  return Children.toArray(children.props.children)[0] ?? null
}

/** Questions and channels use the same dock row surface and spacing. */
export default function QuestionRow({ sessionId = '', inlineAnswers = false, ...props }: Omit<ComponentProps<typeof QuestionCard>, 'renderRow'> & { sessionId?: string; inlineAnswers?: boolean }) {
  const current = useMessageStore(s => s.messages.find(message => message.id === props.id))
  const display = questionPresentation(current?.body ?? props.question, props.worker)
  const fullQuestion = display.text
  const asker = [display.asker, display.parent && `via ${display.parent}`, props.audience === 'you' ? 'Needs you' : ''].filter(Boolean).join(' · ')
  if (inlineAnswers) return <QuestionReferenceScope sessionId={sessionId} id={props.id}><QuestionCard {...props} asCard question={current?.body ?? props.question} turn={current?.question_turn} /></QuestionReferenceScope>
  const card = <QuestionCard {...props} question={current?.body ?? props.question} turn={current?.question_turn} renderRow={row => <AttentionRow {...row} action={props.headerActions}>{props.audience === 'you' ? <>{leadingGlyph(row.children)}<span className="question-card-summary"><span className="question-full-text">{fullQuestion}</span>{(display.asker !== 'Main' || display.parent) && <span className="question-card-asker">{asker}</span>}</span></> : row.children}</AttentionRow>} />
  return <QuestionReferenceScope sessionId={sessionId} id={props.id}>{card}</QuestionReferenceScope>
}

export function QuestionReferenceScope({sessionId,id,children}:{sessionId:string;id:string;children:ReactNode}) {
  const board = useLoadSessionBoard(sessionId, !!sessionId)
  const [ticket, setTicket] = useState<Ticket | null>(null)
  const refs = useMemo<ChatRefs>(() => ({
    cards: board ? boardCardsReader(board, sessionId) : undefined,
    boardCards: (id, numbers) => boardCardsReader({ id }, sessionId)(numbers),
    openCard: target => setTicket({ source: 'blerg-board', board_id: target.boardId, key: target.key, title: target.title ?? '' }),
  }), [board, sessionId])
  return <><ReferenceScope sessionId={`question:${sessionId}:${id}`} refs={refs}>{children}</ReferenceScope>{ticket && <PinnedTicketModal ticket={ticket} sessionId={sessionId} onClose={() => setTicket(null)} />}</>
}
