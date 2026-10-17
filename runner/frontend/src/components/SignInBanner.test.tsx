import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'

const { redirectToRefresh, getAccessToken, connected, renewSilently, startTokenRenewal } = vi.hoisted(() => ({
  redirectToRefresh: vi.fn(),
  getAccessToken: vi.fn<() => string | null>(),
  connected: vi.fn<() => boolean>(),
  renewSilently: vi.fn<() => Promise<'token' | 'signed-out' | 'no-answer'>>(),
  startTokenRenewal: vi.fn(),
}))

vi.mock('../authClient', () => ({ redirectToRefresh, getAccessToken, renewSilently, startTokenRenewal }))
vi.mock('../ws', () => ({ connected }))

import SignInBanner from './SignInBanner'
import { SIGN_IN_GRACE_MS, bounceToRefresh, clearSignInStuck, markSignInStuck } from '../signIn'
import { MAX_ATTEMPTS, markRefreshAttempted, refreshAlreadyAttempted, refreshReturnUrl } from '../refreshGuard'

describe('SignInBanner', () => {
  beforeEach(() => {
    redirectToRefresh.mockClear()
    getAccessToken.mockReturnValue(null)
    connected.mockReturnValue(false)
    renewSilently.mockReset()
    renewSilently.mockResolvedValue('no-answer')
    sessionStorage.clear()
    act(() => clearSignInStuck())
  })

  afterEach(() => {
    act(() => clearSignInStuck())
    vi.useRealTimers()
  })

  it('shows nothing while signing in is still possible', () => {
    render(<SignInBanner />)
    expect(screen.queryByTestId('sign-in-banner')).not.toBeInTheDocument()
  })

  it('appears when a bounce is refused by the guard, and Retry clears both keys and bounces again', () => {
    for (let i = 0; i < MAX_ATTEMPTS; i++) markRefreshAttempted()
    render(<SignInBanner />)

    act(() => {
      expect(bounceToRefresh()).toBe(false)
    })
    expect(redirectToRefresh).not.toHaveBeenCalled()
    expect(screen.getByTestId('sign-in-banner')).toHaveTextContent("Couldn't sign you in")

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(redirectToRefresh).toHaveBeenCalledTimes(1)
    expect(redirectToRefresh).toHaveBeenCalledWith(refreshReturnUrl())
    // The retry is itself one counted attempt on a fresh window, so its return leg stays bounded.
    expect(refreshAlreadyAttempted()).toBe(true)
    expect(JSON.parse(sessionStorage.getItem('blerg_refresh_attempts')!).n).toBe(1)
  })

  it('appears when, ten seconds after load, there is no token and no socket', () => {
    vi.useFakeTimers()
    render(<SignInBanner />)
    act(() => {
      vi.advanceTimersByTime(SIGN_IN_GRACE_MS - 1)
    })
    expect(screen.queryByTestId('sign-in-banner')).not.toBeInTheDocument()
    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(screen.getByTestId('sign-in-banner')).toBeInTheDocument()
  })

  it('stays hidden after ten seconds when the socket is open', () => {
    vi.useFakeTimers()
    connected.mockReturnValue(true)
    getAccessToken.mockReturnValue('tok')
    render(<SignInBanner />)
    act(() => {
      vi.advanceTimersByTime(SIGN_IN_GRACE_MS)
    })
    expect(screen.queryByTestId('sign-in-banner')).not.toBeInTheDocument()
  })

  it('goes away when told the socket opened', () => {
    render(<SignInBanner />)
    act(() => markSignInStuck())
    expect(screen.getByTestId('sign-in-banner')).toBeInTheDocument()
    act(() => clearSignInStuck())
    expect(screen.queryByTestId('sign-in-banner')).not.toBeInTheDocument()
  })

  it('goes away by itself when a quiet retry gets a token after the network comes back', async () => {
    vi.useFakeTimers()
    render(<SignInBanner />)
    act(() => markSignInStuck())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000) // the first timed attempt: still offline
    })
    expect(renewSilently).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('sign-in-banner')).toHaveTextContent("Couldn't sign you in")
    renewSilently.mockResolvedValueOnce('token')
    await act(async () => {
      window.dispatchEvent(new Event('online'))
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(renewSilently).toHaveBeenCalledTimes(2)
    expect(screen.queryByTestId('sign-in-banner')).not.toBeInTheDocument()
    expect(redirectToRefresh).not.toHaveBeenCalled()
  })

  it('says the sign-in has ended, stops trying, and Sign in does the full redirect', async () => {
    vi.useFakeTimers()
    render(<SignInBanner />)
    renewSilently.mockResolvedValueOnce('signed-out')
    act(() => markSignInStuck())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000)
    })
    expect(screen.getByTestId('sign-in-banner')).toHaveTextContent('Your sign-in has ended')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600_000)
    })
    expect(renewSilently).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(redirectToRefresh).toHaveBeenCalledWith(refreshReturnUrl())
  })
})
