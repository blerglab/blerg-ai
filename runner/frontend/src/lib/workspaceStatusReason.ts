const reasons: Record<string, string> = {
  metadata_pointer: 'Git metadata contains an unapproved or changed repository pointer. Status remains disabled for this binding.',
  metadata_limit: 'Git metadata exceeds the status read limit (256 MiB, 200,000 entries, or 32 directory levels).',
  metadata_changed: 'Git metadata changed during validation. Try Refresh.',
  metadata_read_refused: 'Git metadata could not be safely read. Check permissions, symlinks, and special files.',

  metadata_refused: 'Git metadata failed its safety check. It may have changed, contain an unsupported pointer, or exceed the read limit.',
  identity_changed: 'The repository or Git directory identity changed. Refresh to reconnect it.',
  snapshot_refused: 'The private Git metadata snapshot could not be prepared. Check daemon storage and permissions.',
  git_failed: 'The hardened Git status command could not complete. Check the daemon Git installation and resource limits.',
  invalid_status: 'Git returned an unsupported or oversized status response.',
  deadline: 'Git status exceeded its read deadline. Try Refresh.',
}
export function workspaceStatusReason(code: unknown): string | undefined {
  return typeof code === 'string' && Object.hasOwn(reasons, code) ? reasons[code] : undefined
}
