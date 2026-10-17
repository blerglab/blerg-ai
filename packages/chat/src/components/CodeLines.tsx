// Numbered lines of a file in the code viewer's look: a gutter and the text, coloured by language
// (the start of a big file; the rest plain, see useHighlightHead). The text is drawn as React
// spans, never markup. The testid sits on the code so the viewer's text can be read from it.
import { useHighlightHead } from '../model/highlight'
import type { CodeLang } from '../model/codeLang'
import { countLines } from '../model/lineCap'
import CodeText from './CodeText'

export default function CodeLines({ text, lang }: { text: string; lang: CodeLang | null }) {
  const { tokens, tail } = useHighlightHead(lang, text)
  const lines = countLines(text)
  return (
    <div className="code-viewer-scroll">
      <pre className="code-viewer-gutter" aria-hidden="true">{Array.from({ length: lines }, (_, i) => i + 1).join('\n')}</pre>
      <pre className="code-viewer-code" data-testid="artifact-text"><code><CodeText text={text} highlighted={tokens} tail={tail} /></code></pre>
    </div>
  )
}
