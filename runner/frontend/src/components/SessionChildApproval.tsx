import { useEffect, useRef } from 'react'
import { ENGINE_LABELS } from '../lib/runtimes'
import { useChildApproval, type ApprovalRuntime } from '../hooks/useChildApproval'
import './SessionChildApproval.css'

const names = (engines: string[]) => engines.map(id => ENGINE_LABELS[id] ?? id).join(', ')

/** Grants are authoritative server snapshots, never optimistic or inferred from an engine name. */
export default function SessionChildApproval({sessionId, active = true, refreshKey = ''}: {sessionId: string; active?:boolean; refreshKey?:string}) {
  // A new parent always starts without the previous parent's authority on screen.
  return <ApprovalControls key={sessionId} sessionId={sessionId} active={active} refreshKey={refreshKey} />
}
function ApprovalControls({sessionId,active,refreshKey}: {sessionId: string; active:boolean; refreshKey:string}) {
  const { value, loading, saving, error, save, refresh } = useChildApproval(sessionId, active, refreshKey)
  const toggle = (runtime: ApprovalRuntime['runtime'], enabled: boolean) => save(runtime, enabled)
  if (loading) return <p className="child-approval-note">Loading child approvals…</p>
  if (!value && !error) return null
  return <div className="child-approval" data-testid="session-child-approval">
    {value && <>
      {value.runtimes.map(r => <RuntimeControl key={r.runtime} value={r} saving={saving} onChange={enabled => { void toggle(r.runtime, enabled) }} />)}
      <p className="child-approval-note">Children of this session {value.runtimes.some(r => r.runtime === 'cluster') ? 'on the cluster' : 'on this desktop'}, in any repository, with the engines listed; a newly available engine asks again. Turning one off stops new automatic starts; running children keep running. This does not change desktop rules.</p>
    </>}
    {saving && <p role="status" className="child-approval-note">Saving child approval…</p>}
    {error && <p role="alert" className="child-approval-note">{error} {!saving && <button type="button" className="perm-revoke" onClick={refresh}>Refresh</button>}</p>}
  </div>
}
function RuntimeControl({value:r, saving, onChange}: {value: ApprovalRuntime; saving:boolean; onChange:(enabled:boolean)=>void}) {
  const input = useRef<HTMLInputElement>(null)
  useEffect(() => { if (input.current) input.current.indeterminate = r.partial }, [r.partial])
  const sandbox = r.runtime === 'docker'
  const cluster = r.runtime === 'cluster'
  const kind = cluster ? 'cluster' : sandbox ? 'sandboxed' : 'unsandboxed'
  return <div className="child-approval-runtime" data-testid={`perm-children-${r.runtime}`}>
    <label className="rail-check"><input ref={input} type="checkbox" checked={r.enabled} disabled={saving || (!r.can_enable && !r.granted_engines.length)}
      onChange={e => onChange(r.partial && !r.can_enable ? false : e.target.checked)} />
      <span>{cluster ? 'Start children on the cluster without asking' : `Start ${kind} children without asking`}</span></label>
    {!sandbox && !cluster && <p className="child-approval-note">Unsandboxed children run as you with full filesystem access.</p>}
    {r.allowed_engines.length > 0 && <p className="child-approval-note">Engines: {names(r.allowed_engines)}</p>}
    {r.partial && <p className="child-approval-note perm-active">Allowed for {names(r.granted_engines)} only. <button type="button" className="perm-revoke" disabled={saving} onClick={() => onChange(false)} aria-label={`Revoke ${kind} children`}>Revoke</button></p>}
    {r.reason && <p className="child-approval-note">{r.reason}</p>}
    {r.standing_rule_engines.length > 0 && <p className="child-approval-note">{cluster ? 'Cluster' : 'Desktop'} rules still allow {names(r.standing_rule_engines)} independently. Manage those in Settings → Sessions starting sessions.</p>}
  </div>
}
