# Runner command palette and quick open

Press **Ctrl+K** (Windows/Linux) or **Command+K** (macOS), or use the search
icon in the runner header. Search with words or fuzzy abbreviations. Arrow keys
choose a result, Enter opens it, and Esc closes search and restores focus.
Results are grouped into **Sessions**, **Cards**, **Files** and **Commands**, with
up to five per group. Choose **More** to expand a group. Arrow keys cross group
boundaries. Each group has a label in the listbox for screen readers. Short
names appear above muted details; keyboard shortcuts appear only when registered.

An empty palette, inside a session, opens on **This session**: quick open a file,
toggle the shell, **Switch model…**, **Set effort…**, open the board, split right
and session insights (the first safe one is highlighted; the model and effort
entries fill the search with their choices). Then come at most three **Recent
sessions**: live sessions you opened in this browser, most recent first. Crons,
ended, stopped and archived sessions and the session in view are never offered
there, though search still finds them (`s literary`). Until you have opened one,
your own live sessions appear, latest first. **Commands** follow: **New
session**, **Open board** (outside a session) and **Settings**. Outside a session
the This session group is left out. Type to search all groups, or narrow the
search with a scope:

| Prefix | Scope |
| --- | --- |
| `s ` or `session:` | Sessions |
| `#` | Cards by number or text |
| `/` | Files in the current session |
| `>` | Commands |

A prefix becomes a chip. Remove it with its × button or Backspace in an empty
input. Session names match before repositories, then secondary details. Exact
and prefix names beat fuzzy matches. Recency only breaks ties between similar
matches. Switching sessions omits the session you are already viewing.

Card search starts after three characters in an unscoped query, or immediately
in the Cards scope. It searches the person's accessible boards through the Board's
existing search operation, with debounced requests canceled on further typing,
closing or navigation. Results show the card number and title, board name and
column, and open the existing in-app card modal. Descriptions matched by Board
search are retained even when the title does not contain the query. Number lookup
uses the Board's exact card read. Board commands open a new browser tab without
giving it access to the runner tab; the conversation stays open.

In a session, mounted features register panel tabs, available engine models and
effort levels, pane actions and session actions. **Discard session…** and
**End session…** retain their existing confirmations. **New shell** can open
host-shell consent; **Pause session** stops the cluster runtime. Both receive the same
selection and recency protection, including when a shell is already running.
Confirmed/destructive actions
are marked `destructive`, styled in the danger color, listed last in Commands,
and never boosted by recency or selected by default. When only destructive
results match, Enter does nothing until an arrow key or pointer selects one.
Tabs with no content or features that are not mounted are not advertised.

Pane commands include **Toggle session list**, **Toggle explorer**, **Toggle
session panel**, **New shell** when available, and **Focus mode**. Focus mode
maximizes the active tab (Chat, file, diff or shell), hides the other split groups,
and collapses the explorer, session list and session panel. Double-click a kept tab or
press Ctrl+Shift+Enter (Command+Shift+Enter on macOS) to toggle it; this shortcut
also works inside a terminal. Double-clicking a preview tab only keeps it open.
Dialogs and review comment forms retain this binding for their own actions, including
Ctrl/Command+Enter comment saves with Shift held. The tab bar shows **Exit tab focus**,
with the shortcut in its tooltip. Entering and leaving are announced to screen readers and return
keyboard focus to the active content.

Other groups stay mounted: their tabs, drafts and running shells survive. Selecting
a tab through the focused group's strip, quick open or the palette focuses that tab;
new tabs open in the focused group. Closing the focused tab exits focus mode.
Exiting restores the split tree, ratios and each pane's open state and width,
keeping the group of the last-used tab selected. Tab selections, opens and closes
made while focused are kept;
closing the last tab of a group removes that group as usual. Split/move commands
are available again after exiting. Opening a pane directly also exits focus mode.

