# Session tabs

Chat stays pinned first within its tab group. A single click on a workspace
file, a chat file, a published diff, a commit or a Markdown document opens an
italic preview in the focused group. Opening another new view replaces that
group's preview. Double-click its tab or explorer row, or interact with a viewer
control, to keep it. Double-clicking a kept tab (including Chat) toggles
[tab focus](command-palette.md); a preview double-click only keeps it open. Kept
tabs stay available when another preview opens.
Selecting an already open view focuses the group that owns it.

Close a file with its tab's ×, Delete while its tab is focused, or the
**Close current tab** command. Chat cannot be closed. Drag file tabs within a
group to reorder them; Alt+Shift+Left/Right reorders a focused file tab.
Left/Right, Home and End switch tabs within their group. Views, their order,
the selected tab in each group, split directions, divider positions and the
focused group survive reload in this browser, separately for each signed-in
person and session. File bytes are read again through the existing authenticated
transport. Clearing browser storage clears this preference.

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

Drag any tab, including Chat, over the tab area. As in VS Code, one translucent
highlight shows where it will land, and nothing else is drawn:

- **Near the left / right edge:** the left or right half of the group is
  highlighted; dropping splits the group side by side.
- **Near the top / bottom edge:** the top or bottom half is highlighted;
  dropping splits it into stacked groups.
- **Center:** the whole group is highlighted; dropping moves the tab into it.
- **Tab strip:** a thin marker between two tabs shows where the tab will be
  inserted; dropping moves it there. Chat stays first.

Groups can split again. Tabs move between groups without duplication. Moving or
closing the last tab removes its empty group. Dragging the only tab onto its own
edge leaves it in place; open another tab first. Escape cancels a drag.

Drag a divider to resize it. Focus the divider and use Left/Right or Up/Down to
resize by five percentage points; Home/End choose the limits. Double-click
restores equal sizes. A side-by-side split stacks when its available width cannot
fit two 320 px groups, keeping content inside the session area.

The Ctrl/Command+K palette includes **Split tab group right**, **Split tab group
down**, **Move tab to next group**, and **Focus next tab group**. These commands
act on the focused group. Moving to the next group follows visual tree order
and wraps. Splitting requires another tab in that group. Numbered, next,
previous and close shortcuts also use that group's tab order. Escape from file
content returns to Chat, including when Chat is in another group. See
[the shortcut table](command-palette.md#session-tab-shortcuts-200).

Chat keeps its transcript, composer and draft in whichever group holds it.
Files fill their group without a composer. Viewer controls and review drafts
retain their React state. Scroll offsets are restored when views move between
groups, within the destination's scroll range. Browsers with atomic DOM moves
also retain iframe documents when both groups survive the move. Embedded HTML
or PDF documents can reload when the source group is removed, when switching
between phone and desktop layouts, or on browsers without atomic DOM moves.
The Chat badge indicates agent output received while Chat is hidden in every
group; selecting Chat clears it. Existing review, markup, download and save-copy
controls remain in the embedded viewers. Files and commits stay inside Blerg. Code, raw text and diffs keep scroll-past-end
space inside the viewer: at least one-third of its height, with a six-line
minimum. Rendered Markdown has modest bottom padding. Resizing a group updates
the reading space without adding session-area scrolling.

With one group, its tab strip runs above Explorer and carries the toggles: the
folder toggle at its left end opens Explorer; the **>_** button (the owner shell,
when the runtime reports that it is available), the board button and the session
panel toggle sit at its right end. A split adds no row of its own: every group's
tabs stay on the top row, the folder toggle moves to the left end of the first
group's strip, and the **>_**, board and panel toggles to the right end of the
top-right group's strip (the top group's, when the groups are stacked).
Ctrl/Command+backtick still toggles it. Explorer, the session panel and the shell
retain their shared resize, collapse, Focus mode and shortcut ownership behavior.
The collapsed session-list tile rail remains; there is no separate explorer rail.

On phones (below 900 px), all tabs share one group. The horizontally scrolling
tab strip gets a full-width row below the pinned navigation tools, including
**All tabs** when needed. Splitting commands and dividers are hidden. The desktop
split tree is retained and returns when the screen widens. Navigation history,
Back, **← Chat** and the **kept** menu remain available. The kept menu lists only
kept file and shell tabs; Chat stays in the tab row and back button. Explorer and the session
panel use full-screen sheets with keyboard focus containment, Escape and Back
dismissal. Opening a file closes its source sheet. Notifications remain available
from the sessions list.

## Former More actions destinations

| Action | Destination |
| --- | --- |
| Explorer | Folder toggle at the left of the tab/navigation bar; palette |
| Star / Unstar | Desktop header star; phone palette |
| Shell | **>_** at the right of the tab/navigation bar; palette and Ctrl/Command+backtick |
| Insights | Desktop header chart; phone palette |
| Pause | Session panel → Live → Session → Pause for eligible cluster sessions; palette |
| Skills & plugins | Session panel → Capabilities; palette opens that tab |
| Skills & plugins for terminals | Palette, with the existing unavailable explanation |
| Discard | Palette → **Discard session…**, with the existing parent/child confirmation |
| End session | Desktop header ×; palette (including phones), with confirmation |

Settings keeps appearance, notifications and chat display preferences. No action
is hidden behind a session-header More actions menu.
