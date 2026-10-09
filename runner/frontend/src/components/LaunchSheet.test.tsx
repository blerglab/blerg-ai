import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import LaunchSheet from './LaunchSheet'
import { useSessionStore } from '../hooks/useSessionStore'
import { coreOrigin } from '../authClient'
import { ACK_AGENT, ACK_TERMINAL, AGENT_NO_PROMPTS, NO_DAEMON_REASON, TERMINAL_NEEDS_DAEMON } from '../lib/runtimes'

// Pin randomSessionName to a stable value so assertions are deterministic.
vi.mock('../sessionNames', () => ({
  randomSessionName: () => 'Granite',
}))

const AUTO_NAME = 'Granite'

function buildFetchMock(
  captureBody: { value?: Record<string, unknown> },
  opts?: {
    daemon?: Record<string, unknown>
    clusterStatus?: Record<string, unknown>
    // daemons: [] models "no workstation daemon connected at all".
    daemons?: Record<string, unknown>[]
    // myCreds: the /api/me/credentials answer. 'fail' models a fetch that
    // never resolves usefully — the UI must treat that as "unknown".
    myCreds?: Record<string, unknown> | 'fail'
    // models: the GET /api/models/claude answer; 'fail' = a 500. Omitted, the
    // catch-all below answers {} — unusable, so the built-in list is shown.
    models?: Record<string, unknown> | 'fail'
    // modelsByEngine: GET /api/models/<engine> answers for other engines.
    modelsByEngine?: Record<string, Record<string, unknown>>
    // modelRequests: collects every GET /api/models/… path (+ query) asked.
    modelRequests?: string[]
    // extraRepos: appended to the default /api/repos list (e.g. local-only
    // folders with or without a known GitHub remote).
    extraRepos?: Record<string, unknown>[]
    // reposStaleAt: /api/repos's stale_at — a provider list failed to refresh.
    reposStaleAt?: string
    // noRepos: /api/repos answers an empty list (nothing checked out, no provider).
    noRepos?: boolean
    // mcpConnections / mcpTools: the runner's MCP connection list and (for connection c1) its tools.
    mcpConnections?: Record<string, unknown>[]
    mcpTools?: Record<string, unknown>[]
  },
) {
  return vi.fn((url: string, init?: RequestInit) => {
    if (url === '/api/mcp/connections') {
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ connections: opts?.mcpConnections ?? [] }) })
    }
    if (url === '/api/mcp/connections/c1/tools') {
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ tools: opts?.mcpTools ?? [] }) })
    }
    // modelsByEngine keys: "<engine>?daemon_id=<id>" for one daemon's answer,
    // or just "<engine>" for any daemon (and none).
    const modelsPath = url.startsWith('/api/models/') ? url.slice('/api/models/'.length) : undefined
    if (modelsPath !== undefined) opts?.modelRequests?.push(modelsPath)
    const engineModels = modelsPath !== undefined
      ? (opts?.modelsByEngine?.[modelsPath] ?? opts?.modelsByEngine?.[modelsPath.split('?')[0]])
      : undefined
    if (engineModels) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(engineModels) })
    }
    if (modelsPath?.split('?')[0] === 'claude' && opts?.models !== undefined) {
      if (opts.models === 'fail') {
        return Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({}) })
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve(opts.models) })
    }
    if (url === '/api/repos') {
      return Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({
            repos: [
              ...(opts?.noRepos ? [] : [{
                name: 'myrepo',
                full_name: 'org/myrepo',
                // already checked out on the daemon — gives us "Launch Session" button text
                checked_out: ['workstation'],
              }]),
              ...(opts?.extraRepos ?? []),
            ],
            stale_at: opts?.reposStaleAt ?? null,
          }),
      })
    }
    if (url === '/api/daemons') {
      return Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve(opts?.daemons ?? [
            {
              id: 'd1',
              name: 'workstation',
              mode: 'local',
              repos_root: '/repos',
              status: 'connected',
              // The common case since Task 1: the daemon can run sessions in
              // its sandbox container, so Local sandbox is the default.
              sandbox_available: true,
              ...opts?.daemon,
            },
          ]),
      })
    }
    if (url === '/api/cluster/status') {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve(opts?.clusterStatus ?? { configured: false, available_engines: [] }),
      })
    }
    if (url === '/api/me/credentials') {
      if (opts?.myCreds === 'fail') return Promise.reject(new Error('boom'))
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve(opts?.myCreds ?? { engines: [], git: false, unavailable: true }),
      })
    }
    if (url === '/api/sessions') {
      if (init?.body) captureBody.value = JSON.parse(init.body as string)
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ session_id: 'new-sess' }),
        text: () => Promise.resolve(''),
      })
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) })
  })
}

// jsdom in this vitest setup doesn't provide a real localStorage — a minimal
// in-memory stand-in is enough to exercise the persistence logic under test.
function makeMemoryStorage(): Storage {
  const store = new Map<string, string>()
  return {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => void store.clear(),
    key: (i: number) => Array.from(store.keys())[i] ?? null,
    get length() { return store.size },
  }
}

const CLUSTER_ON = { configured: true, available_engines: ['claude'], git_configured: false }

describe('LaunchSheet', () => {
  let captureBody: { value?: Record<string, unknown> }

  beforeEach(() => {
    captureBody = {}
    vi.stubGlobal('fetch', buildFetchMock(captureBody))
    useSessionStore.setState({
      pendingSessionIds: {},
      failedSessions: {},
    })
    vi.stubGlobal('localStorage', makeMemoryStorage())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  async function renderAndWaitForLoad() {
    render(
      <MemoryRouter>
        <LaunchSheet open={true} onClose={vi.fn()} />
      </MemoryRouter>,
    )
    // Wait for repos/daemons/cluster status to land.
    await screen.findByText('Repository')
  }

  async function selectRepoAndSubmit(typedName?: string) {
    fireEvent.click(screen.getByText('myrepo'))
    if (typedName !== undefined) {
      const nameInput = screen.getByPlaceholderText(AUTO_NAME)
      fireEvent.change(nameInput, { target: { value: typedName } })
    }
    fireEvent.click(screen.getByText('Launch Session'))
  }

  it('renders a session name input with autoName as placeholder', async () => {
    await renderAndWaitForLoad()
    expect(screen.getByPlaceholderText(AUTO_NAME)).toBeInTheDocument()
  })

  it('uses autoName as title when name input is left blank', async () => {
    await renderAndWaitForLoad()
    await selectRepoAndSubmit()
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.title).toBe(AUTO_NAME)
  })

  it('uses the typed name as title when the user provides one', async () => {
    await renderAndWaitForLoad()
    await selectRepoAndSubmit('Night Shift')
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.title).toBe('Night Shift')
  })

  it('trims whitespace from the typed name', async () => {
    await renderAndWaitForLoad()
    await selectRepoAndSubmit('  Deep Refactor  ')
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.title).toBe('Deep Refactor')
  })

  it('2xx create whose JSON lacks session_id surfaces an error without navigating or pending', async () => {
    const mockAddPendingSession = vi.fn()
    useSessionStore.setState({ addPendingSession: mockAddPendingSession })

    // Override the sessions endpoint to return 200 with no session_id.
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        if (url === '/api/repos') {
          return Promise.resolve({
            ok: true,
            json: () =>
              Promise.resolve({
                repos: [{ name: 'myrepo', full_name: 'org/myrepo', checked_out: ['workstation'] }],
              }),
          })
        }
        if (url === '/api/daemons') {
          return Promise.resolve({
            ok: true,
            json: () =>
              Promise.resolve([
                { id: 'd1', name: 'workstation', mode: 'local', repos_root: '/repos', status: 'connected', sandbox_available: true },
              ]),
          })
        }
        if (url === '/api/sessions') {
          return Promise.resolve({
            ok: true,
            // session_id intentionally absent
            json: () => Promise.resolve({}),
            text: () => Promise.resolve(''),
          })
        }
        return Promise.resolve({ ok: true, json: () => Promise.resolve({}) })
      }),
    )

    render(
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route path="/" element={<LaunchSheet open={true} onClose={vi.fn()} />} />
          <Route path="/sessions/:id" element={<div data-testid="session-page">Session Page</div>} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByText('Repository')
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByText('Launch Session'))

    await waitFor(() => expect(screen.getByText(/Failed to launch session/)).toBeInTheDocument())
    expect(screen.queryByTestId('session-page')).not.toBeInTheDocument()
    expect(mockAddPendingSession).not.toHaveBeenCalled()
  })

  it('remembers the picked engine across launches (localStorage)', async () => {
    await renderAndWaitForLoad()
    fireEvent.click(screen.getByTestId('engine-codex'))
    await selectRepoAndSubmit()
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.engine).toBe('codex')
    expect(localStorage.getItem('blerg.launch.engine')).toBe('codex')
  })

  it('defaults the engine picker from a previously saved choice', async () => {
    localStorage.setItem('blerg.launch.engine', 'hermes')
    await renderAndWaitForLoad()
    await selectRepoAndSubmit()
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.engine).toBe('hermes')
  })

  it('ignores a bogus saved engine value and falls back to claude', async () => {
    localStorage.setItem('blerg.launch.engine', 'not-a-real-engine')
    await renderAndWaitForLoad()
    await selectRepoAndSubmit()
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.engine).toBeUndefined() // undefined = claude, the default
  })

  it('calls addPendingSession, closes the sheet, and navigates to the new session on 202', async () => {
    const mockAddPendingSession = vi.fn()
    useSessionStore.setState({ addPendingSession: mockAddPendingSession })
    const mockOnClose = vi.fn()

    render(
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route path="/" element={<LaunchSheet open={true} onClose={mockOnClose} />} />
          <Route path="/sessions/:id" element={<div data-testid="session-page">Session Page</div>} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByText('Repository')
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByText('Launch Session'))

    await waitFor(() => expect(mockAddPendingSession).toHaveBeenCalledWith('new-sess'))
    expect(screen.getByTestId('session-page')).toBeInTheDocument()
    expect(mockOnClose).toHaveBeenCalled()
  })
})

