import { afterEach, beforeEach, expect, it, vi } from 'vitest'

const { getAccessToken, startTokenRenewal, initializePersonTime, render } = vi.hoisted(() => ({
  getAccessToken: vi.fn<() => string | null>(),
  startTokenRenewal: vi.fn(),
  initializePersonTime: vi.fn(),
  render: vi.fn(),
}))
vi.mock('./App.tsx', () => ({ default: () => null }))
vi.mock('react-dom/client', () => ({ createRoot: () => ({ render }) }))
vi.mock('./authClient', () => ({ getAccessToken, startTokenRenewal, consumeAccessTokenFromFragment: vi.fn() }))
vi.mock('./lib/personTime', () => ({ initializePersonTime }))
vi.mock('./lib/codeColours', () => ({ applyCodeColoursToPage: vi.fn() }))
vi.mock('./refreshGuard', () => ({ clearRefreshAttempted: vi.fn() }))

beforeEach(() => {
  vi.resetModules()
  vi.clearAllMocks()
})
afterEach(() => {
  // Consume the once-only startup listener even if an assertion failed.
  window.dispatchEvent(new Event('blerg-auth-refreshed'))
})

it('starts renewal and captures preferences after silent cold-load authentication, once', async () => {
  getAccessToken.mockReturnValue(null)
  await import('./main')
  expect(render).toHaveBeenCalledTimes(1)
  expect(startTokenRenewal).not.toHaveBeenCalled()
  getAccessToken.mockReturnValue('test-token')
  window.dispatchEvent(new Event('blerg-auth-refreshed'))
  expect(startTokenRenewal).toHaveBeenCalledTimes(1)
  expect(initializePersonTime).toHaveBeenCalledTimes(1)
  window.dispatchEvent(new Event('blerg-auth-refreshed'))
  expect(initializePersonTime).toHaveBeenCalledTimes(1)
})

it('initializes immediately when a redirect supplied a token', async () => {
  getAccessToken.mockReturnValue('test-token')
  await import('./main')
  expect(startTokenRenewal).toHaveBeenCalledTimes(1)
  expect(initializePersonTime).toHaveBeenCalledTimes(1)
})
