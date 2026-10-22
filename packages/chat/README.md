# @blerglab/chat

The chat surface for apps built on Blerg sessions: transcript, composer, files, cards, highlighted code, diagrams, charts, diffs and a
token theme, over a pluggable transport. The runner's own web app renders it; your app can too,
against your own backend, with the runner's agent token never leaving your server.

This document is for the person (or the session) wiring the package into an app. It ends with
an unattended procedure an agent can follow end to end.

What you add to the app:

| Piece | Where | What it does |
| --- | --- | --- |
| `@blerglab/chat` | the app's frontend | `ChatView` and friends, installed from the runner |
| `chatproxy.go` | the app's backend | eleven routes under a prefix, forwarding one session's traffic to the runner |
| `Authorize` | the app's backend | your rule for "may this signed-in person see this Blerg session" |
| a theme file | the app's frontend | the `--chat-*` tokens mapped to your palette |

## Install

The version moves whenever the package's contents do: a tarball is served under its version's
name with a year-long immutable cache, so one version is one set of bytes, for good. To take a
newer chat, read the manifest again and install the version it names.

The runner tells you where the package is. Its entry in the `/agents` manifest (the aggregate at
your core, or the runner's own `GET <runner>/agents`) carries `ui.chat_package`:

```json
{
  "name": "blerg-runner",
  "base_url": "https://runner.example.test",
  "ui": {
    "chat_package": {
      "name": "@blerglab/chat",
      "version": "0.23.0",
      "url": "https://runner.example.test/packages/blerglab-chat-0.23.0.tgz",
      "sha512": "…",
      "docs_url": "https://runner.example.test/packages/@blerglab/chat/README.md"
    }
  }
}
```

Install from `url` and pin it with the `sha512` as the package's `integrity`, so the lockfile
proves the bytes are the runner's:

```sh
MANIFEST=$(curl -fsS https://core.example.test/agents)
URL=$(echo "$MANIFEST"    | jq -r '.components[] | select(.name=="blerg-runner") | .ui.chat_package.url')
SHA512=$(echo "$MANIFEST" | jq -r '.components[] | select(.name=="blerg-runner") | .ui.chat_package.sha512')

npm i "$URL"
```

Then open `package-lock.json`, find the `@blerglab/chat` entry and check its `integrity` is
`sha512-<base64>` of the manifest's value. npm stores the digest as base64; if the manifest
gives it as hex (128 hex characters), convert first: `echo "$SHA512" | xxd -r -p | base64 -w0`.
If the manifest value is already base64 (88 characters ending in `=`), compare as is. If npm
recorded a different integrity, the tarball did not match the manifest: stop and tell the
runner's operator.

The tarball route is immutable (`Cache-Control: public, max-age=31536000, immutable`) and
unauthenticated, as `/agents` is; a new version is a new file name. The package needs
`react` and `react-dom` 19 as peers and brings `react-markdown`, `remark-gfm`, `zustand` and
`lowlight` (the code highlighter), Mermaid and Vega-Lite/Vega with it. Renderers are loaded on
demand. Closed `mermaid` and `vega-lite` fences render in messages and Markdown views; charts
accept inline data only. `VisualBlock` exposes the same source/copy/zoom surface independently
of a session or transport. Visuals resolve the surrounding `--chat-*` tokens for colors and
fonts, remain transparent, fit the message width and redraw when those tokens change.
Explicit chart mark and encoding colors override the theme defaults; uploaded chart
configuration and external resources remain disabled.

## Mount the proxy

The browser never holds the runner's agent token. It talks to your backend over the **proxy
contract**, your backend authorizes the person its own way and forwards to the runner with the
token. The package ships the reference proxy as a single standard-library Go file:

```
node_modules/@blerglab/chat/proxy/go/chatproxy.go
```

It is meant to be **copied** into the app (apps cannot import the private Blerg module). Three
steps:

1. **Copy** `chatproxy.go` into your server's package (rename the `package` line to yours, or
   keep it as its own package `chatproxy` in a subdirectory).

2. **Implement `RunnerClient`** over the runner client the app already has, the one that adds
   the agent token and the runner's base URL:

   ```go
   type RunnerClient interface {
       Do(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error)
   }
   ```

   `path` already includes the query string. The implementation must build the request with
   `ctx` (`http.NewRequestWithContext`) so a browser that goes away cancels the runner request
   behind it. An upload passes the browser's declared size as a `Content-Length` header: set it
   as the request's `ContentLength` (`req.ContentLength = chatproxy.ContentLength(v)`), because
   net/http never sends that header from `Header`. Without it the body goes chunked and the
   runner reads a file up to its limit before it can refuse it. If the app has no client yet, the file includes `HTTPRunnerClient{BaseURL,
   Token}` which does exactly this.

3. **Write `Authorize`**: for the app's own signed-in person (your cookie, your session store),
   may they see this Blerg session? Your rule is your own; the usual one is "the session belongs
   to a job of theirs".

   ```go
   type Authorize func(r *http.Request, sessionID string) (ok bool, err error)
   ```

   `(false, nil)` is a 403 and the runner is never contacted; an error is a 500 with a generic
   body.

Then the four lines:

```go
mux := http.NewServeMux()
runner := &HTTPRunnerClient{BaseURL: cfg.RunnerURL, Token: cfg.AgentToken} // or your own client
chatproxy.Mount(mux, "/blerg", runner, authorizeSession)
http.ListenAndServe(":8080", mux)
```

What `Mount` registers, and where each goes:

```
GET    /blerg/sessions/{id}                       -> GET    /api/runner/sessions/{id}
GET    /blerg/sessions/{id}/events/live?...       -> GET    /api/runner/sessions/{id}/events/live   (SSE, streamed)
GET    /blerg/sessions/{id}/events?...            -> GET    /api/runner/sessions/{id}/events
POST   /blerg/sessions/{id}/messages {text}       -> POST   /api/runner/sessions/{id}/message
POST   /blerg/sessions/{id}/stop                  -> POST   /api/runner/sessions/{id}/stop
POST   /blerg/sessions/{id}/model                 -> POST   /api/runner/sessions/{id}/model
GET    /blerg/sessions/{id}/artifacts             -> GET    /api/runner/sessions/{id}/artifacts
GET    /blerg/sessions/{id}/artifacts/{aid}/raw   -> GET    .../artifacts/{aid}/raw
GET    /blerg/sessions/{id}/artifacts/{aid}/download -> GET .../artifacts/{aid}/download
DELETE /blerg/sessions/{id}/artifacts/{aid}       -> DELETE .../artifacts/{aid}
POST   /blerg/sessions/{id}/uploads               -> POST   /api/runner/sessions/{id}/uploads  (raw bytes, X-Artifact-Name)
GET    /blerg/preferences                         -> GET    /api/runner/preferences?account=...   (0.14.0, optional)
```

