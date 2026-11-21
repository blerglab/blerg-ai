// Attribute browser event-handler errors and rejected background work to the current case.
// Vitest's default browser listener otherwise reports these outside the test result.
// The browser reports a ResizeObserver whose callback changed layout and was re-delivered on
// the next frame. That is a scheduling notice, not a fault, and every browser test harness
// ignores it; real errors still fail their case.
const benignBrowserNotices = [
  /^ResizeObserver loop completed with undelivered notifications\.?$/,
  /^ResizeObserver loop limit exceeded\.?$/,
]

export function isBenignBrowserNotice(message: string): boolean {
  return benignBrowserNotices.some(pattern => pattern.test(message.trim()))
}

export function collectBrowserErrors(target: EventTarget, log: (message: string, cause: unknown) => void) {
  const pending: Error[] = []
  const record = (event: Event) => {
    const cause: unknown = event.type === 'error'
      ? (event as ErrorEvent).error ?? (event as ErrorEvent).message
      : (event as PromiseRejectionEvent).reason
    const error = cause instanceof Error ? cause : new Error(String(cause))
    if (isBenignBrowserNotice(error.message)) { event.preventDefault(); return }
    pending.push(error)
    event.preventDefault()
    log('Unhandled browser error', { cause: error.name })
  }
  target.addEventListener('error', record)
  target.addEventListener('unhandledrejection', record)
  return {
    assertEmpty() {
      const errors = pending.splice(0)
      if (errors.length) throw new AggregateError(errors, `${errors.length} unhandled browser error(s): ${errors.map(error => error.message).join('; ')}`)
    },
    dispose() {
      target.removeEventListener('error', record)
      target.removeEventListener('unhandledrejection', record)
    },
  }
}
