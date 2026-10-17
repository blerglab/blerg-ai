// The commits an agent named, as chips: seven characters each, the full sha on hover. Nothing links
// out of Blerg: with `onOpen` (the host has a diff of them) a chip opens that diff at the commit,
// else it copies the sha and says "Copied". Three show; "+N more" opens the rest in place. Shared by
// the checklist (here) and the runner's History tab.
import { useEffect, useState } from 'react'
import './commitChips.css'

/** `url` is accepted for compatibility and not used: commits never link to a forge. */
export interface CommitRef { sha: string; url?: string | null }

/** How many chips show before "+N more". */
export const COMMITS_SHOWN = 3

export function CommitChips({ commits, onOpen, variant }: {
  commits: readonly CommitRef[]
  /** Opens the host's diff of these commits at `sha`; absent, a chip copies its sha. */
  onOpen?: (sha: string) => void
  /** 'reel': the runner's History classes (its own stylesheet styles them). */
  variant?: 'reel'
}) {
  const [copied, setCopied] = useState<string | null>(null)
  const [all, setAll] = useState(false)
  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(null), 2000)
    return () => clearTimeout(t)
  }, [copied])
  const copy = (sha: string) => {
    navigator.clipboard?.writeText(sha).then(() => setCopied(sha), () => { /* nothing to say: the sha is in the title */ })
  }
  const reel = variant === 'reel'
  const chip = reel ? 'reel-chip reel-commit' : 'commit-chip'
  const shown = all ? commits : commits.slice(0, COMMITS_SHOWN)
  const rest = commits.length - shown.length
  return (
    <span className={reel ? 'reel-chips' : 'commit-chips'}>
      {shown.map(c => (
        onOpen
          ? (
              <button key={c.sha} type="button" className={chip} title={`${c.sha} · open in the diff`}
                aria-label={`Open commit ${c.sha.slice(0, 7)} in the diff`} data-testid="reel-commit"
                onClick={() => onOpen(c.sha)}>{c.sha.slice(0, 7)}</button>
            )
          : (
              <button key={c.sha} type="button" className={`${chip}${copied === c.sha ? ' copied' : ''}`}
                title={`${c.sha} (click to copy)`} aria-label={`Copy commit ${c.sha}`} data-testid="reel-commit"
                onClick={() => copy(c.sha)}>{c.sha.slice(0, 7)}</button>
            )
      ))}
      {rest > 0 && (
        <button type="button" className={`${chip} commit-more`} aria-expanded={false} onClick={() => setAll(true)}>+{rest} more</button>
      )}
      {copied && <span className={reel ? 'reel-copied' : 'commit-copied'} role="status">Copied</span>}
    </span>
  )
}
