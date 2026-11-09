import {expect, it} from 'vitest'
import {analyzeChartImage, type ChartFrameSample} from './chartFrameWitness'

it('correlates a delayed compositor image with its painted layout, and still detects a blank chart', async () => {
  const canvas = document.createElement('canvas'); canvas.width = 400; canvas.height = 300
  const context = canvas.getContext('2d')!
  context.fillStyle = '#000'; context.fillRect(0, 0, 400, 300)
  // Painted batch 1. Batch 2 has already measured the new breakpoint below it.
  for (let bit = 0; bit < 32; bit++) {
    context.fillStyle = (((1 + 0x5aa5 * 65536) >>> bit) & 1) ? '#fff' : '#000'
    context.fillRect(bit * 4, 0, 4, 8)
  }
  context.fillStyle = '#fff'
  const samples: ChartFrameSample[] = [
    {frame: 1, timestamp: 1, state: 'view', stage: 'drag', hosted: true},
    {frame: 1, timestamp: 1, state: 'exact', stage: 'drag', hosted: true, surface: 1, rect: [20, 20, 100, 80]},
    {frame: 2, timestamp: 2, state: 'view', stage: 'drag', hosted: true},
    {frame: 2, timestamp: 2, state: 'exact', stage: 'drag', hosted: true, surface: 1, rect: [20, 150, 200, 80]},
  ]
  context.fillRect(40, 40, 40, 40)
  const analyze = () => analyzeChartImage({jpeg: canvas.toDataURL('image/jpeg').split(',')[1], samples})
  expect(await analyze()).toEqual({frame: 1, analyzed: true, blank: false})
  context.fillStyle = '#000'; context.fillRect(40, 40, 40, 40)
  expect(await analyze()).toEqual({frame: 1, analyzed: true, blank: true})
})
