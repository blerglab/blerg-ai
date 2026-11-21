# End-to-end encrypted shells (BLG-296)

Status: exploration, not approved. Written against the #269 baseline in
[Persistent owner shells](persistent-shells.md) and the keeper broker in
[Shell keeper](shell-keeper.md). Review findings and open decisions are at the
end, and the findings take precedence; none of this authorizes implementation.

## Today's exposure

Browser-to-server and server-to-daemon shell traffic is TLS on every hop, but
the runner server itself handles it in clear: it terminates the browser
WebSocket, re-encodes keystrokes and output as JSON RPC frames to the
daemon, and holds up to 256 KiB of live output per shell in an in-memory
ring for reattach replay (`runner/internal/ownershell/scrollback.go`). None
of that content is persisted to the database, logs, or History — the audit
trail is metadata only (actor, session, open/close/takeover events, no
bytes: `runner/internal/ownershell/manager.go`). The problem is not logging
or storage; it is that the server process, and anyone with the authority to
read its memory, sees every byte in clear while a shell is live. On an
enterprise cluster, that includes cluster/server root, not just the
product's own code.

## What this can and cannot stop

**It can stop:** a cluster operator (or anyone with read access to the
runner server's host or process memory) passively observing shell content —
a sudo password typed at a prompt, secrets scrolled past in output — without
modifying anything the operator serves. If the server only ever relays
ciphertext it cannot decrypt, passive memory inspection, accidental
logging, and a future bug that writes shell bytes somewhere it shouldn't no
longer expose content, because there is nothing to expose.

**It cannot stop:** the same server deciding to stop being honest. The
runner server **serves the browser the JavaScript that would do this
encryption.** An operator with root on that server can edit the served
script to skip encryption, exfiltrate the session key, or copy plaintext
before it is encrypted — the browser has no way to tell a tampered build
from a real one, because it has no code of its own to check with. This is
the standard "the server ships the client" problem: browser-side crypto
defends against a server that merely *relays*, not one that is willing to
actively serve a different client to a target person. **Answering the
card's question directly: yes, browser-side keys help against passive
observation and incidental exposure; no, they do not help against a
genuinely hostile server operator, because that operator controls the code
the browser runs.** A separately trusted client (next section) is the only
way to close that gap.

## Key agreement, if we build it

There is no existing login-bound or desktop-bound public-key material to
reuse. The repo has no WebAuthn. The daemon's enrollment credential
(`runner/internal/daemon/desktopenrollment.go`) is a bearer token, not a
keypair; the only asymmetric keys are the core's and the runner's own
ed25519 signing keys for authorization grants (`core/internal/signing`,
`runner/internal/ownershell/grant.go`) — service-side signatures, not
end-to-end key agreement. A real implementation starts from nothing: a
persistent per-desktop keypair at daemon enrollment (parallel to the
identity key the keeper design already proposes for daemon↔keeper auth),
and a per-login key the browser holds only for the session, since browsers
have no durable private key storage without WebAuthn or a platform
credential API.

Proposed shape, scoped to what the threat model above actually buys:

- At shell open/attach, the browser and daemon run a key agreement (e.g. a
  Noise handshake) over the existing authenticated channels, producing a
  session key known only to those two endpoints. The server relays the
  handshake messages like any other ciphertext.
- The daemon's long-term key is pinned to its enrollment identity (the same
  `DesktopID`/credential generation persistent-shells.md already binds
  custody to); the browser's side is pinned to the person's login for that
  session, not a durable identity, since nothing today gives a browser a
  durable one.
- Consent UX: the person must be able to see, once, a short fingerprint of
  the daemon's long-term key the first time they connect to a given
  desktop, the same trust-on-first-use model SSH uses — this is a real new
  screen, not implicit.
- Every reattach, adoption, or takeover (persistent-shells.md's access
  epochs) mints a fresh session key. An old key must not decrypt a new
  epoch's traffic, matching that design's existing rule that an old access
  epoch gets no implicit authority.

## Multiple tabs, multiple devices, takeover

Today exactly one attachment holds a shell's live stream; a new attach
forcibly takes over and closes the old one
(`runner/internal/ownershell/manager.go:613-624`). That composes cleanly
with per-attach session keys: each attach negotiates its own key, and
takeover means the daemon starts encrypting to the new key and drops the
old one, with no new sharing primitive required. Multiple simultaneous
devices reading one shell is not supported today, and this design does not
add it; that would be a new fan-out decision, not an encryption one.

## What survives reconnect and replay

Scrollback must move off the server. The daemon (or, once #271 ships, the
keeper) already holds the plaintext stream — it is the real relay endpoint
for the PTY — so it is the natural place for the replay ring to live,
encrypted or not. Concretely: the daemon keeps the existing bounded ring,
but on every reattach it must re-encrypt the retained bytes to the new
attachment's freshly agreed session key before replay, because the ring
itself cannot usefully be stored pre-encrypted to a key that no longer
exists once the epoch turns over. That means the daemon is not a passive
ciphertext store between attaches: it necessarily holds plaintext in memory
and re-terminates encryption at every attach boundary. This is an important
caveat on the card's framing of "browser<->daemon end-to-end": the daemon
is a real cryptographic endpoint, not a relay, and it must remain one for
replay to work at all.

## What breaks or changes

- **Server-side scrollback** (`runner/internal/ownershell/scrollback.go`)
  goes away in its current form; the ring moves to the daemon side of the
  persistent-shells.md custody split, same size (256 KiB). New surface on
  the daemon, not a deletion of a safeguard.
