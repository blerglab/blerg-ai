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
    // Header facts.
    const meta = screen.getByTestId('caps-meta').textContent ?? ''
    expect(meta).toContain('claude-opus-5-5')
    expect(meta).toContain('Claude Code 2.1.283')
    expect(meta).toMatch(/^reported /)
    expect(dialog.textContent).toContain('~/repos/example')
    expect(dialog.textContent).toContain('reflects what was loaded at start')
    // Status and source chips.
    expect(within(screen.getByTestId('caps-group-mcp')).getByText('needs-auth')).toBeTruthy()
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

  it('renders descriptions as plain text, truncated with a toggle', () => {
    const { baseElement } = render(<CapabilitiesPanel report={report} onClose={() => {}} />)
    expect(baseElement.querySelector('img')).toBeNull()
    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeTruthy()
    expect(screen.queryByText(/END/)).toBeNull()
    const more = screen.getByRole('button', { name: 'more' })
    expect(more.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(more)
    expect(screen.getByText(/END/)).toBeTruthy()
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
})
