import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import Markdown from './Markdown'
import MessageRow from './MessageRow'
import WorkingIndicator from './WorkingIndicator'
import { TimeLabel } from './TimeLabel'
import AskReply from './AskReply'
import QuestionCard from './QuestionCard'

export interface ThreadQuestion {
 id: string; session_id: string; body: string; status: string
 question_revision?: number; question_turn?: 'person' | 'main'; answer?: string | null; dismissed?: boolean
 answerable?: boolean; decision?: boolean; owner_only?: boolean; no_wait?: boolean
}
export interface QuestionThreadEntry { seq: number; author: 'person' | 'main'; kind: 'reply' | 'revision'; body: string; previous_body?: string; created_at: string }
export interface QuestionThreadPage { question: ThreadQuestion; entries: QuestionThreadEntry[]; more: boolean }
export interface QuestionThreadsClient {
 load(id: string, after?: number): Promise<QuestionThreadPage>
 reply(id: string, text: string, clientId: string): Promise<void>
 answer(id: string, text: string, revision?: number): Promise<boolean>
 dismiss(id: string, revision?: number): Promise<boolean>
 subscribe?(changed: () => void): () => void
 openTab?(question: ThreadQuestion, title: string): boolean
}
export const QuestionThreadsContext = createContext<QuestionThreadsClient | null>(null)
export function QuestionThreadsProvider({ client, children }: { client: QuestionThreadsClient; children: ReactNode }) {
 return <QuestionThreadsContext.Provider value={client}>{children}</QuestionThreadsContext.Provider>
}
const changes = new EventTarget()
export function useQuestionThread(id: string, active: boolean) {
 const client = useContext(QuestionThreadsContext)
 const [data, setData] = useState<QuestionThreadPage | null>(null)
 const [error, setError] = useState(false)
 const epoch = useRef(0)
 const invalidate = useCallback(() => { ++epoch.current }, [])
 const reload = useCallback(async () => {
  if (!client || !id) return
  const current = ++epoch.current
  try {
   let page = await client.load(id)
   const entries = [...page.entries]
   // Bounded pages preserve every exchange, without silently truncating long threads.
   while (page.more && page.entries.length) {
    const after = page.entries.at(-1)!.seq
    page = await client.load(id, after)
    if (page.entries.length && page.entries[0].seq <= after) throw new Error('Invalid thread cursor')
    entries.push(...page.entries)
   }
   if (epoch.current === current) { setData({ ...page, entries }); setError(false) }
  } catch { if (epoch.current === current) setError(true) }
 }, [client, id])
 useEffect(() => {
  if (!active || !client) return
  const refresh = () => { void reload() }
  refresh()
  changes.addEventListener(id, refresh)
  const unsubscribe = client.subscribe?.(refresh)
  const timer = setInterval(() => { if (!document.hidden) refresh() }, 5000)
  return () => { invalidate(); changes.removeEventListener(id, refresh); unsubscribe?.(); clearInterval(timer) }
 }, [active, client, id, reload, invalidate])
 const reply = useCallback(async (text: string, clientId: string) => {
  if (!client) return false
  try { await client.reply(id, text, clientId); changes.dispatchEvent(new Event(id)); await reload(); return true } catch { return false }
 }, [client, id, reload])
 return { client, data, error, reload, reply }
}
export type QuestionThreadState = ReturnType<typeof useQuestionThread>
export const QuestionAskerContext = createContext('Main')
export const QuestionDiscussionContext = createContext<QuestionThreadState | null>(null)

export function QuestionDismiss({ state }: { state: QuestionThreadState }) {
 const [error, setError] = useState('')
 const [busy, setBusy] = useState(false)
 const q = state.data?.question
 if (!q || q.status !== 'open' || !q.answerable || (q.owner_only && !q.no_wait)) return null
 return <><button type="button" className="artifact-close" aria-label="Dismiss" title="Close without answering" disabled={busy} onClick={async () => {
  setBusy(true); setError('')
  try { if (!await state.client!.dismiss(q.id, q.question_revision ?? 0)) setError('Couldn’t dismiss the question. Try again.'); await state.reload() }
  catch (error) { setError(error instanceof Error && error.message === 'Main revised this question' ? 'Main revised this question' : 'Couldn’t dismiss the question. Try again.'); await state.reload() }
  finally { setBusy(false) }
 }}>⊘</button>{error && <span role="status" className="ask-reply-error">{error}</span>}</>
}

export function QuestionDiscussion({ state, children, firstMessage, asker = 'Main', turn, detail, action }: { state: QuestionThreadState; children: ReactNode; firstMessage: ReactNode; asker?: string; turn?: 'person' | 'main'; detail?: string; action?: ReactNode }) {
 const question = state.data?.question
 const closed = question && question.status !== 'open'
 return <QuestionAskerContext.Provider value={asker}><QuestionDiscussionContext.Provider value={state.client ? state : null}>
  <div className="question-conversation agent-timeline" role="log" aria-label="Question discussion">
   <MessageRow author="assistant"><span className="card-author" title={detail}>{asker}{action}</span>{firstMessage}</MessageRow>
   {state.error && <div className="ask-reply-error" role="status">Couldn’t load the thread. <button type="button" title="Reload the question thread" className="artifact-btn" onClick={() => void state.reload()}>Retry</button></div>}
   {state.data?.entries.map(entry => <MessageRow key={entry.seq} author={entry.author === 'person' ? 'user' : 'assistant'}>
    <span className="card-author">{entry.author === 'person' ? 'You' : asker}{entry.kind === 'revision' ? ' · revised the question' : ''}</span>
    <Markdown text={entry.body} /><div className="card-foot"><TimeLabel ts={entry.created_at} /></div>
    {entry.previous_body && <details><summary>Previous question</summary><Markdown text={entry.previous_body} /></details>}
   </MessageRow>)}
   {!closed && (question?.question_turn ?? turn) === 'main' && <WorkingIndicator since={0} now={0} activity="Main is replying…" />}
  </div>
  {closed ? <p className="ask-outcome" role="status">{question.status === 'withdrawn' ? 'Withdrawn' : question.dismissed ? 'Dismissed' : question.answer ? `Answered: ${question.answer}` : 'Closed'}</p> : children}
 </QuestionDiscussionContext.Provider></QuestionAskerContext.Provider>
}

/** The same discussion and explicit decisions as the question dialog, retained after resolution. */
export function QuestionPanel({ id, title, active = true, worker, onTitle, onClose }: { id: string; title: string; active?: boolean; worker?: string; onTitle?: (title: string) => void; onClose?: () => void }) {
 return <QuestionCard asPanel onClose={onClose} onTitle={onTitle} active={active} worker={worker} id={id} question={title}><QuestionPanelActions id={id} /></QuestionCard>
}
function QuestionPanelActions({ id }: { id: string }) {
 const state = useContext(QuestionDiscussionContext)
 const question = state?.data?.question
 if (state?.error && !question) return null
 if (!question) return <p role="status">Loading question…</p>
 if (!question.answerable) return <p className="question-card-state">Only the question’s owner can answer.</p>
 return <AskReply binary={question.decision} messageId={id} answer={text => state!.client!.answer(id, text)} />
}
