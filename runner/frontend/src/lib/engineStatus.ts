// Engine discovery after install: why an engine isn't offered on a target, and the
// "Check again" / "Re-detect PATH" actions (POST /api/daemons/{id}/engine-check).
// The server writes every reason and fix; this side only bounds what it shows.
import { apiFetch } from '../apiFetch'

export interface EngineStatusRow {
  id: string
  name: string
  /** available, not_installed, not_logged_in, misconfigured, outdated, unsupported or not_connected. */
  status: string
  reason: string
  fix?: string
  /** check_again, redetect_path, settings, or none. */
  action?: string
  daemon_id?: string
  found_at?: string
  searched_path?: string[]
}

const ID = /^[a-z][a-z0-9_-]{0,63}$/
const ACTIONS = new Set(['check_again', 'redetect_path', 'settings'])

function text(v: unknown, max = 600): string {
  return typeof v === 'string' ? v.slice(0, max) : ''
}

/** Rows from a server list (launch options' unavailable, or a desktop's engine_statuses), bounded;
 * malformed entries are dropped rather than failing the whole sheet. */
export function parseEngineRows(raw: unknown, names: Record<string, string> = {}): EngineStatusRow[] {
  if (!Array.isArray(raw)) return []
  const out: EngineStatusRow[] = []
  for (const item of raw.slice(0, 16)) {
    if (!item || typeof item !== 'object') continue
    const r = item as Record<string, unknown>
    const id = typeof r.id === 'string' ? r.id : typeof r.engine === 'string' ? r.engine : ''
    if (!ID.test(id) || out.some(o => o.id === id)) continue
    const status = text(r.status, 32)
    if (!status) continue
    const row: EngineStatusRow = { id, name: text(r.name, 64) || names[id] || id, status, reason: text(r.reason) }
    const fix = text(r.fix)
    if (fix) row.fix = fix
    if (typeof r.action === 'string' && ACTIONS.has(r.action)) row.action = r.action
    if (typeof r.daemon_id === 'string' && r.daemon_id.length <= 128) row.daemon_id = r.daemon_id
    const found = text(r.found_at, 512)
    if (found) row.found_at = found
    if (Array.isArray(r.searched_path)) row.searched_path = r.searched_path.filter((d): d is string => typeof d === 'string').slice(0, 32).map(d => d.slice(0, 512))
    out.push(row)
  }
  return out
}

export interface EngineCheckOutcome {
  rows: EngineStatusRow[]
  pathAdded: string[]
  pathError?: string
}

/** Asks the desktop to re-detect its engines now (optionally re-reading the login-shell PATH first). */
export async function checkEngines(daemonId: string, redetectPath: boolean, names: Record<string, string> = {}): Promise<EngineCheckOutcome> {
  const res = await apiFetch(`/api/daemons/${encodeURIComponent(daemonId)}/engine-check`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ redetect_path: redetectPath }),
  })
  let body: Record<string, unknown> = {}
  try { body = await res.json() as Record<string, unknown> } catch { /* an empty or HTML answer */ }
  if (!res.ok) throw new Error(text(body.error, 300) || 'The desktop could not check its engines. Try again.')
  return {
    rows: parseEngineRows(body.engine_statuses, names),
    pathAdded: Array.isArray(body.path_added) ? body.path_added.filter((d): d is string => typeof d === 'string').slice(0, 32) : [],
    pathError: text(body.path_error, 400) || undefined,
  }
}

export const ENGINE_STATUS_LABELS: Record<string, string> = {
  available: 'Ready',
  not_installed: 'Not found',
  not_logged_in: 'Not logged in',
  misconfigured: 'Not set up',
  outdated: 'Too old',
  unsupported: 'Not offered here',
  not_connected: 'Not connected',
}

// The outcome of a check outlives the list: the launch sheet hides it while it re-reads what is
// offered, and the note must still be there when it comes back. Kept for a minute, per row.
export type EngineNote = { text: string; error?: boolean; at: number }
export const recentNotes = new Map<string, EngineNote>()
export const NOTE_TTL_MS = 60_000
/** Forget every kept outcome (tests). */
export function clearEngineNotes() { recentNotes.clear() }
export function noteKey(row: EngineStatusRow, daemonId?: string) { return `${row.daemon_id ?? daemonId ?? ''}\0${row.id}` }

/** The kept outcome for a row, while it is fresh. */
export function keptNote(row: EngineStatusRow, daemonId?: string): EngineNote | undefined {
  const kept = recentNotes.get(noteKey(row, daemonId))
  return kept && Date.now() - kept.at < NOTE_TTL_MS ? kept : undefined
}
/** Keep a row's outcome. */
export function keepNote(row: EngineStatusRow, daemonId: string | undefined, note: { text: string; error?: boolean }) {
  recentNotes.set(noteKey(row, daemonId), { ...note, at: Date.now() })
}
