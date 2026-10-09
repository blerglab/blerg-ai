# `@blerglab/chat`: one chat surface for every app built on Blerg sessions

Status: design, 2026-10-06, amended after an adversarial review (see "Review amendments"); the
owner approved the two contract additions (A, B below). Applies to a new workspace package
`packages/chat`, the runner's web app (its first consumer), the runner's v1 contract (two
additive operations and one manifest field), and the docs apps read.

## Why

Every app built on Blerg sessions — each of the owner's own apps, next week's one — rebuilds the
same chat: a transcript that renders markdown, tool calls, files and the person's attachments; a
composer with attachments; a viewer; the same arguments about styling and the same bugs fixed
twice. The runner's own chat is the reference implementation and the only one that keeps up.
This makes it a package the runner uses too, so it cannot drift, and the apps add only what is
theirs: a palette, fonts, and their own inline cards.

## Decisions (made with the owner)

1. One package, `@blerglab/chat`, in the Blerg monorepo under `packages/chat` (a workspace root
   is added). The runner's web app is the first consumer; an outside app the second, either
   from an agent script we hand a session or by the session discovering the package through the
   agent contract and the operator's own guide to Blerg and doing the integration itself.
2. Distribution now: Blerg itself serves versioned tarballs
   (`GET <runner>/packages/@blerglab/chat-<version>.tgz`) and the agent contract advertises the
   current one. Later: `npm publish` of the same artifact (GitHub Packages, then the public
   registry once the repository is public). Apps never change: they install the URL the
   contract gives them today and the package name tomorrow.
3. Transport is pluggable, two adapters ship: `BlergTransport` (the runner's REST + websocket,
   with the person's own token — what the runner UI uses) and `ProxyTransport` (the app's own
   backend, over a small documented contract that mirrors the runner's). An agent token never
   reaches a browser.
4. App-specific inline cards: fenced blocks in the agent's text (```` ```card:<kind> ````, JSON
   body) rendered by components the app registers; an unregistered kind stays a code block. A
   first-class `app_card` transcript event comes later when something needs the structure.
5. Theming is CSS custom properties only, with the runner's current look as the default theme.

## What the package is

```
packages/chat/
  src/
    transport/      Transport interface; BlergTransport; ProxyTransport; the proxy contract
    model/          the transcript model: events -> groups -> what to draw (today's lib/toolGroups,
                    harnessText, attachments note, artifact versions), with no React in it
    components/     ChatView (the surface), Transcript, Composer, ArtifactCard/Viewer/FilesPanel,
                    ToolGroup, StartProgress, Markdown (with the card fence hook)
    cards/          registerCard(kind, Component) and the fence parser
    theme/          tokens.css (the contract), blerg.css (the default theme), ThemeProvider
    index.ts        the public surface, and nothing else is public
  README.md         for the app author (and the session integrating an app)
  package.json      peerDependencies: react ^19, react-dom ^19; no other runtime dependency
                    beyond what the runner's chat already has
```

**Public API** (the whole of it):

- `<ChatView session={id} transport={t} theme?={tokens} cards?={registry} slots?={…} />` — the
  surface: transcript, composer, files. `slots`: `composerActions`, `messageFooter`, `header`.
- `createBlergTransport({ baseUrl, token })`, `createProxyTransport({ baseUrl })`.
- `defineCard(kind, Component)` / `createCardRegistry([...])`.
- `useSession(transport, id)` — the hook `ChatView` is built on, for an app that wants its own
  surface: `{ events, streaming, status, send(text, files), stop(), artifacts }`.
