# Files from a session (artifacts)

A Claude Code session can hand you a **file**: a report, a chart, an export, a generated
document. The agent runs one command, and the file shows up as a card in the session's chat and in
the **Files** tab of the session panel (a dialog from the chat's Files button where the chat has
no panel: the board), where you can download it or, for most types, read it in the app.

```bash
blerg-runner publish report.md
blerg-runner publish out/chart.png --name "latency-p99.png"
blerg-runner publish ./dist/site        # a directory is zipped and published as site.zip
```

The command prints what happened, including whether the app can show the file:

```
Published report.md (12.4 KB) — viewable in the app
Link: /sessions/<session id>?artifact=<artifact id>
```

```
Published report.docx (240.0 KB) — no in-app preview (download only)
Hint: if a preview would help the user, publish a pdf, html or markdown version as well.
```

`blerg-runner publish --help` lists the same guidance for the agent, and the instructions every
Claude Code session reads point at it, so the agent picks a format you can read without
downloading. The link opens the session with that file in the viewer.

## Opening files the agent names

A file reference in chat, such as `.agent-notes/report.md` or
`attachments/report.md`, opens from the live session's folder for its owner.
Benign dot folders and git-ignored files can be opened directly. Protected
filenames and folders that commonly hold secrets stay blocked with an explanation.

Upgrade-era desktops in shared-credential compatibility mode retain access for
people with historical sessions on that desktop. Enrolling the desktop preserves
the owner's access. If access is refused, use **Settings → Account → Your desktops
→ Enroll**, or ask an administrator to assign the desktop to you. Another person's
session files require that session's owner to open or publish the file for you;
a private session remains invisible to other people.

## Inline in the chat

An image (up to 10 MiB) is drawn in its card, and a video or audio file gets a play tile; the
same goes for an image, video or audio file you attach to a message, once it is in the Files
list. Click the picture or the tile to open the file in the viewer — a video or audio file
starts playing at once. Everything else keeps the plain card with **View** and **Download**.
Previews are fetched through the same authenticated route as the viewer, when the card scrolls
into view, and only once per file.

## What the app can show

Each file gets a **view** from its name (and, for images and PDFs, its first bytes). Anything the
app cannot show is download only.

