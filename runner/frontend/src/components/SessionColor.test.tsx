import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import { SessionColorPicker } from './SessionColor'
import { SESSION_COLORS } from '../lib/sessionColors'
import { apiFetch } from '../apiFetch'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
const open = () => fireEvent.click(screen.getByRole('button', { name: /Session color:/ }))
describe('session colors', () => {
 it('lets its containing dialog handle Tab', () => {
  const tab = vi.fn()
  render(<div onKeyDown={event => { if (event.key === 'Tab') tab() }}><SessionColorPicker sessionId="s" color="blue" /></div>)
  open()
  fireEvent.keyDown(screen.getByRole('radio',{name:'Blue'}),{key:'Tab'})
  expect(tab).toHaveBeenCalledOnce()
 })
 it('starts with one compact swatch and opens thirteen named radios with one tab stop', () => {
  render(<SessionColorPicker sessionId="s" color="blue" />)
  expect(screen.queryByRole('radiogroup')).toBeNull()
  open()
  expect(screen.getAllByRole('radio').map(r => r.getAttribute('aria-label'))).toEqual(SESSION_COLORS.map(c=>c.name))
  expect(screen.getAllByRole('radio')).toHaveLength(13)
  expect(screen.getByRole('radio', {name:'Blue'})).toBeChecked()
  expect(screen.getByRole('radio', {name:'Blue'})).toHaveFocus()
  expect(screen.getAllByRole('radio').filter(r => r.tabIndex === 0)).toHaveLength(1)
  fireEvent.keyDown(screen.getByRole('radio',{name:'Blue'}),{key:'Escape'})
  expect(screen.queryByRole('radiogroup')).toBeNull()
  expect(screen.getByRole('button',{name:'Session color: Blue'})).toHaveFocus()
 })
 it('saves stable keys with arrow navigation, wrapping to None', async () => {
  vi.mocked(apiFetch).mockResolvedValue({ok:true} as Response)
  render(<SessionColorPicker sessionId="s" color={SESSION_COLORS.at(-1)!.key} />)
  open()
  fireEvent.keyDown(screen.getByRole('radio',{name:SESSION_COLORS.at(-1)!.name}),{key:'ArrowRight'})
  expect(screen.getByRole('radio',{name:'None'})).toHaveFocus()
  await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s',expect.objectContaining({method:'PATCH',body:JSON.stringify({color:''})})))
 })
 it('shows a refused change inside the open palette', async () => {
  vi.mocked(apiFetch).mockResolvedValue({ok:false} as Response)
  render(<SessionColorPicker sessionId="s" color="blue" />)
  open()
  fireEvent.click(screen.getByRole('radio',{name:'None'}))
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not set color')
 })
})
