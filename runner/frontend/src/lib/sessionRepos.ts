import type { SessionInfo } from '../types'
import { repoLabel } from './noRepo'

export function sessionRepos(session: SessionInfo) {
  const names = session.workspace?.members.map(member => member.name) ?? []
  return {
    label: names.length ? names[0] + (names.length > 1 ? ` + ${names.length - 1}` : '') : repoLabel(session.repo),
    full: names.length ? names.join(', ') : repoLabel(session.repo),
  }
}
