import { useEffect } from 'react'
import { resetKeptAttachments } from '@blerglab/chat'
import { getAccessToken } from '../authClient'
import { connected } from '../ws'
import { SIGN_IN_GRACE_MS, markSignInStuck, retrySignIn, useSignInState } from '../signIn'

/**
 * SignInBanner — one line at the top of the runner when no token can be obtained: the refresh
 * bounce was already spent (signIn.ts), or ten seconds after load there is still no token and no
 * socket. Without it the runner showed no sessions and no daemons and said nothing. While it shows,
 * signIn.ts keeps trying quietly and takes it down when a token arrives; Retry starts the refresh
 * guard over and goes for a token at once. When core says the sign-in itself has ended it reads
 * "Your sign-in has ended · Sign in" and stops trying; the click is the same full redirect, which
 * lands on the login form. The board's twin is board/web/src/SignInBanner.tsx.
 */
export default function SignInBanner() {
  const state = useSignInState()
  useEffect(() => {
    const t = setTimeout(() => {
      if (!connected() && !getAccessToken()) markSignInStuck()
    }, SIGN_IN_GRACE_MS)
    return () => clearTimeout(t)
  }, [])
  // A sign-in that has ended takes the files waiting on this device with it (#231 review, 7).
  useEffect(() => {
    if (state === 'ended') void resetKeptAttachments()
  }, [state])
  if (state === 'ok') return null
  const ended = state === 'ended'
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
      <span>{ended ? 'Your sign-in has ended' : "Couldn't sign you in"}</span>
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
        {ended ? 'Sign in' : 'Retry'}
      </button>
    </div>
  )
}
