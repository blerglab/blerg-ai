#!/usr/bin/env bash
# Proves the compose file derives every origin/port-bearing value from BLERG_PORT_* so a
# changed port cannot silently break login (audit M9). Needs docker compose; skips otherwise.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 || { echo "skip: docker compose unavailable"; exit 0; }
out="$(BLERG_PORT_CORE=9001 BLERG_PORT_BOARD=9002 BLERG_PORT_RUNNER=9003 \
       BLERG_CORE_INTERNAL_KEY=k BLERG_CORE_LOCAL_KEY=$(openssl rand -base64 32) \
       BLERG_BOARD_SECRET_KEY=board-secret-sentinel \
       docker compose --env-file /dev/null config)"
fail=0
for want in 'http://localhost:9001,http://localhost:9002,http://localhost:9003' \
            'http://localhost:9002=blerg-board' 'VITE_CORE_PORT: "9001"' \
            'BLERG_CORE_PUBLIC_URL: http://localhost:9001' 'BLERG_BOARD_AGENT_URL: http://localhost:9002' \
            'RUNNER_UI_BASE: http://localhost:9003/sessions' 'BLERG_RUNNER_DATA_DIR: /data' \
            'BLERG_RUNNER_CORE_INTERNAL_KEY: k' 'pg_isready -U blerg -d blerg_core' \
            'BLERG_CORE_COOKIE_SECURE: "false"' 'BLERG_BOARD_SECRET_KEY: board-secret-sentinel'; do
  grep -qF -- "$want" <<<"$out" || { echo "FAIL: $want"; fail=1; }
done
if grep -E 'localhost:808[123]' <<<"$out" | grep -qv '^#'; then echo "FAIL: a literal default port survived a port override"; fail=1; fi

# R2: ports are published on loopback unless BLERG_BIND_ADDR says otherwise.
grep -qE 'host_ip: 127\.0\.0\.1' <<<"$out" || { echo "FAIL: ports not bound to 127.0.0.1 by default"; fail=1; }
out_lan="$(BLERG_BIND_ADDR=0.0.0.0 BLERG_CORE_INTERNAL_KEY=k BLERG_CORE_LOCAL_KEY=$(openssl rand -base64 32) docker compose --env-file /dev/null config)"
grep -qE 'host_ip: 0\.0\.0\.0' <<<"$out_lan" || { echo "FAIL: BLERG_BIND_ADDR override not applied"; fail=1; }

# Sandbox network: the daemon attaches sandbox containers to a network it finds by its literal
# name `blerg-sandbox`, so the name must be pinned (not project-prefixed). Only board and runner
# may be on it — a sandbox must never reach Postgres or core. Board and runner stay on default
# too, or they would lose Postgres/core themselves.
if command -v python3 >/dev/null 2>&1; then
  net_out="$(BLERG_CORE_INTERNAL_KEY=k BLERG_CORE_LOCAL_KEY=$(openssl rand -base64 32) \
             docker compose --env-file /dev/null config --format json | python3 -c '
import json, sys
c = json.load(sys.stdin)
nets = c.get("networks") or {}
bad = []
if (nets.get("sandbox") or {}).get("name") != "blerg-sandbox":
    bad.append("network `sandbox` is not declared with name blerg-sandbox")
for svc, want in (("blerg-board", True), ("blerg-runner", True), ("postgres", False), ("blerg-core", False)):
    on = set((c["services"][svc].get("networks") or {}).keys())
    if want and not {"default", "sandbox"} <= on:
        bad.append(svc + " is not on both default and sandbox (on: " + ",".join(sorted(on)) + ")")
    if not want and "sandbox" in on:
        bad.append(svc + " must NOT be on the sandbox network")
print("\n".join(bad))
')"
  [ -z "$net_out" ] || { while IFS= read -r l; do echo "FAIL: $l"; done <<<"$net_out"; fail=1; }
else
  echo "skip: python3 unavailable, sandbox network checks not run"
fi


# R4 (desktop-security I1): .env.example's uncommented keys must be exactly what blerg-up.sh
# generates (plus the two origin vars it derives separately) — a stale .env.example drifting
# from the generator is how a missing key (e.g. BLERG_CORE_REGISTER_KEY) went unnoticed until a
# live install hit 503 registration retries. Optional keys stay commented (# KEY=) on purpose and
# are excluded from this comparison.
gen_keys="$(sed -n '/cat > .env <<ENVEOF/,/^ENVEOF/p' blerg-up.sh | grep -oE '^[A-Z_]+=' | sort -u)"
ex_keys="$(grep -oE '^[A-Z_]+=' .env.example | sort -u)"
want_keys="$(printf '%s\nBLERG_CORE_ALLOWED_RETURN_ORIGINS=\nBLERG_CORE_ORIGIN_AUDIENCES=\n' "$gen_keys" | sort -u)"
if ! diff <(echo "$want_keys") <(echo "$ex_keys") >/dev/null; then echo "FAIL: .env.example keys differ from what blerg-up.sh generates"; diff <(echo "$want_keys") <(echo "$ex_keys") || true; fail=1; fi
if grep -qiE '^[A-Z_]+=change-?me' .env.example; then echo "FAIL: .env.example still ships placeholder secrets"; fail=1; fi

