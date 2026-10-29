import { useRef, useState, type RefObject } from 'react'
import { ConfirmPopover } from '@blerglab/chat'

/** Button failures use the same anchored surface as confirmations. Callers own retry behavior. */
export default function ActionError({ title, message, anchor, onRetry, onDismiss, testId }: { title: string; message: string; anchor?: RefObject<HTMLElement | null>; onRetry(): void; onDismiss(): void; testId?: string }) {
 const [trigger] = useState(() => document.activeElement instanceof HTMLElement ? document.activeElement : null)
 const fallback = useRef(trigger)
 return <ConfirmPopover anchor={anchor ?? fallback} title={title} description={message} confirmLabel="Retry" cancelLabel="Dismiss" danger={false} onConfirm={onRetry} onCancel={onDismiss} testId={testId} />
}
