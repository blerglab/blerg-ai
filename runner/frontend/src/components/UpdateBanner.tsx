import { useSessionStore } from '../hooks/useSessionStore'

/**
 * UpdateBanner — shown when the server this tab is connected to is no longer the version the
 * page was loaded from. A tab left open across a deploy keeps running the app it loaded: it
 * shows pages and controls the new server no longer has, and lacks what was added. Nothing is
 * reloaded behind the person's back (they may be in the middle of typing); they are told, and
 * choose when.
 */
export default function UpdateBanner() {
  const updated = useSessionStore(s => s.updated)
  if (!updated) return null
  return (
    <div
      role="status"
      data-testid="update-banner"
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        gap: 12,
        padding: '6px 12px',
        background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
        borderBottom: '1px solid var(--blaze)',
        color: 'var(--chalk)',
        fontSize: 13,
        flexShrink: 0,
      }}
    >
      <span>Blerg was updated. This tab is still showing the old version.</span>
      <button
        type="button"
        onClick={() => window.location.reload()}
        style={{
          padding: '3px 10px',
          background: 'var(--blaze)',
          border: 'none',
          borderRadius: 6,
          color: 'var(--basalt)',
          fontFamily: 'inherit',
          fontSize: 13,
          fontWeight: 600,
          cursor: 'pointer',
        }}
      >
        Reload
      </button>
    </div>
  )
}
