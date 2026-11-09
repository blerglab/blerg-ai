import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { codeFilename, downloadText, tableCsv, tableMarkdown } from '../model/contentExport'
import { PrintAction, ViewerAction } from './ViewerToolbar'

type ContentActionsProps = {
  content: () => HTMLElement | null
  text?: string
  name?: string
  markdown?: boolean
  table?: boolean
}

/** The same controls and error path for inline content, modal viewers, and hosted tabs. */
export function ContentActions({ content, text, name, markdown = false, table = false }: ContentActionsProps) {
  const [error, setError] = useState('')
  function run(action: () => void | Promise<void>) {
    setError('')
    void Promise.resolve().then(action).catch(() => {
      console.warn('content_export_failed', { cause: 'browser_export_rejected' })
      setError('Could not export this content. Try again.')
    })
  }
  return <>
    <PrintAction content={content} />
    {table && <ViewerAction icon="download" label="Download CSV" text="CSV" onClick={() => run(() => downloadText(tableCsv(content()!.querySelector('table')!), 'table.csv', 'text/csv'))} />}
    <ViewerAction icon="download" label={table || markdown ? 'Download Markdown' : 'Download'} text={table ? 'MD' : undefined} onClick={() => run(() => downloadText(table ? tableMarkdown(content()!.querySelector('table')!) : text ?? '', table ? 'table.md' : name ?? 'document.txt', table || markdown ? 'text/markdown' : 'text/plain'))} />
    {error && <span role="alert" className="artifact-error">{error}</span>}
  </>
}

function InlineContentActions({ kind, ...props }: ContentActionsProps & { kind: 'Table' | 'Code' }) {
  const [state, setState] = useState<'unbuilt' | 'open' | 'closed'>('unbuilt')
  const root = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const id = useId()
  const open = state === 'open'
  useEffect(() => {
    if (!open) return
    const outside = (event: PointerEvent) => { if (event.target instanceof Node && !root.current?.contains(event.target)) setState('closed') }
    document.addEventListener('pointerdown', outside)
    return () => document.removeEventListener('pointerdown', outside)
  }, [open])
  return <div ref={root} className="md-export-actions" onKeyDown={event => {
    if (event.key !== 'Escape' || !open) return
    event.preventDefault(); event.stopPropagation(); setState('closed'); trigger.current?.focus()
  }}>
    <button ref={trigger} type="button" className="refresh-control viewer-action inline-export-trigger" aria-label={`${kind} actions`} title={`${kind} actions`} aria-expanded={open} aria-controls={state === 'unbuilt' ? undefined : id} onClick={() => setState(previous => previous === 'open' ? 'closed' : 'open')}>⋯</button>
    {state !== 'unbuilt' && <div id={id} className="inline-export-controls" role="toolbar" aria-label={`${kind} export actions`} hidden={!open}><ContentActions {...props} /></div>}
  </div>
}

export function ExportTable({ children, align }: { children?: ReactNode; align?: 'left' | 'center' | 'right' }) {
  const root = useRef<HTMLDivElement>(null)
  return <div ref={root} className={`md-table-wrap${align === 'center' ? ' is-centered' : ''}`}>
    <InlineContentActions kind="Table" table content={() => root.current} />
    <table align={align}>{children}</table>
  </div>
}
export function CodeActions({ text, lang, content, inline = false }: { text: string; lang: string | null; content: () => HTMLElement | null; inline?: boolean }) {
  return inline ? <InlineContentActions kind="Code" text={text} name={codeFilename(lang)} content={content} /> : <ContentActions text={text} name={codeFilename(lang)} content={content} />
}
