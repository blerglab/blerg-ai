# Files from a session (artifacts)

A Claude Code session can hand you a **file**: a report, a chart, an export, a generated
document. The agent runs one command, and the file shows up as a card in the session's chat and in
a **Files** panel, where you can download it or, for most types, read it in the app.

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
| Markdown (`.md`, `.markdown`) | Formatted text. Raw HTML in it is shown as text, never run. |
| Plain text, logs and source code (`.txt .log .py .js .ts .tsx .go .rs .java .c .h .cpp .sh .yaml .yml .toml .ini .xml .sql .css .rb .php` and similar, and a file with an unknown extension whose bytes are text) | Monospace text. The first 200 KB, with **Show all**; over 2 MiB it is download only. |
| JSON (`.json`) | Pretty-printed; the raw text if it does not parse. Same size rules as text. |
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
- **In the Files panel.** One row per file, showing the latest version with a `v3` badge. Under it,
  **Earlier versions (2)** opens the older ones with their own time, Download, View and Delete.
  Deleting acts on that one version; deleting the latest makes the previous one the latest. The
  header and the Files button count files, not versions. The same grouping applies to "Uploaded by
  you".
- **In the viewer.** The title reads `report.md · v2 of 3`, and an older version says so, with a
  **View latest** button. A `?artifact=<id>` link opens any version.
- **Downloads.** The latest version is saved under the plain name, an older one as `report-v2.md`
  (a name without an extension just gets `-v2`).
- **For the agent.** `blerg-runner fetch --all` fetches the **latest** version of each attached
  name; `fetch --all-versions` fetches every version, saved as `name-v1.ext`, `name-v2.ext`;
  `fetch --list` shows the versions. `publish --help` and the instructions every Claude Code
  session reads carry the "publish again under the same name" rule.
- **API.** An artifact (list entry, upload reply, the `artifact` event) has `version`; the list also
  has `latest_version`, and the upload reply has `latest_version` (the version just made) and `previous` (the version it follows, or `null`).

## Limits

- 25 MiB per file.
- 50 files per session, every version counting as a file. A 51st is refused. The agent frees a slot
  itself with `blerg-runner unpublish <name>` (`blerg-runner files` lists what it published); a file
  you attached is yours to delete, from the Files panel.
  Together that is up to about 1.25 GiB per session, so on a shared machine or cluster node give the
  runner's data a disk quota (see [`SECURITY.md`](../SECURITY.md)).
- 20 versions per file name (per side). A 21st is refused with "This file already has 20 versions;
  delete an old one first."
- A file name is cut to its base name (no folders), 120 bytes at most, with control characters
  removed.
- A directory is zipped by the command (skipping `.git`, `node_modules` and symlinks, at most
  200 MiB of input) and the zip must fit in 25 MiB.
- Files can only be published while the session is running.

## Who can see a file

The same people who can see the session. A session anyone on the install can see shows its files
to everyone; a **private** session's files are visible to its owner alone, and everyone else gets
the same "not found" an unknown file gets. Only a signed-in person can open, download or delete a
file: an agent token cannot. Publishing uses the session's own messaging token, and that token
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

The other direction works too: you can attach files (for example two PDFs to analyse) to a message
from the chat box, and the session's agent fetches them into its working folder.

- **Attach** with the paperclip next to the message box, by dragging files onto the chat, or by
  pasting them. Each file uploads immediately and shows as a chip under the box with its size and a
  remove button; removing a chip deletes the upload. Send is disabled while an upload is running.
- **Limits:** 25 MiB per file (larger ones are refused in the browser), 10 files per message, and
  per session at most 100 uploaded files and 200 MiB in total. These are separate from the agent's
  own 50 files; uploading is refused once the session has ended. Name handling and the content type
  work exactly as for an agent's files (the server decides the type; folders in names are dropped).
- **What the agent sees:** your text, then a blank line and one line naming the files, for example
  `Attached files (fetch them with: blerg-runner fetch --all): a.pdf (1.2 MB), b.pdf (800 KB)`. If
  you attach files without typing anything, the message starts with "Please take a look at the
  attached files." The chat shows the files as chips on your message. It works while the session is
  running too: the message is queued like any other.
- **How the agent gets them:** `blerg-runner fetch --all` downloads every attachment into
  `./attachments/` (or `--out DIR`), never overwriting an existing file; `fetch --list` only lists
  them. It uses the session's own token, which only works for that session, and it only serves
  files you uploaded, never the agent's published ones.
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

A published file can be answered precisely, without quoting by hand. Open it in the viewer:

- **Review** (markdown and PDF). The document is shown as it reads: rendered markdown, or the
  PDF's real pages. Select a passage and press **N** (or **Request change**) to ask for a change
  there; the request is anchored to the quote, the heading above it, and for a PDF the page. For a
  markdown file the source is beside the page and can be edited directly; **Changes** marks your
  edits on the page. Requests list in the margin and their quotes are highlighted. **Submit** sends
  everything to the session in one message: the requests, each with an id, and your edits as a
  diff with the edited copy attached.
- **Mark up** (images, the session's or your own). Pen, arrow, box, and numbered pins with a note
  each. **Send** attaches the marked-up image and puts the pins' notes in the message.

The session makes the changes, publishes the file again, and answers each request as done or
declined with a line; the answers show beside your requests the next time you open the review.

What is stored: `<file>.review.json`, uploaded beside the file on every submit (the requests,
their anchors, and the diff), and the session's copy of the same name with its replies. The viewer
merges the two: your requests and wording, the session's status and reply. A review submit counts
against your upload limits like any attachment (one or two files), and a file keeps at most 20
versions, so a long-running review of one file may need old `.review.json` versions deleted from
the Files panel.

On the session's side the commands are `blerg-runner review list [<file>]` and
`blerg-runner review reply <id> done|declined "<line>"`; `blerg-runner review --help` explains the
loop. Design: [`docs/design/feedback-tools.md`](design/feedback-tools.md).

## Where the files are

On the runner's data volume, under `BLERG_RUNNER_DATA_DIR` (`/data` in the desktop stack and the
cluster deployment), at `artifacts/<session id>/<artifact id>/<file name>`; the database holds
the index (`session_artifacts`, migration `031`). The volume must be kept for the files to survive
a restart of the runner: the desktop stack mounts the `blerg-desktop-runner-data` volume there.
**The Kubernetes manifests mount an `emptyDir`**, which is emptied whenever the runner pod
restarts. The list would still show the files, but downloading one would say it is no longer
available. Swap the volume for a PersistentVolumeClaim in `runner-deployment.yaml` to keep them.

A session's files are deleted with the session. The runner also removes, at start and then every
hour, the directory of any session that no longer exists. You can delete a single file any time
from the Files panel. Back them up with the rest of the runner data
([`backup-and-restore.md`](backup-and-restore.md)).

## In a cluster pod

The pod image installs the same `blerg-runner` command on its `PATH` (the pod's own entry point is
`blerg-runner-pod`), and each cluster session is started with its own `BLERG_RUNNER_SESSION_TOKEN`,
as a desktop session is. Sessions started before the image and server carrying this feature have
no token and cannot publish; start a new one. The token lasts 24 hours.
