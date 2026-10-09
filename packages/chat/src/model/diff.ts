// Diffs with no dependency: a unified diff of two texts (what the review message carries, for the
// agent to apply) and a word-level diff (what the Changes toggle renders). Both rest on one LCS
// over lines; the word diff refines each changed run of lines with a second LCS over words.

export type DiffPart = { kind: 'same' | 'ins' | 'del'; text: string }

type Op = 'same' | 'ins' | 'del'

/** The largest DP table the line and word LCS will fill; beyond it a changed middle is given up
 *  on as one deletion and one insertion. */
const MAX_CELLS = 25_000_000

/** An edit script from a to b: the shortest in the LCS sense, with equal prefix and suffix
 *  trimmed first; within a changed stretch deletions come before insertions. */
function edits<T>(a: T[], b: T[]): Op[] {
  let pre = 0
  while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++
  let suf = 0
  while (suf < a.length - pre && suf < b.length - pre && a[a.length - 1 - suf] === b[b.length - 1 - suf]) suf++
  const n = a.length - pre - suf
  const m = b.length - pre - suf
  const out: Op[] = new Array(pre).fill('same')
  if (n === 0 || m === 0 || (n + 1) * (m + 1) > MAX_CELLS) {
    for (let i = 0; i < n; i++) out.push('del')
    for (let j = 0; j < m; j++) out.push('ins')
  } else {
    // dp[i][j]: the LCS length of a[pre+i..] and b[pre+j..], as one flat table.
    const w = m + 1
    const dp = new Int32Array((n + 1) * w)
    for (let i = n - 1; i >= 0; i--) {
      for (let j = m - 1; j >= 0; j--) {
        dp[i * w + j] = a[pre + i] === b[pre + j]
          ? dp[(i + 1) * w + j + 1] + 1
          : Math.max(dp[(i + 1) * w + j], dp[i * w + j + 1])
      }
    }
    let i = 0
    let j = 0
    while (i < n || j < m) {
      if (i < n && j < m && a[pre + i] === b[pre + j]) { out.push('same'); i++; j++ }
      else if (j >= m || (i < n && dp[(i + 1) * w + j] >= dp[i * w + j + 1])) { out.push('del'); i++ }
      else { out.push('ins'); j++ }
    }
  }
  for (let k = 0; k < suf; k++) out.push('same')
  return out
}

/** The text's lines, each with its newline kept (so a final line without one differs from the
 *  same line with it). An empty text has no lines. */
function lines(text: string): string[] {
  if (text === '') return []
  return text.split(/(?<=\n)/)
}

// ─── unified diff ─────────────────────────────────────────────────────────────

const CONTEXT = 3

interface Hunk { aStart: number; aCount: number; bStart: number; bCount: number; lines: string[] }

/** One line of a hunk, with the marker the format wants when the line has no newline. */
function hunkLine(prefix: string, line: string): string[] {
  if (line.endsWith('\n')) return [prefix + line.slice(0, -1)]
  return [prefix + line, '\\ No newline at end of file']
}

const range = (start: number, count: number) => (count === 1 ? `${start}` : `${count === 0 ? start - 1 : start},${count}`)

/** A unified diff of a against b (`--- a/<name>`, `+++ b/<name>`, @@ hunks with three lines of
 *  context); '' when they are equal. */
export function unifiedDiff(a: string, b: string, name: string): string {
  if (a === b) return ''
  const al = lines(a)
  const bl = lines(b)
  const ops = edits(al, bl)
  // Which op each change sits at, and where the a and b lines stand before each op.
  const aAt: number[] = []
  const bAt: number[] = []
  let ai = 0
  let bi = 0
  for (const op of ops) {
    aAt.push(ai)
    bAt.push(bi)
    if (op !== 'ins') ai++
    if (op !== 'del') bi++
  }
  const hunks: Hunk[] = []
  let k = 0
  while (k < ops.length) {
    if (ops[k] === 'same') { k++; continue }
    // A hunk runs from CONTEXT lines before this change to CONTEXT lines after the last change
    // reachable without a gap wider than twice the context.
    const from = Math.max(0, k - CONTEXT)
    let last = k
    let probe = k + 1
    while (probe < ops.length) {
      if (ops[probe] !== 'same') { last = probe; probe++; continue }
      if (probe - last > 2 * CONTEXT) break
      probe++
    }
    const to = Math.min(ops.length, last + 1 + CONTEXT)
    const out: string[] = []
    let aCount = 0
    let bCount = 0
    for (let i = from; i < to; i++) {
      const op = ops[i]
      if (op === 'same') { out.push(...hunkLine(' ', al[aAt[i]])); aCount++; bCount++ }
      else if (op === 'del') { out.push(...hunkLine('-', al[aAt[i]])); aCount++ }
      else { out.push(...hunkLine('+', bl[bAt[i]])); bCount++ }
    }
    hunks.push({ aStart: aAt[from] + 1, aCount, bStart: bAt[from] + 1, bCount, lines: out })
    k = to
  }
  const text = [`--- a/${name}`, `+++ b/${name}`]
  for (const h of hunks) {
    text.push(`@@ -${range(h.aStart, h.aCount)} +${range(h.bStart, h.bCount)} @@`, ...h.lines)
  }
  return text.join('\n') + '\n'
}

// ─── word diff ────────────────────────────────────────────────────────────────

function push(parts: DiffPart[], kind: DiffPart['kind'], text: string) {
  if (!text) return
  const last = parts[parts.length - 1]
  if (last && last.kind === kind) last.text += text
  else parts.push({ kind, text })
}

/** Words and the whitespace between them, each its own token. */
const words = (text: string) => text.match(/\S+|\s+/g) ?? []

/** The parts of b against a: same, inserted and deleted, in reading order. Lines are compared
 *  first; within each run of changed lines, words. Adjacent parts of one kind are merged, so the
 *  same parts plus the deletions read as a, and the same parts plus the insertions as b. */
export function wordDiff(a: string, b: string): DiffPart[] {
  const al = lines(a)
  const bl = lines(b)
  const parts: DiffPart[] = []
  let ai = 0
  let bi = 0
  let del = ''
  let ins = ''
  const flush = () => {
    if (del && ins) {
      const aw = words(del)
      const bw = words(ins)
      let i = 0
      let j = 0
      for (const op of edits(aw, bw)) {
        if (op === 'same') { push(parts, 'same', aw[i]); i++; j++ }
        else if (op === 'del') { push(parts, 'del', aw[i]); i++ }
        else { push(parts, 'ins', bw[j]); j++ }
      }
    } else {
      push(parts, 'del', del)
      push(parts, 'ins', ins)
    }
    del = ''
    ins = ''
  }
  for (const op of edits(al, bl)) {
    if (op === 'same') { flush(); push(parts, 'same', al[ai]); ai++; bi++ }
    else if (op === 'del') { del += al[ai]; ai++ }
    else { ins += bl[bi]; bi++ }
  }
  flush()
  return parts
}
