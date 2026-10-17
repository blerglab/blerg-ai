// A static preview of the file links and the viewer, written with the chat's stylesheets to
// /tmp/fl-preview.html. Skipped unless FL_PREVIEW=1:  FL_PREVIEW=1 npx vitest run ChatView.workspaceFiles.preview
import { describe, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { ev, meta, seed, SESSION } from '../test/chat'

interface NodeFs { readFileSync(p: URL, enc: 'utf8'): string; writeFileSync(p: string, data: string): void }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const env = import.meta.env as Record<string, string | undefined>

const CODE = Array.from({ length: 40 }, (_, i) => i === 0 ? 'package server' : i === 2 ? 'import "net/http"' : i === 11 ? 'func (s *Server) handleArtifacts(w http.ResponseWriter, r *http.Request) {' : i === 12 ? '\tif r.Method != http.MethodGet {' : i === 13 ? '\t\thttp.Error(w, "method not allowed", 405)' : i === 14 ? '\t}' : `// line ${i + 1}`).join('\n')

describe('file links preview', () => {
  it.skipIf(!env.FL_PREVIEW)('writes /tmp/fl-preview.html', async () => {
    seed(
      ev({ kind: 'user_message', payload: { text: 'Where is the artifact list handled? Also `README.md`.', source: 'user' }, seq: 1 }),
      ev({ kind: 'assistant_text', seq: 2, payload: { text: 'The list is served by `runner/internal/server/artifacts.go:12` (see also [the readme](README.md#L3), `./scripts/scrub.sh` and `docs/artifacts.md`). Not files: `fmt.Println`, `1.2.3`, `src/components` and [the site](https://example.com/a.md).\n\n```\nruner/internal/server/artifacts.go\n```' } }),
    )
    const t = createFakeTransport()
    Object.assign(t.files!, {
      workspaceFile: vi.fn(async (_s: string, path: string) => {
        const blob = new Blob([CODE])
        return { path, size: blob.size, view: 'text', content_type: 'text/x-go', mtime: '2026-10-17T00:00:00Z', blob }
      }),
    })
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} canOpenWorkspaceFiles />)
    await screen.findByText(/The list is served/)
    const chat = document.body.innerHTML
    fireEvent.click(screen.getAllByTitle('Open from the session’s folder')[0])
    await screen.findByTestId('workspace-line')
    await new Promise(r => setTimeout(r, 300))
    const viewer = document.querySelector('[data-testid="workspace-viewer-overlay"]')!.outerHTML
    const { readFileSync, writeFileSync } = (await nodeModule('node:fs')) as NodeFs
    const css = ['../theme/blerg.css', './chat.css', './markdown.css', './worklist.css', './commitChips.css']
      .map(f => { try { return readFileSync(new URL(f, import.meta.url), 'utf8') } catch { return '' } }).join('\n')
    const bare = chat.replace(/<div class="artifact-overlay[\s\S]*$/, '')
    writeFileSync('/tmp/fl-preview.html', `<!doctype html>
<html lang="en" data-theme="dark"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>File links preview</title>
<style>${css}
body{margin:0;background:var(--chat-bg,#111);color:var(--chat-fg,#eee);font-family:system-ui,sans-serif}
.pv-chat{max-width:760px;margin:0 auto;padding:12px}
.pv-viewer .artifact-overlay{position:static;height:560px;padding:8px}
.pv-viewer .artifact-dialog{max-height:540px}
</style></head><body class="chat-root" style="background:var(--chat-bg)">
<div class="pv-chat">${bare}</div>
<div class="pv-viewer">${viewer}</div>
</body></html>`)
  })
})
