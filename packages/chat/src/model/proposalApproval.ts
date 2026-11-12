// A presentation rule only. The server still authorizes the frozen request.
// The empty tool is the chat transport's canonical name for a built-in filing draft.
// MCP stays out: any connected server can name a tool anything.
const ONE_CLICK: Readonly<Record<string, readonly string[]>> = {
  board_file: ['', 'file_ticket'],
}

export function proposalNeedsConfirmation(p: { kind?: string; tool: string }): boolean {
  const kind = p.kind ?? 'mcp'
  return !Object.hasOwn(ONE_CLICK, kind) || !ONE_CLICK[kind].includes(p.tool)
}
