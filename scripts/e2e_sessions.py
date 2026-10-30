#!/usr/bin/env python3
"""Real-install session acceptance runner. Uses only Python's standard library."""
import argparse
import contextlib
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import time
import urllib.error
import urllib.request
import uuid

BAD_LOG = re.compile(r"writer freeze|admission closed|app-server could not start|broken pipe|not recovered", re.I)


def snapshot(repo):
    """Include ignored files and raw index bytes, not just Git's stat cache."""
    def git(*args):
        return subprocess.check_output(["git", "-C", str(repo), *args])
    index = Path(os.fsdecode(git("rev-parse", "--path-format=absolute", "--git-path", "index")).strip())
    files = {}
    names = git("ls-files", "-z", "--cached", "--others").split(b"\0")
    for name in names:
        if not name:
            continue
        path = repo / os.fsdecode(name)
        if path.is_symlink():
            files[os.fsdecode(name)] = ["link", os.readlink(path)]
        elif path.is_file():
            files[os.fsdecode(name)] = [path.stat().st_mode, hashlib.sha256(path.read_bytes()).hexdigest()]
        else:
            files[os.fsdecode(name)] = ["absent"]
    return [git("rev-parse", "HEAD").hex(), git("symbolic-ref", "HEAD").hex(),
            index.read_bytes().hex(), files]


