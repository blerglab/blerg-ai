# Live preview trust model (#263)

## Assets and boundaries

Published bytes and workspace files are untrusted agent output. The authenticated app,
its cookies, storage, session APIs, workspace secrets, and the person's network are
privileged. A preview must never inherit those privileges. Publishing a replacement
is not permission to broaden its sandbox or open a development server.

HTML continues through the existing opaque-origin iframe with only `allow-scripts`
and the existing restrictive CSP: no network, same origin, forms, popups, top
navigation, or app credentials. Chart renderers retain their existing validation and
network restrictions. Parent messages from preview scripts are untrusted and must
not control tabs, authorize access, or report authoritative viewer state.

## Safe implementation scope

Published file tabs follow by default (BLG-436), using only the same name, origin
and view type within the current session, the authenticated artifact inventory and
immutable artifact IDs. Pin stops updates. The browser remembers the person’s
follow/pin choice per session and file across tab closure and reload; this state is
not disclosed to agents. A pinned viewer shows a newer-version indicator from live
artifact events. Review views retain their immutable reviewed version.
Failed or stale reads must not replace the current preview. Rapid publications are
coalesced before reading. No arbitrary URL, proxy, server launch, port forwarding,
filesystem watcher, or new server API is introduced. Workspace watching is an explicit
action after a supported file has been opened. It polls only the existing authenticated,
confined workspace-file API, sequentially after each read completes; stopping or closing
aborts the read. Existing root, path and symlink checks remain authoritative.
Workspace watching starts without polling and remains limited to HTML and named
chart formats; changes to other types or oversized bytes fail closed and retain the
previous preview. Published files retain their existing preview type and size rules.

A successful byte read does not prove that arbitrary HTML scripts ran correctly.
Charts are drawn off-screen through the existing restricted renderer before replacement.
Runtime script errors and browser termination cannot safely be inferred from messages
sent by an untrusted frame. The initial implementation reports read and chart preflight
failures, not script correctness, and does not promise scroll restoration inside opaque frames.

## Product decisions needed before expanding scope

- Workspace scope expansion: whether additional roots or runtime capabilities may
  be watched. Current watches use only the existing workspace-file read contract and
  require an explicit Watch file action after opening a supported preview.
- Developer servers: whether they are supported at all. If yes, define origin isolation,
  allowed endpoints, authentication, network policy and consent before adding a proxy.
  The current implementation exposes none.
- Viewer disclosure: which agents may learn the person's visible artifact ID/version,
  whether hidden split panes count, retention, and whether consent is required.
  No viewer state is sent in this implementation.
- Reload semantics: define what counts as a good render for HTML and charts, timeout
  behavior, and whether scroll preservation warrants a narrowly scoped frame bridge.
  No sandbox privilege is added for scroll access.

## Required verification

Exercise default follow, remembered pinning, live publication indicators,
same-name/origin isolation, failed reads, stale results,
workspace type and size changes, and tab cleanup. Keep the existing real-browser sandbox tests. Check controls at
390 and 3840 pixels. These checks do not constitute approval of additional workspace or server access.
