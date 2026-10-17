// The inside of a code block: highlighted tokens drawn as React spans (never markup), a diff's
// lines coloured by their first character, or plain text.
import type { ReactNode } from 'react'
import type { Token } from '../model/highlight'

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

export default function CodeText({ text, diff, highlighted }: { text: string; diff?: boolean; highlighted?: Token[] | null }) {
  if (diff) {
    return (
      <>
        {text.split('\n').map((line, i, all) => (
          <span key={i} className={diffClass(line)}>{line}{i < all.length - 1 ? '\n' : ''}</span>
        ))}
      </>
    )
  }
  if (highlighted) return <>{tokens(highlighted)}</>
  return <>{text}</>
}
