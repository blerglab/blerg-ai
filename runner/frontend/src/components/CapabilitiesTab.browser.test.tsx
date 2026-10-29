// Card #132: the session panel's Capabilities tab in real Chromium, in the fixed 340 px panel on a
// 3840 px screen and in the phone drawer at 390 px: the header card (engine, version, human model
// name, workspace, permissions chip, As of), the ⓘ that holds the explanations and is reached by
// keyboard, one toolbar row (filter + "+ Add") that fits without overflow, the sections with their
// counts, and the filter. With VITE_CAPS_SHOT_DIR set it also saves a screenshot of each.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import '../index.css'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async (orig) => ({
  ...(await orig<typeof import('../authClient')>()),
  getAccessToken: () => 'tok',
  // Never let a test navigate the test page itself.
  redirectToRefresh: vi.fn(),
}))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_CAPS_SHOT_DIR

// A report from 12:05 AM today, as the owner's screenshot showed.
function todayAt(h: number, m: number) {
  const d = new Date()
  d.setHours(h, m, 0, 0)
  return d.toISOString()
}

const skills = Array.from({ length: 18 }, (_, i) => ({
  name: i === 0 ? 'release-notes' : `skill-${String(i).padStart(2, '0')}`,
  description: i === 0 ? 'Writes release notes from the merged pull requests since the last tag, grouped by area, with links.' : `Does step ${i} of the routine.`,
  source: i % 3 === 0 ? 'user' : 'plugin',
}))

const CAPS = {
  seq: 9,
  ts: todayAt(0, 5),
  capabilities: {
    engine: 'claude',
    engine_name: 'Claude Code',
    version: '2.1.305',
    model: 'claude-opus-5-5',
    cwd: '/home/dev/repositories/an-especially-long-workspace-name/services/session-store',
    permission_mode: 'bypassPermissions',
    tool_schema_refresh: 'next_turn',
    note: "What Claude Code loaded when this session's latest turn started.",
    groups: [
      { id: 'skills', label: 'Skills', items: skills },
      { id: 'plugins', label: 'Plugins', items: [
        { name: 'frontend-design', status: 'enabled', source: 'user', description: 'Guidance for distinctive, intentional visual design.' },
        { name: 'superpowers', status: 'enabled', source: 'user', detail: '6.4.1' },
        { name: 'old-helper', status: 'disabled', source: 'user' },
      ] },
      { id: 'mcp', label: 'MCP servers', items: [
        { name: 'issues', status: 'connected', detail: '3 tools', source: 'user' },
        { name: 'drive', status: 'failed', source: 'project' },
      ] },
      { id: 'tools', label: 'Tools', items: [{ name: 'Bash' }, { name: 'Read' }, { name: 'Edit' }] },
    ],
  },
}

const PLUGINS = { engine: 'claude', supported: true, session_apply: true, allowed_marketplaces: ['example/plugins'], any_marketplace: false, plugins: [], requests: [] }

const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))

beforeEach(() => {
  localStorage.clear()
  apiFetch.mockReset()
  apiFetch.mockImplementation((url: string) => url.endsWith('/plugins/catalog')
    // An engine without a catalog: "+ Add" falls back to the add-by-name form (card #144).
    ? Promise.resolve(new Response(JSON.stringify({ code: 'unsupported', error: 'none' }), { status: 409, headers: { 'Content-Type': 'application/json' } }))
    : json(url.endsWith('/capabilities') ? CAPS : url.endsWith('/plugins') ? PLUGINS : {}))
})

function show(mode: 'panel' | 'drawer') {
  render(
    <div style={{ height: '100vh', display: 'flex', justifyContent: 'flex-end' }}>
      <MemoryRouter>
        <SessionRail id="rail" session={makeSession()} live={makeLive({ canReleasePrompt: true })} toolChanges={0}
          mode={mode} open onClose={() => {}} onOpenArtifact={() => {}} />
      </MemoryRouter>
    </div>,
  )
}

