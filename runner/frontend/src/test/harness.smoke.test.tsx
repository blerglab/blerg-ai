import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import * as wsMock from './wsMock'

vi.mock('../ws', () => import('./wsMock'))

describe('test harness', () => {
  beforeEach(() => wsMock.resetWsMock())

  it('renders a component and applies jest-dom matchers', () => {
    render(<div>hello harness</div>)
    expect(screen.getByText('hello harness')).toBeInTheDocument()
  })

  it('shares the mocked ws bus with the importing test', async () => {
    const ws = await import('../ws')
    expect(ws.send).toBe(wsMock.send)
  })
})
