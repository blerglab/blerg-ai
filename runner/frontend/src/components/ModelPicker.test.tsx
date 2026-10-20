import { fireEvent, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import ModelPicker from './ModelPicker'
const id = 'custom:local/nvidia-nemotron-3.5-lightning-30b-a3b-nvfp4'
it('shows concise model names and sends the unchanged qualified routing ID', () => {
  const onModelChange = vi.fn()
  render(<ModelPicker models={[{ id, name: id, description: 'Local model', section: 'main', efforts: [] }]} model="" onModelChange={onModelChange} labelStyle={{}} noteStyle={{}} pillStyle={() => ({})} />)
  const button = screen.getByRole('button', { name: 'Nemotron Lightning' })
  expect(button).toHaveAttribute('title', `${id} — Local model`)
  fireEvent.click(button)
  expect(onModelChange).toHaveBeenCalledWith(id)
})
it('keeps equal family models distinguishable in the picker', () => {
  const other = id.replace('30b-a3b-nvfp4', '49b-q8')
  render(<ModelPicker models={[id, other].map(id => ({ id, name: id, description: '', section: 'main', efforts: [] }))} model={id} onModelChange={() => {}} labelStyle={{}} noteStyle={{}} pillStyle={() => ({})} />)
  expect(screen.getByRole('button', { name: /30B/ })).toHaveAttribute('aria-pressed', 'true')
  expect(screen.getByRole('button', { name: /49B/ })).toHaveAttribute('aria-pressed', 'false')
})
