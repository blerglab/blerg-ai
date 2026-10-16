// The thin divider a topic marker draws in the transcript; the session page's History tab lists the
// topics themselves.
import type { TopicPayload } from '../types'

export function topicDividerText(p: Partial<TopicPayload> | null | undefined): string {
  const title = (p?.title ?? '').trim()
  if (p?.action === 'done') return `✓ Done: ${(p.outcome ?? '').trim() || title}`
  return `▸ ${title}`
}
