// What the main pane shows on a wide screen when no session is selected: the list is already in the
// sidebar, so this points at it and offers the other way in.

interface Props {
  onNewSession: () => void
}

export default function SessionsHome({ onNewSession }: Props) {
  return (
    <div
      data-testid="sessions-home"
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        gap: 14,
        height: '100%',
        minHeight: 320,
        padding: '0 24px',
        textAlign: 'center',
        color: 'var(--fog)',
      }}
    >
      <h1
        style={{
          margin: 0,
          fontFamily: 'var(--display)',
          fontSize: '1.05rem',
          fontWeight: 600,
          letterSpacing: '0.14em',
          textTransform: 'uppercase',
          color: 'var(--chalk)',
        }}
      >
        No session selected
      </h1>
      <p style={{ margin: 0, maxWidth: 380, lineHeight: 1.5, fontSize: '0.92rem' }}>
        Pick a session from the list to read it, or start a new one.
      </p>
      <button
        type="button"
        onClick={onNewSession}
        style={{
          background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
          color: 'var(--amber)',
          border: '1px solid var(--blaze)',
          borderRadius: 6,
          padding: '8px 18px',
          fontWeight: 600,
          cursor: 'pointer',
          fontSize: '0.95rem',
          letterSpacing: '0.05em',
        }}
      >
        + New session
      </button>
    </div>
  )
}
