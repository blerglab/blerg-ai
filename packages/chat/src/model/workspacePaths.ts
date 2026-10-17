// Which text in an agent's message names a file in the session's folder. Two sources, both
// conservative (a wrong guess is a click that ends in "Not found", so unsure means plain text):
//   - a markdown link with a RELATIVE target: `[x](docs/a.md#L12)`;
//   - inline code that looks like a path: `runner/a.go:120`, `README.md`, `./scripts/x.sh`.
// What comes back is the path the read route wants (relative, no `./`, no `/workspace/`, no
// `:line`, `#L`, `?query`) and the line to land on. Nothing here touches the network.

export interface WorkspaceRef {
  path: string
  line?: number
  col?: number
}

const MAX_LEN = 300

// A slash-less name must end in one of these to count; a `foo.bar` that is not one is far more
// likely an identifier (os.Args, user.name) than a file.
const KNOWN_EXT = new Set([
  'md', 'mdx', 'txt', 'rst', 'adoc', 'go', 'mod', 'sum', 'ts', 'tsx', 'js', 'jsx', 'mjs', 'cjs', 'json', 'jsonc',
  'yml', 'yaml', 'toml', 'ini', 'cfg', 'conf', 'env', 'py', 'rb', 'rs', 'java', 'kt', 'swift', 'c', 'h', 'cc',
  'cpp', 'hpp', 'cs', 'php', 'sh', 'bash', 'zsh', 'fish', 'ps1', 'sql', 'css', 'scss', 'less', 'html', 'htm',
  'xml', 'svg', 'csv', 'tsv', 'log', 'lock', 'diff', 'patch', 'png', 'jpg', 'jpeg', 'gif', 'webp', 'pdf', 'tf',
  'proto', 'gradle', 'vue', 'svelte', 'dockerfile',
])
const KNOWN_BARE = new Set(['makefile', 'dockerfile', 'license', 'readme', 'changelog', 'codeowners', 'procfile', 'gemfile'])

// :12, :12:5, :12-20 and #L12, #L12-L20 (a trailing ?query is dropped before this runs).
const LINE_SUFFIX = /(?::(\d{1,7})(?::(\d{1,7}))?(?:-\d{1,7})?|#L(\d{1,7})(?:C\d{1,7})?(?:-L?\d{1,7}(?:C\d{1,7})?)?)$/

function splitSuffix(raw: string): { rest: string; line?: number; col?: number } {
  const q = raw.indexOf('?')
  const base = q >= 0 ? raw.slice(0, q) : raw
  const m = LINE_SUFFIX.exec(base)
  if (!m) return { rest: base }
  const line = Number(m[1] ?? m[3])
  const col = m[2] ? Number(m[2]) : undefined
  return { rest: base.slice(0, m.index), line: line > 0 ? line : undefined, col }
}

// The path itself, or null: relative after `./` and `/workspace/` are dropped, with no `..`,
// no empty or trailing-slash parts. Other absolute paths are never readable, so they stay text.
function clean(p: string): string | null {
  let s = p
  if (s.startsWith('/workspace/')) s = s.slice('/workspace/'.length)
  while (s.startsWith('./')) s = s.slice(2)
  if (s === '' || s.startsWith('/') || s.endsWith('/') || s.includes('//')) return null
  const parts = s.split('/')
  if (parts.some(x => x === '' || x === '.' || x === '..')) return null
  return s
}

function hasControl(s: string): boolean {
  for (const ch of s) {
    const n = ch.charCodeAt(0)
    if (n < 32 || n === 127) return true
  }
  return false
}

/** The file inline code names, or null. */
export function workspaceFromCode(text: string): WorkspaceRef | null {
  const raw = text.trim()
  if (raw === '' || raw.length > MAX_LEN) return null
  const { rest, line, col } = splitSuffix(raw)
  // Letters, digits and . _ - + / only: no spaces, no scheme (a colon is left), no @ (emails),
  // no backslash or drive letter, no ~.
  if (!/^[\p{L}\p{N}_.\-+/]+$/u.test(rest)) return null
  if (raw.includes('?') && !/^[^?]+\?[^\s]*$/.test(raw)) return null
  const path = clean(rest)
  if (!path) return null
  if (/^[\d.]+$/.test(path) || /^v?\d+(\.\d+)+$/i.test(path)) return null // numbers, versions
  const last = path.slice(path.lastIndexOf('/') + 1)
  const dot = last.lastIndexOf('.')
  const ext = dot > 0 ? last.slice(dot + 1).toLowerCase() : ''
  const bare = KNOWN_BARE.has(last.toLowerCase())
  const dotfile = dot === 0 && last.length > 2 && !KNOWN_EXT.has(last.slice(1).toLowerCase())
  const slashed = path.includes('/')
  // `and/or`, `TCP/IP`, `src/components`: a slash alone is not enough, the last part must look
  // like a file (an extension with a letter in it, a well-known bare name, a dotfile) — or the
  // text said `./`/`/workspace/` outright.
  const explicit = raw.startsWith('./') || raw.startsWith('/workspace/')
  const fileLike = (ext !== '' && /[a-z]/i.test(ext)) || bare || dotfile
  if (slashed) {
    if (!fileLike && !explicit) return null
  } else if (!(KNOWN_EXT.has(ext) || bare || explicit)) {
    return null
  }
  return { path, ...(line ? { line } : {}), ...(col ? { col } : {}) }
}

/** The file a markdown link's relative target names, or null (absolute URLs, anchors, other schemes,
 *  host paths and anything climbing out of the folder are not workspace links). */
export function workspaceFromHref(href: string | null | undefined): WorkspaceRef | null {
  if (!href) return null
  const h = href.trim()
  if (h === '' || h.length > MAX_LEN * 2 || h.startsWith('#') || h.startsWith('//') || h.startsWith('~') || h.includes('\\')) return null
  // A scheme is letters then a colon NOT followed by a line number (main.go:12 is a file).
  if (/^[a-z][a-z0-9+.-]*:(?!\d)/i.test(h)) return null
  const hashAt = h.indexOf('#')
  const frag = hashAt >= 0 ? h.slice(hashAt + 1) : ''
  const beforeFrag = hashAt >= 0 ? h.slice(0, hashAt) : h
  const q = beforeFrag.indexOf('?')
  const target = q >= 0 ? beforeFrag.slice(0, q) : beforeFrag
  let decoded: string
  try {
    decoded = decodeURIComponent(target)
  } catch {
    return null
  }
  if (hasControl(decoded)) return null
  const lm = /^L(\d{1,7})(?:C\d{1,7})?(?:-L?\d{1,7}(?:C\d{1,7})?)?$/.exec(frag)
  const { rest, line, col } = splitSuffix(decoded)
  const path = clean(rest)
  if (!path || path.includes('\\') || /^[a-z]:/i.test(path)) return null
  const at = line ?? (lm ? Number(lm[1]) : undefined)
  return { path, ...(at ? { line: at } : {}), ...(col ? { col } : {}) }
}
