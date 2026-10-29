import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { Markdown, refStoreFor, resetRefStoresForTest, REF_DEBOUNCE_MS, type RefStore } from '@blerglab/chat'
import { RefContext } from '../../../../packages/chat/src/components/RefLinks'
import { boardCardsReader } from './cardRefs'
import { checkAccount, resetLiveBoardCardsForTest } from './liveBoardCard'
import * as auth from '../authClient'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const TITLE = 'PRIVATE TITLE'
const key = 'card:private-board:7'
function signIn(sub: string) {
  const token = `h.${btoa(JSON.stringify({ sub }))}.s`
  history.replaceState(null, '', `/#access_token=${token}`)
  auth.consumeAccessTokenFromFragment()
}
const reply = (allowed: boolean) => new Response(JSON.stringify(allowed
  ? { card: { id: 'private-card', board_id: 'private-board', number: 7, title: TITLE }, url: '' }
  : { error: 'Forbidden' }), { status: allowed ? 200 : 403 })
const settle = () => act(async () => { await vi.advanceTimersByTimeAsync(REF_DEBOUNCE_MS) })
function mount(store: RefStore, bare: boolean) {
  return render(<RefContext.Provider value={{ store, sessionId: 'shared-session', refs: { openCard: vi.fn() } }}>
    <Markdown text={bare ? '#7' : 'board:private-board#7'} />
  </RefContext.Provider>)
}
function install(store: RefStore) {
  const read = boardCardsReader({ id: 'private-board', name: 'Private board' }, 'shared-session')
  store.setResolvers({ cards: read, boardCards: (_board, ns) => read(ns), commit: null })
}
beforeEach(() => {
  vi.useFakeTimers()
  resetRefStoresForTest()
  resetLiveBoardCardsForTest()
  apiFetch.mockReset()
  signIn('account-a')
  checkAccount()
})
afterEach(() => { resetRefStoresForTest(); resetLiveBoardCardsForTest(); vi.restoreAllMocks(); vi.useRealTimers() })

for (const { surface, bare, session } of [
  { surface: 'qualified chat reference', bare: false, session: 'shared-session' },
  { surface: 'bare chat reference', bare: true, session: 'shared-session' },
  { surface: 'checklist reference', bare: true, session: 'checklist:shared-session' },
]) describe(surface, () => {
  it('remounts for a denied viewer without the previous viewer’s private title', async () => {
    apiFetch.mockImplementation(async () => reply(true))
    const old = refStoreFor(session)
    install(old)
    const view = mount(old, bare)
    await settle()
    expect(screen.getByRole('button', { name: `Open card #7: ${TITLE}` })).toBeInTheDocument()
    view.unmount()

    apiFetch.mockImplementation(async () => reply(false))
    act(() => { signIn('account-b'); checkAccount() })
    const current = refStoreFor(session)
    install(current)
    mount(current, bare)
    // Check before and after the 403: no transient disclosure on remount either.
    expect(document.body.innerHTML).not.toContain(TITLE)
    await settle()
    expect(apiFetch).toHaveBeenCalledTimes(2)
    expect(document.body.innerHTML).not.toContain(TITLE)
    expect(screen.queryByTestId('md-ref')).toBeNull()
    expect(document.querySelector(`[data-ref-candidate="${bare ? 'card:7' : key}"]`)).toHaveAttribute('data-ref-state', 'none')
  })

  it('discards an old HTTP read that finishes after the identity event', async () => {
    let finish!: (response: Response) => void
    apiFetch.mockImplementationOnce(() => new Promise<Response>(resolve => { finish = resolve }))
    const old = refStoreFor(session)
    install(old)
    const view = mount(old, bare)
    await settle()
    view.unmount()
    apiFetch.mockImplementation(async () => reply(false))
    act(() => { signIn('account-b'); checkAccount() })
    const current = refStoreFor(session)
    install(current)
    mount(current, bare)
    await settle()
    await act(async () => { finish(reply(true)) })
    await settle()
    expect(old.get(bare ? 'card:7' : key)).toEqual({ state: 'pending' })
    expect(document.body.innerHTML).not.toContain(TITLE)
    expect(screen.queryByTestId('md-ref')).toBeNull()
    expect(apiFetch).toHaveBeenCalledTimes(2)
  })
})

it('clears mounted card chips and cached commits synchronously on the identity event', async () => {
  apiFetch.mockImplementation(async () => reply(true))
  const store = refStoreFor('shared-session')
  const read = boardCardsReader({ id: 'private-board' }, 'shared-session')
  const sha = '08025f2c9d1e4b7a0c3f5e6d7a8b9c0d1e2f3a4b'
  store.setResolvers({ cards: read, boardCards: (_board, ns) => read(ns), commit: async () => sha })
  store.want('card:7')
  store.want(`commit:${sha.slice(0, 8)}`)
  mount(store, false)
  await settle()
  expect(store.get('card:7').state).toBe('card')
  expect(store.get(`commit:${sha.slice(0, 8)}`).state).toBe('commit')
  expect(screen.getByRole('button', { name: `Open card #7: ${TITLE}` })).toBeInTheDocument()
  act(() => { signIn('account-b') }) // No checkAccount or remount is needed to forget private answers.
  expect(document.body.innerHTML).not.toContain(TITLE)
  for (const key of ['card:7', 'card:private-board:7', `commit:${sha.slice(0, 8)}`] as const) {
    expect(store.get(key)).toEqual({ state: 'pending' })
  }
  expect(refStoreFor('shared-session')).not.toBe(store)
})

it('also clears reference stores when checkAccount observes a changed subject without an event', async () => {
  const store = refStoreFor('shared-session')
  install(store)
  apiFetch.mockImplementation(async () => reply(true))
  mount(store, false)
  await settle()
  expect(screen.queryByTestId('md-ref')).not.toBeNull()
  vi.spyOn(auth, 'getAccessToken').mockReturnValue(`h.${btoa(JSON.stringify({ sub: 'account-b' }))}.s`)
  act(() => checkAccount())
  expect(document.body.innerHTML).not.toContain(TITLE)
  expect(store.get(key)).toEqual({ state: 'pending' })
  expect(refStoreFor('shared-session')).not.toBe(store)
})
