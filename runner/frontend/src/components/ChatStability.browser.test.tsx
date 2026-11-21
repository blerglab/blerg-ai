// BLG-753: exercise the shipped chat, composer and subscription together.
import { act, render, screen, waitFor } from '@testing-library/react'
import { expect, it } from 'vitest'
import { commands, page, userEvent } from 'vitest/browser'
import { ChatView } from '@blerglab/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'
import { ev, resetChatState, seed, SESSION } from '../../../../packages/chat/src/test/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

const frames = async () => { for (let i = 0; i < 3; i++) await new Promise(requestAnimationFrame) }
const gap = (el: HTMLElement) => el.scrollHeight - el.clientHeight - el.scrollTop
async function mount(width: number) {
  await page.viewport(width, 900)
  resetChatState()
  seed(...Array.from({ length: 40 }, (_, i) => ev({ seq: i + 1, payload: { text: `Message ${i}: ${'Reading a steady conversation. '.repeat(40)}` } })))
  const fake = createFakeTransport({ meta: { engine: 'codex', status: 'running' } })
  const view = render(<div style={{ height: 'calc(100dvh - 50px)', width: '100%' }}><ChatView session={SESSION} transport={fake.transport} /></div>)
  act(() => fake.replayDone({ lastSeq: 40 }))
  await screen.findByTestId('working')
  const root = view.container.querySelector<HTMLElement>('.agent-timeline')!
  await waitFor(() => expect(gap(root)).toBeLessThanOrEqual(1))
  return { ...view, root, fake }
}
for (const width of [390, 3840]) {
  it(`keeps the last line above a growing composer with live Codex output at ${width}px`, async () => {
    const { root, fake, container } = await mount(width)
    const box = screen.getByRole('textbox') as HTMLTextAreaElement
    for (let i = 1; i <= 18; i++) {
      await userEvent.fill(box, 'Draft line\n'.repeat(i))
      act(() => fake.emit(ev({ kind: 'assistant_text', transient: true, payload: { text: 'Streaming line\n', done: false } })))
      if (i % 3 === 0) act(() => fake.emit(ev({ seq: 40 + i / 3, kind: 'assistant_text', payload: { text: 'A completed reply during typing.', done: true } })))
      await frames()
      expect(gap(root)).toBeLessThanOrEqual(1)
      const last = root.querySelector<HTMLElement>('[data-testid="streaming"]') ?? Array.from(root.querySelectorAll<HTMLElement>('.agent-card')).at(-1)
      expect(last).toBeTruthy()
      expect(last!.getBoundingClientRect().bottom).toBeLessThanOrEqual(box.getBoundingClientRect().top)
    }
    // Same-height typing must stay pinned without a new delta or a resize entry.
    await userEvent.fill(box, box.value.replace('Draft line', 'Edited line'))
    await frames()
    expect(gap(root)).toBeLessThanOrEqual(1)
    const dir = import.meta.env.VITE_LAUNCH_SHOTS_DIR
    if (dir) await page.screenshot({ path: `${dir}/chat-stability-${width}.png` })
    if (width === 3840) {
      container.style.width = '320px'
      await waitFor(() => expect(gap(root)).toBeLessThanOrEqual(1))
      expect(screen.getByTestId('working').getBoundingClientRect().right).toBeLessThanOrEqual(root.getBoundingClientRect().right)
    }
  })
  it(`never moves a read message while typing and streaming tokens at ${width}px`, async () => {
    const { root, fake } = await mount(width)
    await commands.transcriptInput('wheel')
    await waitFor(() => expect(gap(root)).toBeGreaterThan(100))
    await frames()
    const row = Array.from(root.querySelectorAll<HTMLElement>('[data-event-id]')).find(el => el.getBoundingClientRect().top >= root.getBoundingClientRect().top)!
    const before = row.getBoundingClientRect().top
    for (let i = 1; i <= 24; i++) {
      await userEvent.fill(screen.getByRole('textbox'), 'Draft line\n'.repeat(i % 12 + 1))
      act(() => fake.emit(ev({ kind: 'assistant_text', transient: true, payload: { text: ' token\n', done: false } })))
      await frames()
      expect(row.getBoundingClientRect().top).toBeCloseTo(before, 0)
    }
  })
  it(`keeps a read message still when Working toggles at ${width}px`, async () => {
    const { root, fake } = await mount(width)
    await commands.transcriptInput('wheel')
    await waitFor(() => expect(gap(root)).toBeGreaterThan(100))
    await frames()
    const row = Array.from(root.querySelectorAll<HTMLElement>('[data-event-id]')).find(el => el.getBoundingClientRect().top >= root.getBoundingClientRect().top)!
    const before = row.getBoundingClientRect().top
    for (const status of ['idle', 'running', 'waiting', 'running'] as const) {
      act(() => fake.setStatus(status))
      await frames()
      expect(row.getBoundingClientRect().top).toBeCloseTo(before, 0)
    }
  })
  it(`keeps background task details reachable inside the reserved row at ${width}px`, async () => {
    const { root, fake } = await mount(width)
    act(() => fake.setStatus('idle'))
    await frames()
    act(() => fake.emit(ev({ seq: 41, ts: new Date().toISOString(), kind: 'background_task_started', payload: {
      task_id: 'tests', call_id: 'test-call', kind: 'bash', description: 'Run focused tests',
      command: 'npm test -- --project unit\n'.repeat(20), started_at: new Date().toISOString(),
    } })))
    await frames()
    const slot = root.querySelector<HTMLElement>('.agent-status-slot')!
    const height = slot.getBoundingClientRect().height
    await page.getByText('Task details', { exact: true }).click()
    expect(slot.querySelector('details')!.open).toBe(true)
    expect(slot.querySelector('pre')!.textContent).toContain('npm test')
    expect(slot.getBoundingClientRect().height).toBeGreaterThan(height)
    expect(getComputedStyle(slot).overflowY).toBe('visible')
    const command = slot.querySelector('pre')!
    expect(getComputedStyle(command).overflowY).toBe('auto')
    expect(command.scrollHeight).toBeGreaterThan(command.clientHeight)
    expect(command.getBoundingClientRect().height).toBeLessThanOrEqual(192)
    command.scrollTop = command.scrollHeight
    expect(command.scrollTop + command.clientHeight).toBeGreaterThanOrEqual(command.scrollHeight - 1)
    await waitFor(() => expect(gap(root)).toBeLessThanOrEqual(1))
    const dir = import.meta.env.VITE_LAUNCH_SHOTS_DIR
    if (dir) await page.screenshot({ path: `${dir}/chat-stability-details-${width}.png` })
  })
  it(`reclaims idle space and keeps live status stable through streaming transitions at ${width}px`, async () => {
    const { root, fake } = await mount(width)
    expect(screen.getByTestId('working')).toBeTruthy()
    const hint = screen.getByTestId('composer-hint')
    expect(hint.closest('.agent-composer')).toBeTruthy()
    act(() => fake.setStatus('idle'))
    expect(screen.queryByTestId('working')).toBeNull()
    await frames()
    expect(root.querySelector('.agent-status-slot')).toBeNull()
    expect(document.querySelector('.agent-composer-hint')).toBeNull()
    const dir = import.meta.env.VITE_LAUNCH_SHOTS_DIR
    if (dir) await page.screenshot({ path: `${dir}/chat-stability-idle-${width}.png` })
    expect(gap(root)).toBeLessThanOrEqual(1)
    act(() => fake.setStatus('running'))
    await frames()
    expect(root.querySelector('.agent-status-slot')).toBeTruthy()
    expect(gap(root)).toBeLessThanOrEqual(1)
    const toolbarTop = root.getBoundingClientRect().top
    act(() => fake.emit(ev({ kind: 'assistant_text', transient: true, payload: { text: 'First token', done: false } })))
    await frames()
    expect(root.getBoundingClientRect().top).toBe(toolbarTop)
    expect(screen.getByTestId('working')).toBeTruthy()
  })
}

