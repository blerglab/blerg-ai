import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { apiFetch } from '../apiFetch'
import { disableNotifications, pushState, restoreNotifications, enableNotifications } from './push'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
const unsubscribe = vi.fn(async () => true)
const subscription = { endpoint: 'https://push.example.com/browser', unsubscribe }
const getSubscription = vi.fn(async (): Promise<typeof subscription | null> => subscription)
beforeEach(() => {
  localStorage.setItem('blerg_notifications_wanted', '0')
  window.dispatchEvent(new Event('blerg-login-changed'))
  localStorage.clear()
  vi.stubGlobal('Notification', { permission: 'granted', requestPermission: vi.fn(async () => 'granted') })
  vi.stubGlobal('PushManager', class {})
  Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: { getRegistration: vi.fn(async () => ({ pushManager: { getSubscription } })), register: vi.fn(async () => ({ pushManager: { getSubscription, subscribe: vi.fn(async () => subscription) } })) } })
  getSubscription.mockResolvedValue(subscription)
  vi.mocked(apiFetch).mockReset().mockResolvedValue(new Response(null, { status: 204 }))
})
afterEach(() => { vi.unstubAllGlobals(); vi.clearAllMocks() })
it('keeps opt-in and browser subscription when server deletion fails', async () => {
  localStorage.setItem('blerg_notifications_wanted', '1')
  vi.mocked(apiFetch).mockResolvedValue(new Response(null, { status: 503 }))
  await expect(disableNotifications()).rejects.toThrow()
  expect(localStorage.getItem('blerg_notifications_wanted')).toBe('1')
  expect(unsubscribe).not.toHaveBeenCalled()
})
it('cleans the remembered endpoint even if the browser lost its subscription', async () => {
  localStorage.setItem('blerg_notifications_wanted', '1')
  localStorage.setItem('blerg_notifications_endpoint', subscription.endpoint)
  getSubscription.mockResolvedValue(null)
  await disableNotifications()
  expect(apiFetch).toHaveBeenCalledWith('/api/push/subscribe', expect.objectContaining({ method: 'DELETE', body: JSON.stringify({ endpoint: subscription.endpoint }) }))
  expect(pushState()).toBe('off')
})
it('does not infer opt-in from granted permission before checking the subscription', () => {
  expect(pushState()).toBe('off')
  expect(Notification.requestPermission).not.toHaveBeenCalled()
})

it('restores a legacy granted subscription and records its endpoint without prompting', async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({ publicKey: 'YWJj' })))
  await restoreNotifications()
  expect(Notification.requestPermission).not.toHaveBeenCalled()
  expect(apiFetch).toHaveBeenLastCalledWith('/api/push/subscribe', expect.objectContaining({ method: 'POST' }))
  expect(localStorage.getItem('blerg_notifications_endpoint')).toBe(subscription.endpoint)
})
it.each(['0', '1'])('restores only an explicit opt-in (%s), without prompting for default permission', async flag => {
  localStorage.setItem('blerg_notifications_wanted', flag)
  vi.stubGlobal('Notification', { permission: 'default', requestPermission: vi.fn() })
  await restoreNotifications()
  expect(Notification.requestPermission).not.toHaveBeenCalled()
  expect(apiFetch).not.toHaveBeenCalled()
})
it('keeps explicit Turn off across reload with granted permission', async () => {
  await disableNotifications()
  await restoreNotifications()
  expect(pushState()).toBe('off')
  expect(apiFetch).toHaveBeenCalledTimes(1)
})
it('does not report an opt-in when registration fails', async () => {
  localStorage.setItem('blerg_notifications_wanted', '0')
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({ publicKey: 'YWJj' }))).mockResolvedValueOnce(new Response(null, { status: 503 }))
  await expect(enableNotifications()).rejects.toThrow()
  expect(pushState()).toBe('off')
})
it('drops the browser subscription on confirmed sign-out but preserves opt-in', async () => {
  localStorage.setItem('blerg_notifications_wanted','1')
  vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 204 })))
  window.dispatchEvent(new Event('blerg-signed-out'))
  await vi.waitFor(() => expect(unsubscribe).toHaveBeenCalledOnce())
  await vi.waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/push/subscribe', expect.objectContaining({ method: 'DELETE' })))
  expect(localStorage.getItem('blerg_notifications_wanted')).toBe('1')
})
it('unsubscribes on sign-out even while a restore request is still in flight', async () => {
  let finish!: (response: Response) => void
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({ publicKey: 'YWJj' }))).mockImplementationOnce(() => new Promise<Response>(resolve => { finish = resolve }))
  vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 204 })))
  const restoring = restoreNotifications()
  await vi.waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
  window.dispatchEvent(new Event('blerg-signed-out'))
  try {
    await vi.waitFor(() => expect(unsubscribe).toHaveBeenCalled())
  } finally {
    finish(new Response(null, { status: 201 }))
    await restoring
  }
  expect(localStorage.getItem('blerg_notifications_wanted')).toBe('1')
})
it('does not mistake a failed new opt-in for legacy granted permission', async () => {
  const notification = { permission: 'default', requestPermission: vi.fn(async () => { notification.permission = 'granted'; return 'granted' }) }
  vi.stubGlobal('Notification', notification)
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({ publicKey: 'YWJj' }))).mockResolvedValueOnce(new Response(null, { status: 503 }))
  await expect(enableNotifications()).rejects.toThrow()
  expect(pushState()).toBe('off')
})
it('does not restore a main-era Turn off with granted permission and no subscription', async () => {
  getSubscription.mockResolvedValue(null)
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify({ publicKey: 'YWJj' })))
  await restoreNotifications()
  expect(apiFetch).not.toHaveBeenCalled()
  expect(localStorage.getItem('blerg_notifications_wanted')).toBeNull()
})
it('keeps opt-in through expired sign-out and restores on the next login', async () => {
  localStorage.setItem('blerg_notifications_wanted', '1')
  localStorage.setItem('blerg_notifications_endpoint', subscription.endpoint)
  vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 401 })))
  window.dispatchEvent(new Event('blerg-signed-out'))
  await vi.waitFor(() => expect(fetch).toHaveBeenCalled())
  expect(localStorage.getItem('blerg_notifications_wanted')).toBe('1')
  expect(localStorage.getItem('blerg_notifications_endpoint')).toBe(subscription.endpoint)
  getSubscription.mockResolvedValue(null)
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response(null, { status: 204 })).mockResolvedValueOnce(new Response(JSON.stringify({ publicKey: 'YWJj' }))).mockResolvedValueOnce(new Response(null, { status: 201 }))
  window.dispatchEvent(new Event('blerg-login-changed'))
  await vi.waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/push/subscribe',expect.objectContaining({ method: 'POST' })))
  expect(apiFetch).toHaveBeenCalledWith('/api/push/subscribe',expect.objectContaining({ method: 'DELETE' }))
})
