import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import CronForm from './CronForm'
import { FOCUS_BOARD_PROMPT } from '../lib/focusPrompt'
import type { CronInfo } from '../types'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const CONNS = [
  { id: 'c1', name: 'calendar', url: 'https://x/mcp', auth_kind: 'static', status: 'ok', default_tools: { read: { mode: 'allow', hash: 'h-read' } } },
]
const TOOLS = [
  { name: 'read', description: 'reads', hash: 'h-read', annotations: { readOnlyHint: true } },
  { name: 'list', description: 'lists', hash: 'h-list', annotations: { readOnlyHint: true } },
  { name: 'send', description: 'sends', hash: 'h-send' },
]
const BROWSER_TZ = Intl.DateTimeFormat().resolvedOptions().timeZone

function ok(body: unknown, status = 200) {
  return Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) })
}

let posted: { url: string; method: string; body: Record<string, unknown> }[]
let saveResponse: () => Promise<unknown>

beforeEach(() => {
  posted = []
  apiFetch.mockReset()
  saveResponse = () => ok({ id: 'new-cron' }, 201)
  apiFetch.mockImplementation((url: string, init?: RequestInit) => {
    if (init?.method === 'POST' || init?.method === 'PATCH') {
      posted.push({ url, method: init.method, body: JSON.parse(String(init.body)) })
      return saveResponse()
    }
    if (url === '/api/mcp/connections') return ok({ connections: CONNS })
    if (url === '/api/mcp/connections/c1/tools') return ok({ tools: TOOLS })
    if (url === '/api/daemons') return ok([{ id: 'd1', name: 'workstation', mode: 'local', repos_root: '/r', status: 'connected' }])
    if (url === '/api/boards') return ok([{ id: 'b1', name: 'Focus', description: null, repos: [], default_daemon_id: null, created_at: '', updated_at: '' }])
    if (url.startsWith('/api/models/')) return ok({ models: [], source: 'none' })
    return ok({ error: 'unexpected ' + url }, 404)
  })
})

function fill(id: string, value: string) {
  fireEvent.change(document.getElementById(id)!, { target: { value } })
}

async function renderForm(cron?: CronInfo) {
  const onSaved = vi.fn()
  const onCancel = vi.fn()
  render(<CronForm cron={cron} onSaved={onSaved} onCancel={onCancel} />)
  await screen.findByTestId('mcp-picker')
  return { onSaved, onCancel }
}

const save = () => fireEvent.click(screen.getByRole('button', { name: /^(create|save)/i }))

function existing(over: Partial<CronInfo> = {}): CronInfo {
  return {
    id: 'c9', name: 'Digest', status: 'active', enabled: true, schedule: '30 8,17 * * 1-5', timezone: 'Europe/Paris', prompt: 'Summarise', engine: 'claude',
    model: null, effort: null, runtime: 'docker', daemon_id: 'd1', board_id: 'b1',
    mcp: [{ connection: 'c1', tools: { read: { mode: 'allow', hash: 'h-read' } } }],
    token_expires_at: '2027-01-01T00:00:00Z', grace_seconds: 600, max_runtime_seconds: 900, next_run_at: '2026-10-01T06:30:00Z',
    last_run_at: null, consecutive_failures: 0, paused_reason: null, last_run: null, connection_problems: [],
    created_at: '', updated_at: '', ...over,
  }
}

