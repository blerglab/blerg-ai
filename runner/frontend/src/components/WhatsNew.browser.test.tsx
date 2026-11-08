import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { commands, page, userEvent } from 'vitest/browser'
import WhatsNew from './WhatsNew'
import { consumeAccessTokenFromFragment } from '../authClient'
import { openWhatsNew } from '../lib/whatsNew'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import '../index.css'
import { makeSession } from '../test/fixtures'

vi.mock('../ws', () => import('../test/wsMock'))
afterEach(cleanup)
for (const width of [390,3840]) it(`uses real runner/core HTTP, scales and dismisses at ${width}px`,async () => {
  const fixture=await commands.startWhatsNewFixture()
  try {
    history.replaceState(null,'',location.pathname+location.search+'#access_token='+encodeURIComponent(fixture.token))
    consumeAccessTokenFromFragment()
    await page.viewport(width,1000)
    useSessionStore.setState({sessions:[],daemons:[],serverRelease:'0.6.0',serverVersion:'fixture'})
    useMessageStore.setState({messages:[]})
    const mount=() => render(<MemoryRouter><WhatsNew /></MemoryRouter>)
    mount()
    let dialog=await screen.findByRole('dialog')
    expect(await screen.findByRole('heading', {name: /More room for your work/})).toBeTruthy()
    const box=dialog.getBoundingClientRect()
    expect(box.left).toBeGreaterThanOrEqual(0)
    expect(box.right).toBeLessThanOrEqual(width)
    expect(box.bottom).toBeLessThanOrEqual(1000)
    if (width===3840) expect(box.width).toBeLessThanOrEqual(720)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    expect(document.activeElement).toBe(screen.getByRole('button',{name:'Done'}))
    await userEvent.keyboard('{Tab}')
    expect(dialog.contains(document.activeElement)).toBe(true)
    await userEvent.keyboard('{Enter}')
    expect(dialog.getBoundingClientRect().width).toBeGreaterThan(width-40)
    // A breakpoint remount or reload before dismissal retains the unread notes.
    cleanup();mount()
    dialog=await screen.findByRole('dialog')
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    cleanup();mount()
    // Read the real same-origin route to prove dismissal persisted.
    const response=await fetch('/api/me/whats-new?release=0.6.0',{headers:{Authorization:'Bearer '+fixture.token}})
    expect(response.status).toBe(200)
    expect((await response.json()).pending).toBe(false)
    useSessionStore.setState({ sessions: [makeSession({ status: 'waiting' })] })
    openWhatsNew()
    await screen.findByRole('dialog')
    await userEvent.click(page.getByRole('button',{name:'Done'}))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  } finally {cleanup();await commands.stopWhatsNewFixture(fixture.token)}
},120000)
