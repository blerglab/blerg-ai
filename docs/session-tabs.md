# Workspace tabs

Main is an ordinary channel tab: it can be closed, reordered and split.
Click Main in the Channels sidebar to reopen it. A single click on a workspace
file, a chat file, a published diff, a commit or a Markdown document opens an
italic preview in the focused group. Opening another new view replaces that
group's preview. Double-click its tab or explorer row, or interact with a viewer
control, to keep it. Double-clicking a kept tab (including Main) toggles
[tab focus](command-palette.md); a preview double-click only keeps it open. Kept
tabs stay available when another preview opens.
Selecting an already open view focuses the group that owns it.

Close a tab with its tab's ×, Delete while its tab is focused, or the
**Close current tab** command. Drag tabs within a
group to reorder them; Alt+Shift+Left/Right reorders a focused tab.
Left/Right, Home and End switch tabs within their group. Views, their order,
the selected tab in each group, split directions, divider positions and the
focused group survive reload in this browser, separately for each signed-in
person and workspace. File bytes are read again through the existing authenticated
transport. Earlier saved layouts retain all tabs, including Main. Closing all
tabs leaves a quiet prompt to open a channel or file; messaging, Needs you and
unread indicators remain active. Channel rows highlight every visible channel
quietly, with a stronger highlight for the focused panel. Hidden channels have
no selection highlight. Clearing browser storage clears this preference.

Every tab strip scrolls horizontally with a wheel, Shift+wheel, trackpad or
phone swipe. A thin scrollbar appears on hover or keyboard focus. Workspace tools
stay pinned outside the scroll viewport. The active tab is revealed when the
active identity or tab identities/order change, or the strip itself resizes,
without scrolling its content. Badge, label and preview-style width changes only
recheck overflow and preserve manual scrolling, as does selecting the same active tab.
Dragging near either strip edge scrolls toward the hidden tabs. Canceling a drag
keeps that scroll position; completing a reorder reveals the active tab.

When a group's tabs overflow, **All tabs** at the end of that strip opens a menu
with every tab in that group: its icon, name and Preview state. Choose a tab to
jump to it. Arrow keys, Home/End and Escape work within the menu. This also works
while the group is maximized in tab focus mode.

## Split groups

Drag any tab, including Main, over the tab area. As in VS Code, one translucent
highlight shows where it will land, and nothing else is drawn:

- **Near the left / right edge:** the left or right half of the group is
  highlighted; dropping splits the group side by side.
- **Near the top / bottom edge:** the top or bottom half is highlighted;
  dropping splits it into stacked groups.
- **Center:** the whole group is highlighted; dropping moves the tab into it.
- **Tab strip:** a thin marker between two tabs shows where the tab will be
  inserted; dropping moves it there.

Groups can split again. Tabs move between groups without duplication. Moving or
closing the last tab removes its empty group. Dragging the only tab onto its own
edge leaves it in place; open another tab first. Escape cancels a drag.

Drag a divider to resize it. Focus the divider and use Left/Right or Up/Down to
resize by five percentage points; Home/End choose the limits. Double-click
restores equal sizes. A side-by-side split stacks when its available width cannot
fit two 320 px groups, keeping content inside the workspace area.

