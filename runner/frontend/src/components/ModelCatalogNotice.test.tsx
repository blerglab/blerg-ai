import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import ModelCatalogNotice from './ModelCatalogNotice'
import { coreOrigin } from '../authClient'

it('offers a direct reconnect action only when the server requires login', () => {
  const state = { models: [], loading: false, fallback: false, engineEfforts: [], error: 'Connection needs renewal', reconnect: true }
  const view = render(<ModelCatalogNotice state={state} />)
  expect(screen.getByRole('link', { name: 'Reconnect in Settings' })).toHaveAttribute('href', `${coreOrigin()}/settings#managed-connections`)
  view.rerender(<ModelCatalogNotice state={{ ...state, reconnect: false }} />)
  expect(screen.queryByRole('link')).toBeNull()
})
