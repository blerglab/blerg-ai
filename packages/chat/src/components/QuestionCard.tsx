import { useEffect, useState, type ComponentPropsWithRef, type ReactElement, type ReactNode } from 'react'
import { Hover } from './Hovercard'
import { TicketModal } from './TicketView'
import { questionPresentation } from '../model/questionPresentation'
import Markdown from './Markdown'
import { ReferenceText } from './RefLinks'
import './question.css'
import { useQuestionThread, QuestionDiscussion, QuestionDismiss } from './questionThreads'

function PlanOperations({ operations }: { operations: string[] }) {
  const groups = new Map<string, { action: string; title: string; detail: string }[]>()
  for (const operation of operations) {
    const [first, ...fields] = operation.trim().split('\n')
    const thrust = fields.find(line => line.trim().startsWith('Thrust: '))?.trim().slice(8) || 'Other changes'
    const match = /^- (Add|Remove|Move|Archive): ?(.*)$/.exec(first)!
    const detail = fields.filter(line => !line.trim().startsWith('Thrust: ')).map(line => line.trim().replace(/^Reason: /, '')).join(' · ')
    const rows = groups.get(thrust) ?? []
    rows.push({ action: match[1], title: match[2], detail })
    groups.set(thrust, rows)
  }
  return <div className="question-plan-operations">
    {[...groups].map(([thrust, rows]) => {
      const sharedAction = rows.every(row => row.action === rows[0].action) ? rows[0].action : ''
      const sharedDetail = rows.every(row => row.detail === rows[0].detail) ? rows[0].detail : ''
      return <section className="question-plan-group" key={thrust}>
        {/* A lone fallback group (a cut, a plan with no thrusts) needs no 'Other changes' heading. */}
        {!(groups.size === 1 && thrust === 'Other changes') && <h3>{thrust} · {rows.length}</h3>}
        {(sharedAction || sharedDetail) && <div className="question-plan-context">{[sharedAction, sharedDetail].filter(Boolean).join(' · ')}</div>}
        <ul>{rows.map((row, i) => <li className="question-plan-operation" key={i}>
          {!sharedAction && <><span className="question-plan-action">{row.action}</span>{' '}</>}
          <span className="question-plan-title"><ReferenceText text={row.title} /></span>{' '}
          {!sharedDetail && row.detail && <span className="question-plan-detail"><ReferenceText text={row.detail} /></span>}
        </li>)}</ul>
      </section>
    })}
  </div>
}

