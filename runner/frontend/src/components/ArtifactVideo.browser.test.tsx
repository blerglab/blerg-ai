import { browserWidths } from '../test/browserWidths'
import { beforeEach, expect, it } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ChatView } from '@blerglab/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'
import { ev, meta, resetChatState, seed, SESSION } from '../../../../packages/chat/src/test/chat'
import '../index.css'

// Generated solid-color portrait WebM, one second, no audio.
const clipBytes = Uint8Array.from(atob('GkXfo59ChoEBQveBAULygQRC84EIQoKEd2VibUKHgQJChYECGFOAZwEAAAAAAARKEU2bdLpNu4tTq4QVSalmU6yBoU27i1OrhBZUrmtTrIHYTbuMU6uEElTDZ1OsggEiTbuMU6uEHFO7a1OsggQ07AEAAAAAAABZAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAVSalmsirXsYMPQkBNgI1MYXZmNTguNzYuMTAwV0GNTGF2ZjU4Ljc2LjEwMESJiECPQAAAAAAAFlSua8WuAQAAAAAAADzXgQFzxYjYXj6YDpqi1JyBACK1nIN1bmSGhVZfVlA4g4EBI+ODhAJiWgDgAQAAAAAAAAmwgSC6gUCagQISVMNnQJpzcwEAAAAAAAAnY8CAZ8gBAAAAAAAAGkWjh0VOQ09ERVJEh41MYXZmNTguNzYuMTAwc3MBAAAAAAAAX2PAi2PFiNhePpgOmqLUZ8gBAAAAAAAAIkWjh0VOQ09ERVJEh5VMYXZjNTguMTM0LjEwMCBsaWJ2cHhnyKJFo4hEVVJBVElPTkSHlDAwOjAwOjAxLjAwMDAwMDAwMAAAH0O2dUJs54EAo7+BAACAEAMAnQEqIABAAABHCIWFiIWEiAICAnWqA/gCB1CDNHGGgP7/TRL//FhX8WFfxYV/8WFf/PzO7cX85gCjlYEAKACxAQAFEKwAGAAYWC/0AAiUAKOVgQBQALEBAAUQrAAYABhYL/QACJQAo5WBAHgAsQEABRCsABgAGFgv9AAIlACjlYEAoACxAQAFEKwAGAAYWC/0AAiUAKOVgQDIALEBAAUQrAAYABhYL/QACJQAo5WBAPAAsQEABRCsABgAGFgv9AAIlACjlYEBGACxAQAFEBAUYABhYL/QACJQAKOVgQFAALEBAAUQrAAYABhYL/QACJQAo5WBAWgAsQEABRCsABgAGFgv9AAIlACjlYEBkACxAQAFEKwAGAAYWC/0AAiUAKOVgQG4ALEBAAUQrAAYABhYL/QACJQAo5WBAeAAsQEABRCsABgAGFgv9AAIlACjlYECCACxAQAFEKwAGAAYWC/0AAiUAKOVgQIwALEBAAUQrAAYABhYL/QACJQAo5WBAlgAsQEABRCsABgAGFgv9AAIlACjlYECgACxAQAFEKwAGAAYWC/0AAiUAKOVgQKoALEBAAUQrAAYABhYL/QACJQAo5WBAtAAsQEABRAQFGAAYWC/0AAiUACjlYEC+ACxAQAFEKwAGAAYWC/0AAiUAKOVgQMgALEBAAUQrAAYABhYL/QACJQAo5WBA0gAsQEABRCsABgAGFgv9AAIlACjlYEDcACxAQAFEKwAGAAYWC/0AAiUAKOVgQOYALEBAAUQrAAYABhYL/QACJQAo5WBA8AAsQEABRCsABgAGFgv9AAIlAAcU7trkbuPs4EAt4r3gQHxggHC8IED'), c => c.charCodeAt(0))
const clip = { id: 'clip', name: 'clip.webm', size: clipBytes.length, content_type: 'video/webm', view: 'video', version: 1 }
beforeEach(resetChatState)

it.each(browserWidths([390, 3840]))('plays inline with stable layout and opens larger at %ipx', async width => {
  await page.viewport(width, width === 3840 ? 2160 : 844)
  seed(ev({ kind: 'artifact', seq: 1, payload: clip }))
  const t = createFakeTransport({ files: { items: [clip] } })
  t.files!.serve(clipBytes)
  render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
  const card = await screen.findByTestId('artifact-card')
  const video = card.querySelector('video')!
  expect(video).not.toBeNull()
  expect(video.controls).toBe(true)
  expect(video.autoplay).toBe(false)
  expect(video.preload).toBe('none')
  expect(t.files!.rawCalls).toEqual([])
  const frame = video.parentElement!
  const before = frame.getBoundingClientRect()
  const cardHeight = card.getBoundingClientRect().height
  expect(card.getBoundingClientRect().right).toBeLessThanOrEqual(width)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  expect(before.width).toBeGreaterThan(100)
  expect(before.right).toBeLessThanOrEqual(width)
  expect(before.height).toBeLessThanOrEqual(320)
  await userEvent.click(within(card).getByRole('button', { name: 'Play clip.webm' }))
  await waitFor(() => expect(video.videoHeight).toBe(64))
  expect(video.videoWidth).toBe(32)
  expect(getComputedStyle(video).objectFit).toBe('contain')
  expect(frame.getBoundingClientRect().height).toBe(before.height)
  expect(card.getBoundingClientRect().height).toBe(cardHeight)
  expect(card.textContent).toContain('clip.webm')
  expect(document.querySelectorAll('video')).toHaveLength(1)
  await userEvent.click(within(card).getByRole('button', { name: 'Open larger' }))
  expect(await screen.findByRole('dialog', { name: /clip.webm/ })).toBeTruthy()
})

it('falls back to the card and Open for an unsupported video', async () => {
  seed(ev({ kind: 'artifact', seq: 1, payload: clip }))
  const t = createFakeTransport({ files: { items: [clip] } })
  t.files!.serve('unsupported video bytes')
  render(<ChatView session={SESSION} transport={t.transport} meta={meta()} />)
  const card = await screen.findByTestId('artifact-card')
  await userEvent.click(within(card).getByRole('button', { name: 'Play clip.webm' }))
  await waitFor(() => expect(card.querySelector('video')).toBeNull())
  await userEvent.click(within(card).getByRole('button', { name: 'Open' }))
  expect(await screen.findByRole('dialog', { name: /clip.webm/ })).toBeTruthy()
})
