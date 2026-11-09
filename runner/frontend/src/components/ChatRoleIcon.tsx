/** Main and channels share the bubble; every worker uses the same diamond. */
export default function ChatRoleIcon({ worker = false, size = 16 }: { worker?: boolean; size?: number }) {
  return <span aria-hidden="true" data-chat-role={worker ? 'worker' : 'channel'} style={{ display: 'inline-flex', flexShrink: 0, width: size, height: size, alignItems: 'center', justifyContent: 'center', lineHeight: 1, fontSize: size, fontStyle: 'normal' }}>{worker ? '◇' : <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor"><path d="M3 2h10v8H6l-3 3z" /></svg>}</span>
}
