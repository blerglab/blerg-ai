import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { useState } from 'react'
import McpPicker from './McpPicker'
import { MCP_ALLOW_WARNING } from '../lib/mcpSelection'
import type { McpSelectionEntry } from '../types'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const CONNS = [
  { id: 'c1', name: 'calendar', url: 'https://x/mcp', auth_kind: 'static', status: 'ok', default_tools: { read: { mode: 'allow', hash: 'h-read' } } },
  { id: 'c2', name: 'broken', url: 'https://y/mcp', auth_kind: 'oauth', status: 'needs_auth', default_tools: {} },
]
const TOOLS = [
  { name: 'read', description: 'reads', hash: 'h-read', annotations: { readOnlyHint: true } },
  { name: 'list', description: 'lists', hash: 'h-list', annotations: { readOnlyHint: true } },
  { name: 'send', description: 'sends', hash: 'h-send' },
]

function ok(body: unknown) {
  return Promise.resolve({ ok: true, json: () => Promise.resolve(body) })
}

let toolsCalls: string[]
beforeEach(() => {
  toolsCalls = []
  apiFetch.mockReset()
  apiFetch.mockImplementation((url: string) => {
    if (url === '/api/mcp/connections') return ok({ connections: CONNS })
    if (url === '/api/mcp/connections/c1/tools') {
      toolsCalls.push(url)
      return ok({ tools: TOOLS })
    }
    return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({ error: 'connection not found' }) })
  })
})

// Hosts the picker and records every value it emits.
function setup(opts?: { initial?: McpSelectionEntry[]; requireExplicit?: boolean }) {
  const emitted: McpSelectionEntry[][] = []
  function Host() {
    const [v, setV] = useState<McpSelectionEntry[]>(opts?.initial ?? [])
    return <McpPicker value={v} requireExplicit={opts?.requireExplicit} onChange={n => { emitted.push(n); setV(n) }} />
  }
  render(<Host />)
  return emitted
}

// Check the connection and open its per-tool list (Customize); the crons form shows the list directly.
const check = async (explicit = false) => {
  fireEvent.click(await screen.findByRole('checkbox', { name: 'calendar' }))
  if (!explicit) fireEvent.click(await screen.findByTestId('mcp-customize-calendar'))
  await screen.findByTestId('mcp-tool-calendar-read')
}
const modeSelect = (tool: string) => screen.getByLabelText(`calendar ${tool} mode`) as HTMLSelectElement

