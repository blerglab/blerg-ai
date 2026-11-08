import { useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { formatDuration } from '../lib/insights'

/** Mounted by the hovercard, so unseen sessions do not generate reads. */
export default function SessionInsightSummary({ sessionId }: { sessionId: string }) {
  const [summary, setSummary] = useState<{ id: string; seconds: number } | null>(null)
  useEffect(() => {
    let cancelled = false
    apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/insights`)
      .then(r => r.ok ? r.json() : Promise.reject())
      .then(d => { if (!cancelled && Number.isFinite(d.lifetime_seconds)) setSummary({ id: sessionId, seconds: d.lifetime_seconds }) })
      .catch(() => { /* A failed preview read adds no claim about this session. */ })
    return () => { cancelled = true }
  }, [sessionId])
  if (summary?.id !== sessionId) return null
  return <p className="insights-note">{formatDuration(summary.seconds)} · test time and review rounds unavailable</p>
}
