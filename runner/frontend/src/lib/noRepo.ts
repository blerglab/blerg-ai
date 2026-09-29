// "No repository" sessions (mirrors internal/scratch on the server).
//
// A cluster pod needs no folder at all, so its session's repo is "". A session
// on a daemon (This machine or Local sandbox) still runs in a real folder on
// that machine's disk — a disposable, dot-prefixed one directly under the repos
// root, which the daemon never lists as a repository. That name is the one
// source of truth for "this session has no repository": nothing else records it.
import { randomSessionName } from '../sessionNames'

export const SCRATCH_PREFIX = '.scratch-'
export const NO_REPO_LABEL = 'No repository'

// What may follow SCRATCH_PREFIX — the server's scratch.Valid rule: 1-64
// letters, digits, '-' or '_', starting with a letter or digit.
const SUFFIX_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/

export function isNoRepo(repo: string | null | undefined): boolean {
  return !repo || repo.startsWith(SCRATCH_PREFIX)
}

// How a session's repo reads anywhere it is shown or grouped by: every
// no-repo session under one clear label, never a raw ".scratch-…" name that
// looks like a project.
export function repoLabel(repo: string | null | undefined): string {
  return isNoRepo(repo) ? NO_REPO_LABEL : (repo as string)
}

// The longer form for a session's own detail view: a daemon session's scratch
// folder is real and stays on disk, so its name is worth being able to find.
export function repoDetail(repo: string | null | undefined): string {
  if (!repo) return NO_REPO_LABEL
  return repo.startsWith(SCRATCH_PREFIX) ? `${NO_REPO_LABEL} · scratch folder ${repo}` : repo
}

export function validScratchSuffix(s: string): boolean {
  return SUFFIX_RE.test(s)
}

// A fresh, readable suffix: one of the session-name words plus 64 random bits
// (16 hex digits) from crypto.getRandomValues. Only a suggestion the person
// can keep or rename — the daemon is what guarantees a new folder: it never
// reuses an existing one, and picks a fresh name if this one is taken.
export function newScratchSuffix(): string {
  const bytes = new Uint8Array(8)
  crypto.getRandomValues(bytes)
  const hex = Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')
  const word = randomSessionName().toLowerCase().replace(/[^a-z0-9]/g, '') || 'scratch'
  return `${word}-${hex}`
}