The Ctrl/Command+K palette includes **Split tab group right**, **Split tab group
down**, **Move tab to next group**, and **Focus next tab group**. These commands
act on the focused group. Moving to the next group follows visual tree order
and wraps. Splitting requires another tab in that group. Numbered, next,
previous and close shortcuts also use that group's tab order. Escape from file
content returns to Main, including when Main is in another group. See
[the shortcut table](command-palette.md#workspace-tab-shortcuts-200).

Main keeps its transcript, composer and draft in whichever group holds it.
Files fill their group without a composer. Viewer controls and review drafts
retain their React state. Scroll offsets are restored when views move between
groups, within the destination's scroll range. Browsers with atomic DOM moves
also retain iframe documents when both groups survive the move. Embedded HTML
or PDF documents can reload when the source group is removed, when switching
between phone and desktop layouts, or on browsers without atomic DOM moves.
The Main badge indicates agent output received while Main is hidden in every
group; selecting Main clears it. Existing review, markup, download and save-copy
controls remain in the embedded viewers. Files and commits stay inside Blerg. Code, raw text and diffs keep scroll-past-end
space inside the viewer: at least one-third of its height, with a six-line
minimum. Rendered Markdown has modest bottom padding. Resizing a group updates
the reading space without adding workspace-area scrolling.

With one group, its tab strip runs above Explorer and carries the toggles: the
folder toggle at its left end opens Explorer; the **>_** button (the owner shell,
when the runtime reports that it is available), the board button and the workspace
panel toggle sit at its right end. A split adds no row of its own: every group's
tabs stay on the top row, the folder toggle moves to the left end of the first
group's strip, and the **>_**, board and panel toggles to the right end of the
top-right group's strip (the top group's, when the groups are stacked).
Ctrl/Command+backtick still toggles it. Explorer, the workspace panel and the shell
retain their shared resize, collapse, Focus mode and shortcut ownership behavior.
The collapsed workspace-list tile rail remains; there is no separate explorer rail.

On phones (below 900 px), all tabs share one group. The horizontally scrolling
tab strip gets a full-width row below the pinned navigation tools, including
**All tabs** when needed. Splitting commands and dividers are hidden. The desktop
split tree is retained and returns when the screen widens. Navigation history,
Back, **← Main** and the **kept** menu remain available. The kept menu lists only
kept file and shell tabs; Main stays in the tab row and back button. Explorer and the workspace
panel use full-screen sheets with keyboard focus containment, Escape and Back
dismissal. Opening a file closes its source sheet. Notifications remain available
from the workspaces list.

## Former More actions destinations

| Action | Destination |
| --- | --- |
| Explorer | Folder toggle at the left of the tab/navigation bar; palette |
| Star / Unstar | Desktop header star; phone palette |
| Shell | **>_** at the right of the tab/navigation bar; palette and Ctrl/Command+backtick |
| Insights | Desktop header chart; phone palette |
| Pause | Workspace panel → Live → Workspace → Pause for eligible cluster workspaces; palette |
| Skills & plugins | Workspace panel → Capabilities; palette opens that tab |
| Skills & plugins for terminals | Palette, with the existing unavailable explanation |
| Discard | Palette → **Discard workspace…**, with the existing parent/child confirmation |
| End workspace | Desktop header ×; palette (including phones), with confirmation |

Settings keeps appearance, notifications and chat display preferences. No action
is hidden behind a workspace-header More actions menu.

## Channels and workers

The left dock has **Channels**, repository **Files**, and **Repositories**. Main
stands alone first. Chats you start appear under Channels; the heading is hidden
when there are none. Agents start workers, grouped on a collapsed **Workers** line
with a count and state glyphs. A worker needing you or failing is named there.
Expand the line to see the smaller worker rows. Opening a worker keeps its worker
identity. Host, sandbox and cluster glyphs explain runtimes in tooltips and
accessible text; the collapsed group shows a runtime only for host or mixed groups.

The right panel's **Shared** tab holds files published by agents and attached by
you. It is separate from repository Files. Saved dock and tab layouts keep working.

### Recovering and saving layouts

The workspace toolbar's **Layouts** button saves named layouts in this browser for the
signed-in account. Each card shows its pane arrangement, open tabs, saved time and last-used state.
Load, rename or delete it from the same dialog. **Last layout**
restores the most recent workspace layout with more than Main open; **Reset layout**
returns to Main with the explorer and workspace panel closed. Reset offers Undo,
including the previous panel widths and visibility. The command palette also offers Restore layout
and Reset layout. Tab order, split proportions, selection and panel geometry persist
per account and workspace. Named layouts store view identifiers and geometry, never
message or visualization bodies; those tabs resolve their existing bounded snapshots.

Closing tabs or hiding panels offers **Undo**. Ctrl/Cmd+Shift+T reopens recently closed
tabs, one at a time. Shell closure waits eight seconds so Undo can restore the live
terminal; leaving the page completes a pending close. Focus mode (Ctrl/Cmd+Shift+Enter)
hides surrounding panels and other split groups temporarily; **Exit focus** restores them.
