import { useEffect } from 'react'
import { getAccessToken } from '../authClient'
import { connected } from '../ws'
import { SIGN_IN_GRACE_MS, markSignInStuck, retrySignIn, useSignInStuck } from '../signIn'

/**
 * SignInBanner — one line at the top of the runner when no token can be obtained: the refresh
 * bounce was already spent (signIn.ts), or ten seconds after load there is still no token and no
 * socket. Without it the runner showed no sessions and no daemons and said nothing. Retry starts
 * the refresh guard over and goes for a token again. The board's twin is board/web/src/SignInBanner.tsx.
 */
export default function SignInBanner() {
  const stuck = useSignInStuck()
  useEffect(() => {
    const t = setTimeout(() => {
      if (!connected() && !getAccessToken()) markSignInStuck()
    }, SIGN_IN_GRACE_MS)
    return () => clearTimeout(t)
  }, [])
  if (!stuck) return null
  return (
    <div
      role="alert"
      data-testid="sign-in-banner"
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        gap: 8,
        padding: '6px 12px',
        background: 'color-mix(in srgb, var(--blaze) 22%, var(--basalt))',
        borderBottom: '1px solid var(--blaze)',
        color: 'var(--chalk)',
        fontSize: 13,
        flexShrink: 0,
      }}
    >
      <span>Couldn't sign you in</span>
      <span aria-hidden="true">·</span>
      <button
        type="button"
        onClick={retrySignIn}
        style={{
          padding: 0,
          background: 'none',
          border: 'none',
          color: 'var(--blaze)',
          fontFamily: 'inherit',
          fontSize: 13,
          fontWeight: 600,
          cursor: 'pointer',
          textDecoration: 'underline',
        }}
      >
        Retry
      </button>
    </div>
  )
}
