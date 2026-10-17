// The commits an agent named, as chips: seven characters each, the full sha on hover; a link to the
// commit's page when the host knows the repository, else a button that copies the sha. Shared by the
// checklist (here) and the runner's History tab. `onViewDiff` is the hook for "View diff": when the
// host supplies it, each commit gets a small button that calls it with the sha; absent, none is drawn.
import { useEffect, useState } from 'react'
import './commitChips.css'

export interface CommitRef { sha: string; url?: string | null }

export function CommitChips({ commits, onViewDiff, variant }: {
  commits: readonly CommitRef[]
  onViewDiff?: (sha: string) => void
  /** 'reel': the runner's History classes (its own stylesheet styles them). */
  variant?: 'reel'
}) {
  const [copied, setCopied] = useState<string | null>(null)
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
  return (
    <div className={reel ? 'reel-chips' : 'commit-chips'}>
      {commits.map(c => (
        <span key={c.sha} className="commit-one">
          {c.url
            ? (
              <a className={chip} href={c.url} target="_blank" rel="noopener noreferrer"
                title={c.sha} aria-label={`Commit ${c.sha}`} data-testid="reel-commit">{c.sha.slice(0, 7)}</a>
            )
            : (
              <button type="button" className={`${chip}${copied === c.sha ? ' copied' : ''}`}
                title={`${c.sha} (click to copy)`} aria-label={`Copy commit ${c.sha}`} data-testid="reel-commit"
                onClick={() => copy(c.sha)}>{c.sha.slice(0, 7)}</button>
            )}
          {onViewDiff && (
            <button type="button" className="commit-diff" aria-label="View diff" title={`View the diff of ${c.sha.slice(0, 7)}`}
              onClick={() => onViewDiff(c.sha)}>diff</button>
          )}
        </span>
      ))}
      {copied && <span className={reel ? 'reel-copied' : 'commit-copied'} role="status">Copied</span>}
    </div>
  )
}
