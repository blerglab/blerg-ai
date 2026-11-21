import type { AttentionItem } from '../hooks/useQuestionItems'
export function newestAttention(items: AttentionItem[]) {
  const time = (item: AttentionItem) => Date.parse(item.kind === 'ask' ? item.question.created_at : item.kind === 'review' ? item.review.created_at : item.session.started_at) || 0
  return [...items].sort((a, b) => time(b) - time(a))
}
