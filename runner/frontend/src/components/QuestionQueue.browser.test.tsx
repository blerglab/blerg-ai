import { describe, it, expect, vi } from 'vitest'
import { act, render, screen, fireEvent, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { page } from 'vitest/browser'
import { setTheme } from '../../../../packages/settings/theme'
import '@blerglab/chat/blerg.css'
import '../index.css'
import SessionDockWindows from './SessionDockWindows'
import { makeSession } from '../test/fixtures'
import { useMessageStore } from '../hooks/useMessageStore'
import { useSessionStore } from '../hooks/useSessionStore'
import { HovercardProvider } from '@blerglab/chat'

vi.mock('../ws', () => import('../test/wsMock'))

for (const width of [390, 3840]) describe(`Question queue at ${width}`, () => {
  it('opens a compact card with keyboard and answers only a person question', async () => {
    await page.viewport(width, 900)
    localStorage.clear()
    useSessionStore.setState({ sessions: [makeSession({ id: 'main', title: 'Main' }), makeSession({ id: 'worker', parent_session_id: 'main', title: 'Worker' })] })
    useMessageStore.setState({ messages: [{ id: 'q', session_id: 'worker', kind: 'ask', body: 'Child "Worker", parent "Main": Choose a color?\nFull question details', status: 'open', answerable: true, created_at: '' }, { id: 'w', session_id: 'main', kind: 'ask', body: 'Main question', status: 'open', answerable: false, created_at: '' }] })
    const fetch = vi.spyOn(window, 'fetch').mockImplementation(async () => new Response('{}', { status: 200 }))
    render(<MemoryRouter><HovercardProvider value={{}}><div style={{ height: 800, display: 'flex', width: '100%' }}><SessionDockWindows sessionId="main" mobile={width === 390} files={<p>Workspace files</p>} /></div></HovercardProvider></MemoryRouter>)
    await page.getByRole('tab', { name: 'Questions' }).click()
    const button = screen.getByRole('button', { name: /Choose a color/ })
    expect(button).toHaveClass('workspace-row')
    expect(button.textContent).toContain('Worker · via Main · Needs you')
    expect(button.textContent).not.toContain('Child')
    expect(screen.queryByRole('button', { name: /Main question/ })).toBeNull()
    button.focus()
    if (width === 390) await page.getByRole('button', { name: /Choose a color/ }).click()
    else await userEvent.keyboard('{Enter}')
    expect(screen.getByRole('dialog').textContent).toContain('Full question details')
    expect(screen.getByRole('dialog').textContent).not.toContain('Child "')
    expect(useMessageStore.getState().messages[0].body).toContain('Child "Worker", parent "Main":')
    fireEvent.change(screen.getByLabelText('Your answer'), { target: { value: 'Blue' } })
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    fireEvent.click(screen.getByRole('button', { name: /Choose a color/ }))
    expect(screen.getByLabelText('Your answer')).toHaveValue('Blue')
    fireEvent.click(screen.getByRole('button', { name: 'Answer' }))
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/messages/q/reply', expect.objectContaining({ body: '{"answer":"Blue"}' })))
    act(() => useMessageStore.setState({ messages: useMessageStore.getState().messages.map(m => m.id === 'q' ? { ...m, status: 'answered' } : m) }))
    expect(screen.queryByRole('button', { name: /Choose a color/ })).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByLabelText('Your answer')).toBeNull()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    fetch.mockRestore()
  })
})

