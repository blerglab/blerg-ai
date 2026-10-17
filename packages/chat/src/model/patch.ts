// Reading a unified patch (what `blerg-runner diff` publishes, or any .diff/.patch): its commits,
// its files, their hunks and lines with the numbers of both sides. Pure, and the text is untrusted:
// nothing here is ever drawn as markup, and a line the format does not expect is kept as a note or
// dropped, never trusted. Also the rows of a side-by-side view and the cheap summary a card shows.

export type PatchLineKind = 'ctx' | 'add' | 'del' | 'note'

export interface PatchLine {
  kind: PatchLineKind
  /** The line without its first character (' ', '+', '-', '\'). */
  text: string
  /** Its number on the old side (context and deletions). */
  old?: number
  /** Its number on the new side (context and additions). */
  new?: number
}

export interface PatchHunk {
  /** The whole @@ line. */
  header: string
  oldStart: number
  newStart: number
  lines: PatchLine[]
}

export type PatchFileStatus = 'modified' | 'added' | 'deleted' | 'renamed' | 'copied'

export interface PatchFile {
  /** The path shown: the new side's, or the old side's for a deletion. */
  path: string
  oldPath: string | null
  newPath: string | null
  status: PatchFileStatus
  /** Set for a binary file: git's line about it (with the sizes the CLI adds). */
  binary?: string
  /** Mode changes, similarity and the like, as git wrote them. */
  meta: string[]
  hunks: PatchHunk[]
  adds: number
  dels: number
  /** The commit (an index into Patch.commits) the file belongs to, when the patch has commits. */
  commit?: number
}

export interface PatchCommit {
  sha: string
  /** The header lines after `commit <sha>`: author, date, the message (indented). */
  lines: string[]
  /** The message's first line. */
  subject: string
}

export interface Patch {
  commits: PatchCommit[]
  files: PatchFile[]
  adds: number
  dels: number
}

const HUNK_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/
const COMMIT_RE = /^commit ([0-9a-f]{40}|[0-9a-f]{64})\b/

/** A path as git prints it: C-quoted ("a/x\ty", UTF-8 bytes as octal escapes) or plain. */
export function unquotePath(s: string): string {
  if (!(s.length >= 2 && s.startsWith('"') && s.endsWith('"'))) return s
  const bytes: number[] = []
  const simple: Record<string, number> = { n: 10, t: 9, '"': 34, '\\': 92, a: 7, b: 8, f: 12, r: 13, v: 11 }
  const body = s.slice(1, -1)
  const enc = new TextEncoder()
  for (let i = 0; i < body.length; i++) {
    const c = body[i]
    if (c === '\\' && i + 1 < body.length) {
      const n = body[i + 1]
      if (n in simple) { bytes.push(simple[n]); i++; continue }
      const oct = body.slice(i + 1, i + 4)
      if (/^[0-7]{3}$/.test(oct)) { bytes.push(parseInt(oct, 8) & 0xff); i += 3; continue }
    }
    bytes.push(...enc.encode(c))
  }
  return new TextDecoder().decode(new Uint8Array(bytes))
}

function dropPrefix(p: string, prefix: string): string {
  return p.startsWith(prefix) ? p.slice(prefix.length) : p
}

/** The two paths of `diff --git <rest>`: `a/<p> b/<q>`, plain or quoted. */
function headerPaths(rest: string): [string | null, string | null] {
  const q = '"(?:[^"\\\\]|\\\\.)*"'
  const m = new RegExp(`^(${q}) (${q})$`).exec(rest)
    ?? new RegExp(`^(${q}) (b/.*)$`).exec(rest)
    ?? new RegExp(`^(a/.*) (${q})$`).exec(rest)
  if (m) return [dropPrefix(unquotePath(m[1]), 'a/'), dropPrefix(unquotePath(m[2]), 'b/')]
  // Plain: the two halves are the same path when nothing was renamed.
  if (rest.length % 2 === 1 && rest.startsWith('a/')) {
    const half = (rest.length - 1) / 2
    const a = rest.slice(0, half)
    const b = rest.slice(half + 1)
    if (b.startsWith('b/') && a.slice(2) === b.slice(2)) return [a.slice(2), b.slice(2)]
  }
  const i = rest.indexOf(' b/')
  if (i > 0) return [dropPrefix(rest.slice(0, i), 'a/'), rest.slice(i + 3)]
  return [rest, rest]
}

/** The path of a ---/+++ line: null for /dev/null; without the a/ or b/ git puts on it, and
 *  without a tab and what follows (a timestamp in a patch from diff -u, git's own marker). */
function sidePath(rest: string, prefix: string): string | null {
  let p = rest
  if (!p.startsWith('"')) {
    const tab = p.indexOf('\t')
    if (tab >= 0) p = p.slice(0, tab)
  } else {
    p = unquotePath(p.replace(/\t.*$/, ''))
  }
  if (p === '/dev/null') return null
  return dropPrefix(p, prefix)
}

