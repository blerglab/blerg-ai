# Answer-back pages (BLG-336)

An agent publishes a self-contained HTML file with `blerg-runner publish page.html
--answer-schema result.schema.json --answer-ask "Rank these options"`. Publishing
creates a request pinned to that exact artifact version. It appears in Needs you
while pending. Ordinary published HTML and workspace previews cannot answer back.
MCP UI resources can use this contract after they have been published as an artifact;
this change does not add an MCP resource renderer.

The page declares its current result with
`parent.postMessage({type: 'blerg:answer', result: {order: ['a', 'b']}}, '*')`.
Only the matching iframe window's messages count. No token, session ID, schema,
or other parent data is sent into the page. Clicking Review result freezes the latest
proposed JSON as an escaped-text snapshot outside the page; the owner then clicks
Submit in trusted chrome. Page updates cannot change that
snapshot. Only the signed-in session owner can submit. Other viewers can view the
page but cannot answer. Success closes the request; reopening or retrying cannot
create a second answer. A new version permanently closes an older pending request, even if the newer file
is later deleted.
A deleted or ended session, missing artifact, or obsolete request cannot submit.

## Limits and schema

The result is JSON, at most 16 KiB in UTF-8, with nesting at most 16 levels.
Numbers use browser JSON precision (IEEE 754 doubles).
Schemas are at most 8 KiB and 8 levels deep. The supported JSON Schema subset is
`type` (object, array, string, number, integer, boolean, null), `properties`,
`required`, `additionalProperties` (false only), `items`, `enum`, `minItems`,
`maxItems`, `minLength`, `maxLength`, `minimum`, and `maximum`. A type is required
at every schema node; objects require `additionalProperties: false`, arrays
require `items`. Unknown keywords, references, regexes, and remote schemas are
rejected. Limits must be nonnegative integers (except numeric bounds). Schema
validation runs on the server at creation and submission; unsupported schemas
fail closed. Invalid results remain editable and do not consume the request.

## Threat model

The HTML, schema, and result are agent-controlled, untrusted input. A hostile page
may forge messages, flood the viewer, change a result after preview, include
instruction-like strings, navigate itself, or try to steal credentials. A hostile
viewer may call the HTTP API directly, replay a request, race another tab, or
submit against another workspace. The browser-to-server request is a trust boundary:
client validation and iframe origin alone are insufficient authorization.

The iframe keeps exactly today's `sandbox="allow-scripts"`, opaque origin,
no-referrer policy, and deny-network CSP. No forms, popups, same-origin access,
top navigation, credentials, or network permissions are added. The parent accepts
only bounded JSON messages from its exact iframe window and never interprets
result strings as HTML, code, URLs, or commands. The server independently checks
human ownership, artifact/session/version binding, schema, payload limits, and
one-time state under a database lock. The page cannot trigger submission; trusted
chrome requires an explicit review and submit. Navigating/reloading the frame
clears the proposed result and preview.

The delivered message identifies the artifact and request and labels the JSON as
untrusted page data, not instructions or approval. Schema validity does not make
it trustworthy. No result can grant permission or stand in for Blerg's approval
or review APIs. A deterministic delivery ID makes retries idempotent on receipt-
capable daemons; legacy daemons are refused for this flow. Delivery errors leave
the accepted result available for retry, without allowing its contents to change.

Residual risks: a page can deceive the person about what its controls mean, consume
CPU/memory inside browser sandbox limits, and send malicious prose within otherwise
valid data. The trusted JSON preview and explicit data label reduce confusion but
do not establish semantic safety. Browser resource exhaustion is not solved here.

## HTTP contract

- `POST /api/sessions/{id}/answer-requests`: the session's messaging token;
  `{artifact: "<exact artifact id>", ask: "...", schema: {...}}`. Returns the
  request pinned to the version (201); a retry returns that version's existing
  declaration without changing it. The HTML upload happens first; if declaration
  fails, the file remains published as ordinary HTML. Retry this endpoint with
  the uploaded artifact ID to recover without publishing another version.
- `GET /api/sessions/{id}/artifacts/{aid}/answer`: owner browser bearer token;
  the declaration, saved result if any, and `delivered`. No session credential
  is returned. A file with no declaration returns 404.
- `POST /api/sessions/{id}/artifacts/{aid}/answer`: owner browser bearer token;
  the raw JSON result. Returns `{delivered: true}` after durable relay acceptance.
  Invalid data returns 422; obsolete/changed answers return 409. The saved value
  is immutable even if delivery fails; an identical retry resumes delivery.

The normal workspace live response contains pending `answer_requests` for its
owner. New versions and file deletion remove obsolete requests from that list;
submitting checks the latest stored version again under the artifact lock.
