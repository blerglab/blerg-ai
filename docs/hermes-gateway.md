# Hermes agent sessions

Hermes agent sessions use its native TUI gateway over private stdio, with one gateway process and one native conversation per Blerg session. Terminal sessions still launch Hermes's interactive CLI. The adapter supports native desktop protocol contracts 6 and 8; the sandbox image pins Hermes v0.21.5 (`v2026.9.24`). An incompatible gateway fails explicitly instead of silently falling back to a final-text-only CLI.

| Behavior | Adapter behavior |
| --- | --- |
| Conversation and steering | Streams native text and tool events; acknowledges a person's message only after native acceptance, preserving its client ID. Native queued follow-ups remain owned by Hermes. |
| Questions and approvals | Bridges native clarification requests for both protocol versions. Like Claude and Codex agent sessions, Hermes runs without permission prompts, and its capability report says so: the gateway starts in Hermes's own yolo mode (`HERMES_YOLO_MODE`), which still keeps Hermes's hardline blocks and the operator's `approvals.deny` rules. Hermes still asks before some actions yolo does not cover, such as writes to protected instruction files. Such an approval is asked in the chat with the command and Hermes's reason shown verbatim in code blocks, followed by only the choices Hermes offered (once, session, always, deny). Only the session's owner, signed in as themselves, can answer it, in Runner: the daemon token, board tokens, agent tokens and other people who can see the session cannot. The board's chat relays messages with the board's own automation token, so a reply typed there reaches the agent as an ordinary message and leaves the approval open; answer it from the session in Runner. When Hermes withdraws an approval or the turn ends first, the daemon closes the card with an empty answer, which is a deny. The answer must be exactly one offered choice, by word, number or full label; any other reply is a deny. An approval that offers no choice Blerg recognizes, or that cannot be asked (it is over the question size limit, too many questions are open, or the session cannot ask questions), is denied without asking. Choosing `always` lets Hermes save the command pattern to the active Hermes profile's `command_allowlist`; on the host that is the person's own `~/.hermes/config.yaml`, so it also affects Hermes outside Blerg. Sudo, secret and vault requests are refused. |
| Work visibility | Native reasoning, tool lifecycle, plan snapshots and child lifecycle/model/counters. Child prose is not mirrored into the parent stream. Background process inventory uses native session-scoped `process.list`; closing a terminal tab does not imply process exit. |
| Models and capabilities | Reports actual native model, effort, tools and skills. Live MCP state uses the shared tracker and first-prompt gate; on-demand servers stay explicitly on demand rather than appearing connected. Model changes use explicit session scope. A native confirmation requirement is reported without approving it or changing global configuration. |
| Usage | Converts native cumulative token counters into turn deltas. Missing exact cache counts remain unavailable; rounded cache-hit percentages are not converted into fabricated token counts. Child usage stays attributed to its reported model. |
| Host recovery | Stores a versioned engine-bound native conversation ID in the private recovery record. Resumes that exact identity, including native compression updates; missing or mismatched identities fail without replacing history. Modern recovery admission happens before native resume. |
| Runtime | The same gateway adapter executes on the host, inside the existing session sandbox, or in the cluster runner image. In-container cancellation kills the owned gateway process group before permitting replacement; unverified shutdown disables automatic restart. |

Two small process-local compatibility hooks append Blerg's trusted session guide to the original native system prompt and disable Hermes's automatic crash-marker continuation. Blerg owns recovery admission and safe continuation, including human-message precedence and interrupted-question handling. These hooks validate the native API signatures before starting. The bootstrap also sets `HERMES_YOLO_MODE` for the gateway and every process it starts, including the terminal tool's shell, so a `hermes` command the agent runs there inherits yolo mode too. It is set in the bootstrap because a sandboxed gateway does not inherit the daemon's environment. Blerg itself does not modify Hermes source files, profile configuration or tool permissions; only a person's `always` answer to an approval makes Hermes write to its profile.

