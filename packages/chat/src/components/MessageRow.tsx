import type { ReactNode } from 'react'

/** The message surface shared by the transcript and question conversations. */
export default function MessageRow({ author, className = '', eventId, children }: { author: 'user' | 'assistant'; className?: string; eventId?: string; children: ReactNode }) {
  return <div className={`agent-card ${author}${className ? ` ${className}` : ''}`} data-event-id={eventId}>{children}</div>
}