it('keeps following through a 390px visual viewport keyboard resize', async () => {
  const { root, fake } = await mount(390)
  const height = root.clientHeight
  await userEvent.fill(screen.getByRole('textbox'), 'Typing with the keyboard open')
  await page.viewport(390, 480)
  await waitFor(() => expect(window.visualViewport!.height).toBe(480))
  await waitFor(() => expect(root.clientHeight).toBeLessThan(height))
  act(() => fake.emit(ev({ kind: 'assistant_text', transient: true, payload: { text: 'Output while the keyboard is open', done: false } })))
  await userEvent.fill(screen.getByRole('textbox'), 'Two lines\nWith the keyboard open')
  await waitFor(() => expect(gap(root)).toBeLessThanOrEqual(1))
  await page.viewport(390, 900)
  await waitFor(() => expect(gap(root)).toBeLessThanOrEqual(1))
})

it('keeps a native selection drag reader still during streaming', async () => {
    const { root, fake } = await mount(390)
    const stream = () => act(() => fake.emit(ev({ kind: 'assistant_text', transient: true, payload: { text: ' token\n', done: false } })))
    stream()
    await commands.transcriptInput('selection')
    await waitFor(() => expect(gap(root)).toBeGreaterThan(30))
    await frames()
    const row = Array.from(root.querySelectorAll<HTMLElement>('[data-event-id]')).find(el => el.getBoundingClientRect().top >= root.getBoundingClientRect().top)!
    const before = row.getBoundingClientRect().top
    for (let i = 0; i < 10; i++) { stream(); await frames(); expect(row.getBoundingClientRect().top).toBeCloseTo(before, 0) }
})

for (const width of [390, 3840]) {
  it(`follows a long reply and an image decoded after layout at ${width}px`, async () => {
    const { root, fake } = await mount(width)
    act(() => fake.emit(ev({ seq: 41, kind: 'assistant_text', payload: { text: 'A long reply.\n\n'.repeat(200), done: true } })))
    await waitFor(() => expect(gap(root)).toBeLessThanOrEqual(1))
    const last = Array.from(root.querySelectorAll<HTMLElement>('.agent-card.assistant')).at(-1)!
    const image = new Image()
    image.alt = 'Late illustration'
    last.append(image)
    await frames()
    const height = root.scrollHeight
    image.src = 'data:image/svg+xml,' + encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" width="320" height="1200"><rect width="320" height="1200" fill="currentColor"/></svg>')
    await image.decode()
    await waitFor(() => expect(root.scrollHeight).toBeGreaterThan(height + 500))
    await waitFor(() => expect(gap(root)).toBeLessThanOrEqual(1))
  })
}
