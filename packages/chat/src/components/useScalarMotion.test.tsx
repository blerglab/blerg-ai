import {act, render} from '@testing-library/react'
import {afterEach, expect, it, vi} from 'vitest'
import {useScalarMotion} from './useScalarMotion'

function Scalar({n}: {n: number | null}) {
  const ref = useScalarMotion(n)
  return <span ref={ref} style={{'--chat-data-transition-ms': 180} as React.CSSProperties} />
}
afterEach(() => {vi.useRealTimers(); vi.unstubAllGlobals()})
it('animates the retained value node and cancels work on unmount', () => {
  vi.useFakeTimers()
  vi.stubGlobal('matchMedia', () => ({matches: false}))
  const view = render(<Scalar n={40} />)
  const node = view.container.firstElementChild!
  expect(node).toHaveTextContent('40')
  view.rerender(<Scalar n={75} />)
  expect(view.container.firstElementChild).toBe(node)
  act(() => vi.advanceTimersByTime(80))
  expect(Number(node.textContent)).toBeGreaterThan(40)
  expect(Number(node.textContent)).toBeLessThan(75)
  const intermediate = node.textContent
  view.rerender(<Scalar n={75} />)
  expect(node.textContent).toBe(intermediate)
  act(() => vi.advanceTimersByTime(200))
  expect(node).toHaveTextContent('75')
  view.unmount()
  expect(vi.getTimerCount()).toBe(0)
})
it('schedules no motion frames under reduced motion, including unknown', () => {
  vi.stubGlobal('matchMedia', () => ({matches: true}))
  const frame = vi.spyOn(window, 'requestAnimationFrame')
  const view = render(<Scalar n={0} />)
  const node = view.container.firstElementChild!
  view.rerender(<Scalar n={75} />)
  expect(node).toHaveTextContent('75')
  expect(frame).not.toHaveBeenCalled()
  view.rerender(<Scalar n={null} />)
  expect(node).toHaveTextContent('Unknown')
  frame.mockRestore()
})
