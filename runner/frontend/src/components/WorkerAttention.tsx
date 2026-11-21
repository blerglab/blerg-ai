import type { useAttentionItems } from '../hooks/useSessionAttention'

/** The full workspace's decisions, independent of list search or whether its workers are open. */
export default function WorkerAttention({ items }: { items: ReturnType<typeof useAttentionItems> }) {
  if (!items.length) return null
  const failed = items.filter(item => item.failed).length
  const blocked = items.reduce((sum, item) => sum + item.count - (item.failed ? 1 : 0), 0)
  const summary = [failed ? `${failed} failed` : '', blocked ? `${blocked} ${blocked === 1 ? 'needs' : 'need'} you` : ''].filter(Boolean).join(', ')
  return <span style={{ display: 'flex', gap: 4, minWidth: 0, overflow: 'hidden' }}>
    <span data-testid="card-attention-count" aria-label={summary} title={summary} style={{ fontSize: '0.7rem', flexShrink: 0, color: failed ? 'var(--danger)' : 'var(--amber)' }}>{summary}</span>
    {items.map(({ session, failed }) => <span key={session.id} role="img" aria-label={`${session.title || 'Untitled chat'}: ${failed ? 'failed' : 'Needs you'}`} title={`${session.title || 'Untitled chat'}: ${failed ? 'failed' : 'Needs you'}`} style={{ color: failed ? 'var(--danger)' : 'var(--amber)', fontSize: '0.7rem', overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis' }}>{failed ? '!' : '?'} {session.title || 'Untitled chat'}</span>)}
  </span>
}
