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
  output?: (bytes: Uint8Array) => void
  state?: (state: ShellState) => void
  constructor(id: string) { this.info = { shell_id: id, handle: `handle-${id}`, runtime: 'sandbox', folder: `project-${id}` } }
  subscribe(output: (bytes: Uint8Array) => void, state: (state: ShellState) => void) {
    this.output = output; this.state = state; state('running')
    return () => { this.output = undefined; this.state = undefined }
  }
  input(bytes: Uint8Array, human = false) { this.inputs.push(bytes); this.activities.push(human) }
  resize(cols: number, rows: number) { this.resizes.push([cols, rows]) }
  async reconnect(_takeover?: boolean) { this.state?.('running') }
  disconnect() { this.disconnected = true; this.state?.('disconnected') }
  async end() { this.ended = true; this.state?.('closed') }
  emit(text: string) { this.output?.(new TextEncoder().encode(text)) }
}
