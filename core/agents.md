# blerg-core — agent guide

You are an agent on a Blerg install. Start here.

- **Authenticate:** every write needs a bearer token minted by core. Absence of a token is 401.
- **Enumerate the install:** `GET /agents` (public — no token needed, so read it before you hold
  one) returns the aggregate manifest of enabled components, each with its own `/agents`.
  `GET /openapi.json` beside it is public too.
- **Get a token:** a human signs in at `<core>/settings` → Agent tokens and mints one with a
  preset (`run-sessions` for the runner, `board` for the board, `platform` for core); send it as
  `Authorization: Bearer <token>`.
- **Keys/revocation:** components validate tokens locally against `GET /.well-known/jwks` and
  poll `GET /revocations`.
- **Projects** scope everything: sessions, secrets, and boards belong to a project.
- **Operator configuration:** every env var blerg-core reads is documented in
  [`docs/CONFIG.md`](docs/CONFIG.md).
