// Whether the runner's Settings dialog is open. The left bar's Settings entry (and the phone's tab bar)
// opens it over whatever page the person is on; the app draws it once. No route: they stay where they are.
import { useSyncExternalStore } from 'react'

let open = false
const listeners = new Set<() => void>()

export function setSettingsOpen(on: boolean) {
  if (open === on) return
  open = on
  listeners.forEach(l => l())
}

export function settingsIsOpen(): boolean {
  return open
}

export function useSettingsOpen(): boolean {
  return useSyncExternalStore(cb => { listeners.add(cb); return () => { listeners.delete(cb) } }, settingsIsOpen)
}