# BLERG_BOARD_SECRET_KEY: generated with openssl rand -base64 32 on a fresh .env, added (never replaced)
# on an existing one, and validated as base64 of 32 bytes.
grep -q '^BLERG_BOARD_SECRET_KEY=$(openssl rand -base64 32)' blerg-up.sh || { echo "FAIL: blerg-up.sh does not generate BLERG_BOARD_SECRET_KEY"; fail=1; }
grep -q "grep -q '^BLERG_BOARD_SECRET_KEY=.' .env" blerg-up.sh || { echo "FAIL: blerg-up.sh must only generate BLERG_BOARD_SECRET_KEY when absent"; fail=1; }
grep -q 'check_b64_key BLERG_BOARD_SECRET_KEY' blerg-up.sh || { echo "FAIL: blerg-up.sh does not validate BLERG_BOARD_SECRET_KEY"; fail=1; }

for s in blerg-up.sh daemon/install.sh; do grep -q '^umask 077' "$s" || { echo "FAIL: $s lacks umask 077"; fail=1; }; done

# Regression guard for the "tighten an existing secret file's permissions"
# lines added to both scripts: each one is `[ -f X ] && chmod 600 X
# 2>/dev/null || true`. Under `set -euo pipefail`, errexit fires on the
# final command of an `&&` list too — a bare `[ -f X ] && chmod 600 X
# 2>/dev/null` (no trailing `|| true`) aborts the WHOLE script the moment
# chmod fails (read-only home, NFS, a file owned by someone else), the
# opposite of the intended best-effort behaviour. Static check: every such
# line in both scripts must end in `|| true`.
want_tighten_lines=4  # 3 in install.sh (env/unit/plist) + 1 in blerg-up.sh (.env)
got_tighten_lines="$(grep -c '\[ -f .*\] && chmod 600 .* || true' blerg-up.sh daemon/install.sh | awk -F: '{s+=$2} END{print s+0}')"
[ "$got_tighten_lines" -ge "$want_tighten_lines" ] || { echo "FAIL: expected at least $want_tighten_lines '[ -f X ] && chmod 600 X ... || true' lines, found $got_tighten_lines"; fail=1; }
if grep -nE '\[ -f .*\] && chmod 600 [^|]*2>/dev/null[[:space:]]*$' blerg-up.sh daemon/install.sh; then
  echo "FAIL: a tighten-permissions chmod is missing its trailing '|| true' (errexit would abort the script on a real chmod failure)"
  fail=1
fi

# Behavioural check, not just static grep: prove the exact pattern actually
# survives a real chmod failure under set -euo pipefail (a faked chmod that
# always fails stands in for a read-only home / NFS / wrong-owner file,
# which we can't reliably provoke as an unprivileged test).
tighten_behaviour="$(
  fakebin="$(mktemp -d)"
  cat > "$fakebin/chmod" <<'FAKECHMOD'
#!/usr/bin/env bash
exit 1
FAKECHMOD
  chmod +x "$fakebin/chmod"
  target="$(mktemp)"
  PATH="$fakebin:$PATH" bash -c 'set -euo pipefail; [ -f "$1" ] && chmod 600 "$1" 2>/dev/null || true; echo survived' _ "$target"
  rc=$?
  rm -rf "$fakebin" "$target"
  exit "$rc"
)" || true
[ "$tighten_behaviour" = "survived" ] || { echo "FAIL: tighten-permissions pattern did not survive a failing chmod under set -euo pipefail (got: $tighten_behaviour)"; fail=1; }

# MCP gateway: set on the runner, dialled by sandbox containers as blerg-runner:8090 on the
# blerg-sandbox network, and NEVER published on the host (the runner publishes exactly one port).
for want in 'BLERG_RUNNER_MCP_GW_ADDR: :8090' 'BLERG_RUNNER_MCP_GW_URL: http://blerg-runner:8090' \
            'BLERG_RUNNER_MCP_ALLOW_HTTP_HOSTS: ""' 'BLERG_RUNNER_MCP_ALLOW_PRIVATE_HOSTS: ""' \
            'BLERG_CORE_MCP_ALLOW_HTTP_HOSTS: ""' 'BLERG_CORE_MCP_ALLOW_PRIVATE_HOSTS: ""'; do
  grep -qF -- "$want" <<<"$out" || { echo "FAIL: $want"; fail=1; }
done
if command -v python3 >/dev/null 2>&1; then
  gw_out="$(BLERG_CORE_INTERNAL_KEY=k BLERG_CORE_LOCAL_KEY=$(openssl rand -base64 32) \
            docker compose --env-file /dev/null config --format json | python3 -c '
import json, sys
c = json.load(sys.stdin)
bad = []
for svc, s in c["services"].items():
    for p in s.get("ports") or []:
        if "8090" in (str(p.get("target")), str(p.get("published"))):
            bad.append(svc + " publishes the MCP gateway port " + json.dumps(p))
if len(c["services"]["blerg-runner"].get("ports") or []) != 1:
    bad.append("blerg-runner must publish exactly one port (its UI/API)")
print("\n".join(bad))
')"
  [ -z "$gw_out" ] || { while IFS= read -r l; do echo "FAIL: $l"; done <<<"$gw_out"; fail=1; }
fi
grep -q 'BLERG_RUNNER_MCP_GW_URL' .env.example || { echo "FAIL: .env.example does not document the MCP gateway variables"; fail=1; }

[ "$fail" -eq 0 ] && echo ok
exit "$fail"