- The `Transport` interface, so an app can write a third adapter.
- Types: every event payload the transcript understands (today's 15 kinds), `ArtifactInfo`,
  `ThemeTokens`.

**What stays in the runner**: the session header, model/effort switching, the capabilities
panel, start-stage chrome that reads runner-only state, the sidebar — everything that is the
runner *app* rather than the chat. The runner's `AgentChatView` becomes a thin page that mounts
`ChatView` with `BlergTransport`, the runner theme and the runner's slots.

## Two additions to the runner's v1 contract (approved)

Today an agent token can start, message and follow a session, but cannot touch its files (the
artifact and upload routes are browser-only), and the contract's event stream is a database poll
with no typing deltas and no session status. An app's chat needs both, so the contract gains,
additively and under the same `requireSessionAccess` rule as `message` (the token's account
started the session):

- **A. Session files for agent tokens.** `GET .../sessions/{id}/artifacts` (list, with
  versions), `GET .../artifacts/{aid}/raw` and `/download`, `DELETE .../artifacts/{aid}`, and
  `POST .../sessions/{id}/uploads` (a person's attachment, the app vouching for the person). The
  same handlers as the browser routes behind a second auth path; the same caps and the same
  "a file is deleted by whoever may see the session" rule. Listed in `runnerOperations`, in
  `openapi.json`, documented in `runner/README.md`.
- **B. A live stream.** `GET .../sessions/{id}/events/live?after_seq=` — SSE straight from the
  hub, not the database: the replay (with its `replay_done` frame carrying `has_more`,
  `has_older`, `first_seq`, `last_seq`, `server_time`), then every persisted event as it is
  recorded, the transient typing deltas (`streaming` frames, never persisted), and `status`
  frames (the runner's `SessionStatus`: starting, running, waiting, idle, disconnected, ended,
  error) whenever it changes. The existing polling stream stays for callbacks and CLIs.

Both are what the runner's own browser gets over its websocket, offered to the token that
started the session. Neither reaches another account's session, a private session not the
token's, or a file of another session.

## The transport contract

`Transport` is what a chat needs from a session — what the runner's view actually reads today,
no more:

```ts
interface Transport {
  // Replay then live. tail=true asks for the newest page first (how the runner opens a long
  // transcript); onReplayDone carries has_more/has_older/first_seq/last_seq/server_time.
  subscribe(sessionId, opts: {afterSeq?: number; tail?: boolean},
            handlers: {onEvent; onStreaming; onReplayDone; onStatus; onConnection}): () => void
  loadOlder(sessionId, beforeSeq, limit): Promise<{events; hasOlder; firstSeq; serverTime}>
  session(sessionId): Promise<SessionMeta>   // status, engine, runtime, model, effort, started_at, ended_at
  send(sessionId, text): Promise<{queued: boolean}>   // false: not connected, nothing was sent
  stop(sessionId): Promise<void>
  setModel?(sessionId, model, effort): Promise<void>  // optional: the composer's /model and /effort
  connected(): boolean
  files?: {                                     // absent: ChatView shows no Files panel, no attach
    list(sessionId): Promise<ArtifactInfo[]>
    raw(sessionId, id): Promise<Blob>           // bytes through the authenticated route, never a bare URL
    download(sessionId, id): Promise<void>
    remove(sessionId, id): Promise<void>
    upload(sessionId, file, onProgress): Promise<UploadedFile>
  }
}
```

`SessionMeta.status` is the runner's `SessionStatus`; the composer's lock, the queued and
pending bubbles, the working/waiting indicators and the disconnected banner all key on it, so
every transport must supply it live (`onStatus`). The attachment note is composed by the
package from the uploaded files' names and sizes, exactly as today, and `send` carries text
only, as the runner's message body does.

**`BlergTransport`** wraps the runner's own routes and websocket for the runner's web app:
`createBlergTransport({ baseUrl, getToken, socket?, onUnauthorized? })`. The host supplies
`getToken` (the runner passes `ensureFreshToken`/`getAccessToken`: a token lasts ten minutes and
is renewed silently) and may inject its shared socket (the runner's is a module singleton other
panels use). It works from the runner's origin with a core-issued browser token, which is the
only place such a token exists; an outside app never uses it.

**`ProxyTransport`** talks to the app's own backend over the **proxy contract**, which mirrors
the runner's v1 operations one to one (same bodies, same frames, A and B included):

```
GET    /blerg/sessions/{id}                     SessionMeta
GET    /blerg/sessions/{id}/events/live?after_seq=&tail=   SSE (B's frames, verbatim)
GET    /blerg/sessions/{id}/events?before_seq=&limit=      older pages
POST   /blerg/sessions/{id}/messages            {text}
POST   /blerg/sessions/{id}/stop
POST   /blerg/sessions/{id}/model               {model, effort}        (optional)
GET    /blerg/sessions/{id}/artifacts           and /{aid}/raw, /{aid}/download, DELETE /{aid}
POST   /blerg/sessions/{id}/uploads             raw bytes, X-Artifact-Name
```

The app authenticates the person its own way (its session cookie), decides whether that person
may see that Blerg session (its rule: the session belongs to a job of theirs), and forwards to
the runner with the app's agent token. The package ships `proxy/go/chatproxy.go` — a reference
handler set over a `RunnerClient` interface an app's existing runner client satisfies —
which a session copies into the app (apps cannot import the private module). A proxy never
widens the token: each route forwards one session's traffic, and the app's own authorization
runs before any of them.

## Cards

In assistant text:

````
```card:character
{"name": "Mara Vance", "role": "antagonist", "arc": "…"}
```
````

The markdown renderer (react-markdown + remark-gfm, no raw HTML, links through `safeUrl`,
images never loaded — unchanged) intercepts the fence at the `code` element, where the info
string arrives as `language-card:character`, and hands `{kind, data}` to the registry. A
registered card renders in place of the block; an unregistered one renders as the code block it
is (the person still sees the data; nothing is hidden). The JSON is parsed with strict
`JSON.parse` (no extra dependency); anything but an object, or a parse failure, renders the raw
block with a one-line note. While the text is still streaming, an unclosed fence is a code
block until it closes — partial JSON is never parsed. A registered card component renders its
fields as text, never as HTML, and puts links through `safeUrl`: the data is model output. The
agent learns the kinds and shapes from the app's own instructions (its CLAUDE.md, a skill); the
package defines no kind.

Later, by design but not built now: an `app_card` transcript event (`blerg-runner card <kind>
<json>`) that the registry routes identically, for cards that must survive a rewrite of the
text or come from a tool rather than the model.

## Theming

`theme/tokens.css` is the contract: `--chat-bg`, `--chat-surface`, `--chat-surface-2`,
`--chat-fg`, `--chat-muted`, `--chat-muted-2`, `--chat-accent`, `--chat-accent-fg`,
`--chat-success`, `--chat-warning`, `--chat-info`, `--chat-danger`, `--chat-border`,
`--chat-radius`, `--chat-font`, `--chat-font-mono`, `--chat-text-xs`, `--chat-text-sm`,
`--chat-density` (spacing scale), plus the per-kind card tokens (`--chat-card-user-bg`,
`--chat-card-assistant-bg`, …). The runner's chat CSS today uses seventeen palette names
(`--basalt`, `--scree`, `--stone`, `--blaze`, `--lichen`, `--amber`, `--batch`…) in ~170 places
across three files: a mechanical rename onto the contract. Every component styles against the
contract and nothing else, enforced by a lint rule (no `var(--` outside `--chat-*` in the
package); `blerg.css` maps the contract to the runner's palette, both its dark and light blocks,
so the runner looks exactly as it does today. An app supplies its own file or a `ThemeTokens`
object to `ChatView`; light/dark follow the host's `prefers-color-scheme` through the tokens,
not through component code.

## Distribution

- `packages/chat` builds to `dist/` (ESM + types + `tokens.css` + `blerg.css`) with Vite in
  library mode; `npm pack` makes the tarball.
- The runner's image build packs it and the runner serves it at
  `GET /packages/@blerglab/chat-<version>.tgz` (a new route, not the `no-cache` static file
  server: unauthenticated like `/agents`, `Cache-Control: public, max-age=31536000, immutable`,
  404 for a version it does not have), plus `GET /packages/` (JSON: name, versions, each with
  `sha512`). The runner's Dockerfile today builds `runner/frontend` alone with its own lockfile;
  with a workspace root the build context becomes the repository root and the lockfile the
  workspace's — a listed change, with the sandbox and pod images untouched.
- The agent contract: the runner's manifest entry gains `ui: { chat_package: { name, version,
  url, sha512, docs_url } }`, so `/agents` tells a session what to install, how to pin it and
  where to read. `ComponentEntry` lives in `contracts/agentsmanifest` (shared with core and the
  board) and the manifest/openapi/mux cross-check test covers the new operations, so the change
  is to the shared contract type and `openapi.json`, additive.
- From a cluster pod `npm i <url>` works: the pod image has node and npm and reaches the
  runner's public hostname; a workstation uses the same public origin.
- The operator's own guide to Blerg for their apps gets a section: "to give the
  app a chat, install `@blerglab/chat` from the URL in `/agents`, mount the proxy handlers,
  render `ChatView`".
- Later, `npm publish` from the same `dist/` on release (a workflow step behind a flag); the
  contract's `url` then points at the registry tarball. Nothing about the package changes.

## Migration of the runner

What moves cleanly (about a third): the pure model — tool grouping, live calls, harness text,
the attachment note, artifact grouping and versions — and the markdown renderer. What moves
once it takes the transport instead of `apiFetch` (another third): the artifact card, previews,
viewer and files panel, the tool-group renderer, the composer chips. What is **rewritten** on
`useSession` (the last third): the body of `AgentChatView` — its status machine over
`useSessionStore`'s `SessionInfo`, the subscribe/paging effect over websocket frame shapes, and
the runner chrome interleaved in it (the rules panel, the model/effort selects, the capabilities
panel, start-stage chrome that issues its own requests). Those stay in the runner as slots and
siblings of `ChatView`, not inside it. Runner-only persistence (`blerg.agent.compactTools`, the
draft keys, the `?artifact=` link) becomes `ChatView` props with runner-supplied storage.

1. The package is built with its own tests; the runner then imports from `@blerglab/chat`
   through the workspace, so the runner's suites are the package's first integration test.
2. Parity is the acceptance test: the runner looks and behaves exactly as before (a staging
   install, side by side, before the deploy).
3. `runner/frontend` keeps a thin `AgentChatView` page; the duplicated code is deleted in the
   same change, not "later".

## The second consumer

An outside app, as the proof that one can do it: the owner gives its session the agent-script
version of the README (or the session reads `/agents` → `docs_url` itself), the session mounts
the proxy handlers with the app's existing runner client, renders `ChatView` with the app's
theme, and registers its own cards (events, outreach items). The next app follows with cards of
its own.

## Testing

- Package: unit tests for the model (event grouping, versions, the card fence parser, the
  attachment note), component tests for `ChatView` with a fake transport (every event kind,
  attachments, previews, cards registered and not), transport tests against a stub runner (for
  `BlergTransport`) and a stub proxy (`ProxyTransport`), theme test (every component uses only
  `--chat-*` tokens: a lint rule, not a snapshot).
- Runner: its existing suites, unchanged in intent, now exercising the package.
- Go: the tarball route (content type, immutability, 404 for an unknown version), the
  manifest field, the proxy handler set (a Go test with a stub runner: one session's traffic,
  auth is the app's).

## Not in scope

The sidebar and session list; the board; a design system beyond the chat; an `app_card` event
(designed, not built); publishing to npm (designed, not built); any change to the agent contract
beyond A, B and the additive `ui` field.

## Review amendments (adversarial review, 2026-10-06)

1. The agent token could not touch a session's files: contract addition A (approved).
2. The contract's event stream had no typing and no status: contract addition B (approved).
3. The transport interface carries the paging metadata, server clock, session metadata,
   `send`'s connection result and the optional model switch the view depends on.
4. `BlergTransport` takes `getToken` and an injected socket; it is for the runner's own origin.
5. The runner migration is a rewrite of the view body on `useSession`, not a file move.
6. Cards: intercept at `code`; strict `JSON.parse`; unclosed fences stay code while streaming;
   card components render text only.
7. Distribution: a dedicated immutable route with `sha512` in the manifest; the Docker build
   context and lockfile change is listed; the shared contract type and `openapi.json` change.
8. The token contract gained success/warning/info and a type scale; the lint rule enforces it.
9. Corrections: the message body is `{text}` with the attachment note composed client-side; the
   event kinds are every kind in `types.ts`; the manifest field is in the shared contract.
