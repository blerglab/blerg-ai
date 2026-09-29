import { usePreview } from '../hooks/usePreview'

export function PreviewTab() {
  const { html } = usePreview()

  if (!html) {
    return (
      <div style={{
        display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center',
        height: '100%', color: 'var(--fog)', gap: 8
      }}>
        <span style={{ fontSize: '2rem' }}>📄</span>
        <span>No preview yet</span>
        <span style={{ fontSize: 12, color: 'var(--fog-dim)' }}>Push HTML via POST /api/preview</span>
      </div>
    )
  }

  return (
    <iframe
      srcDoc={html}
      style={{ width: '100%', height: '100%', border: 'none', background: 'white' }}
      sandbox="allow-scripts allow-same-origin"
      title="Preview"
    />
  )
}
