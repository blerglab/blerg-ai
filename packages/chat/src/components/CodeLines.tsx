// Numbered lines of a file in the code viewer's look: a gutter and the text, coloured by language
// (the start of a big file; the rest plain, see useHighlightHead). The text is drawn as React
// spans, never markup. The one renderer behind every code and plain-text view (the raw file, a
// published code or text file, an expanded code block), so line wrap (#265) is the same in all:
// wrapped, each line is a row numbered once. A file over 5,000 lines does not wrap.
import { useLayoutEffect, useRef, type CSSProperties } from 'react'
import { useHighlightHead } from '../model/highlight'
import { langLabel, type CodeLang } from '../model/codeLang'
import { countLines } from '../model/lineCap'
import CodeText, { CodeTextLines } from './CodeText'
import { useCodeWrap, type WrapKind } from '../model/lineWrap'

export default function CodeLines({ text, lang, kind, diff = false, line, scrollTestId, codeTestId = 'artifact-text', codeClassName = '', showLang = false }: {
  text: string
  lang: CodeLang | null
  /** Prose wraps by default, code does not (lineWrap.ts). */
  kind: WrapKind
  /** Colour a patch's lines by their first character. */
  diff?: boolean
  /** The line to mark and bring into view (1-based). */
  line?: number
  scrollTestId?: string
  codeTestId?: string | null
  codeClassName?: string
  showLang?: boolean
}) {
  const { tokens, tail } = useHighlightHead(lang, text)
  const lines = countLines(text)
  const at = line && line >= 1 && line <= lines ? line : undefined
  const wrap = useCodeWrap(kind, lines)
  const scroller = useRef<HTMLDivElement | null>(null)
  const code = useRef<HTMLPreElement | null>(null)
  // Unwrapped, line N sits where the stylesheet puts it (10px padding, 1.5 line height), so the band
  // is drawn by CSS from the number. Wrapped, earlier lines may take several rows: the marked row is
  // measured.
  useLayoutEffect(() => {
    const box = scroller.current
    const pre = code.current
    if (!at || !box || !pre) return
    if (wrap) {
      const row = pre.querySelector<HTMLElement>('.code-line.is-marked')
      if (row) box.scrollTop = Math.max(0, row.getBoundingClientRect().top - box.getBoundingClientRect().top + box.scrollTop - box.clientHeight / 3)
      return
    }
    const cs = getComputedStyle(pre)
    const lh = parseFloat(cs.lineHeight) || (parseFloat(cs.fontSize) || 13) * 1.5
    box.scrollTop = Math.max(0, (parseFloat(cs.paddingTop) || 0) + (at - 1) * lh - box.clientHeight / 3)
  }, [at, text, wrap])
  return (
    <div className={`code-viewer-scroll${wrap ? ' is-wrapped' : ''}`} data-testid={scrollTestId} data-wrap={wrap ? 'on' : 'off'} ref={scroller} style={{ '--chat-code-gutter': `${String(lines).length}ch` } as CSSProperties}>
      {!wrap && <pre className="code-viewer-gutter" aria-hidden="true">{Array.from({ length: lines }, (_, i) => i + 1).join('\n')}</pre>}
      <pre className={`code-viewer-code${codeClassName ? ` ${codeClassName}` : ''}`} ref={code} data-testid={codeTestId ?? undefined} data-lang={showLang && lang ? langLabel(lang) : undefined}>
        {at && !wrap && <span className="workspace-line-band" data-testid="workspace-line" style={{ '--chat-ws-line': at } as CSSProperties} />}
        <code>{wrap
          ? <CodeTextLines diff={diff} text={text} highlighted={tokens} tail={tail} marked={at} />
          : <CodeText diff={diff} text={text} highlighted={tokens} tail={tail} />}</code>
      </pre>
    </div>
  )
}
