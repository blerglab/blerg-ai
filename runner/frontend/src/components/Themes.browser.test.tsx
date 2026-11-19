import { afterEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import ThemeToggle from './ThemeToggle'
import TerminalHistory from './TerminalHistory'
import * as wsMock from '../test/wsMock'
import UsageMeters from './UsageMeters'
import { makeDaemon } from '../test/fixtures'
import { shellTheme } from '../lib/shellTheme'
import { currentCodeMode, applyCodeColours, DEFAULT_CODE_COLOURS } from '@blerglab/chat'
import { readVisualTheme, mermaidTheme, vegaTheme } from '../../../../packages/chat/src/visual/theme'
import { THEMES } from '../../../../packages/settings/theme'
import './ActivityInsights.css'
import './SessionInsights.css'
import '../index.css'
import '@blerglab/chat/blerg.css'
const request = vi.hoisted(() => vi.fn())
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({apiFetch: request}))
vi.mock('../authClient', () => ({getAccessToken: () => 'fixture'}))
afterEach(() => { document.documentElement.removeAttribute('data-theme'); document.documentElement.removeAttribute('style'); localStorage.clear() })
// The only themes brand/tokens.css gives --meter-pattern-*/--activity-pattern-* real values — a
// bundled scheme stays at the base 'none', same as the defaults.
const ACCESSIBILITY_PRESETS = ['colorblind-dark', 'colorblind-light', 'high-contrast']
it.each([390, 3840])('applies all five presets to chat, terminal, chart and Mermaid at %i px', async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 request.mockImplementation(async (_url: string, init?: RequestInit) => new Response(init?.body as string || '{"theme":"dark"}'))
 render(<div style={{width: '100%', maxWidth: 300}}><ThemeToggle /><TerminalHistory sessionId="legacy" cols={20} /><UsageMeters now={new Date('2026-01-01')} daemons={[makeDaemon({usage_limits:[{provider:'codex',windows:[{id:'5h',used_percent:80},{id:'7d',used_percent:90},{id:'month',used_percent:95}]}]})]} /></div>)
 act(() => wsMock.emit({type:'session_output',session_id:'legacy',seq:1,data:btoa('\x1b[31mSaved\x1b[0m')}))
 await screen.findByText('Saved')
 const select = screen.getByRole('combobox', {name:'Theme'})
 await waitFor(() => expect(document.documentElement.dataset.theme).toBe('dark'))
 for (const {id} of THEMES.filter(theme => theme.id !== 'system')) {
  fireEvent.change(select, {target:{value:id}})
  await waitFor(() => expect(document.documentElement.dataset.theme).toBe(id))
  await waitFor(() => expect(select).not.toBeDisabled())
  applyCodeColours(document.documentElement,DEFAULT_CODE_COLOURS,currentCodeMode())
  const styles=getComputedStyle(document.documentElement)
  // A bundled scheme's own light/dark call, not an id-naming guess (catppuccin-latte is light but
  // doesn't end in "light") — brand/tokens.css's own `color-scheme` for this theme is the truth.
  expect(currentCodeMode()).toBe(styles.colorScheme as 'light' | 'dark')
  const probe=document.createElement('span'); probe.style.color=styles.getPropertyValue('--history-ansi-1').trim()
  await waitFor(() => expect(screen.getByText('Saved').style.color).toBe(probe.style.color))
  const visual=readVisualTheme(document.body)
  expect(visual.surface).toBe(styles.getPropertyValue('--scree').trim())
  expect(vegaTheme(visual).range.category).toEqual(visual.palette)
  // Every theme's chart palette: its ordered --chart-* series tokens, then the accent (BLG-507).
  const hex=(name:string)=>{const p=document.createElement('span');p.style.color=`var(${name})`;document.body.append(p);const [r,g,b]=getComputedStyle(p).color.match(/\d+/g)!.map(Number);p.remove();return '#'+[r,g,b].map(v=>v.toString(16).padStart(2,'0')).join('')}
  const expected=[...new Set([...['tests','build','git','read','edit','other','model','waiting'].map(n=>hex(`--chart-${n}`)),hex('--chat-accent')])]
  expect(visual.palette).toEqual(expected)
  expect(vegaTheme(visual).range.category).toEqual(expected)
  expect(mermaidTheme(visual).themeVariables.textColor).toBe(visual.fg)
  expect(shellTheme(document.body).theme.green).toBe(styles.getPropertyValue('--terminal-green').trim())
  expect(styles.getPropertyValue('--chat-code-keyword').trim()).toBe(styles.getPropertyValue('--code-keyword').trim())
  const bounds=select.getBoundingClientRect()
  expect(bounds.right).toBeLessThanOrEqual(width)
  expect(bounds.left).toBeGreaterThanOrEqual(0)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  if (ACCESSIBILITY_PRESETS.includes(id)) {
   const fills=screen.getAllByRole('progressbar').map(meter => getComputedStyle(meter.querySelector('.usage-meter__fill')!).backgroundImage)
   expect(fills[0]).toContain('45deg'); expect(fills[1]).toContain('90deg')
   expect(screen.getByLabelText('Usage limit nearly reached')).toBeVisible()
  }
 }
})

it('keeps stack textures distinct and shared with legends in accessibility presets', () => {
 const activity = ['tests','build_lint','git','read','edit','other','model','waiting_on_person','idle']
 const view = render(<div>{activity.map(name => <span key={name} className={'activity-'+name} />)}{[1,2,3].map(n => <span key={n} className={'si-s-'+n} />)}</div>)
 for (const {id} of THEMES.filter(option => option.id !== 'system')) {
  document.documentElement.dataset.theme = id
  const images = Array.from(view.container.querySelectorAll('span'), el => getComputedStyle(el).backgroundImage)
  // The stack texture is an accessibility-preset feature, not a general theme one: every other
  // theme — the defaults and every bundled scheme alike — leaves --activity-pattern-* at 'none'.
  if (!ACCESSIBILITY_PRESETS.includes(id)) expect(images.every(image => image === 'none')).toBe(true)
  else {
   expect(new Set(images.slice(0,9)).size).toBe(9)
   expect(new Set(images.slice(9)).size).toBe(3)
   expect(images.every(image => image.includes('repeating-linear-gradient'))).toBe(true)
  }
 }
})
