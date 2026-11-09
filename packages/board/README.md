# Board presentation

Source-shared React presentation for the Board app and runner board tabs, like
the sibling settings primitives. This belongs to the board domain rather than
the published chat package. It has no independent release/version; both hosts
bundle the source. Hosts own loading, credentials, drag behavior and navigation.
Import `BoardPresentation` for columns/cards and `FilterMenu` for filters.

`useColumnVisibility` keeps browser-local preferences per person and board. Pass
the result to `FilterMenu` and filter columns by its `hidden` IDs; use
`HiddenColumnsNotice` outside the column scroller for counts and recovery even
when every column is hidden. Hosts call `announceBoardMove` only after a
successful move so a hidden destination is named. Hosts on different origins
have separate browser storage; no cross-device preference synchronization is
claimed.

Column headers offer a focus control and double-click shortcut. Focus displays the
column's cards in their existing order in a fixed-card-width responsive grid.
The same control returns to all columns; Escape returns when no card or menu is
open. Hosts use `useColumnFocus` with a person/view scope to retain focus on reload.

Runner board tabs have independent instance identities. Each instance saves its
search, filters, release, visible columns, focused column and selected card.
New tabs seed column visibility from the person's board defaults. Existing saved
`board:<board>` identities remain valid. Boards refresh at the existing 15-second
interval with failure backoff, rather than issuing a per-second request stream.

A failed runner refresh keeps the last loaded board and selected card. A
dismissible notice names the last successful load time and the server's error;
successful refresh clears it. Access wording is reserved for actual denials.
Browser reads use human proof and do not consume the session-token agent call
budget. Runner logs failed view status/class and gateway error type, without
card contents.

Workspace navigation retains the last loaded runner board in a bounded in-memory
cache (12 board views). Remounts draw cached cards immediately and refresh
quietly. Only matching account, workspace grant scope, board, query and release
reuse a snapshot; uncached views show the skeleton.
