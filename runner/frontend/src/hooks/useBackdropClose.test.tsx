import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { useBackdropClose } from './useBackdropClose'

function Modal({ onClose }: { onClose: () => void }) {
  const backdrop = useBackdropClose(onClose)
  return (
    <div data-testid="backdrop" {...backdrop}>
      <div data-testid="panel"><input data-testid="field" /></div>
    </div>
  )
}

describe('useBackdropClose', () => {
  it('closes on a press and release on the backdrop itself', () => {
    const onClose = vi.fn()
    render(<Modal onClose={onClose} />)
    fireEvent.pointerDown(screen.getByTestId('backdrop'))
    fireEvent.click(screen.getByTestId('backdrop'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('does not close when the press started inside the panel', () => {
    const onClose = vi.fn()
    render(<Modal onClose={onClose} />)
    fireEvent.pointerDown(screen.getByTestId('field'))
    fireEvent.click(screen.getByTestId('backdrop'))
    expect(onClose).not.toHaveBeenCalled()
  })

  it('does not close on a click with no press on the backdrop, or on a click inside the panel', () => {
    const onClose = vi.fn()
    render(<Modal onClose={onClose} />)
    fireEvent.click(screen.getByTestId('backdrop'))
    fireEvent.pointerDown(screen.getByTestId('backdrop'))
    fireEvent.click(screen.getByTestId('panel'))
    // The stale backdrop press is consumed by that click, not carried forward.
    fireEvent.click(screen.getByTestId('backdrop'))
    expect(onClose).not.toHaveBeenCalled()
  })
})
