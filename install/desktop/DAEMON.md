# The runner daemon

The runner **daemon** drives your local `claude` CLI + tmux to actually run agent
sessions. It can't be containerized — it needs your own Claude auth and your own
tmux — so it always runs directly on the host, alongside the containerized
`blerg-core` / `blerg-board` / `blerg-runner` stack from `blerg-up.sh`.

`./blerg-up.sh` installs it automatically as a host service. Before touching
`.env` or bringing up any containers, it checks (in order) that Docker itself
is installed and reachable, then that this service can actually be installed —
if either check fails and can't be fixed automatically, it stops right there
with instructions instead of starting anything. Use `--no-daemon` to skip the
daemon-service check entirely and get manual run instructions instead (the
Docker checks still run either way). `./daemon/install.sh install` also
builds the local-sandbox image the first time.

**Desktop daemon:** being in the `docker` group is root-equivalent on this
machine — a Local-sandbox session can start any container with any mount.
Only add accounts to the `docker` group that you'd trust with root.

## Files and permissions

What to back up (`.env`, this daemon's `settings.json`, the databases) and how to restore
it: [`docs/backup-and-restore.md`](../../docs/backup-and-restore.md).

`install/desktop/.env`, the systemd unit (`~/.config/systemd/user/blerg-runner-daemon.service`),
and the launchd plist (`~/Library/LaunchAgents/dev.blerg.runner-daemon.plist`)
hold private install configuration and, if you gave one, your Claude OAuth token.
Person-issued desktop credentials live only in the private daemon state file; the
new service loads that file rather than copying its token into the environment. Both
installer scripts `umask 077` before writing anything, and the unit/plist are
additionally `chmod 600` right after being rendered — so all three end up
owner-read/write-only (`0600`), not group- or world-readable. This is enforced
on every run, not just a fresh install: an `.env`/unit/plist left over from
before this hygiene was added (the common case on an upgrade) is `chmod 600`
in place at the top of `blerg-up.sh` / `daemon/install.sh`, whether or not
that run goes on to rewrite it. `install.sh status` redacts `DAEMON_TOKEN`/
`OAUTH_TOKEN`/`API_KEY` values in its output rather than printing the unit's
environment (or, on macOS, `launchctl print`'s rendering of the plist)
verbatim — the redaction isn't tied to one renderer's syntax: it also blots
out the actual configured token values wherever they appear literally, so an
unanticipated output format can't leak them through.

By default the daemon does **not** edit `~/.claude/CLAUDE.md` — set
`BLERG_RUNNER_PROVISION_CLAUDE_MD=true` to opt in to it adding/refreshing the
blerg-runner messaging guidance block there on every start.

The Claude usage meter is on by default: the daemon reads the account usage
endpoint the CLI's usage view reads, with the CLI's sign-in from
`~/.claude/.credentials.json` (read only, never refreshed or written; only
percentages and reset times reach the server; a work account's monthly spend limit is the share
used only, never an amount). Set
`BLERG_RUNNER_CLAUDE_USAGE=0` to turn it off. Its start-up log line says which.
The endpoint and the file are the CLI's internals, not a documented API: if
they change the meter goes empty (logged once). On macOS the CLI's login is in
the Keychain, not this file, so there is no Claude meter (the daemon logs "no
~/.claude/.credentials.json … meter off" once). The meter reads the account in
that file; a daemon started with `CLAUDE_CODE_OAUTH_TOKEN` may run sessions on
a different account.

**Setting a daemon option.** The daemon never reads `.env` itself; it reads
its service environment (the systemd unit's `Environment=` lines, or the
plist's `EnvironmentVariables`). `install.sh install` writes that environment,
and it copies `BLERG_RUNNER_PROVISION_CLAUDE_MD`,
`BLERG_RUNNER_ALLOW_HOST_CREDENTIAL_CLONE` and `BLERG_RUNNER_CLAUDE_USAGE` into it when one is set, from
your shell first, else from `install/desktop/.env`. So to turn one on, add it
to `.env` and reinstall the service:

```
echo 'BLERG_RUNNER_PROVISION_CLAUDE_MD=true' >> install/desktop/.env
install/desktop/daemon/install.sh install
systemctl --user cat blerg-runner-daemon | grep BLERG_RUNNER_PROVISION   # check (Linux)
```

To turn it off again, delete the line from `.env` and run
`install.sh install` again. Any other daemon variable in this document (for
example `BLERG_RUNNER_SANDBOX_IMAGE`) is not copied: add it to the unit with
`systemctl --user edit blerg-runner-daemon` (an `[Service]` section with
`Environment=NAME=value`), then `systemctl --user restart blerg-runner-daemon`.
On macOS, add a `<key>`/`<string>` pair under `EnvironmentVariables` in
`~/Library/LaunchAgents/dev.blerg.runner-daemon.plist`, then run
`launchctl bootout gui/$(id -u)/dev.blerg.runner-daemon` and
`launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.blerg.runner-daemon.plist`.
A hand edit to the plist is lost the next time `install.sh install` runs; a
systemd drop-in from `systemctl --user edit` is kept.

`install.sh` requires an explicit repos root: interactively it asks (and
saves your answer to `.env`), but a non-interactive run (no terminal to
prompt) now fails with an actionable message instead of silently guessing
`~/repositories` — export `BLERG_RUNNER_REPOS_ROOT`, or add it to
`install/desktop/.env`, before running it non-interactively.

### Changing the repos folder later

`BLERG_RUNNER_REPOS_ROOT` is only the **first-run default**. To move it
without touching `.env` or restarting anything, open the launch sheet (New
session), and under **Where it runs** use **Repos folder → Change** for the
daemon. The daemon checks the folder itself — an absolute path, not `/` or
`/proc`/`/sys`/`/dev`, a folder (created if missing) it can write to — and
refuses with the reason otherwise, changing nothing.

Because session recovery records live in `<repos folder>/.blerg-runner/`
(and carry the session's messaging/board tokens), a folder other users
control is refused too: another user's folder that is group- or
world-writable (such as `/tmp`), or your own that anyone can write to
without the sticky bit. An existing `.blerg-runner` there must be yours; if
it is, it (and its `sessions/` directory) is tightened to `0700` before use,
and that check is repeated on every record read or write — a directory or
record file owned by someone else is refused, never adopted.

A change is saved to `~/.blerg-runner-daemon/settings.json` (or
`$BLERG_RUNNER_DAEMON_STATE_DIR/settings.json`; the file is 0600, its
directory 0700) and **wins over `BLERG_RUNNER_REPOS_ROOT` on every later
start** — so editing `.env` afterwards has no effect until you delete that
file (or its `repos_root` key). A corrupt or unreadable settings file is
ignored with a warning in the daemon log, and the env var is used. The
startup log line says which one is in effect (`repos_root=… from settings`
or `from env`).

Sessions already running keep the folder they started in; only new sessions
(and the repo list in the launch sheet) use the new one. Their recovery
records (`<repos root>/.blerg-runner/sessions/`) move with the root, so a
daemon restart still recovers them.

### Plugin marketplace policy

An administrator manages trusted sources in Core's **Settings → Plugin marketplaces**.
Official Anthropic and OpenAI Codex marketplaces ship on. A connected desktop daemon
receives changes from its runner server without restarting and checks that policy locally.
Turning a source off keeps existing plugin installations and running snapshots; lists flag
them, while new sessions skip them. `BLERG_RUNNER_PLUGIN_MARKETPLACES` is the daemon's
initial fallback only until the first Core policy is accepted. The daemon saves
that policy, its Core-generated ID, and its revision locally. A changed ID resets
revision tracking; stale or replayed revisions within one ID are rejected. Saved
permissions survive disconnections, Core outages, lease expiry, and restarts.
See [Core's precedence and upgrade rules](../../core/docs/CONFIG.md#plugin-marketplace-policy).

### What a daemon restart does to running sessions

Restarting the daemon (an update with `install.sh install`, a crash, a reboot of the service)
does not end the sessions it hosts:

- **Terminal sessions** live in tmux and are reattached.
- **Agent sessions on Claude Code** are hosted again from a recovery record in the daemon's state
  directory (`~/.blerg-runner-daemon/agents/`, one 0600 file per session, holding the session's
  token and environment). The same session continues, with the same Claude conversation
  (`claude --resume`). The step that was running is lost: the agent is told it was cut off, which
  of its own tool calls had not returned and what each ran, to check their state first and never
  re-run anything destructive without checking, and why the daemon restarted (below), and the
  chat says so. Then it is asked to continue, once, by itself, so you do not have to go through every
  session after a restart: whether it was mid-generation, in a command of its own, or waiting on
  subagents or background tasks (below), interactive or unattended. The exceptions: an
  interactive session that was waiting for you (an open `blerg-runner ask`, a permission prompt)
  waits for your answer instead; any session (unattended too) cut off in one of the commands
  below waits for you to check it; so does one already continued once since its last finished
  turn. A session cut off before it had a conversation is not continued either: its task is
  unknown to the agent, and the chat asks you to send it again.
  Such a session is **paused by a restart**: its card shows "?" (tooltip "paused by a restart")
  and it waits for your message. A message
  accepted for a paused session, from any tab, ends its pause. A question it had asked is
  closed: your next message goes to the agent, which is told its question was cut off. A
  message you send while the daemon is away is kept for it, not taken as the answer to that
  dead question, and replaces the continue.
- **Background work dies with the restart:** commands run in the background, Monitors and
  subagents. The record lists them, so after the restart each one is marked stopped in the chat
  ("the daemon restarted"; the sidebar's ⏱ count drops with them) and the agent is told, once,
  what was cut off: each task and subagent by kind, description and id, with the instruction to
  check the state of their worktrees and outputs first, resume a subagent by sending a message to
  its agent id, re-run a background command that did not finish, and not re-run anything
  destructive. The session is asked to pick it up straight away, once, by a system message that
  carries the list and "Continue the task you were working on.", unless it is paused (above):
  then the list shows in the chat as the system's message, and the agent gets it with your next
  message. A message you write before the session is picked up again (also one sent while the
  daemon was down) goes first and carries the list instead; nothing is
  sent for you. Background work that ends while the daemon runs (its Claude Code process closed
  or crashed) is marked stopped then ("the engine process ended"), and a later restart does not
  report it again.
- **Not brought back:** a sandboxed agent session, a session with MCP connections, a cron's
  session, and sessions on engines other than Claude Code and host Codex (a host Codex session
  started before its recovery record existed is not brought back either). They end as before.

**Why it restarted.** The agent is also told what caused the restart, so that it does not take
its own last command (a test run, a build) for the cause and stop. Right before `install.sh
install` (and so `blerg-up.sh`) restarts a running daemon, it writes `restart-planned` into the
daemon's state directory (`$BLERG_RUNNER_DAEMON_STATE_DIR`, default `~/.blerg-runner-daemon`),
holding `update` (the new build differs from the running one) or `reinstall` (the same build).
The daemon itself writes `stopped-cleanly` there when it is told to stop (SIGTERM from systemd or
launchd, Ctrl-C). The next daemon reads and removes both at start, and its log says which cause it
found. With either, a session the restart cut off hears "The daemon was updated and restarted.
Nothing you ran caused it. Continue where you left off; check the state of anything that was
mid-run first — never re-run anything destructive without checking." ("reinstalled and
restarted" for a reinstall, "restarted" for a stop with no installer marker, such as a
`systemctl --user restart` by hand or a reboot). "Nothing you ran caused it." is left out when
one of its cut-off commands was the installer or a service restart or stop, or, for a stop with
no installer marker, a `kill`, `pkill` or `killall`. With neither marker (a crash, a `kill -9`, a
power cut): "The daemon stopped unexpectedly. A test run, a build or ordinary commands cannot
cause that; only re-check a command that could affect the daemon itself (an install, a service
restart) before running it again. Continue where you left off; check the state of anything that
was mid-run first." An installer marker older than 15 minutes (its restart never happened) is
removed without being believed.

The server gives a daemon 20 seconds to come back before it marks anything: within that, its
sessions look as they did, a message you type is kept and delivered when the daemon returns, and
a board following one of them notices nothing. A daemon away for longer has its sessions shown
as errored ("daemon disconnected") until it reconnects. A session you stop in the app while the
daemon is down stays stopped. A daemon that stays away for more than 30 minutes still gets its
sessions back, but their `blerg-runner` tokens have been revoked by then.

Only one daemon process uses the recovery records: a second daemon started by the same user
while the first is running (a build run by hand, say) keeps none and recovers nothing, and says
so in its log.

#### Commands a restart never continues

The agent's own tool calls are recorded as they start, and each Bash command is matched, whole
and case-insensitively, against a fixed list. A session cut off in a matching command is paused
(the notice says it "looked destructive"), and the agent is told not to run it again unless you
ask; your message picks it up. The list:

- a force push: `git push` with `--force`, `--force-with-lease`, `-f` (also in a flag group,
  `-uf`) or a `+refspec`; `git reset --hard`; `git clean` with `-f` or `--force`;
- a recursive forced delete: `rm` both recursive (`-r`, `-R`, `--recursive`) and forced (`-f`,
  `--force`), the flags in any order and anywhere among its arguments (`rm -rf`, `rm x -rf`,
  `rm --recursive --force`);
- a migration: the words `migrate`, `migrates`, `migrated`, `migration`, `migrations`, or
  `alembic`, `goose`, `flyway`, `liquibase`;
- a deploy: a word starting with `deploy` or `redeploy` (`make deploy`, `./deploy.sh`),
  `terraform apply|destroy`, `helm
  install|upgrade|uninstall|delete|rollback`, `kubectl … apply|delete`;
- the installer or a service restart: `install.sh`, `blerg-up.sh`, `systemctl …
  restart|stop|kill`, `launchctl … kickstart|stop|unload|bootout`, `service <name> restart|stop`,
  `make … install-daemon|restart-daemon`;
- dropping data: `DROP TABLE|DATABASE|SCHEMA|INDEX|VIEW|OWNED`, `TRUNCATE`, `DELETE FROM`.

Only the command is matched, never the call's description. It errs towards pausing: a command that only mentions one of these (`kubectl get deployment`)
pauses too. Background tasks and subagents are not matched (their record keeps a description,
not the command).

The restart that first installs recovery support over a daemon without it still ends
agent sessions: the old daemon kept no records. Ordinary nondestructive work continues
under the usual conversation, waiting-for-you and once-per-turn guards.

A planned successful update continues the session that started it when its **only**
cut-off tool call is the one the running daemon observed. This works with the plain
installer, `make restart-daemon`, and detached `setsid nohup … &` launches followed
by a wait loop. The wait must stay in the **same tool call**, with a Bash timeout long
enough to cover the build and restart (120 seconds by default, 600 seconds maximum);
if that call finishes or times out first, ordinary recovery applies. The installer asks
the running daemon to record the planned target;
only the daemon writes update evidence. The replacement checks its executable digest
and embedded commit, consumes the record once, and resumes the matching conversation.
The person sees `↻ Updated to <commit> · continued`; the agent gets the detailed
instructions and is told not to repeat the update. Idle sessions get no restart row;
a paused row expands to list the interrupted calls, background tasks and subagents.

Other sessions, two interrupted calls, other destructive work in the same shell call,
failed or rolled-back updates, expired evidence, missing conversation history and the
usual person-wait and once-per-turn guards retain the normal pause. Paused children
also tell their live parent, using a deduplicated child-turn note with the pause reason.
The ordinary user service handles updates; no root service or special sudo policy is
installed. A daemon that predates this protocol cannot observe an update, so its first
update to this version still uses the ordinary recovery rules.

If you tried #221's unreleased `--protected-updates`, remove its stray system service before reinstalling: `sudo systemctl disable --now "blerg-runner-daemon-protected-$(id -u).service" && sudo rm -f "/etc/systemd/system/blerg-runner-daemon-protected-$(id -u).service" && sudo systemctl daemon-reload && sudo rm -rf "/var/lib/blerg-daemon-updates/$(id -u)"` (that folder held the supervisor and a copy of the daemon token).

The state directory is owner-only (0700), with 0600 pending-update records. These are
same-user recovery bookkeeping, not a security boundary against host sessions: those
sessions already run as you, so pretending to have started an update adds no authority.
Process ancestry identifies the live requester; for already-reparented detached
installers, the daemon also checks the session identity inherited in the OS process
environment against the live engine. No session-written receipts are used. Evidence
expires after five minutes and is consumed even on rejection. Legacy
`restart-update.json`, `note-update` and `note-update-failed` remain unsupported.

(A daemon older than the background-work records cannot list what its sessions run in the
background, so the first update past it marks none of it stopped.)

Before `install.sh install` restarts a running daemon, it asks the new build which agent sessions
the restart would cut off (`blerg-runner-daemon status --open-work`, read from the recovery
records): a turn running, background tasks, subagents. If there are any it lists them and asks
"Reinstall anyway? [y/N]"; a no leaves the daemon and its binary as they were. `--yes` reinstalls
without asking (`./blerg-up.sh --yes` passes it on); with no terminal to ask (an agent running
the installer, a script), the install stops with exit code 5 and says to re-run it with `--yes`. Run from inside an agent session, the list leaves out
that session's own running turn. Sessions that keep no record (sandboxed, with MCP connections)
are not listed.

### Cloning a repository you name

In the launch sheet's Repository box you can type any `owner/name` (GitHub,
or GitLab from the picker under it) or paste a repository URL
(`https://gitlab.com/grp/tool`, `git@github.com:owner/name.git`). The
daemon clones it into the repos folder before the session starts, as
`<name>`, or as `<owner>-<name>` when a `<name>` folder there is some other
repository. A folder that already is that repository is used as it is, and
an unrelated folder is never cloned into or over. If both names are taken,
the launch is refused and the sheet says why.

The clone goes over HTTPS to the provider's host. A public repository needs
no token, and the server checks with the provider so that a public clone
never carries one. For a private repository the server sends the daemon your
own token for that provider from **Settings**, but only in these cases:

- **Local sandbox:** the clone runs inside a throwaway sandbox container.
  The token reaches it on the container's standard input and goes into a
  git config file private to that container. It never appears on a command
  line or in any process's environment, on the host or in the container.
- **This machine:** only on a daemon started with
  `BLERG_RUNNER_ALLOW_HOST_CREDENTIAL_CLONE=true` (see *Setting a daemon
  option* above for how to set it). Otherwise the launch is
  refused with the reason. On the bare host, git's environment holds the
  token while it clones, and any other session running unsandboxed on the
  same machine as the same user could read it. **Only set this on a daemon
  nobody else uses.** git on the host then receives it as an
  `Authorization` header limited to that host, through its environment
  (this needs git 2.31 or newer; an older git is refused with that reason).

In both cases the token is used for that one clone only. It never lands in
the clone's `.git/config`, in a session record or in the daemon log. If
HTTPS fails for any reason other than its own timeout, the daemon tries the
provider's ssh address with this machine's own keys. The clone's `origin` is
the HTTPS URL without the token, so later pushes use this machine's own git
credentials.

A clone never waits for input. A password prompt, an unknown ssh host key or
a passphrase fails it instead. A transfer that stalls for a minute is
aborted, and one attempt may take up to 30 minutes. Stopping the session
stops its clone at once. Other session starts on this daemon wait in line
behind a running clone. If the daemon is killed mid-clone, it removes the
hidden `.blerg-clone-*` folder that clone left behind at its next start.

## Local sandbox runtime

**Both** session kinds — Terminal ("classic tmux") **and** Agent (the headless
kind the Board and the API start) — default to the **Local sandbox** runtime
on a daemon: each session runs in a throwaway Docker container with **a clone
of its own** of the repo mounted at `/workspace`, never your checkout and
never your `.git`. Its commits land on the branch `wip/<session-id>` in your
repository at the end of each turn and when it stops (work it left
uncommitted is committed for it at the stop); nothing is merged or pushed.
What the sandbox can and cannot touch, and how the commits come back:
[`docs/sandbox-image.md`](../../docs/sandbox-image.md#what-a-sandbox-works-in-and-where-its-commits-land).
Your engine directories are not mounted: each
sandboxed session gets a home of its own with the current sign-in of the
engine it runs (it cannot renew it or change yours) and your settings,
without their secrets, read-only, so nothing it does there runs on your
machine later. Your own Claude hooks and status line do not run in a Local
sandbox session (see
[the sandbox home](../../docs/sandbox-image.md#what-a-sandbox-works-in-and-where-its-commits-land)).
The session can use that sign-in until it expires. Network is open. "This machine,
unsandboxed" (bare host, full filesystem access) is one click away in the
launch sheet's Run column for anyone who wants it, behind an explicit
acknowledgement — it is never a default while a sandbox is available.

A session started through the v1 contract (`POST /api/runner/start` — Board
session, card spawn, **Run board**, the MCP `start_session` tool) gets the same
default: with no `runtime` in the request it runs in the **local sandbox**
whenever the chosen daemon reports the sandbox image available, and on the
bare host only when it doesn't. `runtime: daemon` is still an explicit choice,
and a board runs on the host only where its owner chose and acknowledged it in
Where it runs (`BLERG_BOARD_RUNNER_RUNTIME=daemon` no longer puts board starts on the host). That contract has no "bypass
permission prompts" field, so a v1 start never asks for one — though an Agent
session runs non-interactively and its engine bypasses the prompts anyway, on
either runtime.

The Board makes those starts with the board's **automation token** (a
`run-sessions` agent token a board admin saved in the board's settings panel),
never with the shared `BLERG_RUNNER_KEY` — a board with no token starts
nothing and says so. On a daemon the session still uses this machine's engine
login; the token decides whose session it is and which engine it runs. See
[`board/docs/CONFIG.md`](../../board/docs/CONFIG.md#board-automation-identity-who-a-boards-sessions-run-as).

**The `blerg-sandbox` network.** A board-started agent has to talk back to
the board (and to the runner, for messaging), but the URLs it is handed are
the host-side `http://localhost:<port>` ones, which mean nothing inside a
container. So the desktop compose declares a dedicated network named
`blerg-sandbox`, with only `blerg-board` and `blerg-runner` on it. At every
sandboxed spawn the daemon checks whether that network exists; if it does, the
container joins it and the daemon rewrites a loopback `BLERG_BOARD_URL` to
`http://blerg-board:8080` and `BLERG_RUNNER_SERVER_HTTP` to
`http://blerg-runner:8080`. From inside, a sandboxed session can reach:

- the **board and runner APIs**, by service name (both still require a token);
- **other sandbox containers** on the same network;
- the **open internet** (outbound is not restricted — see *Network* below).

It can **not** reach Postgres or `blerg-core`: neither is on `blerg-sandbox`,
so their names don't even resolve from a sandbox. Nothing about what the
stack publishes on the host changes.

The network name comes from `BLERG_RUNNER_SANDBOX_NETWORK` on the daemon
process (default `blerg-sandbox`); set it to an empty string to turn the
network lookup off entirely, so containers always use Docker's default bridge
with their environment as given — which is also what happens, automatically,
when the named network doesn't exist (for example a daemon pointed at a remote
cluster, whose URLs are public and reachable anyway).

**The messaging CLI is mounted, not baked in.** The daemon bind-mounts the
`blerg-runner` messaging CLI read-only when present
(`runner/.claude/skills/session-messaging/blerg-runner`) to
`/home/agent/.local/bin/blerg-runner` in the container, which is already on the
image's `PATH` (and `python3` is in the image). No image rebuild is needed to
pick it up.

**No git credentials in the sandbox.** `~/.ssh` and `~/.gitconfig` are not
mounted, so an agent in the sandbox can commit into `/workspace` but cannot
push. Its commits reach your repo on `wip/<session-id>`; review and push them
from the host yourself, or choose **This machine** for a session that must
push on its own.

**Upgrading an older stack.** A stack brought up from a compose file that
predates the network has no `blerg-sandbox`. Re-run `./blerg-up.sh`: it
creates the network and reattaches board and runner to it. Until you do,
sandboxed sessions still start and run, but cannot reach the board; the daemon
logs one warning saying the network is missing.

The two kinds get the identical container; only what runs inside it differs.
A Terminal session gets a tmux server and a PTY you attach to. An Agent
session has no tmux at all: the daemon runs the engine process inside the
container with `docker exec -i -w /workspace`, so the engine's cwd is the
mount point and the host path it came from means nothing to it. The driver's
stdin/stdout/exit handling is the same code as on the host — only the command
is wrapped.

Started with `--cap-drop ALL --security-opt no-new-privileges --init --pids-limit
512 --memory 4g`. Docker's init process reaps orphaned tool and test processes so
exited children do not accumulate as zombies and exhaust the process limit.
The limit also counts threads; live processes and threads can still reach it.
No `--restart` — a sandbox stopped by a reboot is recreated by the daemon from
its session record on the next launch.

**Orphan cleanup.** A container is named after its session, so one left behind
by a killed daemon, a reboot or a crash still owns that name and would make
the next `docker run` fail on the collision. The daemon therefore does a
`docker rm -f <name>` before creating a session's container (and again if the
run itself fails, so a half-created container can't block the next attempt) —
the old container belongs to a session that is being recreated right now
either way. Agent-kind containers are also skipped when the daemon scans for
sandboxes to reattach to on restart: it only reattaches to containers that
actually have a tmux server inside, so an agent container is never mistaken
for a resumable terminal session — which is also why an agent container needs
cleaning up rather than adopting: nothing will ever reattach to it, and the
engine process inside it died with the daemon that started it. So on every
(re)connection, after reattaching and recovering everything it can, the daemon
sweeps its own sandbox containers: any that is not a session it is managing,
and has no tmux server inside it, is `docker rm -f`'d. "Its own" means the
container carries the daemon's `blerg.runner.owner` label (set on every
container it starts), or, for a container from before that label, that the
daemon has a record of the session (its clone, sandbox home or session
record). A container whose name merely starts with `blerg-sandbox-` (another
daemon's on the same Docker, a test's) is never reattached or removed; it is
logged once per daemon run. To find unlabeled ones left from older daemons:
`docker ps -a --filter name=^/blerg-sandbox- --filter label!=blerg.runner.owner`.

**A container that stops.** While a sandboxed agent session runs, the daemon
looks at its container every few seconds (one `docker ps` of its own
containers for all sessions, then `docker inspect` only for one that is not
running) and whenever the engine fails. Every look goes to the docker context
the container was started on, so switching your docker CLI to another context
does not make it look removed. The `blerg.runner.docker-context` container label
restores that pin after a daemon restart. Older containers without the label use
the current context at rediscovery, logged once per container. A slow listing
does not block other watchers; while a listing is in flight, peers on that
context defer judgment until a later check. When Docker reports it stopped (a paused
container, as during the turn-end sync, is alive), or reports it missing twice
a few seconds apart, the session ends as failed with the container's exit
code, OOM flag and time, recorded in the session's events; no engine is
restarted in it. When a session ends —
either kind — its container is `docker rm -f`'d and unregistered, by name even
when the in-memory registry has been emptied by a restart. Note that the engine's own
in-container conversation state does not survive that recreation; a resumed
sandboxed agent session starts the engine fresh, the same way a cluster pod
coming back does.

**Interrupt.** Killing the host side of a `docker exec` does not signal the
process inside the container, so a sandboxed turn records its own PID to
`/tmp/blerg-turn.pid` in the container and Interrupt sends it a `SIGTERM`
from inside (`docker exec <container> sh -c 'kill -TERM $(cat …)'`). If that
can't be delivered — no turn running, container gone, docker unavailable —
the daemon falls back to killing the host-side `docker exec` client so the
driver's wait returns instead of hanging.

**How each engine's credential reaches the container.** Claude, Codex and
Hermes read the login directories mounted below, which is the whole point of
mounting them. Claude has one extra path, because in the sandbox the Claude
engine *always* drives the Claude Code CLI (`claude -p …`) inside the
container rather than the daemon's in-process API loop: if there is no
`claude` login on the host to mount, the daemon passes
`CLAUDE_CODE_OAUTH_TOKEN`, or failing that `ANTHROPIC_API_KEY`, into the
container as an env var. Those two are the only variables outside the
`BLERG_RUNNER_*` allowlist that ever enter a sandbox — the engine runs
*inside* now, so a credential the host-side driver used to read from its own
environment has to travel with it, which is the same exposure as the
`~/.claude` mount beside it. The daemon's own master token is not among them
and never will be. Neither value is ever written into a docker *argument*: the
run passes a bare `-e NAME` and docker reads the value from its own
environment, so the secret is not visible in `ps` output while the container
starts. With none of the three available, the spawn is refused
before any container is created: *"no Claude login or API key is available to
the sandbox — run `claude` login on this machine, set `ANTHROPIC_API_KEY` for
the daemon, pick Codex/Hermes, or choose This machine"*.

**The exceptions.**

- **The native API-key agent loop** (the daemon's own in-process tool-calling
  loop) is never used in the sandbox. Its filesystem and shell tools act on
  host paths in-process, so "sandboxed" would be a lie; a sandboxed Claude
  agent session drives Claude Code in the container instead, whichever
  credential it found.

**This machine + Agent + Claude.** The session runs your own Claude Code
login: `claude -p` from the daemon's PATH, one headless turn per message,
non-interactively (`--dangerously-skip-permissions` — an agent session never
stops for a permission prompt), with the session's model/effort and its
per-session env (messaging token etc.). Only when `claude` is not on the
daemon's PATH does it fall back to the native API loop, which needs
`ANTHROPIC_API_KEY`. With neither, the spawn is refused with *"install Claude
Code and log in (`claude` on the daemon's PATH), or set ANTHROPIC_API_KEY for
the daemon"* — and the launch sheet already says so: the daemon reports
`claude_cli_available` / `anthropic_key_set` in its hello and heartbeats.
The native loop is the only engine the daemon itself runs enabled Claude
plugins' `SessionStart` hooks for (on the host, for its system prompt): Claude
Code runs its own, and no other engine, restricted session or sandboxed
session runs them while it is prepared.
- **OpenClaw** stays host-only. A sandboxed spawn with `engine=openclaw` is
  refused with *"OpenClaw runs only on the host"*, and the launch sheet
  disables the Cluster pod and Local sandbox cards outright while OpenClaw is
  selected, so it rarely gets that far.

**Preflight** for a sandboxed Agent spawn checks the sandbox **image**, not
the host: first that the image exists at all, then that the engine's binary
exists *inside* it (`docker run --rm <image> which <bin>`). The host's PATH is
never consulted on that path — an engine installed only in the image would
otherwise be refused for no reason, and one installed only on the host would
be accepted and then fail inside the container.

**The image** is built by `install.sh install` the first time (so by
`blerg-up.sh`). To rebuild it by hand — after a `git pull` that changed
`runner/sandbox/`, say — run this from the repository root, exactly as the
installer does:

```
docker build -f runner/sandbox/Dockerfile --build-arg UID=$(id -u) -t blerg-runner-sandbox:latest .
```

(`runner/sandbox/Dockerfile` — debian + git/node/python/build tools + all
three engine CLIs (`claude`, `codex`, `hermes`) + tmux, non-root `agent` user.
Keep the `--build-arg UID=$(id -u)`: without it the `agent` user gets UID
1000, and if yours differs, files the sandbox writes into your repo come out
owned by the wrong user. Built a different tag, or pulling from
a private registry instead of building locally? Set
`BLERG_RUNNER_SANDBOX_IMAGE` on the daemon process to whatever you tagged/
pulled it as — otherwise the daemon looks for `blerg-runner-sandbox:latest`.)

**Engine versions in the image are pinned**, so rebuilding gives you the same
engines until the pins change. `claude` and `codex` are the exact versions in
`runner/sandbox/engines/package.json` (installed with `npm ci` from its
lockfile); `hermes` is the release named in `runner/sandbox/install-hermes.sh`,
which also lists what it cannot pin. To move to a newer engine, change the
version there (for the first two, then run `npm install --package-lock-only`
in `runner/sandbox/engines` to refresh the lockfile) and rebuild the image.
The cluster session image (`runner/Dockerfile.devcontainer`) uses the same
two files.

**What's shared with the container:** the target repo (read-write, at
`/workspace`), and whichever engine's account state is present on the host —
`~/.claude` + `~/.claude.json`, `~/.codex`, and/or
`~/.hermes/{config.yaml,.env}` — all read-write. This was found by testing,
not assumed: mounting only `~/.claude/.credentials.json` does not satisfy
claude's login check, it still runs the interactive OAuth wizard (which can't
complete headlessly in a container, it tries to open a browser); `~/.codex`
gets the identical treatment on the assumption the same is true there
(untested against codex's own login check specifically, but there's no
reason to expect the credentials-file shortcut to work when it didn't for
claude). Hermes is mounted differently: only its two config/credential files,
not the whole `~/.hermes` directory, since that directory also holds the
hermes-agent code checkout the installer manages — mounting all of it would
silently replace the image's own install with whatever's on the host.
Practically, this means a sandboxed session has the same visibility into your
engine account state (chat history across all your other projects, settings)
that the bare-host daemon already has — the isolation this runtime buys you
is your **filesystem** (nothing outside `/workspace`, which is the session's
own clone, and the mounted account dirs is reachable; your checkout and its
`.git` are not) and **process boundary**, not your account data.

**Network**: full outbound access (v1) — nothing is blocked. This means
package installs, anonymous git fetches, and the engine's own API calls all
just work (a push needs credentials the sandbox doesn't have — see above),
but a sandboxed session is not isolated from exfiltrating data over the
network or hitting other services reachable from your machine. Revisit if
that tradeoff stops being acceptable for your use case.

## Troubleshooting the stack

### Publishing or uploading a file says "storage error"

From 0.2.1 the message reads "The runner could not save the file because it is not allowed to
write to its data folder (permission denied)", followed by an error id; the runner's log line for
that id has the details. Older runners said "storage error".

The runner keeps published files and uploaded attachments in its data volume
(`blerg-desktop-runner-data`, mounted at `/data`). The runner runs as an unprivileged user
(id 65532), so the volume has to belong to that user. Installs made before 0.2.1 could end up
with a volume owned by root, and then every publish and upload failed.

What Blerg does now:

- The runner image ships `/data` owned by 65532, so a new volume starts out right.
- Every `./blerg-up.sh` runs a small one-shot service (`blerg-runner-data-init`, it reuses the
  `postgres:16` image the stack already has) that sets the owner of the files in that one volume
  to 65532 and exits. It does not touch other volumes or your repos folder, and it keeps every file.
  The runner starts after it finishes.
- At startup the runner checks that it can write to its data directory. If it cannot, its log
  has one line that starts with `STORAGE NOT WRITABLE`, naming the directory, the user id and the
  error: `docker compose -f install/desktop/docker-compose.yml logs blerg-runner`.

What to do: run `./blerg-up.sh` again (or `docker compose up -d` in `install/desktop`) with the
0.2.1 files, then publish again. If the `STORAGE NOT WRITABLE` line is still in the runner's log,
the repair could not change the owner: `docker compose logs blerg-runner-data-init` says why (the
service still ends normally, so the runner and the board start and the runner's line explains).
Nothing needs deleting; do not remove the volume, it holds your published files and attachments.
If you changed `BLERG_RUNNER_DATA_DIR` or mounted another folder in place of the volume, make that
folder writable by user id 65532.

### Container sessions or the stack fail behind a corporate proxy

Docker copies the `proxies` of `~/.docker/config.json` (your corporate `HTTP_PROXY`,
`HTTPS_PROXY` and `NO_PROXY`) into every container it starts. A proxy cannot reach Blerg's own
service names (`blerg-runner`, `blerg-board`), so before 0.2.1 the messaging CLI inside a
container session sent `http://blerg-runner:8080` to the proxy and failed with a
"connection reset by peer", and the services calling each other could fail the same way.

What Blerg does now:

- A session in the local sandbox gets `NO_PROXY` and `no_proxy` set to: the values in the
  daemon's own environment, the `noProxy` of your Docker client config (the entry for
  `DOCKER_HOST`, else `default`), and `blerg-runner,blerg-board,localhost,127.0.0.1`. Your own
  exceptions are kept. Blerg never adds, changes or removes the proxy itself, so the session
  still uses the proxy for everything else.
- In the compose file, `blerg-core`, `blerg-board` and `blerg-runner` list the service names
  (and `postgres`, `localhost`, `127.0.0.1`) in `NO_PROXY` and `no_proxy`, plus the `NO_PROXY` /
  `no_proxy` of the shell you run `./blerg-up.sh` from, or of `install/desktop/.env`, and the
  `noProxy` of your Docker client config, which `./blerg-up.sh` reads and passes on, so your own
  internal hosts keep working. (A plain `docker compose up -d` without `./blerg-up.sh` does not
  have the Docker client config part; use `./blerg-up.sh`, or put those hosts in `NO_PROXY`.)

What to check:

- This needs the 0.2.1 daemon: reinstall it with `./daemon/install.sh install` (or run
  `./blerg-up.sh`), then start a new session. A session that was already running keeps the
  environment it started with. Inside the new one, `env | grep -i proxy` should show `NO_PROXY`
  containing `blerg-runner`.
- After changing `NO_PROXY` for the services, run `./blerg-up.sh` again so the containers are
  recreated with it.
- If the daemon itself needs a proxy to reach Blerg's runner, that is separate: set it in the
  service's environment (see [Files and permissions](#files-and-permissions)), and keep `localhost` in its `NO_PROXY`.
- Only the exceptions are added. If your proxy blocks the sites a session needs (git, package
  registries, the model API), ask whoever runs it; Blerg cannot fix that.

### Claude Code or Codex run with their own sandbox

If you run Claude Code with its sandbox on (`"sandbox": {"enabled": true}` in your settings, or
your organization's managed settings), its sandboxed commands have no direct route to this
machine's `localhost`: they go out only through the sandbox's proxy, to the hosts its settings
allow. `blerg-runner` needs two things: the runner's address (`BLERG_RUNNER_SERVER_HTTP`, for
example `http://localhost:8083`) and a read of its token file
(`~/.blerg-runner-daemon/session-tokens/<session-id>`, which the sandbox leaves readable). It
writes only into the working folder. Without the address, Claude asks every time to run
`blerg-runner` outside the sandbox.

What Blerg does: when it starts a Claude session on this machine and your settings (user, project
or managed) turn the sandbox on, the daemon adds the runner's host and port (and the board's, for
a board session) to `sandbox.network.allowedDomains` in **that session's own** settings (Claude's
`--settings`), always with the port. Your settings files are never changed, and no command is
taken out of the sandbox. Inside the sandbox, `blerg-runner` dials the runner directly first and,
when the sandbox refuses that, sends the request through the sandbox's proxy, only for the
session's own runner and board addresses. If your organization's managed settings allow only
managed domains (`sandbox.network.allowManagedDomainsOnly`), Blerg does not override that: the
session says so and names the address your administrator has to add to the managed
`sandbox.network.allowedDomains`. A Local sandbox session is not changed: Claude Code's sandbox
does not run in the image.

To run `blerg-runner` from a Claude Code you started yourself, add the same to your
`~/.claude/settings.json` (use your runner's host and port; always keep the port, since a bare
`localhost` would open every local port):

```json
{
  "sandbox": {
    "network": { "allowedDomains": ["localhost:8083"] }
  }
}
```

**Codex.** Codex's `workspace-write` sandbox blocks the network for the commands it runs, so
`blerg-runner` fails there with a connection error. Blerg does not change that for you: Codex agent
sessions run without Codex's sandbox and need nothing; for a Codex terminal (or a Codex you started
yourself) that should reach the runner, allow the network in `~/.codex/config.toml` (this opens the
network for all of its sandboxed commands, not only the runner):

```toml
[sandbox_workspace_write]
network_access = true
```

## Always-on plugins

The plugin list a person keeps in core **Settings → Always-on plugins** reaches their desktop
sessions too: every Claude **agent-kind** session (Local sandbox or This machine) starts with
those plugins loaded. Terminal sessions do not (run `/plugin` there yourself), and neither does a
restricted session (an MCP grant, a cron).

Codex also accepts native `codex:<marketplace>` identities. The official
`codex:openai-curated` and `codex:openai-curated-remote` identities ship enabled,
alongside Anthropic's official marketplace. An admin manages these sources in
Core **Settings → Plugin marketplaces**; the server delivers changes to this
daemon without a restart. Enter the native identity under Codex in Settings or
in **Capabilities → Add → Enter a plugin by name**. Local sandboxes without a
catalog can save it as **Always on** for new sessions; host sessions also support
**This session only**. Claude does not accept native Codex identities. The wildcard
permits both kinds of source. [Precedence and fallback](../../core/docs/CONFIG.md#plugin-marketplace-policy)
apply to desktops too.

How, and what it touches:

- The daemon's hello says `plugins` only when `claude` is on its PATH and it has a state
  directory; the server sends the list
  (`SpawnSession.plugins`) only to such a daemon, so an older daemon or a host without the CLI just
  starts without plugins and the start panel never promises them.
- The daemon installs into its **plugin workshop**, `~/.blerg-runner-daemon/plugins/claude`
  (under `$BLERG_RUNNER_DAEMON_STATE_DIR` when set), by running `claude plugin marketplace add`
  and `claude plugin install … --scope user` with `CLAUDE_CONFIG_DIR` pointed at it. **Your own
  `~/.claude` is never written**: nothing is installed or enabled for your interactive `claude`.
- Each session gets its own **snapshot** of the verified plugin directories (hardlinks, so it is
  cheap) under `<workshop>/sessions/<session id>`, and its Claude Code is started with one
  `--plugin-dir=<snapshot>/<marketplace>/<plugin>/<version>` per plugin: loaded for that process
  only, and untouched by a later launch updating or rebuilding the cache. The snapshot is removed
  when the session ends, and leftovers are swept when the daemon starts. A same-named plugin you
  installed yourself is replaced by the workshop's copy for the session, not duplicated.
- A Local sandbox session gets its snapshot bind-mounted **read-only** at `/blerg/plugins`. A plugin
  that must write into its own directory fails loudly rather than changing what the next session
  loads. The workshop directory is created `0755` for that mount.
- Once an hour the daemon refreshes: `claude plugin marketplace update` per marketplace and
  `claude plugin update` per plugin, so a plugin's new version arrives on a later launch. A warm
  launch costs a few seconds; the very first launch clones the marketplace (about a minute).
- The daemon re-validates every entry against Core's delivered policy, or its own
  `BLERG_RUNNER_PLUGIN_MARKETPLACES` environment only before any Core policy has
  been saved. The last accepted policy remains in force during outages and after
  restarts. Defaults include the official Anthropic and OpenAI
  marketplaces. Entries that fail are
  skipped, never installed; one failing plugin never stops the others or the session. The start
  panel's `plugins` stage shows `2 of 2 installed` or a warning naming what failed. When the
  workshop itself looks broken — a marketplace that cannot be added or listed, an install path
  that does not check out — the daemon rebuilds it once (at most once an hour; it is a cache, and
  a daemon restart or a timeout can leave a half-cloned marketplace behind) and retries. One
  plugin that simply will not install is only reported. A launch spends at most five minutes on
  plugins.
- The install children get an allow-listed environment (PATH, HOME, locale, proxy and CA settings,
  `CLAUDE_CONFIG_DIR`, git's no-prompt settings) — never the session token, board token or
  initial prompt. HOME is your real home, so a private marketplace your git credentials can reach
  works; git never prompts.
- The workshop is a cache: delete it to start over. It is not part of a backup. Two daemons sharing
  one state dir (two people on one host) may race each other over it; give each its own
  `BLERG_RUNNER_DAEMON_STATE_DIR`.

A plugin runs with the session's full access once loaded, on the host or in the container; the
marketplace allow-list is the control. When a plugin does not show up in the session's **Skills &
plugins** panel, check the daemon log (`journalctl --user -u blerg-runner-daemon` on Linux) for
`daemon: plugins:` lines.

## Engines

The launch sheet's Run column has an Engine picker: **Claude** (default),
**Codex**, **Hermes** (Nous Research's Hermes Agent CLI), or **OpenClaw**.
Claude/Codex/Hermes work with any of the three runtimes and either session
kind (Terminal or Agent); OpenClaw currently only runs Agent-kind sessions on
**This machine** (see its own subsection below — the launch UI disables the
other combinations). A few things differ underneath:

`daemon/install.sh install` auto-detects which of the four are already set
up on this machine (a real login/token/provider found, not just the CLI
installed) and reports them before installing — purely informational, it
doesn't change what gets installed, but it means you find out what's
already usable without reading this section end to end first. Only Claude
gets an interactive setup prompt, and only when nothing else was detected
either — the other three are self-contained CLIs with their own setup
flows (see each one's own doc: Codex's own docs, Hermes's `hermes setup`,
OpenClaw's `openclaw onboard`).

- **Skip-permissions flag**: Claude uses `--dangerously-skip-permissions`;
  Codex uses `--dangerously-bypass-approvals-and-sandbox` (Codex's own docs
  say this is meant specifically for an externally-sandboxed environment —
  exactly what Local sandbox provides); Hermes uses `--yolo`. OpenClaw has no
  per-invocation equivalent at all — its approval policy is config-driven
  (`openclaw approvals` / `openclaw exec-policy`), so the launch sheet hides
  the toggle rather than showing one that does nothing.
- **Resuming after a crash**: Claude sessions can pin a session id upfront
  (`--session-id`) and resume it later (`--resume <id>`) after a hard daemon
  restart. New Codex **Terminal** sessions keep native conversations in a private
  per-session Codex home, preserving the existing login and settings. After tmux
  loss the daemon resumes that terminal's latest interactive thread by exact ID;
  subagent conversations cannot replace it. Missing or older untracked terminal
  history is refused explicitly, without replaying the initial task. Docker
  terminal sessions bind the same persistent home into their replacement container.
  Hermes terminals still start fresh with the stored initial prompt; Hermes's
  `-c/--continue` option belongs to its `chat` subcommand. Codex and Hermes **Agent** sessions
  track their own conversation identity between turns. Host Codex agent sessions
  persist their thread ID in private daemon recovery records and are reclaimed
  before reconnecting to the server. Eligible interrupted work is continued once,
  with the same destructive-command and open-question guards as Claude. A message
  from the person goes first. A missing transcript is reported explicitly before
  the agent starts fresh. Sandboxed, restricted and MCP-granted Codex sessions
  are not recovered. This applies to sessions started with the updated daemon;
  older Codex sessions have no recovery record. Hermes uses its own on-disk
  SQLite session store.
- **Codex Agent sessions** use a persistent `codex app-server` process rather
  than starting `codex exec` for each message. Messages can steer the active turn;
  interrupts retain the conversation. The session reports native skills, plugins,
  MCP inventory, observed tools, subagents, background work and thinking/compaction.
  The session images pin Codex 0.162.1; reinstall the workstation daemon to use the
  new adapter on the host. Existing managed MCP/restricted/cron policies remain
  separate from the capabilities a user's own Codex installation provides.
- **Tool visibility (Hermes Agent-kind only)**: Hermes's headless mode
  (`hermes chat --oneshot -Q`) has no structured event stream — verified by
  testing, not assumed — it prints only the final response text (or an error
  message) to stdout, nothing else. So a Hermes Agent session only ever emits
  `assistant_text` and `turn_done`; unlike Claude/Codex it never surfaces
  `tool_call`/`tool_result` events, and turn token-usage isn't tracked either.
  Terminal-kind Hermes sessions are unaffected (real TUI, same as claude/codex).
- **Model picker**: shows the engine's own model list (and an Effort choice
  for models that take one). Codex's account-aware list is probed from its CLI;
  the running app-server confirms its initial model and effort. Changes are
  acknowledged in the session and apply to subsequent turns. Engines without a
  model source use their own defaults. See runner/README.md's "Model lists".
- **Cluster pod auth**: the k8s runtime has no host filesystem to mount
  account state from, so each engine gets its credential a different way
  there — Claude via the `CLAUDE_CODE_OAUTH_TOKEN` env var (from
  `blerg-runner-agent`'s Secret), Codex via a `CODEX_AUTH_JSON` env var
  holding a snapshot of a logged-in `~/.codex/auth.json`, stripped of refresh authority and written to
  `$HOME/.codex/auth.json` by the pod on startup (Codex has no equivalent of
  a pure-token env var — confirmed by testing that a headless `codex exec`
  only needs that one file, not the rest of the interactive login state), and
  Hermes via a `HERMES_ENV_CONTENTS` env var holding the raw contents of
  `~/.hermes/.env` (provider API keys and everything else hermes reads from
  dotenv — there's no single bare token, so the whole file rides through),
  written to `$HOME/.hermes/.env` on startup. Hermes's non-secret
  model/provider choice (`~/.hermes/config.yaml`) instead rides the existing
  config-bundle mechanism alongside `CLAUDE.md`/skills. All three credential
  env vars are optional keys in the same Secret; a session picks whichever
  one its engine needs.

### Adding an engine after install

You don't need to reinstall Blerg to use an engine you install or log in to later. The daemon
looks for engines again on every heartbeat (about every 30 seconds), and you can make it look
right away.

1. Install the engine's CLI and log in, in your normal terminal on that machine:
   - **Claude Code**: install it (`npm install -g @anthropic-ai/claude-code`, or Anthropic's
     installer), then run `claude` once and log in. On macOS, where the login lives in the
     keychain, run `claude setup-token`, put the token in `install/desktop/.env` as
     `CLAUDE_CODE_OAUTH_TOKEN=…` and run `./daemon/install.sh install`.
   - **Codex**: `npm install -g @openai/codex`, then `codex login`.
   - **Hermes**: install it, then `hermes setup` to choose a model provider.
   - **OpenClaw**: install it, then `openclaw onboard`.
2. Open the launch sheet. Every engine is listed: one that can't run yet is shown disabled under
   **Not available here**, with the reason and the exact fix (for example "Codex is installed
   on blerg but not logged in. Run `codex login` on blerg, then Check again."). Select **Check
   again** to re-detect now instead of at the next heartbeat. **Settings → Engines** shows the
   same list for each of your desktops.
3. If the engine is installed but the daemon can't find it ("isn't on the daemon's PATH"),
   select **Re-detect PATH**. The daemon reads your login shell's PATH (the PATH your terminal
   gets, including what nvm, a custom npm prefix, Volta, asdf or Homebrew add), puts it ahead of
   its own and keeps it for later restarts. No restart is needed. When the reason names the
   folder ("Codex is installed at ~/.nvm/versions/node/v22.11.0/bin/codex …") and Re-detect
   PATH doesn't help, that folder isn't on your login shell's PATH either: add it in your shell
   profile (`~/.zprofile`, `~/.bash_profile` or `~/.profile`), then Re-detect PATH.

Without the app, the installer does the same:

- **Linux (systemd)**: `./daemon/install.sh refresh-path` reads your login shell's PATH again,
  writes it into `~/.config/systemd/user/blerg-runner-daemon.service` as `Environment=PATH=…`
  and restarts the daemon. It doesn't rebuild anything. If agent work is running it lists it and
  asks first (`--yes` goes ahead). Sessions it interrupts are continued after the restart, the
  same as after an install.
- **macOS (launchd)**: the same command writes `PATH` into the `EnvironmentVariables` of
  `~/Library/LaunchAgents/dev.blerg.runner-daemon.plist` and reloads the agent.

`install` itself captures the PATH too: it runs your login shell once (interactive first, then
plain; 10 seconds each, enforced without `timeout(1)` so a hanging profile can't stall the
install on stock macOS, with no input and none of the installer's tokens), keeps only absolute
folders made of plain path characters, puts them ahead of the defaults
(`~/.local/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin`, plus `/opt/homebrew/bin` on
macOS), drops duplicates and stops at 64. When the shell can't be read it says so and uses the
defaults.

**Which PATH wins.** Re-detect PATH (the app) saves what it read in the daemon's state folder
(`~/.blerg-runner-daemon/login-path.json`), and the daemon puts that ahead of its service PATH
at every start. `install` and `refresh-path` rewrite the same file with what they just read, so
the most recent capture wins, from the app or from the installer. When the installer can't read
your login shell it leaves the file alone. If the app can apply a PATH but can't save it (a
read-only or full state folder), it says so: the PATH works until the daemon restarts.

What each status means:

| Status | Meaning | Fix |
|---|---|---|
| Not found | the CLI isn't on the daemon's PATH (the reason names where it is when it's in a usual install folder, and lists the PATH searched) | install it, or Re-detect PATH |
| Not logged in | the CLI is there, its login isn't (`~/.codex/auth.json`, `~/.claude/.credentials.json`) | log in, then Check again |
| Not set up | logged in or installed, but not ready to run: Hermes has no provider, OpenClaw isn't onboarded | `hermes setup` / `openclaw onboard`, then Check again |
| Too old | the CLI is older than the oldest version Blerg supports | update it, then Check again |
| Not offered here | this runtime or session kind never runs it (OpenClaw in the Local sandbox or on the Cluster) | choose another runtime |
| Not connected | Cluster only: no credential for this engine | connect it in Settings |

The daemon reports facts only: a status, where a CLI is and the PATH it searched, with your home
folder written as `~`. It never reads a login's contents to report it. The server writes the
words you see.

### OpenClaw

OpenClaw (Agent-kind only, unsandboxed on the daemon host only for now) exists specifically
to reach self-hosted OpenAI-compatible inference — e.g. a local GPU box
running vLLM — rather than a CLI-native subscription login. Register it as a
custom OpenClaw provider once per box — easiest via the wizard:

```
./daemon/configure-openclaw-endpoint.sh
```

It prompts for a provider id, base URL, model id, display name, and context
window, probes the endpoint before writing anything, dry-runs the config
patch, and prints the exact `<provider>/<model>` string to use — safe to
re-run to add another endpoint or update an existing one. `daemon/install.sh`
suggests it automatically when openclaw is installed but has no provider
configured yet.

Or do it by hand:

```
openclaw config patch --file - <<'EOF'
{
  models: {
    providers: {
      mybox: {
        baseUrl: "http://<box-ip>:8000/v1",
        api: "openai-completions",
        apiKey: "not-needed",
        models: [
          { id: "qwen3-30b", name: "Qwen3 30B (mybox)", contextWindow: 32768 }
        ]
      }
    }
  }
}
EOF
```

Either way, launch with model `mybox/qwen3-30b` (substitute whatever provider
id you actually chose). The vLLM server must be started
with `--enable-auto-tool-choice --tool-call-parser hermes` (or whatever
parser matches the model family) — without it every turn 400s with `"auto"
tool choice requires --enable-auto-tool-choice and --tool-call-parser to be
set`, confirmed by testing.

**Turn model**: `openclaw agent exec "<msg>" --json` — chosen deliberately
over OpenClaw's other headless mode, `openclaw agent --local --session-id
<id> --message ...`, after testing both against a local OpenAI-compatible inference server:
`agent exec` gives a small, predictable system prompt and a stable JSON
envelope but is **completely stateless** (verified: two calls against the
same `--state-dir` got different session ids and no shared memory);
`--session-id` gives real pinnable continuity but ships a ~35K-char default
system prompt (all of OpenClaw's 40+ built-in tools, skills, bootstrap
files) that overflowed a 32K-context model before the conversation even
started. Since the whole point is reaching smaller self-hosted boxes, the
openclaw driver uses `agent exec` and replays a truncated transcript into
each turn's prompt itself.

**Replay budget**: `BLERG_OPENCLAW_CONTEXT_CHARS` (env var on the daemon
process, default 16000) caps how much prior conversation gets replayed per
turn, oldest turns dropped first. Raise it for bigger hardware — a
larger-context model, more or larger GPUs, or routing through a hosted API
instead of a small local one.

## Linux (systemd --user)

Installed automatically by `blerg-up.sh`. To manage it directly:

```
./daemon/install.sh install     # build + install + start
./daemon/install.sh status      # is it running, and is it connected?
./daemon/install.sh refresh-path  # give the service your login shell's PATH again, restart it
./daemon/install.sh uninstall   # stop + remove
journalctl --user -u blerg-runner-daemon -f   # logs
```

Requires `systemctl --user` (present on any systemd desktop, including WSL2 with
systemd enabled). If lingering isn't enabled, the service stops when you log out —
the installer tries `loginctl enable-linger $USER` for you; if that needs a
privilege it doesn't have, it prints a warning (the daemon is running, but stops
when you log out) and the fix: run `sudo loginctl enable-linger $USER` once.

The daemon binary is built as you, into `bin/` at the repo root. If an older
install (built in a container as root) left `bin/` owned by root, the installer
stops with the exact `sudo chown -R` to run.

**WSL2 without systemd** has no `systemd --user` session at all. `blerg-up.sh`
detects it and prints the fix: add `[boot]` and `systemd=true` (two lines) to
`/etc/wsl.conf`, run `wsl --shutdown` from Windows, reopen the distribution and
re-run `./blerg-up.sh`.

### WSL2: return after Windows restart

There are two separate startup steps. The enabled systemd user service plus
**lingering** starts the daemon when the distro boots, without a Linux login.
Windows must also start the distro after sign-in. Systemd services alone
[do not keep WSL alive](https://learn.microsoft.com/en-us/windows/wsl/systemd).

During interactive setup, the installer offers an optional Windows logon task
(default **No**). Noninteractive setup prints the opt-in command; `--yes` only
confirms replacing a busy daemon and does not opt in to Windows startup.
From `install/desktop` inside the distro, as your normal Linux account:

```bash
./daemon/install.sh wsl-startup enable   # opt in, without restarting the daemon
./daemon/install.sh wsl-startup status   # task name, state, result, log location
./daemon/install.sh wsl-startup disable  # remove this account/distro's task
# Or opt in while installing/upgrading the daemon:
./daemon/install.sh install --wsl-startup
```

**Disclosure:** enabling creates one `Blerg-WSL-<id>` task in Windows Task
Scheduler for the current Windows account, distro, and Linux account. PowerShell
runs without a profile and with a process-scoped execution-policy bypass;
organization policy still applies. The task runs at that Windows account's
sign-in, without elevation or a stored password,
and starts immediately on opt-in. A hidden Windows PowerShell process runs
`wsl.exe --distribution <distro> --user <linux-account> --exec /bin/sleep infinity`.
This keeps the distro and its background services running while signed in,
**including on battery**, even after you close all terminals. It uses resources
until Windows sign-out/shutdown or you disable the task. It does not edit
`.wslconfig`, Windows Startup folders, or WSL boot configuration.

The task's self-contained worker and bounded startup logs live under
`%LOCALAPPDATA%\Blerg\wsl\<id>\`. They contain no Blerg credentials. The
worker is stored on Windows so it can start before the distro is running;
it does not depend on the source checkout at sign-in. Re-running enable updates
the same task and worker. Task settings and a worker lock prevent duplicates.
A running worker picks up script updates at its next start/sign-in. Disable is
idempotent and retains logs; it does not stop the daemon or terminate WSL.
Daemon uninstall removes this task first; if Windows is inaccessible, fix interop
and retry uninstall, or delete the exact task shown by `wsl-startup status` in
Windows Task Scheduler before removing the Linux service.

**Prerequisites:** WSL2 with systemd support (WSL 0.67.6 or newer; run
`wsl --version` in Windows and `wsl --update` if needed), systemd enabled in
`/etc/wsl.conf`, an enabled daemon unit, and `Linger=yes` from
`loginctl show-user "$(id -u)" -p Linger`. If needed, run
`sudo loginctl enable-linger "$(id -un)"` once; enabling the Windows task refuses
to proceed without lingering. The distro needs `/bin/sleep` with `infinity`
support (standard on Ubuntu/Debian), Windows interop (`powershell.exe` and
`wslpath` on PATH), Windows PowerShell 5.1 with ScheduledTasks, and permission
to create your own interactive logon task. Start from the Windows account that
owns the registered distro. Distro names support letters, numbers, spaces,
dots, underscores, and hyphens (starting with a letter or number); other names
need a manually configured startup task.

**Recovery and diagnostics:** the Windows worker retries every 15 seconds after
WSL startup errors or exits, including a deliberate WSL shutdown. To keep WSL
stopped for maintenance, disable the task first. Task Scheduler also retries a
failed worker every minute (up to 999 times), without its default execution
time limit or battery restrictions. Check `wsl-startup status`, Task Scheduler's
History/last result, and the printed `startup.log` location for WSL errors.
`0x41301` (267009) means the keep-alive task is running; it does not confirm the
daemon connected. Once the distro is running, check:

```bash
systemctl --user is-enabled blerg-runner-daemon
loginctl show-user "$(id -u)" -p Linger
./daemon/install.sh status
journalctl --user -u blerg-runner-daemon -b --no-pager
```

The daemon unit retries process startup failures every 15 seconds without a
start rate limit. The daemon independently reconnects with exponential backoff
when the runner/network is unavailable, and reports its connection state in
`install.sh status`. A local Docker Desktop/Compose server must also start at
Windows sign-in (or start the server yourself); this task starts only the WSL
distro. A remote runner needs working network/DNS/VPN. Token refusal needs the
correct daemon token in `.env` and a service reinstall; retries cannot fix it.

**Unsupported configurations:** WSL1 or WSL without systemd needs migration to
WSL2/systemd or the manual terminal daemon below. If interop or Windows PATH
import is disabled, enable `[interop] enabled=true` and `appendWindowsPath=true`
in `/etc/wsl.conf` and restart WSL at a safe time, or run the copied
`wsl-startup.ps1 -Action enable -Distro "<distro>" -LinuxUser "<account>"` from
Windows PowerShell as the distro's Windows owner. If organization policy blocks
PowerShell or Task Scheduler, ask your administrator to provision an equivalent
interactive logon task that keeps a foreground WSL process alive. Without it,
start the distro manually after sign-in. Running before Windows sign-in or after
sign-out is not supported by this integration; use a separate always-on Linux
host for that requirement. Do not run setup as root or elevate the whole installer.

### Failed to connect to bus

There are two different causes, and `blerg-up.sh` tells them apart:

**Session not running at all** — your shell isn't a full login session with a
reachable systemd user bus (common over `su`, some containers, or SSH without
`pam_systemd`). `blerg-up.sh` tries to fix this itself: default
`XDG_RUNTIME_DIR` to `/run/user/$(id -u)`, then (if still unreachable) run
`sudo systemctl start user@$(id -u).service` and `sudo loginctl enable-linger`
for you — a normal sudo password prompt, nothing else is running under that
session yet so this is safe to automate. If that still fails (sudo declined,
wrong password, no sudo on this box), it stops immediately, before starting
anything, and prints the same two commands to run yourself, then
`./blerg-up.sh` again.

**Session running, but its bus never came up** — a known **WSL2** quirk: even
with `dbus-user-session` installed, `user@<uid>.service` can end up active
with no D-Bus session socket in `$XDG_RUNTIME_DIR`. The only fix is
`sudo systemctl restart user@$(id -u).service` — but this is NOT run
automatically, because restarting the user manager kills every *other*
process it's supervising too: other tmux sessions, background daemons, an
in-progress Claude Code session, anything. `blerg-up.sh` detects this specific
case and tells you the exact command, so you can decide when it's safe to run
it, rather than losing unrelated work to a script you ran for something else.

**Never run `sudo ./blerg-up.sh` or `sudo ./daemon/install.sh` to work around
either of these** — see below for why.

**Never run `sudo ./blerg-up.sh` or `sudo ./daemon/install.sh`** — `systemctl
--user` always operates on the *calling* user's session, so running the whole
script as root installs and runs the daemon under root's own systemd session
instead of yours: agent sessions would then execute as root, and it wouldn't
see your real `~/.claude` login or `~/repositories`. Both scripts refuse to
run as root for this reason; the sudo calls above are scoped to just the two
commands that need it.

### Claude auth

On Linux, if `CLAUDE_CODE_OAUTH_TOKEN` isn't set, the installer checks
`~/.claude/.credentials.json` for an existing login and, if found, proceeds
silently — nothing to do, the service (running as you, same `$HOME`) picks it
up the same way an interactive `claude` session would. That file's format is
an internal detail of the `claude` CLI, not a documented API, so a check that
comes back inconclusive (missing `jq`, unexpected shape, a future CLI version)
is treated as "can't tell," never as "not logged in" — it falls through to
asking instead of silently failing.

If you answer "no" (no token, not logged in), the installer saves
`BLERG_SKIP_CLAUDE_TOKEN_PROMPT=1` to `install/desktop/.env` and doesn't ask
again on later runs; delete that line to be asked again. `blerg-up.sh` also
prints a single up-front notice when no engine looks usable (Claude with a
login, Codex, Hermes or OpenClaw). It never blocks the install. On macOS a
Claude login in the keychain can't be seen from a script, so it says it could
not verify rather than that it is missing.

If neither a token nor an existing login is found, and you're at an
interactive terminal, the installer asks directly: confirm `claude` is
already logged in as you, or paste a token from `claude setup-token` right
there — it's saved to `.env` for next time. In a non-interactive context
(scripted, piped) it can't prompt, so it just warns and installs anyway.
macOS logins typically live in the system Keychain rather than a file, so this
detection only runs on Linux — see the macOS section above for that flow.

## macOS (launchd)

Installed automatically by `blerg-up.sh`. Same commands as above — `install.sh`
detects macOS and uses `launchctl` instead of `systemctl`:

```
./daemon/install.sh install     # build + install + start
./daemon/install.sh status
./daemon/install.sh uninstall
tail -f ~/Library/Logs/blerg/blerg-runner-daemon.log
```

**Auth note:** launchd agents run outside your interactive login session, so they
often can't reach a `claude` login stored in the system keychain. Get a token with
`claude setup-token` and export it before installing:

```
export CLAUDE_CODE_OAUTH_TOKEN=<token from claude setup-token>
./daemon/install.sh install
```

Requires either a local Go toolchain (1.26+) or Docker (used to cross-compile the
daemon binary for macOS/arm64 or macOS/amd64, whichever you're on).

## Is the daemon connected?

The daemon logs `daemon: connected to <server> as "<name>"` only once the server has said it
accepted the daemon token: from 0.2.1 the server sends a confirmation right after the daemon's
hello. A server older than 0.2.1 sends none, so the daemon waits 5 seconds; if the connection is
still open by then it is taken as accepted, and the log says it was `not confirmed by the server`.
A refused token is never logged as connected.

The daemon keeps the state of its connection in `~/.blerg-runner-daemon/connection.json` (mode
0600; `BLERG_RUNNER_DAEMON_STATE_DIR` moves it), and `./daemon/install.sh status` prints it after
the service's own status:

| State | What it means |
|---|---|
| `connecting` | starting, or reconnecting after the connection dropped; the detail says why |
| `connected` | the server accepted the daemon token |
| `unconfirmed` | the connection is open to a server older than 0.2.1, which does not confirm it |
| `refused` | the server refused the daemon token. It stays this way while the daemon retries, until a connection is up again |

The file holds the server's address, the time the state began, a short reason and the daemon's
process id; never a token. Only the daemon that owns the state directory writes it (a second one
started by hand does not), and a session pod in a cluster never writes one.

**What the installer checks.** `install.sh install` (and `blerg-up.sh`, which runs it) asks the
server whether it accepts the enrolled credential before it replaces the running
daemon (`GET /api/daemon/whoami`; the token goes to `curl` in a private header file, never on a
command line, and is never printed):

- **refused**: it stops with exit code 6 and changes nothing; a daemon running now keeps running
  as it was.
- **an older server** (no such check; it answers with its web page or a 404): it says the check
  was skipped and goes on.
- **the server cannot be reached**: it says so and goes on; the server may simply be down.

After the service restarts it waits up to 30 seconds (`BLERG_DAEMON_CONNECT_WAIT`) for the new
daemon's `connection.json`; a file the replaced daemon left is not taken as the answer.
`connected` and `unconfirmed` are fine. `refused` stops it with exit code 6. No answer in time is
a warning only, with the command to read the daemon's log; it never waits for a key press.

`blerg-up.sh` exits 6 too when the daemon token was refused: the stack is up, but the daemon is
not connected, and its last box says to enroll the desktop for its recorded owner and
run `./daemon/install.sh install`. The installer's messages never show a `user:password@` part of the server URL, and its
check ignores your `~/.curlrc`.

### The server refused the daemon token

The daemon reports close 4401 when its credential is expired, revoked or bound to an
old desktop owner/revision. Enroll again for the persisted owner (or **Add a desktop**
and run `./daemon/install.sh install --enroll <code>`), then rerun
`./daemon/install.sh install`. The private credential is
`~/.blerg-runner-daemon/desktop-credential.json`; the daemon prefers it over environment
credentials. See [Desktop ownership](../../docs/desktop-ownership.md) for hosted
issuance, admin assignment and the installer-account/first-admin upgrade path.

A refused daemon retries with backoff, but an obsolete credential cannot become valid
by reconnecting. Do not copy the install-wide server secret to repair a person-owned
desktop. The explicit legacy compatibility switch supports unowned desktops during
0.5 only and is removed in 0.6.

## Verifying an engine actually works

Unit tests (`internal/daemon/*driver_test.go`) exercise each driver against a
fake CLI stub — they catch regressions in the Go code, but they cannot catch
"the real CLI's args/output don't match what the driver expects," since the
stub always answers exactly as scripted. `internal/daemon/engine_integration_test.go`
closes that gap: one shared test, run against every engine in the registry,
that drives a real minimal turn through the real CLI and checks a real reply
comes back. It's opt-in (needs each engine's CLI installed and authenticated
on this machine, and spends real time/quota/GPU) and skipped by default:

```
BLERG_ENGINE_INTEGRATION_TEST=1 go test ./internal/daemon/ -run TestEngineIntegration -v -timeout 5m
```

OpenClaw needs an explicit model (no CLI-native default is guaranteed
valid) — set it with `BLERG_ENGINE_TEST_MODEL_OPENCLAW=<provider>/<model>`
to whatever you registered via `configure-openclaw-endpoint.sh` (the
in-code fallback if unset is just a placeholder and won't work on your
machine).
Hermes needs whatever local backend its `~/.hermes/config.yaml` points at
actually running — confirmed the hard way: this test failed with a real
connection error after the local `ollama serve` backing it had been shut
down for something unrelated, and passed again the moment it was restarted.
A new engine added to the registry is covered automatically — nothing to
update in the test itself.

## Anything else (manual)

No automated installer — `blerg-up.sh --no-daemon` (or a failed auto-install)
prints the exact commands to run the daemon in a foreground terminal instead:

```
export BLERG_RUNNER_SERVER_URL=ws://localhost:8083/ws/daemon   # your BLERG_PORT_RUNNER
# Enroll first; the daemon loads its private desktop-credential.json file.
export BLERG_RUNNER_REPOS_ROOT=<the directory your repos live in — required>
# either already logged into claude, or:
export CLAUDE_CODE_OAUTH_TOKEN=<from `claude setup-token`>
( cd ../../runner && GOWORK=off go run ./cmd/daemon )     # needs Go >= 1.26; or ./daemon/install.sh install builds it in Docker
```

It has to keep running (in that terminal, in `tmux`/`screen`, whatever you use) for
agent sessions to work — killing it just means no new sessions can spawn until you
start it again.

Running Blerg on a Kubernetes cluster instead? See [`install/k8s/README.md`](../k8s/README.md).

### Automatic continuation after usage limits

The server schedules **Resume when limit resets**, but the workstation daemon
must report the session's provider refusal and reset time. After upgrading Blerg,
run `install/desktop/daemon/install.sh install` from the updated checkout to rebuild
and restart the daemon. Restarting an old binary alone does not install the new
behavior. The option is in each owned Claude or Codex agent chat; see
[Resume after a usage limit](../../docs/talking-to-an-agent.md#resume-after-a-usage-limit).

### Adding to an existing session

The session panel's **Capabilities → Add plugin or skill** uses the same marketplace policy and
account settings as always-on plugins. **This session only** adds to that session's private
snapshot without changing the account list. **Always on** saves the engine-specific account entry;
on a compatible live session it can load there too.

Live application currently requires an updated desktop daemon and an unrestricted host Claude
agent session. The daemon waits until foreground turns and owned background work are idle,
installs a verified fresh snapshot, and resumes the conversation with the plugin directories.
It preserves the session's existing MCP configuration and permissions. A failed install retains
the old plugin snapshot. Codex, containers, cluster pods, restricted sessions and older daemons
cannot load it into the existing session through this path; the panel offers always-on changes
for new sessions and says so. Installing the updated daemon enables this path for supported
sessions; no separate change to the person's interactive CLI configuration is needed.

An agent's `request-plugin` command only asks the person. Approval is required before installing
or saving an account setting, including for cron and board-started sessions. Rejection or expiry
leaves both the running session and settings unchanged.

### Person-owned desktop credentials (0.5 upgrade)

Existing shared-credential desktops recorded at upgrade keep working automatically
through 0.5, including launch targets, children and file references. Fresh desktops
require a personal credential. Compatibility ends in 0.6.

**A new desktop** (a laptop for a new person, a second machine): in Blerg's
**Settings → Account → Your desktops**, select **Add a desktop**. It shows one
command with a one-time code; run it from a checkout of this repository on the new
machine, within 15 minutes:

```sh
BLERG_RUNNER_SERVER_URL=wss://runner.example.com/ws/daemon install/desktop/daemon/install.sh install --enroll <code>
```

The installer sends the code to the runner, which redeems it through core for a
credential bound to you and a new desktop identity, stores it privately in
`~/.blerg-runner-daemon/desktop-credential.json` and installs the service. The
code works once, expires, can be revoked from the same page and is never printed
or written to disk by the installer; no `.env` is needed (one is created, keeping
the server address). The desktop then shows as **Enrolled**. A refused code (wrong,
expired, revoked or already used) stops with exit code 7 and changes nothing.

**A desktop that already connects** (upgrade-era): update the daemon with
`daemon/install.sh install`, then open **Settings → Account → Your desktops** (a
session on that desktop links there) and select **Enroll**.
Review the hostname, OS user, connected-since time and server-observed source
address, then select **Confirm enrollment**. If the connection changes, review
its details and confirm again. The server delivers the credential over the existing authenticated connection;
the daemon stores it privately and reconnects. The button needs no copying,
internal key or reinstall. **Rename** and **Remove** are on the same page; Remove
disconnects the desktop and revokes its credential. Ask an administrator to assign the desktop first if
it belongs to another person.

If the button is unavailable, ask your installation operator to run
`daemon/enroll.py --export` on the component host as described in
[desktop ownership](../../docs/desktop-ownership.md). That helper writes a private
file locally using the internal component key; no browser response contains the
credential. Transfer the file privately to this desktop, then run:

```sh
chmod 600 /path/to/credential.json
daemon/install.sh import-credential /path/to/credential.json
daemon/install.sh install
```

The import never prints the credential. The all-in-one installer can still enroll
using its internal key and `BLERG_RUNNER_INSTALLING_ACCOUNT_ID` or the first enabled
admin. A fresh install must create its account first.
`BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS=auto` is the default; explicit `false` disables
shared sockets and `true` permits unowned legacy connections without granting
fresh desktops launch access. Neither override authenticates an owned desktop.

## Workspace 0.5.0 upgrade status

Update core, runner and the desktop daemon together and retain database backups.
Core migrations 026/027 add desktop credentials and shell policy; runner migrations
076–083 add selection, ownership, runtime, settlement, consent and completion state.
The installer builds the contained Linux workspace worker (protocol 3). Sandbox
builds use the repository root as their Docker build context.

Workspace v2 and owner shells remain gated off. Existing single-repository sessions
retain scalar contracts. A discovery-capable peer without workspace v2 refuses
checked-out selections, including a single repository, rather than losing source
identity through a bare-name downgrade. Sandbox file viewing requires an in-container
reader and returns unavailable when that binding is absent. Do not enable a
capability from discovery availability or a successful image build. See
[workspace integration](../../docs/workspace-integration.md) for remaining gates,
including physical Mac and Docker engine restart acceptance. Reinstalling does
not authorize deleting retained workspace allocations.
