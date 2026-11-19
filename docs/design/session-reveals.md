# Session reveal rules

A request is a durable `ui_reveal` chat event, never a navigation command from a transcript
replay. The action list is deliberately small: reveal a workspace-relative file or directory,
preview a workspace file, open a published file/attachment by ID, or select Live, Plan, History,
Files, or Capabilities. Paths reject traversal, absolute paths, control and bidi formatting characters and alternate
path separators. Requests do not confer file authority.

Default behavior is a Show button in the transcript. Follow is opt-in for the mounted session,
not an account preference. It handles only live requests received after opt-in. It stops on
connection loss, reload and session unmount. Hidden pages, focused editing controls, any open
dialog and another selected session suppress automatic application. Suppressed requests remain
clickable in chat and are never replayed after typing stops. Focus on the Follow checkbox and an untouched, empty composer focused automatically do not
block Follow. Other form controls still defer it.
Every automatic application has
the original transcript event and a local status line.

Workspace previews use the existing viewer tabs. Explorer reveals expand the requested parents,
select and scroll the row, and pulse it without explicitly focusing it. On phones this opens the
existing Explorer sheet; subsequent follow requests are blocked while that modal is open.
Explicit clicks use the viewer's normal focus rules. Panel selection uses the real panel tab
host. Follow never selects a different session or changes the saved panel tab.

Show also works in read-only chats. Standalone and Board chats open available attachments and
workspace files, or explain when a workspace-only action is unavailable. Attachment suggestions
never display storage identifiers. Requests are limited to 30 per session per minute on each server (429 with
Retry-After on excess).

Reactive highlights compare committed UI snapshots. Checklist additions and done-state changes,
Plan ticket additions, and explorer Git status transitions pulse locally; the agent has no pulse
tool. Initial snapshots do not pulse. Reduced motion keeps a steady highlight for the same short
period, with no fading or scrolling animation. Git status alone cannot distinguish two edits
that leave the same path marked Modified; those repeated edits do not retrigger the pulse.
