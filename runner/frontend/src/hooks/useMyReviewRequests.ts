// The pending review requests of the sessions this person started (GET /api/review-requests): the
// bell lists them beside questions and proposals. Read when the bell mounts, when it opens, every
// minute while the page is visible, when the page comes back into view, and at once when this tab
// decides or dismisses one (notifyReviewRequestsChanged), so the badge does not count it meanwhile.
import { useCallback, useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'

export interface MyReviewRequest {
  request_id: string
  session_id: string
  artifact_id: string
  artifact_name: string
  artifact_version: number
  ask: string
  url: string
}

const POLL_MS = 60_000

const CHANGED = 'blerg:review-requests-changed'

/** This tab decided or dismissed a review request: the bell reads its list again. */
export function notifyReviewRequestsChanged() {
  if (typeof window !== 'undefined') window.dispatchEvent(new Event(CHANGED))
}

export function useMyReviewRequests(open: boolean): MyReviewRequest[] {
  const [items, setItems] = useState<MyReviewRequest[]>([])
  const read = useCallback(() => {
    // Through a promise, so a fetch that throws at once is just a read that failed.
    Promise.resolve().then(() => apiFetch('/api/review-requests')).then(async resp => {
      if (!resp?.ok) return
      const body = await resp.json() as { review_requests?: MyReviewRequest[] }
      setItems(Array.isArray(body.review_requests) ? body.review_requests : [])
    }, () => { /* the next read tries again */ })
  }, [])
  useEffect(() => {
    read()
    const timer = setInterval(() => { if (document.visibilityState === 'visible') read() }, POLL_MS)
    const onVisible = () => { if (document.visibilityState === 'visible') read() }
    window.addEventListener(CHANGED, read)
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      clearInterval(timer)
      window.removeEventListener(CHANGED, read)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [read])
  useEffect(() => { if (open) read() }, [open, read])
  return items
}
