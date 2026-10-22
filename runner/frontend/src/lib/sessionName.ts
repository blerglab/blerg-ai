// The new-session sheet's default name (card #143): the repository's name ("blerg"), "Session"
// without one, numbered " 2", " 3"… when a session still running already has it. No made-up
// names: the person can always type their own, and an agent can rename the session later.

/** A repository's short name: the last part of owner/name or a URL, without ".git". */
export function repoShortName(repo: string | null | undefined): string {
  const r = (repo ?? '').trim().replace(/[?#].*$/, '').replace(/\/+$/, '').replace(/\.git$/i, '')
  const last = r.split(/[/:\\]/).filter(Boolean).pop() ?? ''
  return last.trim()
}

/** The name a new session gets by default: the repository's name, else "Session"; numbered past
 *  the names running sessions already have. */
export function defaultSessionName(repo: string | null | undefined, running: Iterable<string>): string {
  const base = repoShortName(repo) || 'Session'
  const taken = new Set(Array.from(running, t => t.trim().toLowerCase()))
  if (!taken.has(base.toLowerCase())) return base
  for (let n = 2; ; n++) {
    const name = `${base} ${n}`
    if (!taken.has(name.toLowerCase())) return name
  }
}
