import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { page } from 'vitest/browser'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async (url: string) => new Response(JSON.stringify(
  url.endsWith('/permissions') ? { can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [] }
    : { enabled: true, state: 'ready', attempts: 0 },
))) }))
vi.mock('../lib/engineModels', async original => ({
  ...await original<typeof import('../lib/engineModels')>(),
  useEngineModels: () => ({ models: [{ id: 'claude-opus-5-5', name: 'Opus 5.5', efforts: ['low', 'medium', 'high'], default_effort: 'medium' }], loading: false, fallback: false }),
}))

beforeEach(() => { localStorage.clear(); document.documentElement.setAttribute('data-theme', 'dark') })
afterEach(() => document.documentElement.removeAttribute('data-theme'))

it.each([390, 3840])('keeps Session controls compact, labeled and inside the panel at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const session = makeSession({ kind: 'agent', status: 'idle', engine: 'claude', runtime: 'daemon', model: 'claude-opus-5-5', effort: 'medium', limit_resume_supported: true, started_at: '2026-10-10T18:11:00Z' })
  render(<MemoryRouter><div style={{ height: '100vh' }}>
    <SessionRail id="polish-rail" session={session} machine="blerg" live={makeLive()} tasks={[]} childSessions={[]} toolChanges={0}
      mode={width === 390 ? 'drawer' : 'panel'} open onClose={() => {}} onOpenArtifact={() => {}} />
  </div></MemoryRouter>)
  const section = screen.getByTestId('rail-session')
  const model = within(section).getByRole('combobox', { name: 'Model' })
  const effort = within(section).getByRole('combobox', { name: 'Effort' })
  section.scrollIntoView()
  expect(Math.abs(model.getBoundingClientRect().top - effort.getBoundingClientRect().top)).toBeLessThan(2)
  // Agent standing rules are gone: no Rules button, no rules list.
  expect(within(section).queryByRole('button', { name: 'Rules' })).toBeNull()
  expect(within(section).queryByTestId('rules-panel')).toBeNull()
  expect(within(section).queryByTestId('rail-status')).toBeNull()
  const runtime = within(section).getByTestId('rail-runtime')
  expect(runtime).toHaveTextContent('This machine · blerg')
  const permission = within(runtime).getByTestId('session-permission')
  expect(Math.abs(permission.getBoundingClientRect().top - runtime.getBoundingClientRect().top)).toBeLessThan(5)
  const note = section.querySelector('.rail-runtime-note')!
  expect(note.getBoundingClientRect().height).toBeLessThan(22)
  const engine = within(section).getByTestId('rail-engine')
  expect(engine).toHaveTextContent('Engine Claude')
  const timing = within(section).getByTestId('rail-timing')
  expect(timing).toHaveTextContent(/Started Oct 10.* · running /)
  const options = within(section).getByRole('group', { name: 'Options' })
  expect(await within(options).findByRole('checkbox', { name: 'Resume when limit resets' })).toBeVisible()
  expect(within(options).getByRole('checkbox', { name: 'Nudge when the plan drifts' })).toBeVisible()
  // Static facts first (runs on, its sentence, the engine, when it started), then what can change
  // (model, effort), then the options: in the document and on the screen.
  const order = [runtime, note, engine, timing, model, effort, options]
  for (let i = 1; i < order.length; i++) {
    expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(order[i].getBoundingClientRect().top).toBeGreaterThanOrEqual(order[i - 1].getBoundingClientRect().top - 1)
  }
  expect(timing.getBoundingClientRect().bottom).toBeLessThanOrEqual(model.getBoundingClientRect().top)
  expect(model.getBoundingClientRect().bottom).toBeLessThanOrEqual(options.getBoundingClientRect().top)
  for (const el of [model, effort, permission, engine]) {
    const r = el.getBoundingClientRect(), bounds = section.getBoundingClientRect()
    expect(r.left).toBeGreaterThanOrEqual(bounds.left)
    expect(r.right).toBeLessThanOrEqual(bounds.right)
  }
  expect(section.scrollWidth).toBeLessThanOrEqual(section.clientWidth)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/session-polish-${width}.png` })
})