function newFile(oldPath: string | null, newPath: string | null, commit: number | undefined): PatchFile {
  return {
    path: newPath ?? oldPath ?? '',
    oldPath,
    newPath,
    status: 'modified',
    meta: [],
    hunks: [],
    adds: 0,
    dels: 0,
    ...(commit !== undefined ? { commit } : {}),
  }
}

function settle(f: PatchFile) {
  if (f.oldPath === null && f.newPath !== null) f.status = 'added'
  else if (f.newPath === null && f.oldPath !== null) f.status = 'deleted'
  f.path = (f.status === 'deleted' ? f.oldPath : f.newPath) ?? f.oldPath ?? f.path
}

/** The commits and files of a unified patch. Text before the first file or commit is dropped. */
export function parsePatch(text: string): Patch {
  const out: Patch = { commits: [], files: [], adds: 0, dels: 0 }
  const lines = text.split('\n')
  if (lines.length && lines[lines.length - 1] === '') lines.pop()
  let file: PatchFile | null = null
  let commit: PatchCommit | null = null
  let hunk: PatchHunk | null = null
  let oldLeft = 0
  let newLeft = 0
  let oldNo = 0
  let newNo = 0
  let inBinaryPatch = false
  // The file whose ---/+++ lines are still to come (set by its diff --git line).
  let gitHeader = false

  const closeFile = () => {
    if (file) settle(file)
    file = null
    hunk = null
    oldLeft = newLeft = 0
    inBinaryPatch = false
    gitHeader = false
  }

  for (const raw of lines) {
    const line = raw.endsWith('\r') ? raw.slice(0, -1) : raw
    if (hunk && (oldLeft > 0 || newLeft > 0)) {
      const c = line[0]
      const h: PatchHunk = hunk
      const f = file as PatchFile | null
      if (c === '+') {
        h.lines.push({ kind: 'add', text: line.slice(1), new: newNo++ })
        newLeft--
        if (f) f.adds++
        continue
      }
      if (c === '-') {
        h.lines.push({ kind: 'del', text: line.slice(1), old: oldNo++ })
        oldLeft--
        if (f) f.dels++
        continue
      }
      if (c === ' ' || line === '') {
        h.lines.push({ kind: 'ctx', text: line.slice(1), old: oldNo++, new: newNo++ })
        oldLeft--
        newLeft--
        continue
      }
      if (c === '\\') {
        h.lines.push({ kind: 'note', text: line.slice(1).trim() })
        continue
      }
      // A short hunk: what follows is not its body.
      oldLeft = newLeft = 0
    }
    if (hunk && line.startsWith('\\')) {
      ;(hunk as PatchHunk).lines.push({ kind: 'note', text: line.slice(1).trim() })
      continue
    }
    const cm = COMMIT_RE.exec(line)
    if (cm) {
      closeFile()
      commit = { sha: cm[1], lines: [], subject: '' }
      out.commits.push(commit)
      continue
    }
    if (line.startsWith('diff --git ') || line.startsWith('diff --cc ') || line.startsWith('diff --combined ')) {
      closeFile()
      const commitIndex = commit ? out.commits.length - 1 : undefined
      if (line.startsWith('diff --git ')) {
        const [a, b] = headerPaths(line.slice('diff --git '.length))
        file = newFile(a, b, commitIndex)
        gitHeader = true
      } else {
        const p = unquotePath(line.replace(/^diff --(cc|combined) /, ''))
        file = newFile(p, p, commitIndex)
      }
      out.files.push(file)
      continue
    }
    const hm = HUNK_RE.exec(line)
    if (hm && file) {
      const f: PatchFile = file
      hunk = { header: line, oldStart: +hm[1], newStart: +hm[3], lines: [] }
      f.hunks.push(hunk)
      oldLeft = hm[2] !== undefined ? +hm[2] : 1
      newLeft = hm[4] !== undefined ? +hm[4] : 1
      oldNo = +hm[1]
      newNo = +hm[3]
      gitHeader = false
      continue
    }
    if (line.startsWith('--- ') && (!file || gitHeader || (file as PatchFile).hunks.length > 0)) {
      // A git file's own --- line, or (diff -u) the start of a new file.
      if (!file || !gitHeader) {
        closeFile()
        file = newFile(null, null, commit ? out.commits.length - 1 : undefined)
        out.files.push(file)
      }
      ;(file as PatchFile).oldPath = sidePath(line.slice(4), 'a/')
      continue
    }
    if (line.startsWith('+++ ') && file && (file as PatchFile).hunks.length === 0) {
      ;(file as PatchFile).newPath = sidePath(line.slice(4), 'b/')
      continue
    }
    if (file) {
      const f: PatchFile = file
      if (inBinaryPatch) {
        if (line === '') inBinaryPatch = false
        continue
      }
      if (line.startsWith('new file mode')) { f.oldPath = null; f.meta.push(line); continue }
      if (line.startsWith('deleted file mode')) { f.newPath = null; f.meta.push(line); continue }
      if (line.startsWith('rename from ')) { f.oldPath = unquotePath(line.slice(12)); f.status = 'renamed'; continue }
      if (line.startsWith('rename to ')) { f.newPath = unquotePath(line.slice(10)); f.status = 'renamed'; continue }
      if (line.startsWith('copy from ')) { f.oldPath = unquotePath(line.slice(10)); f.status = 'copied'; continue }
      if (line.startsWith('copy to ')) { f.newPath = unquotePath(line.slice(8)); f.status = 'copied'; continue }
      if (line.startsWith('Binary files ') || line === 'GIT binary patch') {
        f.binary = line === 'GIT binary patch' ? 'Binary file (patch not shown)' : line
        inBinaryPatch = line === 'GIT binary patch'
        continue
      }
      if (line.startsWith('index ') || line === '') continue
      if (/^(old mode|new mode|similarity index|dissimilarity index)/.test(line)) { f.meta.push(line); continue }
      continue
    }
    if (commit) {
      commit.lines.push(line)
      if (!commit.subject && /^ {4}\S/.test(line)) commit.subject = line.trim()
    }
  }
  closeFile()
  for (const f of out.files) {
    out.adds += f.adds
    out.dels += f.dels
  }
  return out
}

