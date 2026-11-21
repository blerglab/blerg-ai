import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import WhatsNew from './WhatsNew'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { makeSession } from '../test/fixtures'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', () => ({ coreOrigin: () => '', ensureFreshToken: async () => true, getAccessToken: () => 'test' }))
beforeEach(() => {
  HTMLDialogElement.prototype.showModal = function () { this.setAttribute('open', '') }
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })
const response = { current: '0.6.0', since: '0.5.4', pending: true, releases: [{ version: '0.6.0', title: 'Your workspace', items: ['Find your workers.'] }] }
function setup(path = '/sessions', sessions = [makeSession({status: 'idle'})]) {
  useSessionStore.setState({ serverVersion: 'fixture', serverRelease: '0.6.0', sessions })
  useMessageStore.setState({ messages: [] })
  const fetcher = vi.fn(async () => new Response(JSON.stringify(response)))
  vi.stubGlobal('fetch', fetcher)
  render(<MemoryRouter initialEntries={[path]}><WhatsNew /></MemoryRouter>)
  return fetcher
}
it('reads from runner and marks seen only after dismissal, then reopens', async () => {
  const fetcher = setup()
  expect(await screen.findByRole('dialog')).toBeTruthy()
  expect(fetcher).toHaveBeenCalledWith('/api/me/whats-new?release=0.6.0', expect.objectContaining({method:'GET'}))
  fireEvent.click(screen.getByRole('button', {name: 'Done'}))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(fetcher).toHaveBeenCalledWith('/api/me/whats-new?release=0.6.0', expect.objectContaining({method:'POST'}))
  fireEvent(window, new Event('blerg:whats-new'))
  expect(await screen.findByRole('dialog')).toBeTruthy()
})
it('defers unattended workers and waiting questions', async () => {
  const fetcher = setup('/sessions/worker', [makeSession({id:'worker', interaction:'unattended', status:'running'})])
  await waitFor(() => expect(fetcher).not.toHaveBeenCalled())
  expect(screen.queryByRole('dialog')).toBeNull()
})
it('does not claim private builds', async () => {
  useSessionStore.setState({serverRelease:''})
  const fetcher = vi.fn()
  vi.stubGlobal('fetch', fetcher)
  render(<MemoryRouter><WhatsNew /></MemoryRouter>)
  expect(fetcher).not.toHaveBeenCalled()
})
it('waits for an existing modal, then shows the unclaimed upgrade', async () => {
  const blocker = document.createElement('div')
  blocker.setAttribute('role', 'dialog')
  document.body.append(blocker)
  const fetcher = setup()
  expect(fetcher).not.toHaveBeenCalled()
  blocker.remove()
  expect(await screen.findByRole('dialog')).toBeTruthy()
  expect(fetcher).toHaveBeenCalledTimes(1)
})
it('does not show a notice another device already claimed', async () => {
  useSessionStore.setState({serverRelease:'0.6.0', sessions:[]})
  useMessageStore.setState({messages:[]})
  const fetcher = vi.fn(async () => new Response(JSON.stringify({...response, pending:false})))
  vi.stubGlobal('fetch', fetcher)
  render(<MemoryRouter><WhatsNew /></MemoryRouter>)
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1))
  expect(screen.queryByRole('dialog')).toBeNull()
})

it('includes skipped releases and leaves out notes already seen', async () => {
  useSessionStore.setState({serverRelease:'0.6.0', sessions:[]})
  useMessageStore.setState({messages:[]})
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({...response, releases:[
    {version:'0.5.4',title:'Earlier',items:['Already read.']},
    {version:'0.5.5',title:'Skipped',items:['A skipped improvement.']},
    ...response.releases,
  ]}))))
  render(<MemoryRouter><WhatsNew /></MemoryRouter>)
  await screen.findByRole('dialog')
  expect(screen.getByText('A skipped improvement.')).toBeTruthy()
  expect(screen.getByText('Find your workers.')).toBeTruthy()
  expect(screen.queryByText('Already read.')).toBeNull()
})

it('keeps unread notes deferred when a blocking question arrives during the read', async () => {
  useSessionStore.setState({serverRelease:'0.6.0', sessions:[makeSession({id:'worker',status:'idle'})]})
  useMessageStore.setState({messages:[]})
  let finish!: (response: Response) => void
  const fetcher = vi.fn(() => new Promise<Response>(resolve => { finish = resolve }))
  vi.stubGlobal('fetch',fetcher)
  render(<MemoryRouter><WhatsNew /></MemoryRouter>)
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1))
  act(() => useSessionStore.setState({sessions:[makeSession({id:'worker',status:'waiting'})]}))
  await act(async () => { finish(new Response(JSON.stringify(response))); await Promise.resolve() })
  expect(screen.queryByRole('dialog')).toBeNull()
  act(() => useSessionStore.setState({sessions:[makeSession({id:'worker',status:'idle'})]}))
  expect(await screen.findByRole('dialog')).toBeTruthy()
  expect(fetcher).toHaveBeenCalledTimes(1)
})

it('opens a manual request while an unattended worker is selected', async () => {
  setup('/sessions/worker', [makeSession({id:'worker',interaction:'unattended',status:'running'})])
  fireEvent(window,new Event('blerg:whats-new'))
  expect(await screen.findByRole('dialog')).toBeTruthy()
})
it('keeps the notes open after a lost dismissal response', async () => {
  setup()
  await screen.findByRole('dialog')
  vi.stubGlobal('fetch',vi.fn(async () => {throw new Error('offline')}))
  fireEvent.click(screen.getByRole('button',{name:'Done'}))
  expect(await screen.findByRole('alert')).toHaveTextContent(/could not.*save/i)
  expect(screen.getByRole('dialog')).toBeTruthy()
})
it('can maximize the notes', async () => {
  setup()
  await screen.findByRole('dialog')
  fireEvent.click(screen.getByRole('button',{name:'Maximize'}))
  expect(screen.getByRole('button',{name:'Restore size'})).toBeTruthy()
})
it('does not acknowledge on a breakpoint remount', async () => {
  const fetcher=setup()
  await screen.findByRole('dialog')
  cleanup()
  render(<MemoryRouter><WhatsNew /></MemoryRouter>)
  await screen.findByRole('dialog')
  expect(fetcher).toHaveBeenCalledTimes(2)
  for (const call of fetcher.mock.calls) expect(call).toEqual([expect.stringContaining('/api/me/whats-new'),expect.objectContaining({method:'GET'})])
})

it('opens a manual request over an existing dialog and waiting question', async () => {
  const blocker = document.createElement('div')
  blocker.setAttribute('role', 'dialog')
  document.body.append(blocker)
  setup('/sessions/worker', [makeSession({id:'worker',status:'waiting'})])
  fireEvent(window,new Event('blerg:whats-new'))
  expect(await screen.findByText('Find your workers.')).toBeTruthy()
  blocker.remove()
})

it('keeps a manual read when the blocking state changes before it finishes', async () => {
  setup('/sessions/worker', [makeSession({id:'worker', interaction:'unattended', status:'running'})])
  let finish!: (response: Response) => void
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => { finish = resolve })))
  fireEvent(window, new Event('blerg:whats-new'))
  await waitFor(() => expect(finish).toBeTypeOf('function'))
  act(() => useSessionStore.setState({ sessions: [] }))
  await act(async () => { finish(new Response(JSON.stringify(response))); await Promise.resolve() })
  expect(await screen.findByRole('dialog')).toBeTruthy()
})
