import { expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { TicketKey } from './TicketView'

it.each(['', 'https://board.example.test/card/145'])('preserves the full key in a tooltip with URL %s', url => {
  const key = 'BLG-' + '145'.repeat(30)
  render(<TicketKey ticket={{ key, url }} />)
  expect(screen.getByText(key)).toHaveAttribute('title', key)
})