// ─── the Run column: defaults ────────────────────────────────────────────────

describe('LaunchSheet — Run column defaults', () => {
  let captureBody: { value?: Record<string, unknown> }

  beforeEach(() => {
    captureBody = {}
    useSessionStore.setState({ pendingSessionIds: {}, failedSessions: {} })
    vi.stubGlobal('localStorage', makeMemoryStorage())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  async function renderSheet(opts?: Parameters<typeof buildFetchMock>[1]) {
    vi.stubGlobal('fetch', buildFetchMock(captureBody, opts))
    render(
      <MemoryRouter>
        <LaunchSheet open={true} onClose={vi.fn()} />
      </MemoryRouter>,
    )
    await screen.findByText('Repository')
  }

  it('defaults to the cluster pod and an agent session when cluster is configured, even with a daemon connected', async () => {
    await renderSheet({ clusterStatus: CLUSTER_ON })
    expect(screen.getByTestId('runtime-cluster')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('runtime-docker')).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByTestId('kind-agent')).toHaveAttribute('aria-checked', 'true')
    // A cluster pod is throwaway — no acknowledgement to read.
    expect(screen.queryByTestId('agent-ack')).not.toBeInTheDocument()
  })

  it('defaults to the local sandbox when there is no cluster but the daemon has a sandbox image', async () => {
    await renderSheet()
    expect(screen.getByTestId('runtime-docker')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('kind-agent')).toHaveAttribute('aria-checked', 'true')
    expect(screen.queryByTestId('agent-ack')).not.toBeInTheDocument()
  })

  it('falls back to this machine only when nothing else can run the session, and then demands the acknowledgement', async () => {
    await renderSheet({ daemon: { sandbox_available: false } })
    expect(screen.getByTestId('runtime-daemon')).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(screen.getByText('myrepo'))
    expect(screen.getByLabelText(ACK_AGENT)).toBeInTheDocument()
    expect(screen.getByText(/Launch/).closest('button')).toBeDisabled()
    fireEvent.click(screen.getByTestId('agent-ack'))
    expect(screen.getByText(/Launch/).closest('button')).not.toBeDisabled()
  })

  it('keeps a runtime the user picked when kind and engine change around it', async () => {
    await renderSheet({ clusterStatus: CLUSTER_ON })
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    fireEvent.click(screen.getByTestId('kind-tmux'))
    fireEvent.click(screen.getByTestId('kind-agent'))
    fireEvent.click(screen.getByTestId('engine-codex'))
    expect(screen.getByTestId('runtime-daemon')).toHaveAttribute('aria-checked', 'true')
  })

  it('leaves nothing collapsed — no Advanced disclosure survives', async () => {
    await renderSheet()
    expect(document.querySelector('details')).toBeNull()
    expect(screen.queryByText('Advanced')).not.toBeInTheDocument()
  })

  it('offers no daemon picker when there is only one daemon', async () => {
    await renderSheet()
    expect(screen.queryByTestId('daemon-picker')).not.toBeInTheDocument()
    expect(screen.getByTestId('daemon-single')).toHaveTextContent('on workstation')
  })

  it('offers a daemon picker when more than one daemon is connected', async () => {
    await renderSheet({
      daemons: [
        { id: 'd1', name: 'alpha', mode: 'local', repos_root: '/repos', status: 'connected', sandbox_available: true },
        { id: 'd2', name: 'beta', mode: 'local', repos_root: '/repos', status: 'connected', sandbox_available: true },
      ],
    })
    expect(screen.getByTestId('daemon-picker')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('daemon-d2'))
    expect(screen.getByTestId('daemon-d2')).toHaveAttribute('aria-checked', 'true')
  })

  // At phone width the three columns stack, and the stack must not put a
  // greyed-out Launch above the acknowledgement that greys it out. jsdom
  // applies no stylesheet, so the CSS `order` values are mirrored onto
  // data-phone-order (see LaunchSheet.css's max-width:899px block).
  it('stacks Repository → Run → Details at phone width, with Launch last', async () => {
    await renderSheet()
    const order = (testid: string) =>
      Number(screen.getByTestId(testid).getAttribute('data-phone-order'))
    expect(order('launch-col-repo')).toBe(1)
    expect(order('launch-col-run')).toBe(2)
    expect(order('launch-col-config')).toBe(3)
    // The submit button lives in the column that stacks last.
    const config = screen.getByTestId('launch-col-config')
    expect(config).toContainElement(screen.getByText(/Launch/).closest('button'))
  })

  // The repo list's height comes from LaunchSheet.css (it fills the modal's row on
  // desktop instead of a fixed 200px). jsdom applies no stylesheet, so this pins the
  // structure the CSS depends on: the list sits inside the flexible wrapper inside the
  // stretching section, and carries no inline height cap that would defeat it.
  it('renders the repository list inside the fill-the-row wrapper with no inline height cap', async () => {
    await renderSheet()
    const list = screen.getByTestId('repo-list')
    expect(list).toHaveClass('launch-repo-list')
    expect(list.parentElement).toHaveClass('launch-repo-list-wrap')
    expect(list.parentElement?.parentElement).toHaveClass('launch-repo-section')
    expect(list.style.maxHeight).toBe('')
    expect(list.style.height).toBe('')
  })

  it('still tells the user sessions run as cluster pods when no daemon is connected', async () => {
    await renderSheet({ daemons: [], clusterStatus: CLUSTER_ON })
    expect(
      screen.getByText('No workstation daemon connected — sessions will run as cluster pods.'),
    ).toBeInTheDocument()
  })

  it('keeps the daemon install hint when no daemon is connected and cluster is not configured', async () => {
    await renderSheet({ daemons: [] })
    expect(screen.getByText(/No daemon connected\. Start it:/)).toBeInTheDocument()
  })
})

// ─── the Run column: what reaches the wire ───────────────────────────────────

describe('LaunchSheet — runtime and kind on the wire', () => {
  let captureBody: { value?: Record<string, unknown> }

  beforeEach(() => {
    captureBody = {}
    useSessionStore.setState({ pendingSessionIds: {}, failedSessions: {} })
    vi.stubGlobal('localStorage', makeMemoryStorage())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  async function renderSheet(opts?: Parameters<typeof buildFetchMock>[1]) {
    vi.stubGlobal('fetch', buildFetchMock(captureBody, opts))
    render(
      <MemoryRouter>
        <LaunchSheet open={true} onClose={vi.fn()} />
      </MemoryRouter>,
    )
    await screen.findByText('Repository')
  }

  it('sends cluster + agent', async () => {
    await renderSheet({ clusterStatus: CLUSTER_ON, myCreds: { engines: ['claude'], git: true } })
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.runtime).toBe('cluster')
    expect(captureBody.value!.kind).toBe('agent')
  })

  it('sends docker + agent', async () => {
    await renderSheet()
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.runtime).toBe('docker')
    expect(captureBody.value!.kind).toBe('agent')
  })

  it('sends docker + tmux', async () => {
    await renderSheet()
    fireEvent.click(screen.getByTestId('kind-tmux'))
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.runtime).toBe('docker')
    expect(captureBody.value!.kind).toBe('tmux')
  })

  it('refuses This machine + Agent + Claude when the daemon has neither the claude CLI nor an API key', async () => {
    await renderSheet({ daemon: { claude_cli_available: false, anthropic_key_set: false } })
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByTestId('agent-ack'))
    expect(screen.getByText(/Launch/)).toBeDisabled()
    expect(screen.getByTestId('launch-blocked-reason')).toHaveTextContent('ANTHROPIC_API_KEY')
    // Another engine on the same machine is fine.
    fireEvent.click(screen.getByTestId('engine-codex'))
    expect(screen.queryByTestId('launch-blocked-reason')).not.toBeInTheDocument()
  })

  it('warns before Launch when the daemon has no Claude credential for the Local sandbox, without blocking', async () => {
    await renderSheet({ daemon: { available_engines: [], sandbox_claude_credential: false } })
    expect(screen.getByTestId('runtime-docker')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('sandbox-claude-missing')).toHaveTextContent('no Claude login or API key')
    fireEvent.click(screen.getByText('myrepo'))
    // An advisory: the server is the real gate.
    expect(screen.getByText(/Launch/)).not.toBeDisabled()
    // Another engine takes the specific note away.
    fireEvent.click(screen.getByTestId('engine-codex'))
    expect(screen.queryByTestId('sandbox-claude-missing')).not.toBeInTheDocument()
  })

  it('reads a missing available_engines as none (not unknown) on a daemon that reports the sandbox credential', async () => {
    // Go sends an empty engine list as null.
    await renderSheet({ daemon: { available_engines: null, sandbox_claude_credential: false } })
    expect(screen.getByTestId('sandbox-claude-missing')).toBeInTheDocument()
  })

  it('does not turn a missing engine list into a Claude warning on This machine', async () => {
    // A keychain login or a CLAUDE_CODE_OAUTH_TOKEN in the service environment
    // runs Claude on the host without the credentials file available_engines
    // looks for, so a null list there still means "unknown".
    await renderSheet({ daemon: { available_engines: null, sandbox_claude_credential: false } })
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    expect(screen.getByTestId('runtime-daemon')).toHaveAttribute('aria-checked', 'true')
    expect(screen.queryByTestId('sandbox-claude-missing')).not.toBeInTheDocument()
    expect(screen.queryByTestId('engine-missing-warning')).not.toBeInTheDocument()
  })

  it('does not warn about the sandbox when the daemon has a token or key but no credentials file', async () => {
    // available_engines lacks claude (no ~/.claude credentials file), but the
    // daemon passes CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY in.
    await renderSheet({ daemon: { available_engines: [], sandbox_claude_credential: true } })
    expect(screen.getByTestId('runtime-docker')).toHaveAttribute('aria-checked', 'true')
    expect(screen.queryByTestId('sandbox-claude-missing')).not.toBeInTheDocument()
    expect(screen.queryByTestId('engine-missing-warning')).not.toBeInTheDocument()
  })

  it('says nothing about the sandbox credential for an older daemon that does not report it', async () => {
    await renderSheet({ daemon: { available_engines: null } })
    expect(screen.queryByTestId('sandbox-claude-missing')).not.toBeInTheDocument()
    expect(screen.queryByTestId('engine-missing-warning')).not.toBeInTheDocument()
  })

  it('shows a dismissible note when the repository list failed to refresh', async () => {
    await renderSheet({ reposStaleAt: '2026-09-27T10:00:00Z' })
    expect(screen.getByTestId('repos-stale-note')).toHaveTextContent(/check your token/i)
    fireEvent.click(screen.getByTestId('repos-stale-dismiss'))
    expect(screen.queryByTestId('repos-stale-note')).not.toBeInTheDocument()
  })

  it('explains how to add a repository when the list is empty', async () => {
    await renderSheet({ noRepos: true })
    const hint = screen.getByTestId('repos-empty-hint')
    expect(hint).toHaveTextContent(
      'Type owner/name or paste a Git URL to clone a repository, or connect GitHub/GitLab in Settings to list yours.',
    )
    expect(within(hint).getByRole('link', { name: 'Settings' })).toHaveAttribute('href', expect.stringMatching(/\/settings$/))
    // The picker still offers its two fixed entries.
    expect(screen.getByTestId('no-repo-option')).toBeInTheDocument()
  })

  it('hides the empty-list hint when repositories are listed', async () => {
    await renderSheet()
    expect(screen.queryByTestId('repos-empty-hint')).not.toBeInTheDocument()
  })

  it('shows no repository note when the list is fresh', async () => {
    await renderSheet()
    expect(screen.queryByTestId('repos-stale-note')).not.toBeInTheDocument()
  })

  it('allows This machine + Agent + Claude with the claude CLI and no key', async () => {
    await renderSheet({ daemon: { claude_cli_available: true, anthropic_key_set: false } })
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByTestId('agent-ack'))
    expect(screen.queryByTestId('launch-blocked-reason')).not.toBeInTheDocument()
    expect(screen.getByText(/Launch/)).not.toBeDisabled()
  })

  it('sends daemon + agent once acknowledged', async () => {
    await renderSheet()
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByTestId('agent-ack'))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.runtime).toBe('daemon')
    expect(captureBody.value!.kind).toBe('agent')
  })

  it('sends daemon + tmux once acknowledged', async () => {
    await renderSheet()
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    fireEvent.click(screen.getByTestId('kind-tmux'))
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByLabelText(ACK_TERMINAL))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.runtime).toBe('daemon')
    expect(captureBody.value!.kind).toBe('tmux')
  })
})

