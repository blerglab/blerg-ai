// SourcePane: the markdown source as a plain textarea with a line-number gutter. The value is the
// edited copy (ReviewMode keeps it); "Discard edits" puts the published text back.
import { useRef } from 'react'

interface Props {
  value: string
  published: string
  onChange: (value: string) => void
  onDiscard: () => void
}

export default function SourcePane({ value, published, onChange, onDiscard }: Props) {
  const gutterRef = useRef<HTMLDivElement | null>(null)
  const lines = value.split('\n').length
  const changed = value !== published

  return (
    <div className="review-source">
      <div className="review-source-bar">
        <span className="review-pane-title">Source</span>
        {changed && (
          <button type="button" className="artifact-link" onClick={onDiscard}>Discard edits</button>
        )}
      </div>
      <div className="review-editor">
        <div className="review-gutter" data-testid="review-gutter" aria-hidden="true" ref={gutterRef}>
          {Array.from({ length: lines }, (_, i) => <span key={i}>{i + 1}</span>)}
        </div>
        <textarea
          className="review-textarea"
          aria-label="Source"
          value={value}
          spellCheck={false}
          onChange={e => onChange(e.target.value)}
          onScroll={e => {
            if (gutterRef.current) gutterRef.current.style.transform = `translateY(-${e.currentTarget.scrollTop}px)`
          }}
        />
      </div>
    </div>
  )
}
