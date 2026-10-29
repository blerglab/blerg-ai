// #250: the session's model menu is filled at once from the launch sheet's model lists (per account,
// engine and daemon) on a session switch, and another account never sees them.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'

const who = vi.hoisted(() => ({ subject: 'person-a' }))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: who.subject, exp: 9999999999 }))}.x` }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../ws', () => import('../test/wsMock'))

import SessionSettings from './SessionSettings'
import { clearLaunchCache, launchModelsCache } from '../lib/launchCache'
import { makeSession } from '../test/fixtures'

const session = makeSession({ id: 's', kind: 'agent', status: 'idle', engine: 'claude', model: 'model-cached', daemon_id: 'd1' })
beforeEach(() => {
  who.subject = 'person-a'
  clearLaunchCache()
  // The server does not answer during the test: what shows comes from the cache or the built-in list.
  apiFetch.mockReset().mockImplementation(() => new Promise(() => {}))
})
afterEach(() => clearLaunchCache())

it('fills the model menu at once from the launch cache', () => {
  launchModelsCache.put('claude', 'd1', { models: [{ id: 'model-cached', name: 'Cached model', efforts: ['low'], default_effort: 'low', description: '', section: 'main' as const }, { id: 'model-other', name: 'Other cached', efforts: [], default_effort: '', description: '', section: 'main' as const }], source: 'live', engineEfforts: ['low'] })
  render(<SessionSettings session={session} />)
  const options = within(screen.getByRole('combobox', { name: 'Model' })).getAllByRole('option').map(o => o.getAttribute('value'))
  expect(options).toEqual(expect.arrayContaining(['model-cached', 'model-other']))
})

it("another account does not see the cached list", () => {
  launchModelsCache.put('claude', 'd1', { models: [{ id: 'model-other', name: 'Other cached', efforts: [], default_effort: '', description: '', section: 'main' as const }], source: 'live', engineEfforts: [] })
  who.subject = 'person-b'
  render(<SessionSettings session={session} />)
  const options = within(screen.getByRole('combobox', { name: 'Model' })).getAllByRole('option').map(o => o.getAttribute('value'))
  expect(options).not.toContain('model-other')
})
