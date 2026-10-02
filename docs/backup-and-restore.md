# Backup and restore

What state a Blerg install holds, how to back it up, and how to get it back. There are two
installs and they keep state in different places: the [desktop install](#desktop-install)
(Docker Compose plus a daemon on your machine) and the
[Kubernetes install](#kubernetes-install).

> **Status of this page.** The commands were checked by reading them against
> `install/desktop/docker-compose.yml`, `install/desktop/blerg-up.sh` and the manifests in
> `install/k8s/`. The restore drill below **has not been executed**: it was written without
> access to Docker or a cluster. Run the drill once, on a scratch machine, before you rely on
> this page.

## What matters, in one paragraph

Three things hold everything: the **databases** (accounts, boards and cards, sessions, and every
stored credential in encrypted form), the **secrets** (above all `BLERG_CORE_LOCAL_KEY`, the key
that decrypts those credentials), and, on the desktop, the **daemon's small state on your
machine**. Images, the sandbox image, session containers and Jobs are rebuilt or recreated and
need no backup. A database backup without `BLERG_CORE_LOCAL_KEY` restores accounts and boards but
not credentials; a key without a database is useless. The board has a second key of the same
kind, `BLERG_BOARD_SECRET_KEY`, which encrypts each board's automation token in the board
database; without it those tokens are unreadable and must be minted and saved again (nothing
else is lost).

Treat backup files as secrets: `.env` and `blerg-secrets.json` hold every secret of the
install, and the SQL dumps hold encrypted credentials plus session and automation tokens. Write
them with `umask 077`, keep them off shared drives and out of git.

## What if `BLERG_CORE_LOCAL_KEY` is lost

There is no recovery. The key exists only in `install/desktop/.env` (desktop) or the
`blerg-secrets` Secret (Kubernetes); it is never stored in the database. Core still starts and
everything else works, but every stored credential (engine logins, GitHub and GitLab tokens)
is unreadable. Each person must sign in and enter them again on the **Settings** page, and any
board automation token has to be minted and pasted again. Generating a replacement key
(`openssl rand -base64 32`) is enough to make credentials storable again, but it does not bring
the old ones back. Rotating the key is equally destructive in this version, so do not do it on a
whim ([`core/docs/CONFIG.md`](../core/docs/CONFIG.md)).

## Desktop install

### What state exists

| What | Where | Back up? |
|---|---|---|
| Databases `blerg_core`, `blerg_board`, `blerg_runner` in one Postgres 16 (user `blerg`) | Docker volume behind the `postgres` service (`blerg-desktop-postgres` in `install/desktop/docker-compose.yml`) | **Yes**, with `pg_dump` |
| `install/desktop/.env`: `BLERG_CORE_LOCAL_KEY`, the service keys, the daemon token, ports, and your Claude OAuth token if you gave one | `install/desktop/.env` (mode 0600) | **Yes.** It is the only copy of the key |
| Daemon settings, such as the repos folder you chose | `~/.blerg-runner-daemon/settings.json` (or `$BLERG_RUNNER_DAEMON_STATE_DIR/settings.json`) | Yes, it is tiny. Without it the daemon falls back to `BLERG_RUNNER_REPOS_ROOT` from `.env` |
| Session recovery records (they carry session tokens) | `<repos root>/.blerg-runner/sessions/` | Optional. Only useful together with a database restored from the same moment |
| Runner data: uploaded agent-config bundles, published mockups, files agents published (`artifacts/`) | Docker volume `blerg-desktop-runner-data`, mounted at `/data` in the runner | Back it up to keep published and uploaded files: they cannot be regenerated, and without the volume the Files panel still lists them but they cannot be downloaded |
| Your repositories | the repos root | They are yours, and in git. Back them up the way you back up any code |
| The sandbox image `blerg-runner-sandbox:latest`, the three `desktop-blerg-*` images | Docker | No. `./blerg-up.sh` rebuilds them |

The Postgres password on the desktop is the fixed development value `blerg`; the database is not
published on a host port, so it is not part of the secrets to protect. Volume names are prefixed
with the Compose project name (`desktop`, from the directory), so the full name is usually
`desktop_blerg-desktop-postgres`; `docker volume ls` shows yours.

### Back up

Run from `install/desktop`. Stopping the three services first makes the three dumps describe the
same moment; skip that line for a quick backup of a running stack.

```sh
umask 077
dir="$HOME/blerg-backup-$(date +%F)"; mkdir -p "$dir"

docker compose stop blerg-core blerg-board blerg-runner     # optional, for a consistent set
for db in blerg_core blerg_board blerg_runner; do
  docker compose exec -T postgres pg_dump -U blerg -d "$db" > "$dir/$db.sql"
done
docker compose start blerg-core blerg-board blerg-runner

cp .env "$dir/env"
cp "$HOME/.blerg-runner-daemon/settings.json" "$dir/daemon-settings.json" 2>/dev/null || true

# Optional: the runner's data volume (find the exact name with `docker volume ls`).
docker run --rm -v desktop_blerg-desktop-runner-data:/data -v "$dir":/backup \
  debian:bookworm-slim tar czf /backup/runner-data.tgz -C /data .

ls -l "$dir"
```

`-T` matters: without it Compose allocates a terminal and the dump can pick up carriage
returns. **Checkpoint:** `blerg_core.sql`, `blerg_board.sql` and `blerg_runner.sql` are all
larger than a few kilobytes and `env` is present. To be sure a dump is complete, its last lines
read `-- PostgreSQL database dump complete`: `tail -n 2 "$dir/blerg_core.sql"`.

The daemon token lives in `.env` and is baked into the installed daemon service, so restoring
the same `.env` keeps that service valid.

### Restore

The idea: bring `.env` back first, so the stack reuses the same secrets (above all the key), then
load the databases while no service is connected, then start everything. Run from
`install/desktop` on the machine you are restoring to, with the repository checked out.

1. Put the secrets back.

   ```sh
   cp "$dir/env" .env && chmod 600 .env
   mkdir -p ~/.blerg-runner-daemon && chmod 700 ~/.blerg-runner-daemon
   cp "$dir/daemon-settings.json" ~/.blerg-runner-daemon/settings.json && chmod 600 ~/.blerg-runner-daemon/settings.json
   ```

2. Start only Postgres. On a new volume its init script creates the three empty databases; on a
   machine that already ran Blerg, stop the services and recreate the databases as below.

   ```sh
   docker compose stop blerg-core blerg-board blerg-runner 2>/dev/null || true
   docker compose up -d --wait postgres
   for db in blerg_core blerg_board blerg_runner; do
     docker compose exec -T postgres psql -U blerg -d postgres \
       -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" \
       -c "CREATE DATABASE $db OWNER blerg"
   done
   ```

3. Load the dumps.

   ```sh
   for db in blerg_core blerg_board blerg_runner; do
     docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U blerg -d "$db" < "$dir/$db.sql"
   done
   ```

4. Optional: the runner data volume.

   ```sh
   docker volume create desktop_blerg-desktop-runner-data
   docker run --rm -v desktop_blerg-desktop-runner-data:/data -v "$dir":/backup \
     debian:bookworm-slim tar xzf /backup/runner-data.tgz -C /data
   ```

5. Start everything. The script reuses your `.env`, rebuilds the images, runs any newer database
   migrations on boot and reinstalls or restarts the daemon.

   ```sh
   ./blerg-up.sh
   ```

6. Check it worked: see [the drill](#restore-drill-checklist).

Restoring the daemon's session recovery records is only worth doing when the databases come from
the same moment; leave `<repos root>/.blerg-runner/` alone otherwise. A record whose session the
database no longer knows is simply not recovered.

A fresh machine with no backup, but with the key in hand, is the same procedure minus the
dumps: put `.env` back and run `./blerg-up.sh`. You get a new, empty install that can read
nothing that was stored before, so this is only useful for the key.

## Kubernetes install

The Kubernetes procedure is in [`install/k8s/README.md`](../install/k8s/README.md#backup-restore-and-uninstall),
and [`install/k8s/AGENT-INSTALL.md`](../install/k8s/AGENT-INSTALL.md) asks for the same backup
before an upgrade. This section is the same procedure, plus what those pages leave out. Every
command uses namespace `blerg`, the Postgres StatefulSet pod `postgres-0` and user `blerg`, as
in `install/k8s/postgres-statefulset.yaml`.

### What state exists

| What | Where | Back up? |
|---|---|---|
| Databases `blerg_core`, `blerg_board`, `blerg_runner` | PVC `postgres-data-postgres-0` in `blerg` (5Gi) | **Yes**, with `pg_dump` (or a volume snapshot if your StorageClass offers one) |
| `blerg-secrets`: `BLERG_CORE_LOCAL_KEY`, `POSTGRES_PASSWORD`, the service keys, the daemon token | Secret in namespace `blerg` | **Yes.** Losing `POSTGRES_PASSWORD` locks the components out of the database |
| `blerg-runner-agent`: the daemon token and any engine credential you pinned in `.env` | Secret in namespace `blerg-runner-sessions` | Only if you pinned credentials or manage it yourself (`AGENT_SECRET_EXTERNAL`). Otherwise the next `deploy.sh` recreates it |
| `install/k8s/.env`: `REGISTRY`, `TAG`, `DOMAIN` and any `SECRET_*` pins | your checkout, not the cluster | Yes, it is small |
| Runner data (agent-config bundles, mockups, files agents published and files you uploaded) | `emptyDir` in the runner pod | Lost when the runner pod restarts, by design: the Files panel keeps listing them afterwards, but they cannot be downloaded. Give the runner a volume if you want them kept |
| Session Jobs, their per-session Secrets, the images | cluster and registry | No |

### Back up

Run from the repository root.

```sh
umask 077
dir="$HOME/blerg-backup-$(date +%F)"; mkdir -p "$dir"
for db in blerg_core blerg_board blerg_runner; do
  kubectl -n blerg exec postgres-0 -- pg_dump -U blerg -d "$db" > "$dir/$db.sql"
done
kubectl -n blerg get secret blerg-secrets -o json \
  | jq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.managedFields, .metadata.annotations)' \
  > "$dir/blerg-secrets.json"

# Only if you pinned engine credentials or manage this Secret yourself:
kubectl -n blerg-runner-sessions get secret blerg-runner-agent -o json \
  | jq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.managedFields, .metadata.annotations)' \
  > "$dir/blerg-runner-agent.json"

if [ -f install/k8s/.env ]; then cp install/k8s/.env "$dir/k8s-env"; fi
ls -l "$dir"
```

**Checkpoint:** every file is non-empty and `-rw-------`. `blerg-secrets.json` holds every
secret of the install in base64, so store it like a password vault.

### Restore

Restore the Secret **before** the first `deploy.sh` run, which then reuses it rather than
generating new values, and the databases before anyone uses the stack. Run from `install/k8s`.

```sh
kubectl create namespace blerg
kubectl apply -f "$dir/blerg-secrets.json"
if [ -f "$dir/k8s-env" ]; then cp "$dir/k8s-env" .env; fi
./deploy.sh
kubectl -n blerg scale deploy/blerg-core deploy/blerg-board deploy/blerg-runner --replicas=0
kubectl -n blerg wait --for=delete pod -l 'app in (blerg-core,blerg-board,blerg-runner)' --timeout=120s
for db in blerg_core blerg_board blerg_runner; do
  kubectl -n blerg exec postgres-0 -- psql -U blerg -d postgres \
    -c "DROP DATABASE IF EXISTS $db" -c "CREATE DATABASE $db"
  kubectl -n blerg exec -i postgres-0 -- psql -v ON_ERROR_STOP=1 -U blerg -d "$db" < "$dir/$db.sql"
done
kubectl -n blerg scale deploy/blerg-core deploy/blerg-board deploy/blerg-runner --replicas=1
```

`deploy.sh` creates the `blerg-runner-agent` Secret again on its own; if you keep it yourself,
`kubectl apply -f "$dir/blerg-runner-agent.json"` before deploying. If the namespace already
holds a working install you are rolling back, skip `create namespace`, and take a fresh backup
first.

## Restore drill checklist

Do this on a scratch machine or a scratch namespace, never on the live install, and record the
date you last did it.

1. Take a backup as above and copy the backup directory to the scratch machine.
2. On the scratch machine, with nothing running (`docker compose down -v` in `install/desktop`,
   or a new namespace), restore with the steps above.
3. Sign in with an existing account and password. **Expect** success, not a first-boot admin
   prompt: if core prints a fresh `BLERG_BOOTSTRAP_ADMIN_PASSWORD` the database did not load.
4. Open **Settings**. **Expect** your stored credentials to show as connected. If they do not,
   the key in the restored `.env` or Secret is not the one they were encrypted with.
5. Open a board and check that cards and columns are there.
6. Open **Runner**: past sessions are listed, and a new **Launch** starts and reaches idle.
   On the desktop the daemon shows as connected.
7. Write down what you had to fix, and update this page.

**This drill was not executed while writing this page.** Until someone has run it, treat the
procedure as reviewed, not proven.
