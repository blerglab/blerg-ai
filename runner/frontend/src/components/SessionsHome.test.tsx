import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import SessionsHome from './SessionsHome'

describe('SessionsHome', () => {
  it('tells the person nothing is selected and offers to start a session', () => {
    const onNew = vi.fn()
    render(<SessionsHome onNewSession={onNew} />)
    expect(screen.getByText(/no session selected/i)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /new session/i }))
    expect(onNew).toHaveBeenCalledTimes(1)
  })
})
