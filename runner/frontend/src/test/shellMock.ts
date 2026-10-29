import type { ShellInfo, ShellState, ShellTransport } from '../shellTransport'
export class FakeShell implements ShellTransport {
  readonly info: ShellInfo
  inputs: Uint8Array[] = []
  activities: boolean[] = []
  activityCount = 0
  activity() { this.activityCount++ }
  resizes: [number, number][] = []
  ended = false
  disconnected = false
  output?: (bytes: Uint8Array, replay?: boolean) => void
  state?: (state: ShellState) => void
  constructor(id: string) { this.info = { shell_id: id, handle: `handle-${id}`, runtime: 'sandbox', folder: `project-${id}` } }
  /** Output an earlier view displayed, handed to each new subscriber as replay. */
  replay: string[] = []
  connectionState: ShellState = 'running'
  /** Why it is disconnected, as WebSocketShell reports it. */
  reason: 'elsewhere' | 'outage' | undefined
  disconnectedReason() { return this.reason }
  subscribe(output: (bytes: Uint8Array, replay?: boolean) => void, state: (state: ShellState) => void, _error?: (message: string) => void) {
    this.output = output; this.state = state; state(this.connectionState)
    for (const text of this.replay) output(new TextEncoder().encode(text), true)
    return () => { this.output = undefined; this.state = undefined }
  }
  input(bytes: Uint8Array, human = false) { this.inputs.push(bytes); this.activities.push(human) }
  resize(cols: number, rows: number) { this.resizes.push([cols, rows]) }
  async reconnect(_takeover?: boolean) { this.state?.('running') }
  disconnect() { this.disconnected = true; this.state?.('disconnected') }
  async end() { this.ended = true; this.state?.('closed') }
  emit(text: string) { this.output?.(new TextEncoder().encode(text)) }
  /** Server scrollback replay, as WebSocketShell delivers it: a terminal reset, then the kept bytes. */
  emitReplay(text: string) { this.output?.(new Uint8Array([0x1b, 0x63]), true); this.output?.(new TextEncoder().encode(text), true) }
}
