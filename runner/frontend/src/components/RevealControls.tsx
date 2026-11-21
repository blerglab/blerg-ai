import { createPortal } from 'react-dom'
import { useEffect, useEffectEvent, useState } from 'react'
import { onMessage, onClose } from '../ws'
import { signedInAccount } from '../lib/liveBoardCard'
import { canFollowReveal, UI_REVEAL, UI_REVEAL_APPLY, validReveal, type UIReveal } from '../lib/uiReveal'

// Follow is opt-in for this mounted session. A switch or reload clears it.
export default function RevealControls({ sessionId, onAttachment, enabled = true, host }: { sessionId: string; onAttachment(id: string): void; enabled?: boolean; host?: HTMLElement | null }) {
 const [follow, setFollow] = useState(false)
 const [status, setStatus] = useState('')
 const following = useEffectEvent(() => enabled && follow)
 useEffect(() => {
  const account = signedInAccount()
  const selected = () => location.pathname === `/sessions/${encodeURIComponent(sessionId)}` && signedInAccount() === account
  const apply = (request: UIReveal) => {
   if (request.action === 'attachment') onAttachment(request.target)
   else window.dispatchEvent(new CustomEvent(UI_REVEAL_APPLY, { detail: { ...request, session: sessionId, account } }))
   setStatus(request.action === 'attachment' ? 'Attachment shown' : `Shown: ${request.target}`)
  }
  const click = (event: Event) => {
   const request = (event as CustomEvent).detail
   if (!event.defaultPrevented && signedInAccount() === account && request?.session === sessionId && validReveal(request)) { event.preventDefault(); apply(request) }
  }
  window.addEventListener(UI_REVEAL, click, true)
  const stop = onClose(() => setFollow(false))
  const unsubscribe = onMessage('agent_event', ev => {
   if (ev.type !== 'agent_event' || ev.session_id !== sessionId || ev.kind !== 'ui_reveal' || ev.replayed || !validReveal(ev.payload)) return
   if (following() && selected() && canFollowReveal()) apply(ev.payload)
   else setStatus(ev.payload.action === 'attachment' ? 'Attachment suggested in chat' : `Suggested in chat: ${ev.payload.target}`)
  })
  return () => { window.removeEventListener(UI_REVEAL, click, true); unsubscribe(); stop() }
 }, [sessionId, onAttachment])
 if (!enabled) return null
 const controls = <div className="reveal-controls"><label><input data-reveal-follow="" type="checkbox" checked={follow} onChange={event => setFollow(event.target.checked)} /> Follow the agent in this workspace</label>{status && <span role="status"> · {status}</span>}</div>
 return host ? createPortal(controls, host) : null
}
