// Text Claude Code writes into the user role itself: a background task's completion, a system
// reminder, a notice it labels as not user input. The daemon records such a message as source
// "system", but a pod built before it learned to, and rows recorded then, say "chat"; the chat
// recognises the text as well so it is never drawn as the person's.
const MARKERS = ['<task-notification>', '<system-reminder>', '[SYSTEM NOTIFICATION', '<command-name>']

export function isHarnessText(text: string): boolean {
  const s = text.trimStart()
  return MARKERS.some(m => s.startsWith(m))
}
