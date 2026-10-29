import { useEffect } from 'react'
import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { page } from 'vitest/browser'
import { ComposerChips, FileIcon, FilesList } from '@blerglab/chat'
import { fileIconPaths } from '../../../../packages/chat/src/components/fileIconPaths'
import { createFakeFiles } from '../../../../packages/chat/src/test/fakeTransport'
import { useExplorerLayout } from '../hooks/useExplorerLayout'
import { useSessionTabs } from '../hooks/useSessionTabs'
import { CommandRegistry } from '../commands/registry'
import WorkspaceTree from './WorkspaceTree'
import SessionTabBar from './SessionTabBar'
import { PaletteDialog } from './CommandPalette'
import '../index.css'
import '@blerglab/chat/blerg.css'

const entries = vi.hoisted(() => [
  ...['src', 'docs', '.github', 'test'].map(name => ({ name, path: name, directory: true })),
  ...['main.go', 'README.md', 'Dockerfile.dev', 'Makefile', 'package-lock.json', 'app.tsx'].map(name => ({ name, path: name, directory: false })),
])
vi.mock('../apiFetch', () => ({ apiFetch: async () => new Response(JSON.stringify({ result: { ok: true, launch_generation: 'launch', generation: 1, entries, truncated: false }, members: [], runtime: 'host' })) }))
vi.mock('../lib/chatTransport', () => ({ blergTransport: () => ({ files: null }) }))

