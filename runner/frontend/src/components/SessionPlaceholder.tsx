export default function SessionPlaceholder() {
  return (
    <div
      style={{
        height: '100%',
        background: 'var(--basalt)',
        display: 'flex',
        justifyContent: 'center',
        alignItems: 'center',
      }}
    >
      <span style={{ color: 'var(--fog-dim)', fontSize: '1rem' }}>
        Select a session
      </span>
    </div>
  )
}
