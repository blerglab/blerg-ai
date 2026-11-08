// Push notifications to this browser (a phone's or a desktop's): the setting in the Notifications
// bell turns them on. Nothing here asks for permission on its own.
import { apiFetch } from '../apiFetch'
import { getAccessToken } from '../authClient'

function urlBase64ToUint8Array(base64String: string): Uint8Array {
  const padding = '='.repeat((4 - base64String.length % 4) % 4)
  const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/')
  const rawData = window.atob(base64)
  return Uint8Array.from([...rawData].map(c => c.charCodeAt(0)))
}

const WANTED = 'blerg_notifications_wanted'
const ENDPOINT = 'blerg_notifications_endpoint'

function read(key: string): string | null {
  try { return localStorage.getItem(key) } catch { return null }
}
function remember(key: string, value: string) {
  try { localStorage.setItem(key, value) } catch { /* storage unavailable */ }
}
function wanted(): boolean { return read(WANTED) === '1' }

/** Where push stands in this browser: it can't (no Notification, Service Worker or Push support),
 *  it is blocked in the browser's settings, it is on (permission granted and turned on here), or
 *  it can be turned on. */
export type PushState = 'unsupported' | 'blocked' | 'on' | 'off'

export function pushSupported(): boolean {
  return typeof Notification !== 'undefined' && typeof navigator !== 'undefined' && 'serviceWorker' in navigator && 'PushManager' in window
}

export function pushState(): PushState {
  if (!pushSupported()) return 'unsupported'
  if (Notification.permission === 'denied') return 'blocked'
  // Explicit Turn off stays off even while browser permission remains granted.
  if (Notification.permission === 'granted' && wanted()) return 'on'
  return 'off'
}

/** Turns push off for this browser: the server forgets its subscription, the browser drops it, and
 *  the app no longer restores it on load. The browser's permission stays as the person set it. */
export function disableNotifications() {
  identityRevision++
  return serial(removeNotifications)
}

async function removeNotifications() {
  const registration = pushSupported() ? await navigator.serviceWorker.getRegistration('/sw.js') : undefined
  const subscription = await registration?.pushManager.getSubscription()
  const endpoint = subscription?.endpoint ?? read(ENDPOINT)
  if (endpoint) {
    remember(ENDPOINT, endpoint)
    const response = await apiFetch('/api/push/subscribe', { method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ endpoint }) })
    if (!response.ok) throw new Error('Could not remove push subscription')
  }
  remember(WANTED, '0')
  if (subscription) await subscription.unsubscribe()
  try { localStorage.removeItem(ENDPOINT) } catch { /* storage unavailable */ }
}

let identityRevision = 0
let signedOut = false
let signOutCleanup: Promise<void> = Promise.resolve()
let pending: Promise<void> = Promise.resolve()
function serial(action: () => Promise<void>): Promise<void> {
  const next = pending.catch(() => {}).then(action)
  pending = next
  return next
}
function registerNotifications() {
  const revision = identityRevision
  return serial(() => registerCurrentNotifications(revision))
}
async function registerCurrentNotifications(revision: number) {
  await signOutCleanup.catch(() => {})
  if (signedOut || revision !== identityRevision) return
  const previous = read(ENDPOINT)
  const existing = await navigator.serviceWorker.getRegistration('/sw.js')
  const current = await existing?.pushManager.getSubscription()
  if (previous && previous !== current?.endpoint) {
    const removed = await apiFetch('/api/push/subscribe', { method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ endpoint: previous }) })
    if (!removed.ok) throw new Error('Could not remove old push subscription')
    try { localStorage.removeItem(ENDPOINT) } catch { /* storage unavailable */ }
  }
  const registration = await navigator.serviceWorker.register('/sw.js')
  const response = await apiFetch('/api/push/vapid-public-key')
  if (!response.ok) throw new Error('Could not load push configuration')
  const { publicKey } = await response.json()
  if (!publicKey || revision !== identityRevision) return
  const subscription = await registration.pushManager.getSubscription() ?? await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(publicKey).buffer as ArrayBuffer })
  if (revision !== identityRevision) { await subscription.unsubscribe(); return }
  remember(ENDPOINT, subscription.endpoint)
  const saved = await apiFetch('/api/push/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(subscription) })
  if (!saved.ok) throw new Error('Could not save push subscription')
  if (revision !== identityRevision) { await subscription.unsubscribe(); return }
  remember(WANTED, '1')
}

/** Only the bell's explicit Turn on action may request permission. */
export async function enableNotifications() {
  if (!pushSupported()) return
  const revision = identityRevision
  // A newly granted permission must not be mistaken for a legacy opt-in if saving fails.
  if (read(WANTED) === null) remember(WANTED, '0')
  const permission = Notification.permission === 'granted' ? 'granted' : await Notification.requestPermission()
  if (permission === 'granted' && revision === identityRevision) await registerNotifications()
}

/** Restore legacy opt-in and renew account/login ownership without a permission prompt. */
export async function restoreNotifications() {
  if (signedOut || !pushSupported() || Notification.permission !== 'granted') return
  if (!wanted()) {
    if (read(WANTED) !== null) return
    const revision = identityRevision
    const registration = await navigator.serviceWorker.getRegistration('/sw.js')
    const subscription = await registration?.pushManager.getSubscription()
    // Main-era Turn off removed the flag. Only an existing subscription proves
    // legacy opt-in; granted permission alone must never recreate it.
    if (!subscription || signedOut || revision !== identityRevision) return
    remember(WANTED, '1')
    remember(ENDPOINT, subscription.endpoint)
  }
  await registerNotifications()
}

// Core may report sign-out only after the login has already ended. Drop the
// browser subscription immediately. The server also verifies the saved login
// before delivery and deletes revoked rows even when this page is closed.
window.addEventListener('blerg-signed-out', () => {
  identityRevision++
  signedOut = true
  const token = getAccessToken()
  signOutCleanup = (async () => {
    const registration = pushSupported() ? await navigator.serviceWorker.getRegistration('/sw.js') : undefined
    const subscription = await registration?.pushManager.getSubscription()
    const endpoint = subscription?.endpoint ?? read(ENDPOINT)
    if (subscription) await subscription.unsubscribe()
    if (endpoint) {
      const response = await fetch('/api/push/subscribe', { method: 'DELETE', signal: AbortSignal.timeout(5000), headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) }, body: JSON.stringify({ endpoint }) })
      if (response.ok) { try { localStorage.removeItem(ENDPOINT) } catch { /* storage unavailable */ } }
    }
  })()
  void signOutCleanup.catch(console.error)
})

function loginChanged() {
  identityRevision++
  signedOut = false
  void restoreNotifications().catch(console.error)
}
window.addEventListener('blerg-identity-changed', loginChanged)
window.addEventListener('blerg-login-changed', loginChanged)

// The service worker has no bearer token. It asks a signed-in window to repair
// the subscription; a closed browser restores the saved opt-in on its next load.
if (typeof navigator !== 'undefined' && 'serviceWorker' in navigator) {
  navigator.serviceWorker.addEventListener('message', event => {
    if (event.data?.type === 'push_subscription_changed') void restoreNotifications().catch(console.error)
  })
}
