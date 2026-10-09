# Feedback tools: review and draw-over

*2026-10-06. `@blerglab/chat`, the runner, the `blerg-runner` CLI.*

## What this is

Two ways for a person to give a session precise feedback on a file it published, from the chat,
without typing coordinates or quoting by hand:

- **Review** of a markdown or PDF file: the document on the right, laid out as it reads (rendered
  markdown, or the PDF's real pages), the markdown source on the left for a markdown file; select
  a passage and press **N** to request a change; edit the source directly; **Submit** sends it all
  to the session in one message. The session's replies (done, declined, with a line each) show
  beside the requests on the next open.
- **Draw-over** of an image (one the session published, or one the person attached): pen,
  arrow, box, numbered pin with a note; **Send** attaches the marked-up image and the pins'
  notes to a message.

Both come from the same place: `tools/review.py` and the `md-review` skill in the LiteQuest
repository, which run as a local page over files on disk and an inbox the session polls. Here the
file is a published artifact, the inbox is the chat, and the session answers through a tool it
already has.

## One model for both

```ts
// A request anchored to a place in a file.
interface Anchor {
  kind: 'text' | 'region'
  // text: the selected passage, where it sits, and which occurrence on the page or in the file
  quote?: string
  heading?: string
  page?: number          // PDF only, 1-based
  occurrence?: number    // 1-based, among equal quotes on the page / in the file
  line?: number          // best-effort line in the markdown source
  // region: an image area, in fractions of the image's width and height
  rect?: { x: number; y: number; w: number; h: number }
  pin?: number           // the number drawn on the image
}

interface ReviewRequest {
  id: string             // short random id
  anchor: Anchor
  text: string           // what the person asks for
  status: 'open' | 'done' | 'declined'
  reply?: string         // the session's one line
  createdAt: string
}

// `<name>.review.json`: everything about one file's review, published beside it.
interface ReviewFile {
  version: 1
  file: { name: string; artifactId: string; artifactVersion?: number }
  requests: ReviewRequest[]
  // In-browser edits of a markdown source: the edited copy's artifact id, and the diff.
  edit?: { artifactId: string; diff: string }
  submittedAt?: string
}
```

The review file is the persisted state, uploaded by the person (origin `user`) under the name
`<file>.review.json`, a new version on every submit. The session answers by publishing the same
name (origin `agent`) with `status` and `reply` filled in; the viewer shows the newest of the two
origins per request id (the session's reply wins for status and reply, the person's text for
everything else). Nothing else persists: the chat message carries the human-readable form.

## The message the session gets

Submit sends one message through the ordinary send path, with files attached the ordinary way:

```
Review of report.md (v3): 2 requests, 1 edit.

1. Under "Results", page 2: "the mean rose by 12%" — this is the median, not the mean
2. Under "Method": "we sampled weekly" — say how many weeks

Edited copy attached as report.md (your copy, edited directly); the diff:
```diff
@@ -14,7 +14,7 @@
-We sampled weekly.
+We sampled weekly for six weeks.
```

Apply the diff first (it is the author's own wording), then each request, in the file this was
published from. Reply with `blerg-runner review reply <id> done|declined "<one line>"` per request,
and publish the file again.
```

A draw-over sends:

```
Marked up screenshot-3.png: 3 pins.

1. (top left) the title is clipped
2. (centre) this button should be primary
3. (bottom right) remove the footer here

Attached files (fetch them with: blerg-runner fetch --all): screenshot-3.annotated.png (140 KB)
```

The pin numbers are drawn on the image. The composed text is what `model/feedback.ts` produces;
the attachment note is the existing one.

## The session's side

`blerg-runner review` subcommands, beside `publish`:

- `blerg-runner review list [<file>]`: the open requests of a file (or every file), from the
  newest `<file>.review.json` of either origin.
- `blerg-runner review reply <id> done|declined "<line>"`: writes the status and reply into the
  session's copy of the review file and publishes it as `<file>.review.json`. Idempotent per id.
- `blerg-runner review --help` says all of the above and the loop: diff first, then requests,
  republish the file, reply per request.

The system prompt's line on visual work mentions it. No new runner route: the CLI uses `files`,
`fetch` and `publish`.

## The viewer

`ArtifactViewer` gains a **Review** button for markdown and PDF files and a **Mark up** button
for images. Each opens a mode over the same dialog:

**Review, markdown.** Two panes (one on a phone, with a Source / Page toggle). Left: a plain
textarea over the source (the file's bytes), with a line gutter; edits are kept in memory
and in `localStorage` under the artifact id until submitted. Right: the package's `Markdown`
rendering of the *edited* source, with a **Changes** toggle that renders a word-level diff
against the published text (deletions struck, insertions marked). Selecting text on the
right and pressing **N** (or the Request button) opens a one-line note box anchored to the
selection: the quote, the nearest heading above it, the occurrence of that quote in the
document, and a best-effort source line. Requests list in a margin on the right, newest first,
each with a highlight of its quote in the page (amber open, green done, grey declined); hovering
one lights the other. Submit: upload the edited source as a new version of the file (user
origin), upload `<file>.review.json`, send the message, clear the local edit.

**Review, PDF.** The right pane shows the PDF's pages rendered by pdf.js (`pdfjs-dist`, loaded
on demand with a dynamic import so the chat bundle does not grow) with its text layer, so a
selection is real text; a request records `page` and `occurrence` on that page. Highlights are
drawn by matching the quote in the page's text layer. No source pane (a PDF has no source
here); no edits; the rest is the markdown mode.

