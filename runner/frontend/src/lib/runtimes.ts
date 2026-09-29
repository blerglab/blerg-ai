// Where and how a session runs: the closed sets the launch sheet offers, the
// copy that describes them, and the rules between them. Kept out of the
// component file so both the sheet and its tests can reuse the rules without
// importing a React component.

export interface RunDaemon {
  id: string
  name: string
  repos_root: string
  status?: string
  sandbox_available?: boolean
  available_engines?: string[]
}

export type RunRuntime = 'cluster' | 'docker' | 'daemon'
export type RunKind = 'agent' | 'tmux'
export type RunEngine = 'claude' | 'codex' | 'hermes' | 'openclaw'

// ─── Copy (verbatim from the spec; exported so tests assert one source) ──────

export const RUNTIME_CARDS: { id: RunRuntime; title: string; body: string }[] = [
  {
    id: 'cluster',
    title: 'Cluster pod',
    body: 'A throwaway container in the cluster, with your credentials.',
  },
  {
    id: 'docker',
    title: 'Local sandbox',
    body: 'A container on the connected daemon. Your ~/.claude and ~/.codex are mounted so the engine can use them; the rest of the machine is not.',
  },
  {
    id: 'daemon',
    title: 'This machine, unsandboxed',
    body: 'Runs directly on the daemon host, as you, with full filesystem access.',
  },
]

export const TERMINAL_NEEDS_DAEMON = 'Terminal sessions run on a daemon.'
export const AGENT_NO_PROMPTS = 'Agent sessions run without permission prompts.'
export const ACK_AGENT =
  'I understand this runs unsandboxed on this machine, as me, with no permission prompts'
export const ACK_TERMINAL =
  'I understand this runs unsandboxed on this machine, as me, with full filesystem access'
// The daemon's own refusal, word for word (sandboxOpenclawRefusal in
// internal/daemon/agentsession.go): a person who ignores the disabled card and
// gets the error from the server must not read two different sentences.
export const OPENCLAW_HOST_ONLY = 'OpenClaw runs only on the host'
export const NO_DAEMON_REASON = 'No workstation daemon connected.'
export const NO_CLUSTER_REASON = 'No cluster runtime is configured on this server.'

export const ENGINE_LABELS: Record<string, string> = {
  claude: 'Claude',
  codex: 'Codex',
  hermes: 'Hermes',
  openclaw: 'OpenClaw',
}

// ─── Pure rules (exported: LaunchSheet reuses them for canSubmit/defaults) ───

// defaultRuntime picks where a fresh session runs when nobody has said.
// Unsandboxed is never a default: the cluster pod first (throwaway, nothing of
// yours on the host), then the daemon's sandbox container, and only when
// neither exists does it land on the host — where the acknowledgement is then
// mandatory before Launch enables.
export function defaultRuntime(daemons: RunDaemon[], clusterConfigured: boolean): RunRuntime {
  if (clusterConfigured) return 'cluster'
  if (daemons.some(d => d.sandbox_available)) return 'docker'
  return 'daemon'
}

// runtimeDisabledReason returns why a runtime cannot be chosen right now, or
// null when it can. A reason that is only "this will probably fail" (a daemon
// with no sandbox image built) is deliberately NOT here — that stays a
// warning, so a person who knows better can still try.
export function runtimeDisabledReason(
  rt: RunRuntime,
  opts: { clusterConfigured: boolean; daemons: RunDaemon[]; engine: RunEngine; kind: RunKind },
): string | null {
  const { clusterConfigured, daemons, engine, kind } = opts
  if (rt !== 'daemon' && engine === 'openclaw') return OPENCLAW_HOST_ONLY
  if (rt === 'cluster') {
    if (!clusterConfigured) return NO_CLUSTER_REASON
    if (kind === 'tmux') return TERMINAL_NEEDS_DAEMON
    return null
  }
  if (daemons.length === 0) return NO_DAEMON_REASON
  return null
}
