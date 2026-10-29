# Roadmap

Where Blerg is heading after the current release. This is direction, not a commitment: priorities
change as people use Blerg and tell us what they need. Releases have no fixed dates; each one ships
when it is ready. What already shipped is in [`CHANGELOG.md`](CHANGELOG.md).

Updated with each release. Last updated for 0.5.0.

## 0.6.0: Blerg as the IDE of AI-driven software engineering

**The session as a workspace**

- **Engineering reports in Blerg:** lint and static analysis, test results and coverage shown in
  Blerg and shared with the session, so the agent sees what you see.
- **Live preview:** an HTML or chart tab that reloads as the agent iterates on it, from published
  versions or from a file in the workspace.
- **Tabs you define:** web pages, views provided by MCP servers, and custom views bound to a data
  source (for example a live data feed), sandboxed like every other tab.
- **The board as a tab:** see and move cards without leaving the session.
- **Review any file:** anchored change requests on any workspace file, sent to the session.
- **A personal workspace** whose data outlives a single session, starting with pinning a useful
  answer for later.
- **Editing keys:** Vim or Emacs key bindings as a preference.

**Great at software engineering out of the box**

- **Blerg skills in every session, on every engine:** how to work with boards and tickets, talk
  with you, publish files and mockups, and run helper sessions, plus engineering discipline (tests
  first, root-cause debugging, verification before calling something done, reviews). Installed
  automatically, loaded only when relevant, and versioned with Blerg.

**Workflows and guardrails**

- **Command and Git policies:** per-session or per-card rules (deny, ask or allow) for shell commands,
  commits and pushes, enforced by the engines and by Git itself, with approvals and an audit record.
- **Audit tooling:** who did what, where and how, across sessions, tools, permissions and
  connections.
- **Scheduled runs with fixed commands:** a cron can run commands you define and hand their output
  to the agent as data, so monitoring scripts move over without giving an unattended agent a shell.

**Teams and Git**

- **Any Git host:** the board's Git panel reads commits and diffs from Git itself, not only from
  GitHub.
- **Your branch names:** a configurable naming pattern for work branches (for example
  `dev/{username}/{slug}`).
- **Release planning on boards.**

**Runtimes and engines**

- **Shells that last:** a desktop shell survives your laptop sleeping, network drops and VPN
  reconnects, and later a restart of the desktop app or of Blerg's server, without giving up any of
  the shell's security checks.
- **Inference gateways:** route engines through your organization's model gateway.
- **Groundwork for multiple clusters:** clusters become named runtime targets.

## 0.7.0: workflows and more places to run

- **Workflow engine:** defined workflows with session and MCP steps, static checks, outcomes and an
  audit trail.
- **Multiple Kubernetes clusters as runtimes:** register several clusters, pick one when you start a
  session, with per-cluster credentials, images, limits and scheduling (for example GPU work on a
  GPU cluster).

## Suggesting something

Open an issue describing what you are trying to do and what gets in the way. The problem matters more
to us than a proposed solution.