// ─── the Run column: rules between the options ───────────────────────────────

describe('LaunchSheet — Run column rules', () => {
  let captureBody: { value?: Record<string, unknown> }

  beforeEach(() => {
    captureBody = {}
    useSessionStore.setState({ pendingSessionIds: {}, failedSessions: {} })
    vi.stubGlobal('localStorage', makeMemoryStorage())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  async function renderSheet(opts?: Parameters<typeof buildFetchMock>[1]) {
    vi.stubGlobal('fetch', buildFetchMock(captureBody, opts))
    render(
      <MemoryRouter>
        <LaunchSheet open={true} onClose={vi.fn()} />
      </MemoryRouter>,
    )
    await screen.findByText('Repository')
  }

  it('disables Terminal on the cluster pod, with the reason', async () => {
    await renderSheet({ clusterStatus: CLUSTER_ON })
    expect(screen.getByTestId('kind-tmux')).toBeDisabled()
    expect(screen.getByTestId('terminal-disabled-reason')).toHaveTextContent(TERMINAL_NEEDS_DAEMON)
    // ...and it comes back on a runtime that can host a terminal.
    fireEvent.click(screen.getByTestId('runtime-docker'))
    expect(screen.getByTestId('kind-tmux')).not.toBeDisabled()
  })

  it('disables the cluster card with a reason when no cluster is configured', async () => {
    await renderSheet()
    expect(screen.getByTestId('runtime-cluster')).toBeDisabled()
    expect(screen.getByTestId('runtime-cluster-reason')).toHaveTextContent(
      'No cluster runtime is configured on this server.',
    )
  })

  it('disables both daemon-backed cards when no daemon is connected', async () => {
    await renderSheet({ daemons: [], clusterStatus: CLUSTER_ON })
    expect(screen.getByTestId('runtime-docker')).toBeDisabled()
    expect(screen.getByTestId('runtime-daemon')).toBeDisabled()
    expect(screen.getByTestId('runtime-cluster')).not.toBeDisabled()
  })

  it('warns, without blocking, when the daemon has no sandbox image built', async () => {
    await renderSheet({ daemon: { sandbox_available: false } })
    // Default landed on this machine; choosing the sandbox anyway is allowed.
    fireEvent.click(screen.getByTestId('runtime-docker'))
    expect(screen.getByTestId('sandbox-image-warning')).toHaveTextContent(
      "workstation hasn't built the sandbox image yet",
    )
    expect(screen.getByTestId('runtime-docker')).not.toBeDisabled()
  })

  // Unattended: one box, off by default, and the launch always says which.
  it('launches interactive unless Unattended is ticked', async () => {
    await renderSheet()
    const box = screen.getByTestId('unattended-toggle')
    expect(box).not.toBeChecked()
    expect(screen.getByLabelText(/Unattended/)).toBe(box)
    expect(screen.getByTestId('launch-unattended')).toHaveTextContent(
      'Nobody is watching: the agent decides and finishes on its own instead of discussing and waiting for you.',
    )
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.interaction).toBe('interactive')
  })

  it('launches unattended when the box is ticked', async () => {
    await renderSheet()
    fireEvent.click(screen.getByTestId('unattended-toggle'))
    expect(screen.getByTestId('unattended-toggle')).toBeChecked()
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.interaction).toBe('unattended')
  })

  it('offers Unattended for an agent session only, and a terminal launch sends none', async () => {
    await renderSheet()
    fireEvent.click(screen.getByTestId('unattended-toggle'))
    fireEvent.click(screen.getByTestId('kind-tmux'))
    expect(screen.queryByTestId('unattended-toggle')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.kind).toBe('tmux')
    expect('interaction' in captureBody.value!).toBe(false)
  })

  it('shows the permissions toggle only for a terminal session in the sandbox', async () => {
    await renderSheet()
    // Agent kind: a note, no toggle.
    expect(screen.getByTestId('agent-permissions-note')).toHaveTextContent(AGENT_NO_PROMPTS)
    expect(screen.queryByTestId('skip-permissions-toggle')).not.toBeInTheDocument()

    fireEvent.click(screen.getByTestId('kind-tmux'))
    const toggle = screen.getByTestId('skip-permissions-toggle')
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-checked', 'true')

    // Off the sandbox it disappears — and the flag it set goes with it.
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    expect(screen.queryByTestId('skip-permissions-toggle')).not.toBeInTheDocument()

    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByLabelText(ACK_TERMINAL))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.dangerously_skip_permissions).toBe(false)
  })

  // A remembered OpenClaw is picked before any daemon or cluster data has
  // arrived, so the runtime default must account for it — otherwise the sheet
  // opens on Cluster pod with OpenClaw selected: a disabled card that is also
  // the current choice, and a Launch that would post a request the server
  // refuses.
  it('a remembered OpenClaw opens on this machine, never on a disabled card', async () => {
    localStorage.setItem('blerg.launch.engine', 'openclaw')
    await renderSheet({ clusterStatus: CLUSTER_ON })
    expect(screen.getByTestId('engine-openclaw')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('runtime-daemon')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('runtime-cluster')).toHaveAttribute('aria-checked', 'false')
  })

  // The belt to that braces: whatever left a disabled runtime selected, Launch
  // refuses and says why, next to the button rather than three columns away.
  it('Launch is disabled, with the reason, while the selected runtime is disabled', async () => {
    await renderSheet({ daemons: [] })
    expect(screen.getByTestId('runtime-daemon')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('runtime-daemon')).toBeDisabled()
    fireEvent.click(screen.getByText('myrepo'))
    const launch = screen.getByText(/Launch/)
    expect(launch).toBeDisabled()
    expect(screen.getByTestId('launch-blocked-reason')).toHaveTextContent(NO_DAEMON_REASON)
    fireEvent.click(launch)
    expect(captureBody.value).toBeUndefined()
  })

  it('picking OpenClaw forces this machine and closes off the sandboxed runtimes', async () => {
    await renderSheet({ clusterStatus: CLUSTER_ON })
    fireEvent.click(screen.getByTestId('engine-openclaw'))
    expect(screen.getByTestId('runtime-daemon')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('runtime-docker')).toBeDisabled()
    expect(screen.getByTestId('runtime-cluster')).toBeDisabled()
    expect(screen.getByTestId('runtime-docker-reason')).toHaveTextContent('OpenClaw runs only on the host')

    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByTestId('agent-ack'))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.engine).toBe('openclaw')
    expect(captureBody.value!.runtime).toBe('daemon')
  })

  it('re-asks for the acknowledgement when the runtime or kind changes under it', async () => {
    await renderSheet()
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    fireEvent.click(screen.getByTestId('agent-ack'))
    expect(screen.getByTestId('agent-ack')).toBeChecked()
    fireEvent.click(screen.getByTestId('kind-tmux'))
    expect(screen.getByTestId('agent-ack')).not.toBeChecked()
    expect(screen.getByLabelText(ACK_TERMINAL)).toBeInTheDocument()
  })

  it('warns when the picked engine has no credentials on cluster runtime', async () => {
    await renderSheet({ clusterStatus: { configured: true, available_engines: ['claude'] } })
    fireEvent.click(screen.getByTestId('engine-codex'))
    expect(screen.getByText(/Cluster runtime has no credentials configured for codex/)).toBeInTheDocument()
  })

  it('shows no warning when the picked engine is available on cluster runtime', async () => {
    await renderSheet({ clusterStatus: { configured: true, available_engines: ['claude', 'codex'] } })
    fireEvent.click(screen.getByTestId('engine-codex'))
    expect(screen.queryByText(/has no credentials configured/)).not.toBeInTheDocument()
  })
})

