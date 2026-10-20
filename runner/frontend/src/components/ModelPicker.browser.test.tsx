import { expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import ModelPicker from './ModelPicker'
import '../index.css'
const ids = ['nvidia-nemotron-3.5-lightning-30b-a3b-nvfp4', 'custom:local-vision/NVIDIA-Nemotron-3.5-Super-VL-120B-A12B-NVFP4']
it.each([390, 1280])('keeps concise model choices visible and keyboard selectable at %ipx', async width => {
  await page.viewport(width, 600)
  const change = vi.fn()
  render(<main style={{ width: '100%', maxWidth: 480, padding: 16, boxSizing: 'border-box' }}><ModelPicker models={ids.map(id => ({ id, name: id, description: '', section: 'main', efforts: ['medium', 'high'] }))} model={ids[0]} effort="medium" onModelChange={change} labelStyle={{}} noteStyle={{}} pillStyle={() => ({ padding: 8 })} /></main>)
  const lightning = screen.getByRole('button', { name: 'Nemotron Lightning' })
  const superVL = screen.getByRole('button', { name: 'Nemotron Super VL' })
  expect(lightning).toHaveAttribute('title', ids[0])
  expect(superVL).toHaveAttribute('title', ids[1])
  superVL.focus(); await userEvent.keyboard('{Enter}')
  expect(change).toHaveBeenCalledWith(ids[1])
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  for (const button of [lightning, superVL]) expect(button.getBoundingClientRect().right).toBeLessThanOrEqual(width)
})