The last route reads the signed-in person's display preferences (their code colors) so the chat
shows code in them. `Options.Account` names the person (the blerg-core account id); `Authorize` runs
with an empty session id; with no `Account` the route answers `{}` and the runner is not asked. It
is read-only: the person changes their colors in the runner.

Every route calls `Authorize` first. Only the runner's status code, `Content-Type`,
`Content-Disposition` and `Cache-Control` come back to the browser; `Set-Cookie` and
`Authorization` never do, in either direction, and the proxy logs nothing. The live stream is
copied as it arrives and flushed per frame; put it behind a reverse proxy that does not buffer
(`X-Accel-Buffering: no` is set for you).

The runner-side routes answer only for sessions the token's account started, with 404
otherwise, so a proxy cannot widen the token even if `Authorize` is wrong. Keep `Authorize`
right anyway: it is what keeps one of your people out of another's session.

`MountWith` takes the same pieces and two optional ones, for an app that needs them:

```go
chatproxy.MountWith(mux, "/api/chat", client, chatproxy.Options{
    Authorize: authorize,
    // The app names sessions its own way (a row id): map it to the runner's id.
    // Return chatproxy.ErrNotFound for an id the app does not know (404).
    Resolve: func(r *http.Request, id string) (string, error) { return runnerIDOf(r.Context(), id) },
    // What the runner records as the source of a proxied message, so the app can
    // tell a person's message from one its own automation sent another way.
    MessageSource: "human",
})
```

Verify the copy builds and behaves: the package also ships `chatproxy_test.go` next to it;
with both files in a directory of their own, `go test ./...` runs the contract tests against a
stub runner.

## Render

```tsx
import { ChatView, createProxyTransport, defineCard } from '@blerglab/chat'
import '@blerglab/chat/tokens.css'   // the token contract (defaults)
import './chat-theme.css'            // your palette, see Theme

const transport = createProxyTransport({ baseUrl: '/blerg' })

export function JobChat({ sessionId }: { sessionId: string }) {
  return <ChatView session={sessionId} transport={transport} />
}
```

`ChatView` is the whole surface: transcript (replay, then live, with older pages loaded as the
person scrolls up), composer, the working and waiting indicators, the disconnected banner, and
the Files panel. It needs nothing from the page but a height. A `topic` event (a piece of work
the agent marked with `blerg-runner topic`) draws as a thin divider (`▸ <title>`, `✓ Done: <outcome>`);
`topicDividerText(payload)` gives the words (0.10.0). Optional props:

