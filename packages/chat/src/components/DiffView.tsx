// DiffView: a published patch (view "diff"), file by file. A list of the files with their +/−
// beside the patch (above it on a phone), unified by default with side by side on a wide screen,
// hunk headers, both sides' line numbers, long unchanged context folded, and each file's code
// coloured by its extension with the person's code colours. The patch is untrusted text: it is
// parsed (model/patch.ts) and drawn by React as text and token spans, never as markup.
import { Fragment, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { langFromName, type CodeLang } from '../model/codeLang'
import { HIGHLIGHT_MAX_CHARS, highlightSync, splitTokenLines, useHighlighterReady, type Token } from '../model/highlight'
import { foldContext, parsePatch, splitRows, patchSummaryText, type PatchFile, type PatchLine, type PatchCommit } from '../model/patch'
import { createDiffAnchor, diffAnchorContains, type DiffPoint } from '../model/diffSelection'
import type { Anchor, ReviewRequest } from '../model/review'
import './diff.css'

/** Lines of one file drawn before "Show more" (and how many more each click adds). */
export const DIFF_FILE_LINES = 5000
/** Files are drawn open until this many lines are on the page; the rest start closed. */
export const DIFF_OPEN_LINES = 10000
/** Side by side needs at least this much width. */
const SPLIT_MIN_WIDTH = 900

type Mode = 'unified' | 'split'

function useWide(): boolean {
  const query = `(min-width: ${SPLIT_MIN_WIDTH}px)`
  const get = () => typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia(query).matches
  const [wide, setWide] = useState(get)
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const mq = window.matchMedia(query)
    const on = () => setWide(mq.matches)
    mq.addEventListener?.('change', on)
    return () => mq.removeEventListener?.('change', on)
  }, [query])
  return wide
}

// A phone: the file list becomes a picker in the toolbar (the same breakpoint as diff.css).
const PHONE_MAX_WIDTH = 699
function usePhone(): boolean {
  const query = `(max-width: ${PHONE_MAX_WIDTH}px)`
  const get = () => typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia(query).matches
  const [phone, setPhone] = useState(get)
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const mq = window.matchMedia(query)
    const on = () => setPhone(mq.matches)
    mq.addEventListener?.('change', on)
    return () => mq.removeEventListener?.('change', on)
  }, [query])
  return phone
}

function tokens(list: Token[]): ReactNode[] {
  return list.map((t, i) => ('text' in t ? t.text : <span key={i} className={t.cls}>{tokens(t.kids)}</span>))
}

/** Each line's tokens, per side, for one hunk: the old side (context and deletions) and the new
 *  side (context and additions) are coloured as two texts so a construct spanning lines reads right. */
function hunkTokens(lang: CodeLang | null, lines: readonly PatchLine[], ready: boolean): Map<PatchLine, Token[]> {
  const out = new Map<PatchLine, Token[]>()
  if (!ready || !lang || lang === 'diff') return out
  for (const side of ['old', 'new'] as const) {
    const picked = lines.filter(l => (side === 'old' ? l.kind === 'ctx' || l.kind === 'del' : l.kind === 'ctx' || l.kind === 'add'))
    const text = picked.map(l => l.text).join('\n')
    if (!text || text.length > HIGHLIGHT_MAX_CHARS) continue
    const hl = highlightSync(lang, text)
    if (!hl) continue
    const split = splitTokenLines(hl)
    picked.forEach((l, i) => {
      // Context is coloured from the new side; both texts hold it.
      if (side === 'old' && l.kind === 'ctx') return
      if (split[i]) out.set(l, split[i])
    })
  }
  return out
}

function Code({ line, hl }: { line: PatchLine; hl: Map<PatchLine, Token[]> }) {
  const t = hl.get(line)
  return <>{t ? tokens(t) : line.text}</>
}

const STATUS_LABEL: Record<PatchFile['status'], string> = { modified: 'M', added: 'A', deleted: 'D', renamed: 'R', copied: 'C' }
const STATUS_WORD: Record<PatchFile['status'], string> = { modified: 'modified', added: 'added', deleted: 'deleted', renamed: 'renamed', copied: 'copied' }

function lineCount(f: PatchFile): number {
  return f.hunks.reduce((n, h) => n + h.lines.length + 1, 0)
}

