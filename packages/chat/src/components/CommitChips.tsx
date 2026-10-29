// The commits an agent named, as chips: seven characters each, the full sha on hover. Nothing links
// out of Blerg: with `onOpen` (the host has a diff of them) a chip opens that diff at the commit;
// else, under a CommitViewerHost that can read the session's commits, it opens the commit's diff
// generated on demand from the session's workspace; else it copies the sha and says "Copied". Three show; "+N more" opens the rest in place. Shared by
// the checklist (here) and the runner's History tab.
import { useRefContext } from './RefLinks'
import { useEffect, useState, type ReactElement } from 'react'
import { useCommitOpener } from './CommitViewer'
import type { WorkspaceGitRef } from '../transport/types'
import { Hover } from './Hovercard'
import './commitChips.css'

/** `url` is accepted for compatibility and not used: commits never link to a forge. */
export interface CommitRef { sha: string; url?: string | null; member?: WorkspaceGitRef }

/** How many chips show before "+N more". */
export const COMMITS_SHOWN = 3

export function CommitChips({ commits, onOpen, variant, hover }: {
  commits: readonly CommitRef[]
  /** Where a chip's hovercard reads the commit from: the session and its published diff of it. */
  hover?: { sessionId: string; diff?: string }
  /** Opens the host's diff of these commits at `sha`; absent, a chip copies its sha. */
  onOpen?: (sha: string) => void
  /** 'reel': the runner's History classes (its own stylesheet styles them). */
  variant?: 'reel'
}) {
  const refs = useRefContext()
  const opener = useCommitOpener()
  const sessionId = hover?.sessionId
  const open = onOpen ?? (opener && sessionId ? (sha: string, member?: WorkspaceGitRef) => opener(sessionId, sha, member, refs) : undefined)
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
  const withHover = (sha: string, member: WorkspaceGitRef | undefined, chip: ReactElement) => hover
    ? <Hover key={`${sha}/${JSON.stringify(member)}`} target={{ kind: 'commit', sessionId: hover.sessionId, sha, ...(member ? { member } : {}), ...(hover.diff ? { diff: hover.diff } : {}) }}>{chip}</Hover>
    : chip
  const chip = reel ? 'reel-chip reel-commit' : 'commit-chip'
  const shown = all ? commits : commits.slice(0, COMMITS_SHOWN)
  const rest = commits.length - shown.length
  return (
    <span className={reel ? 'reel-chips' : 'commit-chips'}>
      {shown.map(c => withHover(c.sha, c.member, (
        (c.member ? opener && sessionId : open)
          ? (
              // button-guard-ignore: variant="reel" is styled by the runner's .reel-chip.reel-commit (SessionRail.css); the default by .commit-chip.
              <button key={`${c.sha}/${JSON.stringify(c.member)}`} type="button" className={chip} title={`${c.sha} · open in the diff`}
                aria-label={`Open commit ${c.sha.slice(0, 7)} in the diff`} data-testid="reel-commit"
                onClick={() => c.member && opener && sessionId ? opener(sessionId, c.sha, c.member) : open?.(c.sha)}>{c.sha.slice(0, 7)}</button>
            )
          : (
              // button-guard-ignore: variant="reel" is styled by the runner's .reel-chip.reel-commit (SessionRail.css); the default by .commit-chip.
              <button key={`${c.sha}/${JSON.stringify(c.member)}`} type="button" className={`${chip}${copied === c.sha ? ' copied' : ''}`}
                title={`${c.sha} (click to copy)`} aria-label={`Copy commit ${c.sha}`} data-testid="reel-commit"
                onClick={() => copy(c.sha)}>{c.sha.slice(0, 7)}</button>
            )
      )))}
      {rest > 0 && (
        // button-guard-ignore: variant="reel" is styled by the runner's .reel-chip.reel-commit (SessionRail.css); the default by .commit-chip.
        <button type="button" className={`${chip} commit-more`} aria-expanded={false} onClick={() => setAll(true)}>+{rest} more</button>
      )}
      {copied && <span className={reel ? 'reel-copied' : 'commit-copied'} role="status">Copied</span>}
    </span>
  )
}