/** A plain question row, with the full text in its preview and dialog. */
export default function QuestionCard({ id, question, status = 'open', worker, audience, wait, renderRow, children, approval = false, decisionButtons = false, turn, asPanel = false, asCard = false, active = true, onTitle, onClose, activity, statusAction, headerActions }: {
  headerActions?: ReactNode
  onClose?: () => void
  wrapActivity?: boolean
  activity?: string
  statusAction?: ReactNode
  onTitle?: (title: string) => void
  active?: boolean
  asPanel?: boolean
  /** Embed the dialog conversation in a content card instead of opening an overlay. */
  asCard?: boolean
  turn?: 'person' | 'main'
  id: string; question: string; status?: string; worker?: string; audience?: 'you' | 'main'; wait?: string
  renderRow?: (props: ComponentPropsWithRef<'button'>) => ReactElement
  children?: ReactNode
  /** Decision controls are rendered in this dialog; omit typed yes/no instructions here. */
  decisionButtons?: boolean
  approval?: boolean
}) {
  const [open, setOpen] = useState(false)
  const thread = useQuestionThread(id, open || ((asPanel || asCard) && active))
  const display = questionPresentation(thread.data?.question.body ?? question, worker)
  const paragraph = display.text.indexOf('\n')
  const rawSnapshot = paragraph >= 0 ? display.text.slice(paragraph + 1).trimStart() : ''
  const snapshot = (decisionButtons || thread.data?.question.decision) ? rawSnapshot.replace(/^Reply to this question with yes to [^\n]*?, or no to [^.\n]*\.(?: Other replies ask for clarification\.)?[ \t]*/gm, '') : rawSnapshot
  const title = (paragraph >= 0 ? display.text.slice(0, paragraph) : display.text).replace(/\s+/g, ' ').trim() || 'Question'
  useEffect(() => { if (asPanel && thread.data) onTitle?.(title) }, [asPanel, thread.data, title, onTitle])
  const sections = snapshot.split('\n\n')
  const operations = (title.startsWith('Plan changes ·') || title.startsWith('Cut ')) && sections.length >= 3 ? sections[1].split(/(?=^- (?:Add|Remove|Move|Archive):)/m).filter(text => text.trim()) : []
  const planSnapshot = operations.length > 0 && operations.every(text => /^- (?:Add|Remove|Move|Archive):/.test(text))
  const firstGoal = title.startsWith('Create blackboard ·') ? sections.findIndex(text => /^Goal [0-9]+:/.test(text)) : -1
  const goals = firstGoal >= 0 ? sections.slice(firstGoal).filter(text => /^Goal [0-9]+:/.test(text)) : []
  const decision = thread.data?.question.decision || approval || decisionButtons || planSnapshot || title.startsWith('Create blackboard ·')
  const asker = [display.asker, display.parent && `via ${display.parent}`, audience === 'you' ? turn === 'main' ? 'replying' : 'Needs you' : audience === 'main' ? 'Main answers' : ''].filter(Boolean).join(' · ')
  const rowProps: ComponentPropsWithRef<'button'> = {
    type: 'button', className: 'question-card-open', 'aria-haspopup': 'dialog', 'aria-expanded': open, onClick: () => setOpen(true),
    children: <><span className="question-card-glyph" role={turn ? "img" : undefined} aria-hidden={turn ? undefined : true} title={turn ? turn === "main" ? "Main is replying" : "Your turn" : undefined} aria-label={turn ? turn === "main" ? "Main is replying" : "Your turn" : undefined}>{turn === "main" ? "↩" : "?"}</span><span className="question-card-summary"><span className="question-card-title">{title}</span>{(audience !== 'you' || (display.asker && display.asker !== 'Main') || display.parent) && <span className="question-card-asker">{asker}</span>}</span></>,
  }
  // The wrapping header shows the complete first line; keep only later lines in the body.
  const firstMessage = <>{!decision && rawSnapshot && <Markdown text={rawSnapshot} />}{decision && snapshot && <div className="question-snapshot" data-testid="question-snapshot">{planSnapshot ? <><div><ReferenceText text={sections[0]} /></div><PlanOperations operations={operations} /><div><ReferenceText text={sections.slice(2).join('\n\n')} /></div></> : goals.length ? <><div><ReferenceText text={sections.slice(0, firstGoal).join('\n\n')} /></div><div className="question-snapshot-grid">{goals.map((goal, i) => <div className="question-snapshot-operation" key={i}><ReferenceText text={goal} /></div>)}</div><div><ReferenceText text={sections.slice(firstGoal + goals.length).join('\n\n')} /></div></> : <ReferenceText text={snapshot} />}</div>}</>
  // Share the resolved asker with reply controls, including legacy escalated worker questions.
  const body = <div className="question-chat"><QuestionDiscussion state={thread} firstMessage={firstMessage} asker={display.asker || worker || 'Main'} turn={turn} detail={[wait, activity].filter(Boolean).join(' · ')} action={asCard ? undefined : statusAction}>{children}</QuestionDiscussion></div>
  const dismiss = thread.data ? <QuestionDismiss state={thread} /> : headerActions
  if (asPanel || asCard) return <section className={`question-panel${asCard ? ' question-inline' : ''}`} aria-label={`Question: ${title}`}><header className="artifact-header ticket-modal-header"><div role="heading" aria-level={2} className="ticket-modal-title" title={title}><Markdown text={title} inline references={false} /></div><div className="ticket-modal-actions">{asCard && statusAction}{dismiss}{onClose && <button type="button" className="artifact-close" title="Close question tab" aria-label="Close" onClick={onClose}>✕</button>}</div></header>{body}</section>
  return <div className={`question-card${audience === 'you' ? ' question-answerable' : ''}`}>
    <Hover target={{ kind: 'card', source: 'question', key: id, title }} content={<div className="question-preview">
      <div className="question-card-asker">{asker}</div>
      <div className="question-preview-text">{approval ? display.text : <Markdown text={display.text} />}</div>
      <div className="question-preview-footer">{wait && <span>{wait}</span>}<span>{status === 'open' ? 'Click question to read and answer' : 'Click question to read in full'}</span></div>
    </div>}>
      {renderRow ? renderRow(rowProps) : <button {...rowProps} />}
    </Hover>
    {open && <TicketModal ticket={{ source: 'question', key: id, title, status: thread.data?.question.status ?? status }} actions={<>{dismiss}{thread.client?.openTab && thread.data && <button type="button" className="artifact-close" aria-label="Open as tab" title="Open as tab" onClick={() => { if (thread.client!.openTab!(thread.data!.question, title)) setOpen(false) }}>↗</button>}</>} wideQuestion={planSnapshot || (!!snapshot && title.startsWith('Create blackboard ·'))} plainTitle={decision} onClose={() => setOpen(false)} body={body} />}
  </div>
}
