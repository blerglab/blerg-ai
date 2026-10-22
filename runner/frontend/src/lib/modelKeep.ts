// Keeping the person's model and effort when the sheet changes where a session runs (card #151).
// The pick is kept as the person made it; each place's list is matched against it: the same
// routing id, else the same display name (one model under another id), else the same family (an
// Opus for an Opus). Nothing matching: the place's default, said aloud (never a silent swap).
import type { EngineModel } from '../types'

export interface ModelPick { id: string; name?: string }

/** A model's family: "opus" for claude-opus-5-5 or "Opus 5.5"; else the name's first word. */
export function modelFamily(m: { id: string; name?: string }): string {
  const fam = /(?:^|[/.])claude-([a-z]+)-/i.exec(m.id)?.[1]
  if (fam) return fam.toLowerCase()
  const word = (m.name ?? '').trim().split(/\s+/)[0] ?? ''
  return word.toLowerCase()
}

export type ModelMatch = { model: EngineModel; by: 'id' | 'name' | 'family' }

/** The model in `list` that stands for the person's pick, and how it was found; null: none. */
export function matchModel(pick: ModelPick | null, list: readonly EngineModel[]): ModelMatch | null {
  if (!pick) return null
  const byId = list.find(m => m.id === pick.id)
  if (byId) return { model: byId, by: 'id' }
  const name = pick.name?.trim().toLowerCase()
  const byName = name ? list.find(m => m.name.trim().toLowerCase() === name) : undefined
  if (byName) return { model: byName, by: 'name' }
  const fam = modelFamily(pick)
  const byFamily = fam ? list.filter(m => modelFamily(m) === fam) : []
  if (byFamily.length > 0) return { model: byFamily.find(m => m.section === 'main') ?? byFamily[0], by: 'family' }
  return null
}

/** Where a session runs, as the notice says it. */
export function placeWords(runtime: string, daemonName?: string): string {
  if (runtime === 'cluster') return 'in the cluster'
  if (runtime === 'docker') return 'in the local sandbox'
  return daemonName ? `on ${daemonName}` : 'on this machine'
}

/** What changed and why, when the person's model or effort is not what will run here; null when it
 *  is (the same model, by id or by name, and the effort they picked or none picked). */
export function keepNotice(opts: {
  pick: ModelPick | null
  match: ModelMatch | null
  selected: EngineModel | undefined
  explicitEffort: string | null
  effort: string | undefined
  place: string
}): string | null {
  const { pick, match, selected, explicitEffort, effort, place } = opts
  const parts: string[] = []
  const pickName = pick ? pick.name || pick.id : ''
  if (pick && selected && (!match || match.by === 'family') && selected.name.trim().toLowerCase() !== pickName.trim().toLowerCase()) {
    parts.push(`${pickName} isn't available ${place}: switched to ${selected.name}`)
  }
  if (explicitEffort && selected && selected.efforts.length > 0 && !selected.efforts.includes(explicitEffort)) {
    parts.push(`${explicitEffort} effort isn't available for ${selected.name}: ${effort ? `using ${effort}` : 'using the model\'s own'}`)
  }
  return parts.length > 0 ? parts.join('. ') : null
}