class Run:
    def __init__(self, args):
        self.args = args
        self.token = os.environ["E2E_AGENT_TOKEN"]
        self.cookie = os.environ["E2E_OWNER_COOKIE"]
        self.base = os.environ["E2E_RUNNER_URL"].rstrip("/")
        self.repo = Path(os.environ["E2E_CHECKOUT"]).resolve()
        self.log = Path(os.environ["E2E_DAEMON_LOG"])
        self.offset = self.log.stat().st_size
        self.log_inode = self.log.stat().st_ino
        self.out = Path(args.logs).resolve()
        self.out.mkdir(parents=True, exist_ok=False, mode=0o700)
        self.before = snapshot(self.repo)
        self.rows = []
        self.parents = []
        self.seq = {}
        self.serial = 0

    def save(self, name, value):
        # Credentials are never saved, even if a remote service echoes one.
        text = json.dumps(value, indent=2)
        for secret in (self.token, self.cookie):
            text = text.replace(secret, "[redacted]")
        (self.out / name).write_text(text)

    def api(self, method, path, body=None, human=False):
        headers = {"Content-Type": "application/json", "Idempotency-Key": str(uuid.uuid4())}
        headers.update({"Cookie": self.cookie} if human else {"Authorization": "Bearer " + self.token})
        req = urllib.request.Request(self.base + path, data=None if body is None else json.dumps(body).encode(), headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=60) as response:
                data = response.read()
                value = json.loads(data) if data else {}
                status = response.status
        except urllib.error.HTTPError as error:
            # Do not persist request headers or an error page that could echo them.
            raise RuntimeError(f"{method} {path}: HTTP {error.code}") from None
        self.serial += 1
        self.save(f"{self.serial:05d}.json", {"method": method, "path": path, "status": status, "response": value})
        return status, value

    def check(self):
        assert snapshot(self.repo) == self.before, "parent checkout changed (HEAD, branch, index or tree)"
        assert self.log.stat().st_ino == self.log_inode and self.log.stat().st_size >= self.offset, "daemon log rotated; supply a continuous capture"
        with self.log.open("rb") as stream:
            stream.seek(self.offset)
            text = stream.read().decode(errors="replace")
        # Full daemon capture is local-only; redact credentials defensively.
        for secret in (self.token, self.cookie):
            text = text.replace(secret, "[redacted]")
        (self.out / "daemon.log").write_text(text)
        matches = [line for line in text.splitlines() if BAD_LOG.search(line)]
        assert not matches, "daemon diagnostic matched; see daemon.log"

    @contextlib.contextmanager
    def scenario(self, name):
        start = time.monotonic()
        try:
            yield
            self.check()
        except Exception:
            self.rows.append([name, "FAIL", round(time.monotonic() - start, 2)])
            raise
        else:
            self.rows.append([name, "PASS", round(time.monotonic() - start, 2)])

    def until(self, what, fn):
        deadline = time.monotonic() + self.args.timeout
        while time.monotonic() < deadline:
            value = fn()
            if value:
                return value
            self.check()
            time.sleep(2)
        raise TimeoutError(what)

    def events(self, sid):
        events = []
        after = self.seq.get(sid, 0)
        while True:
            _, page = self.api("GET", f"/api/runner/sessions/{sid}/events?after_seq={after}&limit=500")
            batch = page.get("events", [])
            events.extend(batch)
            if batch:
                after = max(e["seq"] for e in batch)
            if not page.get("has_more"):
                self.seq[sid] = after
                return events
            assert batch, "events pagination made no progress"

    def turn(self, sid):
        def done():
            events = self.events(sid)
            assert not any(e.get("kind") == "error" for e in events), "engine error; see response logs"
            return any(e.get("kind") == "turn_done" for e in events)
        self.until("turn completion " + sid, done)

    def message(self, sid, text):
        self.events(sid)  # drain the previous turn before sending
        self.api("POST", f"/api/runner/sessions/{sid}/message", {"text": text, "client_id": str(uuid.uuid4())})

    def start(self, engine):
        body = {"repo": os.environ["E2E_REPO"], "runtime": "daemon", "engine": engine,
                "prompt": "You coordinate a release test. Do not change files or start workers. Reply ready.",
                "title": "Session acceptance", "auto_stop": False}
        if os.environ.get("E2E_DAEMON_ID"):
            body["daemon_id"] = os.environ["E2E_DAEMON_ID"]
        if engine == "hermes":
            body["model"] = os.environ["E2E_MODEL"]
            if os.environ.get("E2E_HERMES_HOME"):
                body["env"] = {"HERMES_HOME": os.environ["E2E_HERMES_HOME"]}
        _, reply = self.api("POST", "/api/runner/start", body)
        sid = reply["session_id"]
        self.parents.append(sid)
        self.turn(sid)
        _, result = self.api("GET", f"/api/runner/sessions/{sid}/result")
        assert result.get("workspace"), "host session did not receive a workspace manifest"
        return sid

    def commit(self, sid, marker):
        def collected():
            _, result = self.api("GET", f"/api/runner/sessions/{sid}/result")
            members = result.get("workspace_results", [])
            if not members or any(m.get("pending") or m.get("sync_error") or not m.get("head") for m in members):
                return False
            for member in members:
                head = member["head"]
                # Host-child commits must have arrived in the source object database.
                text = subprocess.check_output(["git", "-C", str(self.repo), "show", f"{head}:{marker}"], stderr=subprocess.DEVNULL)
                assert text.strip() == marker.encode(), "wrong committed marker"
                build = subprocess.check_output(["git", "-C", str(self.repo), "show", f"{head}:e2e-build.status"])
                assert build.strip() == b"0", "child build failed or status missing"
                test = subprocess.check_output(["git", "-C", str(self.repo), "show", f"{head}:e2e-test.json"])
                results = [json.loads(line) for line in test.splitlines() if line.strip()]
                assert results and not any(e.get("Action") == "fail" for e in results), "child test failures"
                assert any(e.get("Action") == "pass" and "Test" not in e for e in results), "no passing Go test package"
            return result
        return self.until("turn commits collected", collected)

    def run(self):
        with self.scenario("host workspace"):
            parent = self.start("hermes")
        with self.scenario("two parallel host children build/test/commit"):
            # The parent's own session token authorizes child creation. The model invokes
            # the supported CLI; the external run-sessions token cannot impersonate it.
            instruction = ("Start exactly two parallel host children using blerg-runner session start --engine hermes --runtime daemon --model " + os.environ["E2E_MODEL"] + ". "
                "Each must work in its own assigned checkout and the Go module root. Run go build ./...; save its numeric exit status in e2e-build.status and stop if it is nonzero. Run go test -json ./... > e2e-test.json; stop if it fails. "
                "Write a file named e2e-child.txt containing exactly e2e-child.txt. Git add and commit the marker, e2e-build.status and e2e-test.json. "
                "Do not push or merge. Do not wait for approval yourself. Wait for both to finish. Do not discard them. "
                "Do not change this parent checkout. Report both child IDs and test results.")
            self.message(parent, instruction)
            self.turn(parent)
            _, live = self.api("GET", f"/api/sessions/{parent}/live", human=True)
            children = live.get("children", [])
            assert len(children) == 2, "expected exactly two children; check delegation approval policy"
            ids = [c.get("session_id", c.get("id")) for c in children]
            assert all(ids), "child IDs missing"
            for sid in ids:
                self.commit(sid, "e2e-child.txt")
            # Confirm overlapping engine activity from persisted event timestamps.
            windows = []
            for sid in ids:
                events = self.events(sid)
                starts = [datetime.fromisoformat(e["ts"].replace("Z", "+00:00")) for e in events if e.get("kind") == "user_message"]
                ends = [datetime.fromisoformat(e["ts"].replace("Z", "+00:00")) for e in events if e.get("kind") == "turn_done"]
                assert starts and ends, "child turn timing missing"
                windows.append((min(starts), max(ends)))
            assert max(w[0] for w in windows) < min(w[1] for w in windows), "children did not overlap"
        with self.scenario("idle engine exit and respawn"):
            # No fixed sleep can prove an exit. Match the selected session's daemon line.
            pattern = os.environ["E2E_IDLE_EXIT_REGEX"].replace("{session}", re.escape(parent))
            self.until("idle engine exit", lambda: re.search(pattern, self.log.read_bytes()[self.offset:].decode(errors="replace")))
            self.message(parent, "Reply respawn complete. Do not change files.")
            self.turn(parent)
        with self.scenario("child ask reaches Needs you"):
            child = ids[0]
            self.message(child, "Run blerg-runner ask 'Session acceptance: proceed?' and wait for the answer. Do not change files.")
            def ask():
                _, live = self.api("GET", f"/api/sessions/{child}/live", human=True)
                if not live.get("asks") or live.get("counts", {}).get("needs_you", 0) <= 0:
                    return None
                _, parent_live = self.api("GET", f"/api/sessions/{parent}/live", human=True)
                return live["asks"] if any(c.get("id") == child and c.get("asking") for c in parent_live.get("children", [])) else None
            asks = self.until("child ask in Needs you", ask)
            self.api("POST", f"/api/messages/{asks[0]['message_id']}/reply", {"answer": "Proceed; reply complete."}, human=True)
            self.turn(child)
        with self.scenario("discard children"):
            # Ask the parent to discard through its authenticated child API.
            self.message(parent, "Run blerg-runner session discard for each of these children: " + " ".join(ids) + ". Report command results. Do not change files.")
            self.turn(parent)
            for sid in ids:
                _, status = self.api("GET", f"/api/runner/sessions/{sid}")
                assert status["lifecycle"] == "ended", "discard did not end child"
                worktrees = subprocess.check_output(["git", "-C", str(self.repo), "worktree", "list", "--porcelain"]).decode()
                assert "refs/heads/wip/" + sid not in worktrees, "discard left child worktree"
                _, live = self.api("GET", f"/api/sessions/{parent}/live", human=True)
                assert not any(c.get("session_id", c.get("id")) == sid for c in live.get("children", [])), "discard did not archive child"
        if self.args.smoke:
            for engine in ("claude", "codex"):
                with self.scenario(engine + " smoke"):
                    sid = self.start(engine)
                    self.message(sid, "Reply second turn complete. Do not change files.")
                    self.turn(sid)

    def finish(self):
        # Always end test parents, including failures. No slots or kept children leak.
        for sid in self.parents:
            try:
                self.api("POST", f"/api/sessions/{sid}/discard", {}, human=True)
            except Exception as error:
                self.rows.append(["cleanup " + sid, "FAIL", 0])
                print(str(error))
        try:
            self.check()
        except Exception as error:
            self.rows.append(["final invariants", "FAIL", 0])
            print(str(error))
        self.save("summary.json", self.rows)
        print("\n| Scenario | Result | Seconds |\n|---|---|---:|")
        for name, result, duration in self.rows:
            print(f"| {name} | {result} | {duration} |")
        print("Logs:", self.out)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--logs", default="session-e2e-" + time.strftime("%Y%m%d-%H%M%S"))
    parser.add_argument("--timeout", type=int, default=1200, help="deadline per wait, seconds")
    parser.add_argument("--smoke", action="store_true", help="also spend two tiny turns each on Claude and Codex")
    args = parser.parse_args()
    required = ["E2E_AGENT_TOKEN", "E2E_OWNER_COOKIE", "E2E_RUNNER_URL", "E2E_CHECKOUT", "E2E_REPO", "E2E_MODEL", "E2E_DAEMON_LOG", "E2E_IDLE_EXIT_REGEX"]
    missing = [key for key in required if not os.environ.get(key)]
    if missing:
        parser.error("missing environment: " + ", ".join(missing))
    if "{session}" not in os.environ["E2E_IDLE_EXIT_REGEX"]:
        parser.error("E2E_IDLE_EXIT_REGEX must contain {session}")
    os.umask(0o077)
    run = Run(args)
    try:
        run.run()
    except Exception as error:
        print(type(error).__name__ + ": " + str(error))
    finally:
        run.finish()
    return int(not run.rows or any(row[1] != "PASS" for row in run.rows))


if __name__ == "__main__":
    raise SystemExit(main())
