# Codex session command guidance

Codex sessions with shell access receive the same shared Blerg command guidance
as Claude sessions: topics, ticket pins and checklists. The shared
`sessionWorkGuideLines` bundle feeds the native prompt and managed instructions;
a future command such as the diff command can add its guide line once there.
This change does not advertise a diff command before that feature is available.

## Entry points

- Native unrestricted sessions, including granted MCP sessions, use the shared
  developer prompt. Both new threads and resumed threads receive it.
- Terminal sessions get an owned `AGENTS.override.md` in their private Codex home.
  Host and sandbox launches use the same preparation path. Recovery refreshes
  this file before resuming, including homes created before this change.
- Restricted sessions have no shell and keep their restricted prompt; it does
  not advertise unavailable Blerg shell commands.

The private file preserves the effective source instructions: a nonempty
`AGENTS.override.md` takes precedence over `AGENTS.md`. User configuration,
including `developer_instructions`, remains unchanged. The managed guide comes
first so a native instruction-document budget does not hide it behind a large
user document; copied user text remains in the private file. Native document
budgets still apply, and this change does not increase them.

## File handling

Only the private session home is written. Reads of source instruction symlinks
are allowed to preserve existing dotfile configurations. Reads resolve the target,
open it without following a final replacement symlink, require a regular file,
and enforce a one-MiB cap. FIFOs cannot block the reader. Source and destination
must be different directory identities.

Publication uses a private temporary file and atomic rename. An old instruction
symlink is replaced, without reading or writing its target. User profile files
are never modified, and no new global provisioning flag is introduced.

## Validation

Tests use temporary profiles and fake engines. They cover shared line presence,
restricted exclusion, effective overrides, unchanged user config, recovery,
source symlinks, private symlink replacement, bounded/nonregular reads and
idempotent refresh. Full Runner tests passed in 211.75 seconds after rebasing.
Runner vet, pinned Go lint across all four modules, scrub and link checks passed.
Repeated daemon race runs passed, including independent security tests and the
three unchanged regression tests that caught a formatting change. An independent
comparison confirms the native prompt and managed Markdown preserve their
pre-change bytes.
