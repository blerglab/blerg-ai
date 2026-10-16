import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import * as wsMock from '../test/wsMock'
import UpdateBanner from './UpdateBanner'
import { useSessionStore } from '../hooks/useSessionStore'

vi.mock('../ws', () => import('../test/wsMock'))

function initialState(version: string, commit?: string) {
  act(() => wsMock.emit({ type: 'initial_state', daemons: [], sessions: [], server_version: version, server_commit: commit }))
}

describe('UpdateBanner', () => {
  beforeEach(() => {
    useSessionStore.setState({ serverVersion: '', loadedVersion: '', updated: false })
  })

  it('says nothing while the server is the version the page was loaded from', () => {
    render(<UpdateBanner />)
    initialState('abc123')
    initialState('abc123') // a plain reconnect
    expect(screen.queryByTestId('update-banner')).toBeNull()
  })

  it('asks for a reload once a reconnect reports another version, and keeps asking', () => {
    render(<UpdateBanner />)
    initialState('abc123')
    initialState('def456') // the server was deployed under this tab
    expect(screen.getByTestId('update-banner')).toHaveTextContent('Blerg was updated')
    expect(screen.getByRole('button', { name: 'Reload' })).toBeInTheDocument()
    expect(useSessionStore.getState().loadedVersion).toBe('abc123@')
    initialState('def456')
    expect(screen.getByTestId('update-banner')).toBeInTheDocument()
  })

  it('asks for a reload when only the commit changes (a desktop rebuild keeps the version "dev")', () => {
    render(<UpdateBanner />)
    initialState('dev', 'aaa1111')
    initialState('dev', 'aaa1111')
    expect(screen.queryByTestId('update-banner')).toBeNull()
    initialState('dev', 'bbb2222')
    expect(screen.getByTestId('update-banner')).toHaveTextContent('Blerg was updated')
  })

  it('does not take a server that reports no version for an update', () => {
    render(<UpdateBanner />)
    initialState('abc123')
    initialState('')
    expect(screen.queryByTestId('update-banner')).toBeNull()
  })
})
