import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import ShellLimitsSection from './ShellLimitsSection'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const respond = (status: number, body: unknown = {}) =>
  Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) } as Response)
const defaults = { desktop: { detached_seconds: 0, idle_seconds: 0 }, cluster: { detached_seconds: 300, idle_seconds: 1800 } }
type Stored = { detached_seconds: number | null; idle_seconds: number | null }
const unset = { desktop: { detached_seconds: null, idle_seconds: null }, cluster: { detached_seconds: null, idle_seconds: null } }
const reply = (over: Partial<typeof defaults> = {}, stored: { desktop: Stored; cluster: Stored } = unset) => ({ ...defaults, ...over, defaults, stored, min_seconds: 600, max_seconds: 87600 * 3600 })

beforeEach(() => apiFetch.mockReset())

describe('ShellLimitsSection', () => {
  it('stays hidden for a person who is not an administrator', async () => {
    apiFetch.mockReturnValue(respond(403, { error: 'missing capability: account.manage' }))
    const view = render(<ShellLimitsSection />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/shells/settings'))
    await waitFor(() => expect(view.container).toBeEmptyDOMElement())
  })

  it('shows each limit per runtime, with desktop until the session ends and the sleep caveat', async () => {
    apiFetch.mockReturnValue(respond(200, reply()))
    render(<ShellLimitsSection />)
    const desktopDetached = await screen.findByRole('combobox', { name: 'Desktop: close a shell with no browser attached after' })
    expect(desktopDetached).toHaveValue('default')
    expect(desktopDetached).toHaveDisplayValue('Default (until the session ends)')
    expect(screen.getByRole('combobox', { name: 'Desktop: close a shell with no typing after' })).toHaveValue('default')
    expect(screen.getByRole('combobox', { name: 'Cluster: close a shell with no browser attached after' })).toHaveDisplayValue('Default (5 minutes)')
    expect(screen.getByRole('combobox', { name: 'Cluster: close a shell with no typing after' })).toHaveDisplayValue('Default (30 minutes)')
    expect(screen.getByText(/a desktop shell still ends when the computer sleeps or loses its network connection/)).toBeInTheDocument()
    expect(screen.getByText(/keeps at least 10 minutes/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save shell limits' })).toBeDisabled()
  })

  it('refuses under 10 minutes and over 87,600 hours, and saves only what changed', async () => {
    apiFetch.mockReturnValueOnce(respond(200, reply()))
    render(<ShellLimitsSection />)
    const mode = await screen.findByRole('combobox', { name: 'Desktop: close a shell with no browser attached after' })
    fireEvent.change(mode, { target: { value: 'after' } })
    const amount = screen.getByRole('spinbutton', { name: 'Desktop: close a shell with no browser attached after: amount' })
    const unit = screen.getByRole('combobox', { name: 'Desktop: close a shell with no browser attached after: unit' })
    const save = screen.getByRole('button', { name: 'Save shell limits' })
    fireEvent.change(unit, { target: { value: 'minutes' } })
    fireEvent.change(amount, { target: { value: '9' } })
    expect(screen.getByRole('alert')).toHaveTextContent('The shortest limit is 10 minutes.')
    expect(save).toBeDisabled()
    fireEvent.change(unit, { target: { value: 'hours' } })
    fireEvent.change(amount, { target: { value: '87601' } })
    expect(screen.getByRole('alert')).toHaveTextContent('The longest limit is 87,600 hours.')
    expect(save).toBeDisabled()
    fireEvent.change(amount, { target: { value: '8' } })
    expect(screen.queryByRole('alert')).toBeNull()
    // The cluster's idle limit becomes "until the session ends".
    fireEvent.change(screen.getByRole('combobox', { name: 'Cluster: close a shell with no typing after' }), { target: { value: 'session' } })
    apiFetch.mockReturnValueOnce(respond(200, reply({ desktop: { detached_seconds: 8 * 3600, idle_seconds: 0 }, cluster: { detached_seconds: 300, idle_seconds: 0 } })))
    fireEvent.click(save)
    await screen.findByText('Saved. Running shells use the new limits.')
    const [path, init] = apiFetch.mock.calls[1] as [string, RequestInit]
    expect(path).toBe('/api/shells/settings')
    expect(init.method).toBe('PUT')
    expect(JSON.parse(init.body as string)).toEqual({ desktop: { detached_seconds: 28800 }, cluster: { idle_seconds: 0 } })
    expect(save).toBeDisabled()
  })

  it('shows the server refusal', async () => {
    apiFetch.mockReturnValueOnce(respond(200, reply()))
    render(<ShellLimitsSection />)
    fireEvent.change(await screen.findByRole('combobox', { name: 'Desktop: close a shell with no typing after' }), { target: { value: 'after' } })
    apiFetch.mockReturnValueOnce(respond(403, { error: 'missing capability: account.manage' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save shell limits' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Only an administrator can change this.')
  })
})

describe('ShellLimitsSection defaults and exact values', () => {
  it('restores a runtime default with null, including the five-minute cluster grace', async () => {
    apiFetch.mockReturnValueOnce(respond(200, reply({ cluster: { detached_seconds: 3600, idle_seconds: 1800 } }, { desktop: { detached_seconds: null, idle_seconds: null }, cluster: { detached_seconds: 3600, idle_seconds: null } })))
    render(<ShellLimitsSection />)
    const mode = await screen.findByRole('combobox', { name: 'Cluster: close a shell with no browser attached after' })
    expect(mode).toHaveValue('after')
    fireEvent.change(mode, { target: { value: 'default' } })
    apiFetch.mockReturnValueOnce(respond(200, reply()))
    fireEvent.click(screen.getByRole('button', { name: 'Save shell limits' }))
    await screen.findByText('Saved. Running shells use the new limits.')
    expect(JSON.parse((apiFetch.mock.calls[1] as [string, RequestInit])[1].body as string)).toEqual({ cluster: { detached_seconds: null } })
    expect(mode).toHaveDisplayValue('Default (5 minutes)')
  })

  it('never rewrites an untouched override that is not whole minutes', async () => {
    const stored = { desktop: { detached_seconds: 601, idle_seconds: 1801 }, cluster: { detached_seconds: 630, idle_seconds: null } }
    apiFetch.mockReturnValueOnce(respond(200, reply({ desktop: { detached_seconds: 601, idle_seconds: 1801 }, cluster: { detached_seconds: 630, idle_seconds: 1800 } }, stored)))
    render(<ShellLimitsSection />)
    const save = await screen.findByRole('button', { name: 'Save shell limits' })
    expect(save).toBeDisabled()
    expect(screen.queryByRole('alert')).toBeNull()
    fireEvent.change(screen.getByRole('combobox', { name: 'Cluster: close a shell with no typing after' }), { target: { value: 'session' } })
    apiFetch.mockReturnValueOnce(respond(200, reply()))
    fireEvent.click(save)
    await screen.findByText('Saved. Running shells use the new limits.')
    expect(JSON.parse((apiFetch.mock.calls[1] as [string, RequestInit])[1].body as string)).toEqual({ cluster: { idle_seconds: 0 } })
  })
})
