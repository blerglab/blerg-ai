import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import ThemeToggle from './ThemeToggle'
const state = vi.hoisted(() => ({ request: vi.fn(), token: 'fixture' as string | null }))
vi.mock('../apiFetch', () => ({ apiFetch: state.request }))
vi.mock('../authClient', () => ({ getAccessToken: () => state.token }))
afterEach(() => { document.documentElement.removeAttribute('data-theme'); localStorage.clear(); state.request.mockReset(); state.token = 'fixture' })
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
it('keeps keyboard focus while saving and follows the OS after choosing system', async () => {
 state.request.mockResolvedValue(new Response('{"theme":"dark"}'))
 render(<ThemeToggle />)
 await waitFor(() => expect(document.documentElement.dataset.theme).toBe('dark'))
 const select=screen.getByRole('combobox',{name:'Theme'})
 select.focus()
 fireEvent.change(select,{target:{value:'system'}})
 expect(select).toHaveFocus()
 expect(select).not.toBeDisabled()
 await waitFor(() => expect(document.documentElement.hasAttribute('data-theme')).toBe(false))
 expect(localStorage.getItem('blerg-theme')).toBe('system')
})
it('seeds a signed-out choice when signing in to an unset account', async () => {
 state.token=null
 render(<ThemeToggle />)
 fireEvent.change(screen.getByRole('combobox'),{target:{value:'colorblind-dark'}})
 await waitFor(() => expect(localStorage.getItem('blerg-theme')).toBe('colorblind-dark'))
 state.token='fixture'
 state.request.mockResolvedValue(new Response('{"theme":""}'))
 fireEvent.focus(window)
 await waitFor(() => expect(state.request).toHaveBeenCalledWith('/api/me/theme',expect.objectContaining({method:'PUT',body:'{"theme":"colorblind-dark"}'})))
})
it('ignores an account read that finishes after a newer selection', async () => {
 let finish!: (response:Response) => void
 state.request.mockImplementation((_url:string, init?:RequestInit) => init?.method==='PUT' ? Promise.resolve(new Response('{}')) : new Promise<Response>(resolve => {finish=resolve}))
 render(<ThemeToggle />)
 fireEvent.change(screen.getByRole('combobox'),{target:{value:'high-contrast'}})
 await waitFor(() => expect(document.documentElement.dataset.theme).toBe('high-contrast'))
 finish(new Response('{"theme":"light"}'))
 await waitFor(() => expect(screen.getByRole('combobox')).toHaveValue('high-contrast'))
 expect(document.documentElement.dataset.theme).toBe('high-contrast')
})

it('labels an unpinned device Follow system and previews keyboard arrows without saving', async () => {
 state.token=null
 render(<ThemeToggle />)
 const select=screen.getByRole('combobox',{name:'Theme'})
 expect(select).toHaveValue('system')
 fireEvent.keyDown(select,{key:'ArrowDown'})
 expect(select).toHaveValue('dark')
 expect(localStorage.getItem('blerg-theme')).toBeNull()
 fireEvent.keyDown(select,{key:'ArrowDown'})
 expect(select).toHaveValue('light')
 fireEvent.keyDown(select,{key:'Escape'})
 expect(select).toHaveValue('system')
 fireEvent.keyDown(select,{key:'End'})
 fireEvent.keyDown(select,{key:'Enter'})
 await waitFor(()=>expect(localStorage.getItem('blerg-theme')).toBe('tokyo-night'))
})
it('serializes rapid choices while an earlier save is pending', async () => {
 const saved:string[]=[]
 let finish!:()=>void
 state.request.mockImplementation(async (_url:string, init?:RequestInit)=>{
  if(!init?.method) return new Response('{"theme":"dark"}')
  saved.push(JSON.parse(init.body as string).theme)
  if(saved.length===1) await new Promise<void>(resolve=>{finish=resolve})
  return new Response('{}')
 })
 render(<ThemeToggle />)
 await waitFor(()=>expect(document.documentElement.dataset.theme).toBe('dark'))
 const select=screen.getByRole('combobox')
 fireEvent.change(select,{target:{value:'light'}})
 await waitFor(()=>expect(saved).toEqual(['light']))
 fireEvent.change(select,{target:{value:'colorblind-dark'}})
 fireEvent.change(select,{target:{value:'high-contrast'}})
 finish()
 await waitFor(()=>expect(document.documentElement.dataset.theme).toBe('high-contrast'))
 expect(saved).toEqual(['light','colorblind-dark','high-contrast'])
})
it('serializes seeding before a selection and prevents focus reads from repainting it', async () => {
 localStorage.setItem('blerg-theme','light')
 let finish!:()=>void
 const saved:string[]=[]
 state.request.mockImplementation(async (_url:string,init?:RequestInit)=>{
  if(!init?.method) return new Response('{"theme":""}')
  saved.push(JSON.parse(init.body as string).theme)
  if(saved.length===1) await new Promise<void>(resolve=>{finish=resolve})
  return new Response('{}')
 })
 render(<ThemeToggle />)
 await waitFor(()=>expect(saved).toEqual(['light']))
 fireEvent.change(screen.getByRole('combobox'),{target:{value:'high-contrast'}})
 fireEvent.focus(window)
 expect(saved).toEqual(['light'])
 finish()
 await waitFor(()=>expect(document.documentElement.dataset.theme).toBe('high-contrast'))
 expect(saved).toEqual(['light','high-contrast'])
})
it('does not seed another account from the previous account cache', async () => {
 localStorage.setItem('blerg-theme','high-contrast')
 localStorage.setItem('blerg-theme-account','previous-person')
 state.token='header.'+btoa(JSON.stringify({sub:'next-person'}))+'.signature'
 state.request.mockResolvedValue(new Response('{"theme":""}'))
 render(<ThemeToggle />)
 await waitFor(()=>expect(localStorage.getItem('blerg-theme-account')).toBe('next-person'))
 expect(localStorage.getItem('blerg-theme')).toBeNull()
 expect(screen.getByRole('combobox')).toHaveValue('system')
 expect(state.request.mock.calls.every(([,init])=>!init?.method)).toBe(true)
})
it('resyncs after silent credential renewal without waiting for focus', async () => {
 state.request.mockResolvedValue(new Response('{"theme":"dark"}'))
 render(<ThemeToggle />)
 await waitFor(()=>expect(document.documentElement.dataset.theme).toBe('dark'))
 state.request.mockResolvedValue(new Response('{"theme":"light"}'))
 fireEvent(window,new Event('blerg-auth-refreshed'))
 await waitFor(()=>expect(document.documentElement.dataset.theme).toBe('light'))
})
