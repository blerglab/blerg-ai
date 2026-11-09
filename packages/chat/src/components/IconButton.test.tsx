import { act, fireEvent, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { IconButton } from './IconButton'

it('names the icon and spins, blocks duplicate activation, then settles', async () => {
  let finish!: () => void
  const click = vi.fn(() => new Promise<void>(resolve => { finish = resolve }))
  render(<IconButton label="Retry models" onClick={click} />)
  const control = screen.getByRole('button', { name: 'Retry models' })
  expect(control).toHaveAttribute('title', 'Retry models')
  expect(control).toHaveTextContent('')
  fireEvent.click(control)
  expect(control).toBeDisabled()
  expect(control).toHaveAttribute('aria-busy', 'true')
  expect(control.querySelector('svg')).toHaveClass('refresh-control-spin')
  fireEvent.click(control)
  expect(click).toHaveBeenCalledTimes(1)
  await act(async () => finish())
  expect(control).toBeEnabled()
  expect(control).toHaveAttribute('aria-busy', 'false')
})
it('retains a visible label when retry is the main action', () => {
  render(<IconButton label="Retry" labeled busy />)
  expect(screen.getByRole('button', { name: 'Retry' })).toHaveTextContent('Retry')
  expect(screen.getByRole('button')).toBeDisabled()
})
