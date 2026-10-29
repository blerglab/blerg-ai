import { expect, it } from 'vitest'
import { tabDragSpeed, tabScrollDelta, tabWheelDelta } from './tabStrip'
it('reveals only clipped edges and leaves a visible tab in place', () => {
  const viewport = { left: 100, right: 400 }
  expect(tabScrollDelta(viewport, { left: 80, right: 200 })).toBe(-20)
  expect(tabScrollDelta(viewport, { left: 300, right: 450 })).toBe(50)
  expect(tabScrollDelta(viewport, { left: 120, right: 390 })).toBe(0)
})
it('normalizes wheel axes and units while preserving zoom', () => {
  const wheel = { deltaX: 0, deltaY: 3, deltaMode: 0, ctrlKey: false }
  expect(tabWheelDelta(wheel, 300)).toBe(3)
  expect(tabWheelDelta({ ...wheel, deltaX: .4, deltaY: 30 }, 300)).toBe(30)
  expect(tabWheelDelta({ ...wheel, deltaX: -.4, deltaY: -30 }, 300)).toBe(-30)
  expect(tabWheelDelta({ ...wheel, deltaX: 30, deltaY: -.4 }, 300)).toBe(30)
  expect(tabWheelDelta({ ...wheel, deltaX: -7 }, 300)).toBe(-7)
  expect(tabWheelDelta({ ...wheel, deltaMode: 1 }, 300)).toBe(48)
  expect(tabWheelDelta({ ...wheel, deltaY: 1, deltaMode: 2 }, 300)).toBe(300)
  expect(tabWheelDelta({ ...wheel, ctrlKey: true }, 300)).toBe(0)
})
it('scrolls only at drag edges and scales down near their inner boundary', () => {
  expect(tabDragSpeed(100, 100, 500)).toBe(-900)
  expect(tabDragSpeed(500, 100, 500)).toBe(900)
  expect(tabDragSpeed(120, 100, 500)).toBe(-450)
  expect(tabDragSpeed(300, 100, 500)).toBe(0)
  expect(tabDragSpeed(0, 0, 0)).toBe(0)
})
