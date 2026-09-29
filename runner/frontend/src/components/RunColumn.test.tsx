import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import RunColumn from './RunColumn'
import type { RunColumnProps } from './RunColumn'
import {
  defaultRuntime,
  runtimeDisabledReason,
  RUNTIME_CARDS,
  ACK_AGENT,
  ACK_TERMINAL,
  AGENT_NO_PROMPTS,
  TERMINAL_NEEDS_DAEMON,
  OPENCLAW_HOST_ONLY,
  NO_CLUSTER_REASON,
  NO_DAEMON_REASON,
} from '../lib/runtimes'
import type { RunDaemon } from '../lib/runtimes'
import { BUILTIN_MODELS } from '../lib/engineModels'

const sandboxDaemon: RunDaemon = {
  id: 'd1', name: 'workstation', repos_root: '/repos', status: 'connected', sandbox_available: true,
}
const plainDaemon: RunDaemon = { ...sandboxDaemon, sandbox_available: false }

function renderColumn(over: Partial<RunColumnProps> = {}) {
  const props: RunColumnProps = {
    runtime: 'docker',
    onRuntimeChange: vi.fn(),
    kind: 'agent',
    onKindChange: vi.fn(),
    engine: 'claude',
    onEngineChange: vi.fn(),
    models: BUILTIN_MODELS.claude,
    model: 'claude-sonnet-5',
    onModelChange: vi.fn(),
    effort: 'high',
    onEffortChange: vi.fn(),
    daemons: [sandboxDaemon],
    selectedDaemon: sandboxDaemon,
    onDaemonChange: vi.fn(),
    clusterConfigured: false,
    availableEnginesHere: null,
    clusterEngineBlocked: false,
    clusterGitMissing: false,
    settingsLink: <a href="/settings">Settings</a>,
    dangerouslySkipPermissions: false,
    onSkipPermissionsChange: vi.fn(),
    ack: false,
    onAckChange: vi.fn(),
    ...over,
  }
  render(<RunColumn {...props} />)
  return props
}

describe('defaultRuntime', () => {
  it('prefers the cluster pod whenever one is configured', () => {
    expect(defaultRuntime([sandboxDaemon], true)).toBe('cluster')
    expect(defaultRuntime([], true)).toBe('cluster')
  })

  it('falls back to the local sandbox when a daemon can build one', () => {
    expect(defaultRuntime([sandboxDaemon], false)).toBe('docker')
  })

  it('only reaches the unsandboxed host when nothing else can run the session', () => {
    expect(defaultRuntime([plainDaemon], false)).toBe('daemon')
    expect(defaultRuntime([], false)).toBe('daemon')
  })
})

describe('runtimeDisabledReason', () => {
  const base = { clusterConfigured: true, daemons: [sandboxDaemon], engine: 'claude' as const, kind: 'agent' as const }

  it('allows all three when cluster, daemon and sandbox are present', () => {
    for (const card of RUNTIME_CARDS) {
      expect(runtimeDisabledReason(card.id, base)).toBeNull()
    }
  })

  it('refuses the cluster pod with no cluster configured', () => {
    expect(runtimeDisabledReason('cluster', { ...base, clusterConfigured: false })).toBe(NO_CLUSTER_REASON)
  })

  it('refuses the cluster pod for a terminal session', () => {
    expect(runtimeDisabledReason('cluster', { ...base, kind: 'tmux' })).toBe(TERMINAL_NEEDS_DAEMON)
  })

  it('refuses both daemon-backed runtimes with no daemon connected', () => {
    expect(runtimeDisabledReason('docker', { ...base, daemons: [] })).toBe(NO_DAEMON_REASON)
    expect(runtimeDisabledReason('daemon', { ...base, daemons: [] })).toBe(NO_DAEMON_REASON)
    expect(runtimeDisabledReason('cluster', { ...base, daemons: [] })).toBeNull()
  })

  it('pins OpenClaw to the host', () => {
    expect(runtimeDisabledReason('cluster', { ...base, engine: 'openclaw' })).toBe(OPENCLAW_HOST_ONLY)
    expect(runtimeDisabledReason('docker', { ...base, engine: 'openclaw' })).toBe(OPENCLAW_HOST_ONLY)
    expect(runtimeDisabledReason('daemon', { ...base, engine: 'openclaw' })).toBeNull()
  })

  // A daemon with no sandbox image is a "this may fail", not a "you may not":
  // the person may be about to build it, and the warning says so.
  it('does not refuse the sandbox merely because the image is missing', () => {
    expect(runtimeDisabledReason('docker', { ...base, daemons: [plainDaemon] })).toBeNull()
  })
})