**Draw-over.** A canvas over the image at its natural size, scaled to fit. Tools: pen, arrow,
box, pin (click places the next number and opens a note box), undo, clear. Send: the image
with the marks flattened to a PNG (`<name>.annotated.png`) uploaded as the person's
attachment, the pins' notes in the message, done. The overlay is not kept; the flattened file is
the record.

## Where it lives

- `packages/chat/src/model/{anchor.ts, diff.ts, feedback.ts, review.ts}`: pure; tests.
  - `anchor.ts`: `anchorSelection(root, selection, opts) → Anchor` (quote, heading, occurrence,
    line from a source map the markdown pane keeps), `findQuote(root, anchor) → Range | null`.
  - `diff.ts`: `unifiedDiff(a, b, name) → string`, `wordDiff(a, b) → Array<{kind, text}>`
    (own implementation, line LCS then word LCS within changed runs; no dependency).
  - `feedback.ts`: `composeReviewMessage(review, file) → string`,
    `composeMarkupMessage(file, pins) → string`.
  - `review.ts`: the types above, `mergeReviews(user, agent) → ReviewFile`,
    `parseReviewFile(json) → ReviewFile | null` (strict).
- `packages/chat/src/components/review/{ReviewMode.tsx, SourcePane.tsx, MarkdownPage.tsx,
  PdfPages.tsx, RequestList.tsx, review.css}` and `components/markup/{MarkupMode.tsx,
  markup.css}`; `ArtifactViewer` mounts them.
- `ChatContext` gains `submitFeedback(text, files: File[]) → Promise<boolean>` (ChatView
  implements it: upload each file through `transport.files.upload`, then `send` with the
  attachment note) and `reviewFor(name) → Promise<ReviewFile | null>` (the newest review file of
  either origin, through `files.list` + `files.raw`).
- `runner/.claude/skills/session-messaging/blerg-runner`: the `review` subcommands and tests.
- `runner/internal/daemon/agentprompt.go`: one line. `docs/artifacts.md`: a "Review and
  mark-up" section. `docs/talking-to-an-agent.md`: a paragraph. CHANGELOG.
- `pdfjs-dist` joins the package's dependencies (the one addition to the no-new-dependency rule,
  for the one job nothing else does); the worker is bundled through Vite's `?url` import.

## Not in this round

- Replies pushed live into an open viewer (the next open shows them).
- Review of HTML or CSV files.
- Keeping draw-over marks editable after sending.
