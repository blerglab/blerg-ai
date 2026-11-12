// Navigate the shipped application; only the service boundary is replaced.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import App from '../App'
import '../index.css'
import { richInsights, richPrices } from '../test/insightsFixture'
import { richActivity } from '../test/activityFixture'
import { makeDaemon, makeSession } from '../test/fixtures'
import { useSessionStore } from '../hooks/useSessionStore'
import { setSettingsOpen } from '../lib/settingsDialog'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => 'fixture', redirectToRefresh: vi.fn() }))
vi.mock('../claims', async original => ({ ...await original<typeof import('../claims')>(), tokenExpired: () => false }))
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } }))
beforeEach(() => {
  localStorage.clear(); sessionStorage.clear()
  localStorage.setItem('blerg-theme', 'dark'); document.documentElement.dataset.theme = 'dark'
  useSessionStore.setState({ sessions: [makeSession({ title: 'Prepare the autumn release', repo: 'example-repo' })], daemons: [makeDaemon({ available_engines: ['claude'] })] })
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
    const url = String(input)
    if (url.startsWith('/api/insights/activity')) return json(richActivity)
    if (url.startsWith('/api/insights/prices')) return json(richPrices)
    if (url.startsWith('/api/insights')) return json(richInsights)
    if (url === '/api/daemons') return json([{ ...makeDaemon({ available_engines: ['claude'] }), sandbox_available: true }])
    if (url === '/api/repos') return json({ repos: [{ name: 'example-repo', full_name: 'example-org/example-repo', checked_out: ['workstation'] }] })
    if (url.startsWith('/api/runner/launch-options')) return json({ state: 'known', engines: [{ id: 'claude', name: 'Claude', restricted: true }] })
    if (url === '/api/cluster/status') return json({ configured: false })
    if (url === '/api/me/theme') return json({ theme: 'dark' })
    if (url === '/api/me/preferences') return json({ branch_template: 'wip/{title}-{id}' })
    if (url === '/api/proposals') return json({ proposals: [] })
    if (url.includes('skills')) return json({ skills: [], roots: [] })
    if (url.includes('usage-notices')) return json({ thresholds: [80, 90, 95] })
    if (url.includes('/engines')) return json({ engines: [] })
    if (url.includes('/version')) return json({ release: '0.5.0', commit: 'fixture' })
    if (url.includes('delegation')) return json({ rules: [], limits: { per_parent_running: 2, per_parent_starts_per_hour: 10, per_account_running: 4, per_account_starts_per_hour: 20 } })
    if (url.includes('credentials')) return json({ unavailable: true })
    return json({})
  }))
})
afterEach(() => { cleanup(); setSettingsOpen(false); vi.unstubAllGlobals(); history.replaceState({}, '', '/') })
for (const width of [390, 3840]) it(`audits runner navigation at ${width}px`, async () => {
  await page.viewport(width, 1000)
  history.replaceState({}, '', '/sessions')
  const host = document.createElement('div'); host.id = 'root'; document.body.append(host)
  render(<App />, { container: host })
  await screen.findByText('Prepare the autumn release')
  const shots = import.meta.env.VITE_AUDIT_SHOTS_DIR
  const shot = async (name: string) => {
    if (!shots) return
    // Clip to the app viewport rather than the test runner's enclosing iframe.
    const frame = document.createElement('div')
    frame.style.cssText = 'position:fixed;inset:0;pointer-events:none'
    document.body.append(frame)
    try { await page.screenshot({ element: frame, path: `${shots}/${name}-${width}.png` }) }
    finally { frame.remove() }
  }
  await shot('sidebar')
  await userEvent.click(screen.getAllByRole('button', { name: /New workspace|\+ New/ })[0])
  await screen.findByText('example-repo', { selector: '.launch-panel *' })
  await shot('launch')
  await userEvent.click(screen.getByRole('button', { name: 'Close' }))
  const settings = screen.getByTestId(width < 900 ? 'settings-tab' : 'settings-nav')
  settings.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  await userEvent.click(settings)
  await screen.findByRole('dialog', { name: 'Settings' })
  await screen.findByText('No standing rules. Agents ask before starting another worker.')
  await shot('runner-settings')
  await userEvent.click(screen.getByText('Session verbosity', { selector: 'summary' }))
  screen.getByLabelText('Default verbosity').scrollIntoView({ block: 'center' })
  await shot('verbosity')
  if (!import.meta.env.VITE_AUDIT_BEFORE) expect(screen.getByLabelText('Default verbosity').getBoundingClientRect().width).toBeLessThan(220)
  await userEvent.click(screen.getByRole('button', { name: 'Close' }))
  const insights = width < 900 ? screen.getByRole('button', { name: 'Insights' }) : screen.getByTestId('insights-nav')
  insights.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  await userEvent.click(insights)
  await screen.findByTestId('question-health')
  if(width<900)await userEvent.selectOptions(screen.getByRole('combobox',{name:'Insights view'}),'spending')
  else await userEvent.click(screen.getByRole('button',{name:'Spending'}))
  await userEvent.click(screen.getByText('Model prices', {selector:'summary'}))
  await screen.findByRole('button', { name: /remove claude-opus-5-5/i })
  await shot('insights')
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})
