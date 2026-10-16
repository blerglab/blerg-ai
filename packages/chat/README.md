# @blerglab/chat

The chat surface for apps built on Blerg sessions: transcript, composer, files, cards and a
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
      "version": "0.11.0",
      "url": "https://runner.example.test/packages/blerglab-chat-0.11.0.tgz",
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
`react` and `react-dom` 19 as peers and brings `react-markdown`, `remark-gfm` and `zustand`
with it; nothing else.

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
POST   /blerg/sessions/{id}/model                 -> 501 (reserved)
GET    /blerg/sessions/{id}/artifacts             -> GET    /api/runner/sessions/{id}/artifacts
GET    /blerg/sessions/{id}/artifacts/{aid}/raw   -> GET    .../artifacts/{aid}/raw
GET    /blerg/sessions/{id}/artifacts/{aid}/download -> GET .../artifacts/{aid}/download
DELETE /blerg/sessions/{id}/artifacts/{aid}       -> DELETE .../artifacts/{aid}
POST   /blerg/sessions/{id}/uploads               -> POST   /api/runner/sessions/{id}/uploads  (raw bytes, X-Artifact-Name)
```

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
- `slots`: `header`, `composerActions`, `messageFooter` for your own chrome.
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
upload routes; a `501` from `/model` simply leaves `setModel` unavailable. The transport
subscribes with `tail=true` to open a long transcript at its end and resubscribes from the last
sequence number after a drop.

An app whose own sign-in is a bearer token rather than a cookie passes `headers` (asked for on
every request, the live stream's reconnects included) and `onUnauthorized`:

```ts
createProxyTransport({
  baseUrl: '/api/chat',
  headers: async () => ({ Authorization: `Bearer ${await freshToken()}` }),
  onUnauthorized: () => signIn(),
})
```

`ChatView` takes a few props for the host's own rules. `describeUserMessage` says how a user
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
| `--chat-text-xs` | smallest text size |
| `--chat-text-sm` | small text size |
| `--chat-density` | spacing scale (1 = default) |
| `--chat-card-user-bg`, `--chat-card-assistant-bg`, … | per-kind card backgrounds |

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

## Review and mark-up

The file viewer that `ChatView` opens has two feedback modes, with nothing for the app to wire:
**Review** on a markdown or PDF file (anchored change requests, direct edits of markdown, one
submit) and **Mark up** on an image (pen, arrow, box, numbered pins). Both go out through the
transport's ordinary `files.upload` and `send`, so a transport without `files` shows neither.
PDF pages are drawn by `pdfjs-dist`, which the package loads on demand in a chunk of its own
(worker included); the chat bundle does not carry it. The session answers with
`blerg-runner review reply`; the replies are read back from `<file>.review.json`.

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

8. **Open the page.** Load the chat page in a browser (or a headless one) for `SID`. Check: the transcript shows the session's history, typing a message and sending it makes a user bubble appear and the runner's session receive it (`GET /blerg/sessions/$SID/events?limit=1` after a moment shows the new event), the Files panel lists the session's files, and the page renders in both colour schemes.

9. **Report.** The package version and sha512 installed, where `chatproxy.go` landed, the prefix, what `Authorize` keys on, and the output of each check.
