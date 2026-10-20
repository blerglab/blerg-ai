import { describe, expect, it } from 'vitest'
import {
  diffPermissions, changeSummary, matchesQuery, toggleShown, launchChildOptions, launchDraft, draftToLaunch,
  toolKey, sessionChildOption, type PermissionsDraft,
} from './permissionsModal'

const draft = (over: Partial<PermissionsDraft> = {}): PermissionsDraft => ({ children: {}, boards: [], tools: [], revokeDelegations: [], revokeDormant: [], ...over })

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
    const initial = draft({ children: { docker: 'off', daemon: 'on' }, boards: ['b1', 'b2'], tools: [toolKey('c1', 'send')] })
    const next = draft({ children: { docker: 'on', daemon: 'on' }, boards: ['b2', 'b3'], tools: [toolKey('c1', 'send'), toolKey('c1', 'delete')] })
    const c = diffPermissions(initial, next)
    expect(c.children).toEqual([{ runtime: 'docker', enabled: true }])
    expect(c.grantBoards).toEqual(['b3'])
    expect(c.revokeBoards).toEqual(['b1'])
    expect(c.grantTools).toEqual([{ connection_id: 'c1', tool: 'delete' }])
    expect(c.revokeTools).toEqual([])
    expect(changeSummary(c)).toBe('Grant 3, revoke 1')
  })
  it('says there is nothing to save when nothing changed', () => {
    const same = draft({ boards: ['b1'], children: { docker: 'partial' } })
    expect(changeSummary(diffPermissions(same, draft({ boards: ['b1'], children: { docker: 'partial' } })))).toBe('No changes')
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
    const c = diffPermissions(draft(), draft({ boards: ['b1'], revokeDormant: ['g-b1', 'g-other'] }), { 'g-b1': { board: 'b1' }, 'g-other': { board: 'b9' } })
    expect(c.grantBoards).toEqual(['b1'])
    expect(c.revokeDormant).toEqual(['g-other'])
  })
  it('counts grants only in the summary when nothing is revoked', () => {
    expect(changeSummary(diffPermissions(draft(), draft({ boards: ['b1', 'b2'] })))).toBe('Grant 2')
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
  it('round-trips the launch value through the draft and keeps only what the sheet still allows', () => {
    const value = { children: ['docker', 'daemon'], boards: ['b1', 'gone'], mcp_tools: [{ connection_id: 'c1', tool: 'send' }] }
    const d = launchDraft(value)
    expect(d.children).toEqual({ docker: 'on', daemon: 'on' })
    const out = draftToLaunch({ ...d, boards: [...d.boards, 'b2'] }, { runtime: 'docker', boards: [{ id: 'b1' }, { id: 'b2' }], tools: [{ connection_id: 'c1', tool: 'send' }] })
    expect(out).toEqual({ children: ['docker'], boards: ['b1', 'b2'], mcp_tools: [{ connection_id: 'c1', tool: 'send' }] })
  })
})
