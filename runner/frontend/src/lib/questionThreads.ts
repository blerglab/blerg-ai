import type { QuestionThreadsClient, QuestionThreadPage } from '@blerglab/chat'
import { apiFetch } from '../apiFetch'
import { useMessageStore } from '../hooks/useMessageStore'

export const OPEN_QUESTION_TAB = 'blerg-open-question-tab'
async function request(id: string, payload: unknown) {
 return apiFetch(`/api/messages/${encodeURIComponent(id)}/thread`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) })
}
export const questionThreads: QuestionThreadsClient = {
 async load(id, after = 0) {
  const response = await apiFetch(`/api/messages/${encodeURIComponent(id)}/thread?after=${after}`)
  if (!response.ok) throw new Error('Could not load question thread')
  return await response.json() as QuestionThreadPage
 },
 async reply(id, text, clientId) {
  if (!(await request(id, { reply: text, client_id: clientId })).ok) throw new Error('Could not send reply')
 },
 async answer(id, answer, revision = 0) {
  const response = await apiFetch(`/api/messages/${encodeURIComponent(id)}/reply`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ answer, revision_seq: revision }) })
  if (response.status === 409) { const body = await response.json() as {error?:string}; if(body.error?.startsWith('Main revised this question')) throw new Error('Main revised this question') }
  if (response.ok) useMessageStore.setState(state => ({ messages: state.messages.map(message => message.id === id ? { ...message, status: 'answered', answer } : message) }))
  return response.ok
 },
 async dismiss(id, revision = 0) {
  const response = await apiFetch(`/api/messages/${encodeURIComponent(id)}/dismiss`, { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({revision_seq: revision}) })
  if (response.status === 409) { const body = await response.json() as {error?:string}; if(body.error?.startsWith('Main revised this question')) throw new Error('Main revised this question') }
  if (response.ok) useMessageStore.setState(state => ({ messages: state.messages.map(message => message.id === id ? { ...message, status: 'answered', dismissed: true } : message) }))
  return response.ok
 },
 subscribe: (changed) => useMessageStore.subscribe(changed),
 openTab: (question, title) => !window.dispatchEvent(new CustomEvent(OPEN_QUESTION_TAB, { cancelable: true, detail: { id: question.id, sessionId: question.session_id, title } })),
}