- **Audit** does not change: it is already metadata only
  (`shellAuditLog.Record`, `runner/internal/server/shell.go:277-283`); say
  so in any announcement, since "encrypted shells" invites the wrong
  assumption that audit was capturing content before.
- **Agent shell sharing** — a feature letting an agent or second person read
  a shell's output through the server stops working once the server cannot
  decrypt it. None is known in-tree; check before scoping implementation.
- **Debugging and support** — an operator who today inspects a stuck
  shell's live buffer loses that; diagnosis becomes daemon-side only,
  consistent with the threat model but worth naming up front.

## Cost

New per-desktop keypair generation and storage at enrollment; a new
handshake protocol on the existing authenticated channels, with its own
versioning and capability negotiation (the pattern persistent-shells.md
already uses for its grant envelope version); a new consent/fingerprint
screen; the daemon-side ring rewrite and re-encryption on every reattach;
and a security review at the same bar #269/#271 got, because a flawed
key-pinning or epoch-key scheme is worse than no encryption — it would look
protected while leaking. None of this reuses the daemon's current
enrollment credential; the keypair work is new.

## Options

1. **Ship browser↔daemon session encryption as scoped above.** Stops
   passive operator observation and incidental exposure; explicitly does
   not stop a hostile server operator, and the announcement must say so
   plainly rather than imply "end-to-end" means what it usually means to a
   security-conscious reader. Moderate cost (new keys, new handshake, new
   daemon-side ring, new consent UX, one security review).
2. **Do not build server-relayed E2E; invest instead in a separately
   trusted client** (a signed desktop app, or a browser extension with a
   pinned update channel the person installs once and verifies out of
   band) that talks to the daemon directly and never runs server-served
   code for the crypto path. This is the only option that stops a hostile
   server operator, because the client code is no longer something that
   operator can silently swap. Materially higher cost: a second shipped
   client, its own update/signing infrastructure, and loss of "just open a
   URL" for encrypted sessions.
3. **Do nothing; document the gap and rely on today's mitigations** (TLS
   per hop, in-memory-only scrollback, metadata-only audit) as the
   accepted residual risk for this threat.

## Recommendation

Option 1, labeled honestly. It is a real, bounded improvement for the
threat the card names — a cluster operator reading server memory, not one
editing served code — and proportionate given the daemon-side work
#269/#271 already did. It must not be marketed as closing the "hostile
server operator" case; if the owner wants that closed, option 2 is the
only path, and it is a separate, larger decision to make explicitly rather
than inherit from this ticket's title.

## Slices, if option 1 proceeds

1. Daemon-side keypair generation and persistence at enrollment; capability
   bit and version negotiation; identity and fingerprint only, no encryption
   or ring changes yet.
2. Session key agreement at attach/reattach; server relays opaque handshake
   and payload frames unchanged; daemon/browser each decrypt their own PTY
   side. The existing server-side ring still works, now holding ciphertext
   it cannot read — already most of the value.
3. Move the replay ring to the daemon, with per-reattach re-encryption; only
   now does the server stop seeing even the replay buffer.
4. Consent/fingerprint UX, multi-desktop handling, and the security review.

## Open decisions for the owner

- Is option 1's honest-but-partial protection worth building for 0.6.0, or
  is the real ask option 2 (a separately trusted client), which is a
  different and larger project?
- Should the fingerprint/trust-on-first-use screen block the first connect
  per desktop, or only warn — and who is the person expected to verify it
  against (an admin-distributed value, or genuinely nothing, same as SSH's
  usual practice)?
- Does any existing or planned feature let a second person or an agent read
  a live shell through the server? If so, this design needs that scope
  before slice 2, since it changes who the daemon must be able to encrypt
  to.
- Does this ship before or after #271 (keeper)? The ring and plaintext
  termination point move again if the keeper, not the daemon, ends up
  owning the PTY — better to decide the owner (daemon vs. keeper) once,
  not twice.

## Review findings to settle before any slice

Codex reviewed this design at a98874d0. The narrow conclusion stands: encryption
reduces passive exposure at the server. These findings correct the rest, and
they take precedence over the text above.

- **A trusted client alone doesn't close the hostile-server case.** The daemon
  obeys the server: spawn, input and file-read handlers
  (`daemon/manager.go:247`) and the grant-verification key arriving through
  server configuration (`shellruntime/service.go:636`). A hostile server can
  leave the client alone and use that authority. Option 2 also needs
  endpoint-local authorization and isolation from server-controlled workloads.
- **"Pinned" has no defined mechanism yet.** A desktop ID or a login asserted by
  the relay authenticates nothing. Define pin storage, first-use verification,
  key-change blocking, enrollment rotation, cross-device verification and how the
  browser authenticates to the daemon. Browsers can hold durable non-exportable
  keys without WebAuthn; the earlier claim otherwise is wrong.
- **Replay and downgrade aren't specified.** Require authenticated context and
  counters, nonce uniqueness, transcript identity and a fail-closed minimum policy
  at the endpoints. Existing sequence checks trust server-provided fields.
- **The slice order breaks replay.** Scrollback truncates and the server splits
  replay at arbitrary byte counts (`scrollback.go`, `server/shell.go:934`), which
  ciphertext records can't survive, and a new attachment lacks the old key.
  Pinning and a security review come before any release of encryption.
- **Cluster root can own the endpoint too.** The benefit holds only for an
  endpoint trusted independently of the operator. Timing, size and control
  metadata stay visible either way.
