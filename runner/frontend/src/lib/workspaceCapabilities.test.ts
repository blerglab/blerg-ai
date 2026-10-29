import { describe, expect, it } from 'vitest'
import { workspaceRuntimeAvailable } from './workspaceCapabilities'

describe('independent workspace runtime policy', () => {
  it('allows affirmative Linux/WSL host and sandbox support', () => {
    expect(workspaceRuntimeAvailable({ manifest_version: 2, linux_host: true }, 'daemon')).toBe(true)
    expect(workspaceRuntimeAvailable({ manifest_version: 2, sandbox: true }, 'docker')).toBe(true)
  })
  it('refuses off macOS host and cluster even when sandbox is available', () => {
    const caps = { manifest_version: 2, sandbox: true, macos_host: false, cluster: false }
    expect(workspaceRuntimeAvailable(caps, 'daemon')).toBe(false)
    expect(workspaceRuntimeAvailable(caps, 'cluster')).toBe(false)
  })
  it('refuses missing, legacy, unknown and cross-runtime claims', () => {
    for (const caps of [undefined, { manifest_version: 2 }, { manifest_version: 3, linux_host: true }, { manifest_version: 2, cluster: true }]) {
      expect(workspaceRuntimeAvailable(caps, 'daemon')).toBe(false)
      expect(workspaceRuntimeAvailable(caps, 'docker')).toBe(false)
    }
    expect(workspaceRuntimeAvailable({ manifest_version: 2, linux_host: true }, 'unknown')).toBe(false)
  })
})