- `theme`: a `ThemeTokens` object instead of a CSS file.
- `cards`: a registry from `createCardRegistry([...])`; see Cards.
- `slots`: `header`, `composerActions`, `messageFooter` and, since 0.15.0, `aboveComposer` (a node between the transcript and the composer) for your own chrome.
- `commitUrl(sha)` (0.15.0): unused since 0.19.0 (commit chips never link out of Blerg), still accepted. 0.15.0's `onViewCommitDiff(sha)` is gone in 0.16.0: an item's "View diff" opens the diff the agent published of its commits, in the chat's own viewer.
- `linkedCommit` (0.19.0): with `linkedArtifact` a diff, the commit it opens at (its section scrolled to and marked).
- `maxFileBytes`: the largest file a person may attach, in bytes; a larger one is refused in the
  browser, with the limit named, before it is sent (0.11.0). Without it the chat uses the
  session's `limits.max_file_bytes`, which the runner (0.2.1 on) puts in its session status and
  the proxy transport reads, and else `MAX_ATTACHMENT_BYTES` (25 MiB, an older runner's limit).

A refused upload or download shows the server's own sentence (the runner says which step failed
and what to do; a proxy's plain-text answer is shown as it is) and, when the runner gave one, its
error id in small text after it: the runner's log line for the failure carries the same id. The
transport throws a `FileRequestError` (`status`, `errorId`); `fileErrorText(e, fallback)` gives
the words and the id to show.

`createProxyTransport({ baseUrl })` speaks the proxy contract above. Its `files` are present
(so the Files button and the attach control show) because the proxy forwards the artifact and
upload routes. Model/effort controls forward to the session’s existing engine; an unsupported
control is reported as an error. `send` forwards `SendOptions.clientId` as `client_id`, allowing
the runner to persist and deduplicate conversational messages until native consumption.
The transport
subscribes with `tail=true` to open a long transcript at its end and resubscribes from the last
sequence number after a drop.

Published artifact links in messages open the existing file viewer on an ordinary click when
that file is known, viewable, and belongs to the current session. Modified clicks retain normal
navigation. The direct Blerg transport knows its runner URL. An embedded proxy needs an explicit
mapping to the public runner URL, because its local session ID may name a different database row:

```ts
createProxyTransport({
  baseUrl: '/api/chat',
  publishedSessionUrl: appSessionId => {
    const runnerId = sessions.get(appSessionId)?.external_session_id
    return runnerId ? `https://runner.example.com/sessions/${encodeURIComponent(runnerId)}` : null
  },
})
```

The canonical URL must contain no query, fragment, or user information. Without this optional
mapping, links keep their existing behavior; the component never guesses the runner from an
embedded page's origin. Only the exact declared origin and session path with a single `artifact`
query parameter are recognized. External, other-session, unknown-file and download-only links
keep normal navigation; unrelated relative links remain inert as before.

An app whose own sign-in is a bearer token rather than a cookie passes `headers` (asked for on
every request, the live stream's reconnects included) and `onUnauthorized`:

```ts
createProxyTransport({
  baseUrl: '/api/chat',
  headers: async () => ({ Authorization: `Bearer ${await freshToken()}` }),
  onUnauthorized: () => signIn(),
})
```

`ChatView` takes a few props for the host's own rules. Optional `onManageChildApproval` opens
the host's current parent-session permissions from an allowed delegation card; it only navigates
and never changes an approval itself. A `proposal_request` event draws a proposal card that reads
and decides the proposal through the transport's optional `proposal` and `decideProposal` (without
them the card says it waits for the owner); `onEditProposal(id)` opens the host's draft editor for a
board card, `onOpenBoardCard(card)` opens a created card in the host's board, `onManagePermissions`
opens the host's session permissions, and bumping `proposalRevision` has the cards read their
proposal again (after the host's editor approved one). A proposal of kind `board_create` (0.74.0: a
new board the session asks for) is approved by its owner only: its card has no Edit or Allow for this
session, sends the reviewed arguments with the approval, and once decided says the board was
created and the access the session got (`ProposalDecidedPayload.board`, `ProposalDetail.board`,
`CreatedBoard`, `readCreatedBoard`). `describeUserMessage` says how a user
message is drawn: return `{ author: 'the board' }` for one the app's automation sent rather than
the person, `{ brief: true }` to fold the long prompt a session was started with into one line
that opens, or nothing to leave it as the person's. Its third argument says whether the message is the
session's first (`{ first }`: the oldest user message, once nothing older is left to fetch), so
only the brief folds. Text nobody typed (the daemon's notes, a slash command's output) never
reaches it: the chat draws that as a system card. `readOnly` shows the transcript and files
with no composer. `placeholder` replaces the composer's prompt. A host that keeps the chat's
settings and files in a place of its own (the runner's session panel) passes `compact` (whether runs
of tool calls fold into one row: the toolbar then has no "Compact tool calls" checkbox, and the host
keeps the choice; `loadCompact` and `saveCompact` read and write the chat's own key) and
`filesButton={false}` (no Files button or dialog: draw the list from `useLiveState`'s `files` with
`FilesList`, below; `openFiles` is then not reported, from 0.10.10) (0.10.9).

What is live in a session beside its chat is the host's to place: the runner draws it as a side
panel. The working line names background tasks either way ("Waiting on 2 background tasks"); the
chat itself draws no list of them (`backgroundTasks` takes `'none'` only). Two hooks read the
session as the transcript store holds it:

```tsx
const { subagents, openAsks, backgroundRunning, settled, answer, openFiles, filesKey, files } = useLiveState(sessionId)
const { running, recent, counts, now, dismiss } = useBackgroundTasks(sessionId)
```

`useLiveState` reads what the transcript has loaded, so on a long session its lists fill in as the
history loads and miss what started before it; a host whose runner server answers
`GET /api/sessions/{id}/live` (the whole log, server-side) should read that and keep this hook as the
fallback for an older server, as the runner's side panel does (0.10.2). It lists the top-level subagents still running (`SubagentLive`: title, type, model,
tokens, what it is doing, when it started, whether it runs in the background) and the agent's open
questions (`AskLive`: the question, its message id, when it was asked), with the same rules as the
subagent and ask cards (it reads the transcript's own timeline, built once for both), and
`heldUntil`, the server time of the newest event held. `settled` is whether the session's initial history has
finished loading (the lists are read from what is held either way). It re-evaluates by the minute, not
by the second: a subagent or background task with no end whose newest event is over
`STALE_RUNNING_MS` (12 hours) old no longer counts as running, and its transcript card reads
"no end recorded". `answer(text, messageId)` answers an ask through the
session's `ChatView` (draw it with `AskReply`, the ask card's own reply box), and `openFiles()` opens
its Files panel (absent when the chat has `filesButton={false}`); both come from the `ChatView` showing the session, so they are absent until one has
and gone once it unmounts. `filesKey` is the Files list's ids as the chat last read them (`''` until a
`ChatView` reports), so a side view can reread what it shows when a file is deleted (0.10.0). Give `AskReply` `draft` and `onDraft` to keep what was typed when its box
goes (a drawer that closes). `files` is the Files list as the chat holds it (`items`, `status`,
`refresh()`), the transport's file operations (`ops`) and `attach()`, which opens the message box's
file picker (absent for a read-only chat); `FilesList` draws it, the Files panel without its dialog:
`<FilesList sessionId={id} files={files.ops} items={files.items} status={files.status} onView={…}
onChanged={files.refresh} onAttach={files.attach} />` (0.10.9).
`useBackgroundTasks` keeps a finished task for ten seconds and a failure until `dismiss`; render it
under a key of the session. `KindIcon`, `StatusGlyph`, `elapsedOf` and `titleOf` draw a task.
`scrollToCall(callId)` scrolls the page's transcript to the call that started a task or a subagent
(a subagent's card carries its key) and flashes it; `scrollToEvent(eventId)` does the same for an
event's own card: ask cards, and (0.10.0) person bubbles, briefs, artifact cards and topic dividers,
which carry `data-event-id`. Both return false when the row is not drawn yet (it is older than what the transcript
has loaded) (0.9.0).

An app that wants its own surface uses the hook `ChatView` is built on:

```ts
const { events, streaming, status, send, stop, artifacts } = useSession(transport, sessionId)
```

`useSession` takes a replayed page in one transcript update when the transport hands it over
whole (the optional `onEvents` handler); live events still come one by one through `onEvent`.
The runner's `createBlergTransport` does this, and also bounds how far it replays forward: a
session left and opened again a little behind keeps what it shows and catches up in a few
batches, while one still behind after `MAX_FORWARD_PAGES` (5) full pages, about 1000 events, is
dropped (the optional `onReset`) and loaded from its end as a reload does, the older part paged
in behind it. Both handlers are optional: a transport without them, `createProxyTransport`
among them, hands events over one by one as before.

A long transcript is drawn a window at a time. The transcript draws its newest 300 rows (a row
is a message, a card or a folded run of tool calls); scrolling near the top of what is drawn
reveals 300 more, and once everything held is drawn, fetches the next older page. Older pages
are not fetched up front: `useSession` asks for one page behind the tail (and one after a
reconnect or on a later visit), and otherwise only when `loadOlder()` is called. `loadOlder()`
fetches one page and does nothing while one is in flight; `loadingOlder` says one is, and
`hasOlder` that the server holds more. Reopening a session whose long transcript is already held
paints the switch at once (the skeleton) and draws the transcript on the next frame. A host that renders `Transcript` itself passes `sessionId`, `hasOlder` and `onLoadOlder`
for this, and `since` (the turn's start, or `null`) with no `now`: the working line keeps its own
clock (0.5.0).

## Theme

`tokens.css` is the contract; every component styles against these and nothing else. Override
them on `:root` (or on the element that wraps `ChatView`), with a `prefers-color-scheme` block
for dark mode; the components never branch on light or dark themselves.

| Token | Meaning |
| --- | --- |
| `--chat-bg` | page background behind the transcript |
| `--chat-surface` | bubbles, panels |
| `--chat-surface-2` | nested surfaces (code, tool groups) |
| `--chat-fg` | text |
| `--chat-muted` | secondary text |
| `--chat-muted-2` | tertiary text, placeholders |
| `--chat-accent` | the one accent (send button, links, focus) |
| `--chat-accent-fg` | text on the accent |
| `--chat-success` | finished, uploaded |
| `--chat-warning` | waiting on the person, queued |
| `--chat-info` | informational notes |
| `--chat-danger` | errors, stop, delete |
| `--chat-border` | hairlines |
| `--chat-radius` | corner radius |
| `--chat-font` | text face |
| `--chat-font-mono` | code face |
| `--chat-font-display` | section headings (condensed, uppercase) |
| `--chat-text-xs` | smallest text size |
| `--chat-text-sm` | small text size |
| `--chat-density` | spacing scale (1 = default) |
| `--chat-card-user-bg`, `--chat-card-assistant-bg`, … | per-kind card backgrounds |
| `--chat-code-keyword`, `-string`, `-number`, `-comment`, `-title`, `-type` | muted syntax colors in code blocks and previews (0.12.0) |
| `--chat-code-variable`, `-builtin`, `-operator` | variables and attributes, built-ins and meta, operators (0.14.0) |
| `--chat-code-fg`, `-bg`, `-linenum` | code text, code background and the viewer's line numbers (0.14.0) |
| `--chat-diff-add`, `--chat-diff-del` | added and removed lines of a `diff` block (0.12.0) |

A minimal `chat-theme.css`:

```css
:root {
  --chat-bg: #faf8f4;
  --chat-surface: #ffffff;
  --chat-surface-2: #f1ede6;
  --chat-fg: #1d1a16;
  --chat-muted: #5f5950;
  --chat-muted-2: #8e877c;
  --chat-accent: #8a4b1f;
  --chat-accent-fg: #ffffff;
  --chat-success: #2f6b3a;
  --chat-warning: #9a6a00;
  --chat-info: #2a5d8f;
  --chat-danger: #a3301f;
  --chat-border: #e3ddd3;
  --chat-radius: 10px;
  --chat-font: "Source Serif 4", Georgia, serif;
  --chat-font-mono: ui-monospace, Menlo, monospace;
}
@media (prefers-color-scheme: dark) {
  :root {
    --chat-bg: #16140f;
    --chat-surface: #1f1c16;
    --chat-surface-2: #2a261e;
    --chat-fg: #ece6da;
    --chat-muted: #b2a999;
    --chat-muted-2: #7e7668;
    --chat-border: #353026;
  }
}
```

`@blerglab/chat/blerg.css` is the runner's own mapping, if you want to start from how the
runner looks.

## Code color themes

Added in 0.14.0; no new props, nothing removed. Six code tokens were added (above), so a theme can
set what editors' themes set.

- **`codeThemes`** (and `CODE_THEMES`, the same in picker order) are ten named sets of the code
  tokens: Blerg (follows the app's palette, with its own light colors), GitHub Dark, GitHub Light,
  One Dark, Dracula, Monokai, Solarized Dark, Solarized Light, Nord and Gruvbox Dark. Pure data,
  no network.
- **A choice** is `{ dark, light, overrides: { dark, light } }`: a theme per mode and, on top, a
  `#rrggbb` color per token. `validateCodeColours` refuses anything else; `normaliseCodeColours`
  makes a stored value safe, piece by piece.
