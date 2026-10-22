// The thin divider a topic marker draws in the transcript; the session page's History tab lists the
// topics themselves.
import type { MarkPayload, TopicPayload } from '../types'

export function topicDividerText(p: Partial<TopicPayload> | null | undefined): string {
  const title = (p?.title ?? '').trim()
  if (p?.action === 'done') return `✓ Done: ${(p.outcome ?? '').trim() || title}`
  return `▸ ${title}`
}

/** The thin row a mark draws in the transcript ("◆ Decision: …" for the person's own, "◇ Decision
 *  (agent): …" for any decision the agent wrote, its account of the person's ruling included,
 *  "✓ Milestone: …", "⚠ Finding: …"), or null for a back-dated mark (an import: it records an earlier
 *  moment, which History shows) and for one that is not a mark. Text only, never markup. */
export function markRowText(p: Partial<MarkPayload> | null | undefined): string | null {
  const text = typeof p?.text === 'string' ? p.text.trim() : ''
  if (!text || (typeof p?.at === 'string' && p.at)) return null
  switch (p?.kind) {
    case 'decision': return p.by === 'person' && p.posted_by === 'owner' ? `◆ Decision: ${text}` : `◇ Decision (agent): ${text}`
    case 'milestone': return `✓ Milestone: ${text}`
    case 'finding': return `⚠ Finding: ${text}`
    default: return null
  }
}
