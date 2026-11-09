# Internal inference hubs (BLG-255)

Status: authenticated owner validation pending. No new environment forwarding,
Claude model syntax or account/admin gateway settings are enabled by this change.
Estimate: M for investigation, medium risk, medium complexity. Implementation of
credential settings needs a separate estimate after scope approval. Board fields
could not be updated because this session has no board write access.

## What the repository establishes

| Path | Evidence and limit |
| --- | --- |
| Hermes host | Native provider configuration is the existing integration path. This does not establish compatibility with a particular company's authentication. |
| Hermes Docker | [sandboxhome.go](../../runner/internal/daemon/sandboxhome.go) mounts the selected engine's `config.yaml` and `.env` read-only. Environment present only in the daemon shell is not equivalent to configuration in those files. Restricted sessions intentionally do not inherit the personal profile. |
| Hermes cluster | [hermesconfig.go](../../runner/internal/runner/hermesconfig.go) merges the shared provider fragment into the pod profile. See [shared provider installation](../../install/k8s/CLUSTER-RUNTIME.md#shared-hermes-providers) for configuration, Secret delivery and egress policy. A successful merge is not an authenticated inference test. |
| Model selection | [validate.go](../../runner/internal/models/validate.go) accepts slash-containing Hermes/Codex IDs under the generic 128-character rule. Claude uses a separate lowercase 64-character rule without slashes. Broadening that rule alone would not configure a gateway. |
| Hermes discovery | [Named provider routing](../hermes-gateway.md#named-openai-compatible-providers) discovers credential-free providers only. Authenticated providers are deliberately excluded; an empty picker is not proof that native authenticated inference fails. |
| Claude/Codex sandbox and pod | No general promise to copy arbitrary daemon gateway environment into a runtime. A host engine's inherited environment does not establish sandbox/pod parity. |

The safest reversible 0.6.0 proposal is to retain Hermes's native configuration
path and defer managed Claude/Codex gateway support until owner evidence and a
credential policy exist. This is an agent recommendation, not owner acceptance
or a claim that authenticated Hermes hubs have passed validation.

## Owner test receipt

Run only on an authorized work environment, using a disposable profile, a
least-privilege short-lived credential and a harmless prompt. Configure the native
provider using the hub operator's instructions. Do not paste credentials or private
endpoint names into Blerg chat, tickets, screenshots or committed receipts.

For each intended runtime (host, Docker, cluster), record separately:

1. Release/commit, engine version, runtime and redacted provider type. Confirm
   baseline authenticated inference directly through the native engine first.
2. Start a Blerg agent session with that runtime and explicit model/provider.
   Record whether its first response and reported model match the intended hub.
   Have the hub operator confirm the request used the intended identity and route;
   retain private references rather than request bodies or headers.
3. Exercise a slash-containing model ID, a second turn, a session-scoped model
   switch if supported, and restart/resume. Confirm there is no silent fallback to
   a public provider and no global configuration change from a session switch.
4. Remove or revoke the test credential. A new request must fail without routing
   elsewhere or disclosing credentials in chat, errors or retained artifacts.
   Confirm profile/configuration handling remains read-only where promised.
5. Check DNS, TLS trust and egress from the actual runtime. A host VPN connection
   need not supply Docker or pod split DNS or routes. Container loopback names the
   container; pod loopback names the pod. Use operator-approved routing and CA
   configuration. Do not disable TLS verification or widen egress to make a test
   appear successful. Report network failures separately from authentication.
6. Delete the disposable profile and revoke its credential. Record pass/fail/not
   tested for every step and runtime, with private evidence references and date.

No authenticated company hub was available in this session. All owner-test rows
remain **not tested**. Existing mock and credential-free tests cannot fill them.
A host-only pass cannot authorize a Docker or cluster support claim.

## Gate for managed Claude/Codex support

If the owner chooses this scope, first specify per-engine allowlisted settings:
base URL, credential reference, supported authentication/headers and model ID
syntax. Confirm the installed engines' native contracts rather than treating
arbitrary environment variables as a portable protocol. New settings must define
account versus install precedence, who may select each destination, and whether
restricted sessions may use it. Never inherit all host environment variables.

Resolve credentials only at launch into the intended runtime, with encrypted
transport and restricted storage/access. Specify rotation, revocation, cleanup,
recovery and Secret ownership. Prevent credentials from appearing in argv, pod
annotations, events, launch API responses, logs and error bodies. Gateway URLs
need their own privacy policy and destination authorization; a model selector
must not become a URL or secret-delivery override. Preserve authentication headers
exactly only within the reviewed engine-specific mapping.

Write failing public launch/transport tests before implementation for selection,
precedence, allowed model IDs, forbidden settings, secret isolation, redaction,
revocation, all runtime launch paths and restricted-session refusal. Use a mock
hub to verify actual authenticated inference and failure without fallback, then
retain authorized owner evidence for the real hub. Run touched Go tests with race,
lint and builds; settings UI also requires unit/browser tests, typecheck and build.

Open decisions: authenticated Hermes result by runtime; whether credential-free
catalog exclusion needs a separate authenticated catalog design; which engines
and authentication mechanisms are release requirements; account versus admin
ownership. This document leaves BLG-255's owner acceptance and product decision
open rather than inventing an authenticated result.