| Files | Shown as |
|---|---|
| Markdown (`.md`, `.markdown`) | Formatted text, including Mermaid diagrams and inline-data Vega-Lite charts. Raw HTML is never run. |
| Plain text, logs and source code (`.txt .log .py .js .ts .tsx .go .rs .java .c .h .cpp .sh .yaml .yml .toml .ini .xml .sql .css .rb .php` and similar, and a file with an unknown extension whose bytes are text) | Monospace text. The first 200 KB, with **Show all**; over 2 MiB it is download only. |
| Diffs and patches (`.diff`, `.patch`) | The diff viewer: see [Diffs](#diffs). Over 2 MiB, download only. |
| JSON (`.json`) | Pretty-printed; Vega-Lite schema JSON and `.vl.json` / `.vega-lite.json` draw charts. Same size rules as text. |
| Mermaid (`.mmd`, `.mermaid`) | A diagram, scaled to fit the viewer, with source, copy and zoom controls; it also draws on its card in the chat. Invalid or refused source stays code, with the reason. Color styling (`classDef`, `class`, `style`) is drawn; external resources are refused. |
| CSV and TSV | A scrollable table: the first 500 rows and 50 columns, with a note when there is more. |
| Images: `.png .jpg .jpeg .gif .webp .svg` | The image. An SVG is shown only through `<img>`, where scripts in it never run. |
| PDF | The browser's PDF viewer, with **Open in new tab**. |
| Audio: `.mp3 .wav .ogg .m4a` and video: `.mp4 .webm` | A player. |
| HTML (`.html`, `.htm`) | A sandboxed page: its scripts run, but it cannot reach the app, your session, cookies or storage, and it **cannot use the network at all**. See below. |
| Office files and other binaries (`.docx .xlsx .pptx .zip`, and the rest) | No preview: name, size and type, and **Download**. |

Not offered as a preview, on purpose: Office documents (there is no safe in-browser renderer to
bundle), archives, and executables. The agent is told to send a PDF, HTML or Markdown version
when you only need to read something.

### Choosing a format

The guidance the agent reads:

- To read or review: Markdown, HTML or PDF.
- Data: CSV or JSON.
- A visual: PNG, SVG or HTML. To show you something for feedback — a UI state, a rendered page, a
  diagram — the agent screenshots it and publishes the PNG, which shows inline in the chat.
- Something you will edit in Office: DOCX or XLSX (download only).
- An HTML file must be **one self-contained file**: inline CSS and JavaScript, images as `data:`
  URIs. It cannot load anything from the network.

## Diffs

To show you what it changed, the agent runs `blerg-runner diff`:

```bash
blerg-runner diff                         # everything not committed, new files included
blerg-runner diff --staged
blerg-runner diff main..HEAD --title "Retry the upload"
blerg-runner diff 1a2b3c4 5d6e7f8         # commits, each with its own patch
blerg-runner diff HEAD -- src/            # narrowed to paths
```

```
Published diff Retry the upload · 3 files · +12 −4 — viewable in the app
Link: /sessions/<session id>?artifact=<artifact id>
```

The command runs `git` in the session's folder itself, with a fixed command line and no shell:
revisions are resolved to commits first (nothing that starts with `-` is taken for one), paths go
after `--`, and nothing the repository's own settings name is run: hooks, the fsmonitor, filter
drivers, external diff tools, text conversions, signature checks and pagers are all overridden, and
git may not fetch. Binary files are named with their size (`binary, 5 -> 7 bytes`), never embedded;
commit authors appear by name, without their email. The patch is published as `<title>.diff` (a file
like any other: it counts against the limits below, and the command refuses one over the per-file
limit, saying both sizes).

In the chat its card says "3 files · +12 −4" and names the first files (a patch over 256 KiB keeps the
plain card). **View** opens the diff viewer: the files with their +/− on the side (on top on a phone),
the patch unified, or **Side by side** on a wide screen, hunk headers, line numbers on both sides,
long unchanged stretches folded behind a click, each file colored by its extension with your
[code colors](talking-to-an-agent.md), commit headers, **Copy patch** and **Download**. A file shows
5 000 lines before **Show more**. Any `.diff` or `.patch` you publish or attach opens the same way.

A checklist item ticked with `--commit` gets the diff of its commits the same way, opened from its
**View diff** ([Tickets and checklists](talking-to-an-agent.md#tickets-and-checklists)).

## Versions

Publishing a file whose **name already exists** in the session, from the same side (the agent's
files, or the ones you attached), makes the next **version** of it instead of an unrelated
duplicate. To revise a file the agent just publishes it again under the same name, and says so:

```
Published report.md as v3 (previous: v2) — viewable in the app
```

The first publish of a name prints no version note.

- **Numbering.** A version number is stored, never recomputed: deleting v1 leaves v2 as v2, and the
  next publish is v3. The agent's files and your uploads of the same name are separate sequences.
  Migration `033` numbered the files that already existed, per name, oldest first.
- **In the chat.** The card reads `📎 report.md · v3 · 12 KB`. The version shows from v2 on (and on
  a v1 card once its file has later versions); a file with a single version has none.
- **In the Files tab (or panel).** One row per file, showing the latest version with a `v3` badge. Under it,
  **Earlier versions (2)** opens the older ones with their own time, Download, View and Delete.
  Deleting acts on that one version; deleting the latest makes the previous one the latest. The
  count above the list (and the board's Files button) counts files, not versions. The same grouping applies to "Uploaded by
  you".
- **In the viewer.** The title reads `report.md · v2 of 3`, and an older version says so, with a
  **View latest** button. A `?artifact=<id>` link opens any version.
- **Downloads.** The latest version is saved under the plain name, an older one as `report-v2.md`
  (a name without an extension just gets `-v2`).
- **For the agent.** `blerg-runner fetch --all` fetches every attachment ID, including older
  files with the same name; `fetch --all-versions` fetches every version, saved as `name-v1.ext`,
  `name-v2.ext`; `fetch --list` shows their IDs and versions. Already fetched, unchanged files are
  skipped. `publish --help` and the instructions every Claude Code session reads carry the "publish again under the same name" rule.
- **API.** An artifact (list entry, upload reply, the `artifact` event) has `version`; the list also
  has `latest_version`, and the upload reply has `latest_version` (the version just made) and `previous` (the version it follows, or `null`).
- **In the History tab.** The session panel's History tab lists each file under the topic that
  published it, with every version as a chip; a file published outside every topic is listed under
  the "Unmarked work" line instead
  ([talking-to-an-agent.md](talking-to-an-agent.md#history)). An agent marks its topics with
  `blerg-runner topic`, and names the commits a topic landed with `topic --done --commit <sha>`.

## Limits

- 100 MiB per file (`BLERG_RUNNER_MAX_FILE_MB`). A larger file is refused before it is sent: the
  command checks it against the limit the runner reports, and says both sizes. It checks the
  session's 1 GiB total below the same way.
- There is no lifetime file-count cap. The agent frees published space with
  `blerg-runner unpublish <name>` (`blerg-runner files` lists what it published); a file
  you attached is removed from the Files panel by you, the session owner or an administrator.
- 1 GiB in total for the files one session published (`BLERG_RUNNER_MAX_SESSION_ARTIFACTS_MB`).
  With the attachments below, one session can hold about 1.5 GiB, so size the runner's data volume
  for the sessions you keep, and on a shared machine or cluster node give it a disk quota (see
  [`SECURITY.md`](../SECURITY.md)).
- 20 versions per file name (per side). A 21st is refused with "This file already has 20 versions;
  delete an old one first."
- A file name is cut to its base name (no folders), 120 bytes at most, with control characters
  removed.
- A directory is zipped by the command (skipping `.git`, `node_modules` and symlinks, at most
  eight times the per-file limit of input, 800 MiB by default) and the zip must fit in the per-file
  limit.
- Files can only be published while the session is running.

## Who can see a file

The same people who can see the session. A session anyone on the install can see shows its files
to everyone; a **private** session's files are visible to its owner alone, and everyone else gets
the same "not found" an unknown file gets. The browser routes let a signed-in person open or
download visible files. The session owner or an administrator may delete any file; a person may
delete the attachments they attached. The runner contract applies the same visibility and deletion
rules to credentials acting for an account; a broker credential (the shared runner key the board
uses, or a service token) decides for the person it names in `X-Blerg-Acting-Account`, after
the runner checks with core that the account exists, is active and can see the session (its real
role decides administrator status); it deletes nothing when it names no valid person or core
cannot be reached. Each deletion is recorded with who made it and how. Publishing uses the session's own messaging token, and that token
only works for that one session.

## How a page is shown safely

The file's bytes are agent-authored, and they are served from the app's own origin, so they are
never trusted:

- The content type is chosen by the server from the file's name and bytes, never from what the
  uploader claimed.
- Downloads are attachments, sent as `application/octet-stream` when the type could render (HTML,
  SVG, XML), with `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox` and
  `Cache-Control: private, no-store`. The viewer's `raw` route sends only PNG, JPEG, GIF, WebP and
  PDF with their own type; everything else is `application/octet-stream`.
- Text, Markdown, JSON and CSV are read as text and drawn by the app (escaped), never inserted as
  markup.
- An HTML file runs in an `<iframe sandbox="allow-scripts">` with **no** `allow-same-origin`,
  forms, popups or top navigation, so it has an opaque origin and cannot touch the app, your
  login or its storage. A Content-Security-Policy is placed at the very top of the page
  (`default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; script-src 'unsafe-inline';
  font-src data:; media-src data: blob:; form-action 'none'; base-uri 'none'; frame-src 'none'`),
  which cuts off the network (no `fetch`, no images or scripts from elsewhere) and stops the page
  submitting a form, changing where its links point, or embedding another page.
- In the chat, an HTML file (up to 2 MB) also shows as a small preview in its card: the page
  itself, laid out at desktop width and scaled down. It runs in the same sandbox with the same
  policy, and from the chat it cannot be clicked, scrolled or focused: a cover over it opens the
  viewer, which is where the page can be used. The preview only exists while its card is near
  the visible part of the chat.

## Attaching a file to a board card

A session that the board started for a card (its prompt is the card's task) can attach what it
publishes to that card:

```bash
blerg-runner publish report.pdf --card            # this session's own card
blerg-runner publish report.pdf --card-id <card>  # another card on the same board
```

The file is published as usual and the card gains an **artifact** link, `report.pdf (v2)`, that opens
the file in the runner's viewer in a new tab. Opening it still needs a signed-in person with access to
the session, so a private session's file stays private; but everyone who can read the card sees the
file's **name and the session id**, so do not publish a file whose name is itself sensitive with
`--card`. Attaching again with the same version is a no-op. If the card cannot be reached, or the board's
admission gate holds the change for review, the command says so and the file is published anyway.

The board checks the link: it must be exactly the runner's viewer address for one file of one session
(the board needs `RUNNER_UI_BASE`, which the installers set), and only `http(s)` addresses are made
clickable on a card. The same link can be added through the board's API
(`PATCH /api/cards/{id}` with `{"add_links": [{"kind": "artifact", "url": "/sessions/<id>?artifact=<file id>"}]}`,
an atomic append) or its MCP tool `blerg_card_link`.

## Uploading files to a session

The other direction works too: you can attach files (for example two PDFs to analyze) to a message
from the chat box, and the session's agent fetches them into a private folder outside repositories.

- **Attach** with the paperclip next to the message box, by dragging files onto the chat, or by
  pasting them. Each file uploads immediately and shows as a chip under the box with its size and a
  remove button; removing a completed chip deletes the upload. Send is disabled while an upload is
  running; files waiting for space do not hold it: Send sends your message without them and they
  stay waiting.
- **Limits:** 100 MiB per file (larger ones are refused in the browser, which says the limit), 10
  files per message, and 500 MiB of attachments per session. The per-session total and the most
  an administrator may raise one session to (5000 MiB) are administrator settings in Core
  (**Settings → Session storage**); `BLERG_RUNNER_MAX_SESSION_UPLOADS_MB` is the initial
  per-session value until one is saved. There is no lifetime attachment-count cap.
  The agent’s published files have a separate 1 GiB quota; uploading is refused once the session has ended. A runner older than 0.2.1 does not report
  its limits, and the chat then assumes its old 25 MiB.
- **Waiting for space:** an attachment that would exceed the session’s attachment total stays
  queued with “Waiting for space,” and one note under the chips says the session’s attachments are
  full and that you can delete older attachments to make room. (When the runner cannot be reached
  to check, the chip says “Waiting to reach the server” instead.) Uploads to one session go one at
  a time, so files chosen together that only fit one by one wait rather than fail. Its bytes and
  unsent completed chips are saved locally in IndexedDB under the current account and runner, so
  reloading or switching sessions (even mid-upload) keeps the queue without exposing it to another
  account using the same browser. A waiting file belongs to the browser tab it was chosen in:
  only that tab uploads it, and another tab on the same session shows it as “Waiting in another
  tab” (or “Attached in another tab”) without sending it. When that tab is closed, the next tab
  takes the file over; reloading a tab keeps its files. The saved bytes are removed when the sign-in ends, when the session is discarded, when it
  ends (the waiting file then fails), and after seven days without the session being opened. If
  saving to browser storage fails, the chat tells you to keep the page open. Removing a waiting
  chip cancels it.
- **Free up space…** on a waiting chip or in the session panel’s Files list opens the same
  cleanup dialog. It lists all file versions oldest first with their sizes; when files are waiting,
  it preselects the oldest attachments whose sizes cover the shortfall. Published files count
  against a different quota and are not preselected for attachment space. Only files you may
  delete can be chosen (your own attachments, or every file for the owner or an administrator).
  Published files retain their removed marker in History. Each attachment can also be deleted on
  its own from the session panel’s **Files** tab, where Delete shows only on files you may delete.
  Waiting uploads resume after a deletion, on refocus, on reconnecting, or on the next five-second
  space check. A single file over the per-file limit still fails immediately.
- **Increase storage:** an administrator sees **Increase storage** under waiting files; each click
  raises this session’s attachment total by 500 MiB, up to the install maximum, and is recorded
  with who made it. Anyone else is told to ask an admin to increase this session’s storage.
- **When an upload or a download fails**, the chat shows what the runner said: which step failed
  and why (for example that its disk is full, or that it may not write to its data folder) and what
  to do, followed by a short error id. Whoever runs Blerg finds the same id in the runner's log. Name handling and the content type
  work exactly as for an agent's files (the server decides the type; folders in names are dropped).
- **What the agent sees:** your text, then a blank line and an attachment note containing each
  file's ID, name and size. The chat uses the IDs for previews, so a later upload with the same
  name cannot replace an earlier message's picture. A review or an image mark-up names its files
  the same way. An older message with a name-only note (sent before 0.4.4) shows the newest
  upload of that name made at or before the message, never a later one. A pasted screenshot (a
  clipboard image called `image.png`, or with no name) gets a unique `screenshot-…` name; a copied
  file keeps its own name. If
  you attach files without typing anything, the message starts with "Please take a look at the
  attached files." The chat shows the files as chips on your message. It works while the session is
  running too: the message is queued like any other.
- **How the agent gets them:** `blerg-runner fetch --all` downloads every attachment into
  `$BLERG_RUNNER_ATTACHMENTS_DIR/` outside repositories (or an explicit `--out DIR`), never overwriting an existing file. Same-name files are named
  by upload time: the oldest keeps `name.ext`, later ones are `name-1.ext`, then `name-2.ext`,
  whichever order they are fetched in. Each `Saved` line names the attachment ID next to the local
  file, and a skipped file reads `Already have <id> at <path>`. `fetch --list` lists IDs and where
  each was saved; pass an ID to fetch one attachment (a name is refused, with the matching IDs as
  a hint). Repeating `fetch --all` skips unchanged downloads using the hidden `.blerg-fetch/`
  ID and hash index. Keep that folder with the files. Edited files are preserved and a new copy
  is saved. Downloads from before this tracking was added are preserved and fetched once more.
  It uses the session's own token, which only works for that session, and it only serves files
  you uploaded, never the agent's published ones.
- **Privacy:** an upload belongs to its session. Anyone who can see the session can see the uploaded
  files in the Files panel (under "Uploaded by you" / their own), and a private session's files
  are visible to its owner alone. Uploads are stored on the same volume as published files
  (`origin = 'user'` in `session_artifacts`, migration `032`), deleted from the Files panel or with
  the session, and there is no chat event for them, only the message you send.
- **Untrusted content:** a file you attach is data. The agent is told to use it for the task you
  gave and not to follow instructions found inside it, but a prompt hidden in a document can still
  try to steer a model; attach files you would be comfortable having the agent read.

The routes behind this: `POST /api/sessions/{id}/uploads` (a signed-in person), and, for the agent's
token, `GET /api/sessions/{id}/attachments` and `GET /api/sessions/{id}/attachments/{aid}/file`
(always `application/octet-stream`, `nosniff`).

## Review and mark-up

A published file can be answered precisely, without quoting by hand. Open it in the viewer. A
review or a diff fills most of the window; the **⤢** button in the viewer's header (or
**Ctrl+Alt+F**, Command+Option+F on a Mac) takes it edge to edge, and the choice is remembered
separately for files, reviews, diffs and image mark-up.

- **Review** (markdown and PDF). The document is shown as it reads: rendered markdown, or the
  PDF's real pages. Select a passage and press **Ctrl+Alt+M** (or **Add comment**) to ask for a change
  there; the request is anchored to the quote, the heading above it, and for a PDF the page. For a
  Markdown file can be edited on the rendered page; **Show changes** marks insertions and
  deletions, while **Clean view** hides the marks. **Source** offers direct source editing. Requests list in the margin and their quotes are highlighted. **Submit** sends
  everything to the session in one message: the requests, each with an id, and your edits as a
  diff with the edited copy attached.
- **Review** (diffs and patches). Select an old or new line number and choose **Request change**.
  Shift-click another line to select a range, or enable **Select range** and tap the first and last
  lines on a phone. Ranges stay within one side of one hunk (at most 200 lines and 32 KiB).
  Save several requests, then **Submit** sends one message and the existing `.review.json` file.
  Each request records its source artifact, file, side, line range, hunk and commit when present.
  **Show lines** opens its target, including folded context. Requests from an earlier version stay
  listed without being pinned onto the new patch. The agent changes the source files, replies
  with the same review command and posts an updated diff.
- **Mark up** (images, the session's or your own). Pen, arrow and box drawings become individually
  selectable annotations with comments, style editing, hide/show and delete. **Comment** adds a
  numbered point comment. **Send** attaches a flattened copy of visible annotations and puts their
  numbered notes in the message; the original image stays unchanged.

The session makes the changes, publishes the file again, and answers each request as done or
declined with a line; the answers show beside your requests the next time you open the review.
Markdown, PDF and code reviews label these threads **Resolved** or **Rejected**. Collapse a settled
thread individually, or use **Hide resolved** to hide both kinds together. **Reopen** opens a comment editor and requires an explanation. **Reopen with comment** makes the
thread pending while keeping its earlier answers; **Cancel** leaves its status unchanged. The
explanation appears in the thread. **Submit** sends it with the original request, prior answers and any new
comments or edits. Hidden threads and their anchors remain in the saved review.

What is stored: `<file>.review.json`, uploaded beside the file on every submit (the requests,
their anchors, and the diff), and the session's copy of the same name with its replies. The viewer
merges the two: your requests and wording, the session's status and reply. A review submit counts
against your upload limits like any attachment (one or two files), and a file keeps at most 20
versions, so a long-running review of one file may need old `.review.json` versions deleted from
the Files panel.

On the session's side the commands are `blerg-runner review list [<file>]` and
`blerg-runner review reply <id> done|declined "<line>"`, which takes several answers in one call
(the three arguments repeated, or `--file` with one `<id> done|declined <line>` per line) and then
publishes the review file once; `blerg-runner review --help` explains the loop. The session's
`.review.json` versions are review state, not documents: the chat shows no file card for them.
It folds each round of answers into one line, such as "11 requests answered · report.md", that
opens the review. They stay in the Files panel. Design: [`docs/design/feedback-tools.md`](design/feedback-tools.md).

## Asking for an approval

When the agent needs you to approve something it published (a policy, a migration, a diff before
it merges), rather than only show it, it asks explicitly:

```bash
blerg-runner publish SECURITY.md --request-review "OK to publish this policy?"
blerg-runner diff main..HEAD --request-review "Merge this?"
blerg-runner review request SECURITY.md --ask "OK to publish this policy?" [--wait]
```

- **Pinned to one version.** A name means its newest version, an id that exact file. Publishing a
  newer version of the same file **supersedes** a pending request (its card says "Superseded by
  v3"), and the agent asks again; deleting the file withdraws it. The agent can withdraw a request
  itself (`blerg-runner review withdraw <id>`). Deciding a request whose file has a newer version
  supersedes it instead.
- **Where you see it.** Live → Needs you has a row "Review: SECURITY.md" with the ask under it, and
  the chat a card "Review requested: SECURITY.md · v2" with **Open**. Both open the file at that
  version (a diff in the diff viewer). A request also notifies your phone, like a question.
- **Deciding.** The viewer shows a bar with the ask, **Approve** and **Request changes** (with an
  optional note). Comments you already submitted on that version (Review, then Submit) go with the
  decision. Once decided, the bar and the card say how. The bar is only there for an explicit
  request.
- **Who decides.** Only the session's owner, signed in in the browser. Anyone else who can see the
  session sees that it waits for the owner; the session itself, the daemon, board tokens and agent
  tokens are refused.
- **What the agent gets.** `Review of SECURITY.md v2 (request <id>): APPROVED`, or `…: CHANGES
  REQUESTED: <your note>`, followed by your own open comments on that version (the ones you submitted;
  nobody else's), one line each. It arrives as a message, or, when the agent waits with `--wait`, as
  the command's output (exit code 0 approved, 2 changes requested, 3 still waiting). Chat text can be
  typed by anyone who can write in the session, so the agent is told to treat an approval as real
  only when `blerg-runner review status <id>` (or `review wait <id>`) exits 0. Claude, Codex and
  native sessions also see a relayed decision marked with the session's private code (Hermes and
  OpenClaw do not). The marker guards against accidents and against text retyped from a
  transcript; it is not a security boundary against someone who tells the agent to transform and
  print a transcript or the daemon's `marker.key`. The review request's state on the server stays
  the authority.
- **API.** `POST /api/sessions/{id}/review-requests` (the session's token), `GET
  /api/sessions/{id}/review-requests` and `GET /api/review-requests/{id}[?wait=N]` (the session, or
  a person who can see it), `POST /api/review-requests/{id}/decide` `{"decision": "approve" |
  "request_changes", "note": "..."}` (the owner's browser sign-in) and `POST
  /api/review-requests/{id}/withdraw` (the session). The chat events are `review_requested` and
  `review_decided`; the table is `review_requests` (migration `069`).

## Where the files are

On the runner's data volume, under `BLERG_RUNNER_DATA_DIR` (`/data` in the desktop stack and the
cluster deployment), at `artifacts/<session id>/<artifact id>/<file name>`; the database holds
the index (`session_artifacts`, migration `031`). The volume must be kept for the files to survive
a restart of the runner: the desktop stack mounts the `blerg-desktop-runner-data` volume there,
the Kubernetes install the PersistentVolumeClaim `blerg-runner-data`
([`install/k8s/README.md`](../install/k8s/README.md#runner-data-volume)). A file whose row
survived but whose bytes did not (an install from before that claim existed, a lost volume) stays
in the list marked "no longer available": View and Download are hidden, its raw and download
routes answer `410 Gone` with `{"error":"file no longer available"}`, and it can still be
deleted.

A session's files are deleted with the session. The runner also removes, at start and then every
hour, the directory of any session that no longer exists. You can delete a single file any time
from the Files panel. Back them up with the rest of the runner data
([`backup-and-restore.md`](backup-and-restore.md)).

## In a cluster pod

The pod image installs the same `blerg-runner` command on its `PATH` (the pod's own entry point is
`blerg-runner-pod`), and each cluster session is started with its own `BLERG_RUNNER_SESSION_TOKEN`,
as a desktop session is. Sessions started before the image and server carrying this feature have
no token and cannot publish; start a new one. The token lasts 24 hours.

### Raw source in file viewers

Textual previews offer **Raw** in the viewer header and **Rendered** to return.
This includes Markdown, Mermaid, Vega-Lite, HTML, CSV, JSON and diffs, for both
published files and workspace files. Each open tab retains its choice while
switching tabs. Binary media keep their existing viewer controls.
Source highlighting follows **Code colors**, including Go modules/checksums,
Dockerfile names (`Dockerfile`, `Dockerfile.*`, `*.dockerfile`) and Makefile
names (`Makefile`, `GNUmakefile`, `*.mk`). Unknown languages remain plain text;
large sources keep the existing bounded highlighting behavior.

### Private attachment storage

The daemon sets `BLERG_RUNNER_ATTACHMENTS_DIR` for each session. On the host it is
`~/.blerg-runner-daemon/sessions/<session-id>/attachments/` (or under the configured
daemon state directory). Sandbox and cluster sessions use their private home state
under `/home/agent/`, outside `/workspace`. Isolated cluster pods mount a separate
ephemeral volume there, initialized for the agent with mode 0700; sandbox pods use
a private writable temporary mount. The attachment folder is mode 0700; the CLI prints
absolute file paths. Review replies are stored in its `.reviews/` child. For in-flight
reviews, the CLI still reads the old repository's `attachments/.reviews/` as a fallback.

`fetch --out DIR` remains available. A destination inside a Git worktree is added to
that repository's `.git/info/exclude`, idempotently, and the CLI reports the exclude.
It never edits a tracked `.gitignore`. Existing repository `attachments/` folders are
never moved or deleted automatically.

Ending or discarding a session removes its private downloads and review state. On
startup/reconnect and every minute, the host daemon checks the next batch of at
most 128 session directories, and asks the server about attachment folders at
least 24 hours old. Only ended, discarded or
unknown sessions are eligible; locally running sessions and paths through symlinks
are retained. The scan advances past retained folders and starts another pass at
the end, retrying folders skipped earlier. A daemon restart starts a new pass.
A missing or failed server reply retains the files. Cleanup stays
inside the daemon's sessions root and never targets an explicit `--out` folder.
A resumed session can fetch the attachments again.
