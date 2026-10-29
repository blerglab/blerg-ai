// The inside of a code block: highlighted tokens drawn as React spans (never markup), a diff's
// lines coloured by their first character, or plain text.
import { useMemo, type ReactNode } from 'react'
import { splitTokenLines, type Token } from '../model/highlight'

function tokens(list: Token[]): ReactNode[] {
  return list.map((t, i) => ('text' in t ? t.text : <span key={i} className={t.cls}>{tokens(t.kids)}</span>))
}

function diffClass(line: string): string | undefined {
  if (line.startsWith('+++') || line.startsWith('---')) return 'md-diff-file'
  if (line.startsWith('+')) return 'md-diff-add'
  if (line.startsWith('-')) return 'md-diff-del'
  if (line.startsWith('@@')) return 'md-diff-hunk'
  return undefined
}

/** `highlighted` colours `text`; with `tail` the colours cover only the start of the file and the
 *  rest follows as plain text (see useHighlightHead, where `text` is the whole file). */
export default function CodeText({ text, diff, highlighted, tail }: { text: string; diff?: boolean; highlighted?: Token[] | null; tail?: string }) {
  if (diff) {
    return (
      <>
        {text.split('\n').map((line, i, all) => (
          <span key={i} className={diffClass(line)}>{line}{i < all.length - 1 ? '\n' : ''}</span>
        ))}
      </>
    )
  }
  if (highlighted) return <>{tokens(highlighted)}{tail}</>
  return <>{text}</>
}

/** The lines of `text` as nodes, coloured as CodeText colours the whole (the tail plain), with
 *  their raw text. */
function lineNodes(text: string, diff: boolean | undefined, highlighted: Token[] | null | undefined, tail: string | undefined): { raw: string[]; nodes: { node: ReactNode; cls?: string }[] } {
  const raw = text.split('\n')
  if (text.endsWith('\n')) raw.pop()
  if (diff) return { raw, nodes: raw.map(line => ({ node: line, cls: diffClass(line) })) }
  if (!highlighted) return { raw, nodes: raw.map(line => ({ node: line })) }
  const head = splitTokenLines(highlighted)
  // The head ends at a line end: its last (empty) entry is where the tail's first line starts.
  if (tail) head.pop()
  const nodes: { node: ReactNode }[] = head.slice(0, raw.length).map(line => ({ node: tokens(line) }))
  for (let i = nodes.length; i < raw.length; i++) nodes.push({ node: raw[i] })
  return { raw, nodes }
}

type Lines = ReturnType<typeof lineNodes>

function Rows({ lines, from, to, marked, trailing }: { lines: Lines; from: number; to: number; marked?: number; trailing: boolean }) {
  const last = lines.raw.length - 1
  const out: ReactNode[] = []
  for (let i = from; i < to; i++) {
    const { node, cls } = lines.nodes[i]
    out.push(<span key={i} className={`code-line${cls ? ` ${cls}` : ''}${marked === i + 1 ? ' is-marked' : ''}`} data-n={i + 1} data-testid={marked === i + 1 ? 'workspace-line' : undefined}>{node}{i < last || trailing ? '\n' : ''}</span>)
  }
  return <>{out}</>
}

/** CodeText one line per span (#265): a wrapping viewer numbers each line in its own gutter cell
 *  (CSS, from data-n), so a wrapped line keeps one number and its continuation rows stay blank.
 *  The newlines stay in the text, so copying and textContent read the file as it is. */
export function CodeTextLines({ text, diff, highlighted, tail, marked }: { text: string; diff?: boolean; highlighted?: Token[] | null; tail?: string; marked?: number }) {
  const lines = useMemo(() => lineNodes(text, diff, highlighted, tail), [text, diff, highlighted, tail])
  return <Rows lines={lines} from={0} to={lines.raw.length} marked={marked} trailing={text.endsWith('\n')} />
}
