/** Presentation only: IDs remain opaque routing values and belong in full-ID tooltips. */
function family(id: string): { name: string; details: string; route: string } | undefined {
  const parts = id.split('/')
  const native = parts[parts.length - 1]
  const match = /^(?:nvidia-)?nemotron-(?:(\d+(?:\.\d+)?)-)?(lightning|super-vl)(?:-|$)/i.exec(native)
  if (!match) return undefined
  const size = /(?:^|-)(\d+(?:\.\d+)?b)(?:-|$)/i.exec(native)?.[1].toUpperCase()
  const quant = /(?:^|-)((?:nvfp|fp|bf|q)\d+(?:_[a-z0-9]+)?)(?:-|$)/i.exec(native)?.[1].toUpperCase()
  return {
    name: match[2].toLowerCase() === 'lightning' ? 'Nemotron Lightning' : 'Nemotron Super VL',
    details: [size, quant, match[1] && `v${match[1]}`].filter(Boolean).join(' · '),
    route: parts.slice(0, -1).join('/').replace(/^custom:/, ''),
  }
}

export function modelDisplayName(id: string, suppliedName?: string): string {
  return family(id)?.name || suppliedName || id.replace(/^claude-/, '')
}

/** Disambiguate within the actual catalog; a short label must never conceal a choice. */
export function modelDisplayNames(models: readonly { id: string; name?: string }[]): ReadonlyMap<string, string> {
  const names = new Map(models.map(m => [m.id, modelDisplayName(m.id, m.name)]))
  const collisions = (label: string) => [...names.values()].filter(v => v === label).length > 1
  const ambiguous = models.filter(m => collisions(names.get(m.id)!))
  for (const m of ambiguous) {
    const info = family(m.id)
    if (info?.details) names.set(m.id, `${info.name} (${info.details})`)
  }
  const stillAmbiguous = ambiguous.filter(m => collisions(names.get(m.id)!))
  for (const m of stillAmbiguous) {
    const info = family(m.id)
    names.set(m.id, `${names.get(m.id)} — ${info?.route || m.id}`)
  }
  const unresolved = models.filter(m => collisions(names.get(m.id)!))
  for (const m of unresolved) names.set(m.id, `${modelDisplayName(m.id, m.name)} — ${m.id}`)
  // A supplied display name may itself equal one of the generated labels.
  // Exact IDs are the final authority when presentation cannot distinguish choices.
  if (new Set(names.values()).size !== names.size) return new Map(models.map(m => [m.id, m.id]))
  return names
}
