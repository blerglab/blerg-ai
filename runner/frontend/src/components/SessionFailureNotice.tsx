/** A disconnected cluster session can be resumed after its pod terminates. */
export default function SessionFailureNotice({ status, reason }: { status: string; reason?: string }) {
  if (!reason || !['error', 'disconnected'].includes(status)) return null
  return (
    <div data-testid="error-reason" role="status" style={{ color: 'var(--danger)', padding: '6px 12px', fontSize: 12, borderBottom: '1px solid var(--stone)', background: 'color-mix(in srgb, var(--danger) 8%, var(--basalt))', overflowWrap: 'anywhere', flexShrink: 0 }}>
      {reason}
    </div>
  )
}
