// A repository someone typed or pasted into the launch sheet's Repository
// box: "owner/name", or a remote URL (https, ssh://, scp-like
// git@host:owner/name.git, or a bare "host/owner/name"). This is only the
// sheet's reading of it — to label the provider, match it against the list
// and predict the folder. The server parses a pasted URL itself
// (gitprovider.ParseRemoteURL) and is the one that decides; a URL is sent to
// it as typed.

// The git providers the server registers, by host. A URL on any other host is
// not offered: the server would refuse it.
export const PROVIDER_HOSTS: Record<string, string> = {
  'github.com': 'github',
  'gitlab.com': 'gitlab',
}

// Display names; an unknown id shows as itself.
export const PROVIDER_LABELS: Record<string, string> = { github: 'GitHub', gitlab: 'GitLab' }

export function providerLabel(id: string): string {
  return PROVIDER_LABELS[id] ?? id
}

export interface RepoRef {
  provider: string
  owner: string
  name: string
  // The text named its host, so the provider is fixed by it. Such an entry
  // is sent as git_url (url); a bare owner/name takes the picked provider.
  url?: string
}

export function fullName(r: RepoRef): string {
  return `${r.owner}/${r.name}`
}

// Loose segment rule: what every registered provider allows in an owner and
// a repository name that is also a safe folder (no leading dot). The server
// applies each provider's exact rules.
const SEGMENT = /^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$/

function splitPath(path: string): { owner: string; name: string } | null {
  let p = path.replace(/^\/+/, '').replace(/\/+$/, '')
  if (p.endsWith('.git')) p = p.slice(0, -4)
  const parts = p.split('/')
  if (parts.length !== 2 || !SEGMENT.test(parts[0]) || !SEGMENT.test(parts[1])) return null
  return { owner: parts[0], name: parts[1] }
}

// parseRepoInput reads text as a repository, or null when it is not one.
// A bare owner/name gets bareProvider.
export function parseRepoInput(text: string, bareProvider: string): RepoRef | null {
  const t = text.trim()
  if (t === '' || /[\s\\?#]/.test(t)) return null

  if (t.includes('://')) {
    let u: URL
    try {
      u = new URL(t)
    } catch {
      return null
    }
    if (!['https:', 'http:', 'ssh:'].includes(u.protocol) || u.port !== '') return null
    const provider = PROVIDER_HOSTS[u.hostname.toLowerCase()]
    const path = provider ? splitPath(u.pathname) : null
    if (!path || !provider) return null
    // A URL copied with credentials in it: they are never sent anywhere.
    u.username = ''
    u.password = ''
    return { provider, ...path, url: u.toString() }
  }

  // scp-like: [user@]host:owner/name(.git)
  const scp = /^(?:[^@/:]+@)?([^@/:]+):(.+)$/.exec(t)
  if (scp) {
    const provider = PROVIDER_HOSTS[scp[1].toLowerCase()]
    const path = provider && !scp[2].startsWith('/') ? splitPath(scp[2]) : null
    return path && provider ? { provider, ...path, url: t } : null
  }

  // host/owner/name, as copied from an address bar without the scheme.
  const parts = t.split('/')
  if (parts.length >= 3 && PROVIDER_HOSTS[parts[0].toLowerCase()]) {
    const path = splitPath(parts.slice(1).join('/'))
    return path ? { provider: PROVIDER_HOSTS[parts[0].toLowerCase()], ...path, url: 'https://' + t } : null
  }

  const path = splitPath(t)
  return path && !t.endsWith('.git') && !t.startsWith('/') ? { provider: bareProvider, ...path } : null
}

// What the sheet knows about one repository entry (a subset of RepoInfo).
export interface ListedRepo {
  name: string
  full_name: string
  checked_out: string[]
  provider?: string
}

function sameRepo(r: ListedRepo, ref: RepoRef): boolean {
  return (r.provider ?? '').toLowerCase() === ref.provider.toLowerCase() &&
    r.full_name.toLowerCase() === fullName(ref).toLowerCase()
}

export type CloneFolder =
  | { folder: string; present: boolean; displaced?: string }
  | { problem: string }

// predictCloneFolder mirrors the server's folder rule (cloneFolderFor in
// internal/server/repo_provider.go) from what the repository list says is on
// daemonName: "<name>" or "<owner>-<name>" when it already holds this
// repository; else the first of them with nothing there; else both are taken
// by something else. displaced names the folder that pushed the clone to its
// fallback. Advisory: the server decides from the daemon's own report, and
// the daemon re-checks on disk.
export function predictCloneFolder(ref: RepoRef, daemonName: string, repos: ListedRepo[]): CloneFolder {
  const candidates = [ref.name, `${ref.owner}-${ref.name}`]
  const onDaemon = (folder: string) =>
    repos.filter(r => r.name === folder && r.checked_out.includes(daemonName))
  for (const c of candidates) {
    if (onDaemon(c).some(r => sameRepo(r, ref))) return { folder: c, present: true }
  }
  const free = candidates.find(c => onDaemon(c).length === 0)
  if (free === undefined) {
    return { problem: `Folders "${candidates[0]}" and "${candidates[1]}" on ${daemonName} already hold other repositories — rename one to clone ${fullName(ref)} there.` }
  }
  return free === candidates[0] ? { folder: free, present: false } : { folder: free, present: false, displaced: candidates[0] }
}
