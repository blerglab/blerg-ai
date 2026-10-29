import type {ArtifactInfo, FileSpace} from '../transport/types'

export function oldestFiles(items: ArtifactInfo[]): ArtifactInfo[] {
  // The API lists newest first; its timestamps have second precision. Reverse before the
  // stable date sort so files created within the same second retain their true oldest order.
  return [...items].reverse().sort((a, b) => (Date.parse(a.created_at ?? '') || 0) - (Date.parse(b.created_at ?? '') || 0))
}
export function uploadShortfall(space: FileSpace, bytes: number): number {
  const limit = space.limits.max_session_upload_bytes
  if (!limit) return 0
  return Math.max(0, space.artifacts.reduce((n, a) => n + (a.origin === 'user' ? a.size : 0), 0) + bytes - limit)
}
/** Published files use a separate quota and cannot make room for an attachment; files the person
 *  may not delete are never preselected. */
export function selectForSpace(space: FileSpace, bytes: number): Set<string> {
  let remaining = uploadShortfall(space, bytes)
  const selected = new Set<string>()
  for (const a of oldestFiles(space.artifacts)) {
    if (remaining <= 0) break
    if (a.origin !== 'user' || a.size <= 0 || a.can_delete === false) continue
    selected.add(a.id)
    remaining -= a.size
  }
  return selected
}
/** Whether this session's attachment total is already the most an administrator may raise it to. */
export function atStorageMaximum(space: FileSpace): boolean {
  const total = space.limits.max_session_upload_bytes, cap = space.limits.max_session_upload_cap_bytes
  return !!total && !!cap && total >= cap
}
