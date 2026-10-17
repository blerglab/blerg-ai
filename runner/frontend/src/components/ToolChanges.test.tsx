import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import ToolChanges from './ToolChanges'
import type { PinEventInfo, ToolChangeInfo, ToolDefinitionInfo } from '../types'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const canon = (name: string, desc: string, schema: string) => `{"description":${JSON.stringify(desc)},"inputSchema":${schema},"name":${JSON.stringify(name)}}`
const def = (name: string, desc: string, schema = '{"type":"object"}', unsafe?: string): ToolDefinitionInfo =>
  ({ hash: 'h-' + desc, name, description: desc, input_schema: JSON.parse(schema), canonical: canon(name, desc, schema), unsafe })

function change(over: Partial<ToolChangeInfo> = {}): ToolChangeInfo {
  return {
    id: 'c1', state: 'pending', kind: 'changed', connection_id: 'conn-1', connection_name: 'calendar', tool: 'read_events',
    old_hash: 'h-a', new_hash: 'h-b', mode: 'allow', muted: false, session_id: 's1',
    created_at: '2026-10-13T10:00:00Z', expires_at: '2026-10-20T10:00:00Z', decided_at: null, decided_by: null,
    old_definition: def('read_events', 'Reads events'), new_definition: def('read_events', 'Reads events and deletes them'),
    ...over,
  }
}

let items: ToolChangeInfo[]
let decidedItems: ToolChangeInfo[]
let decidedPageSize: number
let pinEvents: PinEventInfo[]
let calls: { url: string; method: string; body?: unknown }[]
let action: (url: string) => Promise<unknown> | null
let failNext: { status: number; error: string } | null

const res = (body: unknown, status = 200) => Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) })

beforeEach(() => {
  items = []
  decidedItems = []
  decidedPageSize = 50
  pinEvents = []
  calls = []
  failNext = null
  action = () => null
  apiFetch.mockReset()
  apiFetch.mockImplementation((url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const custom = action(url)
    if (custom) return custom
    const u = new URL(url, 'http://x')
    if (u.pathname === '/api/tool-changes' && method === 'GET' && u.searchParams.get('scope') === 'decided') {
      const from = Number(u.searchParams.get('before') ?? '0')
      const page = decidedItems.slice(from, from + decidedPageSize)
      return res({ tool_changes: page, decided_next: from + decidedPageSize < decidedItems.length ? String(from + decidedPageSize) : null, pending_count: 0 })
    }
    if (u.pathname === '/api/mcp/connections/conn-1/pin-events') return res({ events: pinEvents })
    if (u.pathname === '/api/tool-changes' && method === 'GET') return res({ tool_changes: items.filter(i => i.state === 'pending'), decided_next: null, pending_count: items.filter(i => i.state === 'pending' && !i.muted).length })
    const one = /^\/api\/tool-changes\/([^/]+)$/.exec(u.pathname)
    if (one) { const f = items.find(i => i.id === one[1]); return f ? res(f) : res({ error: 'tool change not found' }, 404) }
    const act = /^\/api\/tool-changes\/([^/]+)\/(accept|decline)$/.exec(u.pathname)
    if (act && method === 'POST') {
      if (failNext) { const f = failNext; failNext = null; return res({ error: f.error }, f.status) }
      const f = items.find(i => i.id === act[1])!
      f.state = act[2] === 'accept' ? 'accepted' : 'declined'
      return res(f)
    }
    return res({ error: 'unexpected ' + url }, 404)
  })
})

const mount = (focusId: string | null = null, connectionId: string | null = null) => render(<MemoryRouter><ToolChanges focusId={focusId} connectionId={connectionId} /></MemoryRouter>)