- **`applyCodeColours(el, choice, mode)`** (or `applyCodeTheme(el, themeId, mode, overrides)`)
  sets the custom properties on an element, the document root or a preview box;
  `currentCodeMode()` says which mode the page shows. The runner keeps the choice per account and
  offers it in Settings → Code colors; the board reads it through the proxy's
  `GET {prefix}/preferences` (see the proxy contract; the route is optional).

## Cards

A card is structured content the assistant writes inline, as a fenced block whose language is
`card:<kind>` and whose body is one JSON object:

````markdown
```card:character
{"name": "Mara Vance", "role": "antagonist", "arc": "…"}
```
````

Register a component per kind and pass the registry to `ChatView`:

```tsx
import { ChatView, defineCard, createCardRegistry } from '@blerglab/chat'

const CharacterCard = defineCard('character', ({ data }) => (
  <article className="character">
    <h3>{String(data.name)}</h3>
    <p>{String(data.role)}</p>
    <p>{String(data.arc)}</p>
  </article>
))

const cards = createCardRegistry([CharacterCard])

<ChatView session={id} transport={transport} cards={cards} />
```

Rules the renderer enforces, so you can rely on them:

- A registered kind renders in place of the block; an unregistered kind stays a code block (the
  person still sees the data).
- The body is parsed with strict `JSON.parse`; anything but an object, or a parse failure,
  renders the raw block with a one-line note.
- While the text is still streaming an unclosed fence is a code block; partial JSON is never
  parsed.
- Card data is model output: render fields as text (never as HTML, never `dangerouslySetInnerHTML`)
  and allow only `http:`/`https:` URLs in any link you draw from it, as the package's own
  markdown renderer does.

The package defines no kinds. Your agent learns the kinds and their shapes from your app's own
instructions (its `CLAUDE.md` or a skill): tell it the fence form and the fields each kind takes.

## Checklist, plan and pinned tickets

Added in 0.13.0; no new props, nothing removed.

- **Event kinds `checklist` and `ticket`.** The runner records them for `blerg-runner checklist`
  and `blerg-runner pin` / `unpin`. A checklist event holds the whole list after the change and the
  item it touched; consecutive changes with nothing drawn between them are one row (the latest),
  collapsed, that opens to the list. A ticket event holds the whole ticket and what it did (pin,
  update with the status and title it had, unpin); a pin or an update is a card, an unpin a line.
  Older chat builds draw nothing for either.
- **Tracker data is untrusted.** Every field renders as text; the description and the comments go
  through the package's Markdown without the app's cards (a `card:` fence stays a code block), and,
  as everywhere outside a document, raw HTML as literal text and no image loaded; the key links only
  to an `http:`/`https:` url. Any
  session token can claim any source, so the badge's tooltip says the agent pinned it.
- **`LiveState` gains `checklist`, `plan`, `tickets` and `ticket`** (all optional). `useLiveState`
  derives the checklist and the pins from the events it holds; the engine's plan comes only from
  the runner's `GET /api/sessions/{id}/live`. `TicketModal`, `TicketDetails`, `ChecklistItems` and
  the model helpers (`foldTicketEvent`, `readChecklist`, `planLabel`, …) are exported for a side
  panel.

## Tickets and commit chips

Changed in 0.19.0. New: ChatView's `linkedCommit`, `StatusChip`, `TicketSummary`, `statusTone`,
`priorityGlyph`, `ticketMeta`, `loadDiffSummary`, `ChecklistItems`'s `diffSummary`. Unused, still
accepted: ChatView's `commitUrl`, `ChecklistItems`'s `commitUrls`, `CommitRef.url`. Removed:
`CommitChips`'s per-sha `onViewDiff` (nothing passed it), replaced by `onOpen`.

