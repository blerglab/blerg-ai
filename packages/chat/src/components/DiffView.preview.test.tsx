// A static preview of a diff card and the diff viewer (unified and side by side, desktop and phone),
// written with the chat's stylesheets to /tmp/df-preview.html. Skipped unless DF_PREVIEW=1:
//   DF_PREVIEW=1 npx vitest run src/components/DiffView.preview
import { describe, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import ArtifactCard from './ArtifactCard'
import { ChatContext, type ChatContextValue } from './context'
import DiffView from './DiffView'
import { loadHighlighter } from '../model/highlight'
import { createFakeFiles } from '../test/fakeTransport'

interface NodeFs { readFileSync(p: URL, enc: 'utf8'): string; writeFileSync(p: string, data: string): void }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const env = import.meta.env as Record<string, string | undefined>

const PATCH = `commit ${'4b1d9c2e'.repeat(5)}
Author: Dev
Date:   2026-10-17T09:12:00-04:00

    Retry the upload when the runner resets the connection

diff --git a/runner/internal/server/upload.go b/runner/internal/server/upload.go
index 1111111..2222222 100644
--- a/runner/internal/server/upload.go
+++ b/runner/internal/server/upload.go
@@ -40,14 +40,20 @@ func (a *API) upload(w http.ResponseWriter, r *http.Request) {
 	sid := r.PathValue("id")
 	// The body is streamed: a large file is never held in memory.
 	limit := a.maxFileBytes()
-	n, err := io.Copy(dst, io.LimitReader(r.Body, limit+1))
-	if err != nil {
-		writeError(w, http.StatusBadRequest, "upload failed")
+	var n int64
+	var err error
+	for attempt := 0; attempt < 3; attempt++ {
+		n, err = io.Copy(dst, io.LimitReader(r.Body, limit+1))
+		if !errors.Is(err, syscall.ECONNRESET) {
+			break
+		}
+	}
+	if err != nil {
+		writeError(w, http.StatusBadRequest, "upload failed: "+err.Error())
 		return
 	}
 	if n > limit {
 		writeError(w, http.StatusRequestEntityTooLarge, "too large")
 		return
 	}
 	ok := true
 	_ = ok
 	// nothing else changes here
 	log.Printf("upload %s: %d bytes", sid, n)
 	writeJSON(w, http.StatusCreated, info)
diff --git a/packages/chat/src/model/retry.ts b/packages/chat/src/model/retry.ts
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/packages/chat/src/model/retry.ts
@@ -0,0 +1,6 @@
+// How long to wait before the next attempt.
+export function backoff(attempt: number): number {
+  const base = 250
+  return Math.min(base * 2 ** attempt, 4000)
+}
+export const MAX_ATTEMPTS = 3
diff --git a/docs/upload.md b/docs/upload.md
index 4444444..5555555 100644
--- a/docs/upload.md
+++ b/docs/upload.md
@@ -3,3 +3,4 @@
 A file is streamed to the runner.
-A reset connection fails the upload.
+A reset connection is retried up to three times,
+with a short backoff between attempts.
 The limit is the runner's own.
diff --git a/brand/logo.png b/brand/logo.png
index 6666666..7777777 100644
Binary files a/brand/logo.png and b/brand/logo.png differ (binary, 18233 -> 19410 bytes)
`

function wide(on: boolean) {
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: on && q.includes('min-width'), addEventListener: () => {}, removeEventListener: () => {} }))
}

async function snapshot(width: number, split: boolean): Promise<string> {
  wide(width >= 900)
  const files = createFakeFiles()
  files.serve(PATCH)
  const chat: ChatContextValue = {
    sessionId: 's1', files, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined,
    submitFeedback: vi.fn(async () => true), reviewFor: vi.fn(async () => null),
  }
  render(
    <ChatContext.Provider value={chat}>
      <div className="chat-root" style={{ width, padding: 12, boxSizing: 'border-box' }}>
        <div className="agent-card-wrap">
          <ArtifactCard payload={{ id: 'p1', name: 'retry-the-upload.diff', size: PATCH.length, content_type: 'text/x-diff', view: 'diff' }} />
        </div>
        <div className="artifact-dialog artifact-viewer is-diff" style={{ position: 'relative', height: 1000, width: '100%', marginTop: 16 }}>
          <header className="artifact-header">
            <div className="artifact-header-text"><h2>retry-the-upload.diff</h2><p className="artifact-meta">Diff · 2.4 KB</p></div>
            <button type="button" className="artifact-btn">Download</button>
            <button type="button" className="artifact-close" aria-label="Close">✕</button>
          </header>
          <div className="artifact-body"><DiffView text={PATCH} /></div>
        </div>
      </div>
    </ChatContext.Provider>,
  )
  await screen.findByTestId('artifact-preview-diff-counts')
  await waitFor(() => { if (!document.querySelector('.diff-code .hljs-keyword')) throw new Error('not yet') })
  if (split) act(() => { fireEvent.click(screen.getByRole('button', { name: 'Side by side' })) })
  const html = document.body.innerHTML
  cleanup()
  return html
}

describe('diff preview', () => {
  it.skipIf(!env.DF_PREVIEW)('writes /tmp/df-preview.html', async () => {
    await loadHighlighter()
    const unified = await snapshot(1280, false)
    const split = await snapshot(1280, true)
    const phone = await snapshot(390, false)
    const { readFileSync, writeFileSync } = (await nodeModule('node:fs')) as NodeFs
    const css = ['../theme/tokens.css', '../theme/blerg.css', './chat.css', './markdown.css', './diff.css']
      .map(f => readFileSync(new URL(f, import.meta.url), 'utf8')).join('\n')
    writeFileSync('/tmp/df-preview.html', `<!doctype html>
<html lang="en" data-theme="dark"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Diff preview</title>
<style>${css}
body { margin: 0; background: var(--chat-bg); color: var(--chat-fg); font-family: var(--chat-font, system-ui); }
h1 { font: 600 14px system-ui; margin: 16px 12px 0; color: var(--chat-muted); }
</style></head><body>
<div id="desktop"><h1>Unified</h1>${unified}<h1>Side by side</h1>${split}</div>
<div id="phone">${phone}</div>
<script>document.getElementById(location.search.includes('phone') ? 'desktop' : 'phone').remove()</script>
</body></html>
`)
  }, 60_000)
})