A host installation needs the native Hermes source and its Python environment in the standard profile layout, or explicit `HERMES_PYTHON_SRC_ROOT` and `HERMES_PYTHON` launch settings. The Python bootstrap excludes workspace and `PYTHONPATH` import shadowing. Source and interpreter overrides are trusted daemon launch settings, not user chat instructions. Gateway stderr and upstream RPC error bodies are not copied into chat.

Hermes restricted file/MCP sessions, scheduled restricted jobs, managed plugin installation, exact cache-token counts, native child-prose streaming, and retained history after a cluster pod's native profile is lost are not advertised by this adapter. A Docker sandbox container removed during host recovery also loses its private Hermes history. Existing generic transcript handoff is bounded context, not a claim to native history recovery. These limits require separate engine-specific work before their capability gates can open.

## Named OpenAI-compatible providers

Hermes agent sessions can discover credential-free OpenAI-compatible model servers from the operator's native `providers` mapping or legacy `custom_providers` list. Discovery runs through the shared asynchronous model catalog: bounded concurrent requests, a three-second overall budget, no credentials, proxies or redirects, and bounded JSON responses. Cached declared model lists are not presented as live availability.

Keep the existing default and give that endpoint a named entry when adding another endpoint. For example, in the operator-managed Hermes `config.yaml`:

```yaml
model:
  provider: custom
  default: example-model-a
  base_url: http://127.0.0.1:8000/v1
  context_length: 65536
providers:
  local-a:
    base_url: http://127.0.0.1:8000/v1
    api_mode: chat_completions
    context_length: 65536
  local-b:
    base_url: http://127.0.0.1:8001/v1
    api_mode: chat_completions
    context_length: 65536
```

Use addresses reachable from the selected runtime; loopback inside Docker refers to that container. The default endpoint's raw model IDs stay compatible. Other entries use `custom:local-b/<server-model-id>`; identical IDs on different endpoints remain distinct. Equivalent default/named aliases are deduplicated, preferring the raw default and modern provider identities over legacy aliases. Different endpoint policies stay separate. Selector IDs must fit the existing 128-character model limit; they are never truncated. Provider names are bounded lowercase letters, digits, underscores and hyphens. Endpoint URLs are trusted configuration only, never accepted as a model selector or child-launch override.

Agent sessions pass the raw server model and explicit native provider separately, use session-only switches, and require the built native session to confirm that binding before sending input. A rejected or confirmation-requiring switch does not send queued input to the previous model. Offline or removed named providers fail explicitly. Active automatic fallback chains, YAML aliases/merges, duplicate keys, conflicting provider identities, dynamic URLs and unsupported routing syntax refuse qualified selection instead of guessing a fallback. Credential-bearing providers are not discovered by this credential-free adapter.

Private native recovery metadata records an opaque SHA256 intended endpoint/provider/entry-policy checksum, not an endpoint or credential. Dynamic per-model discovery/capability metadata is not frozen by this checksum. Matching routes resume native history without a redundant model switch; a changed or missing qualified route checksum refuses recovery without replacing history. Bound identities use private recovery version 2, which older adapters refuse; version 1 raw legacy recovery remains readable. Named-provider terminal selections are explicitly refused: use **agent mode** for this routing contract. Raw interactive CLI selections retain their existing behavior.

Hermes is reported as local for budgeting and local-only rules only when every configured selectable target is demonstrably local and credential-free. Remote providers, native credential-pool authority, provider keys, automatic fallbacks, and explicit delegation/auxiliary routing overrides withhold that engine-wide claim. Empty native auth metadata and empty/default auxiliary blocks do not. This conservative label does not prevent an owner from explicitly approving a child that uses a known local model.

Optional native integration checks use an isolated temporary profile and a loopback mock provider, so they need no provider credential or GPU. Supply both trusted installation paths; absent settings skip only this opt-in check:

```sh
cd runner
GOWORK=off BLERG_TEST_HERMES_NATIVE_ROOT=/path/to/hermes-agent \
  BLERG_TEST_HERMES_NATIVE_PYTHON=/path/to/hermes-agent/venv/bin/python \
  go test -race ./internal/daemon -run '^TestHermesReal(NativeOfflineAdapter|NamedProviderRouting)$'
```
