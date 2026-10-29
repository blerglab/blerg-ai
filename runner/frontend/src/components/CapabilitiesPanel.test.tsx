import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import CapabilitiesPanel from './CapabilitiesPanel'
import type { CapabilitiesReport } from '../lib/capabilities'

const manyTools = Array.from({ length: 20 }, (_, i) => ({ name: `Tool${i}` }))

const report: CapabilitiesReport = {
  seq: 5,
  ts: '2026-09-26T10:00:00Z',
  payload: {
    engine: 'claude',
    engine_name: 'Claude Code',
    model: 'claude-opus-5-5',
    version: '2.1.283',
    cwd: '~/repos/example',
    permission_mode: 'default',
    note: 'What Claude Code loaded when this session\'s latest turn started.',
    groups: [
      {
        id: 'skills', label: 'Skills', items: [
          { name: 'release-notes', description: 'Writes release notes.', source: 'user' },
          { name: 'long-one', description: 'x'.repeat(300) + ' END' },
          { name: 'xss', description: '<img src=x onerror=alert(1)>' },
        ],
      },
      { id: 'mcp', label: 'MCP servers', items: [
        { name: 'issues', status: 'connected', detail: '3 tools', source: 'user' },
        { name: 'drive', status: 'needs-auth' },
      ] },
      { id: 'tools', label: 'Tools', total: 700, items: manyTools },
      { id: 'plugins', label: 'Plugins', items: [] },
    ],
  },
}