function fileTitle(f: PatchFile): string {
  return (f.status === 'renamed' || f.status === 'copied') && f.oldPath && f.newPath ? `${f.oldPath} → ${f.newPath}` : f.path
}

type DiffRange = NonNullable<Anchor['diff']>
interface RequestRange { id: string; range: DiffRange; request: ReviewRequest }
interface SelectionPaint {
  selected?: DiffRange
  requests?: readonly RequestRange[]
  focusedId?: string | null
  onPick?: (point: DiffPoint, extend: boolean) => void
  onComment?: (point: DiffPoint) => void
  renderThread?: (request: ReviewRequest) => ReactNode
  draftAnchor?: Anchor | null
  renderDraft?: (anchor: Anchor) => ReactNode
}

function FileBlock({ file, index, mode, ready, startOpen, bare = false, selected, requests = [], focusedId, onPick, onComment, renderThread, draftAnchor, renderDraft }: {
  file: PatchFile; index: number; mode: Mode; ready: boolean; startOpen: boolean; bare?: boolean
} & SelectionPaint) {
  const [open, setOpen] = useState(startOpen)
  const [limit, setLimit] = useState(DIFF_FILE_LINES)
  const [unfolded, setUnfolded] = useState<ReadonlySet<string>>(() => new Set())
  const focused = requests.find(request => request.id === focusedId && request.range.fileIndex === index + 1)
  const fileDraft = draftAnchor?.diff?.fileIndex === index + 1 ? draftAnchor : null
  const revealed = open || !!focused || !!fileDraft
  const focusedLines = new Set<number>()
  const lang = useMemo(() => langFromName(file.path), [file.path])
  const hl = useMemo(() => {
    const m = new Map<PatchLine, Token[]>()
    if (!revealed) return m
    for (const h of file.hunks) for (const [k, v] of hunkTokens(lang, h.lines, ready)) m.set(k, v)
    return m
  }, [file, lang, ready, revealed])
  const total = lineCount(file)
  const cols = mode === 'split' ? 4 : 3

  function contains(range: DiffRange, side: DiffPoint['side'], line: number | undefined, hunk: number) {
    return line !== undefined && range.fileIndex === index + 1 && range.hunk === hunk && range.side === side && line >= range.start && line <= range.end
  }
  function paint(side: DiffPoint['side'], line: number | undefined, hunk: number): string {
    const hits = requests.filter(request => contains(request.range, side, line, hunk))
    return `${selected && contains(selected, side, line, hunk) ? ' diff-selected' : ''}${hits.length ? ' diff-requested' : ''}${hits.some(request => request.id === focusedId) ? ' diff-request-focused' : ''}`
  }
  function numberCell(side: DiffPoint['side'], line: number | undefined, hunk: number, tint = '') {
    const classes = `diff-num${tint}${paint(side, line, hunk)}`
    const isFocused = requests.some(request => request.id === focusedId && contains(request.range, side, line, hunk))
    if (isFocused && line !== undefined) focusedLines.add(line)
    if (line === undefined) return <td className={classes} />
    const path = side === 'old' ? file.oldPath : file.newPath
    const point = {fileIndex: index + 1, hunk, side, line}
    return <td className={classes} data-diff-focused={isFocused || undefined}>
      {onPick ? <button type="button" className="diff-line-select"
        aria-label={`Select ${side} line ${line} in ${path ?? file.path}`}
        aria-pressed={!!selected && contains(selected, side, line, hunk)}
        data-diff-side={side} data-diff-line={line} data-diff-file={index + 1} data-diff-hunk={hunk}
        data-diff-focused={isFocused || undefined}
        onClick={event => onPick(point, event.shiftKey)}
        onKeyDown={event => {
          if (event.key === 'Enter' && event.shiftKey) {
            event.preventDefault()
            onPick(point, true)
          }
        }}>{line}</button> : line}
    </td>
  }

  /** The one [+] of a code cell, GitHub's way: at the start of the code, just right of the line
   *  numbers, on the first line of a wrapped line. It comments on `side`'s line. */
  function commentButton(side: DiffPoint['side'], line: number | undefined, hunk: number) {
    if (!onComment || line === undefined) return null
    const path = side === 'old' ? file.oldPath : file.newPath
    const point = {fileIndex: index + 1, hunk, side, line}
    return <button type="button" className="diff-comment-add" data-comment-side={side} data-comment-line={line}
      aria-label={`Comment on ${side} line ${line} in ${path ?? file.path}`} onClick={() => onComment(point)}>+</button>
  }
  const commentable = onComment ? ' diff-code-commentable' : ''

  let budget = limit
  const body: ReactNode[] = []
  const renderedThreads = new Set<string>()
  let renderedDraft = false
  function appendComments(points: readonly DiffPoint[], hidden?: 'folded' | 'not shown') {
    const endsHere = (range: DiffRange) => points.some(point => point.fileIndex === range.fileIndex && point.hunk === range.hunk && point.side === range.side && point.line === range.end)
    const location = (range: DiffRange) => hidden ? <p className="diff-comment-location">{range.file} · {range.side} {range.start === range.end ? `line ${range.start}` : `lines ${range.start}–${range.end}`} · Code {hidden}</p> : null
    if (renderThread) for (const request of requests) {
      if (renderedThreads.has(request.id) || !endsHere(request.range)) continue
      renderedThreads.add(request.id)
      const content = renderThread(request.request)
      if (content !== null && content !== undefined && content !== false) body.push(<tr key={`thread:${request.id}`} className="diff-comment-row" data-inline-request={request.id}><td colSpan={cols}>{location(request.range)}{content}</td></tr>)
    }
    if (!renderedDraft && fileDraft?.diff && renderDraft && endsHere(fileDraft.diff)) {
      renderedDraft = true
      const content = renderDraft(fileDraft)
      if (content !== null && content !== undefined && content !== false) body.push(<tr key="draft" className="diff-comment-row diff-comment-draft"><td colSpan={cols}>{location(fileDraft.diff)}{content}</td></tr>)
    }
  }
  function pointsFor(lines: readonly PatchLine[], hunk: number): DiffPoint[] {
    if (!(renderThread && requests.length) && !(fileDraft && renderDraft)) return []
    if (lines.length > 2) {
      // This path is a folded run of contiguous context. Match only the bounded
      // comment endpoints instead of allocating two points per hidden code line.
      const first = lines[0]
      const last = lines[lines.length - 1]
      const ranges = [...requests.map(request => request.range), ...(fileDraft?.diff ? [fileDraft.diff] : [])]
      return ranges.filter(range => range.fileIndex === index + 1 && range.hunk === hunk &&
        first[range.side] !== undefined && last[range.side] !== undefined &&
        range.end >= first[range.side]! && range.end <= last[range.side]!)
        .map(range => ({fileIndex: index + 1, hunk, side: range.side, line: range.end}))
    }
    return lines.flatMap(line => (['old', 'new'] as const).flatMap(side => line[side] === undefined ? [] : [{fileIndex: index + 1, hunk, side, line: line[side]!}]))
  }
  function appendRows(lines: readonly PatchLine[], hi: number, key: string) {
    if (mode === 'split') {
      splitRows(lines).forEach((r, ri) => {
        if (r.note !== undefined) {
          body.push(<tr key={`${key}:${ri}`} className="diff-note-row"><td colSpan={4} className="diff-note">{r.note}</td></tr>)
          return
        }
        const lk = r.left ? (r.left.kind === 'del' ? ' diff-del' : '') : ' diff-empty'
        const rk = r.right ? (r.right.kind === 'add' ? ' diff-add' : '') : ' diff-empty'
        body.push(
          <tr key={`${key}:${ri}`} className="diff-row">
            {numberCell('old', r.left?.old, hi + 1, `${lk} diff-side-old`)}
            <td className={`diff-code diff-side-old${commentable}${lk}${paint('old', r.left?.old, hi + 1)}`}>{r.left && <>{commentButton('old', r.left.old, hi + 1)}<span className="diff-mark" aria-hidden="true">{r.left.kind === 'del' ? '-' : ' '}</span><Code line={r.left} hl={hl} /></>}</td>
            {numberCell('new', r.right?.new, hi + 1, `${rk} diff-side-new`)}
            <td className={`diff-code diff-side-new${commentable}${rk}${paint('new', r.right?.new, hi + 1)}`}>{r.right && <>{commentButton('new', r.right.new, hi + 1)}<span className="diff-mark" aria-hidden="true">{r.right.kind === 'add' ? '+' : ' '}</span><Code line={r.right} hl={hl} /></>}</td>
          </tr>,
        )
        appendComments(pointsFor([r.left, r.right].filter((line): line is PatchLine => line !== undefined), hi + 1))
      })
    } else {
      lines.forEach((l, li) => {
        if (l.kind === 'note') {
          body.push(<tr key={`${key}:${li}`} className="diff-note-row"><td colSpan={3} className="diff-note">{l.text}</td></tr>)
          return
        }
        const k = l.kind === 'add' ? ' diff-add' : l.kind === 'del' ? ' diff-del' : ''
        body.push(
          <tr key={`${key}:${li}`} className={`diff-row${k}`}>
            {numberCell('old', l.old, hi + 1)}
            {numberCell('new', l.new, hi + 1)}
            <td className={`diff-code${commentable}${paint('old', l.old, hi + 1)}${paint('new', l.new, hi + 1)}`}>
              {/* One [+] per row: an added or unchanged line comments on its new line, a removed one on its old line. */}
              {l.kind === 'del' ? commentButton('old', l.old, hi + 1) : commentButton('new', l.new, hi + 1)}
              <span className="diff-mark" aria-hidden="true">{l.kind === 'add' ? '+' : l.kind === 'del' ? '-' : ' '}</span>
              <span className="diff-sr">{l.kind === 'add' ? 'added: ' : l.kind === 'del' ? 'deleted: ' : ''}</span>
              <Code line={l} hl={hl} />
            </td>
          </tr>,
        )
        appendComments(pointsFor([l], hi + 1))
      })
    }
  }
  for (let hi = 0; hi < file.hunks.length && budget > 0; hi++) {
    const h = file.hunks[hi]
    budget--
    body.push(
      <tr key={`h${hi}`} className="diff-hunk-row"><td colSpan={cols} className="diff-hunk">{h.header}</td></tr>,
    )
    const pieces = foldContext(h.lines)
    for (let pi = 0; pi < pieces.length && budget > 0; pi++) {
      const piece = pieces[pi]
      const key = `${hi}:${pi}`
      if (piece.kind === 'fold' && !unfolded.has(key)) {
        budget--
        body.push(
          <tr key={`f${key}`} className="diff-fold-row">
            <td colSpan={cols}>
              <button type="button" className="diff-fold" onClick={() => setUnfolded(s => new Set(s).add(key))}>
                ⋯ {piece.lines.length} unchanged lines
              </button>
            </td>
          </tr>,
        )
        appendComments(pointsFor(piece.lines, hi + 1), 'folded')
        continue
      }
      const lines = piece.lines.slice(0, budget)
      budget -= lines.length
      appendRows(lines, hi, key)
    }
  }
  const shown = limit - budget
  if (focused && focusedLines.size < focused.range.end - focused.range.start + 1) {
    const range = focused.range
    const hunk = file.hunks[range.hunk - 1]
    // A focused request can sit beyond a huge fold or file cap. Reveal its bounded
    // same-side excerpt without drawing thousands of unrelated context lines.
    const excerpt = hunk?.lines.filter(line => line[range.side] !== undefined && line[range.side]! >= Math.max(1, range.start - 3) && line[range.side]! <= range.end + 3) ?? []
    if (excerpt.length) {
      body.push(<tr key="focused-hunk" className="diff-hunk-row"><td colSpan={cols} className="diff-hunk">Focused request · {hunk.header}</td></tr>)
      appendRows(excerpt, range.hunk - 1, 'focused-request')
    }
  }

  appendComments(requests.filter(request => request.range.fileIndex === index + 1).map(request => ({fileIndex: index + 1, hunk: request.range.hunk, side: request.range.side, line: request.range.end})), 'not shown')
  if (fileDraft?.diff) appendComments([{fileIndex: index + 1, hunk: fileDraft.diff.hunk, side: fileDraft.diff.side, line: fileDraft.diff.end}], 'not shown')

  return (
    <section className={`diff-file${bare ? ' diff-file-bare' : ''}`} id={bare ? undefined : `diff-file-${index}`} data-testid="diff-file">
      {!bare && <header className="diff-file-head">
        <button type="button" className="diff-file-toggle" aria-expanded={revealed} onClick={() => setOpen(o => !o)}>
          <span aria-hidden="true">{revealed ? '▾' : '▸'}</span>
        </button>
        <span className={`diff-status diff-status-${file.status}`} title={STATUS_WORD[file.status]}>{STATUS_LABEL[file.status]}</span>
        <span className="diff-file-path">{fileTitle(file)}</span>
        <span className="diff-file-counts"><span className="diff-plus">+{file.adds}</span> <span className="diff-minus">−{file.dels}</span></span>
      </header>}
      {revealed && (
        <div className="diff-file-body">
          {file.meta.length > 0 && <p className="diff-meta">{file.meta.join(' · ')}</p>}
          {file.binary && <p className="diff-binary" data-testid="diff-binary">{file.binary}</p>}
          {!file.binary && file.hunks.length === 0 && <p className="diff-meta">No change to the text.</p>}
          {file.hunks.length > 0 && (
            <div className="diff-table-wrap">
              <table className={`diff-table diff-${mode}`} data-testid={`diff-table-${mode}`}>
                {mode === 'split' ? (
                  <colgroup><col className="diff-col-num" /><col /><col className="diff-col-num" /><col /></colgroup>
                ) : (
                  <colgroup><col className="diff-col-num" /><col className="diff-col-num" /><col /></colgroup>
                )}
                <tbody>{body}</tbody>
              </table>
            </div>
          )}
          {shown < total && (
            <p className="diff-more">
              Showing {shown} of {total} lines.{' '}
              <button type="button" className="artifact-link" onClick={() => setLimit(n => n + DIFF_FILE_LINES)}>Show more</button>
            </p>
          )}
        </div>
      )}
    </section>
  )
}

