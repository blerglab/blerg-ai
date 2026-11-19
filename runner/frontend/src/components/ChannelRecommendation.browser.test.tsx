import { afterEach, expect, it } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { cdp, commands, page, userEvent } from 'vitest/browser'
import ChatView from '../../../../packages/chat/src/components/ChatView'
import { createBlergTransport } from '../../../../packages/chat/src/transport/blerg'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'
import { ev, meta, resetChatState } from '../../../../packages/chat/src/test/chat'
import { useTranscriptStore } from '../../../../packages/chat/src/hooks/useTranscriptStore'
import '../index.css'

async function emulateTouch(touch: boolean) {
  const client = cdp() as unknown as { send(method: string, params?: object): Promise<unknown> }
  await client.send('Emulation.setTouchEmulationEnabled', { enabled: touch, maxTouchPoints: 1 })
  await client.send('Emulation.setEmulatedMedia', { features: touch ? [{ name: 'hover', value: 'none' }, { name: 'pointer', value: 'coarse' }] : [] })
}
afterEach(async () => { await emulateTouch(false) })

for (const width of [390, 3840]) for (const action of ['approve', 'keep'] as const) it(`${action} side channel through real runner at ${width}px`, async () => {
  resetChatState()
  await page.viewport(width, 900)
  await emulateTouch(width === 390)
  expect(matchMedia('(pointer: coarse)').matches).toBe(width === 390)
  const fixture = await commands.startSideChannelFixture()
  try {
    const http = createBlergTransport({ baseUrl: fixture.url, getToken: async () => fixture.token })
    const fake = createFakeTransport({})
    const transport = { ...fake.transport, channelRecommendation: http.channelRecommendation, decideChannelRecommendation: http.decideChannelRecommendation }
    const store = useTranscriptStore.getState()
    store.ingest(ev({ session_id: fixture.sessionId, seq: 4, kind: 'channel_recommendation', payload: fixture.recommendation }))
    store.ingestReplayDone(fixture.sessionId, { lastSeq: 4, hasMore: false })
    const mount = () => render(<div style={{ width: '100%', height: 750 }}><ChatView session={fixture.sessionId} transport={transport} meta={meta({ status: 'running' })} /></div>)
    const view = mount()
    const button = await screen.findByRole('button', { name: action === 'approve' ? 'Approve' : 'Keep in this thread' })
    const card = screen.getByTestId('channel-recommendation')
    expect(card.scrollWidth).toBeLessThanOrEqual(card.clientWidth)
    if (width === 390) expect(button.getBoundingClientRect().height).toBeGreaterThanOrEqual(44)
    else expect(button.getBoundingClientRect().height).toBeLessThan(44)
    await userEvent.click(button)
    if (action === 'approve') {
      const link = await screen.findByRole('link', { name: 'Open channel' })
      const response = await fetch(fixture.url + '/fixture/launch', { headers: { Authorization: 'Bearer ' + fixture.token } })
      const launch = await response.json()
      expect(link.getAttribute('href')).toBe('/sessions/' + launch.session_id)
      expect(launch.prompt).toContain('Write a TDD skill')
      expect(launch.prompt).toContain('Create a reusable skill')
      expect(launch.prompt).not.toContain('Unrelated')
    } else {
      await screen.findByText('Kept in this thread')
      const response = await fetch(fixture.url + '/fixture/repeat', { method: 'POST', headers: { Authorization: 'Bearer ' + fixture.token } })
      expect(response.status).toBe(200)
      expect(await response.json()).toMatchObject({ id: fixture.recommendation.id, state: 'kept' })
    }
    view.unmount()
    mount()
    if (action === 'keep') await screen.findByText('Kept in this thread')
    else await screen.findByRole('link', { name: 'Open channel' })
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull())
  } finally { await commands.stopSideChannelFixture(fixture.token) }
}, 120000)