export interface PatchSummary { files: number; adds: number; dels: number; names: string[] }

/** What a card says of a patch: how many files (each path once), +adds −dels, the first names. */
export function patchSummary(text: string, names = 3): PatchSummary {
  const p = parsePatch(text)
  const seen: string[] = []
  for (const f of p.files) if (!seen.includes(f.path)) seen.push(f.path)
  return { files: seen.length, adds: p.adds, dels: p.dels, names: seen.slice(0, names) }
}

/** "3 files · +12 −4". */
export function patchSummaryText(s: { files: number; adds: number; dels: number }): string {
  return `${s.files} ${s.files === 1 ? 'file' : 'files'} · +${s.adds} −${s.dels}`
}

export interface SplitRow {
  left?: PatchLine
  right?: PatchLine
  /** A note (no newline at end of file) spans both sides. */
  note?: string
}

/** The rows of a hunk side by side: context on both sides, each run of deletions beside the
 *  additions that follow it. */
export function splitRows(lines: readonly PatchLine[]): SplitRow[] {
  const rows: SplitRow[] = []
  let i = 0
  while (i < lines.length) {
    const l = lines[i]
    if (l.kind === 'ctx') { rows.push({ left: l, right: l }); i++; continue }
    if (l.kind === 'note') { rows.push({ note: l.text }); i++; continue }
    const dels: PatchLine[] = []
    const adds: PatchLine[] = []
    while (i < lines.length && lines[i].kind === 'del') dels.push(lines[i++])
    while (i < lines.length && lines[i].kind === 'add') adds.push(lines[i++])
    for (let k = 0; k < Math.max(dels.length, adds.length); k++) rows.push({ left: dels[k], right: adds[k] })
  }
  return rows
}

/** A run of context longer than this is folded, keeping FOLD_KEEP lines at each end. */
export const FOLD_OVER = 8
export const FOLD_KEEP = 3

export type HunkPiece =
  | { kind: 'lines'; lines: PatchLine[] }
  | { kind: 'fold'; lines: PatchLine[] }

/** A hunk's lines with each long run of context folded in the middle. */
export function foldContext(lines: readonly PatchLine[]): HunkPiece[] {
  const out: HunkPiece[] = []
  let cur: PatchLine[] = []
  let i = 0
  while (i < lines.length) {
    if (lines[i].kind !== 'ctx') { cur.push(lines[i++]); continue }
    let j = i
    while (j < lines.length && lines[j].kind === 'ctx') j++
    const run = lines.slice(i, j)
    const first = i === 0
    const last = j === lines.length
    // At a hunk's edge only the side next to a change is kept.
    const keepHead = first ? 0 : FOLD_KEEP
    const keepTail = last ? 0 : FOLD_KEEP
    if (run.length > FOLD_OVER && run.length > keepHead + keepTail + 1) {
      cur.push(...run.slice(0, keepHead))
      out.push({ kind: 'lines', lines: cur })
      out.push({ kind: 'fold', lines: run.slice(keepHead, run.length - keepTail) })
      cur = run.slice(run.length - keepTail)
    } else {
      cur.push(...run)
    }
    i = j
  }
  if (cur.length) out.push({ kind: 'lines', lines: cur })
  return out.filter(p => p.lines.length > 0)
}
