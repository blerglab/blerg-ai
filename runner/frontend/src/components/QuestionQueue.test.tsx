import { it, expect, vi } from 'vitest'
import { act, render, screen, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import QuestionQueue from './QuestionQueue'
import SessionDockWindows from './SessionDockWindows'
import { OpenReviewContext } from '../lib/openReview'
import { useToastStore } from '../hooks/useToastStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { useSessionStore } from '../hooks/useSessionStore'
import type { SessionInfo } from '../types'
import { HovercardProvider } from '@blerglab/chat'

vi.mock('../ws', () => import('../test/wsMock'))

it('opens a compact card with keyboard and answers only a person question', async () => {
  useSessionStore.setState({ sessions: [{ id: 'main', title: 'Main' }, { id: 'worker', parent_session_id: 'main', title: 'Worker' }] as SessionInfo[] })
  useMessageStore.setState({ messages: [{ id: 'q', session_id: 'worker', kind: 'ask', body: 'Choose a color?\nFull question details', status: 'open', answerable: true, created_at: '' }, { id: 'w', session_id: 'worker', kind: 'ask', body: 'Main question', status: 'open', answerable: false, created_at: '' }] })
  const fetch = vi.spyOn(window, 'fetch').mockImplementation(async () => new Response('{}', { status: 200 }))
  render(<MemoryRouter><HovercardProvider value={{}}><QuestionQueue sessionId="main" /></HovercardProvider></MemoryRouter>)
  const button = screen.getByRole('button', { name: /Choose a color/ })
  button.focus()
  await userEvent.keyboard('{Enter}')
  expect(screen.getByRole('dialog').textContent).toContain('Full question details')
  fireEvent.change(screen.getByLabelText('Your answer'), { target: { value: 'Blue' } })
  fireEvent.click(screen.getByRole('button', { name: 'Answer' }))
  await vi.waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/messages/q/reply', expect.objectContaining({ body: '{"answer":"Blue"}' })))
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
  expect(screen.queryByRole('button', { name: /Main question/ })).toBeNull()
  expect(screen.queryByLabelText('Your answer')).toBeNull()

  fetch.mockRestore()
})

it('offers binary actions only for a server-marked bound question', async () => {
  const {useTranscriptStore} = await import('@blerglab/chat')
  useSessionStore.setState({sessions:[{id:'main',title:'Main'}] as SessionInfo[]})
  useMessageStore.setState({messages:[{id:'bound',session_id:'main',kind:'ask',body:'Link board?',decision:true,status:'open',answerable:true,created_at:''}]})
  useTranscriptStore.setState({sessions:{}})
  const fetch = vi.spyOn(window,'fetch').mockImplementation(async () => new Response('{}',{status:200}))
  render(<MemoryRouter><HovercardProvider value={{}}><QuestionQueue sessionId="main" /></HovercardProvider></MemoryRouter>)
  fireEvent.click(screen.getByRole('button',{name:/Link board/}))
  expect(screen.queryByRole('textbox')).toBeNull()
  fireEvent.click(screen.getByRole('button',{name:'Decline'}))
  await vi.waitFor(()=>expect(fetch).toHaveBeenCalledWith('/api/messages/bound/reply',expect.objectContaining({body:'{"answer":"no"}'})))
  fetch.mockRestore()
  useTranscriptStore.setState({sessions:{}})
})

it('counts and lists the same question and review, then removes a resolved review', async () => {
  useSessionStore.setState({ sessions: [{ id: 'main', title: 'Main', status: 'idle' }] as SessionInfo[] })
  useMessageStore.setState({ messages: [{ id: 'q', session_id: 'main', kind: 'ask', body: 'Publish the notes?', status: 'open', answerable: true, created_at: '' }] })
  let pending = true
  const openReview = vi.fn()
  const fetch = vi.spyOn(window, 'fetch').mockImplementation(async input => new Response(JSON.stringify(String(input).endsWith('/live') ? { session_id: 'main', review_requests: pending ? [{ request_id: 'review', artifact_id: 'notes', artifact_name: 'release-notes.md', artifact_version: 1, ask: 'Check the release notes', created_at: '' }] : [] } : {}), { status: 200 }))
  render(<MemoryRouter><HovercardProvider value={{}}><OpenReviewContext.Provider value={openReview}><SessionDockWindows sessionId="main" mobile files={null} /></OpenReviewContext.Provider></HovercardProvider></MemoryRouter>)
  const tab = screen.getByRole('tab', { name: 'Questions' })
  fireEvent.click(tab)
  expect(await screen.findByRole('button', { name: /Review: release-notes.md/ })).toHaveClass('workspace-row')
  expect(screen.getByRole('button', { name: /Publish the notes/ })).toHaveClass('workspace-row')
  expect(tab).toHaveAttribute('aria-description', '2 open questions')
  fireEvent.click(screen.getByRole('button', { name: /Review: release-notes.md/ }))
  expect(openReview).toHaveBeenCalledExactlyOnceWith('main', 'notes')
  fireEvent.click(screen.getByRole('button', { name: 'Dismiss review request: release-notes.md' }))
  expect(screen.queryByRole('button', { name: /Review: release-notes.md/ })).toBeNull()
  expect(tab).toHaveAttribute('aria-description', '1 open questions')
  act(() => useToastStore.getState().toasts.find(t => t.action?.label === 'Undo')!.action!.onClick())
  expect(screen.getByRole('button', { name: /Review: release-notes.md/ })).toBeVisible()
  expect(tab).toHaveAttribute('aria-description', '2 open questions')
  pending = false
  act(() => window.dispatchEvent(new Event('blerg:review-requests-changed')))
  await vi.waitFor(() => expect(screen.queryByRole('button', { name: /Review: release-notes.md/ })).toBeNull())
  expect(tab).toHaveAttribute('aria-description', '1 open questions')
  fetch.mockRestore()
})

it('opens a review from an unopened descendant with its own session and exact file version', async () => {
  useSessionStore.setState({ sessions: [{ id: 'main', title: 'Main' }, { id: 'worker', title: 'Worker', parent_session_id: 'main' }, { id: 'unrelated' }, { id: 'archived', parent_session_id: 'main', archived_at: '2026-11-01' }] as SessionInfo[] })
  useMessageStore.setState({ messages: [] })
  const fetch = vi.spyOn(window, 'fetch').mockImplementation(async input => {
    const id = String(input).split('/')[3]
    return new Response(JSON.stringify({ session_id: id, review_requests: id === 'worker' ? [{ request_id: 'child-review', artifact_id: 'notes-v2', artifact_name: 'worker-notes.md', artifact_version: 2, ask: 'Check this version' }] : [] }), { status: 200 })
  })
  const openReview = vi.fn()
  render(<MemoryRouter><HovercardProvider value={{}}><OpenReviewContext.Provider value={openReview}><QuestionQueue sessionId="main" /></OpenReviewContext.Provider></HovercardProvider></MemoryRouter>)
  fireEvent.click(await screen.findByRole('button', { name: 'Review: worker-notes.md' }))
  expect(openReview).toHaveBeenCalledExactlyOnceWith('worker', 'notes-v2')
  expect(fetch.mock.calls.map(([url]) => String(url))).not.toContain('/api/sessions/archived/live')
  expect(fetch.mock.calls.map(([url]) => String(url))).not.toContain('/api/sessions/unrelated/live')
  fetch.mockRestore()
})