describe('CronForm', () => {
  it('gives every control of the form an id and a label', async () => {
    await renderForm()
    fireEvent.click(screen.getByRole('radio', { name: /advanced/i }))
    const controls = [...document.querySelectorAll('input, select, textarea')].filter(
      el => !el.closest('[data-testid="cron-mcp"]') && !el.closest('[data-testid="model-picker"]'),
    )
    expect(controls.length).toBeGreaterThan(8)
    const ids = new Set<string>()
    for (const el of controls) {
      expect(el.id, el.outerHTML).not.toBe('')
      expect(ids.has(el.id), `duplicate id ${el.id}`).toBe(false)
      ids.add(el.id)
      const labelled = document.querySelector(`label[for="${el.id}"]`) || el.getAttribute('aria-label') || el.closest('label')
      expect(labelled, `no label for ${el.id}`).toBeTruthy()
    }
  })

  it('states what an unattended run can and cannot do', async () => {
    await renderForm()
    const notes = screen.getByTestId('cron-notes')
    expect(notes).toHaveTextContent(/no shell or web tools/i)
    expect(notes).toHaveTextContent(/private to you/i)
    expect(notes).toHaveTextContent(/desktop/i)
    expect(notes).toHaveTextContent(/host developer's own Claude login/i)
    expect(notes).toHaveTextContent(/connected daemon/i)
  })

  it('creates a cron with the payload the server takes, defaulting to the browser timezone', async () => {
    const { onSaved } = await renderForm()
    fill('cron-name', 'Morning digest')
    fill('cron-prompt', 'Summarise my inbox')
    save()
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].url).toBe('/api/crons')
    expect(posted[0].method).toBe('POST')
    expect(posted[0].body).toEqual({
      name: 'Morning digest', schedule: '0 8 * * *', timezone: BROWSER_TZ, prompt: 'Summarise my inbox',
      runtime: 'auto', mcp: [], grace_seconds: 3600, max_runtime_seconds: 1800,
    })
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
  })

  it('builds the schedule from N times a day and a timezone', async () => {
    await renderForm()
    fill('cron-name', 'x')
    fill('cron-prompt', 'y')
    fill('cron-times', '3')
    fill('cron-time-0', '08:30')
    fill('cron-time-1', '12:30')
    fill('cron-time-2', '17:30')
    fill('cron-timezone', 'UTC')
    expect(screen.getByTestId('cron-schedule-words')).toHaveTextContent('3 times a day, at 08:30, 12:30 and 17:30')
    save()
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body.schedule).toBe('30 8,12,17 * * *')
    expect(posted[0].body.timezone).toBe('UTC')
  })

  it('takes an advanced five-field expression', async () => {
    await renderForm()
    fill('cron-name', 'x')
    fill('cron-prompt', 'y')
    fireEvent.click(screen.getByRole('radio', { name: /advanced/i }))
    fill('cron-expression', '*/20 * * * *')
    save()
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body.schedule).toBe('*/20 * * * *')
  })

  it('does not post an incomplete form and says what is missing', async () => {
    await renderForm()
    save()
    expect(await screen.findByTestId('cron-form-error')).toHaveTextContent(/name/i)
    fill('cron-name', 'x')
    save()
    expect(screen.getByTestId('cron-form-error')).toHaveTextContent(/prompt/i)
    fill('cron-prompt', 'y')
    fireEvent.click(screen.getByRole('radio', { name: /advanced/i }))
    fill('cron-expression', '0 8 * *')
    save()
    expect(screen.getByTestId('cron-form-error')).toHaveTextContent(/five fields/i)
    expect(posted).toHaveLength(0)
  })

  it('refuses times whose minutes differ', async () => {
    await renderForm()
    fill('cron-name', 'x')
    fill('cron-prompt', 'y')
    fill('cron-times', '2')
    fill('cron-time-0', '08:00')
    fill('cron-time-1', '17:30')
    save()
    expect(screen.getByTestId('cron-form-error')).toHaveTextContent(/same minute/i)
    expect(posted).toHaveLength(0)
  })

  it('shows the server validation error and keeps the form', async () => {
    saveResponse = () => ok({ error: 'schedule runs more often than every 15 minutes' }, 400)
    const { onSaved } = await renderForm()
    fill('cron-name', 'x')
    fill('cron-prompt', 'y')
    save()
    expect(await screen.findByTestId('cron-form-error')).toHaveTextContent('schedule runs more often than every 15 minutes')
    expect(onSaved).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: /^create/i })).not.toBeDisabled()
  })

  it('sends only the tools chosen by hand, with the hash seen: nothing defaults on', async () => {
    await renderForm()
    fill('cron-name', 'x')
    fill('cron-prompt', 'y')
    fireEvent.click(await screen.findByRole('checkbox', { name: 'calendar' }))
    await screen.findByTestId('mcp-tool-calendar-read')
    // The bulk "select all read-only" shortcut is not offered, and every tool starts off.
    expect(screen.queryByTestId('mcp-readonly-calendar')).toBeNull()
    for (const t of ['read', 'list', 'send']) {
      expect((screen.getByLabelText(`calendar ${t} mode`) as HTMLSelectElement).value).toBe('off')
    }
    // With nothing chosen the connection is not part of the payload.
    save()
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body.mcp).toEqual([])
  })

  it('sends the explicit mode and hash of each chosen tool', async () => {
    await renderForm()
    fill('cron-name', 'x')
    fill('cron-prompt', 'y')
    fireEvent.click(await screen.findByRole('checkbox', { name: 'calendar' }))
    await screen.findByTestId('mcp-tool-calendar-read')
    fireEvent.change(screen.getByLabelText('calendar read mode'), { target: { value: 'allow' } })
    save()
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body.mcp).toEqual([{ connection: 'c1', tools: { read: { mode: 'allow', hash: 'h-read' } } }])
  })

  it('offers the daemon pin and the target board, and sends them', async () => {
    await renderForm()
    fill('cron-name', 'x')
    fill('cron-prompt', 'y')
    fill('cron-runtime', 'docker')
    await screen.findByRole('option', { name: /workstation/ })
    fill('cron-daemon', 'd1')
    await screen.findByRole('option', { name: 'Focus' })
    fill('cron-board', 'b1')
    save()
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body).toMatchObject({ runtime: 'docker', daemon_id: 'd1', board_id: 'b1' })
  })

  it('edits an existing cron with a PATCH that can clear fields', async () => {
    const { onSaved } = await renderForm(existing())
    expect((document.getElementById('cron-name') as HTMLInputElement).value).toBe('Digest')
    expect((document.getElementById('cron-timezone') as HTMLSelectElement).value).toBe('Europe/Paris')
    expect(screen.getByTestId('cron-schedule-words')).toHaveTextContent('Weekdays at 08:30 and 17:30')
    await screen.findByRole('option', { name: /workstation/ })
    fill('cron-board', '')
    fill('cron-name', 'Digest 2')
    fireEvent.click(screen.getByRole('button', { name: /^save/i }))
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].url).toBe('/api/crons/c9')
    expect(posted[0].method).toBe('PATCH')
    expect(posted[0].body).toMatchObject({
      name: 'Digest 2', schedule: '30 8,17 * * 1-5', timezone: 'Europe/Paris', runtime: 'docker', daemon_id: 'd1', board_id: '',
      model: '', effort: '', grace_seconds: 600, max_runtime_seconds: 900,
    })
    // The tools were not touched, so `mcp` is not sent at all: the server would judge it against
    // the live connections, and an edit of the name must never depend on them.
    expect(posted[0].body).not.toHaveProperty('mcp')
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
  })

  it('sends mcp on an edit only when the picker value changed', async () => {
    await renderForm(existing())
    await screen.findByTestId('mcp-tool-calendar-read')
    fireEvent.change(screen.getByLabelText('calendar list mode'), { target: { value: 'allow' } })
    fireEvent.click(screen.getByRole('button', { name: /^save/i }))
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body.mcp).toEqual([{ connection: 'c1', tools: { read: { mode: 'allow', hash: 'h-read' }, list: { mode: 'allow', hash: 'h-list' } } }])
  })

  it('a cron whose connection is gone is editable: the orphan is shown, and a prompt-only edit sends no mcp', async () => {
    const orphan = { connection: 'gone-1', tools: { read: { mode: 'allow' as const, hash: 'h-old' } } }
    await renderForm(existing({ mcp: [orphan] }))
    expect(screen.getByTestId('mcp-orphan-gone-1')).toHaveTextContent(/no longer exists/i)
    fill('cron-prompt', 'Summarise differently')
    fireEvent.click(screen.getByRole('button', { name: /^save/i }))
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body.prompt).toBe('Summarise differently')
    expect(posted[0].body).not.toHaveProperty('mcp')
  })

  it('removing an orphaned connection sends the mcp value without it', async () => {
    const orphan = { connection: 'gone-1', tools: { read: { mode: 'allow' as const, hash: 'h-old' } } }
    await renderForm(existing({ mcp: [orphan] }))
    fireEvent.click(screen.getByRole('button', { name: /remove missing connection gone-1/i }))
    expect(screen.queryByTestId('mcp-orphan-gone-1')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /^save/i }))
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body.mcp).toEqual([])
  })

  it('sends an explicit zero catch-up window on create and on edit', async () => {
    await renderForm()
    fill('cron-name', 'No catch-up')
    fill('cron-prompt', 'go')
    fill('cron-grace', '0')
    save()
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body.grace_seconds).toBe(0)
  })

  it('opens a cron whose catch-up window is 0 as 0, not the default', async () => {
    await renderForm(existing({ grace_seconds: 0 }))
    expect((document.getElementById('cron-grace') as HTMLInputElement).value).toBe('0')
  })

  it('a cleared catch-up field is not 0: a create omits it so the server default applies', async () => {
    await renderForm()
    fill('cron-name', 'Default window')
    fill('cron-prompt', 'go')
    fill('cron-grace', '')
    save()
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body).not.toHaveProperty('grace_seconds')
  })

  it('a cleared catch-up field on an edit leaves the stored value unchanged (not sent)', async () => {
    await renderForm(existing({ grace_seconds: 600 }))
    fill('cron-grace', '')
    save()
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body).not.toHaveProperty('grace_seconds')
  })

  it('opens a stored 90 s window in seconds and saves it back as 90, not 120', async () => {
    await renderForm(existing({ grace_seconds: 90 }))
    expect((document.getElementById('cron-grace') as HTMLInputElement).value).toBe('90')
    expect((document.getElementById('cron-grace-unit') as HTMLSelectElement).value).toBe('seconds')
    fill('cron-name', 'Renamed')
    save()
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body.grace_seconds).toBe(90)
  })

  it('shows a whole number of minutes in minutes, and saves minutes * 60', async () => {
    await renderForm(existing({ grace_seconds: 600 }))
    expect((document.getElementById('cron-grace') as HTMLInputElement).value).toBe('10')
    expect((document.getElementById('cron-grace-unit') as HTMLSelectElement).value).toBe('minutes')
    fill('cron-grace', '15')
    save()
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body.grace_seconds).toBe(900)
  })

  it('accepts seconds through the unit select and converts the shown value', async () => {
    await renderForm()
    fill('cron-name', 'Seconds')
    fill('cron-prompt', 'go')
    fill('cron-grace', '2')
    fill('cron-grace-unit', 'seconds')
    expect((document.getElementById('cron-grace') as HTMLInputElement).value).toBe('120')
    fill('cron-grace', '45')
    save()
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0].body.grace_seconds).toBe(45)
  })

  it('refuses a catch-up window that is not a whole number of seconds', async () => {
    await renderForm()
    fill('cron-name', 'Bad')
    fill('cron-prompt', 'go')
    fill('cron-grace-unit', 'seconds')
    fill('cron-grace', '1.5')
    save()
    expect(await screen.findByRole('alert')).toHaveTextContent(/catch-up/i)
    expect(posted).toHaveLength(0)
  })

  it('opens a schedule the presets cannot say in the advanced field', async () => {
    await renderForm(existing({ schedule: '*/20 * * * *' }))
    expect((document.getElementById('cron-expression') as HTMLInputElement).value).toBe('*/20 * * * *')
  })

  it('cancels without saving', async () => {
    const { onCancel } = await renderForm()
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(onCancel).toHaveBeenCalled()
    expect(posted).toHaveLength(0)
  })

  describe('focus-board starter prompt', () => {
    const clickStarter = () => fireEvent.click(screen.getByRole('button', { name: /use the focus-board starter prompt/i }))
    const promptValue = () => (document.getElementById('cron-prompt') as HTMLTextAreaElement).value

    it('inserts the starter prompt straight away when the prompt is empty', async () => {
      await renderForm()
      clickStarter()
      expect(promptValue()).toBe(FOCUS_BOARD_PROMPT)
      expect(screen.queryByTestId('focus-prompt-confirm')).toBeNull()
    })

    it('asks before replacing a prompt that has text, and keeps it on Keep', async () => {
      await renderForm()
      fill('cron-prompt', 'my own prompt')
      clickStarter()
      expect(promptValue()).toBe('my own prompt')
      expect(screen.getByTestId('focus-prompt-confirm')).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: /keep my prompt/i }))
      expect(promptValue()).toBe('my own prompt')
      expect(screen.queryByTestId('focus-prompt-confirm')).toBeNull()
    })

    it('replaces the prompt after the person confirms', async () => {
      await renderForm()
      fill('cron-prompt', 'my own prompt')
      clickStarter()
      fireEvent.click(screen.getByRole('button', { name: /replace my prompt/i }))
      expect(promptValue()).toBe(FOCUS_BOARD_PROMPT)
      expect(screen.queryByTestId('focus-prompt-confirm')).toBeNull()
    })

    it('treats a whitespace-only prompt as empty', async () => {
      await renderForm()
      fill('cron-prompt', '  \n ')
      clickStarter()
      expect(promptValue()).toBe(FOCUS_BOARD_PROMPT)
    })

    it('mentions the focus board template beside the target board field', async () => {
      await renderForm()
      expect(screen.getByTestId('cron-board-template-note').textContent).toMatch(/focus board/i)
    })
  })
})