Focus mode is temporary: leaving the session, changing accounts or reloading returns
to the normal layout. On phones, which already show one tab, it only collapses the
panes. Width and shell visibility preferences are retained; restoring never launches
a shell. The dedicated pane keys are Ctrl/Command+B for the desktop session list
and Ctrl/Command+backtick for the shell. Other panes use the palette. See
[shared pane controls](workspace-explorer.md#shared-pane-controls).
The Board keeps its own `/` and Ctrl/Command+K shortcuts.

Global palette shortcuts work from the chat composer, search boxes, settings
fields and other ordinary text inputs, preventing the browser's shortcut action.
Terminals, code editors and review comment editors keep their own bindings; use
the search button to open the palette from those surfaces. IME composition is left untouched. An existing
modal keeps focus and Esc ownership; palette entry waits until it closes.

Custom terminal or editor surfaces can set `data-palette-shortcuts="ignore"`
on their container to keep their keys. xterm, Monaco, CodeMirror and Ace containers
are recognized automatically. The review comment editor uses this marker so
Ctrl/Command+K inserts a Markdown link and keeps the editor focused. A local handler can stop propagation to prevent
palette opening; the global capture listener still cancels the browser default.
Held palette shortcuts cancel each repeated keydown without reopening search.

Inside a session, **Ctrl+P / Command+P** opens quick open instead of browser
Print. Type a filename, optionally preceded by a repository/directory path,
to search across the session's repositories. Type `>` to switch to commands;
remove the chip to return to all groups. Selected files open in the existing workspace
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

No shell command is shown until the shell feature registers one.

## Session tab shortcuts (#200)

Tab shortcuts use the focused group. Chat is pinned first in its group. Windows
and Linux use **Alt**; macOS uses **Control** so
Option can keep entering characters. The palette lists these commands with the
platform’s shortcut. Numbered commands follow the current tab order, including
previews; closing or reordering a tab updates their positions.

| Command | Windows / Linux | macOS |
|---|---|---|
| Back to Chat | Esc in file content; Alt+1 when Chat is first in this group | Esc in file content; Control+1 when Chat is first in this group |
| Go to tab 1 through 9 in the focused group | Alt+1…Alt+9 | Control+1…Control+9 |
| Next tab (wraps within group) | Alt+] | Control+] |
| Previous tab (wraps within group) | Alt+[ | Control+[ |
| Close current file tab | Alt+W | Control+W |

Escape returns to Chat while a file, diff, commit or artifact tab has focus; it
keeps the tab open. Text fields, menus and popovers retain Escape. Modified tab
keys work from the composer, file content, explorer and panel. Terminals and
editors keep their keys, and IME composition is ignored. Claimed keys prevent the
browser default, including held repeats and unavailable tab numbers. A visible
modal, menu or popover blocks tab navigation. Chat cannot be closed. Ctrl+W on
Windows/Linux and Command+W on macOS remain the browser’s close-tab keys.

**Split tab group right**, **Split tab group down**, **Move tab to next group**,
and **Focus next tab group** are available in Ctrl/Command+K on desktop. They
provide keyboard equivalents for splitting and moving tabs between groups.
Splitting moves the active tab; open a second tab first. Focus and move follow
visual tree order and wrap. Phones show one group while retaining the desktop
layout. See [session splits](session-tabs.md#split-groups).

## Registering a feature

`runner/frontend/src/commands/context.ts` exports `usePaletteCommands`.
Pass memoized arrays of items and optional search providers. The registration
is removed when the feature unmounts; capability and permission checks belong
to the feature that supplies the entries. A command has a stable `id`, `title`, optional `detail`, `keywords` and `shortcut`,
and a synchronous `run` action. `group` defaults to `commands`; session items
provide a separate `repo` for ranking. Only safe common actions set `empty: true`.
Every registered feature action must explicitly set `destructive` to a boolean;
TypeScript and a runtime registration guard reject omissions. Set it to `true`
for any action that grants access, changes permissions, destroys or stops work,
or opens a confirmation. Set it to `false` only for safe actions such as navigation.
Command search providers are checked before their results are shown.
Pane helpers require the same explicit choice and preserve it on the command.
Real-command regressions cover End, parent/child Discard, Pause and shell consent;
add the equivalent regression when adding another sensitive action.
The palette restores focus before invoking `run`, so actions can safely open
another dialog. Asynchronous feature actions should handle their own errors.

A provider has `id`, `mode` (`sessions`, `cards`, `files` or `commands`) and
`search(query, AbortSignal) -> Promise<{ items, note? }>`.
Searches are debounced and canceled on typing, closing and navigation. Replies
from canceled requests are discarded even if a provider ignores the signal.
Providers must use the existing authenticated endpoints and never broaden
access on a failure. Registry results are deduplicated by stable item ID.

P5 can register `Open shell` with the Ctrl+backtick shortcut and an action calling
its existing `ShellWorkspaceHandle.open()` from the feature that owns the
capability-gated handle. No P4/P5 implementation is copied into the palette.

Card summaries use `GET /api/board-cards/{board}/search?q=...`. The route accepts
only a signed-in person, honors project-limited sign-ins, and exchanges a read-only
Board credential for that person. It uses `blerg_card_search` (the same search
implementation as the Board REST search API) plus a single column-list read.
Responses contain only summary fields, use `Cache-Control: no-store`, and inherit
request cancellation. The browser discovers boards via `/api/session-boards` and
limits concurrent board requests to four. Access refusals contribute no results.

Focused owner shells reserve only Ctrl+K/P on Windows/Linux or Command+K/P on macOS
for search. All macOS Control keys reach the shell. Tab shortcuts do not switch
or close tabs while terminal input has focus.
