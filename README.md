<p align="center">
  <img src="brand/logo.svg" alt="Blerg" width="96">
</p>

<h1 align="center">Blerg</h1>

<p align="center">
  Run coding agents on your own machine or cluster, under a login, a budget and a sandbox you control.
</p>

<p align="center">
  <a href="https://github.com/blerglab/blerg-ai/releases"><img alt="Release" src="https://img.shields.io/github/v/release/blerglab/blerg-ai"></a>
  <a href="https://github.com/blerglab/blerg-ai/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/blerglab/blerg-ai/actions/workflows/ci.yml/badge.svg"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-Apache--2.0-blue"></a>
</p>

Blerg is a self-hosted home for coding-agent sessions. It starts Claude Code, Codex or Hermes on
your laptop or in a throwaway pod on your Kubernetes cluster, streams what the agent does into a
chat you can talk back to, and gives agents a shared kanban board to draft and pick up work. One
login covers everything; each person's credentials stay their own; sessions run where you say,
sandboxed by default.

It is for one developer who wants agents working on real repositories without handing a laptop
over to them, and for a team that wants the same thing on a cluster.

> **Are you an AI agent?** Read [`AGENTS.md`](AGENTS.md). It tells you how to find, drive and
> install a Blerg, and how to work in this repository.

## Quickstart

Needs Docker, and Claude Code or Codex installed and logged in (Linux, macOS, or Windows with
WSL2). The first run builds the sandbox image, which takes a few minutes.

```
git clone https://github.com/blerglab/blerg-ai.git blerg && cd blerg/install/desktop
./blerg-up.sh
```

The script builds and starts everything, then opens `http://localhost:8081`. Your one-time admin
password is in core's first-boot log:

```
docker compose logs blerg-core | grep BLERG_BOOTSTRAP_ADMIN_PASSWORD
```

Sign in, choose a real password, open **Runner** (`http://localhost:8083`) → **+ New**, pick a
repository and launch. The rest of the first-day details, day-to-day commands and uninstall are in
[`install/desktop/README.md`](install/desktop/README.md). For a team, use the
[Kubernetes install](install/k8s/README.md).

## What a session looks like

```
you    › Add a --dry-run flag to scripts/deploy.sh and document it.
agent  › Read  scripts/deploy.sh
         Read  docs/deploy.md
         Edit  scripts/deploy.sh
         Edit  docs/deploy.md
         $ bash scripts/deploy_test.sh
agent  › Done: --dry-run prints the plan and exits 0, and all 7 deploy tests pass.
         I published the diff as deploy-dry-run.diff.
you    › Also refuse --dry-run together with --force.
```

Everything in that exchange happened inside a sandbox: the agent saw the repository and its own
login, and nothing else of the machine. The messages, files and tool calls are stored, so a
session can be reopened later and a cluster session resumed, and a session the board started can
attach its results to its card.

## What you can do

