// useReviewHistory: what a diff review's Conversation is built from, read once for the review and
// kept: the diff's versions (with the bytes of the other versions), and the person's submitted
// review files (one upload per Submit). Artifacts are immutable, so a refresh (after a Submit) lists
// the files again and reads only what it has not read yet. A failed read is retried with backoff
// and never cached; a read that does not finish is given up after a timeout, and the review says
// so with a Retry instead of loading forever.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useChat } from '../context'
import { tooBigToPreview } from '../../model/artifactViews'
import { parseReviewFile, reviewFileName, type ReviewFile } from '../../model/review'
import type { ArtifactInfo } from '../../transport/types'

/** Older versions read for their summaries and excerpts, newest first. */
const MAX_VERSIONS = 30
/** Review rounds read, newest first. */
const MAX_ROUNDS = 50
/** How each read is bounded and retried (tests shorten these). */
export const HISTORY_READS = { timeoutMs: 15_000, retries: 2, backoffMs: 500 }

export interface ReviewHistory {
  /** This diff's versions, oldest first (the open one included when the list has it). */
  versions: ArtifactInfo[]
  /** The text of each other version that could be read. */
  texts: ReadonlyMap<string, string>
  /** The person's submitted reviews, oldest first. */
  uploads: { artifact: ArtifactInfo; review: ReviewFile | null }[]
  /** Something could not be read (after retries): what is here is partial. */
  error?: string
}

class TimedOut extends Error {}

function withTimeout<T>(promise: Promise<T>, ms: number): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new TimedOut('timed out')), ms)
    promise.then(
      value => { clearTimeout(timer); resolve(value) },
      error => { clearTimeout(timer); reject(error) },
    )
  })
}

async function attempt<T>(read: () => Promise<T>, live: () => boolean): Promise<T> {
  let last: unknown
  for (let i = 0; i <= HISTORY_READS.retries && live(); i++) {
    if (i > 0) await new Promise(done => setTimeout(done, HISTORY_READS.backoffMs * 2 ** (i - 1)))
    try {
      return await withTimeout(read(), HISTORY_READS.timeoutMs)
    } catch (e) {
      last = e
    }
  }
  throw last ?? new Error('cancelled')
}

/** history: null until the first read is done (it stays null without file operations).
 *  retry: read again whatever failed. */
export function useReviewHistory(artifact: ArtifactInfo, refresh: number): { history: ReviewHistory | null; retry: () => void } {
  const chat = useChat()
  const files = chat?.files ?? null
  const sessionId = chat?.sessionId ?? ''
  const [loaded, setLoaded] = useState<ReviewHistory | null>(null)
  const [retries, setRetries] = useState(0)
  // Only successful reads are kept.
  const cache = useRef(new Map<string, string>())

  useEffect(() => {
    if (!files) return
    let cancelled = false
    const live = () => !cancelled
    void (async () => {
      let failed = false
      let items: ArtifactInfo[] = []
      try {
        items = await attempt(() => files.list(sessionId), live)
      } catch {
        failed = true
      }
      const read = async (a: ArtifactInfo): Promise<string | null> => {
        const kept = cache.current.get(a.id)
        if (kept !== undefined) return kept
        try {
          const body = await attempt(async () => (await files.raw(sessionId, a.id)).text(), live)
          cache.current.set(a.id, body)
          return body
        } catch {
          failed = true
          return null
        }
      }
      const versions = items
        .filter(a => a.name === artifact.name && a.origin !== 'user' && a.view === 'diff')
        .sort((a, b) => (a.version ?? 0) - (b.version ?? 0))
      const others = versions
        .filter(a => a.id !== artifact.id && !a.missing && !tooBigToPreview('diff', a.size))
        .slice(-MAX_VERSIONS)
      const reviewName = reviewFileName(artifact.name)
      const uploads = items
        .filter(a => a.name === reviewName && a.origin === 'user' && !a.missing)
        .sort((a, b) => (a.version ?? 0) - (b.version ?? 0))
        .slice(-MAX_ROUNDS)
      const [texts, reviews] = await Promise.all([
        Promise.all(others.map(async a => [a.id, await read(a)] as const)),
        Promise.all(uploads.map(async a => {
          const body = await read(a)
          return { artifact: a, review: body === null ? null : parseReviewFile(body) }
        })),
      ])
      if (cancelled) return
      setLoaded({
        versions,
        texts: new Map(texts.filter((t): t is readonly [string, string] => t[1] !== null)),
        uploads: reviews,
        ...(failed ? { error: 'Part of this review’s history could not be read.' } : {}),
      })
    })()
    return () => {
      cancelled = true
    }
  }, [files, sessionId, artifact.id, artifact.name, refresh, retries])

  const retry = useCallback(() => setRetries(n => n + 1), [])
  return { history: loaded, retry }
}