const box = (el: Element) => el.getBoundingClientRect()

describe.each([
  { width: 390, height: 800, mode: 'drawer' as const },
  { width: 3840, height: 1600, mode: 'panel' as const },
])('the Capabilities tab at $width px ($mode)', ({ width, height, mode }) => {
  beforeEach(async () => { await page.viewport(width, height) })

  async function openTab() {
    show(mode)
    await userEvent.click(screen.getByRole('tab', { name: /^Capabilities/ }))
    await screen.findByText('frontend-design')
    const rail = mode === 'drawer' ? screen.getByRole('dialog', { name: 'Session panel' }) : screen.getByTestId('session-rail')
    if (mode === 'drawer') await waitFor(() => expect(box(rail).right).toBe(width))
    return rail
  }

  it.each(['danger-full-access', 'workspace-write'])('keeps the Codex %s chip concise and exposes scope accessibly', async sandbox => {
    apiFetch.mockImplementation((url: string) => json(url.endsWith('/capabilities')
      ? { ...CAPS, capabilities: { ...CAPS.capabilities, engine: 'codex', engine_name: 'Codex', permission_mode: 'never', sandbox_mode: sandbox } }
      : url.endsWith('/plugins') ? PLUGINS : {}))
    const rail = await openTab()
    const chip = screen.getByTestId('caps-permission')
    expect(chip.textContent).toBe('No prompts')
    expect(chip.getAttribute('title')).toContain(`Codex: approval never, sandbox ${sandbox}`)
    expect(chip.getAttribute('aria-description')).toBe(chip.getAttribute('title'))
    expect(chip.className).toContain('risky')
    expect(box(chip).width).toBeLessThan(100)
    expect(rail.scrollWidth).toBeLessThanOrEqual(rail.clientWidth + 1)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })

  it('heads the tab with a compact card: engine, version, model, workspace, permissions, As of', async () => {
    const rail = await openTab()
    if (shotDir) await page.screenshot({ path: `${shotDir}/capabilities-${width}.png`, element: rail })
    const card = screen.getByTestId('caps-header')
    expect(card.textContent).toContain('Claude Code 2.1.305 · Opus 5.5')
    expect(within(card).getByText('Opus 5.5').getAttribute('title')).toContain('claude-opus-5-5')
    const path = within(card).getByTestId('caps-cwd')
    expect(path.getAttribute('title')).toBe(CAPS.capabilities.cwd)
    expect(path.textContent).toContain('…')
    const chip = within(card).getByTestId('caps-permission')
    expect(chip.textContent).toBe('No prompts')
    // Risky modes are amber; the chip says so to a screen reader too, through its title.
    expect(chip.className).toContain('risky')
    const amber = getComputedStyle(document.documentElement).getPropertyValue('--amber').trim()
    const probe = document.createElement('span')
    probe.style.color = amber
    document.body.appendChild(probe)
    expect(getComputedStyle(chip).color).toBe(getComputedStyle(probe).color)
    probe.remove()
    // The browser's locale, as the rest of the app's times.
    expect(within(card).getByTestId('caps-asof').textContent).toBe(`As of ${new Date(CAPS.ts).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })}`)
    // The run-on prose is gone.
    expect(rail.textContent).not.toContain('permissions: bypassPermissions')
    expect(rail.textContent).not.toMatch(/reported \w{3} \d/)
    // The card sits on the panel's gutter, inside the panel.
    const r = box(rail)
    expect(box(card).left - r.left).toBeGreaterThanOrEqual(12)
    expect(r.right - box(card).right).toBeGreaterThanOrEqual(12)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })

  it('keeps the explanations behind an ⓘ a keyboard opens and Escape closes', async () => {
    const rail = await openTab()
    // Not on screen until asked for.
    expect(rail.textContent).not.toContain('An active turn keeps its loaded tools')
    const info = screen.getByRole('button', { name: 'About this list' })
    expect(info.getAttribute('aria-expanded')).toBe('false')
    // A target of at least 24 by 24 px (WCAG 2.5.8).
    expect(box(info).width).toBeGreaterThanOrEqual(24)
    expect(box(info).height).toBeGreaterThanOrEqual(24)
    // Reached with Tab from the tab strip, like every other control.
    screen.getByRole('tab', { name: /^Capabilities/ }).focus()
    for (let i = 0; i < 6 && document.activeElement !== info; i++) await userEvent.keyboard('{Tab}')
    expect(document.activeElement).toBe(info)
    await userEvent.keyboard('{Enter}')
    expect(info.getAttribute('aria-expanded')).toBe('true')
    const pop = document.getElementById(info.getAttribute('aria-controls')!)!
    expect(pop).not.toBeNull()
    expect(pop.textContent).toContain("What Claude Code loaded when this session's latest turn started.")
    expect(pop.textContent).toContain('aren’t shown until the engine reports again')
    expect(pop.textContent).toContain('An active turn keeps its loaded tools')
    expect(pop.textContent).toContain('Current Blerg grants continue to apply to every call')
    // Read out on open: a polite live region holds the same words.
    const announce = screen.getByTestId('caps-info-announce')
    expect(announce.getAttribute('aria-live')).toBe('polite')
    expect(announce.textContent).toContain('An active turn keeps its loaded tools')
    if (shotDir) {
      document.documentElement.dataset.theme = 'dark'
      await page.screenshot({ path: `${shotDir}/capabilities-${width}-info-dark.png`, element: rail })
      delete document.documentElement.dataset.theme
    }
    const r = box(rail)
    expect(box(pop).left).toBeGreaterThanOrEqual(r.left)
    expect(box(pop).right).toBeLessThanOrEqual(r.right)
    await userEvent.keyboard('{Escape}')
    expect(info.getAttribute('aria-expanded')).toBe('false')
    expect(document.getElementById(info.getAttribute('aria-controls')!)).toBeNull()
    expect(document.activeElement).toBe(info)
    // The drawer is still open: Escape closed only the popover.
    expect(rail.isConnected).toBe(true)
    // Opened with the pointer and the focus moved on to the filter: Esc still closes the popover
    // first, the panel stays, and the focus stays in the filter.
    await userEvent.click(info)
    const search = screen.getByRole('searchbox', { name: 'Filter skills, plugins and tools' })
    search.focus()
    await userEvent.keyboard('{Escape}')
    expect(info.getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(search)
    expect(rail.isConnected).toBe(true)
  })

  it('puts the filter and a styled "+ Add" on one row that fits the panel', async () => {
    const rail = await openTab()
    const search = screen.getByRole('searchbox', { name: 'Filter skills, plugins and tools' })
    const add = screen.getByRole('button', { name: 'Add plugin or skill' })
    expect(add.textContent?.replace(/\s+/g, ' ').trim()).toBe('+ Add')
    const toolbar = screen.getByTestId('caps-toolbar')
    expect(toolbar.contains(search) && toolbar.contains(add)).toBe(true)
    const s = box(search)
    const a = box(add)
    // One row: same vertical band, the button to the filter's right, the same height.
    expect(Math.abs(s.top - a.top)).toBeLessThanOrEqual(2)
    expect(Math.abs(s.height - a.height)).toBeLessThanOrEqual(2)
    expect(a.left).toBeGreaterThan(s.right)
    const r = box(rail)
    expect(s.left - r.left).toBeGreaterThanOrEqual(12)
    expect(r.right - a.right).toBeGreaterThanOrEqual(12)
    expect(toolbar.scrollWidth).toBeLessThanOrEqual(toolbar.clientWidth)
    // Styled like the panel's other buttons, not the browser's default.
    expect(getComputedStyle(add).borderTopLeftRadius).not.toBe('0px')
    expect(getComputedStyle(add).backgroundColor).not.toBe('rgb(239, 239, 239)')
    // It opens the plugin browser (here an engine without a catalog: the add form), and shows itself
    // open in the selected blue.
    await userEvent.click(add)
    expect(add.getAttribute('aria-expanded')).toBe('true')
    expect(await screen.findByLabelText('Plugin name')).toBeTruthy()
    if (shotDir) {
      document.documentElement.dataset.theme = 'dark'
      await page.screenshot({ path: `${shotDir}/capabilities-${width}-add-dark.png`, element: rail })
      delete document.documentElement.dataset.theme
    }
    const selected = getComputedStyle(document.documentElement).getPropertyValue('--selected-border').trim()
    const probe = document.createElement('span')
    probe.style.color = selected
    document.body.appendChild(probe)
    expect(getComputedStyle(add).borderTopColor).toBe(getComputedStyle(probe).color)
    probe.remove()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })

  it('lists consistent sections with counts and glyph-only status, and the filter narrows them', async () => {
    const rail = await openTab()
    const heads = Array.from(rail.querySelectorAll('.caps-group-head'))
    const sizes = new Set(heads.map(h => getComputedStyle(h).fontSize))
    expect(sizes.size).toBe(1)
    expect(screen.getByTestId('caps-count-skills').textContent).toBe('18')
    expect(screen.getByTestId('caps-count-plugins').textContent).toBe('3')
    expect(screen.getByTestId('caps-count-mcp').textContent).toBe('2')
    const plugins = screen.getByTestId('caps-group-plugins')
    const row = within(plugins).getByText('frontend-design').closest('li')!
    // A glyph, not the word: the word is for a screen reader and the tooltip.
    const status = row.querySelector('.caps-status') as HTMLElement
    expect(status.getAttribute('title')).toBe('enabled')
    const visible = Array.from(row.querySelectorAll<HTMLElement>('*')).filter(el => el.children.length === 0 && el.textContent === 'enabled' && box(el).width > 2)
    expect(visible).toHaveLength(0)
    // Every row fits one line of description, inside the panel.
    const r = box(rail)
    for (const li of rail.querySelectorAll('.caps-item')) expect(box(li).right).toBeLessThanOrEqual(r.right)
    // Nothing in a section head runs past the panel, "not reported by this engine" included.
    for (const el of rail.querySelectorAll('.caps-group-head *')) expect(box(el).right).toBeLessThanOrEqual(r.right)
    // Tools come in the report; slash commands do not: said, not hidden.
    expect(within(screen.getByTestId('caps-group-commands')).getByText('not reported by this engine')).toBeTruthy()
    // Long groups start closed; their header opens them.
    await userEvent.click(screen.getByTestId('caps-group-skills').querySelector('.caps-group-head button')!)
    const desc = within(screen.getByTestId('caps-group-skills')).getByText(/Writes release notes/)
    expect(box(desc).height).toBeLessThan(24)
    // A cut description is a button with the whole text in its tooltip; a press shows it whole.
    await waitFor(() => expect(desc.tagName).toBe('BUTTON'))
    const cut = within(screen.getByTestId('caps-group-skills')).getByRole('button', { name: /Writes release notes/ })
    expect(cut.getAttribute('title')).toContain('with links.')
    await userEvent.click(cut)
    expect(cut.getAttribute('aria-expanded')).toBe('true')
    expect(box(cut).height).toBeGreaterThan(24)
    // One that fits its line is plain text, not another tab stop.
    const short = within(screen.getByTestId('caps-group-skills')).getByText('Does step 1 of the routine.')
    expect(short.tagName).toBe('SPAN')
    expect(short.closest('button')).toBeNull()

    await userEvent.fill(screen.getByRole('searchbox', { name: 'Filter skills, plugins and tools' }), 'release')
    await waitFor(() => expect(screen.getByTestId('caps-count-skills').textContent).toBe('1 of 18'))
    expect(screen.queryByTestId('caps-group-plugins')).toBeNull()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })
})