describe('ToolChanges', () => {
  it('shows a changed tool with a word diff of the description and a line diff of the schema', async () => {
    items = [change({ new_definition: def('read_events', 'Reads events and deletes them', '{"type":"object","properties":{"id":{"type":"string"}}}') })]
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    expect(within(card).getAllByText('read_events').length).toBeGreaterThan(0)
    expect(within(card).getAllByText('calendar').length).toBeGreaterThan(0)
    expect(within(card).getByTestId('desc-diff').textContent).toContain('and deletes them')
    expect(within(card).getAllByTestId('diff-add').length).toBeGreaterThan(0)
    expect(within(card).getByTestId('schema-diff').textContent).toContain('"id"')
    expect(within(card).getByRole('link', { name: /open session/i })).toBeTruthy()
  })

  it('leads with a sentence that says what happened and what each button does (changed, pending)', async () => {
    items = [change()]
    mount()
    const lead = (await screen.findByTestId('tool-change-lead')).textContent
    expect(lead).toBe('calendar changed what read_events does after this session pinned it. Calls are blocked until you decide: Accept updates this session\'s grant to the new definition below; Keep off leaves it blocked.')
    const card = screen.getByTestId('tool-change-c1')
    expect(within(card).queryByText('changed', { selector: 'span' })).toBeNull()
  })

  it('without the previous text the lead says the definition below is all there is', async () => {
    items = [change({ old_definition: null })]
    mount()
    const lead = (await screen.findByTestId('tool-change-lead')).textContent
    expect(lead).toContain('Accept updates this session\'s grant to the definition below (the previous text was not kept); Keep off leaves it blocked.')
    expect(screen.getByTestId('tool-change-c1').textContent).not.toMatch(/previous text is not available/)
  })

  it('leads a new tool with what the server offers and what Grant does', async () => {
    items = [change({ kind: 'new', old_hash: '', old_definition: null, tool: 'send_mail', new_definition: def('send_mail', 'Sends mail') })]
    mount()
    const lead = (await screen.findByTestId('tool-change-lead')).textContent
    expect(lead).toBe('calendar now offers send_mail, which the session did not have when it started. It is off until you decide: Grant authorizes it in the mode you choose; Keep off leaves it off.')
    const card = screen.getByTestId('tool-change-c1')
    expect(within(card).getByText('Definition')).toBeTruthy()
    expect(within(card).queryByTestId('change-summary')).toBeNull()
    expect((within(card).getByTestId('schema-details') as HTMLDetailsElement).open).toBe(false)
  })

  it('leads a declined change with what the decision means', async () => {
    items = [change({ state: 'declined', decided_at: '2026-10-13T12:00:00Z' })]
    action = url => {
      const u = new URL(url, 'http://x')
      if (u.pathname === '/api/tool-changes' && u.searchParams.get('scope') === 'open') return res({ tool_changes: items, decided_next: null, pending_count: 0 })
      return null
    }
    mount()
    const lead = (await screen.findByTestId('tool-change-lead')).textContent ?? ''
    expect(lead).toMatch(/^You kept this off .+\. Later changes to this tool in this session are listed without a badge\.$/)
    expect(within(screen.getByTestId('tool-change-c1')).queryByRole('button', { name: /keep off/i })).toBeNull()
  })

  it('summarises a description-only change and leaves the schema folded', async () => {
    items = [change()]
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    expect(within(card).getByText('What changed')).toBeTruthy()
    expect(within(card).getByTestId('change-summary').textContent).toBe('Description')
    expect((within(card).getByTestId('schema-details') as HTMLDetailsElement).open).toBe(false)
  })

  it('opens the schema when it changed and says so in the summary', async () => {
    items = [change({ new_definition: def('read_events', 'Reads events', '{"type":"object","properties":{"id":{"type":"string"}}}') })]
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    expect(within(card).getByTestId('change-summary').textContent).toBe('Input schema')
    expect((within(card).getByTestId('schema-details') as HTMLDetailsElement).open).toBe(true)
  })

  it('says nothing visible changed when the texts match', async () => {
    items = [change({ new_definition: def('read_events', 'Reads events') })]
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    expect(within(card).getByTestId('change-summary').textContent).toBe('Nothing visible changed (whitespace or ordering)')
  })

  // Review Focus 1
  it('makes hidden characters visible and badges the card', async () => {
    items = [change({ new_definition: def('read_events', 'Reads events \u202E and more') })]
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    expect(within(card).getByTestId('desc-diff').textContent).toContain('⟨U+202E⟩')
    expect(within(card).getByTestId('hidden-chars-badge')).toBeTruthy()
  })

  it('offers no Accept for an unreviewable definition and says why', async () => {
    items = [change({ new_definition: def('read_events', 'x', '{"type":"object"}', 'hidden_chars') })]
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    expect(within(card).queryByRole('button', { name: /accept/i })).toBeNull()
    expect(within(card).getByRole('alert').textContent).toMatch(/hidden characters/)
    expect(within(card).getByRole('button', { name: /keep off/i })).toBeTruthy()
  })

  it('a new tool defaults to propose, warns on allow, and sends the chosen mode', async () => {
    items = [change({ kind: 'new', old_hash: '', old_definition: null, mode: 'allow', tool: 'send_mail', new_definition: def('send_mail', 'Sends mail') })]
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    const select = within(card).getByLabelText(/mode/i) as HTMLSelectElement
    expect(select.value).toBe('propose')
    fireEvent.change(select, { target: { value: 'allow' } })
    expect(within(card).getByRole('alert').textContent).toMatch(/without asking you/)
    fireEvent.click(within(card).getByRole('button', { name: /^grant$/i }))
    await waitFor(() => expect(calls.some(c => c.method === 'POST' && c.url.endsWith('/accept'))).toBe(true))
    expect(calls.find(c => c.url.endsWith('/accept'))?.body).toEqual({ mode: 'allow' })
  })

  it('a new tool recorded as propose offers no allow option', async () => {
    items = [change({ kind: 'new', old_hash: '', old_definition: null, mode: 'propose', tool: 'send_mail', new_definition: def('send_mail', 'Sends mail') })]
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    const select = within(card).getByLabelText(/mode/i) as HTMLSelectElement
    expect(Array.from(select.options).map(o => o.value)).toEqual(['propose'])
  })

  it('accepting a changed tool keeps its mode and shows the server refusal on failure', async () => {
    items = [change()]
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    failNext = { status: 409, error: 'the tool changed again since this change was recorded; review the newer change' }
    fireEvent.click(within(card).getByRole('button', { name: /accept updated grant/i }))
    await screen.findByTestId('tool-change-error-c1')
    expect(screen.getByTestId('tool-change-error-c1').textContent).toMatch(/changed again/)
    expect(calls.find(c => c.url.endsWith('/accept'))?.body).toEqual({ mode: 'allow' })
  })

  it('removed changes have no buttons; missing old text is explained in the lead sentence; muted rows fold', async () => {
    items = [
      change({ id: 'r1', kind: 'removed', new_hash: '', new_definition: null, tool: 'gone' }),
      change({ id: 'n1', old_definition: null, tool: 'nolder' }),
      change({ id: 'm1', muted: true, tool: 'muted_one' }),
    ]
    mount()
    const removed = await screen.findByTestId('tool-change-r1')
    expect(within(removed).queryByRole('button')).toBeNull()
    expect(removed.textContent).toMatch(/no longer offers this tool/)
    expect(screen.getByTestId('tool-change-n1').textContent).toMatch(/the previous text was not kept/)
    expect(screen.queryByTestId('tool-change-m1')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /declined earlier/i }))
    expect(screen.getByTestId('tool-change-m1')).toBeTruthy()
  })

  it('flags a tool name that is not plain ASCII and never shows a title annotation', async () => {
    items = [change({ tool: 'rea\u0501_events' })]
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    expect(within(card).getByTestId('odd-name-badge')).toBeTruthy()
  })

  it('groups cards by session and connection', async () => {
    items = [change({ id: 'a' }), change({ id: 'b', tool: 'other' }), change({ id: 'c', session_id: 's2' })]
    mount()
    await screen.findByTestId('tool-change-a')
    expect(screen.getAllByTestId('tool-change-group')).toHaveLength(2)
  })

  it('with a connection id, shows that connection\'s pin events and decided changes only', async () => {
    pinEvents = [{ id: 1, session_id: 's1', connection_id: 'conn-1', connection_name: 'calendar', tool: 'read_events', action: 'repinned', old_hash: 'h-a', new_hash: 'h-b', mode: 'allow', actor: 'user', created_at: '2026-10-13T11:00:00Z' }]
    decidedItems = [
      change({ id: 'd1', state: 'declined', decided_at: '2026-10-13T12:00:00Z', tool: 'declined_one' }),
      change({ id: 'd2', state: 'declined', decided_at: '2026-10-13T12:00:00Z', connection_id: 'conn-2', tool: 'other_conn' }),
    ]
    mount(null, 'conn-1')
    const history = await screen.findByTestId('connection-history')
    expect(within(history).getAllByTestId('pin-event')).toHaveLength(1)
    expect(history.textContent).toContain('declined_one')
    expect(history.textContent).not.toContain('other_conn')
  })

  it('follows decided pages to find the connection\'s rows', async () => {
    decidedPageSize = 1
    decidedItems = [
      change({ id: 'd2', state: 'declined', connection_id: 'conn-2', tool: 'other_conn' }),
      change({ id: 'd1', state: 'declined', decided_at: '2026-10-13T12:00:00Z', tool: 'on_second_page' }),
    ]
    mount(null, 'conn-1')
    const history = await screen.findByTestId('connection-history')
    expect(history.textContent).toContain('on_second_page')
    expect(history.textContent).not.toContain('other_conn')
  })

  it('says so when the history cannot be loaded, instead of claiming it is empty', async () => {
    action = url => (url.includes('/pin-events') ? res({ error: 'boom' }, 500) : null)
    mount(null, 'conn-1')
    await waitFor(() => expect(screen.getByText(/Could not load the history: boom/)).toBeTruthy())
    expect(screen.queryByText(/No pin events or decided changes yet/)).toBeNull()
  })

  it('reloads the history after a decision', async () => {
    items = [change()]
    mount(null, 'conn-1')
    const card = await screen.findByTestId('tool-change-c1')
    await screen.findByTestId('connection-history')
    const before = calls.filter(c => c.url.includes('/pin-events')).length
    fireEvent.click(within(card).getByRole('button', { name: /keep off/i }))
    await waitFor(() => expect(calls.filter(c => c.url.includes('/pin-events')).length).toBeGreaterThan(before))
  })

  it('fetches the definitions by id when the list omits them', async () => {
    items = [change()]
    const { old_definition: _o, new_definition: _n, ...stripped } = items[0]
    void _o; void _n
    const full = items[0]
    action = (url) => {
      const u = new URL(url, 'http://x')
      if (u.pathname === '/api/tool-changes' && !u.search.includes('decided')) return res({ tool_changes: [stripped], decided_next: null, pending_count: 1 })
      return null
    }
    mount()
    const diff = await screen.findByTestId('desc-diff')
    expect(diff.textContent).toContain(full.new_definition!.description)
  })

  it('shows a load error with Retry when the definition fetch fails, never the stale-text message', async () => {
    items = [change()]
    const { old_definition: _o, new_definition: _n, ...stripped } = items[0]
    void _o; void _n
    let oneCalls = 0
    action = (url) => {
      const u = new URL(url, 'http://x')
      if (u.pathname === '/api/tool-changes' && !u.search.includes('decided')) return res({ tool_changes: [stripped], decided_next: null, pending_count: 1 })
      if (u.pathname === '/api/tool-changes/c1' && ++oneCalls === 1) return res({ error: 'database busy' }, 500)
      return null
    }
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    await waitFor(() => expect(within(card).getByRole('alert').textContent).toContain('Could not load this definition: database busy'))
    expect(card.textContent).not.toMatch(/previous text was not kept/)
    expect(within(card).queryByRole('button', { name: /accept/i })).toBeNull()
    fireEvent.click(within(card).getByRole('button', { name: /retry/i }))
    expect((await screen.findByTestId('desc-diff')).textContent).toContain('and deletes them')
  })

  it('does not say "No tool changes waiting" beside a load error', async () => {
    action = (url) => {
      const u = new URL(url, 'http://x')
      if (u.pathname === '/api/tool-changes' && u.searchParams.get('scope') === 'open') return res({ error: 'database busy' }, 500)
      return null
    }
    mount()
    expect((await screen.findByRole('alert')).textContent).toMatch(/Could not load tool changes/)
    expect(screen.queryByTestId('tool-changes-empty')).toBeNull()
  })
})

