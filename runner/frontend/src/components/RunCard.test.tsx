// RunCard's click guard: a click anywhere on the card does what its title does, inside the phone's
// drawer (a role="dialog" ancestor) too; a control inside the card, or a click that comes out of a
// dialog the card portaled elsewhere, does not.
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { createPortal } from 'react-dom'
import RunCard, { RunStep } from './RunCard'

function card(onActivate: () => void, extra?: React.ReactNode) {
  return (
    <ul>
      <RunCard testId="card" state={<span />} title="Survey the store" tooltip="Survey the store" action="Show in conversation" onActivate={onActivate}
        elapsed={<span data-testid="clock">01:00</span>} step={<RunStep text="running Read · a.go" full="running Read · a.go (whole)" testId="step" />}
        controls={<button type="button" data-testid="own">Dismiss</button>} after={extra} />
    </ul>
  )
}

describe('RunCard', () => {
  it('a click on the card body jumps when the card sits inside the phone drawer (a dialog)', () => {
    const go = vi.fn()
    render(<div role="dialog" aria-label="Session panel">{card(go)}</div>)
    fireEvent.click(screen.getByTestId('clock'))
    expect(go).toHaveBeenCalledOnce()
  })

  it('its own controls and its step line do not jump; the step shows its whole text', () => {
    const go = vi.fn()
    render(card(go))
    fireEvent.click(screen.getByTestId('own'))
    const step = screen.getByTestId('step')
    fireEvent.click(step)
    expect(go).not.toHaveBeenCalled()
    expect(step).toHaveAttribute('aria-expanded', 'true')
    expect(step).toHaveTextContent('running Read · a.go (whole)')
  })

  it('a click inside a dialog the card portaled out of itself does not jump', () => {
    const go = vi.fn()
    function Portaled() {
      return createPortal(<div role="dialog" aria-label="Discard"><p data-testid="inside">Discard it?</p></div>, document.body)
    }
    render(card(go, <Portaled />))
    fireEvent.click(screen.getByTestId('inside'))
    expect(go).not.toHaveBeenCalled()
  })
})
