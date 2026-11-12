import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import ThemeToggle from './ThemeToggle'
const state = vi.hoisted(() => ({ request: vi.fn(), token: 'fixture' as string | null }))
vi.mock('./apiFetch', () => ({ apiFetch: state.request }))
vi.mock('./authClient', () => ({ getAccessToken: () => state.token }))
beforeEach(() => {
 const values = new Map<string,string>()
 vi.stubGlobal('localStorage', {getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key,value), removeItem: (key: string) => values.delete(key), clear: () => values.clear()})
})
afterEach(() => { cleanup(); document.documentElement.removeAttribute('data-theme'); localStorage.clear(); state.request.mockReset(); state.token = 'fixture'; vi.unstubAllGlobals() })
it('reads the account choice and saves a selection through the human preference endpoint', async () => {
 state.request.mockResolvedValue(new Response('{"theme":"colorblind-light"}'))
 render(<ThemeToggle />)
 await waitFor(() => expect(screen.getByRole('combobox', {name:'Theme'})).toHaveValue('colorblind-light'))
 state.request.mockResolvedValue(new Response('{"theme":"high-contrast"}'))
 fireEvent.change(screen.getByRole('combobox'), {target: {value:'high-contrast'}})
 await waitFor(() => expect(document.documentElement.dataset.theme).toBe('high-contrast'))
 expect(state.request).toHaveBeenLastCalledWith('/api/me/theme', expect.objectContaining({method:'PUT', body:'{"theme":"high-contrast"}'}))
})
it('keeps the prior choice and reports a failed save', async () => {
 state.request.mockResolvedValue(new Response('{"theme":"dark"}'))
 render(<ThemeToggle />)
 await waitFor(() => expect(document.documentElement.dataset.theme).toBe('dark'))
 state.request.mockResolvedValue(new Response('{}',{status:503}))
 fireEvent.change(screen.getByRole('combobox'), {target:{value:'high-contrast'}})
 expect(await screen.findByRole('alert')).toHaveTextContent('Could not save theme')
 expect(document.documentElement.dataset.theme).toBe('dark')
})
it('seeds an unset account from the local pin', async () => {
 document.documentElement.dataset.theme='high-contrast'
 localStorage.setItem('blerg-theme','high-contrast')
 state.request.mockResolvedValue(new Response('{"theme":""}'))
 render(<ThemeToggle />)
 await waitFor(() => expect(state.request).toHaveBeenCalledWith('/api/me/theme', expect.objectContaining({method:'PUT'})))
 expect(document.documentElement.dataset.theme).toBe('high-contrast')
})

it('refreshes the account choice when the app regains focus', async () => {
 state.request.mockResolvedValue(new Response('{"theme":"dark"}'))
 render(<ThemeToggle />)
 await waitFor(() => expect(document.documentElement.dataset.theme).toBe('dark'))
 state.request.mockResolvedValue(new Response('{"theme":"colorblind-dark"}'))
 fireEvent.focus(window)
 await waitFor(() => expect(document.documentElement.dataset.theme).toBe('colorblind-dark'))
})
it('lets signed-out pages choose a local appearance without an auth redirect', async () => {
 state.token=null
 render(<ThemeToggle />)
 fireEvent.change(screen.getByRole('combobox'), {target:{value:'colorblind-dark'}})
 await waitFor(() => expect(document.documentElement.dataset.theme).toBe('colorblind-dark'))
 expect(state.request).not.toHaveBeenCalled()
})
