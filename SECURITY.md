# Security policy

Blerg runs coding agents that execute code, hold engine and git credentials, and can
reach your repositories. Please treat anything that weakens those boundaries as a
security issue, not an ordinary bug.

## Reporting a vulnerability

**Do not open a public issue.** Use GitHub's private reporting instead: on this
repository, open **Security → Report a vulnerability**. Only the maintainers can see
the report.

Please include:

- what is affected (core, board, runner, the daemon, an installer) and the commit or
  version you tested;
- how to reproduce it, as concretely as you can;
- what an attacker gains.

Never include a real credential, token or private key in a report. If the issue is
that one is exposed, say where, not what it is.

You can expect an acknowledgement within a week. Blerg is maintained on a best-effort
basis, so fixes are prioritised by severity; we will keep you informed and credit you
in the fix unless you ask us not to.

## Supported versions

Blerg is pre-1.0. Only the current `main` receives security fixes.

## What is in scope

- Bypassing authentication or a capability check in core, the board or the runner.
- Reading or using another account's stored credential.
- A session escaping its sandbox container or cluster pod.
- A credential, token or secret reaching a log, an API response, an event or a file it
  should not.
- An installer default that exposes a service or a secret.

## What is not a vulnerability

These are documented properties of the design. They are worth knowing before you
deploy, but they are not bugs:

- A session launched on **This machine, unsandboxed** runs as the daemon's user with
  that user's full access. It is never a default, and the launch sheet requires an
  acknowledgement before it will start one (a caller of the runner's API with a valid token is
  not asked).
- Every account on one install can launch sessions on any connected daemon. One core
  per developer machine is the supported desktop shape.
- Anyone who can write cards on a board can spend the credential behind that board's
  automation token, and could get an agent to reveal it.
- The Local sandbox mounts your engine logins so the engine can use them; it does not
  protect those credentials from the code the agent runs.
- Membership of the `docker` group is equivalent to root on that machine.

## Running Blerg safely

- Keep the desktop stack bound to `127.0.0.1` (the default) unless you have put
  authentication and TLS in front of it.
- Back up `BLERG_CORE_LOCAL_KEY`. Losing it loses every stored credential; leaking it
  exposes them.
- Prefer the Local sandbox or the cluster runtime over unsandboxed sessions.
- Only point a session at code you would be willing to run yourself.
- Cluster session pods are hardened by default: no service-account token, non-root,
  no privilege escalation, all capabilities dropped, the runtime's default seccomp
  profile. They can still reach in-cluster services (Postgres, core, the board) over the pod
  network unless you apply the optional
  [`install/k8s/networkpolicy-sessions.example.yaml`](install/k8s/networkpolicy-sessions.example.yaml),
  which needs a CNI that enforces NetworkPolicy; see
  [`install/k8s/CLUSTER-RUNTIME.md`](install/k8s/CLUSTER-RUNTIME.md).
- Always-on plugins run third-party code in every cluster session you start. Only a signed-in
  human can change the list (an agent token cannot), and only marketplaces the operator allows
  (`BLERG_CORE_PLUGIN_MARKETPLACES`, default: the official Anthropic marketplace) can be used;
  add plugins you trust, and widen the allow-list deliberately. A plugin can act with the
  session's full access once installed.