describe('McpPicker', () => {
  it('starts with every connection unchecked and loads no tools', async () => {
    const emitted = setup()
    const box = await screen.findByRole('checkbox', { name: 'calendar' })
    expect((box as HTMLInputElement).checked).toBe(false)
    expect(toolsCalls).toEqual([])
    expect(emitted).toEqual([])
  })

  it('shows a connection that is not ok as disabled with the reason', async () => {
    setup()
    const box = await screen.findByRole('checkbox', { name: 'broken' }) as HTMLInputElement
    expect(box.disabled).toBe(true)
    expect(screen.getByTestId('mcp-reason-broken').textContent).toMatch(/signed in again/)
  })

  it('defaults every tool to off (the connection default_tools do not pre-select) and emits nothing', async () => {
    const emitted = setup()
    await check()
    for (const t of ['read', 'list', 'send']) expect(modeSelect(t).value).toBe('off')
    expect(emitted[emitted.length - 1]).toEqual([])
    expect(screen.getByText(/No tools selected/)).toBeTruthy()
  })

  it('shows readOnlyHint only as a badge', async () => {
    setup()
    await check()
    expect(screen.getByTestId('mcp-tool-calendar-read').textContent).toMatch(/read-only/)
    expect(screen.getByTestId('mcp-tool-calendar-send').textContent).not.toMatch(/read-only/)
    expect(modeSelect('read').value).toBe('off')
  })

  it('a checked connection shows the presets with none pressed and the tool list hidden', async () => {
    const emitted = setup()
    fireEvent.click(await screen.findByRole('checkbox', { name: 'calendar' }))
    await screen.findByTestId('mcp-presets-calendar')
    expect(screen.queryByTestId('mcp-tool-calendar-read')).toBeNull()
    expect(screen.getByTestId('mcp-preset-calendar-none').getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByTestId('mcp-preset-calendar-allow_all').getAttribute('aria-pressed')).toBe('false')
    expect(emitted[emitted.length - 1]).toEqual([])
  })

  it('"Require approval" allows the read-only tools and proposes the rest', async () => {
    const emitted = setup()
    await check()
    fireEvent.click(screen.getByTestId('mcp-preset-calendar-approval'))
    expect(modeSelect('read').value).toBe('allow')
    expect(modeSelect('list').value).toBe('allow')
    expect(modeSelect('send').value).toBe('propose')
    expect(screen.getByTestId('mcp-preset-calendar-approval').getAttribute('aria-pressed')).toBe('true')
    expect(emitted[emitted.length - 1]).toEqual([
      { connection: 'c1', tools: { read: { mode: 'allow', hash: 'h-read' }, list: { mode: 'allow', hash: 'h-list' }, send: { mode: 'propose', hash: 'h-send' } } },
    ])
  })

  it('"Allow all" allows every tool with one summary warning instead of one per tool', async () => {
    const emitted = setup()
    await check()
    fireEvent.click(screen.getByTestId('mcp-preset-calendar-allow_all'))
    expect(modeSelect('send').value).toBe('allow')
    expect(screen.getByTestId('mcp-warn-calendar').textContent).toContain('1 of 3 tools can change things')
    expect(screen.queryByTestId('mcp-warn-calendar-send')).toBeNull()
    expect(Object.keys(emitted[emitted.length - 1][0].tools)).toEqual(['read', 'list', 'send'])
  })

  it('"None" turns everything off, and a hand-edited list reads as Custom', async () => {
    const emitted = setup()
    await check()
    fireEvent.click(screen.getByTestId('mcp-preset-calendar-allow_all'))
    fireEvent.click(screen.getByTestId('mcp-preset-calendar-none'))
    expect(modeSelect('send').value).toBe('off')
    expect(emitted[emitted.length - 1]).toEqual([])
    fireEvent.change(modeSelect('send'), { target: { value: 'propose' } })
    expect(screen.getByTestId('mcp-preset-calendar-custom')).toBeTruthy()
    expect(screen.getByTestId('mcp-preset-calendar-none').getAttribute('aria-pressed')).toBe('false')
  })

  it('shows no presets and the tool list directly when every mode must be explicit', async () => {
    setup({ requireExplicit: true })
    await check(true)
    expect(screen.queryByTestId('mcp-presets-calendar')).toBeNull()
    expect(screen.queryByTestId('mcp-customize-calendar')).toBeNull()
  })

  it('warns on allow for a tool not marked read-only, and not for a read-only one', async () => {
    setup()
    await check()
    fireEvent.change(modeSelect('read'), { target: { value: 'allow' } })
    expect(screen.queryByTestId('mcp-warn-calendar-read')).toBeNull()
    fireEvent.change(modeSelect('send'), { target: { value: 'allow' } })
    expect(screen.getByTestId('mcp-warn-calendar-send').textContent).toContain(MCP_ALLOW_WARNING)
    expect(MCP_ALLOW_WARNING).toBe('this can change or send things without asking you')
  })

  it('offers propose as selectable, labelled as queued for your approval', async () => {
    setup()
    await check()
    const opt = Array.from(modeSelect('send').options).find(o => o.value === 'propose')!
    expect(opt.disabled).toBe(false)
    expect(opt.textContent).toContain('queued for your approval')
    expect(opt.textContent).not.toContain('not available')
  })

  it('emits mode propose in the payload when chosen, with the tool hash', async () => {
    const emitted = setup()
    await check()
    fireEvent.change(modeSelect('send'), { target: { value: 'propose' } })
    expect(modeSelect('send').value).toBe('propose')
    expect(emitted[emitted.length - 1]).toEqual([{ connection: 'c1', tools: { send: { mode: 'propose', hash: 'h-send' } } }])
    expect(screen.queryByTestId('mcp-warn-calendar-send')).toBeNull()
  })

  it('emits [{connection, tools:{name:{mode,hash}}}] with only non-off tools, and drops an unchecked connection', async () => {
    const emitted = setup()
    await check()
    fireEvent.change(modeSelect('send'), { target: { value: 'allow' } })
    expect(emitted[emitted.length - 1]).toEqual([{ connection: 'c1', tools: { send: { mode: 'allow', hash: 'h-send' } } }])
    fireEvent.change(modeSelect('send'), { target: { value: 'off' } })
    expect(emitted[emitted.length - 1]).toEqual([])
    fireEvent.change(modeSelect('send'), { target: { value: 'allow' } })
    fireEvent.click(screen.getByRole('checkbox', { name: 'calendar' }))
    expect(emitted[emitted.length - 1]).toEqual([])
  })

  it('surfaces a tool-list failure with retry, and a connection-list failure', async () => {
    apiFetch.mockImplementation((url: string) => {
      if (url === '/api/mcp/connections') return ok({ connections: CONNS })
      return Promise.resolve({ ok: false, status: 502, json: () => Promise.resolve({ error: 'could not list this connection\'s tools' }) })
    })
    setup()
    fireEvent.click(await screen.findByRole('checkbox', { name: 'calendar' }))
    const err = await screen.findByTestId('mcp-tools-error-calendar')
    expect(err.textContent).toContain("could not list this connection's tools")
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
  })

  it('reports a failed connection list', async () => {
    apiFetch.mockImplementation(() => Promise.resolve({ ok: false, status: 503, json: () => Promise.resolve({ error: 'MCP connections need blerg-core' }) }))
    setup()
    expect((await screen.findByTestId('mcp-error')).textContent).toContain('MCP connections need blerg-core')
  })

  it('a tool whose hash changed since the value was made comes back off', async () => {
    const emitted = setup({ initial: [{ connection: 'c1', tools: { read: { mode: 'allow', hash: 'old' }, list: { mode: 'allow', hash: 'h-list' } } }] })
    await waitFor(() => expect((screen.getByRole('checkbox', { name: 'calendar' }) as HTMLInputElement).checked).toBe(true))
    await screen.findByTestId('mcp-tool-calendar-read')
    await waitFor(() => expect(modeSelect('read').value).toBe('off'))
    expect(modeSelect('list').value).toBe('allow')
    expect(emitted[emitted.length - 1]).toEqual([{ connection: 'c1', tools: { list: { mode: 'allow', hash: 'h-list' } } }])
  })

  it('shows a chosen connection that core no longer lists, with a Remove action that drops it from the value', async () => {
    const initial: McpSelectionEntry[] = [
      { connection: 'gone-1', tools: { read: { mode: 'allow', hash: 'h-old' } } },
      { connection: 'c1', tools: { read: { mode: 'allow', hash: 'h-read' } } },
    ]
    const emitted = setup({ initial })
    const orphan = await screen.findByTestId('mcp-orphan-gone-1')
    expect(orphan.textContent).toMatch(/no longer exists/i)
    await screen.findByTestId('mcp-tool-calendar-read')
    fireEvent.click(screen.getByRole('button', { name: /remove missing connection gone-1/i }))
    expect(screen.queryByTestId('mcp-orphan-gone-1')).toBeNull()
    expect(emitted[emitted.length - 1]).toEqual([{ connection: 'c1', tools: { read: { mode: 'allow', hash: 'h-read' } } }])
  })

  it('an orphan alone still shows the picker (not the "no connections" message)', async () => {
    apiFetch.mockImplementation((url: string) => (url === '/api/mcp/connections' ? ok({ connections: [] })
      : Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) })))
    setup({ initial: [{ connection: 'gone-1', tools: { read: { mode: 'allow', hash: 'h' } } }] })
    expect(await screen.findByTestId('mcp-orphan-gone-1')).toBeTruthy()
    expect(screen.queryByTestId('mcp-none')).toBeNull()
  })

  it('a chosen connection that needs attention can be un-chosen even though it cannot be newly chosen', async () => {
    const emitted = setup({ initial: [{ connection: 'c2', tools: { read: { mode: 'allow', hash: 'h-read' } } }] })
    const box = await screen.findByRole('checkbox', { name: 'broken' }) as HTMLInputElement
    expect(box.checked).toBe(true)
    expect(box.disabled).toBe(false)
    fireEvent.click(box)
    expect((screen.getByRole('checkbox', { name: 'broken' }) as HTMLInputElement).disabled).toBe(true)
    expect(emitted[emitted.length - 1]).toEqual([])
  })
})
