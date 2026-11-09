import { VerbositySettings } from './VerbositySelect'
import "../../../../packages/settings/settings.css";
// Settings: what applies across sessions, in a dialog over the current page. Chat: Show thinking, Compact
// tool calls and Hover previews, which act at once (kept per browser). Code colors: the syntax-color theme for dark mode and
// for light mode, optional changes to single colors, and a live preview of real chat code blocks (ts, go,
// python, json, a diff) in the choice as it stands. Colors change nothing in the app until Save; closing
// throws the draft away.
import { useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import {
  CODE_THEMES, CODE_TOKEN_KEYS, CODE_TOKEN_LABELS, CODE_TOKEN_NAMES, Markdown, applyCodeColours, currentCodeMode,
  isHexColour, resolveCodeTokens, setHovercardsEnabled, useBackdropClose, useHovercardsEnabled, type CodeColours, type CodeMode, type CodeTokenKey,
} from '@blerglab/chat'
import { saveCompactFolders, useCompactFolders } from '../lib/compactFolders'
import { POP_OUT_CHOICES, usePopOutChoice, writePopOutChoice, type PopOutChoice } from '@blerglab/chat'
import { saveCodeColours, useCodeColours } from '../lib/codeColours'
import { useCompact, useThinking } from '../lib/sessionRail'
import { coreOrigin } from '../authClient'
import { apiFetch } from '../apiFetch'
import './CapabilitiesPanel.css'
import './SettingsDialog.css'
import DelegationRulesSection from './DelegationRulesSection'
import EngineStatusSection from './EngineStatusSection'
import ShellLimitsSection from './ShellLimitsSection'
import UsageNoticeSettings from './UsageNoticeSettings'
import CardTimeBudgetSettings from './CardTimeBudgetSettings'
import BranchSettingsSection from './BranchSettingsSection'
import SkillSettingsSection from "./SkillSettingsSection"

const FOCUSABLE = 'button:not([disabled]), input:not([disabled]), select:not([disabled]), summary, [href], [tabindex]:not([tabindex="-1"])'

// core's own Settings page, keyed by its ?section= query (core/web/src/Settings.tsx). One deep
// link per section, so account/integration/people/install forms stay in core and are never
// duplicated here.
const CORE_SECTIONS: { id: string; label: string; description: string; adminOnly?: boolean }[] = [
  { id: 'account', label: 'Account', description: 'Sign-in, desktops and agent tokens.' },
  { id: 'integrations', label: 'Integrations', description: 'Credentials, read-only tokens for workers, plugins and MCP connections.' },
  { id: 'people', label: 'People', description: 'Invite people and manage their desktops.', adminOnly: true },
  { id: 'install', label: 'Install', description: 'Release notices, worker limits and workspace policies.', adminOnly: true },
]

const SAMPLE = [
  '```ts',
  'export async function load(id: string): Promise<User | null> {',
  '  // Cached for a minute, then read again.',
  '  const hit = cache.get(id) ?? (await db.find<User>({ id, active: true }))',
  '  return hit ?? null',
  '}',
  '```',
  '```go',
  'func (s *Store) Get(ctx context.Context, id string) (*Item, error) {',
  '\tif id == "" { // nothing to look up',
  '\t\treturn nil, ErrEmpty',
  '\t}',
  '\treturn s.rows[id], nil',
  '}',
  '```',
  '```python',
  '@cached(ttl=60)',
  'def total(items: list[Item], tax: float = 0.2) -> float:',
  '    """Sum the prices, then add tax."""',
  '    return round(sum(i.price for i in items) * (1 + tax), 2)',
  '```',
  '```json',
  '{ "name": "blerg", "version": "0.3.0", "private": true, "workers": [4, 8] }',
  '```',
  '```diff',
  '--- a/store.go',
  '+++ b/store.go',
  '@@ -12,3 +12,4 @@ func (s *Store) Get',
  ' \treturn s.rows[id], nil',
  '-\treturn nil',
  '+\treturn nil, ErrMissing',
  '```',
].join('\n')

/** An input[type=color] only takes #rrggbb: turn what the browser computed (#hex or rgb()) into that. */
function toHex(v: string): string {
  const s = v.trim()
  if (isHexColour(s)) return s.toLowerCase()
  const m = /^rgba?\(\s*(\d+)[ ,]+(\d+)[ ,]+(\d+)/.exec(s)
  if (!m) return '#808080' // themecheck:allow BLG-473: input[type=color]'s value attribute requires literal #rrggbb; it can't take a CSS var()
  return '#' + [m[1], m[2], m[3]].map(n => Number(n).toString(16).padStart(2, '0')).join('')
}

export default function SettingsDialog({ onClose }: { onClose: () => void }) {
  const [thinking, setThinking] = useThinking()
  const [compact, setCompact] = useCompact()
  const popOutChoice = usePopOutChoice()
  const compactFolders = useCompactFolders()
  const [folderError, setFolderError] = useState('')
  const [savingFolders, setSavingFolders] = useState(false)
  const hovercards = useHovercardsEnabled()
  const saved = useCodeColours()
  const [draft, setDraft] = useState<CodeColours>(saved)
  const [mode, setMode] = useState<CodeMode>(currentCodeMode)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  // The colors as the browser resolves them, for the swatches of a theme that follows the page palette.
  const [resolved, setResolved] = useState<Partial<Record<CodeTokenKey, string>>>({})
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement>(null)
  const previewRef = useRef<HTMLDivElement>(null)
  const backdrop = useBackdropClose(onClose)
  // Same source as the Skills section's "This install" scope (/api/skills/settings' admin
  // flag): People and Install are administrator-only in core, so a non-admin never sees a link
  // that would only land them back on core's Account section.
  const [admin, setAdmin] = useState(false)

  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    dialogRef.current?.querySelector<HTMLElement>(FOCUSABLE)?.focus()
    return () => opener?.focus?.()
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void apiFetch('/api/skills/settings', { signal: controller.signal }).then(async response => {
      if (!response.ok) return
      const body = await response.json() as { admin?: boolean }
      if (!controller.signal.aborted) setAdmin(!!body.admin)
    }).catch(() => { /* stays non-admin: People and Install stay hidden */ })
    return () => controller.abort()
  }, [])

  // The preview wears the draft for the mode being looked at, scoped to its own box.
  useEffect(() => {
    const el = previewRef.current
    if (!el) return
    applyCodeColours(el, draft, mode)
    const cs = getComputedStyle(el)
    const next: Partial<Record<CodeTokenKey, string>> = {}
    for (const k of CODE_TOKEN_KEYS) next[k] = toHex(cs.getPropertyValue(CODE_TOKEN_NAMES[k]))
    setResolved(prev => (CODE_TOKEN_KEYS.every(k => prev[k] === next[k]) ? prev : next))
  }, [draft, mode])

  const overrides = draft.overrides[mode]
  const dirty = JSON.stringify(draft) !== JSON.stringify(saved)

  function setTheme(m: CodeMode, id: string) {
    setDraft(d => ({ ...d, [m]: id, overrides: { ...d.overrides, [m]: {} } }))
    setMode(m)
  }
  function setOverride(k: CodeTokenKey, value: string | null) {
    setDraft(d => {
      const o = { ...d.overrides[mode] }
      if (value === null) delete o[k]
      else o[k] = value
      return { ...d, overrides: { ...d.overrides, [mode]: o } }
    })
  }
  async function save() {
    setSaving(true)
    setError('')
    const err = await saveCodeColours(draft)
    setSaving(false)
    if (err) setError(err)
    else onClose()
  }
  function onKeyDown(e: React.KeyboardEvent) {
    // This dialog is drawn in a portal but React bubbles its events to the panel's drawer, which traps
    // Tab and closes on Escape for itself: keys typed here are the dialog's alone.
    e.stopPropagation()
    if (e.key === 'Escape') {
      onClose()
      return
    }
    if (e.key !== 'Tab' || !dialogRef.current) return
    const nodes = Array.from(dialogRef.current.querySelectorAll<HTMLElement>(FOCUSABLE))
    if (nodes.length === 0) return
    const first = nodes[0]
    const last = nodes[nodes.length - 1]
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus() }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus() }
  }

  const base = resolveCodeTokens(draft[mode], mode)

  return createPortal(
    // button-guard-ignore: useBackdropClose supplies pointer handlers only; this overlay is not a button
    <div className="caps-overlay" data-testid="settings-overlay" {...backdrop}>
      <div ref={dialogRef} className="caps-dialog cc-dialog" role="dialog" aria-modal="true" aria-labelledby={titleId}
        data-testid="settings" onKeyDown={onKeyDown}>
        <header className="caps-header">
          <h2 className="settings-heading" id={titleId}>Settings</h2>
          <button type="button" className="settings-action caps-close" aria-label="Close" onClick={onClose}>✕</button>
        </header>
        <div className="caps-body">
          <div className="cc-body">
            <section className="settings-section st-section" aria-labelledby={`${titleId}-chat`}>
              <h3 id={`${titleId}-chat`} className="settings-heading st-heading">Chat</h3>
              <p className="settings-description">Choose how your workspace conversations are shown.</p>
              <label className="settings-field st-check">
                <input className="settings-toggle-control" type="checkbox" checked={thinking} onChange={e => setThinking(e.target.checked)} />
                <span>Show thinking</span>
              </label>
              <p className="st-hint">Show the model’s thinking in the transcript.</p>
              <label className="settings-field st-check">
                <input className="settings-toggle-control" type="checkbox" checked={compact} onChange={e => setCompact(e.target.checked)} />
                <span>Compact tool calls</span>
              </label>
              <p className="st-hint">Fold runs of tool calls into one summary row.</p>
              <label className="settings-field cc-field">
                <span>Offer to open a message in a tab when it's longer than</span>
                <select className="settings-field select" data-testid="settings-pop-out" value={popOutChoice} onChange={e => writePopOutChoice(e.target.value as PopOutChoice)}>
                  {POP_OUT_CHOICES.map(c => <option key={c.value} value={c.value}>{c.label}</option>)}
                </select>
              </label>
              <p className="st-hint">A long reply reads more easily in its own tab.</p>
              <label className="settings-field st-check">
                <input className="settings-toggle-control" type="checkbox" data-testid="settings-hovercards" checked={hovercards} onChange={e => setHovercardsEnabled(e.target.checked)} />
                <span>Hover previews</span>
              </label>
              <p className="st-hint">Preview a ticket, file or commit when the pointer rests on its chip (on a device with a mouse or trackpad).</p>
            </section>

            <section className="settings-section st-section" aria-labelledby={`${titleId}-explorer`}>
              <h3 id={`${titleId}-explorer`} className="settings-heading st-heading">File explorer</h3>
              <label className="settings-field st-check">
                <input className="settings-toggle-control" type="checkbox" checked={compactFolders} disabled={savingFolders} onChange={e => {
                  setSavingFolders(true)
                  void saveCompactFolders(e.target.checked).then(error => { setFolderError(error); setSavingFolders(false) })
                }} />
                <span>Compact folders</span>
              </label>
              <p className="st-hint">Show folders with a single child folder in one row. Saved for your account.</p>
              {folderError && <p role="alert">{folderError}</p>}
            </section>
            <DelegationRulesSection />
            <BranchSettingsSection />
            <SkillSettingsSection />
            <VerbositySettings />
              <UsageNoticeSettings />
            <CardTimeBudgetSettings />
            <section className="settings-section st-section" aria-labelledby={`${titleId}-core`}>
              <h3 id={`${titleId}-core`} className="settings-heading st-heading">Blerg’s main Settings</h3>
              <p className="settings-description">Account, integrations, people and install settings live in Blerg’s main Settings. Each link opens it in a new tab, at that section.</p>
              <ul className="st-core-list">
                {CORE_SECTIONS.filter(s => admin || !s.adminOnly).map(s => (
                  <li key={s.id} className="st-core-item">
                    <a className="settings-action btn st-desktops-link" href={`${coreOrigin()}/settings?section=${s.id}`} target="_blank" rel="noopener noreferrer">{s.label}</a>
                    <span className="st-hint">{s.description}</span>
                  </li>
                ))}
              </ul>
            </section>
            <EngineStatusSection />
            <ShellLimitsSection />
            <h3 id={`${titleId}-colors`} className="settings-heading st-heading">Code colors</h3>
            <p className="settings-description">Choose the colors used for code in dark and light mode.</p>
            <div className="cc-pickers" role="group" aria-labelledby={`${titleId}-colors`}>
              {(['dark', 'light'] as const).map(m => (
                <label key={m} className="settings-field cc-field">
                  <span>{m === 'dark' ? 'Dark mode' : 'Light mode'}</span> {/* themecheck:allow BLG-473: CodeMode's own light/dark preview toggle, a second axis from codeThemes.ts (syntax color schemes), not the app theme */}
                  <select className="settings-field select" value={draft[m]} onChange={e => setTheme(m, e.target.value)} data-testid={`cc-theme-${m}`}>
                    {CODE_THEMES.map(t => <option key={t.id} value={t.id}>{t.name}</option>)}
                  </select>
                </label>
              ))}
            </div>

            <div className="cc-previewbar" role="group" aria-label="Preview as">
              <span className="cc-previewbar-label">Preview</span>
              {(['dark', 'light'] as const).map(m => (
                <button key={m} type="button" className={'settings-action ' + ('cc-seg' + (mode === m ? ' on' : ''))} aria-pressed={mode === m}
                  onClick={() => setMode(m)}>{m === 'dark' ? 'Dark' : 'Light'}{/* themecheck:allow BLG-473: same CodeMode toggle as above */}</button>
              ))}
            </div>
            <div ref={previewRef} className="cc-preview" data-testid="cc-preview">
              <Markdown text={SAMPLE} />
            </div>

            <details className="cc-custom">
              <summary>Customize</summary>
              <p className="cc-hint">Changes to the {mode} theme ({CODE_THEMES.find(t => t.id === draft[mode])?.name}). Picking another theme clears them.</p>
              <ul className="cc-tokens">
                {CODE_TOKEN_KEYS.map(k => {
                  const custom = overrides[k]
                  return (
                    <li key={k} className="cc-token">
                      <input className="settings-field" type="color" aria-label={`${CODE_TOKEN_LABELS[k]} color`} value={custom ?? resolved[k] ?? toHex(base[k])}
                        onChange={e => setOverride(k, e.target.value)} />
                      <span className="cc-token-name">{CODE_TOKEN_LABELS[k]}</span>
                      {custom && <button type="button" className="settings-action cc-reset" onClick={() => setOverride(k, null)}>Reset</button>}
                    </li>
                  )
                })}
              </ul>
            </details>
            {error && <div role="alert" className="cc-error">{error}</div>}
          </div>
        </div>
        <footer className="cc-footer">
          <button type="button" className="settings-action btn" onClick={onClose}>Done</button>
          <button type="button" className="settings-action btn primary" disabled={!dirty || saving} onClick={() => void save()}>
            {saving ? 'Saving…' : 'Save colors'}
          </button>
        </footer>
      </div>
    </div>,
    document.body,
  )
}
