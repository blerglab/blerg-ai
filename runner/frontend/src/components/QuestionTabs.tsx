import { useEffect } from 'react'
import { createPortal } from 'react-dom'
import { QuestionPanel } from '@blerglab/chat'
import type { SessionTabs } from '../hooks/useSessionTabs'
import { OPEN_QUESTION_TAB } from '../lib/questionThreads'
import { useSessionStore } from '../hooks/useSessionStore'
import { QuestionReferenceScope } from './QuestionRow'
import { rootSessionId } from '../lib/sessionTree'

export default function QuestionTabs({ tabs, sessionId, onOpen }: { tabs: SessionTabs; sessionId: string; onOpen?: () => void }) {
 const { open, setQuestionLabel } = tabs

 const sessions = useSessionStore(s => s.sessions)
 useEffect(() => {
  const receive = (event: Event) => {
   const detail = (event as CustomEvent<{ id: string; sessionId: string; title: string }>).detail
   const sessions = useSessionStore.getState().sessions
   if ((rootSessionId(sessions, detail.sessionId) ?? detail.sessionId) !== (rootSessionId(sessions, sessionId) ?? sessionId)) return
   event.preventDefault()
   open({ kind: 'question', questionId: detail.id, sessionId: detail.sessionId, label: `Question: ${detail.title.slice(0, 60)}` }, true)
   onOpen?.()
  }
  window.addEventListener(OPEN_QUESTION_TAB, receive)
  return () => window.removeEventListener(OPEN_QUESTION_TAB, receive)
 }, [open, sessionId, onOpen])
 return <>{tabs.tabs.map(tab => {
  const view = tab.view
  if (view.kind !== 'question') return null
  const worker = view.sessionId === (rootSessionId(sessions, view.sessionId) ?? view.sessionId) ? 'Main' : sessions.find(session => session.id === view.sessionId)?.title || 'Main'
  return createPortal(<QuestionReferenceScope sessionId={view.sessionId} id={view.questionId}>
   <QuestionPanel onClose={() => tabs.close(tab.id)} onTitle={title => setQuestionLabel(view.questionId, `Question: ${title.slice(0,60)}`)} worker={worker} active={tabs.visible.includes(tab.id)} id={view.questionId} title={view.label.replace(/^Question: /, '')} />
  </QuestionReferenceScope>, tabs.targetFor(tab.id), tab.id)
 })}</>
}
