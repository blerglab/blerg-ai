import { useEffect, useRef } from 'react'
import { Terminal } from '@xterm/xterm'
import { send, onMessage, onOpen } from '../ws'
import type { HistoryDone, SessionOutput } from '../types'
import { historyTheme, renderRow } from '../terminalRenderer'

// Legacy Terminal sessions retain their recorded output. This viewer has no
// input, focus ownership, resize or runtime-scrollback protocol.
export default function TerminalHistory({ sessionId, cols = 220 }: { sessionId: string; cols?: number }) {
  const outputRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const target = outputRef.current
    if (!target) return
    const terminal = new Terminal({ cols, rows: 50, scrollback: 50000, allowProposedApi: true })
    let disposed = false
    let first = true
    let frame: number | null = null
    const render = () => {
      if (disposed) return
      const theme = historyTheme(target)
      const buffer = terminal.buffer.active
      const cell = buffer.getNullCell()
      const fragment = document.createDocumentFragment()
      for (let y = 0; y < buffer.length; y++) {
        fragment.appendChild(renderRow(buffer.getLine(y), terminal.cols, cell, null, theme))
      }
      target.replaceChildren(fragment)
    }
    const scheduleRender = () => {
      if (frame !== null || disposed) return
      frame = requestAnimationFrame(() => { frame = null; render() })
    }
    const observer = new MutationObserver(scheduleRender)
    observer.observe(document.documentElement, {attributes: true, attributeFilter: ['data-theme']})
    const media = window.matchMedia?.('(prefers-color-scheme: light)')
    media?.addEventListener?.('change', scheduleRender)
    const offOutput = onMessage<SessionOutput>('session_output', msg => {
      if (msg.session_id !== sessionId) return
      if (first) { terminal.reset(); first = false }
      const bytes = Uint8Array.from(atob(msg.data), c => c.charCodeAt(0))
      terminal.write(bytes, scheduleRender)
    })
    const offDone = onMessage<HistoryDone>('history_done', msg => {
      if (msg.session_id !== sessionId) return
      if (msg.cols && msg.cols > 0 && msg.cols <= 1000) terminal.resize(msg.cols, terminal.rows)
      terminal.write('', scheduleRender)
    })
    const offOpen = onOpen(() => {
      first = true
      send({ type: 'subscribe_session', session_id: sessionId })
    })
    return () => {
      disposed = true
      if (frame !== null) cancelAnimationFrame(frame)
      observer.disconnect()
      media?.removeEventListener?.('change', scheduleRender)
      offOpen(); offOutput(); offDone()
      terminal.dispose()
      send({ type: 'unsubscribe_session', session_id: sessionId })
    }
  }, [sessionId, cols])
  return <div role="region" aria-label="Terminal history" style={{ overflow: 'auto', flex: 1, minHeight: 0, minWidth: 0, background: 'var(--history-bg, #16181a)', color: 'var(--history-fg, #e8ecef)', fontFamily: 'monospace', fontSize: 13, padding: 6 }}>
    <p>Terminal sessions have been removed. Saved history is read-only.</p>
    <div ref={outputRef} style={{ width: 'max-content', minWidth: '100%' }} />
  </div>
}
