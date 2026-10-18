// CodeViewer: a block of code or pasted text in hand, shown large: its language, line numbers,
// Copy. Unlike ArtifactViewer it needs no file, session or transport, so it works wherever a
// block can be drawn (the board's proxied chat included). It reuses the viewer's frame and CSS.
import { useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useBackdropClose } from '../hooks/useBackdropClose'
import { useSelectAllIn } from '../hooks/useSelectAllIn'
import { langLabel, type CodeLang } from '../model/codeLang'
import { useHighlight } from '../model/highlight'
import { countLines } from '../model/lineCap'
import CodeText from './CodeText'

export default function CodeViewer({ text, lang, title, onClose }: {
  text: string
  lang: CodeLang | null
  title?: string
  onClose: () => void
}) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  useSelectAllIn(dialogRef)
  const backdrop = useBackdropClose(onClose)
  const [copied, setCopied] = useState(false)
  const highlighted = useHighlight(lang, text, true)
  const lines = countLines(text)
  const canCopy = typeof navigator !== 'undefined' && !!navigator.clipboard
  useEffect(() => dialogRef.current?.focus(), [])

  return createPortal(
    <div className="artifact-overlay artifact-overlay-viewer" data-testid="code-viewer-overlay" {...backdrop}>
      <div
        ref={dialogRef}
        className="artifact-dialog artifact-viewer"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={e => { if (e.key === 'Escape') { e.stopPropagation(); onClose() } }}
      >
        <header className="artifact-header">
          <div className="artifact-header-text">
            <h2 id={titleId}>{title ?? langLabel(lang)}</h2>
            <p className="artifact-meta">{[title ? langLabel(lang) : null, `${lines} ${lines === 1 ? 'line' : 'lines'}`].filter(Boolean).join(' · ')}</p>
          </div>
          {canCopy && (
            <button
              type="button"
              className="artifact-btn"
              onClick={() => {
                void navigator.clipboard.writeText(text).then(
                  () => { setCopied(true); setTimeout(() => setCopied(false), 1200) },
                  () => {},
                )
              }}
            >
              {copied ? 'Copied' : 'Copy'}
            </button>
          )}
          <button type="button" className="artifact-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        <div className="artifact-body">
          <div className="code-viewer-scroll" data-testid="code-viewer-text">
            <pre className="code-viewer-gutter" aria-hidden="true">{Array.from({ length: lines }, (_, i) => i + 1).join('\n')}</pre>
            <pre className="code-viewer-code"><code><CodeText text={text} diff={lang === 'diff'} highlighted={highlighted} /></code></pre>
          </div>
        </div>
      </div>
    </div>,
    document.body,
  )
}
