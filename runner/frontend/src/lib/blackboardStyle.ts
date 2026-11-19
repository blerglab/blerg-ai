export type ReleaseStyle = { name?: string; stages?: { key: string; label: string }[] }
export type BlackboardEventPayload = { reason?: string; number?: number; gate_result?: string }
const eventNames: Record<string,string> = {opened:'Opened',alpha:'Alpha',rc:'RC',plan_set:'Plan set',date_moved:'Date moved',final:'Final',release_imported:'Release imported',decision:'Decision',correction:'Correction',shipped:'Shipped',proposal_rejected:'Proposal declined'}

// Events can carry arbitrary JSON assertions, including null for a declined batch.
export function blackboardEventPayload(raw: unknown): BlackboardEventPayload {
 if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {}
 const payload = raw as Record<string,unknown>
 return {
  reason: typeof payload.reason === 'string' ? payload.reason : undefined,
  gate_result: typeof payload.gate_result === 'string' ? payload.gate_result : undefined,
  number: typeof payload.number === 'number' && Number.isSafeInteger(payload.number) && payload.number > 0 ? payload.number : undefined,
 }
}
export function blackboardEventLabel(event: {kind:string;payload:unknown}, style?:ReleaseStyle): string {
 const payload=blackboardEventPayload(event.payload)
 return (style?.stages?.find(s=>s.key===event.kind)?.label ?? eventNames[event.kind] ?? event.kind.replaceAll('_',' ')) + (payload.number ? ` ${payload.number}` : '')
}