- **Status chips by meaning.** `statusTone(status)` maps a tracker's word, case- and
  separator-insensitive, to `neutral` (to do, open, backlog, new, triage, and anything unknown),
  `active` (in progress, in review, started, doing, testing, QA…), `blocked` (blocked, on hold,
  waiting, paused), `done` (done, closed, resolved, fixed, merged, released, shipped…) or `canceled`
  (won't do, won't fix, canceled, duplicate, invalid, rejected). `StatusChip` draws it as a muted tint
  of `--chat-info`, `--chat-warning` or `--chat-success` with a dot; a canceled ticket's title is
  struck through. The chat's card, the modal and the runner's panel all use it.
- **A pin is one row (0.81.0).** "📌 Pinned #157 ◐ <title, cut to one line> · <time>", a system row
  as wide as a bubble at most: the key is the card chip with its hovercard, then the state glyph
  (`PIN_STATE_GLYPH` by `pinStateOfStatus`: ✓ done, ◐ in progress, ○ not started, shared with the
  runner's panel), the title, another tracker's source ("· JIRA"). Consecutive pins fold into one row
  ("📌 Pinned #157, #158"), as do consecutive unpins ("📌 Unpinned #127 (done)"); a refresh says what
  changed ("📌 Updated #157 ✓ … · status In progress → Done"). A click on the chip opens the host's
  ticket dialog (`ChatRefs.openTicket`: the runner's live Board card in its two-column layout); a host
  without one gets `TicketModal` on the pinned copy. Fields, criteria and comments are in the dialog
  only.
- **The modal is an issue view.** Header: badge, key, status, a large title and "Open in <Source>".
  Body: Description (the `md-doc` look), Acceptance criteria (a plain ☐ list) and Comments (an
  initials avatar, the name, a relative time), with **Details** (Type, Priority, Assignee, Reporter,
  Labels as chips, then the extra fields) beside them from 900 px and above them under it. Tab stays
  inside; the focus returns where it was.
- **Commit chips.** A checklist item shows up to three short shas, then "+N more". With the item's
  published diff a sha opens it in the viewer at that commit's section (`DiffView`'s and
  `ArtifactViewer`'s `commit`); without one it copies the sha ("Copied"). Nothing links to a forge.
  **± View diff** is a small button with the diff's +/− counts, read once (`loadDiffSummary`).

## Hovercards

New in 0.54.0: `Hover`, `HovercardProvider`, `HOVERCARD`, `hovercardsEnabled`,
`setHovercardsEnabled`, `useHovercardsEnabled`, `useCanHover`, `clearHovercardCache`, `VisualThumb`,
`CommitChips`'s `hover`.

- **One preview for every chip.** `<Hover target={…}>{chip}</Hover>` gives a chip a hovercard, as
  GitHub previews an issue or a commit: a card (`{kind: 'card', source, key, boardId?, title?,
  status?, ticket?, worker?}`: key, title, status, priority, type, tags, the assignee or the worker
  on it, the first lines of the description), a file (`{kind: 'file', sessionId, id, artifact?,
  reviewOpen?}`: an image as a thumbnail, markdown, text and code as the first lines, Mermaid and
  Vega-Lite drawn small by the viewer's own renderer and sandbox (`VisualThumb`), a diff as its
  files with +/− counts; then name, version, size, who published it and when, and an open review)
  or a commit (`{kind: 'commit', sessionId, sha, diff?}`: message, author and files, read from the
  published diff that holds it). The chat's checklist commit chips, its pinned ticket rows and the
  Files list use it.
- **Behavior.** It opens after `HOVERCARD.openDelayMs` (400 ms) of hover or keyboard focus, stays
  while the pointer moves into it, and closes on leave, Esc or a press of the chip.
- **Lists (0.64.0).** A card opens beside what its chip sits in, never over the rows that follow:
  beside an ancestor marked `data-hc-boundary` (the runner's side panel: the card opens to its
  left), else beside the chip's row (its `li` or `[data-hc-row]`), else beside the chip, on the
  side with room (`placeCard`); below or above only when neither side has room. While a card is
  open, and `HOVERCARD.warmMs` (300 ms) after one closed, another chip's card opens after only
  `warmDwellMs` (70 ms, so a fast sweep does not read every card). Out of the chip, the card stays
  only while the pointer heads for it: inside the safe triangle (its apex `apexBehindPx` behind
  where the pointer left) and, judged over a window (`windowMoves` moves or `windowMs`), pointing
  between the card's near corners (± `angleDeg`) and no steeper than `steepDeg`; moves under
  `jitterPx` are not judged. Each move toward it restarts `graceMs`, `headingMaxMs` caps the trip;
  any other move closes it at once, and reaching another chip switches to that chip's card at once,
  whatever the triangle says. A
  chip nested in a row (`data-hc-anchor`) keeps its own focus and hover.
- **Live from the board (0.67.0).** A card read whose `CardPreview` has `live: true` says "Live from the
  board" in its hovercard (the rows carry no mark for it).
- **Rows (0.64.0).** `useHovercard(target)` gives a whole element (a list row) the same card:
  spread its `props` on the row and render its `card`. A `{kind: 'run', title, facts}` target is a
  card of facts (`HOVERCARD.fields.run`: kind, engine, model, state, the whole step, elapsed,
  tokens, card, started by), each `{value, raw?}`, the raw value in the fact's tooltip. Only a device that
  can hover (`(hover: hover)`) gets one; a tap on a touch screen opens the thing as before. It is
  drawn in a portal on the page's body, under or over its chip and kept on screen, so a narrow side
  panel never clips it. It holds no links or controls.
- **Data.** Nothing is read until a card opens. `HovercardProvider` says where to read
  (`files`, `artifact(sessionId, id)`, `card(target)`, `reviewOpen(sessionId, id)`,
  `worker(target)`, `diffFor(sessionId, sha)`); a nested provider adds to the one above it, and inside
  a chat the chat's files are used. File and commit reads are cached (bounded): a diff is read
  and parsed once for all its commits, and only its commits are kept.
- **Never more than 256 KB.** A commit's diff is read with `TransportFiles.rawCapped(sessionId, id,
  max)` (new, optional; `createFiles` has it): null when Content-Length is over `max`, or when the
  stream passes it, which stops the download; null too when neither tells the size. A transport
  without it previews a diff only when the list gives a size within the cap.
- **Tuning and turning off.** `HOVERCARD` holds the delays, the width, the line counts and which
  fields each kind draws (`fields.card`, `fields.file`, `fields.commit`, in order).
  `setHovercardsEnabled(false)` turns every hovercard off (kept per browser under
  `blerg.chat.hovercards`).

## Commits read from the session's workspace

New in 0.69.0: `TransportFiles.commit` and `commitDiff` (`createFiles({ commits: true })`; the Blerg
transport has them), `CommitDetails`, `CommitDiff`, `COMMIT_DIFF_MAX`, `CommitViewer`,
`CommitViewerHost`, `useCommitOpener`, `repoName`, `visibleText`, `COMMIT_MISS_MS`, `HovercardSources.repo` and `account`.

- **A commit no published diff holds is read from the session.** `commit(sessionId, sha)` reads
  `GET <base>/commits/<sha>` (7 to 64 hex digits; anything else is not asked): the full and short
  sha, subject, body, author, author date, parents and the files changed with +/− counts, each
  bounded by the server. It resolves null for every refusal (no access, the session ended, its
  daemon offline, an unknown sha): there is nothing for the person to fix.
- **The hovercard.** A commit's card reads the published diff that holds it first, then the
  session's workspace. Neither: the card shows the short sha and the repository's name
  (`HovercardSources.repo(sessionId)`), with no error text. Reads are cached per session and sha
  (64, newest kept); a read that found nothing is asked again after `COMMIT_MISS_MS` (a minute).
  `HovercardSources.account()` names who is signed in: when it changes, every hovercard read is
  forgotten. Subjects, messages, authors and file names show control and bidirectional characters
  as visible escapes (`visibleText`: controls, bidirectional and invisible formatting, the line and
  paragraph separators, the soft hyphen and the tag characters), and file names are isolated
  (`<bdi>`). `CommitViewer` does the same for the commit's header and message.
- **The click.** Under a `CommitViewerHost` whose `files` can read commits, a commit chip with no
  published diff opens `CommitViewer`: the commit's diff generated on demand
  (`commitDiff(sessionId, sha)`, `GET <base>/commits/<sha>/diff`, read no further than 2 MB) in the
  viewer every published diff uses. Over the cap it says the diff is too large; unreadable, one
  neutral line. The host's `opened(sessionId, sha)` can open a published diff instead (return
  true). Without a host, a chip copies its sha as before.

## MCP servers and checklist commits

Added in 0.15.0; nothing removed. Older builds draw neither.

- **Event kind `mcp_status`** (the runner records it when an engine reports its MCP servers). It is
  never drawn in the transcript. `readMcp`, `readMcpHold`, `mcpFromEvents`, `mcpSummary` ("Connecting
  2 of 3…", "2 connected · 1 failed · 1 needs sign-in") and `holdClock` read it for a side panel.
  `LiveState` gains optional `mcp`, `mcpHold` (the first message is held while servers connect) and
  `canReleasePrompt`; `useLiveState` derives the first two from the events held.
- **`McpHoldLine`** is the line "Waiting for MCP servers before sending your first message · 0:12 /
  0:30 · Send now". Mount it in `slots.aboveComposer`; pass `onSendNow` only for a viewer who may
  release the prompt (the runner's session owner), otherwise it has no button.
- **Checklist items carry commits.** An item (and a checklist event's `changed`) may hold `commits`,
  shas of 7 to 40 hex characters. The checklist row and `ChecklistItems` draw them as chips (see
  Tickets and commit chips for 0.19.0). `CommitChips` is the shared chip list. (0.16.0 replaces the per-sha
  "View diff" hook: see Diffs.)

## Files the agent mentions

Added in 0.16.0; nothing removed. Older builds leave such text as text.

- **`canOpenWorkspaceFiles`** (ChatView): with a transport that has `files.workspaceFile` (the
  runner's), a relative link or a path in inline code in the agent's text (`README.md`,
  `src/a.go:120`, `docs/a.md#L12`) opens that file from the session's folder in the viewer, at the
  line named, with Copy path, Download and Save a copy to Files. Pass it only for a viewer the route
  accepts (the runner's session owner).

## Files in the viewer

Changed in 0.18.0; nothing removed, no new props.

- **Code and JSON are colored in the viewer.** A text file opens as numbered lines, colored by its
  extension (the grammars the inline preview uses); JSON is pretty-printed when it parses and colored
  as JSON. Only the first 100,000 characters are colored, the rest of a large file follows plain. A
  file of no known language stays plain wrapped text.
- **One table for CSV and TSV.** The viewer and the inline preview draw the same bordered table with a
  header row and zebra rows; the preview is its first rows and columns.
- **Markdown reads like GitHub.** Since 0.61.0 every rendered Markdown block follows GitHub's README
  styling (adapted from github-markdown-css, MIT): headings 2em/1.5em/1.25em/1em at weight 600 with
  24 px above and 16 px below, ruled h1 and h2, 16 px between blocks, .25em between list items, inline
  code at 85% in a 6 px chip, code blocks at 85% / 1.45 with a 16 px pad, collapsed tables with
  6 px 13 px cells and zebra rows, a quote with a .25em bar. GitHub's px are written in units of the
  block's own text size (`--chat-md-unit`), so a document (`md-doc`: GitHub's font stack at 16 px;
  the inline preview, the viewer, the review page, a ticket's description) matches GitHub exactly. A
  chat message (`.md` without `md-doc`) is compact: the same rhythm at a .75em gap (`--chat-md-gap`)
  and tamer headings (h1 1.3em, h2 1.15em, h3 1.05em, no rules). An app's own bare `h1`, `h2` and `code`
  styles no longer leak in. Since 0.73.0 a document in a viewer (the file viewer, the folder viewer,
  review mode: class `md-box`) reads in GitHub's box: centered, a 1 px border and 6 px radius, 32 px
  of padding around a 1012 px text column, however large or maximized the dialog; in a column 700 px
  or narrower (a phone, review's page beside its source: a container query on the viewer's body and
  review's page column) it is the page itself, with no border. Colors are the usual `--chat-*` tokens.
- **Raw HTML as GitHub allows it, in documents.** New in 0.70.0: `Markdown`'s `html` prop (off by
  default) renders HTML in Markdown (a README's centered banner, badges, a table of screenshots)
  through an allowlist: `p div span img a table thead tbody tr td th b i em strong br hr sub sup
  details summary picture source kbd code pre ul ol li h1–h6 blockquote`, with `align width height
  alt title href src srcset colspan rowspan open`. Only document surfaces opt in: the file viewer,
  the folder-file viewer and review mode. Chat messages, approvals (proposal previews), tickets,
  board cards and threads keep HTML as literal text, so a comment or a closed `<details>` cannot
  hide words from the person reading. DOMPurify (now a direct dependency; mermaid already brought
  it) drops everything else, and the result is rebuilt as React elements, checked against the
  allowlist again: nothing is serialized and parsed a second time. Each HTML block is sanitized once
  and cached. Links keep http(s), relative and `#fragment` URLs; images http(s), relative and base64
  raster data; width and height are clamped (4096 px, 100%) and images keep their aspect ratio.
  `srcset` and `<source>` never load. Past 200 levels of nesting a note says the rest was cut, and
  Markdown that an unclosed element (`<math>`, `<svg>`, `<title>`, `<xmp>`, `<iframe>`, …) would
  swallow follows behind a note. Review mode says "This document has hidden content" (with **Show
  source**) when the page does not show every word: an HTML comment, a closed `<details>`, text the
  sanitizer dropped.
  Headings get GitHub's ids (`user-content-<slug>`), and a `#fragment` link scrolls to its heading.
  `Markdown`'s `document` prop (`MarkdownDocument`: `path`, `readWorkspace`) marks a document rather
  than chat text: a relative image loads from the session's folder through `readWorkspace`, resolved
  beside `path` and never outside the folder, through one queue per page (two reads at a time,
  retried by the server's machine-readable `code`: `busy` for as long as the document is open, `daemon_timeout` and `changed` three times, anything else not at all; one backoff for the whole page, paused while the tab is hidden; a read that failed for a reason reading again could fix offers **Retry**, drawn after the link an image sits in), at most 50 per document before **Load
  images**; a folder SVG draws through `<img>` from a `data:` URL.
  Every viewer makes its media URLs with `mediaUrl` (`releaseMediaUrl` frees them): an SVG is never
  a same-origin `blob:` URL, which opened in a tab would run its script as the app; `blobTypeFor`
  never returns `image/svg+xml`. The folder-file viewer's own read goes through the same queue. A relative link opens the
  folder's file inside Blerg (`openWorkspaceFile(path, line, { folder: true })`). A published or
  uploaded document has no folder: its relative images show their alt text and its relative links
  stay text. A remote image shows its alt text, the host and **Load image**, and loads (lazily,
  without a referrer) only on that click. Chat text loads no image at all, as before.

## Copying and pasting

Added in 0.17.0; nothing removed, no new props. The converter is hand-written on the DOM (no
dependency, about 6 kB gzipped, in the main bundle because a copy event must answer synchronously).

0.17.1 changes wording only, to American English: the markup colors ("Accent color", the "Color" group), the
"upload was canceled" message, and `center` in an anchor's place name (it was `centre`).

- **Copy on a message.** Every assistant and own message has a Copy button (hover or keyboard focus;
  always shown on a touch screen). It puts the message's Markdown source on the clipboard as
  `text/plain` and the rendered message, sanitized, as `text/html`.
- **Selecting keeps structure.** A copy whose selection lies in rendered Markdown puts the selection
  as Markdown (`text/plain`) and sanitized HTML; timestamps, buttons and the like are left out. A
  selection inside one code block copies the code exactly. Anything else, or any failure, is the
  browser's own copy.
- **Pasting into the message box.** Rich HTML (a web page, Docs, Slack, GitHub, Jira) is inserted as
  Markdown: headings, emphasis, code and fenced blocks with their language, lists, task lists,
  `http`/`https`/`mailto` links, tables, quotes and rules. Images, scripts, styles, frames, forms and
  raw HTML are dropped. Plain text, HTML that is only spans (an editor or terminal) and text that is
  already Markdown go in unchanged; Ctrl/Cmd+Shift+V pastes plain text; undo takes the paste back in
  one step; a pasted file still attaches.

## Diffs

Added in 0.16.0. Removed: ChatView's `onViewCommitDiff` (0.15.0); `ChecklistItems`'s `onViewDiff`
now takes an artifact id, not a sha.

- **View `diff`.** A published `.diff` or `.patch` (the runner gives it view `diff`; `blerg-runner
  diff` makes one from git) opens in **`DiffView`** inside the viewer: the files with their +/−
  beside the patch (above it under 700 px), unified by default and **Side by side** from 900 px,
  hunk headers, both sides' line numbers, runs of more than 8 unchanged lines folded behind a click,
  each file's code colored by its extension with the code colors, commit headers for a patch of
  commits, binary files by their size, **Copy patch**. A file draws `DIFF_FILE_LINES` (5 000) lines
  before **Show more**; a patch over 2 MiB is download-only like any text. The patch is parsed
  (`parsePatch`, `patchSummary`, `splitRows`, `foldContext`) and drawn as text, never markup. A
  workspace file named `.diff` or `.patch` opens the same way.
- **The diff card.** An artifact of view `diff` up to 256 KiB is read when its card nears the
  viewport and says "3 files · +12 −4" with the first file names; the tile opens the viewer. A larger
  one keeps the plain card (name and size).
- **Checklist "View diff".** A checklist item may carry `diff`, the artifact id of the diff
  `blerg-runner checklist tick --commit` published of its commits. The item's commit chip gets one
  **View diff**, which opens that file in the chat's viewer when the session's list has it (no
  button otherwise). `ChecklistItems`'s `onViewDiff(artifactId)` lets a side panel open it its own
  way. Two helpers are exported for hosts that color code themselves: `useHighlighterReady` and
  `splitTokenLines`.

## Code, pasted text and file previews

Added in 0.12.0; no new props, nothing removed.

- **Code blocks.** A fence that has closed is colored for ts/tsx/js/jsx, go, python, bash/sh,
  json, yaml, sql, html/xml, css, rust, java, c/c++ and markdown (an unknown language stays plain);
  `diff` and `patch` fences color added and removed lines. The highlighter is `lowlight`
  (highlight.js grammars, MIT/BSD-3) in a chunk of its own, loaded when the first block is
  drawn: plain text shows until it arrives, it produces spans (never HTML), and results are
  cached by language and text. A fence still streaming is plain and uncapped; the cap and the
  colors apply once it closes. Card fences work as before.
- **Line cap.** A block over 25 lines shows its first 15 with a fade, "Show all N lines" (and
  "Show less") and "Open": a small viewer with the language, line numbers and Copy. Copy on
  the block always copies the whole block.
- **Pasted text.** A person's message over 25 lines compacts the same way (a long closed fence
  inside it caps itself instead). A message a host draws as a collapsed brief is not touched.
- **File previews.** Besides images, video/audio and HTML, a published file (and a person's
  attachment) shows a small preview in its card, fetched once when the card scrolls into view
  and opening the viewer on click: markdown rendered (about 12 lines, no card fences), text, code
  and JSON (12 lines; code colored by file extension; JSON pretty-printed), CSV/TSV (6 rows by 6
  columns) and a PDF's first page. Text-like files preview up to 256 KiB and PDFs up to 5 MiB;
  over that, or if the fetch fails, the card stays plain. Only the small result is kept (up to
  50 per kind), and the PDF document is released after its page is drawn.
- **Theme.** The colors are `--chat-code-*` and `--chat-diff-add`/`--chat-diff-del` tokens with
  light and dark values (`blerg.css` follows the app's `--diff-add`/`--diff-del`).

## Review and mark-up

The file viewer that `ChatView` opens has two feedback modes, with nothing for the app to wire:
**Review** on a markdown, PDF or diff file (anchored change requests, direct edits of markdown, one
submit) and **Mark up** on an image (pen, arrow, box, numbered pins). Both go out through the
transport's ordinary `files.upload` and `send`, so a transport without `files` shows neither.
PDF pages are drawn by `pdfjs-dist`, which the package loads on demand in a chunk of its own
(worker included); the chat bundle does not carry it. The session answers with
`blerg-runner review reply`; the replies are read back from `<file>.review.json`.

Since 0.27.0 the session's `<file>.review.json` publishes draw no file card: they are review
state, not documents. Each round of answers (the publishes of one file's review between two of the
person's messages) is one line at its newest publish, "✓ 11 requests answered · report.md", which
opens the reviewed file in Review. The count is the requests the round settled, compared with the
review before it; until that is read, or when the review before it is on a page not loaded, it is
the number of publishes. The files stay in the Files panel. `reviewRounds` and `isReviewStateEvent`
are exported for a host that draws its own transcript.

Diff review uses the same version-1 `ReviewFile` and feedback path. Its text anchors carry
optional `DiffAnchor` metadata: source file, old/new side, start/end lines, file occurrence,
hunk, full commit when available and the source artifact id. `DiffView` accepts controlled
`selection` / `onSelection`, existing `requests` and `focusedId`. A saved request highlights only
when its source metadata and quote still match; the review surface filters older artifacts.
There is no new review endpoint or agent reply format.

## Agent procedure

A session can do the whole integration unattended. Replace `core.example.test` with the core's
origin; the app is a Go backend with a React frontend. Stop at any failing check and report it.

1. **Find the package.** `curl -fsS https://core.example.test/agents | jq '.components[] | select(.name=="blerg-runner") | .ui.chat_package'`. Check: the object has `url`, `sha512` and `version`. No `ui` field means the runner was built without the package; stop and report.

2. **Read the docs.** `curl -fsS <docs_url>` is this file; read it once.

3. **Install.** In the frontend: `npm i <url>`. Check: `node -e "console.log(require('@blerglab/chat/package.json').version)"` prints the manifest's `version`, and `package-lock.json` has an `integrity` for `@blerglab/chat` equal to `sha512-<base64 of the manifest's sha512>` (hex in the manifest: `sha512-$(echo <sha512> | xxd -r -p | base64 -w0)`; base64 in the manifest: `sha512-<value>`). A mismatch is a hard stop.

4. **Copy the proxy.** `mkdir -p <backend>/internal/chatproxy && cp node_modules/@blerglab/chat/proxy/go/chatproxy.go node_modules/@blerglab/chat/proxy/go/chatproxy_test.go <backend>/internal/chatproxy/`. Check: `cd <backend> && go vet ./internal/chatproxy/ && go test ./internal/chatproxy/` passes (it runs against a stub runner; no network).

5. **Wire it.** Implement `RunnerClient` over the app's runner client (or use `HTTPRunnerClient`), write `Authorize` over the app's sign-in and its session-to-job mapping, and add the `Mount` call next to the app's other routes with prefix `/blerg`. Check: `go build ./...` and `go test ./...` pass.

6. **Render.** Add `import '@blerglab/chat/tokens.css'`, a `chat-theme.css` with the tokens above, and a page that mounts `<ChatView session={id} transport={createProxyTransport({ baseUrl: '/blerg' })} />` for a session the person owns. Check: the frontend's typecheck, lint and build pass.

7. **Smoke the proxy.** With the app running and a signed-in cookie `COOKIE` and a session `SID` of that person's:
   - `curl -fsS -b "$COOKIE" http://localhost:8080/blerg/sessions/$SID` returns the session's status JSON.
   - `curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE" http://localhost:8080/blerg/sessions/<another person's session>` is `403`.
   - `curl -sS -o /dev/null -w '%{http_code}' http://localhost:8080/blerg/sessions/$SID` (no cookie) is `403` or `401`, whatever `Authorize` returns for an anonymous request; never `200`.
   - `curl -sSN -b "$COOKIE" "http://localhost:8080/blerg/sessions/$SID/events/live?tail=1" | head -c 2000` shows `event:` frames arriving, the first batch ending with `event: replay_done`, then `event: status`.
   - `curl -sS -D - -o /dev/null -b "$COOKIE" http://localhost:8080/blerg/sessions/$SID/artifacts | grep -i set-cookie` prints nothing.

8. **Open the page.** Load the chat page in a browser (or a headless one) for `SID`. Check: the transcript shows the session's history, typing a message and sending it makes a user bubble appear and the runner's session receive it (`GET /blerg/sessions/$SID/events?limit=1` after a moment shows the new event), the Files panel lists the session's files, and the page renders in both color schemes.

9. **Report.** The package version and sha512 installed, where `chatproxy.go` landed, the prefix, what `Authorize` keys on, and the output of each check.

Model labels are presentation only: `modelDisplayName(id, suppliedName?)` shortens recognized families, while `modelDisplayNames(catalog)` disambiguates collisions using model details and routes. Keep the original ID as the option value and full-ID tooltip; never send a display name to an engine.

## Confirming an action that cannot be undone

New in 0.71.0: `ConfirmPopover` and `endSessionText`.

- **`ConfirmPopover`** asks before an action that cannot be undone (End session), anchored to the
  control that opened it: `anchor` (a ref to that control), `title`, `description`,
  `confirmLabel` (its red button, the only red), `cancelLabel` (default "Cancel"), `onConfirm`,
  `onCancel`, `testId`. It is an `alertdialog` named by its title and described by its text; the
  focus starts on Cancel and stays between the two buttons; Esc or a press outside cancels and the
  focus goes back to the anchor; the rest of the page is inert while it shows. Under the anchor on a wide screen, kept inside the window; at
  640 px and less a bottom sheet. It replaces the "press again to confirm" arming:
  `StartProgress`'s Stop now asks with it.
- **`endSessionText(runtime, terminal?)`** says what ending a session keeps, by where it runs:
  this machine (`daemon`) "The agent stops. The conversation and files are kept."; a local
  sandbox, that its container is removed and its files and commits are kept; a cluster pod, that
  the pod is removed and work in it that was not pushed is lost; unknown, "The agent stops. Work
  not yet saved may be lost; the conversation is kept." With `terminal` it speaks of a terminal
  session, which has no conversation. `StartProgress`'s `session` takes an optional `runtime` for it.
