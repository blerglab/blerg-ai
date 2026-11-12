# Host login refresh for sandbox sessions

Local sandboxes still receive only an access-token copy. Refresh tokens are replaced with an
inert placeholder, and no sandbox login file is read back or written into the host's login.

For a live Codex sandbox, the daemon's background copy loop asks the installed host Codex
app-server to renew a host login when its access token expires within two minutes. It uses
`account/read` with `refreshToken: true`, after the native initialization handshake. Codex
owns the refresh operation and its normal host credential storage. The daemon starts no
thread, prompt, inference request, or tool. It runs from a new empty host directory and
discards all native diagnostics. Failures log only a daemon-owned engine identifier and an
allowlisted cause code (source, temporary_directory, stdin, stdout, start, write, protocol,
read, account, timeout, or unavailable), never native error text, output, tokens, or paths.
The retry backoff bounds these diagnostics to one per engine and source per minute.
A refresh takes at most 20 seconds, is serialized across sessions, and failures retry no more
than once a minute. The normal 15-second copy loop
then distributes the host's current access token. No live sandbox means no renewal attempt;
API keys and tokens without a known expiry are left alone.

The [official Codex app-server contract](https://developers.openai.com/codex/app-server)
documents forced renewal through `account/read` for a managed ChatGPT login. It does not
apply to externally managed tokens. The installed CLI's generated schema also exposes the
boolean `refreshToken` field. An older CLI that rejects the request fails closed: the existing
expired-sign-in notice remains, and the person can renew their login on the host.

The [Claude authentication guide](https://code.claude.com/docs/en/authentication) and installed
`claude auth --help` expose login, logout, and status, without a documented refresh-only
operation. Automatic Claude renewal is deliberately left out: starting a prompt merely to
trigger renewal could run tools or spend inference quota. Claude retains the existing manual
refresh notice. Adding a supported native Claude refresh-only mechanism remains open.

Validation uses synthetic host logins and fake native stdio peers. It checks forced refresh,
serialization, retry limits, ignored sandbox credentials, access-only redistribution, sanitized
errors, bounded output, timeout, and process cleanup. It does not rotate a person's real login
or claim a live provider acceptance test.

Native refresh runs in a separate serial background loop. Its 20-second timeout
cannot delay the ordinary login-copy loop or copies for another engine.

## Exact boundary

Host to sandbox: the existing allowlisted login fields only. Claude gets its current access
token, expiry, scopes, subscription type, and rate-limit tier; Codex gets its current access token,
ID token, account ID, auth mode, and synthetic current last-refresh timestamp (or its existing
API-key login). Both OAuth copies use the inert refresh-token placeholder. No new secret,
refresh credential, host diagnostic, configuration, or native process handle crosses.

Sandbox to host: no credential content. Registered sandbox IDs and the existence of real
engine directories establish demand only. Renewal reads the trusted host login and runs the
installed host binary; the sandbox cannot supply refresh arguments or credentials. Existing
copy, symlink, mount, and no-write-back checks remain unchanged.
