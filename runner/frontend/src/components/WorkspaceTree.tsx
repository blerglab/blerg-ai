import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { useViewerHost, WorkspaceFileViewer, FileIcon } from '@blerglab/chat'
import { workspaceStatusReason } from '../lib/workspaceStatusReason'
import { apiFetch } from '../apiFetch'
import { blergTransport } from '../lib/chatTransport'
import { signedInAccount } from '../lib/liveBoardCard'
import { capPages, dropView, readView, viewEpoch, writeView } from '../lib/sessionViewCache'
import { RefreshScheduler, watchTurnEnds, type Readiness } from '../lib/explorerAutoRefresh'
import InitialLoader from './InitialLoader'
import './WorkspaceTree.css'
import { inputClock } from '../../../../packages/chat/src/model/clock'

import { useCompactFolders } from '../lib/compactFolders'
import { REVEAL_SESSION_FILE, completeSessionFileReveal, sessionFileRevealRequest, type SessionFileRequest } from '../lib/openSessionFile'
import { compactFolderChain, pathCodes, statusLabels, withDeletedEntries, workspaceChanges, type TreeEntry as Entry, type TreeStatus as Status } from '../lib/workspaceTree'
type Member = { id: string; name: string; path: string }
type Result = { ok: boolean; unavailable_members?: string[]; entries: Entry[]; cursor?: string; truncated: boolean; status?: Status; status_reason?: string; error_code?: string; launch_generation: string; generation: number }
type Reply = { result: Result; members: Member[]; runtime: string }
/** loaded: how many pages of the directory were read into entries (Load more); firstSize: how many
 *  of the entries the first page held, so a return can replace the first page and keep the rest. */
type Page = { entries: Entry[]; cursor?: string; truncated: boolean; loaded?: number; firstSize?: number }
type TreeRow = { head: Entry; chain: Entry[]; entry: Entry; children: TreeRow[] }
/** Replace the first listing page without throwing away pages the person loaded later. */
function refreshedPage(before: Page | undefined, first: Page): Page {
  const later = before && (before.loaded ?? 1) > 1 && before.firstSize !== undefined ? before.entries.slice(before.firstSize) : null
  const fresh = new Set(first.entries.map(entry => entry.path))
  return later
    ? { entries: [...first.entries, ...later.filter(entry => !fresh.has(entry.path))], cursor: before!.cursor, truncated: before!.truncated, loaded: before!.loaded, firstSize: first.entries.length }
    : { ...first, loaded: 1, firstSize: first.entries.length }
}
/** Expansion must stop at the same entries as the row, including paths retained by Git status. */
function chainContinuation(path: string, page: Page, members: Member[], repository: boolean, statuses: Record<string, Status | null>): Entry | undefined {
  const entries = withDeletedEntries(page.entries, path, workspaceChanges(statuses))
  return compactFolderChain({ name: path.split('/').at(-1) ?? '', path, directory: true, repository }, { [path]: { ...page, entries } }, new Set(members.map(member => member.path)), true)[1]
}
/** What the explorer showed for a session, kept in memory (sessionViewCache) so coming back to it
 *  draws at once; `binding` is the runtime it was read from (launch_generation:generation). */
export type ExplorerSnapshot = {
  binding: string
  pages: Record<string, Page>
  expanded: string[]
  statuses: Record<string, Status | null>
  statusErrors: Record<string, string>
  statusTimes: Record<string, number>
  members: Member[]
  runtime: string
  unavailable: string[]
  filter: string
  hits: Page | null
  focusRoot: string
  focusedPath: string
  selectedPath?: string
  revealedPath?: string
  revealVersion?: number
  scrollTop: number
}
export const EXPLORER_SLOT = 'explorer'
/** The reply came from another runtime than the pages on screen: they were discarded. */
class RuntimeChanged extends Error {}

/** The directories on screen, in tree order: `root`, then every open folder under an open folder
 *  (also one whose listing is not held, so it can be read again). */
function visibleDirectories(pages: Record<string, Page>, expanded: ReadonlySet<string>, root: string): string[] {
  const out: string[] = []
  const walk = (path: string) => {
    out.push(path)
    for (const e of pages[path]?.entries ?? []) if (e.directory && !e.deleted && expanded.has(e.path)) walk(e.path)
  }
  walk(root)
  return out
}

/** The nearest ancestor that scrolls (the explorer's body). */
function scroller(element: HTMLElement | null): HTMLElement | null {
  for (let node = element?.parentElement ?? null; node; node = node.parentElement) {
    const overflow = getComputedStyle(node).overflowY
    if (overflow === 'auto' || overflow === 'scroll') return node
  }
  return null
}

/** Backoff for busy explorer reads: about 8 seconds in all before the refusal is shown. */
const BUSY_RETRY_MS = [150, 300, 600, 1000, 1500, 2000, 2500]
/** A keystroke in the search this recent holds a refresh the explorer started by itself. */
const TYPING_HOLD_MS = 2000

/** Whether the element is drawn (no hidden or display:none ancestor). */
function shown(element: HTMLElement | null): boolean {
  if (!element?.isConnected) return false
  if (typeof element.checkVisibility === 'function') return element.checkVisibility()
  return !element.closest('[hidden]')
}

export default function WorkspaceTree({ sessionId, active, onFileOpen }: { sessionId: string; active: boolean; onFileOpen?: () => void }) {
  return active ? <WorkspaceTreeBody key={`${sessionId}:${signedInAccount()}`} sessionId={sessionId} onFileOpen={onFileOpen} /> : null
}

