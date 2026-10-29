/** Runtime support is affirmative and independently negotiated. A version-only
 * peer cannot lend workspace or shell support to another runtime. */
export interface WorkspaceCapabilities {
  manifest_version: number
  shell_version?: number
  linux_host?: boolean
  macos_host?: boolean
  sandbox?: boolean
  cluster?: boolean
}

export function workspaceRuntimeAvailable(caps: WorkspaceCapabilities | undefined, runtime: string): boolean {
  if (caps?.manifest_version !== 2) return false
  if (runtime === 'daemon') return caps.linux_host === true || caps.macos_host === true
  if (runtime === 'docker') return caps.sandbox === true
  if (runtime === 'cluster') return caps.cluster === true
  return false
}
