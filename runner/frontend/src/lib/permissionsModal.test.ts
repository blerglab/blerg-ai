import { describe, expect, it } from 'vitest'
import {
  diffPermissions, changeSummary, matchesQuery, toggleShown, launchChildOptions, launchDraft, draftToLaunch,
  toolKey, sessionChildOption, SANDBOXED_CLUSTER_REASON, type PermissionsDraft,
} from './permissionsModal'
import { childRuntimesFor } from './launchPermissions'

const draft = (over: Partial<PermissionsDraft> = {}): PermissionsDraft => ({ children: {}, boards: {}, tools: [], revokeDelegations: [], revokeDormant: [], ...over })

describe('the search filter', () => {
  it('matches every word of the query, in any order and case, across the texts given', () => {
    expect(matchesQuery('', 'Product')).toBe(true)
    expect(matchesQuery('  ', 'Product')).toBe(true)
    expect(matchesQuery('prod', 'Product roadmap')).toBe(true)
    expect(matchesQuery('ROAD prod', 'Product roadmap')).toBe(true)
    expect(matchesQuery('mail send', 'Team mail', 'send_message')).toBe(true)
    expect(matchesQuery('ops', 'Product roadmap')).toBe(false)
  })
})

describe('select all shown', () => {
  it('adds every shown item the filter left, and keeps hidden selections', () => {
    expect(toggleShown(['hidden'], ['a', 'b'])).toEqual(['hidden', 'a', 'b'])
  })
  it('clears only the shown items when all of them are already selected', () => {
    expect(toggleShown(['hidden', 'a', 'b'], ['a', 'b'])).toEqual(['hidden'])
  })
  it('selects the rest when only some shown are selected', () => {
    expect(toggleShown(['a'], ['a', 'b'])).toEqual(['a', 'b'])
  })
  it('does nothing with nothing shown', () => {
    expect(toggleShown(['a'], [])).toEqual(['a'])
  })
})

describe('the save diff', () => {
  it('grants what was added and revokes what was removed, and summarizes it in one line', () => {
    const initial = draft({ children: { docker: 'off', daemon: 'on' }, boards: { b1: 'file', b2: 'full' }, tools: [toolKey('c1', 'send')] })
    const next = draft({ children: { docker: 'on', daemon: 'on' }, boards: { b2: 'full', b3: 'read' }, tools: [toolKey('c1', 'send'), toolKey('c1', 'delete')] })
    const c = diffPermissions(initial, next)
    expect(c.children).toEqual([{ runtime: 'docker', enabled: true }])
    expect(c.boardLevels).toEqual([{ board: 'b1', from: 'file', level: 'propose' }, { board: 'b3', from: 'propose', level: 'read' }])
    expect(c.grantTools).toEqual([{ connection_id: 'c1', tool: 'delete' }])
    expect(c.revokeTools).toEqual([])
    expect(changeSummary(c)).toBe('Grant 3, revoke 1')
  })
  it('renews a signed-out board at its own level as one grant, and not when its level changes anyway', () => {
    const initial = draft({ boards: { b1: 'full', b2: 'read' } })
    const c = diffPermissions(initial, draft({ boards: { b1: 'full', b2: 'full' }, renewBoards: ['b1', 'b2'] }))
    expect(c.boardLevels).toEqual([{ board: 'b2', from: 'read', level: 'full' }, { board: 'b1', from: 'full', level: 'full' }])
    expect(changeSummary(c)).toBe('Grant 2')
  })
  it('counts raising a board level as a grant and lowering it as a revoke', () => {
    const c = diffPermissions(draft({ boards: { b1: 'full', b2: 'read' } }), draft({ boards: { b1: 'read', b2: 'full' } }))
    expect(c.boardLevels).toEqual([{ board: 'b1', from: 'full', level: 'read' }, { board: 'b2', from: 'read', level: 'full' }])
    expect(changeSummary(c)).toBe('Grant 1, revoke 1')
  })
  it('says there is nothing to save when nothing changed', () => {
    const same = draft({ boards: { b1: 'read' }, children: { docker: 'partial' } })
    expect(changeSummary(diffPermissions(same, draft({ boards: { b1: 'read' }, children: { docker: 'partial' } })))).toBe('No changes')
  })
  it('turning a runtime off covers its per-engine grants: no separate revoke for them', () => {
    const initial = draft({ children: { docker: 'partial' } })
    const next = draft({ children: { docker: 'off' }, revokeDelegations: ['docker\u0000codex', 'daemon\u0000claude'] })
    const c = diffPermissions(initial, next)
    expect(c.children).toEqual([{ runtime: 'docker', enabled: false }])
    expect(c.revokeDelegations).toEqual(['daemon\u0000claude'])
    expect(changeSummary(c)).toBe('Revoke 2')
  })
  it('revokes a dormant grant, unless its scope is being given again (that renews it)', () => {
    const c = diffPermissions(draft(), draft({ boards: { b1: 'file' }, revokeDormant: ['g-b1', 'g-other'] }), { 'g-b1': { board: 'b1' }, 'g-other': { board: 'b9' } })
    expect(c.boardLevels).toEqual([{ board: 'b1', from: 'propose', level: 'file' }])
    expect(c.revokeDormant).toEqual(['g-other'])
  })
  it('counts grants only in the summary when nothing is revoked', () => {
    expect(changeSummary(diffPermissions(draft(), draft({ boards: { b1: 'read', b2: 'full' } })))).toBe('Grant 2')
  })
})

