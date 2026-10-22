import { describe, expect, it } from 'vitest'
import { modelFact, modelFullName, runTarget, subagentModelFact } from './runFacts'

describe('Running now row facts', () => {
  it('names a model in full, with its id for the tooltip', () => {
    expect(modelFullName('claude-opus-5-5')).toBe('Opus 5.5')
    expect(modelFullName('claude-sonnet-4-5-20250929')).toBe('Sonnet 4.5')
    expect(modelFullName('claude-haiku-4')).toBe('Haiku 4')
    expect(modelFact('claude', 'claude-opus-5-5')).toEqual({ value: 'Claude Code · Opus 5.5', raw: 'claude-opus-5-5' })
    expect(modelFact('codex', '')).toEqual({ value: 'Codex · engine default' })
  })
  it('a Bedrock, Vertex or long-context id falls back to the chat\'s name of it, the id kept for the tooltip', () => {
    // Deliberate: only the plain Anthropic id is spelled out; any other shape is shown as the chat
    // names it (never mistaken for another model), its exact id in the tooltip.
    for (const id of ['us.anthropic.claude-opus-4-1-20250805-v1:0', 'claude-opus-4-1@20250805', 'claude-opus-5-5[1m]']) {
      expect(modelFullName(id)).not.toBe('Opus 4.1')
      expect(modelFact('claude', id).raw).toBe(id)
    }
  })
  it('a shell command has no engine or model', () => {
    const cmd = runTarget({ title: 'npm run build', kind: 'Background command', runtime: 'daemon', state: 'running' })
    expect(cmd.facts.model).toBeUndefined()
    expect(cmd.facts.engine?.value).toBe('This machine')

  })
  it('gathers kind, engine and runtime, state, step, time, tokens, card and who started it', () => {
    const t = runTarget({ title: 'Survey', kind: 'Subagent', engine: 'claude', runtime: 'docker', model: 'claude-opus-5-5', state: 'Running',
      step: 'Read · src/index.ts', elapsed: '1:30', tokens: '1.2k tokens', card: '#122 Build', startedBy: 'Main' })
    expect(t.facts.engine?.value).toBe('Claude Code · Local sandbox')
    expect(t.facts.model?.value).toBe('Claude Code · Opus 5.5')
    expect(t.facts.step?.value).toBe('Read · src/index.ts')
    expect(t.facts.card?.value).toBe('#122 Build')
    expect(t.facts.startedBy?.value).toBe('Main')
  })
})

describe('a subagent\'s model is always stated', () => {
  it('its own model, by name', () => {
    expect(subagentModelFact('claude-sonnet-5-5', 'claude-opus-5-5')).toEqual({ value: 'Sonnet 5.5', raw: 'claude-sonnet-5-5' })
  })
  it('on the default model: the one it inherits from the session', () => {
    expect(subagentModelFact(undefined, 'claude-opus-5-5')).toEqual({ value: 'Opus 5.5 (same as Main)', raw: 'claude-opus-5-5' })
    expect(subagentModelFact('', 'claude-opus-5-5').value).toBe('Opus 5.5 (same as Main)')
  })
  it('the session\'s model not known either: "same as Main", never blank', () => {
    expect(subagentModelFact(undefined, undefined)).toEqual({ value: 'same as Main' })
    const t = runTarget({ title: 'Survey', kind: 'Subagent', engine: 'claude', modelAs: subagentModelFact(undefined, ''), state: 'Running' })
    expect(t.facts.model?.value).toBe('same as Main')
  })
})
