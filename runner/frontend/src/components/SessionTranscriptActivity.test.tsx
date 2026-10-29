import { useContext, useEffect, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { ViewerHostContext } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { sessionTabsKey } from '../hooks/useSessionTabs'
import { PaletteContext } from '../commands/context'
import { CommandRegistry } from '../commands/registry'

const effects = vi.hoisted(() => ({ active: 0 }))
function TranscriptProbe() {
  useEffect(() => { effects.active++; return () => { effects.active-- } }, [])
  return <div>Transcript subscription</div>
}
vi.mock('./AgentChatView', () => ({ default: function ProbeChat({ transcript }: { transcript: (content: ReactNode) => ReactNode }) {
  const host = useContext(ViewerHostContext)!
  return createPortal(<>{transcript(<TranscriptProbe />)}<textarea aria-label="Composer" /></>, host.chatTarget!)
} }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x` }))
beforeEach(() => {
  localStorage.clear()
  localStorage.setItem(sessionTabsKey('person-a', 'sess-a'), JSON.stringify({ tabs: [{ id: 'workspace:a.ts', view: { kind: 'workspace', path: 'a.ts' } }], active: 'chat' }))
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent' })] })
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}')))
})
afterEach(() => vi.unstubAllGlobals())
it('disconnects transcript effects only when Chat is absent from all visible groups', async () => {
  const registry = new CommandRegistry()
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><PaletteContext.Provider value={{ registry, open: () => {} }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></PaletteContext.Provider></MemoryRouter>)
  expect(effects.active).toBe(1)
  const composer = screen.getByRole('textbox', { name: 'Composer' })
  fireEvent.change(composer, { target: { value: 'Unsent draft' } })
  fireEvent.click(screen.getByRole('tab', { name: /a.ts/ }))
  await waitFor(() => expect(effects.active).toBe(0))
  act(() => registry.getSnapshot().items.find(c => c.title === 'Split tab group right')!.run())
  await waitFor(() => expect(effects.active).toBe(1))
  expect(screen.getByRole('textbox', { name: 'Composer' })).toBe(composer)
  expect(composer).toHaveValue('Unsent draft')
})
