import { useState } from 'react'
import { effectiveTheme, toggleTheme, type Theme } from '../theme'

// A compact sun/moon switch for the header. Shows the theme you'd switch TO.
export default function ThemeToggle() {
  const [theme, setThemeState] = useState<Theme>(effectiveTheme)

  const goDark = theme === 'light' // clicking switches to the other one
  return (
    <button
      type="button"
      onClick={() => setThemeState(toggleTheme())}
      title={goDark ? 'Switch to dark' : 'Switch to light'}
      aria-label={goDark ? 'Switch to dark theme' : 'Switch to light theme'}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        width: 28,
        height: 28,
        borderRadius: 6,
        border: '1px solid var(--stone)',
        background: 'transparent',
        color: 'var(--fog)',
        cursor: 'pointer',
        flexShrink: 0,
        lineHeight: 0,
      }}
    >
      {goDark ? (
        // moon
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>
      ) : (
        // sun
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>
      )}
    </button>
  )
}
