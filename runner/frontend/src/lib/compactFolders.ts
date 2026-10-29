// Same account-preferences endpoint as code colors; the browser cache is account-scoped.
import { useEffect, useSyncExternalStore } from 'react'
import { apiFetch } from '../apiFetch'
import { signedInAccount } from './liveBoardCard'

const key = (account: string) => `blerg-runner.compact-folders:${account}`
const values = new Map<string, boolean>()
const revisions = new Map<string, number>()
const listeners = new Set<() => void>()
const notify = () => listeners.forEach(fn => fn())
const subscribe = (fn: () => void) => { listeners.add(fn); return () => { listeners.delete(fn) } }
export function getCompactFolders(): boolean {
  const account = signedInAccount()
  if (!values.has(account)) {
    let enabled = true
    try { if (account) enabled = localStorage.getItem(key(account)) !== 'false' } catch { /* private mode */ }
    values.set(account, enabled)
  }
  return values.get(account)!
}
function set(account: string, enabled: boolean) {
  values.set(account, enabled)
  try { if (account) localStorage.setItem(key(account), String(enabled)) } catch { /* private mode */ }
  notify()
}
export function useCompactFolders(): boolean {
  return useSyncExternalStore(subscribe, getCompactFolders, getCompactFolders)
}
export async function saveCompactFolders(enabled: boolean): Promise<string> {
  const account = signedInAccount(), previous = getCompactFolders()
  const revision = (revisions.get(account) ?? 0) + 1
  revisions.set(account, revision)
  set(account, enabled)
  try {
    const r = await apiFetch('/api/me/preferences', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ compact_folders: enabled }) })
    if (!r.ok) throw new Error('Could not save compact folders.')
    return ''
  } catch {
    if (revisions.get(account) === revision) set(account, previous)
    return 'Could not save compact folders. Try again.'
  }
}
export function useCompactFoldersSync(): void {
  useEffect(() => {
    let sequence = 0
    const sync = async () => {
      const request = ++sequence, account = signedInAccount(), revision = revisions.get(account)
      notify()
      try {
        const r = await apiFetch('/api/me/preferences')
        if (!r.ok) return
        const body = await r.json() as { compact_folders?: boolean }
        if (request === sequence && account === signedInAccount() && revision === revisions.get(account)) set(account, body.compact_folders !== false)
      } catch { /* Keep this account's cached preference when offline. */ }
    }
    void sync()
    window.addEventListener('blerg-identity-changed', sync)
    return () => { sequence++; window.removeEventListener('blerg-identity-changed', sync) }
  }, [])
}
