# Runner command palette and quick open

Press **Ctrl+K** (Windows/Linux) or **Command+K** (macOS), or use the search
icon in the runner header. Search with words or fuzzy abbreviations. Arrow keys
choose a result, Enter opens it, and Esc closes search and restores focus.
Recently used matches appear first for this page's lifetime. Rows show a
keyboard shortcut only when the command has one. Switching sessions omits
the session you are already viewing.

Commands include switching sessions, starting a session, opening Settings,
Insights, Crons, Proposals and Boards, and opening an accessible board or a card
by number (for example, `card #172`). In a session, the mounted features also
register its panel tabs and available engine models/effort levels. End session
opens the existing confirmation; choosing the command alone does not end it.
Tabs with no content or features that are not mounted are not advertised.
The Board keeps its own `/` and Ctrl/Command+K shortcuts.

Global palette shortcuts leave terminal, editor and other text-input controls in
charge of their keys. Focus an app control or use the search button to open the
palette from those surfaces. An existing modal keeps focus and Esc ownership;
palette entry waits until it closes.

Inside a session, **Ctrl+P / Command+P** opens quick open instead of browser
Print. Type a filename, optionally preceded by a repository/directory path,
to search across the session's repositories. Type `>` to switch to commands;
remove it to return to files. Selected files open in the existing workspace
file viewer, with its regular authorization and content protections.

Quick open uses the workspace explorer's bounded search contract:
`GET /api/sessions/{id}/workspace?op=search&path=&filter=...`.
The explorer requires a nonempty name substring. The provider first searches
the final filename component to narrow exact matches, then its first character
for fuzzy candidates, and ranks the combined paths. There are at most two
sequential bounded searches per query, across all repositories, with at most
200 combined file results. Different runtime generations are never combined.
The server's aggregate visit, time, reply and 200-hit limits apply to each search. Partial
results and unavailable repositories are labeled explicitly. This is a bounded
filename search, not an exhaustive filesystem index; an abbreviation spanning
only directory names may not match. Before typing, quick open shows files
selected earlier in this session on this page. Recent names are memory-only,
scoped to the account, and cleared on an observed runtime-generation change.
Opening a recent file rechecks authorization through the regular file route.

Main has a workspace-file read route but no bounded file search. Until the
workspace explorer lands, searching displays an unavailable message. There is
no recursive browser walk, unbounded search or alternate authorization path.
No shell command is shown until the shell feature registers one.

## Registering a feature

`runner/frontend/src/commands/context.ts` exports `usePaletteCommands`.
Pass memoized arrays of items and optional search providers. The registration
is removed when the feature unmounts; capability and permission checks belong
to the feature that supplies the entries. A command has a stable `id`, `title`,
optional `detail`, `keywords` and `shortcut`, and a synchronous `run` action.
The palette restores focus before invoking `run`, so actions can safely open
another dialog. Asynchronous feature actions should handle their own errors.

A provider has `id`, `mode` (`commands` or `files`) and
`search(query, AbortSignal) -> Promise<{ items, note? }>`.
Searches are debounced and canceled on typing, closing and navigation. Replies
from canceled requests are discarded even if a provider ignores the signal.
Providers must use the existing authenticated endpoints and never broaden
access on a failure. Registry results are deduplicated by stable item ID.

P5 can register `Open shell` with the Ctrl+backtick shortcut and an action calling
its existing `ShellWorkspaceHandle.open()` from the feature that owns the
capability-gated handle. No P4/P5 implementation is copied into the palette.
