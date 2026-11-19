import { expect, it, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import QuestionRow from './QuestionRow'
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn(async () => new Response('{}')) }))
it('shows every option before the answer dialog opens', () => {
  render(<MemoryRouter><QuestionRow id="long-question" question={'Choose an approach?\nOption A preserves the draft.\nOption B starts over.'} audience="you"><button>Answer now</button></QuestionRow></MemoryRouter>)
  expect(screen.getByText(/Option B starts over/)).toBeVisible()
  expect(screen.queryByRole('dialog')).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: /Choose an approach/ }))
  expect(screen.getByRole('button', { name: 'Answer now' })).toBeVisible()
})