describe('the children rules and their disabled reasons', () => {
  it('a sandboxed session offers unsandboxed children disabled, with the reason', () => {
    const opts = launchChildOptions('docker')
    expect(opts.map(o => [o.runtime, o.canEnable])).toEqual([['docker', true], ['daemon', false]])
    expect(opts[1].reason).toMatch(/sandboxed session can't pre-approve unsandboxed children/)
  })
  it('an unsandboxed session may pre-approve both', () => {
    expect(launchChildOptions('daemon').every(o => o.canEnable)).toBe(true)
  })
  it('a cluster session offers cluster children only', () => {
    expect(launchChildOptions('cluster').map(o => o.runtime)).toEqual(['cluster'])
    expect(launchChildOptions('cluster', [], { engines: ['codex'] }).map(o => o.runtime)).toEqual(['cluster'])
  })
  it('a desktop session also offers cluster children where the install has a cluster, each row saying where it runs', () => {
    const opts = launchChildOptions('daemon', ['claude'], { engines: ['codex'] })
    expect(opts.map(o => [o.runtime, o.canEnable])).toEqual([['docker', true], ['daemon', true], ['cluster', true]])
    expect(opts.map(o => o.note?.split('.')[0])).toEqual(['On this desktop', 'On this desktop', 'On the cluster'])
    expect(opts.map(o => o.engines)).toEqual([['claude'], ['claude'], ['codex']])
    expect(launchChildOptions('daemon', ['claude']).map(o => o.runtime)).toEqual(['docker', 'daemon'])
  })
  it('a sandboxed desktop session sees the cluster row disabled with why, unless the runner allows it', () => {
    const opts = launchChildOptions('docker', ['claude'], { engines: ['codex'] })
    expect(opts.map(o => [o.runtime, o.canEnable])).toEqual([['docker', true], ['daemon', false], ['cluster', false]])
    expect(opts[2].reason).toBe(SANDBOXED_CLUSTER_REASON)
    expect(opts[2].disabled).toBe(true)
    const any = launchChildOptions('docker', ['claude'], { engines: ['codex'], sandboxAny: true })
    expect(any.map(o => [o.runtime, o.canEnable])).toEqual([['docker', true], ['daemon', false], ['cluster', true]])
  })
  it('each row of a running session says where its children run', () => {
    const base = { enabled: false, partial: false, allowed_engines: ['claude'], granted_engines: [], standing_rule_engines: [], effective_allowed_engines: [], can_enable: true, reason: '' }
    expect(sessionChildOption({ ...base, runtime: 'cluster' }).note).toMatch(/^On the cluster\./)
    expect(sessionChildOption({ ...base, runtime: 'docker' }).note).toBe('On this desktop.')
    expect(sessionChildOption({ ...base, runtime: 'daemon' }).note).toMatch(/^On this desktop\. Unsandboxed/)
  })
  it('a running session that cannot enable a runtime says why and keeps it disabled, unless something is granted to revoke', () => {
    const base = { runtime: 'daemon' as const, enabled: false, partial: false, allowed_engines: ['claude'], granted_engines: [], standing_rule_engines: [], effective_allowed_engines: [], can_enable: false, reason: 'A sandboxed parent cannot pre-approve unsandboxed children.' }
    const o = sessionChildOption(base)
    expect(o.disabled).toBe(true)
    expect(o.reason).toBe('A sandboxed parent cannot pre-approve unsandboxed children.')
    expect(sessionChildOption({ ...base, partial: true, granted_engines: ['claude'] }).disabled).toBe(false)
  })
})

describe('launch pre-authorization', () => {
  it('keeps the cluster children of a desktop session only while the install has a cluster', () => {
    expect(childRuntimesFor('daemon', true)).toEqual(['docker', 'daemon', 'cluster'])
    expect(childRuntimesFor('docker', true)).toEqual(['docker'])
    expect(childRuntimesFor('docker', true, true)).toEqual(['docker', 'cluster'])
    expect(childRuntimesFor('docker')).toEqual(['docker'])
    const d = launchDraft({ children: ['docker', 'cluster'], board_levels: [], mcp_tools: [] })
    expect(draftToLaunch(d, { runtime: 'daemon', boards: [], tools: [], clusterConfigured: true }).children).toEqual(['docker', 'cluster'])
    expect(draftToLaunch(d, { runtime: 'daemon', boards: [], tools: [] }).children).toEqual(['docker'])
    expect(draftToLaunch(d, { runtime: 'docker', boards: [], tools: [], clusterConfigured: true }).children).toEqual(['docker'])
    expect(draftToLaunch(d, { runtime: 'docker', boards: [], tools: [], clusterConfigured: true, sandboxClusterAny: true }).children).toEqual(['docker', 'cluster'])
  })
  it('round-trips the launch value through the draft and keeps only what the sheet still allows', () => {
    const value = { children: ['docker', 'daemon'], board_levels: [{ board_id: 'b1', level: 'read' as const }, { board_id: 'gone', level: 'full' as const }], mcp_tools: [{ connection_id: 'c1', tool: 'send' }] }
    const d = launchDraft(value)
    expect(d.children).toEqual({ docker: 'on', daemon: 'on' })
    expect(d.boards).toEqual({ b1: 'read', gone: 'full' })
    const out = draftToLaunch({ ...d, boards: { ...d.boards, b2: 'file', b3: 'propose' } }, { runtime: 'docker', boards: [{ id: 'b1' }, { id: 'b2' }, { id: 'b3' }], tools: [{ connection_id: 'c1', tool: 'send' }] })
    expect(out).toEqual({ children: ['docker'], board_levels: [{ board_id: 'b1', level: 'read' }, { board_id: 'b2', level: 'file' }], mcp_tools: [{ connection_id: 'c1', tool: 'send' }] })
  })
})
