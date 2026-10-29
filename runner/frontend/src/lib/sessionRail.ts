// The session page's side panel (SessionRail): the person's open/closed choice for it and its tab,
// kept per browser, the chat settings its Live tab holds, and the words it uses for how long
// something has waited or run.
import { useSyncExternalStore } from 'react'
import { loadCompact, localStorageChatStorage, saveCompact, type BackgroundTask } from '@blerglab/chat'
import { childIsWorking, type LiveChildSession } from './liveState'

export const RAIL_PREF_KEY = 'blerg-runner.sessionRail.open'

/** The person's choice for the desktop panel (open or closed), or null when they never made one. */
export function readRailPref(): boolean | null {
  try {
    const v = localStorage.getItem(RAIL_PREF_KEY)
    return v === '1' ? true : v === '0' ? false : null
  } catch {
    return null
  }
}

export function writeRailPref(open: boolean) {
  try {
    localStorage.setItem(RAIL_PREF_KEY, open ? '1' : '0')
  } catch {
    // Private mode or a full store: the choice lasts for this page only.
  }
}

export const RAIL_TAB_KEY = 'blerg-runner.sessionRail.tab'

/** The panel's tabs, in order: what is live, the plan (tickets, checklist, diffs), the work done, the session's files, what it loaded. */
export const RAIL_TABS = ['live', 'plan', 'history', 'files', 'capabilities'] as const

export type RailTab = typeof RAIL_TABS[number]

/** The panel's tab the person last chose (Live until they choose, or for a value it does not know). */
export function readRailTab(): RailTab {
  try {
    const v = localStorage.getItem(RAIL_TAB_KEY)
    // Capabilities was called Skills: a choice saved under the old name still opens it.
    if (v === 'skills') return 'capabilities'
    return (RAIL_TABS as readonly string[]).includes(v ?? '') ? v as RailTab : 'live'
  } catch {
    return 'live'
  }
}

export function writeRailTab(tab: RailTab) {
  try {
    localStorage.setItem(RAIL_TAB_KEY, tab)
  } catch {
    // Private mode or a full store: the choice lasts for this page only.
  }
}

/** "waiting 4 min", "waiting 1 h 5 min", "asked just now". */
export function waitLabel(since: number | null, now: number): string {
  if (since === null) return ''
  const min = Math.floor(Math.max(0, now - since) / 60_000)
  if (min < 1) return 'asked just now'
  if (min < 60) return `waiting ${min} min`
  const h = Math.floor(min / 60)
  return `waiting ${h} h${min % 60 ? ` ${min % 60} min` : ''}`
}

/** "12 min", "3 h 4 min", "2 d 5 h". */
export function lifetimeLabel(ms: number): string {
  const min = Math.floor(Math.max(0, ms) / 60_000)
  if (min < 1) return 'under a minute'
  if (min < 60) return `${min} min`
  const h = Math.floor(min / 60)
  if (h < 48) return `${h} h${min % 60 ? ` ${min % 60} min` : ''}`
  return `${Math.floor(h / 24)} d${h % 24 ? ` ${h % 24} h` : ''}`
}

// ─── The chat settings the panel's Live tab holds (its Settings section) ──────────────────────

// Whether the model's thinking is shown: on unless the person turned it off (kept in this browser).
export const THINKING_KEY = 'blerg-runner.chat.thinking'

export function readThinking(): boolean {
  try {
    return localStorage.getItem(THINKING_KEY) !== '0'
  } catch {
    return true
  }
}

export function writeThinking(on: boolean) {
  try {
    localStorage.setItem(THINKING_KEY, on ? '1' : '0')
  } catch {
    // Private mode or a full store: the choice lasts for this page only.
  }
  announceChatSettings()
}

/** Compact tool calls: on unless the person turned them off, under the chat's own key (the choice
 *  made before the setting moved to the panel stands). */
export function readCompact(): boolean {
  return loadCompact(localStorageChatStorage)
}

export function writeCompact(on: boolean) {
  saveCompact(localStorageChatStorage, on)
  announceChatSettings()
}

// The Settings dialog and the open session page are different components: a change made in the dialog
// reaches the chat through this event (and another tab's, through the storage event).
const CHAT_SETTINGS_EVENT = 'blerg-runner:chat-settings'
function announceChatSettings() {
  window.dispatchEvent(new Event(CHAT_SETTINGS_EVENT))
}
function subscribeChatSettings(cb: () => void) {
  window.addEventListener(CHAT_SETTINGS_EVENT, cb)
  window.addEventListener('storage', cb)
  return () => {
    window.removeEventListener(CHAT_SETTINGS_EVENT, cb)
    window.removeEventListener('storage', cb)
  }
}

/** Show thinking, kept per browser and shared by everything that shows or sets it. */
export function useThinking(): [boolean, (on: boolean) => void] {
  return [useSyncExternalStore(subscribeChatSettings, readThinking), writeThinking]
}

/** Compact tool calls, likewise. */
export function useCompact(): [boolean, (on: boolean) => void] {
  return [useSyncExternalStore(subscribeChatSettings, readCompact), writeCompact]
}

/** What Running now lists (besides Main), counted once for the list and for the panel toggle's
 *  two counts so they always agree: the work inside the session (its subagents, and its background
 *  commands and tasks still running; a background subagent's own task is listed once, as the
 *  subagent) and its started sessions at work. `tasks` keeps the finished ones the list still shows. */
export function runningWork(o: {
  subagents: readonly { key: string }[]
  /** The server's running tasks; absent: the chat's own reading (bg.running). */
  serverTasks?: readonly BackgroundTask[] | null
  bg: { running: readonly BackgroundTask[]; recent: readonly BackgroundTask[] }
  children: readonly LiveChildSession[]
  archived: boolean
}): { tasks: BackgroundTask[]; runningChildren: LiveChildSession[]; inside: number; children: number } {
  const subKeys = new Set(o.subagents.map(s => s.key))
  const ended = new Set(o.bg.recent.map(t => t.taskId))
  const running = (o.serverTasks ?? o.bg.running).filter(t => !ended.has(t.taskId))
  const tasks = [...running, ...o.bg.recent].filter(t => !(t.kind === 'agent' && subKeys.has(t.callId)))
  const runningChildren = o.archived ? [] : o.children.filter(c => !c.archived_at && childIsWorking(c))
  return { tasks, runningChildren, inside: o.subagents.length + tasks.filter(t => !t.done).length, children: runningChildren.length }
}

/** The panel toggle's words for its counts: "3 subagents and background tasks · 4 child sessions". */
export function runningWorkLabel(inside: number, children: number): string {
  return [
    inside > 0 ? `${inside} ${inside === 1 ? 'subagent or background task' : 'subagents and background tasks'}` : '',
    children > 0 ? `${children} child ${children === 1 ? 'session' : 'sessions'}` : '',
  ].filter(Boolean).join(' · ')
}