function WorkspaceTreeBody({ sessionId, onFileOpen }: { sessionId: string; onFileOpen?: () => void }) {
  const host = useViewerHost()
  const compactFolders = useCompactFolders()
  const account = useRef(signedInAccount())
  // What this explorer showed when the person last left this session: drawn at once, then read again.
  const [cached] = useState(() => readView<ExplorerSnapshot>(account.current, sessionId, EXPLORER_SLOT))
  // What this explorer keeps is kept only while the cache has not been cleared since it mounted
  // (another account, an identity or desktop scope change).
  const [epoch] = useState(viewEpoch)
  const [pages, setPages] = useState<Record<string, Page>>(() => cached?.pages ?? {})
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(cached?.expanded ?? ['']))
  const [statuses, setStatuses] = useState<Record<string, Status | null>>(() => cached?.statuses ?? {})
  const [statusErrors, setStatusErrors] = useState<Record<string, string>>(() => cached?.statusErrors ?? {})
  const [members, setMembers] = useState<Member[]>(() => cached?.members ?? [])
  const [runtime, setRuntime] = useState(() => cached?.runtime ?? '')
  const [unavailable, setUnavailable] = useState<string[]>(() => cached?.unavailable ?? [])
  const [filter, setFilter] = useState(() => cached?.filter ?? '')
  const [searchExpanded, setSearchExpanded] = useState(false)
  const [searchActive, setSearchActive] = useState<string | null>(null)
  const searchInput = useRef<HTMLInputElement>(null)
  const [hits, setHits] = useState<Page | null>(() => cached?.hits ?? null)
  const [focusRoot, setFocusRoot] = useState(() => cached?.focusRoot ?? '')
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [viewing, setViewing] = useState<string | null>(null)
  const viewerTrigger = useRef<HTMLElement | null>(null)
  const [focusedPath, setFocusedPath] = useState(() => cached?.focusedPath ?? '')
  const [selectedPath, setSelectedPath] = useState(() => cached?.selectedPath ?? cached?.focusedPath ?? '')
  const [revealRequest, setRevealRequest] = useState(() => sessionFileRevealRequest(account.current, sessionId))
  const [completedReveal, setCompletedReveal] = useState(() => ({
    version: cached?.revealVersion ?? (revealRequest?.handled ? revealRequest.version : 0),
    path: cached?.revealedPath ?? '',
  }))
  const revealAttempt = useRef('')
  const scrollReveal = useRef('')
  const compactNow = useRef(compactFolders)
  compactNow.current = compactFolders
  const ownsTreeFocus = useRef(false)
  const rowElements = useRef(new Map<string, HTMLButtonElement>())
  const treeId = useId()
  const controller = useRef<AbortController | null>(null)
  const statusTimes = useRef<Record<string, number>>({ ...cached?.statusTimes })
  const binding = useRef(cached?.binding ?? '')
  const scrollTop = useRef(cached?.scrollTop ?? 0)
  const section = useRef<HTMLElement>(null)
  // Cached listings not read again yet (folders that were closed): opening one reads it again.
  const stale = useRef(new Set(Object.keys(cached?.pages ?? {})))
  const filterNow = useRef(filter)
  filterNow.current = filter
  // What a refresh the explorer starts by itself waits for (card #270): explicit reads in flight
  // (expand, Load more, Refresh, the first read) and the person typing in search.
  const explicitReads = useRef(0)
  const lastKey = useRef(-Infinity)
  const revalidateError = useRef('')
  const autoRefresh = useRef<RefreshScheduler | null>(null)
  const operations = blergTransport().files

  async function request(op: string, path: string, signal: AbortSignal, cursor?: string) {
    const query = new URLSearchParams({ op, path })
    if (op === 'search') query.set('filter', filter)
    if (cursor) query.set('cursor', cursor)
    let response = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/workspace?${query}`, { signal })
    // The server runs one explorer read per person at a time and refuses the rest as busy (another
    // session's explorer, a file viewer, everything refetching after a reconnect). Wait and try
    // again quietly, as the file viewer does; only a lasting refusal reaches the person.
    for (const wait of BUSY_RETRY_MS) {
      if (response.status !== 409) break
      const problem = await response.clone().json().catch(() => null) as { code?: string } | null
      if (problem?.code !== 'busy') break
      await new Promise<void>((resolve, reject) => {
        const timer = window.setTimeout(resolve, wait)
        signal.addEventListener('abort', () => { window.clearTimeout(timer); reject(new DOMException('Cancelled', 'AbortError')) }, { once: true })
      })
      response = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/workspace?${query}`, { signal })
    }
    if (!response.ok) {
      const problem = await response.json().catch(() => null) as { error?: string } | null
      throw new Error(problem?.error || (response.status === 403 ? 'Only the session owner or an authorized administrator can browse this workspace.' : response.status === 409 ? 'The workspace is not ready. Check that the session is running and its daemon is connected, then Refresh.' : 'Could not read the workspace.'))
    }
    const reply = await response.json() as Reply
    if (signedInAccount() !== account.current) { setPages({}); setMembers([]); setStatuses({}); setHits(null); setViewing(null); throw new Error('Your sign-in changed. Refresh the workspace.') }
    if (signal.aborted) throw new DOMException('Cancelled', 'AbortError')
    const next = `${reply.result.launch_generation}:${reply.result.generation}`
    if (binding.current && binding.current !== next) {
      setPages({}); setStatuses({}); setStatusErrors({}); setHits(null); setMembers([])
      binding.current = next
      dropView(sessionId, EXPLORER_SLOT)
      throw new RuntimeChanged('The runtime changed. Refresh to browse its workspace.')
    }
    binding.current = next
    if (!reply.result.ok) {
      const reasons: Record<string, string> = {
        stale: 'The directory changed. Refresh to load it again.',
        busy: 'Another workspace read is in progress. Try Refresh when it finishes.',
        unavailable: 'The workspace reader lost its session or directory binding. Refresh to reconnect it.',
        forbidden: 'This file or directory cannot be opened from the session workspace.',
      }
      throw new Error((op === 'status' ? workspaceStatusReason(reply.result.status_reason) : undefined) || reasons[reply.result.error_code ?? ''] || 'The workspace could not be safely read.')
    }
    setMembers(reply.members); setRuntime(reply.runtime)
    if (reply.result.unavailable_members) setUnavailable(reply.result.unavailable_members)
    return reply
  }

  async function load(path: string, cursor?: string, op = 'list', refresh = false, followChain = false) {
    controller.current?.abort()
    const ac = new AbortController(); controller.current = ac
    setBusy(true); setRefreshing(refresh); setMessage('')
    explicitReads.current++
    const knownStatuses = { ...statuses }
    try {
      while (true) {
        const reply = await request(op, path, ac.signal, cursor)
        const result = reply.result
        const first = { entries: result.entries, cursor: result.cursor, truncated: result.truncated }
        const page: Page = op === 'list' && !cursor && stale.current.has(path) ? refreshedPage(pages[path], first) : first
        if (op === 'status') { knownStatuses[path] = result.status ?? null; setStatusErrors(old => ({ ...old, [path]: '' })); setStatuses(old => ({ ...old, [path]: result.status ?? null })); statusTimes.current[path] = inputClock.now() }
        else if (op === 'search') setHits(page)
        else {
          // A loop iteration owns its path even after the next single-child listing starts.
          const directory = path
          setPages(old => ({ ...old, [directory]: { ...page, entries: cursor ? [...(old[directory]?.entries ?? []), ...page.entries] : page.entries, loaded: cursor ? (old[directory]?.loaded ?? 1) + 1 : (page.loaded ?? 1), firstSize: cursor ? old[directory]?.firstSize : (page.firstSize ?? page.entries.length) } }))
          if (!cursor) stale.current.delete(path)
          const member = reply.members.find(m => m.path === (path || '.'))
          const discovered = pagesPathRepository(path)
          if (member || discovered) {
            try {
              const status = await request('status', path, ac.signal)
              knownStatuses[path] = status.result.status ?? null
              setStatusErrors(old => ({ ...old, [directory]: '' })); setStatuses(old => ({ ...old, [directory]: status.result.status ?? null })); statusTimes.current[path] = inputClock.now()
            } catch (error) {
              if (ac.signal.aborted) throw error
              knownStatuses[path] = null
              setStatusErrors(old => ({ ...old, [directory]: error instanceof Error ? error.message : 'Could not read Git status.' })); setStatuses(old => ({ ...old, [directory]: null })); statusTimes.current[path] = inputClock.now()
            }
          }
        }
        if (refresh) setMessage('Workspace refreshed.')
        if (op === 'list' && path === '') window.dispatchEvent(new CustomEvent('blerg-session-workspace-ready', { detail: sessionId }))
        if (!followChain || !compactNow.current || op !== 'list' || cursor) break
        // Only explicit expansion follows new single-folder listings. It stops at a branch,
        // unreadable/partial page or repository boundary; siblings are never prefetched.
        const next = chainContinuation(path, page, reply.members, pagesPathRepository(path), knownStatuses)
        if (!next) break
        setExpanded(old => new Set([...old, next.path]))
        path = next.path
      }
    } catch (error) {
      if (!ac.signal.aborted && op === 'status') { setStatusErrors(old => ({ ...old, [path]: error instanceof Error ? error.message : 'Could not read Git status.' })); setStatuses(old => ({ ...old, [path]: null })); statusTimes.current[path] = inputClock.now() }
      if (!ac.signal.aborted) setMessage(error instanceof Error ? error.message : 'Could not read the workspace.')
    } finally {
      explicitReads.current--
      if (!ac.signal.aborted) { setBusy(false); setRefreshing(false) }
    }
  }
  function pagesPathRepository(path: string) { return hits?.entries.some(e => e.path === path && e.repository) || Object.values(pages).some(p => p.entries.some(e => e.path === path && e.repository)) }

  /** A cached view on screen: read again what is visible (the first page of each listing from `root`
   *  down through the open folders, each repository's status and, with `search`, the search's
   *  matches), one read at a time, replacing each in place. Later pages a listing held stay as
   *  cached until the person loads more; an open folder whose listing was not kept is read (its
   *  first page) and stays open. No folder is closed. Nothing says Loading: a failure keeps the cached view and
   *  says why; a reply from another runtime discards it all and reads afresh. Anything the person
   *  starts (expand, Load more, Refresh) cancels it. A success clears the failure an earlier one
   *  showed. `announce`: say the workspace is ready once the root is read (not on a refresh the
   *  explorer started by itself: the workspace was ready already). Returns how it ended. */
  async function revalidate(root: string, search: boolean, announce = true, opening?: ReadonlySet<string>): Promise<'done' | 'aborted' | 'failed' | 'reset'> {
    controller.current?.abort()
    const ac = new AbortController(); controller.current = ac
    const known: Record<string, Page> = { ...pages }
    const knownStatuses = { ...statuses }
    const open = new Set(opening ?? expanded)
    let chainPath: string | undefined = opening ? root : undefined
    if (opening) explicitReads.current++
    const repository = (path: string, list: Member[]) => list.some(m => m.path === (path || '.')) || (hits?.entries ?? []).concat(...Object.values(known).map(p => p.entries)).some(e => e.path === path && e.repository)
    try {
      const visit = [root]
      while (visit.length) {
        const path = visit.shift()!
        const reply = await request('list', path, ac.signal)
        const first = reply.result.entries
        // Refresh and explicit reopening share the same retained-page semantics.
        const page = refreshedPage(known[path], { entries: first, cursor: reply.result.cursor, truncated: reply.result.truncated })
        const entries = page.entries
        known[path] = page
        stale.current.delete(path)
        setPages(old => ({ ...old, [path]: page }))
        if (path === '' && announce) window.dispatchEvent(new CustomEvent('blerg-session-workspace-ready', { detail: sessionId }))
        if (repository(path, reply.members)) {
          try {
            const status = await request('status', path, ac.signal)
            knownStatuses[path] = status.result.status ?? null
            setStatusErrors(old => ({ ...old, [path]: '' })); setStatuses(old => ({ ...old, [path]: status.result.status ?? null }))
          } catch (error) {
            if (ac.signal.aborted || error instanceof RuntimeChanged) throw error
            knownStatuses[path] = null
            setStatusErrors(old => ({ ...old, [path]: error instanceof Error ? error.message : 'Could not read Git status.' })); setStatuses(old => ({ ...old, [path]: null }))
          }
          statusTimes.current[path] = inputClock.now()
        }
        // An explicit reopen first refreshes its cached listings and all open descendants. Only
        // that row's single-child chain may open new folders as those refreshed listings arrive.
        if (path === chainPath) {
          const child = compactNow.current ? chainContinuation(path, page, reply.members, repository(path, reply.members), knownStatuses) : undefined
          chainPath = child?.path
          if (child) { open.add(child.path); setExpanded(old => new Set([...old, child.path])) }
        }
        // Depth first, in the tree's order: the open folders this listing still has, also one whose
        // listing was not kept (it is read again, one page).
        visit.unshift(...entries.filter(e => e.directory && !e.deleted && open.has(e.path)).map(e => e.path))
      }
      if (search && hits && filter.trim() && filterNow.current === filter) {
        const reply = await request('search', '', ac.signal)
        if (filterNow.current === filter) setHits({ entries: reply.result.entries, cursor: reply.result.cursor, truncated: reply.result.truncated })
      }
      const failed = revalidateError.current
      if (failed) { revalidateError.current = ''; setMessage(m => m === failed ? '' : m) }
      return 'done'
    } catch (error) {
      if (ac.signal.aborted) return 'aborted'
      if (error instanceof RuntimeChanged) {
        // The cached pages belong to the runtime before: none of them is kept or mixed in.
        setExpanded(new Set([''])); setFocusRoot(''); setFocusedPath(''); revealAttempt.current = ''; statusTimes.current = {}; stale.current.clear(); scrollTop.current = 0
        void load('')
        return 'reset'
      }
      const text = error instanceof Error ? error.message : 'Could not read the workspace.'
      revalidateError.current = text
      setMessage(text)
      return 'failed'
    } finally {
      if (opening) explicitReads.current--
    }
  }
  // The latest render's revalidation and view (a timer started renders ago reads what is on screen now).
  const quiet = useRef({ revalidate, focusRoot, hasRoot: false })
  quiet.current = { revalidate, focusRoot, hasRoot: !!pages[''] && !!binding.current }

  useEffect(() => {
    if (cached) void revalidate(focusRoot, true)
    else void load('')
    return () => { controller.current?.abort() }
    // The component is remounted on refresh, session change or tab activation.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Card #270: read again by itself, quietly (revalidate), when the agent's turn ends and when the
  // page comes back (window focus, the tab visible again: the person's own edits). Never a poll. It
  // waits while the person is typing in search or an explicit read is in flight; with the tab or the
  // explorer hidden it only marks the view stale and reads when it is shown again. A refresh that
  // still ends busy (after the request's own busy retries) leaves the view stale until the next
  // trigger (also one that came during it) or the explorer being shown again, which tries it once.
  // Refresh supersedes it: one it cancelled is not asked for again.
  useEffect(() => {
    const ready = (): Readiness => {
      if (document.visibilityState === 'hidden' || !shown(section.current)) return 'hidden'
      if (explicitReads.current > 0) return 'later'
      // Nothing read to read again (the first read failed): stale until the next trigger.
      if (!quiet.current.hasRoot) return 'hidden'
      if (document.activeElement === searchInput.current && inputClock.now() - lastKey.current < TYPING_HOLD_MS) return 'later'
      return true
    }
    const scheduler = new RefreshScheduler(async () => {
      const outcome = await quiet.current.revalidate(quiet.current.focusRoot, true, false)
      return outcome === 'aborted' ? 'cancelled' : outcome === 'failed' ? 'failed' : 'done'
    }, ready)
    autoRefresh.current = scheduler
    const stopWatching = watchTurnEnds(sessionId, () => scheduler.request())
    const back = () => { if (document.visibilityState === 'visible') scheduler.request() }
    window.addEventListener('focus', back)
    document.addEventListener('visibilitychange', back)
    // The explorer coming on screen again (a hidden pane shown), not any other resize: a refresh it
    // was holding runs, and one that failed is tried once more.
    let onScreen = shown(section.current)
    const sized = typeof ResizeObserver === 'undefined' || !section.current ? null : new ResizeObserver(() => {
      const now = shown(section.current)
      if (now && !onScreen) scheduler.wake()
      onScreen = now
    })
    if (sized && section.current) sized.observe(section.current)
    return () => {
      scheduler.stop(); autoRefresh.current = null
      stopWatching()
      sized?.disconnect()
      window.removeEventListener('focus', back)
      document.removeEventListener('visibilitychange', back)
    }
  }, [sessionId])

  // Keep what is on screen for the next visit (only once the root has been read from a runtime).
  useEffect(() => {
    if (signedInAccount() !== account.current) return
    if (!pages[''] || !binding.current) { dropView(sessionId, EXPLORER_SLOT); return }
    const kept = capPages(pages, visibleDirectories(pages, expanded, focusRoot))
    // Status of the directories still listed (a repository row, the root, where the tree is focused).
    const listed = new Set(['', focusRoot, ...Object.values(kept).flatMap(p => p.entries.map(e => e.path))])
    const only = <T,>(map: Record<string, T>) => Object.fromEntries(Object.entries(map).filter(([key]) => listed.has(key)))
    writeView<ExplorerSnapshot>(account.current, sessionId, EXPLORER_SLOT, {
      binding: binding.current, pages: kept, expanded: [...expanded], statuses: only(statuses), statusErrors: only(statusErrors),
      statusTimes: only(statusTimes.current), members, runtime, unavailable, filter, hits, focusRoot, focusedPath, selectedPath, revealedPath: completedReveal.path, revealVersion: completedReveal.version, scrollTop: scrollTop.current,
    }, epoch)
  }, [sessionId, epoch, pages, expanded, statuses, statusErrors, members, runtime, unavailable, filter, hits, focusRoot, focusedPath, selectedPath, completedReveal])
  // The scroll position: restored before the first paint, followed while here, kept on leaving.
  useLayoutEffect(() => {
    const body = scroller(section.current)
    const viewer = account.current
    if (!body) return
    if (scrollTop.current) body.scrollTop = scrollTop.current
    const follow = () => { scrollTop.current = body.scrollTop }
    body.addEventListener('scroll', follow, { passive: true })
    return () => {
      body.removeEventListener('scroll', follow)
      if (signedInAccount() !== viewer) return
      const last = readView<ExplorerSnapshot>(viewer, sessionId, EXPLORER_SLOT)
      if (last) writeView<ExplorerSnapshot>(viewer, sessionId, EXPLORER_SLOT, { ...last, scrollTop: scrollTop.current }, epoch)
    }
  }, [sessionId, epoch])

  function enterDirectory(entry: Entry, opening: ReadonlySet<string> = expanded) {
    if (entry.deleted) { setPages(old => ({ ...old, [entry.path]: { entries: [], truncated: false } })); return }
    if (!pages[entry.path]) void load(entry.path, undefined, 'list', false, true)
    else if (stale.current.has(entry.path)) void revalidate(entry.path, false, true, opening)
    else if (entry.repository && (statusTimes.current[entry.path] === undefined || inputClock.now() - statusTimes.current[entry.path] >= 5000)) void load(entry.path, undefined, 'status')
  }

  function toggle(entry: Entry, chain: Entry[] = [entry]) {
    if (!entry.directory && host) { host.open({ kind: 'workspace', path: entry.path }); onFileOpen?.(); return }
    if (!entry.directory) { viewerTrigger.current = document.activeElement as HTMLElement; setViewing(entry.path); return }
    if (hits) { setHits(null); setFilter(''); setFocusRoot(entry.path); enterDirectory(entry); return }
    const next = new Set(expanded)
    if (chain.every(segment => next.has(segment.path))) { chain.forEach(segment => next.delete(segment.path)); controller.current?.abort(); setBusy(false) }
    else { chain.forEach(segment => next.add(segment.path)); enterDirectory(chain.find(segment => stale.current.has(segment.path)) ?? entry, next) }
    setExpanded(next)
  }
  const badge = (key: string) => {
    const status = statuses[key]
    if (status === undefined) return null
    if (status === null) return <span className="workspace-status">status unavailable<span> · {statusErrors[key] || 'The daemon returned no Git status details. Update the daemon, then Refresh.'}</span></span>
    const detached = !status.branch || status.branch === '(detached)' || status.branch === 'HEAD'
    const name = detached ? status.head?.slice(0, 7) || 'detached' : status.branch
    const tooltip = detached ? status.head ? `Detached at ${name}` : 'Detached HEAD' : `Branch ${name}`
    return <span className="workspace-status" title={tooltip}><span aria-hidden="true">⎇ </span>{name} · {status.changed + status.untracked} changed{status.partial ? ' (partial)' : ''}</span>
  }
  const label = (entry: Entry) => members.find(m => m.path === entry.path || m.id === entry.member_id)?.name ?? entry.name
  const displayPath = (path: string) => {
    const member = members.reduce<Member | undefined>((nearest, m) => m.path !== '.' && (path === m.path || path.startsWith(`${m.path}/`)) && (!nearest || m.path.length > nearest.path.length) ? m : nearest, undefined)
    return member ? member.name + path.slice(member.path.length) : path
  }
  function treeKey(event: KeyboardEvent<HTMLUListElement>) {
    const item = (event.target as HTMLElement).closest<HTMLButtonElement>('[role="treeitem"]')
    if (!item) return
    const items = [...event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="treeitem"]')]
    const index = items.indexOf(item)
    let next: HTMLButtonElement | undefined
    if (event.key === 'ArrowDown') next = items[Math.min(items.length - 1, index + 1)]
    else if (event.key === 'ArrowUp') next = items[Math.max(0, index - 1)]
    else if (event.key === 'Home') next = items[0]
    else if (event.key === 'End') next = items.at(-1)
    else if (event.key === 'ArrowRight') {
      if (item.getAttribute('aria-expanded') === 'false') item.click()
      else if (item.getAttribute('aria-expanded') === 'true') next = item.parentElement?.querySelector<HTMLButtonElement>(':scope > [role="group"] > li > [role="treeitem"]') ?? undefined
    } else if (event.key === 'ArrowLeft') {
      if (item.getAttribute('aria-expanded') === 'true') item.click()
      else next = item.parentElement?.parentElement?.closest('li')?.querySelector<HTMLButtonElement>('[role="treeitem"]') ?? undefined
    } else return
    event.preventDefault()
    if (next) {
      items.forEach(row => { row.tabIndex = row === next ? 0 : -1 })
      next.focus()
    }
  }
  const changes = useMemo(() => workspaceChanges(statuses), [statuses])
  const chainPages = useMemo(() => Object.fromEntries(Object.entries(pages).map(([path, page]) => [path, { ...page, entries: withDeletedEntries(page.entries, path, changes) }])), [pages, changes])
  const entriesAt = (directory: string) => chainPages[directory]?.entries ?? []
  const memberRoots = useMemo(() => new Set(members.map(m => m.path)), [members])
  // Build each row's chain once. Selection, focus, filters and expansion reuse the loaded model.
  const treeRows = useMemo(() => {
    const build = (entries: Entry[]): TreeRow[] => entries.map(head => {
      const chain = compactFolderChain(head, chainPages, memberRoots, compactFolders && !hits)
      const entry = chain.at(-1)!
      return { head, chain, entry, children: entry.directory ? build(chainPages[entry.path]?.entries ?? []) : [] }
    })
    return build(hits?.entries ?? chainPages[focusRoot]?.entries ?? [])
  }, [chainPages, memberRoots, compactFolders, hits, focusRoot])
  function findRow(rows: TreeRow[], path: string): TreeRow | undefined {
    for (const row of rows) {
      if (row.chain.some(segment => segment.path === path)) return row
      const child = findRow(row.children, path)
      if (child) return child
    }
  }
  function matches(entry: Entry): boolean {
    return !filter || label(entry).toLocaleLowerCase().includes(filter.toLocaleLowerCase()) || (entry.directory && entriesAt(entry.path).some(matches))
  }
  const rowOpen = (row: TreeRow) => row.entry.directory && row.chain.every(segment => expanded.has(segment.path))
  const visiblePaths: string[] = []
  function collectVisible(rows: TreeRow[]) {
    for (const row of rows) {
      if (!matches(row.head)) continue
      visiblePaths.push(...row.chain.map(segment => segment.path))
      if (rowOpen(row)) collectVisible(row.children)
    }
  }
  collectVisible(treeRows)
  const tabPath = visiblePaths.includes(focusedPath) ? focusedPath : visiblePaths[0]
  const allLoaded = useMemo(() => {
    const out: Entry[] = []
    const collect = (directory: string) => {
      for (const entry of chainPages[directory]?.entries ?? []) {
        out.push(entry)
        if (entry.directory) collect(entry.path)
      }
    }
    collect(focusRoot)
    return out
  }, [chainPages, focusRoot])
  // Focus can outlive its keyed DOM node when a listing merges/splits compact rows. Track actual
  // tree ownership separately from the logical path, and never take focus back from another control.
  useEffect(() => {
    const outside = (event: Event) => {
      const target = event.target as Element | null
      if (!target?.closest?.('[role="tree"]') || !section.current?.contains(target)) ownsTreeFocus.current = false
    }
    document.addEventListener('focusin', outside)
    document.addEventListener('pointerdown', outside)
    return () => { document.removeEventListener('focusin', outside); document.removeEventListener('pointerdown', outside) }
  }, [])
  useLayoutEffect(() => {
    if (!ownsTreeFocus.current || searchExpanded || !shown(section.current)) return
    const row = rowElements.current.get(tabPath)
    if (row?.isConnected && document.activeElement !== row) row.focus()
  }, [treeRows, expanded, filter, searchExpanded, focusedPath, tabPath])
  // A tab activation is not a reveal. The request keeps its own path and version, including
  // requests received while the explorer was hidden; completion is state kept in the snapshot.
  useEffect(() => {
    const reveal = (event: Event) => {
      const request = (event as CustomEvent<SessionFileRequest>).detail
      if (request?.session !== sessionId || request.account !== account.current) return
      revealAttempt.current = ''
      setRevealRequest(sessionFileRevealRequest(account.current, sessionId))
    }
    window.addEventListener(REVEAL_SESSION_FILE, reveal)
    return () => window.removeEventListener(REVEAL_SESSION_FILE, reveal)
  }, [sessionId])
  useEffect(() => {
    if (!revealRequest || revealRequest.version === completedReveal.version || !pages[''] || busy) return
    const { path, version } = revealRequest
    const parts = path.split('/')
    const parents = parts.slice(0, -1).map((_, i) => parts.slice(0, i + 1).join('/'))
    // Keep each view constraint that still lets the requested row be shown.
    if (hits && !hits.entries.some(entry => entry.path === path || entry.directory && path.startsWith(`${entry.path}/`))) { setHits(null); return }
    if (!hits && focusRoot && !path.startsWith(`${focusRoot}/`)) { setFocusRoot(''); return }
    setSearchExpanded(false)
    const directHit = hits?.entries.some(entry => entry.path === path)
    const missing = !directHit && parents.find(parent => !pages[parent])
    if (missing) {
      const attempt = `${version}:${missing}`
      if (revealAttempt.current !== attempt) { revealAttempt.current = attempt; void load(missing) }
      return
    }
    const targetRow = findRow(treeRows, path)
    if (filter && targetRow && !matches(targetRow.head)) { setFilter(''); return }
    setSelectedPath(path); setFocusedPath(path)
    setExpanded(old => new Set([...old, ...parents]))
    setCompletedReveal({ path, version })
    completeSessionFileReveal(account.current, sessionId, version)
    scrollReveal.current = path
    // load and matches read this render's listings; arrivals advance this one request.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [revealRequest, completedReveal.version, pages, busy, hits, focusRoot, filter, treeRows])
  useLayoutEffect(() => {
    const path = scrollReveal.current
    if (!path) return
    const row = rowElements.current.get(path)
    if (row?.isConnected) { row.scrollIntoView?.({ block: 'nearest' }); scrollReveal.current = '' }
  }, [completedReveal, expanded, pages])

  const searchEntries = (hits?.entries ?? allLoaded).filter(entry => label(entry).toLocaleLowerCase().includes(filter.toLocaleLowerCase()))
  const activeResult = searchEntries.find(entry => entry.path === searchActive)
  const resultId = (entry: Entry) => `${treeId}-result-${encodeURIComponent(entry.path)}`
  const listboxId = `${treeId}-search-results`
  const activeId = searchExpanded && activeResult ? resultId(activeResult) : undefined
  useEffect(() => { if (activeId) document.getElementById(activeId)?.scrollIntoView?.({ block: 'nearest' }) }, [activeId])
  const announcedLabel = (entry: Entry, name = label(entry)) => [name, ...pathCodes(entry, changes).map(code => `${statusLabels[code].toLowerCase()}${entry.directory ? ' in this folder' : ''}`)].join(', ')
  function searchAll() {
    setSearchExpanded(true); setSearchActive(null); searchInput.current?.focus()
    void load('', undefined, 'search')
  }
  function chooseResult(entry: Entry) {
    setSearchExpanded(false); setSearchActive(null)
    if (entry.directory) {
      setHits(null); setFilter(''); setFocusRoot(entry.path); enterDirectory(entry)
    } else toggle(entry)
  }
  function searchKey(event: KeyboardEvent<HTMLInputElement>) {
    if (event.nativeEvent.isComposing) return
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault(); setSearchExpanded(true)
      const current = searchEntries.findIndex(entry => entry.path === searchActive)
      const next = event.key === 'ArrowDown' ? Math.min(searchEntries.length - 1, current + 1) : current < 0 ? searchEntries.length - 1 : Math.max(0, current - 1)
      setSearchActive(searchEntries[next]?.path ?? null)
    } else if (event.key === 'Enter') {
      if (searchExpanded && activeResult) { event.preventDefault(); chooseResult(activeResult) }
      else if (filter.trim() && !busy) { event.preventDefault(); searchAll() }
    } else if (event.key === 'Escape' && searchExpanded) {
      event.preventDefault(); event.stopPropagation(); setSearchExpanded(false); setSearchActive(null)
    }
  }
  function rows(models: TreeRow[], depth: number): React.ReactNode {
    return models.filter(row => matches(row.head)).map(row => {
      const { head, chain, entry } = row
      const compact = chain.length > 1
      const open = rowOpen(row)
      const selected = chain.some(segment => segment.path === selectedPath)
      const activate = () => {
        setSelectedPath(entry.path); setFocusedPath(entry.path)
        // Intermediate segments remain expanded so the plain view and cached traversal agree.
        toggle(entry, chain)
      }
      return <li role="none" key={head.path}>
        <button ref={node => {
          if (!node) return
          chain.forEach(segment => rowElements.current.set(segment.path, node))
          return () => chain.forEach(segment => { if (rowElements.current.get(segment.path) === node) rowElements.current.delete(segment.path) })
        }} type="button" role="treeitem" aria-label={announcedLabel(entry, compact ? displayPath(entry.path) : label(entry))} aria-level={depth + 1} aria-selected={selected} data-selected-path={selected ? selectedPath : undefined} tabIndex={chain.some(segment => segment.path === tabPath) ? 0 : -1} onFocus={() => { ownsTreeFocus.current = true; setFocusedPath(chain.some(segment => segment.path === focusedPath) ? focusedPath : selected ? selectedPath : entry.path) }} aria-owns={open ? `${treeId}-${encodeURIComponent(entry.path)}` : undefined} className="workspace-row" style={{ paddingInlineStart: '6px' }} onClick={activate} onDoubleClick={() => { if (!entry.directory) host?.open({ kind: 'workspace', path: entry.path }, true) }} aria-expanded={entry.directory ? open : undefined}>
          <span className="workspace-chevron" aria-hidden="true">{entry.directory ? open ? '▾' : '▸' : ''}</span>
          <FileIcon name={entry.name} directory={entry.directory} expanded={open} />
          {compact ? <span className="workspace-name workspace-compact-name" title={entry.path}><span className="workspace-segments">{chain.map((segment, index) => <span key={segment.path}>
            {index > 0 && <span className="workspace-separator" aria-hidden="true"> / </span>}
            <span className="workspace-segment" title={segment.path} data-path={segment.path} onClick={event => {
              event.stopPropagation(); setSelectedPath(segment.path); setFocusedPath(segment.path)
            }}>{label(segment)}</span>
          </span>)}</span></span> : <span className="workspace-name" title={label(entry)}>{label(entry)}</span>}
          <span className="workspace-codes">{pathCodes(entry, changes).map(code => <span key={code} className={`workspace-code workspace-code-${code}`} title={`${statusLabels[code]}${entry.directory ? ' in this folder' : ''}`} aria-label={statusLabels[code]}>{code}</span>)}</span>
        </button>
        {badge(entry.path)}
        {open && <ul role="group" style={{ borderInlineStart: depth < 4 ? '1px solid var(--stone)' : '0', marginInlineStart: depth < 4 ? 16 : 0 }} id={`${treeId}-${encodeURIComponent(entry.path)}`}>{pages[entry.path] ? rows(row.children, depth + 1) : <li className="workspace-note">{busy ? 'Loading…' : 'Expand again to retry.'}</li>}{pageEnd(entry.path)}</ul>}
      </li>
    })
  }
  function pageEnd(path: string) {
    const page = pages[path]
    if (!page?.truncated) return null
    return <li className="workspace-note">{page.cursor ? <button type="button" className="btn small" disabled={busy} onClick={() => void load(path, page.cursor)}>Load more</button> : 'Partial listing: this directory reached the read limit.'}</li>
  }
  return <section ref={section} className="workspace-tree" aria-label="Workspace">
    <div className="workspace-tools">
      <input ref={searchInput} role="combobox" aria-label="Search workspace files" aria-autocomplete="list" aria-haspopup="listbox" aria-controls={listboxId} aria-expanded={searchExpanded} aria-activedescendant={activeId} placeholder="Search files…" value={filter} onChange={e => { lastKey.current = inputClock.now(); setFilter(e.target.value); setHits(null); setSearchActive(null); setSearchExpanded(!!e.target.value.trim()) }} onBlur={() => { setSearchExpanded(false); setSearchActive(null) }} onKeyDown={searchKey} maxLength={256} />
      <button type="button" aria-disabled={busy} onClick={() => {
        if (busy) return
        autoRefresh.current?.satisfied()
        setPages({}); setStatuses({}); setStatusErrors({}); setSearchExpanded(false); setSearchActive(null); setExpanded(new Set([''])); setHits(null); setFocusRoot(''); setFocusedPath(''); revealAttempt.current = ''; statusTimes.current = {}; stale.current.clear(); binding.current = ''
        void load('', undefined, 'list', true)
      }} aria-label="Refresh workspace" title="Refresh workspace"><svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.3" aria-hidden="true"><path d="M13 6a5 5 0 1 0 .1 4M13 2v4H9" /></svg></button>
    </div>
    {filter.trim() && <button type="button" className="workspace-search-all" disabled={busy} onClick={searchAll}>Search all files <span aria-hidden="true">↵</span></button>}
    {runtime === 'sandbox' && <p className="workspace-note">Only selected repositories were copied.</p>}
    {members.filter(m => unavailable.includes(m.id)).map(m => <p className="workspace-note" key={m.id}>{m.name} · unavailable</p>)}
    {members.filter(m => m.path === '.').map(m => <p className="workspace-note" key={m.id}>{m.name} {badge('')}</p>)}
    {focusRoot && <p className="workspace-note"><button type="button" className="btn small" onClick={() => { controller.current?.abort(); setBusy(false); setFocusRoot(''); setHits(null) }}>Workspace</button> / {focusRoot} {badge(focusRoot)}</p>}
    {message && <p role="status" className="workspace-note">{message}</p>}
    {hits && <p className="workspace-note">Name matches{hits.truncated ? ' (partial)' : ''} <button type="button" className="btn small" onClick={() => { setHits(null); setSearchActive(null); setSearchExpanded(false); searchInput.current?.focus() }}>Back to tree</button></p>}
    <ul id={listboxId} role="listbox" aria-label="Workspace search results" hidden={!searchExpanded} aria-busy={busy}>
      {searchExpanded && searchEntries.map(entry => <li role="none" key={entry.path}>
        <button id={resultId(entry)} type="button" role="option" tabIndex={-1} aria-selected={entry.path === activeResult?.path} aria-label={announcedLabel(entry, entry.path)} className="workspace-row" onMouseDown={e => e.preventDefault()} onClick={() => chooseResult(entry)}>
          <FileIcon name={entry.name} directory={entry.directory} />
          <span className="workspace-name" title={entry.path}>{entry.path}</span>
          <span className="workspace-codes">{pathCodes(entry, changes).map(code => <span key={code} className={`workspace-code workspace-code-${code}`} title={statusLabels[code]}>{code}</span>)}</span>
        </button>
      </li>)}
    </ul>
    {!searchExpanded && <ul role="tree" aria-label="Workspace files" onKeyDown={treeKey}>{rows(treeRows, 0)}{!hits && pageEnd(focusRoot)}</ul>}
    {searchExpanded && !busy && searchEntries.length === 0 && <p className="workspace-note">No loaded matches. Search all files to look across the workspace.</p>}
    {busy && !pages[''] && <InitialLoader loading label="workspace" />}
    {busy && (pages[''] || refreshing) && <p className="workspace-note" role="status">{refreshing ? 'Refreshing workspace…' : 'Loading…'}</p>}
    {!busy && pages[focusRoot] && entriesAt(focusRoot).length === 0 && <p className="workspace-note">No visible files.</p>}
    {viewing && operations && <WorkspaceFileViewer key={`${sessionId}:${viewing}`} sessionId={sessionId} files={operations} path={viewing} onClose={() => { setViewing(null); viewerTrigger.current?.focus() }} />}
  </section>
}
