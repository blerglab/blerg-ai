import { langFromInfo, langFromName } from '../model/codeLang'
import { HIGHLIGHT_MAX_CHARS } from '../model/highlight'
import { countLines } from '../model/lineCap'
import { wrapKind } from '../model/lineWrap'
import CodeLines from './CodeLines'
import { DIFF_FILE_LINES } from './DiffView'

// Numbered, highlighted lines (the code viewer's look); the named line is marked and scrolled to.
export default function SourceCode({ text, name, line, view }: { text: string; name: string; line?: number; view?: string }) {
  const lang = langFromName(name) ?? langFromInfo(view)
  // CodeText colors diff lines itself, so the token highlighter's cap does not protect this path.
  const plainDiff = lang === 'diff' && (text.length > HIGHLIGHT_MAX_CHARS || countLines(text) > DIFF_FILE_LINES)
  return (
    <>
      {plainDiff && <p className="artifact-status">Large diff shown as plain text to keep scrolling responsive.</p>}
      <CodeLines text={text} lang={lang} kind={wrapKind(name)} diff={lang === 'diff' && !plainDiff} line={line} scrollTestId="workspace-code" codeTestId={null} codeClassName="workspace-code" showLang />
    </>
  )
}