function CommitHead({ commit, index, target }: { commit: PatchCommit; index: number; target: boolean }) {
  const rest = commit.lines.filter(l => l.trim() !== '' && !/^ {4}/.test(l))
  return (
    <div className={`diff-commit${target ? ' target' : ''}`} id={`diff-commit-${index}`} data-testid="diff-commit">
      <span className="diff-commit-sha" title={commit.sha}>{commit.sha.slice(0, 7)}</span>
      <span className="diff-commit-subject">{commit.subject || '(no message)'}</span>
      {rest.length > 0 && <span className="diff-commit-meta">{rest.join(' · ')}</span>}
    </div>
  )
}

/** One small file's hunks, drawn unified without the file header (a published diff's card). The
 *  same table, colors and tints as the viewer. */
export function InlineDiff({ file }: { file: PatchFile }) {
  const ready = useHighlighterReady(langFromName(file.path) !== null)
  return <FileBlock file={file} index={0} mode="unified" ready={ready} startOpen bare />
}

/** `commit`: a sha (or the start of one) to open at: its section is scrolled to and marked. */
export default function DiffView({ text, commit, selection, onSelection, requests = [], focusedId, onComment, renderThread, renderDraft, draftAnchor }: {
  text: string; commit?: string | null
  selection?: Anchor | null
  onSelection?: (anchor: Anchor | null) => void
  requests?: ReviewRequest[]
  focusedId?: string | null
  onComment?: (anchor: Anchor) => void
  renderThread?: (request: ReviewRequest) => ReactNode
  renderDraft?: (anchor: Anchor) => ReactNode
  draftAnchor?: Anchor | null
}) {
  const patch = useMemo(() => parsePatch(text), [text])
  const startPoint = useRef<DiffPoint | null>(null)
  const priorPatch = useRef(patch)
  const [rangeMode, setRangeMode] = useState(false)
  const [notice, setNotice] = useState('')
  const selected = useMemo(() => {
    const range = selection?.diff
    return range && diffAnchorContains(patch, selection, {fileIndex: range.fileIndex, hunk: range.hunk, side: range.side, line: range.start}) ? range : undefined
  }, [patch, selection])
  const requestRanges = useMemo(() => requests.flatMap(request => {
    const range = request.anchor.diff
    return range && diffAnchorContains(patch, request.anchor, {fileIndex: range.fileIndex, hunk: range.hunk, side: range.side, line: range.start}) ? [{id: request.id, range, request}] : []
  }), [patch, requests])
  const validDraft = useMemo(() => {
    const range = draftAnchor?.diff
    return range && diffAnchorContains(patch, draftAnchor, {fileIndex: range.fileIndex, hunk: range.hunk, side: range.side, line: range.start}) ? draftAnchor : null
  }, [patch, draftAnchor])
  function comment(point: DiffPoint) {
    const withinSelection = selected && selected.fileIndex === point.fileIndex && selected.hunk === point.hunk && selected.side === point.side && point.line >= selected.start && point.line <= selected.end
    const anchor = withinSelection ? selection : createDiffAnchor(patch, point)
    if (!anchor) return
    setNotice('')
    onComment?.(anchor)
  }
  useEffect(() => {
    if (priorPatch.current !== patch) {
      priorPatch.current = patch
      startPoint.current = null
      onSelection?.(null)
    }
  }, [patch, onSelection])
  useEffect(() => { if (selection === null) startPoint.current = null }, [selection])
  useEffect(() => {
    if (focusedId) mainRef.current?.querySelector('[data-diff-focused="true"]')?.scrollIntoView({block: 'center'})
  }, [focusedId, requestRanges])

  function pick(point: DiffPoint, extend: boolean) {
    const prior = startPoint.current
    const sameScope = prior && prior.fileIndex === point.fileIndex && prior.hunk === point.hunk && prior.side === point.side
    const range = (extend || rangeMode) && sameScope ? createDiffAnchor(patch, prior, point.line) : createDiffAnchor(patch, point)
    if (!range) {
      setNotice('Select at most 200 lines and 32 KiB of text within one side of one hunk.')
      return
    }
    setNotice('')
    if (rangeMode && sameScope) startPoint.current = null
    else if (!extend || !sameScope) startPoint.current = point
    onSelection?.(range)
  }
  const want = (commit ?? '').toLowerCase()
  const target = want.length >= 7 ? patch.commits.findIndex(c => c.sha.startsWith(want)) : -1
  const wide = useWide()
  const phone = usePhone()
  const [mode, setMode] = useState<Mode>('unified')
  const [copied, setCopied] = useState(false)
  const effective: Mode = wide ? mode : 'unified'
  const anyLang = useMemo(() => patch.files.some(f => langFromName(f.path) !== null), [patch])
  const ready = useHighlighterReady(anyLang)
  const mainRef = useRef<HTMLDivElement | null>(null)
  const filesRef = useRef<HTMLElement | null>(null)
  const [currentFile, setCurrentFile] = useState({patch, index: 0})
  const currentIndex = currentFile.patch === patch ? currentFile.index : 0
  // The code pane owns the active location; file-list scrolling never changes it.
  // Section resize notifications keep collapsed files and async highlighting in sync.
  useEffect(() => {
    const root = mainRef.current
    if (!root) return
    const sections = [...root.children].filter((el): el is HTMLElement => el instanceof HTMLElement && el.classList.contains('diff-file'))
    let frame: number | undefined
    const measure = () => {
      frame = undefined
      if (!root.clientHeight || !sections.length) return
      const top = root.getBoundingClientRect().top + 8
      const visible = sections.findIndex(section => section.getBoundingClientRect().bottom > top)
      let index = visible < 0 ? sections.length - 1 : visible
      if (root.scrollHeight > root.clientHeight + 1 && root.scrollTop + root.clientHeight >= root.scrollHeight - 1) index = sections.length - 1
      setCurrentFile(prior => prior.patch === patch && prior.index === index ? prior : {patch, index})
    }
    const schedule = () => { if (frame === undefined) frame = requestAnimationFrame(measure) }
    root.addEventListener('scroll', schedule, {passive: true})
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(schedule)
    observer?.observe(root)
    sections.forEach(section => observer?.observe(section))
    schedule()
    return () => {
      root.removeEventListener('scroll', schedule)
      observer?.disconnect()
      if (frame !== undefined) cancelAnimationFrame(frame)
    }
  }, [patch])
  useEffect(() => {
    const nav = filesRef.current
    const button = nav?.querySelectorAll('button')[currentIndex]
    if (!nav || !button) return
    const box = button.getBoundingClientRect()
    const view = nav.getBoundingClientRect()
    if (box.top < view.top) nav.scrollTop += box.top - view.top
    else if (box.bottom > view.bottom) nav.scrollTop += box.bottom - view.bottom
  }, [patch, currentIndex])
  const canCopy = typeof navigator !== 'undefined' && !!navigator.clipboard
  // Files start open until DIFF_OPEN_LINES lines are on the page; the rest start closed.
  const opens = useMemo(() => {
    const out: boolean[] = []
    let onPage = 0
    for (const f of patch.files) {
      out.push(onPage < DIFF_OPEN_LINES)
      if (onPage < DIFF_OPEN_LINES) onPage += lineCount(f)
    }
    return out
  }, [patch])

  useEffect(() => {
    if (target < 0) return
    const el = mainRef.current?.querySelector(`#diff-commit-${target}`)
    if (el && 'scrollIntoView' in el) (el as HTMLElement).scrollIntoView({ block: 'start' })
  }, [target])

  if (patch.files.length === 0) {
    return <pre className="artifact-text" data-testid="artifact-text">{text}</pre>
  }

  const paths = new Set(patch.files.map(f => f.path))

  function jump(i: number) {
    setCurrentFile({patch, index: i})
    const el = mainRef.current?.querySelector(`#diff-file-${i}`)
    if (el && 'scrollIntoView' in el) (el as HTMLElement).scrollIntoView({ block: 'start' })
  }

  return (
    <div className="diff-view" data-testid="diff-view">
      <div className="diff-toolbar">
        <span className="diff-summary" data-testid="diff-summary">
          {patchSummaryText({ files: paths.size, adds: patch.adds, dels: patch.dels })}
          {patch.commits.length > 0 && ` · ${patch.commits.length} ${patch.commits.length === 1 ? 'commit' : 'commits'}`}
        </span>
        {wide && (
          <span className="diff-modes" role="group" aria-label="Layout">
            <button type="button" className="artifact-btn" aria-pressed={effective === 'unified'} onClick={() => setMode('unified')}>Unified</button>
            <button type="button" className="artifact-btn" aria-pressed={effective === 'split'} onClick={() => setMode('split')}>Side by side</button>
          </span>
        )}
        {phone && patch.files.length > 1 && (
          <select className="diff-file-picker" aria-label="Files" title={patchSummaryText({ files: paths.size, adds: patch.adds, dels: patch.dels })} data-testid="diff-file-picker" value={currentIndex} onChange={e => jump(Number(e.target.value))}>
            {patch.files.map((f, i) => <option key={i} value={i}>{`${f.path} (+${f.adds} −${f.dels})`}</option>)}
          </select>
        )}
        {canCopy && (
          <button
            type="button"
            className="artifact-btn"
            onClick={() => {
              void navigator.clipboard.writeText(text).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1200) }, () => {})
            }}
          >
            {copied ? 'Copied' : 'Copy patch'}
          </button>
        )}
      </div>
      {onSelection && <div className="diff-selection-toolbar">
        <button type="button" className="artifact-btn" aria-pressed={rangeMode} onClick={() => {
          setRangeMode(value => !value); startPoint.current = null; setNotice('')
        }}>Select range</button>
        <span className="diff-selection-help">Choose a line number; Shift-click for a range, or enable Select range and tap both ends.</span>
        {selection && <button type="button" className="artifact-btn" onClick={() => {
          startPoint.current = null; setNotice(''); onSelection(null)
        }}>Clear line selection</button>}
        {notice && <span role="status">{notice}</span>}
      </div>}
      <div className="diff-layout">
        {!phone && <nav ref={filesRef} className="diff-files" aria-label="Files" data-testid="diff-files">
          <ul>
            {patch.files.map((f, i) => (
              <li key={i}>
                <button type="button" className="diff-files-item" aria-current={currentIndex === i ? 'location' : undefined} title={fileTitle(f)} onClick={() => jump(i)}>
                  <span className={`diff-status diff-status-${f.status}`} aria-label={STATUS_WORD[f.status]}>{STATUS_LABEL[f.status]}</span>
                  <span className="diff-files-name">{f.path}</span>
                  <span className="diff-files-counts"><span className="diff-plus">+{f.adds}</span> <span className="diff-minus">−{f.dels}</span></span>
                </button>
              </li>
            ))}
          </ul>
        </nav>}
        <div className="diff-main" ref={mainRef}>
          {patch.files.map((f, i) => {
            const startsCommit = f.commit !== undefined && (i === 0 || patch.files[i - 1].commit !== f.commit)
            return (
              <Fragment key={i}>
                {startsCommit && <CommitHead commit={patch.commits[f.commit as number]} index={f.commit as number} target={f.commit === target} />}
                <FileBlock file={f} index={i} mode={effective} ready={ready} startOpen={opens[i]}
                  selected={selected} requests={requestRanges} focusedId={focusedId} onPick={onSelection ? pick : undefined}
                  onComment={onComment ? comment : undefined} renderThread={renderThread} renderDraft={renderDraft} draftAnchor={validDraft} />
              </Fragment>
            )
          })}
        </div>
      </div>
    </div>
  )
}
