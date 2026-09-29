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
all hold the daemon token and, if you gave one, your Claude OAuth token. Both
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

**Setting a daemon option.** The daemon never reads `.env` itself; it reads
its service environment (the systemd unit's `Environment=` lines, or the
plist's `EnvironmentVariables`). `install.sh install` writes that environment,
and it copies `BLERG_RUNNER_PROVISION_CLAUDE_MD` and
`BLERG_RUNNER_ALLOW_HOST_CREDENTIAL_CLONE` into it when either is set, from
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
on a daemon: each session runs in a throwaway Docker container with the repo
mounted at `/workspace`. Your `~/.claude` and `~/.codex` are mounted read-write
so the engine can use your login — the sandbox does NOT protect those
credentials or your host Claude settings. Network is open. "This machine,
unsandboxed" (bare host, full filesystem access) is one click away in the
launch sheet's Run column for anyone who wants it, behind an explicit
acknowledgement — it is never a default while a sandbox is available.

A session started through the v1 contract (`POST /api/runner/start` — Board
session, card spawn, **Run board**, the MCP `start_session` tool) gets the same
default: with no `runtime` in the request it runs in the **local sandbox**
whenever the chosen daemon reports the sandbox image available, and on the
bare host only when it doesn't. `runtime: daemon` is still an explicit choice,
and an operator can pin every board-started session to the host by setting
`BLERG_BOARD_RUNNER_RUNTIME=daemon` on the board. That contract has no "bypass
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
push. Push from the host yourself (the commits are in your repo), or choose
**This machine** for a session that must push on its own.

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

Started with `--cap-drop ALL --security-opt no-new-privileges --pids-limit
512 --memory 4g`; no `--restart` — a sandbox stopped by a reboot is
recreated by the daemon from its session record on the next launch.

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
sweeps `blerg-sandbox-*`: any container that is not a session it is managing,
and has no tmux server inside it, is `docker rm -f`'d. When a session ends —
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
docker build --build-arg UID=$(id -u) -t blerg-runner-sandbox:latest runner/sandbox
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
is your **filesystem** (nothing outside `/workspace` and the mounted account
dirs is reachable) and **process boundary**, not your account data.

**Network**: full outbound access (v1) — nothing is blocked. This means
package installs, anonymous git fetches, and the engine's own API calls all
just work (a push needs credentials the sandbox doesn't have — see above),
but a sandboxed session is not isolated from exfiltrating data over the
network or hitting other services reachable from your machine. Revisit if
that tradeoff stops being acceptable for your use case.

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
  restart. Codex and Hermes have no equivalent for an interactive **Terminal**
  session — they assign their own session identity on first spawn, and pairing
  Hermes's own `-c/--continue <name> --create-if-missing` (which *could* pin
  one upfront) only exists on its `chat` subcommand, not plain interactive
  `hermes` — so a recovered Codex or Hermes Terminal session always starts
  fresh with the stored initial prompt, the same fallback already used when a
  Claude transcript is missing. Codex and Hermes **Agent** (headless) sessions
  do track their own conversation identity between turns — Codex via a thread
  id captured from its own output, Hermes via `--continue <blerg-session-id>
  --create-if-missing` (deterministic: no id to capture) — but only in memory
  for Codex, or in Hermes's own on-disk SQLite session store for Hermes; either
  way a daemon restart loses the in-memory Codex case and starts fresh on the
  next message, matching Claude Code's own headless driver in the same
  situation.
- **Tool visibility (Hermes Agent-kind only)**: Hermes's headless mode
  (`hermes chat --oneshot -Q`) has no structured event stream — verified by
  testing, not assumed — it prints only the final response text (or an error
  message) to stdout, nothing else. So a Hermes Agent session only ever emits
  `assistant_text` and `turn_done`; unlike Claude/Codex it never surfaces
  `tool_call`/`tool_result` events, and turn token-usage isn't tracked either.
  Terminal-kind Hermes sessions are unaffected (real TUI, same as claude/codex).
- **Model picker**: shows the engine's own model list (and an Effort choice
  for models that take one). Today only Claude has one — what Claude Code's
  `/model` offers, fetched live by the runner server — so picking Codex,
  Hermes or OpenClaw hides it and lets the engine use your account's own
  default model. See runner/README.md's "Model lists".
- **Cluster pod auth**: the k8s runtime has no host filesystem to mount
  account state from, so each engine gets its credential a different way
  there — Claude via the `CLAUDE_CODE_OAUTH_TOKEN` env var (from
  `blerg-runner-agent`'s Secret), Codex via a `CODEX_AUTH_JSON` env var
  holding the raw contents of a logged-in `~/.codex/auth.json`, written to
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
./daemon/install.sh status      # is it running?
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

Requires either a local Go toolchain (1.25+) or Docker (used to cross-compile the
daemon binary for macOS/arm64 or macOS/amd64, whichever you're on).

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
export BLERG_RUNNER_DAEMON_TOKEN=<BLERG_RUNNER_DAEMON_TOKEN from install/desktop/.env>
export BLERG_RUNNER_REPOS_ROOT=<the directory your repos live in — required>
# either already logged into claude, or:
export CLAUDE_CODE_OAUTH_TOKEN=<from `claude setup-token`>
( cd ../../runner && GOWORK=off go run ./cmd/daemon )     # needs Go >= 1.25; or ./daemon/install.sh install builds it in Docker
```

It has to keep running (in that terminal, in `tmux`/`screen`, whatever you use) for
agent sessions to work — killing it just means no new sessions can spawn until you
start it again.

Running Blerg on a Kubernetes cluster instead? See [`install/k8s/README.md`](../k8s/README.md).
