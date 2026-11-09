import { expect, it } from 'vitest'
import { proposalNeedsConfirmation } from './proposalApproval'

it.each([
  ['board_file', 'file_ticket', false],
  ['board_file', '', false],
  ['mcp', 'blerg_card_create', true],
  ['mcp', 'blerg_card_update', true],
  ['board_metadata', 'update_board_metadata', true],
  ['mcp', 'blerg_card_archive', true],
  ['mcp', 'blerg_card_comment', true],
  ['mcp', 'send_mail', true],
  ['mcp', 'unknown', true],
  ['board_file', 'unknown', true],
  ['toString', 'unknown', true],
] as const)('tiers %s / %s', (kind, tool, expected) => {
  expect(proposalNeedsConfirmation({ kind, tool })).toBe(expected)
})