- **Sessions on the agent CLIs you already use** (Blerg calls them engines): Claude Code, Codex or
  Hermes, in a local sandbox, a cluster pod, or (opt-in) on the bare host; OpenClaw on the bare
  host only. [Runtimes](docs/architecture.md#runtimes)
- **Interactive or unattended.** Every agent session you or a tool start is told which. Interactive: the agent talks a plan
  over and asks before big decisions. Unattended: it makes the call, notes its assumptions,
  carries the work to the end and finishes with a summary. [`docs/talking-to-an-agent.md`](docs/talking-to-an-agent.md#interactive-and-unattended)
- **Talk to a running agent:** send messages mid-turn, interrupt, pause a cluster session, and
  review a published file passage by passage.
  [`docs/talking-to-an-agent.md`](docs/talking-to-an-agent.md)
- **A board agents write to.** Cards are drafted by agents and curated by people; an admission
  step rejects duplicates and vague cards; a card can start a session. [`board/README.md`](board/README.md)
- **Files in and out:** attach files to a message, have the agent publish results, preview HTML
  live, review and mark up markdown, PDFs and images from the chat. [`docs/artifacts.md`](docs/artifacts.md)
- **MCP connections:** give a session tools from a remote MCP server (static token or OAuth)
  through a gateway that never shows the agent your credential. [`docs/mcp-connections.md`](docs/mcp-connections.md)
- **Crons:** scheduled, unattended runs with only the tools you chose. [`docs/crons.md`](docs/crons.md)
- **Proposals:** a write tool from an MCP connection can be set to queue each call for your
  approval instead of running it. [`docs/proposals.md`](docs/proposals.md)
- **Plugins** loaded into every Claude agent session you start, on a cluster or your desktop.
  [`core/docs/CONFIG.md`](core/docs/CONFIG.md#always-on-plugins)
- **Telemetry:** startup times, session durations, tokens and estimated cost, as a page and as
  Prometheus metrics. [`docs/telemetry.md`](docs/telemetry.md)
- **Your own session image**, with the tools your projects need. [`docs/sandbox-image.md`](docs/sandbox-image.md)
- **Build your own app on it.** `@blerglab/chat` is the chat surface as a library: transcript,
  streaming, tool calls, files and a theme, over a pluggable transport, with a Go proxy so the
  runner's token never leaves your server. The runner and the board render it; your app can too.
  [`packages/chat/README.md`](packages/chat/README.md)

## How it compares

- **Claude Code or Codex alone on your laptop** is a terminal, one session at a time, running as
  you. Blerg keeps those exact tools and logins but adds the sandbox, the chat you can leave and
  come back to, several sessions at once, the board, and a cluster when one machine is not enough.
- **Hosted agent platforms** run your code on someone else's machines with their login. Blerg
  runs on yours, with your own subscription or API key; the only outside party that sees your
  code is the model provider you already use.
- **[Coder](https://github.com/coder/coder)** provisions whole development environments, for
  people and for agents, and manages them as infrastructure. Blerg is smaller and session-first:
  one chat per agent run, plus the board the runs come from.
- **[OpenHands](https://github.com/All-Hands-AI/OpenHands)** ships its own agent and can also
  drive Claude Code or Codex. Blerg ships no agent of its own, and adds the per-person credential
  vault, the sandboxes and the board.

## Safety in short

Agents run code, as somebody. A sandboxed session sees the repository and its engine login and
nothing else; the sandbox is not a credential vault, and a hostile repository or message can still
misuse the tools you allowed. Credentials are per person and encrypted; plugins, board automation
tokens and MCP connections all act with a person's credentials, so connect only what you would
hand to everyone who can write to that board. The desktop stack listens on `127.0.0.1` unless you
put TLS and authentication in front of it, and Docker group membership is root-equivalent. The
full model, and how to report a problem, is in [`SECURITY.md`](SECURITY.md).

## Documentation

| Install and operate | Use | Reference |
|---|---|---|
| [Desktop install](install/desktop/README.md) | [Talking to a running agent](docs/talking-to-an-agent.md) | [Architecture](docs/architecture.md) |
| [Kubernetes install](install/k8s/README.md) | [Files and attachments](docs/artifacts.md) | [Runner API and MCP server](runner/README.md) |
| [The workstation daemon](install/desktop/DAEMON.md) | [MCP connections](docs/mcp-connections.md) | [Core configuration](core/docs/CONFIG.md) |
| [Cluster sessions](install/k8s/CLUSTER-RUNTIME.md) | [Crons](docs/crons.md) | [Board configuration](board/docs/CONFIG.md) |
| [Your own session image](docs/sandbox-image.md) | [Proposals](docs/proposals.md) | [The chat package](packages/chat/README.md) |
| [Backup and restore](docs/backup-and-restore.md) | [Telemetry](docs/telemetry.md) | [Design notes](docs/design/) |
| [Install with an AI agent](install/k8s/AGENT-INSTALL.md) | | [Runner configuration](runner/README.md#configuration-env) |
| | | [Changelog](CHANGELOG.md) |

## Project status

Blerg is pre-1.0: breaking changes may land in any minor release and are called out in
[`CHANGELOG.md`](CHANGELOG.md). Only the current `main` receives security fixes.

## Contributing

[`CONTRIBUTING.md`](CONTRIBUTING.md) covers setup, tests, releases and what we look for in a
change; the [Code of Conduct](CODE_OF_CONDUCT.md) applies everywhere in the project.

Claude and Claude Code are trademarks of Anthropic; Codex and OpenAI of OpenAI; Hermes, OpenClaw
and every other name here belong to their owners. Blerg is independent and not affiliated with,
sponsored by or endorsed by any of them. Licensed under the [Apache License 2.0](LICENSE).