// ─── cluster credential preflight (now inline in the Run column) ─────────────

describe('LaunchSheet — cluster credential preflight', () => {
  let captureBody: { value?: Record<string, unknown> }

  beforeEach(() => {
    captureBody = {}
    useSessionStore.setState({ pendingSessionIds: {}, failedSessions: {} })
    vi.stubGlobal('localStorage', makeMemoryStorage())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  async function renderClusterAgent(opts: Parameters<typeof buildFetchMock>[1]) {
    vi.stubGlobal('fetch', buildFetchMock(captureBody, opts))
    render(
      <MemoryRouter>
        <LaunchSheet open={true} onClose={vi.fn()} />
      </MemoryRouter>,
    )
    await screen.findByText('Repository')
    // Cluster is the default wherever it is configured — assert that rather
    // than clicking, so the preflight cases also cover the default path.
    expect(screen.getByTestId('runtime-cluster')).toHaveAttribute('aria-checked', 'true')
  }

  it('blocks launch and links to Settings when the engine has no credential anywhere', async () => {
    await renderClusterAgent({
      daemons: [],
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: true },
    })
    fireEvent.click(screen.getByTestId('engine-codex'))
    fireEvent.click(screen.getByText('myrepo'))

    const msg = screen.getByTestId('cluster-engine-block')
    expect(msg).toHaveTextContent('Add a Codex credential in Settings before launching a cluster session.')
    const link = within(msg).getByRole('link', { name: 'Settings' })
    expect(link).toHaveAttribute('href', `${coreOrigin()}/settings`)
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    expect(screen.getByText(/Launch/).closest('button')).toBeDisabled()
  })

  it('does not block when the engine comes from the user\'s own credentials', async () => {
    await renderClusterAgent({
      daemons: [],
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude', 'codex'], git: true },
    })
    fireEvent.click(screen.getByTestId('engine-codex'))
    fireEvent.click(screen.getByText('myrepo'))

    expect(screen.queryByTestId('cluster-engine-block')).not.toBeInTheDocument()
    expect(screen.queryByText(/has no credentials configured/)).not.toBeInTheDocument()
    expect(screen.getByText(/Launch/).closest('button')).not.toBeDisabled()
  })

  it('keeps the softer warning and does not block when personal credentials are unknown', async () => {
    await renderClusterAgent({ daemons: [], clusterStatus: CLUSTER_ON, myCreds: 'fail' })
    fireEvent.click(screen.getByTestId('engine-codex'))
    fireEvent.click(screen.getByText('myrepo'))

    expect(screen.getByText(/Cluster runtime has no credentials configured for codex/)).toBeInTheDocument()
    expect(screen.queryByTestId('cluster-engine-block')).not.toBeInTheDocument()
    expect(screen.getByText(/Launch/).closest('button')).not.toBeDisabled()
  })

  it('warns (without blocking) when neither the user nor the operator has a GitHub credential', async () => {
    await renderClusterAgent({
      daemons: [],
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: false },
    })
    fireEvent.click(screen.getByText('myrepo'))

    const warning = screen.getByTestId('cluster-git-warning')
    expect(warning).toHaveTextContent(
      'No GitHub credential — this session can only clone public repositories and cannot push. Add one in Settings.',
    )
    expect(within(warning).getByRole('link', { name: 'Settings' })).toHaveAttribute('href', `${coreOrigin()}/settings`)
    expect(screen.getByText(/Launch/).closest('button')).not.toBeDisabled()
  })

  it('shows no git warning when the user has their own GitHub credential', async () => {
    await renderClusterAgent({
      daemons: [],
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: true },
    })
    expect(screen.queryByTestId('cluster-git-warning')).not.toBeInTheDocument()
  })

  it('shows no git warning when the operator has a shared git token', async () => {
    await renderClusterAgent({
      daemons: [],
      clusterStatus: { ...CLUSTER_ON, git_configured: true },
      myCreds: { engines: ['claude'], git: false },
    })
    expect(screen.queryByTestId('cluster-git-warning')).not.toBeInTheDocument()
  })

  it('shows no git warning while personal credentials are unknown', async () => {
    await renderClusterAgent({ daemons: [], clusterStatus: CLUSTER_ON, myCreds: 'fail' })
    expect(screen.queryByTestId('cluster-git-warning')).not.toBeInTheDocument()
  })

  it('accepts a typed org/name repository and sends it as typed', async () => {
    await renderClusterAgent({
      daemons: [],
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: true },
    })
    const repoInput = screen.getByPlaceholderText('org/name')
    fireEvent.change(repoInput, { target: { value: 'acme/widget' } })
    fireEvent.click(screen.getByText('Use acme/widget'))
    fireEvent.click(screen.getByText(/Launch/))

    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.repo).toBe('acme/widget')
    expect(captureBody.value!.runtime).toBe('cluster')
    expect(captureBody.value!.new_repo).toBeUndefined()
  })

  // M2: a cluster pod resolves the repo into a clone URL, and a bare name is
  // only resolvable when the server has an org configured. Picking a repo out
  // of the list must send the same org/name a typed entry does.
  it('sends the full org/name when a listed repo is picked on cluster runtime', async () => {
    await renderClusterAgent({
      daemons: [],
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: true },
    })
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByText(/Launch/))

    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.repo).toBe('org/myrepo')
    expect(captureBody.value!.runtime).toBe('cluster')
  })

  // A local-only folder whose origin is a known GitHub remote: its folder
  // name ("entertainment") differs from the repository's, and the cluster
  // must be sent the remote's org/name — never the bare folder name.
  const LOCAL_KNOWN = {
    name: 'entertainment', full_name: 'example-org/example-repo', remote: 'example-org/example-repo',
    checked_out: ['workstation'], is_local: true, cloneable: true,
  }
  const LOCAL_UNKNOWN = {
    name: 'scratchpad', full_name: 'scratchpad', checked_out: ['workstation'], is_local: true, cloneable: false,
  }

  it('shows and sends the remote org/name for a local-only folder on cluster runtime', async () => {
    await renderClusterAgent({
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: true },
      extraRepos: [LOCAL_KNOWN],
    })
    expect(screen.getByTestId('repo-remote')).toHaveTextContent('example-org/example-repo')
    fireEvent.click(screen.getByText('entertainment'))
    expect(screen.queryByTestId('cluster-repo-unresolved')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText(/Launch/))

    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.repo).toBe('example-org/example-repo')
    expect(captureBody.value!.runtime).toBe('cluster')
  })

  it('labels a GitLab repository and sends its provider with a cluster launch', async () => {
    await renderClusterAgent({
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: true, git_providers: ['gitlab'] },
      extraRepos: [
        { name: 'lab-tool', full_name: 'example-group/lab-tool', provider: 'gitlab', checked_out: [] },
      ],
    })
    expect(screen.getByTestId('repo-provider')).toHaveTextContent('GitLab')
    fireEvent.click(screen.getByText('lab-tool'))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.repo).toBe('example-group/lab-tool')
    expect(captureBody.value!.provider).toBe('gitlab')
  })

  it('blocks a local-only folder with no GitHub remote on cluster runtime and asks for org/name', async () => {
    await renderClusterAgent({
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: true },
      extraRepos: [LOCAL_UNKNOWN],
    })
    fireEvent.click(screen.getByText('scratchpad'))

    expect(screen.getByTestId('cluster-repo-unresolved')).toHaveTextContent(
      'This folder has no GitHub or GitLab remote the cluster can clone — enter org/name',
    )
    const launch = screen.getByText(/Launch/).closest('button')!
    expect(launch).toBeDisabled()
    expect(screen.getByTestId('launch-blocked-reason')).toHaveTextContent('enter org/name')
    fireEvent.click(launch)
    expect(captureBody.value).toBeUndefined()

    // Resolving it through the existing free-text org/name entry unblocks.
    fireEvent.change(screen.getByPlaceholderText('org/name'), { target: { value: 'example-org/scratchpad' } })
    fireEvent.click(screen.getByText('Use example-org/scratchpad'))
    expect(screen.queryByTestId('cluster-repo-unresolved')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.repo).toBe('example-org/scratchpad')
  })

  it('does not block a remote-less local folder on a daemon runtime, which uses the folder name', async () => {
    await renderClusterAgent({
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: true },
      extraRepos: [LOCAL_UNKNOWN],
    })
    fireEvent.click(screen.getByTestId('runtime-docker'))
    fireEvent.click(screen.getByText('scratchpad'))
    expect(screen.queryByTestId('cluster-repo-unresolved')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.repo).toBe('scratchpad')
    expect(captureBody.value!.runtime).toBe('docker')
  })

  // A daemon clone of a GitLab repository must name GitLab: a bare name
  // would be qualified with the daemon's GitHub org — a different repo.
  it('sends owner/name and provider when a daemon must clone a GitLab repository', async () => {
    await renderClusterAgent({
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: true, git_providers: ['gitlab'] },
      extraRepos: [
        { name: 'lab-tool', full_name: 'example-group/lab-tool', provider: 'gitlab', checked_out: [] },
      ],
    })
    fireEvent.click(screen.getByTestId('runtime-docker'))
    fireEvent.click(screen.getByText('lab-tool'))
    fireEvent.click(screen.getByText(/Clone & Launch|Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.repo).toBe('example-group/lab-tool')
    expect(captureBody.value!.provider).toBe('gitlab')
  })

  it('keeps the bare folder name and sends no provider for a checked-out repo on a daemon', async () => {
    await renderClusterAgent({ clusterStatus: CLUSTER_ON, myCreds: { engines: ['claude'], git: true } })
    fireEvent.click(screen.getByTestId('runtime-docker'))
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.repo).toBe('myrepo')
    expect(captureBody.value!.provider).toBeUndefined()
  })

  // A cluster pod is built from a clone and discarded at the end of the
  // session, so "New folder" becomes "New repository": the server creates
  // owner/name on the person's git host and the pod clones it.
  it('offers "New repository" on cluster runtime and sends new_repo with provider and visibility', async () => {
    await renderClusterAgent({
      daemons: [],
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: true, git_providers: ['github'] },
    })
    expect(screen.queryByText('New folder')).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId('new-folder-option'))
    expect(screen.getByText('type owner/name above ↑')).toBeInTheDocument()
    expect(screen.getByText(/Launch/).closest('button')).toBeDisabled()

    fireEvent.change(screen.getByPlaceholderText('you/new-project'), { target: { value: 'me/new-proj' } })
    expect(screen.getByText('Create "me/new-proj" on GitHub')).toBeInTheDocument()
    expect(screen.queryByTestId('new-repo-token-note')).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId('new-repo-visibility-public'))
    expect(screen.getByText('Create & Launch →')).toBeInTheDocument()
    fireEvent.click(screen.getByText('Create & Launch →'))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value).toMatchObject({ repo: 'me/new-proj', provider: 'github', new_repo: true, visibility: 'public', runtime: 'cluster' })
    expect(captureBody.value!.clone).toBeUndefined()
    expect(captureBody.value!.no_repo).toBeUndefined()
  })

  it('warns, without blocking, when the person has no token for the new repository\'s provider', async () => {
    await renderClusterAgent({
      daemons: [],
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: true, git_providers: ['github'] },
    })
    fireEvent.click(screen.getByTestId('new-folder-option'))
    fireEvent.change(screen.getByPlaceholderText('you/new-project'), { target: { value: 'grp/tool' } })
    fireEvent.click(screen.getByTestId('new-repo-provider-gitlab'))
    expect(screen.getByText('Create "grp/tool" on GitLab')).toBeInTheDocument()
    expect(screen.getByTestId('new-repo-token-note')).toHaveTextContent('No GitLab token in Settings')
    expect(screen.getByText(/Launch/).closest('button')).not.toBeDisabled()
    fireEvent.click(screen.getByText('Create & Launch →'))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value).toMatchObject({ repo: 'grp/tool', provider: 'gitlab', new_repo: true, visibility: 'private' })
  })

  it('does not accept a URL as a new repository name', async () => {
    await renderClusterAgent({
      daemons: [],
      clusterStatus: CLUSTER_ON,
      myCreds: { engines: ['claude'], git: true, git_providers: ['github'] },
    })
    fireEvent.click(screen.getByTestId('new-folder-option'))
    fireEvent.change(screen.getByPlaceholderText('you/new-project'), { target: { value: 'https://github.com/me/x' } })
    expect(screen.getByText(/Launch/).closest('button')).toBeDisabled()
  })
})

