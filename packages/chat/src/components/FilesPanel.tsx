// FilesPanel: the session's files (FilesList) in a dialog over the chat — ChatView's Files button
// opens it.
import { useEffect, useId, useRef } from 'react'
import { createPortal } from 'react-dom'
import { fileCount } from '../model/artifacts'
import { useBackdropClose } from '../hooks/useBackdropClose'
import FilesList, { type FilesListProps } from './FilesList'

interface Props extends FilesListProps {
  onClose: () => void
}

export default function FilesPanel({ onClose, ...list }: Props) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const backdrop = useBackdropClose(onClose)

  useEffect(() => dialogRef.current?.focus(), [])

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onClose()
    }
  }

  return createPortal(
    <div className="artifact-overlay" data-testid="artifacts-overlay" {...backdrop}>
      <div
        ref={dialogRef}
        className={`artifact-dialog artifact-panel${list.items.length === 0 ? ' is-empty' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        data-testid="artifacts-panel"
        onKeyDown={onKeyDown}
      >
        <header className="artifact-header">
          <div className="artifact-header-text">
            <h2 id={titleId}>Files <span className="artifact-count">{fileCount(list.items)}</span></h2>
            <p className="artifact-meta">Published by the agent, or attached by you.</p>
          </div>
          <button type="button" className="artifact-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        <div className="artifact-body"><FilesList {...list} /></div>
      </div>
    </div>,
    document.body,
  )
}
