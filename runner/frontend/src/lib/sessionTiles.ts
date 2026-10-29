import type { SessionInfo } from '../types'
import { selectSessionSections } from './sessionSelection'
import { selectSessionAggregate } from './sessionAggregate'
import { isNoRepo } from './noRepo'

export function sessionMonogram(title: string) {
  const words = title.trim().match(/[\p{L}\p{N}]+/gu) ?? []
  return (words.length > 1 ? [...words[0]!][0]! + [...words[1]!][0]! : words[0]?.slice(0, 2) || '??').toLocaleUpperCase('en-US')
}
export function projectTint(session: SessionInfo) {
  const project = !isNoRepo(session.repo) ? session.repo : session.project_path
  let hash = 0
  for (const char of project ?? '') hash = (Math.imul(hash, 31) + char.codePointAt(0)!) | 0
  return `hsl(${(hash >>> 0) % 360} 32% 30%)`
}
export function orderedTiles(sessions: SessionInfo[], query = '', focus: string | null = null, current?: string) {
  return selectSessionSections(sessions, query, focus, current).main
}

export function sessionTileState(session: SessionInfo, sessions: readonly SessionInfo[] = []) {
  return selectSessionAggregate(session, sessions).label
}