describe('LaunchSheet — Claude model and effort', () => {
  let captureBody: { value?: Record<string, unknown> }
  let storage: Storage

  const LIVE = {
    source: 'live',
    fetched_at: '2026-09-26T00:00:00Z',
    models: [
      { id: 'claude-opus-9', name: 'Opus 9', description: 'Biggest', section: 'main', efforts: ['low', 'medium', 'high', 'xhigh', 'max'], default_effort: 'medium' },
      { id: 'claude-sonnet-9', name: 'Sonnet 9', description: 'Everyday', section: 'main', efforts: ['low', 'high', 'max'], default_effort: 'high' },
      { id: 'claude-haiku-9', name: 'Haiku 9', description: 'Fast', section: 'main', efforts: [] },
      { id: 'claude-opus-8', name: 'Opus 8', description: '', section: 'overflow', efforts: ['low', 'high'], default_effort: 'low' },
      // A hostile entry the client drops on its own.
      { id: 'rm -rf /', name: 'Bad', section: 'main', efforts: [] },
    ],
  }

  beforeEach(() => {
    captureBody = {}
    useSessionStore.setState({ pendingSessionIds: {}, failedSessions: {} })
    storage = makeMemoryStorage()
    vi.stubGlobal('localStorage', storage)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  async function renderSheet(opts?: Parameters<typeof buildFetchMock>[1]) {
    vi.stubGlobal('fetch', buildFetchMock(captureBody, opts))
    render(
      <MemoryRouter>
        <LaunchSheet open={true} onClose={vi.fn()} />
      </MemoryRouter>,
    )
    await screen.findByText('Repository')
  }

  async function launch() {
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    return captureBody.value!
  }

  it('shows the fetched list, preselects Sonnet with its default effort, and sends the full id', async () => {
    await renderSheet({ models: LIVE })
    await screen.findByTestId('model-claude-opus-9')
    expect(screen.queryByTestId('model-claude-opus-5-5')).not.toBeInTheDocument()
    expect(screen.queryByText('Bad')).not.toBeInTheDocument()
    expect(screen.getByTestId('model-claude-opus-8').tagName).toBe('OPTION')
    expect(screen.getByTestId('model-claude-sonnet-9')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('effort-high')).toHaveAttribute('aria-checked', 'true')
    expect(screen.queryByTestId('models-fallback')).not.toBeInTheDocument()
    const body = await launch()
    expect(body.model).toBe('claude-sonnet-9')
    expect(body.effort).toBe('high')
  })

  it('falls back to the built-in list when the fetch fails, and still launches', async () => {
    await renderSheet({ models: 'fail' })
    await screen.findByTestId('models-fallback')
    expect(screen.getByTestId('model-claude-opus-5-5')).toBeInTheDocument()
    expect(screen.getByTestId('model-claude-sonnet-5')).toHaveAttribute('aria-pressed', 'true')
    const body = await launch()
    expect(body.model).toBe('claude-sonnet-5')
    expect(body.effort).toBe('high')
  })

  it("re-preselects the new model's default effort when the model changes", async () => {
    await renderSheet({ models: LIVE })
    fireEvent.click(await screen.findByTestId('model-claude-opus-9'))
    expect(screen.getByTestId('effort-medium')).toHaveAttribute('aria-checked', 'true')
    fireEvent.change(screen.getByTestId('model-more'), { target: { value: 'claude-opus-8' } })
    expect(screen.getByTestId('effort-low')).toHaveAttribute('aria-checked', 'true')
    const body = await launch()
    expect(body.model).toBe('claude-opus-8')
    expect(body.effort).toBe('low')
  })

  it('keeps an explicit effort across a model change only while the new model supports it', async () => {
    await renderSheet({ models: LIVE })
    fireEvent.click(await screen.findByTestId('model-claude-opus-9'))
    fireEvent.click(screen.getByTestId('effort-max'))
    // Sonnet 9 has max too: the explicit choice stands.
    fireEvent.click(screen.getByTestId('model-claude-sonnet-9'))
    expect(screen.getByTestId('effort-max')).toHaveAttribute('aria-checked', 'true')
    // Opus 8 has no max: its own default is preselected instead.
    fireEvent.change(screen.getByTestId('model-more'), { target: { value: 'claude-opus-8' } })
    expect(screen.getByTestId('effort-low')).toHaveAttribute('aria-checked', 'true')
    expect(storage.getItem('blerg.launch.effort')).toBe('max')
  })

  it('hides effort for a model without effort levels and sends none', async () => {
    await renderSheet({ models: LIVE })
    fireEvent.click(await screen.findByTestId('model-claude-haiku-9'))
    expect(screen.queryByTestId('effort-row')).not.toBeInTheDocument()
    const body = await launch()
    expect(body.model).toBe('claude-haiku-9')
    expect(body).not.toHaveProperty('effort')
  })

  it('sends the full id and effort for a terminal session too', async () => {
    await renderSheet({ models: LIVE })
    fireEvent.click(screen.getByTestId('kind-tmux'))
    fireEvent.click(await screen.findByTestId('model-claude-opus-9'))
    fireEvent.click(screen.getByTestId('effort-xhigh'))
    const body = await launch()
    expect(body.kind).toBe('tmux')
    expect(body.model).toBe('claude-opus-9')
    expect(body.effort).toBe('xhigh')
  })

  it('remembers the picked model and effort', async () => {
    await renderSheet({ models: LIVE })
    fireEvent.click(await screen.findByTestId('model-claude-opus-9'))
    fireEvent.click(screen.getByTestId('effort-low'))
    expect(storage.getItem('blerg.launch.model')).toBe('claude-opus-9')
    expect(storage.getItem('blerg.launch.effort')).toBe('low')
  })

  it('restores a remembered model and effort', async () => {
    storage.setItem('blerg.launch.model', 'claude-opus-9')
    storage.setItem('blerg.launch.effort', 'xhigh')
    await renderSheet({ models: LIVE })
    await waitFor(() => expect(screen.getByTestId('model-claude-opus-9')).toHaveAttribute('aria-pressed', 'true'))
    expect(screen.getByTestId('effort-xhigh')).toHaveAttribute('aria-checked', 'true')
  })

  it('treats a remembered pre-catalog alias as unset', async () => {
    storage.setItem('blerg.launch.model', 'opus')
    storage.setItem('blerg.launch.effort', 'ultra')
    await renderSheet({ models: LIVE })
    await waitFor(() => expect(screen.getByTestId('model-claude-sonnet-9')).toHaveAttribute('aria-pressed', 'true'))
    const body = await launch()
    expect(body.model).toBe('claude-sonnet-9')
    expect(body.effort).toBe('high')
  })

  it("shows and sends another engine's list the moment its source reports one — no engine special-casing", async () => {
    await renderSheet({
      models: LIVE,
      daemon: { available_engines: ['claude', 'codex'] },
      modelsByEngine: {
        codex: { source: 'live', models: [
          { id: 'gpt-9-codex', name: 'GPT-9 Codex', description: '', section: 'main', efforts: ['low', 'ultra'], default_effort: 'ultra' },
        ] },
      },
    })
    fireEvent.click(screen.getByTestId('engine-codex'))
    await screen.findByTestId('model-gpt-9-codex')
    expect(screen.queryByTestId('model-claude-opus-9')).not.toBeInTheDocument()
    expect(screen.getByTestId('effort-ultra')).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(screen.getByTestId('effort-low'))
    // Remembered under the engine's own key, never Claude's.
    expect(storage.getItem('blerg.launch.effort.codex')).toBe('low')
    expect(storage.getItem('blerg.launch.effort')).toBeNull()
    const body = await launch()
    expect(body.engine).toBe('codex')
    expect(body.model).toBe('gpt-9-codex')
    expect(body.effort).toBe('low')
  })

  it('sends neither model nor effort for another engine', async () => {
    await renderSheet({ models: LIVE, daemon: { available_engines: ['claude', 'codex'] } })
    fireEvent.click(screen.getByTestId('engine-codex'))
    expect(screen.queryByTestId('effort-row')).not.toBeInTheDocument()
    const body = await launch()
    expect(body).not.toHaveProperty('model')
    expect(body).not.toHaveProperty('effort')
  })
})

describe('LaunchSheet — daemon-reported model lists (Codex, Hermes)', () => {
  let captureBody: { value?: Record<string, unknown> }

  const HERMES_EFFORTS = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra']
  const CODEX_D1 = {
    source: 'daemon', daemon_id: 'd1', fetched_at: '2026-09-26T00:00:00Z',
    models: [
      { id: 'gpt-6-astra', name: 'GPT-6-Astra', description: '', section: 'main', efforts: ['low', 'medium', 'high', 'xhigh', 'max', 'ultra'], default_effort: 'low', effort_kind: 'reasoning' },
      { id: 'gpt-5.5', name: 'GPT-5.5', description: '', section: 'main', efforts: ['low', 'medium', 'high', 'xhigh'], default_effort: 'medium', effort_kind: 'reasoning' },
    ],
  }
  const HERMES_D1 = {
    source: 'daemon', daemon_id: 'd1', fetched_at: '2026-09-26T00:00:00Z',
    models: [
      { id: 'qwen3-30b', name: 'qwen3-30b', description: '65536-token context', section: 'main', efforts: HERMES_EFFORTS, effort_kind: 'reasoning' },
    ],
  }
  const NONE = { source: 'none', models: [], fetched_at: null }
  const CODEX_EFFORTS = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra']

  beforeEach(() => {
    captureBody = {}
    useSessionStore.setState({ pendingSessionIds: {}, failedSessions: {} })
    vi.stubGlobal('localStorage', makeMemoryStorage())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  async function renderSheet(opts?: Parameters<typeof buildFetchMock>[1]) {
    vi.stubGlobal('fetch', buildFetchMock(captureBody, {
      daemon: { available_engines: ['claude', 'codex', 'hermes'] }, ...opts,
    }))
    render(
      <MemoryRouter>
        <LaunchSheet open={true} onClose={vi.fn()} />
      </MemoryRouter>,
    )
    await screen.findByText('Repository')
  }

  async function launch() {
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    return captureBody.value!
  }

  it("asks the selected daemon for Codex's list and sends its model and effort", async () => {
    const asked: string[] = []
    await renderSheet({ modelsByEngine: { 'codex?daemon_id=d1': CODEX_D1 }, modelRequests: asked })
    fireEvent.click(screen.getByTestId('engine-codex'))
    await screen.findByTestId('model-gpt-6-astra')
    expect(asked).toContain('codex?daemon_id=d1')
    expect(screen.getByTestId('model-gpt-6-astra')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('effort-low')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('effort-row')).toHaveTextContent('Effort (reasoning)')
    // Codex's models all carry a default: no Auto.
    expect(screen.queryByTestId('effort-auto')).not.toBeInTheDocument()
    expect(screen.queryByTestId('model-free-text')).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId('effort-ultra'))
    const body = await launch()
    expect(body.engine).toBe('codex')
    expect(body.model).toBe('gpt-6-astra')
    expect(body.effort).toBe('ultra')
  })

  it('offers Hermes an Auto effort, preselected, that sends no effort', async () => {
    await renderSheet({ modelsByEngine: { 'hermes?daemon_id=d1': HERMES_D1 } })
    fireEvent.click(screen.getByTestId('engine-hermes'))
    await screen.findByTestId('model-qwen3-30b')
    expect(screen.getByTestId('effort-auto')).toHaveAttribute('aria-checked', 'true')
    for (const e of HERMES_EFFORTS) expect(screen.getByTestId(`effort-${e}`)).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(screen.getByTestId('effort-minimal'))
    expect(screen.getByTestId('effort-auto')).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(screen.getByTestId('effort-auto'))
    expect(screen.getByTestId('effort-auto')).toHaveAttribute('aria-checked', 'true')
    const body = await launch()
    expect(body.engine).toBe('hermes')
    expect(body.model).toBe('qwen3-30b')
    expect(body).not.toHaveProperty('effort')
  })

  it('sends an explicit Hermes reasoning level', async () => {
    await renderSheet({ modelsByEngine: { 'hermes?daemon_id=d1': HERMES_D1 } })
    fireEvent.click(screen.getByTestId('engine-hermes'))
    fireEvent.click(await screen.findByTestId('effort-none'))
    const body = await launch()
    expect(body.effort).toBe('none')
  })

  it('with no list reported, offers a free-text model name — sent only when valid — with an Auto-first effort', async () => {
    await renderSheet({ modelsByEngine: { codex: { ...NONE, engine_efforts: CODEX_EFFORTS } } })
    fireEvent.click(screen.getByTestId('engine-codex'))
    const input = await screen.findByTestId('model-free-text-input')
    expect(screen.queryByTestId('model-picker')).not.toBeInTheDocument()
    // The engine's allowlist, Auto preselected.
    expect(screen.getByTestId('effort-auto')).toHaveAttribute('aria-checked', 'true')
    for (const e of CODEX_EFFORTS) expect(screen.getByTestId(`effort-${e}`)).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(screen.getByText('myrepo'))

    fireEvent.change(input, { target: { value: '--yolo' } })
    expect(screen.getByTestId('model-free-text-invalid')).toBeInTheDocument()
    expect(screen.getByText(/Launch/).closest('button')).toBeDisabled()

    fireEvent.change(input, { target: { value: 'gpt-5.5' } })
    expect(screen.queryByTestId('model-free-text-invalid')).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId('effort-minimal'))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    expect(captureBody.value!.model).toBe('gpt-5.5')
    expect(captureBody.value!.effort).toBe('minimal')
  })

  it('with no list and nothing typed, sends no model and (Auto) no effort', async () => {
    await renderSheet({ modelsByEngine: { hermes: { ...NONE, engine_efforts: HERMES_EFFORTS } } })
    fireEvent.click(screen.getByTestId('engine-hermes'))
    await screen.findByTestId('model-free-text-input')
    expect(screen.getByTestId('effort-auto')).toHaveAttribute('aria-checked', 'true')
    const body = await launch()
    expect(body).not.toHaveProperty('model')
    expect(body).not.toHaveProperty('effort')
  })

  it('never asks for a list without the daemon while the daemon list is still loading', async () => {
    const asked: string[] = []
    const storage = makeMemoryStorage()
    storage.setItem('blerg.launch.engine', 'codex') // codex from the first render
    vi.stubGlobal('localStorage', storage)
    await renderSheet({ modelsByEngine: { 'codex?daemon_id=d1': CODEX_D1, codex: CODEX_D1 }, modelRequests: asked })
    await screen.findByTestId('model-gpt-6-astra')
    expect(asked.filter(a => a.startsWith('codex'))).toEqual(['codex?daemon_id=d1'])
  })

  it("drops a daemon's list — and the typed name — when switching to the cluster", async () => {
    const asked: string[] = []
    await renderSheet({
      clusterStatus: { configured: true, available_engines: ['claude', 'codex'], git_configured: true },
      // With or without a daemon the server has d1's list to give.
      modelsByEngine: { 'codex?daemon_id=d1': CODEX_D1, codex: { ...CODEX_D1, engine_efforts: CODEX_EFFORTS } },
      modelRequests: asked,
    })
    // On the daemon first: its list shows (so the negative check below can fail).
    fireEvent.click(screen.getByTestId('runtime-docker'))
    fireEvent.click(screen.getByTestId('engine-codex'))
    fireEvent.click(await screen.findByTestId('model-gpt-5.5'))
    // Then the cluster: no daemon, so no daemon's list — the free-text box,
    // empty (the list pick is not carried into it).
    fireEvent.click(screen.getByTestId('runtime-cluster'))
    const input = await screen.findByTestId('model-free-text-input')
    expect(screen.queryByTestId('model-gpt-6-astra')).not.toBeInTheDocument()
    expect(screen.queryByTestId('model-gpt-5.5')).not.toBeInTheDocument()
    expect((input as HTMLInputElement).value).toBe('')
    expect(asked).toContain('codex')
    // Typed on the cluster, then back to the daemon and back again: cleared.
    fireEvent.change(input, { target: { value: 'gpt-typed' } })
    fireEvent.click(screen.getByTestId('runtime-docker'))
    await screen.findByTestId('model-gpt-5.5')
    fireEvent.click(screen.getByTestId('runtime-cluster'))
    expect(((await screen.findByTestId('model-free-text-input')) as HTMLInputElement).value).toBe('')
  })
})

// A daemon runtime (Local sandbox here, the default with a sandbox image) can
// launch on a repository nobody listed or checked out: typed as owner/name,
// or pasted as a URL.
describe('LaunchSheet — clone a named repository onto a daemon', () => {
  let captureBody: { value?: Record<string, unknown> }

  beforeEach(() => {
    captureBody = {}
    useSessionStore.setState({ pendingSessionIds: {}, failedSessions: {} })
    vi.stubGlobal('localStorage', makeMemoryStorage())
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  async function renderDaemon(opts?: Parameters<typeof buildFetchMock>[1]) {
    vi.stubGlobal('fetch', buildFetchMock(captureBody, {
      myCreds: { engines: ['claude'], git: true, git_providers: ['github'] },
      ...opts,
      daemon: { clone_from: true, ...opts?.daemon },
    }))
    render(
      <MemoryRouter>
        <LaunchSheet open={true} onClose={vi.fn()} />
      </MemoryRouter>,
    )
    await screen.findByText('Repository')
    expect(screen.getByTestId('runtime-docker')).toHaveAttribute('aria-checked', 'true')
  }

  function type(text: string) {
    fireEvent.change(screen.getByPlaceholderText(/Search repos/), { target: { value: text } })
  }

  async function launch() {
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    return captureBody.value!
  }

  it('offers a typed public owner/name and sends it as a named clone on GitHub', async () => {
    await renderDaemon()
    type('torvalds/linux')
    const option = screen.getByTestId('free-text-repo')
    expect(option).toHaveTextContent('Use torvalds/linux')
    expect(option).toHaveTextContent('clone this repository onto workstation')
    fireEvent.click(option)
    expect(screen.getByTestId('clone-plan')).toHaveTextContent('Will clone torvalds/linux to /repos/linux')
    expect(screen.queryByTestId('clone-folder-collision')).not.toBeInTheDocument()
    const body = await launch()
    expect(body).toMatchObject({ repo: 'torvalds/linux', provider: 'github', clone: true, runtime: 'docker', daemon_id: 'd1' })
    expect(body.git_url).toBeUndefined()
    expect(body.new_repo).toBeUndefined()
  })

  it('reads the provider off a pasted URL and sends the URL for the server to parse', async () => {
    await renderDaemon()
    type('https://gitlab.com/grp/tool.git')
    const option = screen.getByTestId('free-text-repo')
    expect(option).toHaveTextContent('Use grp/tool')
    expect(screen.getByTestId('free-text-provider')).toHaveTextContent('on GitLab')
    // A URL names its host: no provider picker to contradict it.
    expect(screen.queryByTestId('free-text-provider-github')).not.toBeInTheDocument()
    fireEvent.click(option)
    const body = await launch()
    expect(body).toMatchObject({ git_url: 'https://gitlab.com/grp/tool.git', clone: true })
    expect(body.repo).toBeUndefined()
    expect(body.provider).toBeUndefined()
  })

  it('accepts an scp-style remote too', async () => {
    await renderDaemon()
    type('git@github.com:octo/tiny.git')
    expect(screen.getByTestId('free-text-provider')).toHaveTextContent('on GitHub')
    fireEvent.click(screen.getByTestId('free-text-repo'))
    expect(screen.getByTestId('clone-plan')).toHaveTextContent('/repos/tiny')
    expect((await launch()).git_url).toBe('git@github.com:octo/tiny.git')
  })

  it('lets a bare owner/name be read on GitLab instead', async () => {
    await renderDaemon({ myCreds: { engines: ['claude'], git: true, git_providers: ['gitlab'] } })
    type('grp/tool')
    fireEvent.click(screen.getByTestId('free-text-provider-gitlab'))
    expect(screen.getByTestId('free-text-provider-gitlab')).toHaveAttribute('aria-checked', 'true')
    const body = await launch()
    expect(body).toMatchObject({ repo: 'grp/tool', provider: 'gitlab', clone: true })
  })

  it('advises — never blocks — when there is no token for the provider', async () => {
    await renderDaemon({ myCreds: { engines: ['claude'], git: true, git_providers: ['github'] } })
    type('https://gitlab.com/grp/private-tool')
    fireEvent.click(screen.getByTestId('free-text-repo'))
    expect(screen.getByTestId('clone-git-advisory')).toHaveTextContent('You have no GitLab token')
    expect(screen.getByTestId('clone-git-advisory').querySelector('a')).toHaveAttribute('href', `${coreOrigin()}/settings`)
    expect(screen.getByText(/Launch/).closest('button')).not.toBeDisabled()
    expect((await launch()).git_url).toBe('https://gitlab.com/grp/private-tool')
  })

  it('shows no advisory while personal credentials are unknown', async () => {
    await renderDaemon({ myCreds: 'fail' })
    type('torvalds/linux')
    fireEvent.click(screen.getByTestId('free-text-repo'))
    expect(screen.queryByTestId('clone-git-advisory')).not.toBeInTheDocument()
  })

  it('warns that an unrelated same-named folder is left alone and clones beside it', async () => {
    await renderDaemon({
      extraRepos: [{ name: 'linux', full_name: 'someone/linux', remote: 'someone/linux', provider: 'github', checked_out: ['workstation'], is_local: true, cloneable: true }],
    })
    type('torvalds/linux')
    fireEvent.click(screen.getByTestId('free-text-repo'))
    expect(screen.getByTestId('clone-folder-collision')).toHaveTextContent('/repos/linux on workstation is a different repository')
    expect(screen.getByTestId('clone-plan')).toHaveTextContent('Will clone torvalds/linux to /repos/torvalds-linux')
    expect((await launch()).repo).toBe('torvalds/linux')
  })

  it('refuses when both candidate folders hold other repositories', async () => {
    await renderDaemon({
      extraRepos: [
        { name: 'linux', full_name: 'linux', checked_out: ['workstation'], is_local: true, cloneable: false },
        { name: 'torvalds-linux', full_name: 'x/y', remote: 'x/y', provider: 'github', checked_out: ['workstation'], is_local: true, cloneable: true },
      ],
    })
    type('torvalds/linux')
    fireEvent.click(screen.getByTestId('free-text-repo'))
    expect(screen.getByTestId('launch-blocked-reason')).toHaveTextContent('already hold other repositories')
    expect(screen.getByText(/Launch/).closest('button')).toBeDisabled()
  })

  it('says the repository is already there when a folder on this daemon is it', async () => {
    await renderDaemon({
      extraRepos: [{ name: 'linux', full_name: 'torvalds/linux', remote: 'torvalds/linux', provider: 'github', checked_out: ['workstation'], is_local: true, cloneable: true }],
    })
    type('https://github.com/torvalds/linux')
    fireEvent.click(screen.getByTestId('free-text-repo'))
    expect(screen.getByTestId('clone-plan')).toHaveTextContent('Already on workstation at /repos/linux')
    expect(screen.queryByTestId('clone-git-advisory')).not.toBeInTheDocument()
    expect(screen.getByText('Launch Session')).toBeInTheDocument()
  })

  it('does not offer a named clone for text that is not a repository', async () => {
    await renderDaemon()
    type('just-a-folder')
    expect(screen.queryByTestId('free-text-repo')).not.toBeInTheDocument()
    type('https://example.com/a/b')
    expect(screen.queryByTestId('free-text-repo')).not.toBeInTheDocument()
    // New folder is still there for a plain name.
    expect(screen.getByTestId('new-folder-option')).toBeInTheDocument()
  })

  it('blocks with a reason on a daemon too old to clone by name', async () => {
    await renderDaemon({ daemon: { clone_from: false } })
    type('torvalds/linux')
    fireEvent.click(screen.getByTestId('free-text-repo'))
    expect(screen.getByTestId('launch-blocked-reason')).toHaveTextContent("can't clone a repository by name yet")
    expect(screen.getByText(/Launch/).closest('button')).toBeDisabled()
  })

  it('clones a listed repository that is not on this daemon as a named clone', async () => {
    await renderDaemon({
      extraRepos: [{ name: 'lab-tool', full_name: 'example-group/lab-tool', provider: 'gitlab', checked_out: [] }],
    })
    fireEvent.click(screen.getByText('lab-tool'))
    expect(screen.getByTestId('clone-plan')).toHaveTextContent('Will clone example-group/lab-tool to /repos/lab-tool')
    const body = await launch()
    expect(body).toMatchObject({ repo: 'example-group/lab-tool', provider: 'gitlab', clone: true })
  })

  it('says on This machine that the token is not used there, and not in Local sandbox', async () => {
    await renderDaemon({ myCreds: { engines: ['claude'], git: true, git_providers: ['github'] } })
    type('acme/private-app')
    fireEvent.click(screen.getByTestId('free-text-repo'))
    // Local sandbox (the default here): the token is used, inside the container.
    expect(screen.queryByTestId('clone-host-token-note')).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    expect(screen.getByTestId('clone-host-token-note')).toHaveTextContent("On This machine your GitHub token isn't used")
    expect(screen.getByTestId('clone-host-token-note')).toHaveTextContent('pick Local sandbox')
    // Advisory, not a block: a public repository still clones there.
    fireEvent.click(screen.getByTestId('agent-ack'))
    expect(screen.getByText(/Launch/).closest('button')).not.toBeDisabled()
  })

  it('says nothing about the host token on a daemon whose owner allows it', async () => {
    await renderDaemon({ daemon: { allow_host_credential_clone: true } })
    type('acme/private-app')
    fireEvent.click(screen.getByTestId('free-text-repo'))
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    expect(screen.queryByTestId('clone-host-token-note')).not.toBeInTheDocument()
  })

  it('keeps a checked-out folder a plain folder launch', async () => {
    await renderDaemon()
    fireEvent.click(screen.getByText('myrepo'))
    const body = await launch()
    expect(body.repo).toBe('myrepo')
    expect(body.clone).toBeUndefined()
    expect(body.provider).toBeUndefined()
  })
})

// ─── No repository: the default pick ────────────────────────────────────────

describe('LaunchSheet — No repository', () => {
  let captureBody: { value?: Record<string, unknown> }

  beforeEach(() => {
    captureBody = {}
    useSessionStore.setState({ pendingSessionIds: {}, failedSessions: {} })
    vi.stubGlobal('localStorage', makeMemoryStorage())
    // Pins the scratch folder's random part: 8 bytes of 0x80.
    vi.spyOn(crypto, 'getRandomValues').mockImplementation(<T extends ArrayBufferView | null>(arr: T): T => {
      if (arr instanceof Uint8Array) arr.fill(0x80)
      return arr
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  const SCRATCH = '.scratch-granite-8080808080808080'

  async function renderSheet(opts?: Parameters<typeof buildFetchMock>[1]) {
    vi.stubGlobal('fetch', buildFetchMock(captureBody, opts))
    render(
      <MemoryRouter>
        <LaunchSheet open={true} onClose={vi.fn()} />
      </MemoryRouter>,
    )
    await screen.findByText('Repository')
  }

  async function launch() {
    const button = screen.getByText('Launch Session').closest('button')!
    expect(button).not.toBeDisabled()
    fireEvent.click(button)
    await waitFor(() => expect(captureBody.value).toBeDefined())
    return captureBody.value!
  }

  it('is the first entry in the repository list and selected when the sheet opens', async () => {
    await renderSheet()
    const list = screen.getByTestId('repo-list')
    const option = screen.getByTestId('no-repo-option')
    expect(list.firstElementChild).toBe(option)
    expect(option).toHaveAttribute('aria-selected', 'true')
    expect(option).toHaveTextContent('No repository')
  })

  it('stays in the list whatever is typed, and picking a repo deselects it', async () => {
    await renderSheet()
    fireEvent.change(screen.getByPlaceholderText(/Search repos/), { target: { value: 'zzz-no-match' } })
    expect(screen.getByTestId('no-repo-option')).toBeInTheDocument()
    fireEvent.change(screen.getByPlaceholderText(/Search repos/), { target: { value: '' } })
    fireEvent.click(screen.getByText('myrepo'))
    expect(screen.getByTestId('no-repo-option')).toHaveAttribute('aria-selected', 'false')
    expect(screen.queryByTestId('no-repo-plan')).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId('no-repo-option'))
    expect(screen.getByTestId('no-repo-option')).toHaveAttribute('aria-selected', 'true')
  })

  it('launches on the Local sandbox with zero extra input, naming the scratch folder it showed', async () => {
    await renderSheet()
    expect(screen.getByTestId('runtime-docker')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('no-repo-plan')).toHaveTextContent(
      `+ Will create a scratch folder named ${SCRATCH} in /repos (kept after the session; never listed as a repository)`,
    )
    const body = await launch()
    expect(body.no_repo).toBe(true)
    expect(body.scratch_folder).toBe(SCRATCH)
    expect(body.daemon_id).toBe('d1')
    expect(body.runtime).toBe('docker')
    for (const k of ['repo', 'provider', 'git_url', 'clone', 'new_repo']) {
      expect(body[k]).toBeUndefined()
    }
  })

  it('launches on This machine with only the runtime acknowledgement', async () => {
    await renderSheet({ daemon: { sandbox_available: false, repos_root: '/home/dev/code' } })
    expect(screen.getByTestId('runtime-daemon')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByTestId('no-repo-plan')).toHaveTextContent(
      `+ Will create a scratch folder named ${SCRATCH} in /home/dev/code`,
    )
    fireEvent.click(screen.getByTestId('agent-ack'))
    const body = await launch()
    expect(body).toMatchObject({ no_repo: true, scratch_folder: SCRATCH, runtime: 'daemon', daemon_id: 'd1' })
  })

  it('launches on the cluster with zero extra input and never claims a path', async () => {
    await renderSheet({ clusterStatus: CLUSTER_ON, myCreds: { engines: ['claude'], git: false } })
    expect(screen.getByTestId('runtime-cluster')).toHaveAttribute('aria-checked', 'true')
    const plan = screen.getByTestId('no-repo-plan')
    expect(plan).toHaveTextContent("Runs in an empty, throwaway workspace — nothing is kept after the session's pod ends.")
    expect(plan.textContent).not.toMatch(/\/|\.scratch|folder/)
    expect(screen.queryByTestId('scratch-rename')).not.toBeInTheDocument()
    // Nothing is cloned, so the no-git-credential warning does not apply.
    expect(screen.queryByTestId('cluster-git-warning')).not.toBeInTheDocument()
    const body = await launch()
    expect(body.no_repo).toBe(true)
    expect(body.runtime).toBe('cluster')
    expect(body.scratch_folder).toBeUndefined()
    expect(body.repo).toBeUndefined()
  })

  it('shows the cluster git warning again once a repository is picked', async () => {
    await renderSheet({ clusterStatus: CLUSTER_ON, myCreds: { engines: ['claude'], git: false } })
    fireEvent.click(screen.getByText('myrepo'))
    expect(screen.getByTestId('cluster-git-warning')).toBeInTheDocument()
  })

  it('shows no clone, collision or provider notes for No repository', async () => {
    await renderSheet({ myCreds: { engines: ['claude'], git: false } })
    for (const id of ['clone-plan', 'clone-folder-collision', 'clone-host-token-note', 'clone-git-advisory', 'cluster-repo-unresolved', 'launch-blocked-reason']) {
      expect(screen.queryByTestId(id)).not.toBeInTheDocument()
    }
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    for (const id of ['clone-plan', 'clone-host-token-note', 'clone-git-advisory']) {
      expect(screen.queryByTestId(id)).not.toBeInTheDocument()
    }
  })

  it('renames the scratch folder, updating the preview live, and blocks an invalid name', async () => {
    await renderSheet()
    fireEvent.click(screen.getByTestId('scratch-rename'))
    const input = screen.getByTestId('scratch-name-input')
    expect(input).toHaveValue('granite-8080808080808080')
    fireEvent.change(input, { target: { value: 'my-notes' } })
    expect(screen.getByTestId('no-repo-plan')).toHaveTextContent('+ Will create a scratch folder named .scratch-my-notes in /repos')
    fireEvent.change(input, { target: { value: '../escape' } })
    expect(screen.getByText('Launch Session').closest('button')).toBeDisabled()
    expect(screen.getByTestId('launch-blocked-reason')).toHaveTextContent('must be 1-64 letters, digits')
    fireEvent.change(input, { target: { value: 'my-notes' } })
    const body = await launch()
    expect(body.scratch_folder).toBe('.scratch-my-notes')
  })

  it("follows the daemon's repos folder in the preview", async () => {
    await renderSheet({
      daemons: [
        { id: 'd1', name: 'alpha', mode: 'local', repos_root: '/repos/a', status: 'connected', sandbox_available: true },
        { id: 'd2', name: 'beta', mode: 'local', repos_root: '/repos/b', status: 'connected', sandbox_available: true },
      ],
    })
    expect(screen.getByTestId('no-repo-plan')).toHaveTextContent(`${SCRATCH} in /repos/a`)
    fireEvent.click(screen.getByTestId('daemon-d2'))
    expect(screen.getByTestId('no-repo-plan')).toHaveTextContent(`${SCRATCH} in /repos/b`)
  })
})

describe('LaunchSheet MCP servers', () => {
  let captureBody: { value?: Record<string, unknown> }
  const MCP = {
    mcpConnections: [{ id: 'c1', name: 'calendar', url: 'https://x/mcp', auth_kind: 'static', status: 'ok', default_tools: {} }],
    mcpTools: [
      { name: 'read', description: 'reads', hash: 'h-read', annotations: { readOnlyHint: true } },
      { name: 'send', description: 'sends', hash: 'h-send' },
    ],
  }

  beforeEach(() => {
    captureBody = {}
    useSessionStore.setState({ pendingSessionIds: {}, failedSessions: {} })
    vi.stubGlobal('localStorage', makeMemoryStorage())
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  async function renderSheet(opts?: Parameters<typeof buildFetchMock>[1]) {
    vi.stubGlobal('fetch', buildFetchMock(captureBody, { ...MCP, ...opts }))
    render(
      <MemoryRouter>
        <LaunchSheet open={true} onClose={vi.fn()} />
      </MemoryRouter>,
    )
    await screen.findByText('Repository')
  }

  async function launch() {
    fireEvent.click(screen.getByText('myrepo'))
    fireEvent.click(screen.getByText(/Launch/))
    await waitFor(() => expect(captureBody.value).toBeDefined())
    return captureBody.value!
  }

  it('sends no mcp when nothing is selected, even with connections available', async () => {
    await renderSheet()
    const box = await screen.findByRole('checkbox', { name: 'calendar' })
    expect((box as HTMLInputElement).checked).toBe(false)
    const body = await launch()
    expect('mcp' in body).toBe(false)
  })

  it('sends no mcp for a checked connection with every tool off', async () => {
    await renderSheet()
    fireEvent.click(await screen.findByRole('checkbox', { name: 'calendar' }))
    fireEvent.click(await screen.findByTestId('mcp-customize-calendar'))
    await screen.findByTestId('mcp-tool-calendar-read')
    const body = await launch()
    expect('mcp' in body).toBe(false)
  })

  it('sends mcp with each chosen tool and the hash the picker saw', async () => {
    await renderSheet()
    fireEvent.click(await screen.findByRole('checkbox', { name: 'calendar' }))
    fireEvent.click(await screen.findByTestId('mcp-customize-calendar'))
    await screen.findByTestId('mcp-tool-calendar-read')
    fireEvent.change(screen.getByLabelText('calendar read mode'), { target: { value: 'allow' } })
    // A session with connections is private to the person, and keeps its tools.
    expect(screen.getByTestId('launch-mcp-private')).toHaveTextContent(/private to you/)
    const body = await launch()
    expect(body.mcp).toEqual([{ connection: 'c1', tools: { read: { mode: 'allow', hash: 'h-read' } } }])
  })

  it('offers the picker on the cluster pod and the local sandbox, and explains its absence on the host and for other engines', async () => {
    await renderSheet({ clusterStatus: CLUSTER_ON, myCreds: { engines: ['claude'], git: true } })
    expect(await screen.findByTestId('mcp-picker')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('runtime-docker'))
    expect(screen.getByTestId('mcp-picker')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    expect(screen.queryByTestId('mcp-picker')).toBeNull()
    expect(screen.getByTestId('launch-mcp-unavailable')).toHaveTextContent(/not on this machine unsandboxed/)
    fireEvent.click(screen.getByTestId('runtime-docker'))
    fireEvent.click(screen.getByTestId('engine-codex'))
    expect(screen.queryByTestId('mcp-picker')).toBeNull()
    expect(screen.getByTestId('launch-mcp-unavailable')).toBeInTheDocument()
  })

  it('does not send a selection made earlier once the runtime cannot carry it', async () => {
    await renderSheet()
    fireEvent.click(await screen.findByRole('checkbox', { name: 'calendar' }))
    fireEvent.click(await screen.findByTestId('mcp-customize-calendar'))
    await screen.findByTestId('mcp-tool-calendar-read')
    fireEvent.change(screen.getByLabelText('calendar read mode'), { target: { value: 'allow' } })
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    fireEvent.click(screen.getByLabelText(/I understand/))
    const body = await launch()
    expect(body.runtime).toBe('daemon')
    expect('mcp' in body).toBe(false)
  })
})