const names = ['main.go', 'README.md', 'data.json', 'config.yaml', 'app.ts', 'app.js', 'app.tsx', 'main.py', 'run.sh', 'run.ps1', 'Dockerfile', 'Makefile', 'yarn.lock', 'photo.png', 'drawing.svg', 'index.html', 'style.css', 'style.scss', 'style.less', 'main.rs', 'main.c', 'main.cpp', 'main.cs', 'Main.java', 'main.kt', 'main.swift', 'main.rb', 'index.php', 'App.vue', 'App.svelte', '.env', 'package.json', 'data.sql', 'data.csv', 'book.pdf', 'archive.zip', 'music.mp3', 'movie.mp4', 'font.woff', '.gitignore', 'LICENSE', 'main.tf', 'script.lua', 'file.xml', 'notes.ipynb', 'unknown.file']
const files = createFakeFiles()
const items = ['README.md', 'Dockerfile.dev', 'package-lock.json', 'main.go'].map((name, i) => ({ id: `a${i}`, name, size: 1024, content_type: 'text/plain', view: 'text' as const }))
function Tabs() {
  const explorer = useExplorerLayout(true, 'icons')
  const tabs = useSessionTabs('icon-fixture', 'icons', explorer.mobile)
  const { open } = tabs
  useEffect(() => { open({ kind: 'workspace', path: 'main.go' }, true); open({ kind: 'workspace', path: 'README.md' }, true) }, [open])
  return <SessionTabBar tabs={tabs} explorer={explorer} shellOpen={false} />
}
function Gallery() {
  return <main style={{ padding: 16, boxSizing: 'border-box', color: 'var(--chat-fg)', background: 'var(--chat-bg)' }}>
    <h1 style={{ fontSize: 20 }}>Shared file icons</h1>
    <Tabs />
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 300px), 1fr))', gap: 24, marginTop: 16 }}>
      <section><h2 style={{ fontSize: 16 }}>Explorer</h2><WorkspaceTree sessionId="icons" active /></section>
      <section><h2 style={{ fontSize: 16 }}>Files</h2><FilesList sessionId="icons" files={files} items={items} status="ready" onView={() => {}} onChanged={() => {}} />
        <h2 style={{ fontSize: 16 }}>Chat attachments</h2><ComposerChips items={[{ key: 'a', name: 'main.go', size: 1024, status: 'done' }, { key: 'b', name: 'config.yaml', size: 256, status: 'done' }]} onRemove={() => {}} /></section>
      <section style={{ gridColumn: '1 / -1' }}><h2 style={{ fontSize: 16 }}>Language and file types</h2><div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(145px, 1fr))', gap: 12 }}>
        {names.map(name => <div key={name} style={{ display: 'flex', alignItems: 'center', gap: 8 }}><FileIcon name={name} /><span>{name}</span></div>)}
      </div></section>
    </div>
  </main>
}
// Convert any browser color syntax (including color(srgb ...)) to sRGB luminance.
function luminance(color: string) {
  const canvas = document.createElement('canvas')
  const context = canvas.getContext('2d')!
  context.fillStyle = color
  context.fillRect(0, 0, 1, 1)
  const rgb = [...context.getImageData(0, 0, 1, 1).data].slice(0, 3).map(v => v / 255).map(v => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4)
  return rgb[0] * .2126 + rgb[1] * .7152 + rgb[2] * .0722
}
afterEach(() => { document.documentElement.removeAttribute('data-theme'); localStorage.clear() })
for (const width of [390, 1440, 3840]) for (const theme of ['light', 'dark']) {
  it(`keeps the shared icons visible and contrasting at ${width}px in ${theme}`, async () => {
    const shots = import.meta.env.VITE_FILE_ICON_SHOTS_DIR
    await page.viewport(width, shots ? 2160 : 1000)
    document.documentElement.setAttribute('data-theme', theme)
    const { container } = render(<Gallery />)
    await screen.findByRole('treeitem', { name: 'main.go' })
    const treeIcon = container.querySelector('.workspace-row [data-file-icon=go]')!
    const listIcon = container.querySelector('.artifact-row [data-file-icon=go]')!
    const chipIcon = container.querySelector('.attach-chip-icon[data-file-icon=go]')!
    for (const icon of [treeIcon, listIcon, chipIcon]) {
      expect(icon.querySelector('path')!.getAttribute('d')).toBe(fileIconPaths.go)
      const rect = icon.getBoundingClientRect()
      expect(rect.width).toBe(16)
      expect(rect.height).toBe(16)
      expect(rect.left).toBeGreaterThanOrEqual(0)
      expect(rect.right).toBeLessThanOrEqual(width)
    }
    if (width === 390) {
      expect(screen.getByRole('tab', { name: 'README.md' }).querySelector('[data-file-icon=markdown]')).toBeTruthy()
      fireEvent.click(await screen.findByRole('button', { name: '2 open files' }))
      expect(screen.getByRole('menuitem', { name: 'main.go' }).querySelector('[data-file-icon=go]')).toBeTruthy()
    } else {
      expect(screen.getByRole('tab', { name: 'main.go' }).querySelector('[data-file-icon=go]')).toBeTruthy()
    }
    const background = luminance(getComputedStyle(container.querySelector('main')!).backgroundColor)
    for (const icon of container.querySelectorAll<SVGElement>('.file-icon')) {
      const foreground = luminance(getComputedStyle(icon).color)
      const contrast = (Math.max(foreground, background) + .05) / (Math.min(foreground, background) + .05)
      expect(contrast, icon.dataset.fileIcon).toBeGreaterThanOrEqual(3)
    }
    expect(getComputedStyle(treeIcon).color).not.toBe(getComputedStyle(container.querySelector('[data-file-icon=javascript]')!).color)
    expect(container.querySelectorAll('svg use, svg image')).toHaveLength(0)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    window.scrollTo(0, 0)
    await waitFor(() => expect(window.scrollY).toBe(0))
    if (shots) await page.screenshot({ path: `${shots}/file-icons-${width}-${theme}.png`, element: container.querySelector('main')! })
  })
}
it('uses the same filename detection in quick open', async () => {
  await page.viewport(1440, 900)
  const registry = new CommandRegistry()
  registry.register([], [{ id: 'icons', mode: 'files', search: async () => ({ items: [{ id: 'file', title: 'src/main.go', run() {} }] }) }])
  render(<PaletteDialog registry={registry} mode="files" close={() => {}} />)
  await waitFor(() => expect(screen.getByRole('option', { name: 'src/main.go' }).querySelector('[data-file-icon=go] path')?.getAttribute('d')).toBe(fileIconPaths.go))
})
