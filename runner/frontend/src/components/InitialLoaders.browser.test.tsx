import { beforeEach, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { page } from 'vitest/browser'
import SessionRail from './SessionRail'
import WorkspaceTree from './WorkspaceTree'
import ShellView from './ShellView'
import InitialLoader from './InitialLoader'
import { PinnedTicketModal } from './BoardCardView'
import { makeLive } from '../test/planTabFixture'
import { makeSession } from '../test/fixtures'
import { FakeShell } from '../test/shellMock'
import { RAIL_TAB_KEY } from '../lib/sessionRail'
import '../index.css'
import './ShellWorkspace.css'
import '@blerglab/chat/blerg.css'

vi.mock('../apiFetch', () => ({ apiFetch: () => new Promise(() => {}) }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../lib/chatTransport', async original => ({ ...await original<typeof import('../lib/chatTransport')>(), blergTransport: () => ({ files: null }) }))
const shots = import.meta.env.VITE_LOADER_SHOTS_DIR
const reduced = !!import.meta.env.VITE_LOADER_REDUCED_MOTION
beforeEach(() => { localStorage.clear() })

for (const surface of ['live', 'plan', 'history', 'files', 'capabilities', 'explorer', 'shell', 'board']) {
  it.each([390, 1024, 3840])(`${surface} has a labeled skeleton without overflow at %i px`, async width => {
    const height = width === 3840 ? 2160 : 844
    await page.viewport(width, height)
    localStorage.setItem(RAIL_TAB_KEY, surface)
    const shell = new FakeShell('loading'); shell.connectionState = 'connecting'
    let content
    const labels: Record<string, string> = { live: 'live session', explorer: 'workspace', board: 'board card' }
    if (surface === 'explorer') content = <WorkspaceTree sessionId="session" active />
    else if (surface === 'shell') content = <div style={{ height: 400, display: 'flex' }}><ShellView transport={shell} /></div>
    else if (surface === 'board') content = <PinnedTicketModal ticket={{ source: 'blerg-board', board_id: 'board', key: '#12', title: 'Example card' }} sessionId="session" onClose={() => {}} />
    else content = <SessionRail id="rail" session={makeSession()} live={makeLive({ settled: false })} mode="panel" open toolChanges={0} onClose={() => {}} onOpenArtifact={() => {}} />
    const view = render(<MemoryRouter><main style={{ padding: 12, minHeight: height - 24, boxSizing: 'border-box' }}>
      <h1 style={{ fontSize: 18 }}>Initial loader · {surface}</h1>
      <div style={{ width: surface === 'shell' ? '100%' : 'min(100%, 480px)' }}>{content}</div>
    </main></MemoryRouter>)
    const label = await screen.findByText(`Loading ${labels[surface] ?? surface}`)
    expect(label.getBoundingClientRect().width).toBe(1)
    const region = screen.getByRole('region', { name: labels[surface] ?? surface })
    expect(region.getAttribute('aria-busy')).toBe('true')
    const skeleton = region.querySelector<HTMLElement>('.initial-skeleton')!
    expect(skeleton.getBoundingClientRect().width).toBeGreaterThan(100)
    expect(skeleton.getBoundingClientRect().right).toBeLessThanOrEqual(width)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    expect(skeleton.scrollWidth).toBeLessThanOrEqual(skeleton.clientWidth)
    const bar = skeleton.querySelector('.initial-skeleton-value')!
    expect(getComputedStyle(bar, '::after').animationName).toBe(reduced ? 'none' : 'initial-loader-shimmer')
    if (surface === 'shell') {
      // Capture the actual xterm surface and its connection notice, not a standalone loader fixture.
      expect(view.container.querySelector('.shell-terminal-renderer .xterm-screen')).not.toBeNull()
      expect(screen.getByText('Connecting…').getBoundingClientRect().bottom).toBeLessThanOrEqual(region.getBoundingClientRect().top)
    }
    if (shots) await page.screenshot({ path: `${shots}/${surface}-${width}${reduced ? '-reduced' : ''}.png` })
    if (surface === 'shell') {
      act(() => shell.state?.('running'))
      expect(view.container.querySelector('[aria-busy]')).toBeNull()
      act(() => shell.emit('First real output\r\n'))
      expect(view.container.querySelector('.initial-skeleton')).toBeNull()
      expect(await screen.findByText(/First real output/)).toBeTruthy()
    }
  })
}

it('uses the same motion preference for all three skeleton shapes', async () => {
  render(<>{(['rows', 'facts', 'terminal'] as const).map(shape => <InitialLoader key={shape} loading label={shape} shape={shape} />)}</>)
  for (const shape of ['rows', 'facts', 'terminal']) {
    await screen.findByText(`Loading ${shape}`)
    const bar = document.querySelector(`.initial-skeleton-${shape} .initial-skeleton-value`)!
    expect(getComputedStyle(bar, '::after').animationName).toBe(reduced ? 'none' : 'initial-loader-shimmer')
  }
})