for (const width of [390, 1920, 3840]) it(`opens a board decision from Questions without a transcript at ${width}px`, async () => {
  await page.viewport(width, width === 390 ? 844 : 1080)
  localStorage.clear()
  setTheme(width === 3840 ? 'colorblind-dark' : 'light')
  const { useTranscriptStore } = await import('@blerglab/chat')
  useTranscriptStore.setState({ sessions: {} })
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', title: 'Plan engineering work' })] })
  useMessageStore.setState({ messages: [{ id: 'board-link', session_id: 'main', kind: 'ask', body: 'Board decision · 1 link\n\n- Link board: Engineering (ENG)\n  Reason: Track engineering work in this workspace.\n\nReply to this question with yes to commit these exact changes, or no to decline. Other replies ask for clarification. Expires in 24 hours.', status: 'open', decision: true, answerable: true, created_at: '2026-11-05T12:00:00Z' }] })
  render(<MemoryRouter><HovercardProvider value={{}}><div style={{ height: '95vh', display: 'flex', width: '100%' }}><SessionDockWindows sessionId="main" mobile={width === 390} files={<p>Workspace files</p>} /></div></HovercardProvider></MemoryRouter>)
  await page.getByRole('tab', { name: 'Questions' }).click()
  await page.getByRole('button', { name: /Board decision/ }).click()
  expect(screen.getByRole('button', { name: 'Approve' })).toBeVisible()
  expect(screen.getByRole('button', { name: 'Decline' })).toBeVisible()
  expect(screen.queryByRole('textbox')).toBeNull()
  expect(screen.getByRole('dialog').textContent).toContain('Engineering (ENG)')
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  const shots = import.meta.env.VITE_QUESTION_SHOT_DIR
  if (shots) {
    const frame = document.createElement('div')
    frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'
    document.body.append(frame)
    try { await page.screenshot({ element: frame, path: `${shots}/board-link-questions-${width}.png` }) }
    finally { frame.remove() }
  }
})

for (const width of [390, 3840]) it(`opens ticket references from the Questions tab at ${width}px`, async () => {
 await page.viewport(width, 900)
 localStorage.clear()
 setTheme(width === 390 ? 'colorblind-dark' : 'dark')
 const { resetSessionBoardsForTest } = await import('../lib/sessionBoard')
 resetSessionBoardsForTest()
 useSessionStore.setState({ sessions: [makeSession({ id: 'reference-main', title: 'Review question' })] })
 useMessageStore.setState({ messages: [{ id: 'reference-question', session_id: 'reference-main', kind: 'ask', body: 'Review this change?\n\nCheck BLG-514 and #515 before proceeding.', status: 'open', answerable: true, created_at: '' }] })
 const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200 })
 const fetch = vi.spyOn(window, 'fetch').mockImplementation(async input => {
  const url = String(input)
  if (url.includes('/permissions')) return json({ can_grant: false, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'reference-board', board_name: 'Engineering', level: 'read' }] })
  if (url.includes('/api/session-boards')) return json({ boards: [{ id: 'reference-board', key: 'BLG' }] })
  if (url.includes('/api/board-cards/reference-board/515')) return json({ card: { id: 'card-515', board_id: 'reference-board', number: 515, reference: 'BLG-515', title: 'Session board reference' } })
  if (url.includes('/api/board-cards/')) return json({ card: { id: 'card-514', board_id: 'reference-board', number: 514, reference: 'BLG-514', title: 'Keep question decisions readable', column_name: 'Ready', body: 'Review the question layout.' } })
  return json({})
 })
 render(<MemoryRouter><HovercardProvider value={{}}><div style={{ height: '95vh', display: 'flex', width: '100%' }}><SessionDockWindows sessionId="reference-main" mobile={width === 390} files={<p>Workspace files</p>} /></div></HovercardProvider></MemoryRouter>)
 await page.getByRole('tab', { name: 'Questions' }).click()
 await page.getByRole('button', { name: /Review this change/ }).click()
 const dialog = screen.getByRole('dialog')
 expect(await within(dialog).findByRole('button', { name: /Open card BLG-515/ })).toBeVisible()
 const ref = await within(dialog).findByRole('button', { name: /Open card BLG-514/ })
 await page.getByText('BLG-514', { exact: true }).hover()
 await expect.element(page.getByRole('tooltip')).toHaveTextContent('Keep question decisions readable')
 const shots = import.meta.env.VITE_QUESTION_SHOT_DIR
 if (shots) {
  const frame = document.createElement('div'); frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'; document.body.append(frame)
  try { await page.screenshot({ element: frame, path: `${shots}/ticket-question-${width}.png` }) } finally { frame.remove() }
 }
 fireEvent.click(ref)
 await expect.element(page.getByText('Review the question layout.')).toBeVisible()
 fetch.mockRestore()
})
