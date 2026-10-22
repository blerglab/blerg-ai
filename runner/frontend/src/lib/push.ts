// Push notifications to this browser (a phone's or a desktop's): the setting in the Notifications
// bell turns them on. Nothing here asks for permission on its own.
import { apiFetch } from '../apiFetch'

function urlBase64ToUint8Array(base64String: string): Uint8Array {
  const padding = '='.repeat((4 - base64String.length % 4) % 4)
  const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/')
  const rawData = window.atob(base64)
  return Uint8Array.from([...rawData].map(c => c.charCodeAt(0)))
}

const WANTED = 'blerg_notifications_wanted'

function wanted(): boolean {
  try { return localStorage.getItem(WANTED) === '1' } catch { return false }
}

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
  // A browser that granted permission but turned push off here (or never turned it on) is off.
  if (Notification.permission === 'granted' && wanted()) return 'on'
  return 'off'
}

/** Turns push off for this browser: the server forgets its subscription, the browser drops it, and
 *  the app no longer restores it on load. The browser's permission stays as the person set it. */
export async function disableNotifications() {
  try { localStorage.removeItem(WANTED) } catch { /* storage unavailable: nothing remembered */ }
  if (!pushSupported()) return
  const registration = await navigator.serviceWorker.getRegistration('/sw.js')
  const subscription = await registration?.pushManager.getSubscription()
  if (!subscription) return
  await apiFetch('/api/push/subscribe', { method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ endpoint: subscription.endpoint }) })
  await subscription.unsubscribe()
}

// enableNotifications is the ONLY place that ever calls Notification.requestPermission(): a
// user-initiated action from the bell's push setting, never from a mount effect. Browsers
// increasingly ignore (or actively penalize) permission prompts fired without a user gesture, and
// asking on every page load regardless of the visitor's prior answer is exactly that anti-pattern.
export async function enableNotifications() {
  if (!pushSupported()) return
  const permission = await Notification.requestPermission()
  if (permission !== 'granted') return
  try { localStorage.setItem(WANTED, '1') } catch { /* storage unavailable: the opt-in just isn't remembered */ }
  const registration = await navigator.serviceWorker.register('/sw.js')
  const { publicKey } = await (await apiFetch('/api/push/vapid-public-key')).json()
  if (!publicKey) return
  const subscription = await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(publicKey).buffer as ArrayBuffer })
  await apiFetch('/api/push/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(subscription) })
}
