// The pop-out button beside Copy on a long message: opens the message, as it reads right now, in
// its own workspace tab through the same Markdown viewer. Hidden entirely (not just disabled)
// until the message's text runs past the person's chosen number of rendered lines (more than 4 by
// default, set in Settings), and it re-measures live as the card or the pane resizes.
import { useEffect, useRef, useState } from 'react'
import { useViewerHost } from './ViewerHost'
import { useChatPaneRef } from './chatPaneContext'
import { watchPaneCard } from '../model/paneObserver'
import { popOutLines, renderedLines, usePopOutChoice } from '../model/popOutPref'

const HEADING_RE = /^#{1,6}[ \t]+(.+?)[ \t]*#{0,6}[ \t]*$/m
// A fenced code block, backtick or tilde, 3 or more, with up to 3 leading spaces (CommonMark):
// its content is never a real heading, even when a `#` line sits inside it.
const FENCE_RE = /^ {0,3}(`{3,}|~{3,})[^\n]*\n[\s\S]*?^ {0,3}\1[^\n]*$/gm

/** The line-level markdown a heading commonly carries, unwrapped to plain text: inline code,
 *  bold, italic, strikethrough and link text. A tab name is a plain label, not a rendering. */
function stripMarkdown(s: string): string {
  return s
    .replace(/`([^`]+)`/g, '$1')
    .replace(/\*\*([^*]+)\*\*/g, '$1')
    .replace(/__([^_]+)__/g, '$1')
    .replace(/~~([^~]+)~~/g, '$1')
    .replace(/\*([^*]+)\*/g, '$1')
    .replace(/(?<![a-zA-Z0-9])_([^_]+)_(?![a-zA-Z0-9])/g, '$1')
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
    .trim()
}

/** The tab's name: the message's first Markdown heading outside any fenced code block, with its
 *  own inline markdown stripped to plain text, else the host's fallback ("<channel> · <time>",
 *  computed by the caller so this component need not know the clock). */
export function popOutLabel(text: string, fallback: string): string {
  const m = HEADING_RE.exec(text.replace(FENCE_RE, fenced => fenced.replace(/[^\n]/g, ' ')))
  const heading = m?.[1] ? stripMarkdown(m[1]) : ''
  return heading || fallback
}

export default function PopOutMessage({ text, fallbackLabel, messageId, workspaceLinks }: { text: string; fallbackLabel: string; messageId: string; workspaceLinks?: boolean }) {
  const host = useViewerHost()
  const paneRef = useChatPaneRef()
  const anchor = useRef<HTMLSpanElement>(null)
  const [show, setShow] = useState(false)
  const lines = popOutLines(usePopOutChoice())
  useEffect(() => {
    const pane = paneRef?.current
    const card = anchor.current?.closest<HTMLElement>('.agent-card')
    if (!pane || !card) return
    // The shared observer already re-checks on its own whenever the card's size changes —
    // streaming text growing the card included — so this effect need not (and must not) tear
    // down and recreate a subscription on every chunk: `text` is deliberately not a dependency.
    // The message's own text: the card's first child that isn't its author line. Feet, copy and
    // this button never count toward its length.
    const body = [...card.children].find((el): el is HTMLElement => el instanceof HTMLElement && !el.classList.contains('card-author'))
    if (!body) return
    return watchPaneCard(pane, card, () => setShow(lines === 0 || renderedLines(body) > lines))
  }, [paneRef, lines])
  if (!host) return null
  return (
    <>
      <span ref={anchor} aria-hidden="true" data-nocopy="" className="msg-popout-anchor" />
      {show && (
        <button
          type="button"
          className="msg-popout"
          aria-label="Open in a tab"
          title="Open in a tab"
          data-nocopy=""
          data-testid="popout-message"
          onClick={() => host.open({ kind: 'message', messageId, text, label: popOutLabel(text, fallbackLabel), workspaceLinks })}
        >
          <span className="msg-popout-icon" aria-hidden="true" />
        </button>
      )}
    </>
  )
}
