/** Dataset names and parameters are data, never executable queries or credentials. */
export type DatasetReference = {kind: 'dataset'; name: 'blerg.mcp' | 'blerg.gates.bins' | 'blerg.cards.merged.heatmap' | 'blerg.workers.timeline' | 'blerg.reviews.pairs' | 'blerg.cards.time' | 'blerg.gates.durations' | 'blerg.cards.merged' | 'blerg.engine.usage' | 'blerg.workers'; parameters: Record<string, string>}
export type DatasetRow = Record<string, string | number | boolean | null>
export interface DatasetSample {
  type: 'dataset_delta'; subscription_id: string; epoch: string; base_sequence: number; sequence: number
  snapshot: boolean; upserts: DatasetRow[]; removals: string[]; observed_at: string
  valid_until?: string; expected_interval_seconds?: number; next_attempt_at?: string
  status: 'ready' | 'unavailable' | 'access_removed' | 'disconnected'; cause?: string
}
export function validDataset(v: {name?: unknown; parameters?: unknown}): boolean {
  if (!v.parameters || typeof v.parameters !== 'object' || Array.isArray(v.parameters)) return false
  const p = v.parameters as Record<string, unknown>
  const allowed = v.name === 'blerg.mcp' ? ['binding'] : v.name === 'blerg.gates.durations' ? ['suite', 'since'] : v.name === 'blerg.gates.bins' ? ['width', 'suite', 'since'] : ['blerg.cards.time', 'blerg.reviews.pairs'].includes(String(v.name)) ? ['board', 'card'] : ['blerg.cards.merged', 'blerg.cards.merged.heatmap'].includes(String(v.name)) ? ['bucket', 'timezone', 'release', 'base', 'member']
    : v.name === 'blerg.engine.usage' ? ['engine'] : v.name === 'blerg.workers.timeline' ? ['since'] : v.name === 'blerg.workers' ? [] : null
  if (!allowed || Object.entries(p).some(([k, x]) => !allowed.includes(k) || typeof x !== 'string' || !x || x.length > 128)) return false
  if (v.name === 'blerg.mcp' && !/^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(String(p.binding))) return false
  if (p.since !== undefined && (typeof p.since !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(p.since) || !Number.isFinite(Date.parse(p.since)))) return false
  if (v.name === 'blerg.gates.durations' && p.suite !== undefined && !/^[a-z][a-z0-9_-]{0,63}$/.test(String(p.suite))) return false
  if (v.name === 'blerg.gates.bins' && (!/^[1-9][0-9]{0,4}$/.test(String(p.width)) || Number(p.width) > 86400 || (p.suite !== undefined && !/^[a-z][a-z0-9_-]{0,63}$/.test(String(p.suite))))) return false
  if (['blerg.cards.time', 'blerg.reviews.pairs'].includes(String(v.name)) && (!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$/.test(String(p.board)) || !/^[1-9][0-9]{0,8}$/.test(String(p.card)))) return false
  if (['blerg.cards.merged', 'blerg.cards.merged.heatmap'].includes(String(v.name))) {
    if (!['day', 'hour'].includes(String(p.bucket))) return false
    try { new Intl.DateTimeFormat('en-US', {timeZone: String(p.timezone)}) } catch { return false }
    if (p.release && !p.base) return false
    if (p.base && !/^[a-zA-Z0-9][a-zA-Z0-9._/-]*$/.test(String(p.base))) return false
  }
  if (v.name === 'blerg.engine.usage' && !['claude', 'codex', 'hermes'].includes(String(p.engine))) return false
  return true
}

// A disconnected client may retain a sample only within both authorization and
// payload retention. Neither a heartbeat nor a consumer can extend its fetch time.
export function mcpSampleExpiry(sample: Pick<DatasetSample, 'observed_at' | 'valid_until'>): number {
  const observed = Date.parse(sample.observed_at), until = Date.parse(sample.valid_until ?? '')
  return Number.isFinite(observed) && Number.isFinite(until) ? Math.min(until, observed + 24 * 60 * 60_000) : 0
}