describe('CapabilitiesPanel', () => {
  it.each(['danger-full-access', 'workspace-write', 'read-only'])('keeps Codex %s scope in the accessible description', sandbox => {
    render(<CapabilitiesPanel report={{ ...report, payload: { ...report.payload, engine: 'codex', permission_mode: 'never', sandbox_mode: sandbox } }} onClose={() => {}} />)
    const chip = screen.getByTestId('caps-permission')
    expect(chip).toHaveTextContent(/^No prompts$/)
    expect(chip).toHaveAttribute('title', expect.stringContaining(`Codex: approval never, sandbox ${sandbox}`))
    expect(chip).toHaveAttribute('aria-description', chip.getAttribute('title'))
    expect(chip.className).toContain('risky')
  })

  it('renders groups with counts, not-reported and empty states', () => {
    render(<CapabilitiesPanel report={report} onClose={() => {}} />)
    const dialog = screen.getByRole('dialog', { name: 'Skills & plugins' })
    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(screen.getByTestId('caps-count-skills').textContent).toBe('3')
    expect(screen.getByTestId('caps-count-mcp').textContent).toBe('2')
    expect(screen.getByTestId('caps-count-tools').textContent).toBe('20')
    // Reported but empty vs. not reported at all.
    expect(within(screen.getByTestId('caps-group-plugins')).getByText('None loaded.')).toBeTruthy()
    expect(within(screen.getByTestId('caps-group-commands')).getByText('not reported by this engine')).toBeTruthy()
    expect(within(screen.getByTestId('caps-group-agents')).getByText('not reported by this engine')).toBeTruthy()
    // Header card: engine, version and the model's name; its raw id in the tooltip.
    expect(screen.getByTestId('caps-meta').textContent).toBe('Claude Code 2.1.283 · Opus 5.5')
    expect(within(screen.getByTestId('caps-meta')).getByText('Opus 5.5').getAttribute('title')).toBe('Model: claude-opus-5-5')
    expect(screen.getByTestId('caps-cwd').textContent).toBe('~/repos/example')
    expect(screen.getByTestId('caps-permission').textContent).toBe('Asks first')
    expect(screen.getByTestId('caps-permission').className).not.toContain('risky')
    expect(screen.getByTestId('caps-asof').textContent).toMatch(/^As of Sep 26(, 2026)?, \d{1,2}:\d{2}\s?[AP]M$/)
    // The explanations wait behind the ⓘ.
    expect(dialog.textContent).not.toContain('reflects what was loaded at start')
    fireEvent.click(screen.getByRole('button', { name: 'About this list' }))
    expect(screen.getByTestId('caps-info').textContent).toContain('reflects what was loaded at start')
    // Status as a glyph; its word for a screen reader and the tooltip.
    const drive = within(screen.getByTestId('caps-group-mcp')).getByText('drive').closest('li')!
    expect(drive.querySelector('.caps-status')?.getAttribute('title')).toBe('needs sign-in')
    expect(drive.querySelector('.caps-status [aria-hidden]')?.textContent).toBe('!')
    expect(drive.querySelector('.caps-status .rail-sr')?.textContent).toBe('needs sign-in')
  })

  it('collapses long groups, expands on click, and says when a list was cut', () => {
    render(<CapabilitiesPanel report={report} onClose={() => {}} />)
    const toolsHead = screen.getByTestId('caps-group-tools').querySelector('.caps-group-head button')!
    expect(toolsHead.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByText('Tool0')).toBeNull()
    fireEvent.click(toolsHead)
    expect(toolsHead.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText('Tool0')).toBeTruthy()
    expect(screen.getByText('Showing 20 of 700.')).toBeTruthy()
    // Short groups start open; they close too.
    const skillsHead = screen.getByTestId('caps-group-skills').querySelector('.caps-group-head button')!
    expect(skillsHead.getAttribute('aria-expanded')).toBe('true')
    fireEvent.click(skillsHead)
    expect(screen.queryByText('release-notes')).toBeNull()
  })

  it('filters by the search box across groups', () => {
    render(<CapabilitiesPanel report={report} onClose={() => {}} />)
    const search = screen.getByRole('searchbox', { name: /filter/i })
    fireEvent.change(search, { target: { value: 'tool1' } })
    // Collapsed groups open while searching; non-matching groups drop out.
    expect(screen.getByText('Tool1')).toBeTruthy()
    expect(screen.getByText('Tool19')).toBeTruthy()
    expect(screen.queryByText('Tool2')).toBeNull()
    expect(screen.getByTestId('caps-count-tools').textContent).toBe('11 of 20')
    expect(screen.queryByTestId('caps-group-skills')).toBeNull()
    expect(screen.queryByTestId('caps-group-commands')).toBeNull()
    fireEvent.change(search, { target: { value: 'needs-auth' } })
    expect(screen.getByText('drive')).toBeTruthy()
    fireEvent.change(search, { target: { value: 'nothing-like-this' } })
    expect(screen.getByTestId('caps-no-match')).toBeTruthy()
  })

  it('renders descriptions as plain text; only a cut one is a button that opens it whole', () => {
    // jsdom has no layout: a description over 200 characters measures as overflowing its line.
    const sw = vi.spyOn(HTMLElement.prototype, 'scrollWidth', 'get').mockImplementation(function (this: HTMLElement) { return (this.textContent ?? '').length > 200 ? 1000 : 100 })
    const cw = vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(200)
    try {
      const { baseElement } = render(<CapabilitiesPanel report={report} onClose={() => {}} />)
      expect(baseElement.querySelector('img')).toBeNull()
      const short = screen.getByText('<img src=x onerror=alert(1)>')
      expect(short.tagName).toBe('SPAN')
      expect(screen.getByText('Writes release notes.').tagName).toBe('SPAN')
      const long = screen.getByText(/END$/)
      expect(long.tagName).toBe('BUTTON')
      expect(long.className).not.toContain('open')
      expect(long.getAttribute('title')).toBe('x'.repeat(300) + ' END')
      expect(long.getAttribute('aria-expanded')).toBe('false')
      fireEvent.click(long)
      expect(long.getAttribute('aria-expanded')).toBe('true')
      expect(long.className).toContain('open')
    } finally {
      sw.mockRestore()
      cw.mockRestore()
    }
  })

  it('focuses the search box, traps Tab, closes on Escape and backdrop', () => {
    const onClose = vi.fn()
    render(<CapabilitiesPanel report={report} onClose={onClose} />)
    const dialog = screen.getByRole('dialog')
    const search = screen.getByRole('searchbox')
    expect(document.activeElement).toBe(search)
    const close = screen.getByRole('button', { name: 'Close' })
    // Shift+Tab from the first focusable wraps to the last.
    close.focus()
    fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true })
    const focusables = dialog.querySelectorAll('button:not([disabled]), input')
    expect(document.activeElement).toBe(focusables[focusables.length - 1])
    // Tab from the last wraps to the first.
    fireEvent.keyDown(dialog, { key: 'Tab' })
    expect(document.activeElement).toBe(close)
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.pointerDown(screen.getByTestId('caps-overlay'))
    fireEvent.click(screen.getByTestId('caps-overlay'))
    expect(onClose).toHaveBeenCalledTimes(2)
    fireEvent.click(close)
    expect(onClose).toHaveBeenCalledTimes(3)
  })

  it('says when nothing was reported, and when it cannot be', () => {
    const { unmount } = render(<CapabilitiesPanel report={null} onClose={() => {}} />)
    expect(screen.getByTestId('caps-none').textContent).toContain('Nothing reported yet')
    expect(screen.queryByRole('searchbox')).toBeNull()
    unmount()
    render(<CapabilitiesPanel report={null} unavailable="Not available for terminal sessions." onClose={() => {}} />)
    expect(screen.getByTestId('caps-unavailable').textContent).toBe('Not available for terminal sessions.')
  })

  it('restores focus to the opener on close', () => {
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()
    const { unmount } = render(<CapabilitiesPanel report={report} onClose={() => {}} />)
    expect(document.activeElement).not.toBe(opener)
    unmount()
    expect(document.activeElement).toBe(opener)
    opener.remove()
  })

  it('lists the always-on plugins a session loaded', () => {
    const withPlugins: CapabilitiesReport = {
      ...report,
      payload: {
        ...report.payload,
        groups: [
          { id: 'plugins', label: 'Plugins', items: [
            { name: 'superpowers', source: 'user', status: 'enabled', detail: '6.4.1' },
            { name: 'frontend-design', source: 'user', status: 'enabled' },
          ] },
        ],
      },
    }
    render(<CapabilitiesPanel report={withPlugins} onClose={() => {}} />)
    expect(screen.getByTestId('caps-count-plugins').textContent).toBe('2')
    const group = within(screen.getByTestId('caps-group-plugins'))
    expect(group.getByText('superpowers')).toBeTruthy()
    expect(group.getByText('frontend-design')).toBeTruthy()
  })

  it('marks the MCP servers group when the session has tool changes pending', () => {
    render(<CapabilitiesPanel report={report} pendingToolChanges={2} onClose={() => {}} />)
    const note = screen.getByTestId('caps-mcp-pending')
    expect(note.textContent).toMatch(/2 tool changes pending/)
    expect(note.querySelector('a')?.getAttribute('href')).toBe('/proposals')
  })

  // Review Focus 5: no MCP group in the report, still a pending note.
  it('shows the pending note even when the report has no MCP group', () => {
    const noMcp: CapabilitiesReport = { ...report, payload: { ...report.payload, groups: report.payload.groups.filter(g => g.id !== 'mcp') } }
    render(<CapabilitiesPanel report={noMcp} pendingToolChanges={1} onClose={() => {}} />)
    expect(screen.getByTestId('caps-mcp-pending').textContent).toMatch(/1 tool change pending/)
  })
})