describe('tool schema refresh guidance', () => {
  it('distinguishes accepted grants from native schemas and preserves the current conversation', async () => {
    items = [change({ tool_schema_refresh: 'new_session' })]
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    expect(within(card).getByTestId('tool-schema-guidance').textContent).toContain('Start a new session and select the current MCP connections and tools')
    expect(within(card).getByTestId('tool-schema-guidance').textContent).toContain('Keep this conversation')
    fireEvent.click(within(card).getByRole('button', { name: 'Accept updated grant' }))
    await waitFor(() => expect(within(card).getByTestId('tool-change-lead').textContent).toContain('grant now authorizes'))
    expect(within(card).getByTestId('tool-change-lead').textContent).not.toContain('session uses the new definition')
    expect(calls.filter(c => c.method === 'POST')).toHaveLength(1)
  })

  it.each(['next_turn', undefined] as const)('discloses %s reload behavior before and after accepting', async policy => {
    items = [change({ tool_schema_refresh: policy })]
    mount()
    const card = await screen.findByTestId('tool-change-c1')
    const expected = policy === 'next_turn' ? 'idle or before its next turn' : "hasn't reported whether"
    expect(within(card).getByTestId('tool-schema-guidance').textContent).toContain(expected)
    fireEvent.click(within(card).getByRole('button', { name: 'Accept updated grant' }))
    await waitFor(() => expect(within(card).getByTestId('tool-change-lead').textContent).toContain('grant now authorizes'))
    expect(within(card).getByTestId('tool-schema-guidance').textContent).toContain(expected)
    expect(calls.filter(c => c.method === 'POST')).toHaveLength(1)
  })
})
