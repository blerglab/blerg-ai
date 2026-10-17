// A static preview of the Settings dialog for a person to look at in a browser: three themes side by
// side, written with the app's stylesheets (and the chat package's) to /tmp/sp-preview.html. Skipped
// unless SP_PREVIEW=1:  SP_PREVIEW=1 npx vitest run --project unit SettingsDialog.preview
import { describe, it } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { setCodeColours } from '../lib/codeColours'
import SettingsDialog from './SettingsDialog'

interface NodeFs { readFileSync(p: URL, enc: 'utf8'): string; writeFileSync(p: string, data: string): void }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const env = import.meta.env as Record<string, string | undefined>

const pick = (dark: string, light: string) => ({ dark, light, overrides: { dark: {}, light: {} } })

describe('SettingsDialog preview', () => {
  it.skipIf(!env.SP_PREVIEW)('writes /tmp/sp-preview.html', async () => {
    const shots: Array<{ choice: ReturnType<typeof pick>; mode: 'dark' | 'light'; open: boolean }> = [
      { choice: pick('blerg', 'blerg'), mode: 'dark', open: false },
      { choice: pick('dracula', 'github-light'), mode: 'dark', open: true },
      { choice: pick('nord', 'github-light'), mode: 'light', open: false },
    ]
    const marks: string[] = []
    for (const [i, s] of shots.entries()) {
      document.documentElement.setAttribute('data-theme', s.mode)
      setCodeColours(s.choice)
      const { baseElement } = render(<SettingsDialog onClose={() => {}} />)
      const dialogs = baseElement.querySelectorAll('[data-testid="settings"]')
      const mine = dialogs[dialogs.length - 1] as HTMLElement
      await waitFor(() => { if (mine.querySelectorAll('.hljs-keyword').length < 3) throw new Error('not highlighted yet') })
      // A select's choice is a property, not markup: write it into the markup for the static page.
      mine.querySelectorAll('select').forEach(sel => sel.querySelectorAll('option').forEach(o => { if (o.selected) o.setAttribute('selected', ''); else o.removeAttribute('selected') }))
      if (s.open) mine.querySelector('details')!.setAttribute('open', '')
      mine.setAttribute('data-shot', String(i))
      marks.push(mine.closest('.caps-overlay')!.outerHTML)
    }
    const { readFileSync, writeFileSync } = (await nodeModule('node:fs')) as NodeFs
    const read = (f: string) => readFileSync(new URL(f, import.meta.url), 'utf8')
    const css = [
      '../index.css', './CapabilitiesPanel.css', './SettingsDialog.css',
      '../../../../packages/chat/src/theme/tokens.css', '../../../../packages/chat/src/theme/blerg.css',
      '../../../../packages/chat/src/components/markdown.css', '../../../../packages/chat/src/components/chat.css',
    ].map(read).join('\n')
    writeFileSync('/tmp/sp-preview.html', `<!doctype html>
<html lang="en" data-theme="dark"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Settings preview</title>
<style>${css}
body { margin: 0; background: var(--basalt); color: var(--chalk); font-family: var(--body); }
.shots { display: flex; flex-wrap: wrap; gap: 16px; padding: 16px; align-items: flex-start; }
.shots .caps-overlay { position: static; padding: 0; background: none; display: block; flex: 1 1 380px; min-width: 0; }
.shots .caps-dialog { width: 100%; max-height: none; height: auto; }
@media (max-width: 899px) { .shots { padding: 0; gap: 0; } .shots .caps-dialog { border-radius: 0; } }
</style></head><body><div class="shots">${marks.join('\n')}</div></body></html>
`)
  })
})
