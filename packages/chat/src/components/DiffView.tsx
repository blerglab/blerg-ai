// DiffView: a published patch (view "diff"), file by file. A list of the files with their +/−
// beside the patch (above it on a phone), unified by default with side by side on a wide screen,
// hunk headers, both sides' line numbers, long unchanged context folded, and each file's code
// coloured by its extension with the person's code colours. The patch is untrusted text: it is
// parsed (model/patch.ts) and drawn by React as text and token spans, never as markup.
import { Fragment, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { langFromName, type CodeLang } from '../model/codeLang'
import { HIGHLIGHT_MAX_CHARS, highlightSync, splitTokenLines, useHighlighterReady, type Token } from '../model/highlight'
import { foldContext, parsePatch, splitRows, patchSummaryText, type PatchFile, type PatchLine, type PatchCommit } from '../model/patch'
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

function FileBlock({ file, index, mode, ready, startOpen }: { file: PatchFile; index: number; mode: Mode; ready: boolean; startOpen: boolean }) {
  const [open, setOpen] = useState(startOpen)
  const [limit, setLimit] = useState(DIFF_FILE_LINES)
  const [unfolded, setUnfolded] = useState<ReadonlySet<string>>(() => new Set())
  const lang = useMemo(() => langFromName(file.path), [file.path])
  const hl = useMemo(() => {
    const m = new Map<PatchLine, Token[]>()
    if (!open) return m
    for (const h of file.hunks) for (const [k, v] of hunkTokens(lang, h.lines, ready)) m.set(k, v)
    return m
  }, [file, lang, ready, open])
  const total = lineCount(file)
  const cols = mode === 'split' ? 4 : 3

  let budget = limit
  const body: ReactNode[] = []
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
        continue
      }
      const lines = piece.lines.slice(0, budget)
      budget -= lines.length
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
              <td className={`diff-num${lk}`}>{r.left?.old ?? ''}</td>
              <td className={`diff-code${lk}`}>{r.left && <><span className="diff-mark" aria-hidden="true">{r.left.kind === 'del' ? '-' : ' '}</span><Code line={r.left} hl={hl} /></>}</td>
              <td className={`diff-num${rk}`}>{r.right?.new ?? ''}</td>
              <td className={`diff-code${rk}`}>{r.right && <><span className="diff-mark" aria-hidden="true">{r.right.kind === 'add' ? '+' : ' '}</span><Code line={r.right} hl={hl} /></>}</td>
            </tr>,
          )
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
              <td className="diff-num">{l.old ?? ''}</td>
              <td className="diff-num">{l.new ?? ''}</td>
              <td className="diff-code">
                <span className="diff-mark" aria-hidden="true">{l.kind === 'add' ? '+' : l.kind === 'del' ? '-' : ' '}</span>
                <span className="diff-sr">{l.kind === 'add' ? 'added: ' : l.kind === 'del' ? 'deleted: ' : ''}</span>
                <Code line={l} hl={hl} />
              </td>
            </tr>,
          )
        })
      }
    }
  }
  const shown = limit - budget

  return (
    <section className="diff-file" id={`diff-file-${index}`} data-testid="diff-file">
      <header className="diff-file-head">
        <button type="button" className="diff-file-toggle" aria-expanded={open} onClick={() => setOpen(o => !o)}>
          <span aria-hidden="true">{open ? '▾' : '▸'}</span>
        </button>
        <span className={`diff-status diff-status-${file.status}`} title={STATUS_WORD[file.status]}>{STATUS_LABEL[file.status]}</span>
        <span className="diff-file-path">{fileTitle(file)}</span>
        <span className="diff-file-counts"><span className="diff-plus">+{file.adds}</span> <span className="diff-minus">−{file.dels}</span></span>
      </header>
      {open && (
        <div className="diff-file-body">
          {file.meta.length > 0 && <p className="diff-meta">{file.meta.join(' · ')}</p>}
          {file.binary && <p className="diff-binary" data-testid="diff-binary">{file.binary}</p>}
          {!file.binary && file.hunks.length === 0 && <p className="diff-meta">No change to the text.</p>}
          {file.hunks.length > 0 && (
            <div className="diff-table-wrap">
              <table className={`diff-table diff-${mode}`} data-testid={`diff-table-${mode}`}>
                {mode === 'split' && (
                  <colgroup><col className="diff-col-num" /><col /><col className="diff-col-num" /><col /></colgroup>
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

function CommitHead({ commit }: { commit: PatchCommit }) {
  const rest = commit.lines.filter(l => l.trim() !== '' && !/^ {4}/.test(l))
  return (
    <div className="diff-commit" data-testid="diff-commit">
      <span className="diff-commit-sha" title={commit.sha}>{commit.sha.slice(0, 7)}</span>
      <span className="diff-commit-subject">{commit.subject || '(no message)'}</span>
      {rest.length > 0 && <span className="diff-commit-meta">{rest.join(' · ')}</span>}
    </div>
  )
}

export default function DiffView({ text }: { text: string }) {
  const patch = useMemo(() => parsePatch(text), [text])
  const wide = useWide()
  const [mode, setMode] = useState<Mode>('unified')
  const [copied, setCopied] = useState(false)
  const effective: Mode = wide ? mode : 'unified'
  const anyLang = useMemo(() => patch.files.some(f => langFromName(f.path) !== null), [patch])
  const ready = useHighlighterReady(anyLang)
  const mainRef = useRef<HTMLDivElement | null>(null)
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

  if (patch.files.length === 0) {
    return <pre className="artifact-text" data-testid="artifact-text">{text}</pre>
  }

  const paths = new Set(patch.files.map(f => f.path))

  function jump(i: number) {
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
      <div className="diff-layout">
        <nav className="diff-files" aria-label="Files" data-testid="diff-files">
          <ul>
            {patch.files.map((f, i) => (
              <li key={i}>
                <button type="button" className="diff-files-item" title={fileTitle(f)} onClick={() => jump(i)}>
                  <span className={`diff-status diff-status-${f.status}`} aria-label={STATUS_WORD[f.status]}>{STATUS_LABEL[f.status]}</span>
                  <span className="diff-files-name">{f.path}</span>
                  <span className="diff-files-counts"><span className="diff-plus">+{f.adds}</span> <span className="diff-minus">−{f.dels}</span></span>
                </button>
              </li>
            ))}
          </ul>
        </nav>
        <div className="diff-main" ref={mainRef}>
          {patch.files.map((f, i) => {
            const startsCommit = f.commit !== undefined && (i === 0 || patch.files[i - 1].commit !== f.commit)
            return (
              <Fragment key={i}>
                {startsCommit && <CommitHead commit={patch.commits[f.commit as number]} />}
                <FileBlock file={f} index={i} mode={effective} ready={ready} startOpen={opens[i]} />
              </Fragment>
            )
          })}
        </div>
      </div>
    </div>
  )
}