describe('tool schema refresh capability', () => {
  it('uses adapter metadata even for an unfamiliar engine name', () => {
    render(<CapabilitiesPanel report={{ ...report, payload: { ...report.payload, engine: 'fixture-engine', tool_schema_refresh: 'new_session' } }} onClose={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'About this list' }))
    expect(screen.getByTestId('tool-schema-guidance').textContent).toContain('Start a new session and select the current MCP connections and tools')
  })

  it('does not claim legacy reports can reload tools', () => {
    render(<CapabilitiesPanel report={report} onClose={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'About this list' }))
    expect(screen.getByTestId('tool-schema-guidance').textContent).toContain("hasn't reported whether")
  })
})

describe('the ⓘ popover (card #132)', () => {
  it('opens from the keyboard, says what the list is, and Escape closes it without closing the dialog', () => {
    const onClose = vi.fn()
    render(<CapabilitiesPanel report={{ ...report, payload: { ...report.payload, tool_schema_refresh: 'next_turn' } }} onClose={onClose} />)
    const info = screen.getByRole('button', { name: 'About this list' })
    expect(info.getAttribute('aria-expanded')).toBe('false')
    expect(info.getAttribute('aria-controls')).toBeNull()
    info.focus()
    fireEvent.click(info)
    const pop = document.getElementById(info.getAttribute('aria-controls')!)!
    expect(pop.textContent).toContain("What Claude Code loaded when this session's latest turn started.")
    expect(pop.textContent).toContain('An active turn keeps its loaded tools. Current Blerg grants continue to apply to every call.')
    // Announced: the polite region carries the text while it is open.
    expect(screen.getByTestId('caps-info-announce').getAttribute('aria-live')).toBe('polite')
    expect(screen.getByTestId('caps-info-announce').textContent).toContain('An active turn keeps its loaded tools.')
    fireEvent.keyDown(pop, { key: 'Escape' })
    expect(info.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByTestId('caps-info')).toBeNull()
    expect(screen.getByTestId('caps-info-announce').textContent).toBe('')
    expect(document.activeElement).toBe(info)
    expect(onClose).not.toHaveBeenCalled()
    // With the focus in the filter, Esc still closes the popover first, and leaves the focus there.
    fireEvent.click(info)
    const search = screen.getByRole('searchbox')
    search.focus()
    fireEvent.keyDown(search, { key: 'Escape' })
    expect(screen.queryByTestId('caps-info')).toBeNull()
    expect(document.activeElement).toBe(search)
    expect(onClose).not.toHaveBeenCalled()
    // Closed, Esc closes the dialog as before.
    fireEvent.keyDown(search, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
    // A press outside closes it too.
    fireEvent.click(info)
    expect(screen.getByTestId('caps-info')).toBeTruthy()
    fireEvent.pointerDown(document.body)
    expect(screen.queryByTestId('caps-info')).toBeNull()
  })
})