describe('RunColumn', () => {
  it('shows all three runtime cards with their descriptions', () => {
    renderColumn({ clusterConfigured: true })
    for (const card of RUNTIME_CARDS) {
      const el = screen.getByTestId(`runtime-${card.id}`)
      expect(el).toHaveTextContent(card.title)
      expect(el).toHaveTextContent(card.body)
    }
  })

  it('marks the chosen runtime and reports picks', () => {
    const props = renderColumn({ runtime: 'docker', clusterConfigured: true })
    expect(screen.getByTestId('runtime-docker')).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(screen.getByTestId('runtime-daemon'))
    expect(props.onRuntimeChange).toHaveBeenCalledWith('daemon')
  })

  it('shows the agent note instead of the permissions toggle for agent kind', () => {
    renderColumn({ kind: 'agent' })
    expect(screen.getByTestId('agent-permissions-note')).toHaveTextContent(AGENT_NO_PROMPTS)
    expect(screen.queryByTestId('skip-permissions-toggle')).not.toBeInTheDocument()
  })

  it('shows the permissions toggle for a terminal session in the sandbox', () => {
    const props = renderColumn({ kind: 'tmux', runtime: 'docker' })
    fireEvent.click(screen.getByTestId('skip-permissions-toggle'))
    expect(props.onSkipPermissionsChange).toHaveBeenCalledWith(true)
  })

  it('makes the permissions toggle keyboard-operable', () => {
    const props = renderColumn({ kind: 'tmux', runtime: 'docker' })
    const toggle = screen.getByTestId('skip-permissions-toggle')
    expect(toggle).toHaveAttribute('tabindex', '0')
    fireEvent.keyDown(toggle, { key: ' ' })
    fireEvent.keyDown(toggle, { key: 'Enter' })
    expect(props.onSkipPermissionsChange).toHaveBeenCalledTimes(2)
    expect(props.onSkipPermissionsChange).toHaveBeenLastCalledWith(true)
  })

  it('explains the dead end when OpenClaw is picked with no daemon connected', () => {
    renderColumn({ engine: 'openclaw', daemons: [], selectedDaemon: null, runtime: 'daemon' })
    expect(screen.getByTestId('openclaw-no-daemon')).toHaveTextContent('OpenClaw needs a connected daemon')
  })

  it('says nothing about OpenClaw when a daemon is there to run it', () => {
    renderColumn({ engine: 'openclaw', runtime: 'daemon' })
    expect(screen.queryByTestId('openclaw-no-daemon')).not.toBeInTheDocument()
  })

  it('hides the daemon picker on the cluster pod, where no daemon is involved', () => {
    const two = [sandboxDaemon, { ...sandboxDaemon, id: 'd2', name: 'beta' }]
    renderColumn({ daemons: two, runtime: 'cluster', clusterConfigured: true })
    expect(screen.queryByTestId('daemon-picker')).not.toBeInTheDocument()
  })

  it('shows the daemon picker again on a daemon-backed runtime', () => {
    const two = [sandboxDaemon, { ...sandboxDaemon, id: 'd2', name: 'beta' }]
    renderColumn({ daemons: two, runtime: 'docker', clusterConfigured: true })
    expect(screen.getByTestId('daemon-picker')).toBeInTheDocument()
  })

  it('hides the permissions toggle for a terminal session on the host', () => {
    renderColumn({ kind: 'tmux', runtime: 'daemon' })
    expect(screen.queryByTestId('skip-permissions-toggle')).not.toBeInTheDocument()
  })

  it('asks for no acknowledgement in the sandbox or the cluster', () => {
    renderColumn({ runtime: 'docker' })
    expect(screen.queryByTestId('agent-ack')).not.toBeInTheDocument()
  })

  it('asks for no acknowledgement on the cluster pod', () => {
    renderColumn({ runtime: 'cluster', clusterConfigured: true })
    expect(screen.queryByTestId('agent-ack')).not.toBeInTheDocument()
  })

  it('words the acknowledgement for an agent session on the host', () => {
    renderColumn({ runtime: 'daemon', kind: 'agent' })
    expect(screen.getByLabelText(ACK_AGENT)).toBeInTheDocument()
  })

  it('words the acknowledgement for a terminal session on the host', () => {
    renderColumn({ runtime: 'daemon', kind: 'tmux' })
    expect(screen.getByLabelText(ACK_TERMINAL)).toBeInTheDocument()
  })

  it('shows the model picker for Claude only', () => {
    renderColumn({ engine: 'claude' })
    expect(screen.getByTestId('model-claude-opus-5-5')).toBeInTheDocument()
  })

  it('shows no model picker or effort for an engine with no model list', () => {
    renderColumn({ engine: 'codex', models: [], model: '' })
    expect(screen.queryByTestId('model-picker')).not.toBeInTheDocument()
    expect(screen.queryByTestId('effort-row')).not.toBeInTheDocument()
  })

  it("renders any engine's list the same way, with no engine special-casing", () => {
    renderColumn({
      engine: 'codex',
      models: [
        { id: 'gpt-9-codex', name: 'GPT-9 Codex', description: 'd', section: 'main', efforts: ['low', 'ultra'], default_effort: 'low', effort_kind: 'reasoning' },
      ],
      model: 'gpt-9-codex',
      effort: 'ultra',
    })
    expect(screen.getByTestId('model-gpt-9-codex')).toHaveTextContent('GPT-9 Codex')
    expect(screen.getByTestId('effort-ultra')).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByText('Effort (reasoning)')).toBeInTheDocument()
  })

  it('shows main models as named pills and the overflow ones behind More models', () => {
    const onModelChange = vi.fn()
    renderColumn({ onModelChange })
    const pill = screen.getByTestId('model-claude-opus-5-5')
    expect(pill.tagName).toBe('BUTTON')
    expect(pill).toHaveTextContent('Opus 5.5')
    expect(pill).toHaveAttribute('title', 'Most capable for ambitious work')
    expect(screen.getByTestId('model-claude-sonnet-5')).toHaveAttribute('aria-pressed', 'true')
    // Overflow models are options of the disclosure, not pills.
    expect(screen.getByTestId('model-claude-opus-4-7').tagName).toBe('OPTION')
    fireEvent.change(screen.getByTestId('model-more'), { target: { value: 'claude-opus-4-7' } })
    expect(onModelChange).toHaveBeenCalledWith('claude-opus-4-7')
    fireEvent.click(pill)
    expect(onModelChange).toHaveBeenCalledWith('claude-opus-5-5')
  })

  it("offers exactly the selected model's efforts, marking the current one", () => {
    const onEffortChange = vi.fn()
    renderColumn({ model: 'claude-opus-4-6', effort: 'max', onEffortChange })
    expect(screen.queryByTestId('effort-xhigh')).not.toBeInTheDocument()
    expect(screen.getByTestId('effort-max')).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(screen.getByTestId('effort-low'))
    expect(onEffortChange).toHaveBeenCalledWith('low')
  })

  it('hides the effort row for a model with no effort levels (Haiku)', () => {
    renderColumn({ model: 'claude-haiku-4-5-20251001', effort: undefined })
    expect(screen.queryByTestId('effort-row')).not.toBeInTheDocument()
  })

  it('says so when the live model list could not be loaded', () => {
    renderColumn({ modelsFallback: true })
    expect(screen.getByTestId('models-fallback')).toBeInTheDocument()
  })

  it('dims an engine the chosen runtime has no credential for, and says so', () => {
    renderColumn({ engine: 'codex', availableEnginesHere: ['claude'] })
    expect(screen.getByTestId('engine-missing-warning')).toHaveTextContent(
      "workstation doesn't appear to have codex configured",
    )
  })

  it('collapses nothing — there is no details element in the column', () => {
    renderColumn({ clusterConfigured: true })
    expect(document.querySelector('details')).toBeNull()
  })
})

describe('RunColumn repos folder', () => {
  it("shows the selected daemon's repos folder with a Change affordance on a daemon runtime", () => {
    renderColumn({ runtime: 'docker', onReposRootChanged: vi.fn() })
    expect(screen.getByTestId('repos-root-value')).toHaveTextContent('/repos')
    expect(screen.getByTestId('repos-root-edit')).toBeInTheDocument()
  })

  it('hides it on the cluster runtime, which uses no daemon folder', () => {
    renderColumn({ runtime: 'cluster', clusterConfigured: true, onReposRootChanged: vi.fn() })
    expect(screen.queryByTestId('repos-root')).not.toBeInTheDocument()
  })

  it('hides it when the sheet offers no way to change it', () => {
    renderColumn({ runtime: 'daemon' })
    expect(screen.queryByTestId('repos-root')).not.toBeInTheDocument()
  })
})
