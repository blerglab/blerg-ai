#!/usr/bin/env python3
"""A deterministic stand-in for the `claude` CLI, for filming Blerg without model calls.

The demo daemon finds this (through a `claude` shim on its PATH) instead of Claude Code. It speaks
the same streaming protocol the daemon drives the real CLI with (`claude -p --input-format
stream-json --output-format stream-json`; see runner/internal/daemon/claudestream.go), so the
daemon, the runner and the UI are the real ones: only the model is scripted.

What the agent does comes from scenario files: the shared seed scenario (video/demo/seed-agent.yaml,
the seeded sessions' turns) and the active short's `video/shorts/<short>/agent.yaml`. The active
short is named in --active-file (demo.sh writes it from BLERG_DEMO_SHORT); without one, every
short's scenario is loaded and a message that more than one of them would answer is logged. Each
scenario is a list of turns; a turn starts when the person's message matches its `match` regex (and,
when given, `after` names the turn that must have run before it in this session). A turn's steps:

  say: <markdown>          an assistant text block
  think: <text>            a thinking block
  run: <shell command>     a Bash tool call that really runs, in the session's folder, with the
                           session's own environment (so `blerg-runner diff` publishes for real)
  write: {path, from}      a Write tool call that copies a file from the scenario folder
  edit: {path, old, new}   an Edit tool call (a plain string replacement in the file)
  replies: [{match, status, text}]
                           answers the open review requests (`blerg-runner review list`, read
                           quietly; show it with a `run` step first) in one
                           `blerg-runner review reply` call: each open request gets the first
                           entry whose `match` regex finds its line, else the next unused entry
  pause: <seconds>         wait before the next step

Every step is followed by a short pause (`pace` in the scenario, default 0.9 s) so the UI shows
the turn arriving the way a real one does. A message that matches no turn gets a one-line answer.

Nothing here reads credentials or calls a network service; usage is reported as zero.
"""
import argparse
import glob
import json
import os
import re
import shlex
import subprocess
import sys
import threading
import time
import uuid

import yaml

HELP = """Usage: claude [options] [command] [prompt]

Options:
  -p, --print                 Print response and exit
  --output-format <format>    Output format
  --input-format <format>     Input format
  --verbose                   Verbose output
"""
VERSION = "2.1.0 (Claude Code)"


def out(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def log(msg):
    path = os.environ.get("BLERG_DEMO_FAKE_LOG")
    if path:
        with open(path, "a", encoding="utf-8") as f:
            f.write(time.strftime("%H:%M:%S ") + msg + "\n")


def load_scenario(path, name):
    with open(path, encoding="utf-8") as f:
        data = yaml.safe_load(f) or {}
    data["_dir"] = os.path.dirname(path)
    data["_name"] = name
    return data


def load_scenarios(root, seed=None, active_file=None):
    """The seed scenario, then the active short's (or every short's when none is active)."""
    scenarios = []
    if seed and os.path.exists(seed):
        scenarios.append(load_scenario(seed, "seed"))
    active = ""
    if active_file and os.path.exists(active_file):
        with open(active_file, encoding="utf-8") as f:
            active = f.read().strip()
    if active:
        path = os.path.join(root, active, "agent.yaml")
        if os.path.exists(path):
            scenarios.append(load_scenario(path, active))
        else:
            log(f"active short {active!r} has no agent.yaml")
        return scenarios
    for path in sorted(glob.glob(os.path.join(root, "*", "agent.yaml"))):
        scenarios.append(load_scenario(path, os.path.basename(os.path.dirname(path))))
    return scenarios


class State:
    """Which turns already ran in this Blerg session, kept on disk so a restarted process (the
    daemon closes an idle one and resumes it) carries on where it was."""

    def __init__(self, sid):
        base = os.environ.get("BLERG_DEMO_FAKE_STATE") or os.path.join(os.path.expanduser("~"), ".blerg-demo-fake")
        os.makedirs(base, exist_ok=True)
        self.path = os.path.join(base, re.sub(r"[^A-Za-z0-9_.-]", "_", sid) + ".json")
        try:
            with open(self.path, encoding="utf-8") as f:
                self.done = json.load(f)
        except (OSError, ValueError):
            self.done = []

    def add(self, name):
        self.done.append(name)
        with open(self.path, "w", encoding="utf-8") as f:
            json.dump(self.done, f)


class Agent:
    def __init__(self, scenarios, sid):
        self.scenarios = scenarios
        self.sid = sid
        self.state = State(os.environ.get("BLERG_RUNNER_SESSION_ID") or sid)
        self.n = 0

    def find_turn(self, text):
        hits = self.matches(text)
        if len({h[0]["_name"] for h in hits}) > 1:
            log(f"warning: {text[:60]!r} matches turns of several shorts {[h[2] for h in hits]}; "
                "the first wins (set BLERG_DEMO_SHORT)")
        return hits[0] if hits else (None, None, None)

    def matches(self, text):
        out = []
        for sc in self.scenarios:
            for i, turn in enumerate(sc.get("turns") or []):
                name = f"{sc['_name']}:{turn.get('id', i)}"
                if name in self.state.done and not turn.get("repeat"):
                    continue
                after = turn.get("after")
                if after and f"{sc['_name']}:{after}" not in self.state.done:
                    continue
                if re.search(turn.get("match", "$^"), text, re.I | re.S):
                    out.append((sc, turn, name))
        return out

    def tool_id(self):
        self.n += 1
        return f"toolu_demo_{uuid.uuid4().hex[:16]}"

    def assistant(self, blocks):
        out({"type": "assistant", "session_id": self.sid,
             "message": {"id": "msg_demo_" + uuid.uuid4().hex[:12], "model": "claude-demo", "role": "assistant",
                         "content": blocks, "usage": {"input_tokens": 0, "output_tokens": 0}}})

    def tool(self, name, inp, result, is_error=False):
        tid = self.tool_id()
        self.assistant([{"type": "tool_use", "id": tid, "name": name, "input": inp}])
        out({"type": "user", "session_id": self.sid,
             "message": {"role": "user", "content": [{"type": "tool_result", "tool_use_id": tid,
                                                      "content": result, "is_error": is_error}]}})

    def sh(self, command):
        p = subprocess.run(["bash", "-c", command], capture_output=True, text=True, timeout=120)
        text = (p.stdout + p.stderr).strip()
        log(f"run {command!r} -> {p.returncode}: {text[:400]}")
        return text, p.returncode != 0

    def run_turn(self, sc, turn):
        pace = float(sc.get("pace", 0.9))
        last = ""
        for step in turn.get("steps") or []:
            if "say" in step:
                last = step["say"].strip()
                self.assistant([{"type": "text", "text": last}])
            elif "think" in step:
                self.assistant([{"type": "thinking", "thinking": step["think"].strip()}])
            elif "run" in step:
                text, err = self.sh(step["run"])
                self.tool("Bash", {"command": step.get("show", step["run"]), "description": step.get("description", "")},
                          text or "(no output)", err)
            elif "write" in step:
                w = step["write"]
                with open(os.path.join(sc["_dir"], w["from"]), encoding="utf-8") as f:
                    content = f.read()
                os.makedirs(os.path.dirname(os.path.abspath(w["path"])), exist_ok=True)
                with open(w["path"], "w", encoding="utf-8") as f:
                    f.write(content)
                self.tool("Write", {"file_path": os.path.abspath(w["path"]), "content": content},
                          f"File created successfully at: {os.path.abspath(w['path'])}")
            elif "edit" in step:
                e = step["edit"]
                with open(e["path"], encoding="utf-8") as f:
                    body = f.read()
                ok = e["old"] in body
                if ok:
                    with open(e["path"], "w", encoding="utf-8") as f:
                        f.write(body.replace(e["old"], e["new"], 1))
                self.tool("Edit", {"file_path": os.path.abspath(e["path"]), "old_string": e["old"], "new_string": e["new"]},
                          f"The file {os.path.abspath(e['path'])} has been updated." if ok else "String to replace not found in file.",
                          not ok)
            elif "replies" in step:
                self.reply(step["replies"])
            if "pause" in step:
                time.sleep(float(step["pause"]))
            time.sleep(pace)
        return last

    def reply(self, entries):
        listing, _ = self.sh("blerg-runner review list")  # not shown: a `run` step shows it
        reqs = re.findall(r"^\s+\[([^\]]+)\]\s*(.*)$", listing, re.M)
        used, args = set(), []
        for rid, line in reqs:
            pick = next((k for k, e in enumerate(entries) if k not in used and e.get("match")
                         and re.search(e["match"], line, re.I)), None)
            if pick is None:
                pick = next((k for k in range(len(entries)) if k not in used), None)
            if pick is None:
                break
            used.add(pick)
            e = entries[pick]
            args += [rid, e.get("status", "done"), e["text"]]
        if not args:
            return
        cmd = "blerg-runner review reply " + " ".join(shlex.quote(a) for a in args)
        text, err = self.sh(cmd)
        self.tool("Bash", {"command": cmd, "description": "Answer the review"}, text, err)


def user_text(msg):
    content = (msg.get("message") or {}).get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "\n".join(b.get("text", "") for b in content if isinstance(b, dict))
    return ""


def main():
    ap = argparse.ArgumentParser(add_help=False)
    ap.add_argument("--scenarios", required=True)
    ap.add_argument("--seed-scenario")
    ap.add_argument("--active-file")
    ap.add_argument("--resume")
    ap.add_argument("--input-format")
    ap.add_argument("--help", action="store_true")
    ap.add_argument("--version", action="store_true")
    ap.add_argument("-p", "--print", action="store_true")
    args, _ = ap.parse_known_args()
    if args.help:
        print(HELP)
        return 0
    if args.version:
        print(VERSION)
        return 0
    if args.input_format != "stream-json":
        # A one-shot call (a probe, or the per-turn engine): answer briefly and exit.
        if args.print:
            sid = args.resume or "demo-" + uuid.uuid4().hex[:12]
            out({"type": "system", "subtype": "init", "session_id": sid})
            out({"type": "result", "subtype": "success", "is_error": False, "session_id": sid, "result": "ok"})
        return 0

    sid = args.resume or str(uuid.uuid4())
    agent = Agent(load_scenarios(args.scenarios, args.seed_scenario, args.active_file), sid)
    log(f"start sid={sid} session={os.environ.get('BLERG_RUNNER_SESSION_ID', '')} cwd={os.getcwd()}")
    lock = threading.Lock()
    for raw in sys.stdin:
        try:
            msg = json.loads(raw)
        except ValueError:
            continue
        if msg.get("type") == "control_request":
            out({"type": "control_response", "response": {"subtype": "success", "request_id": msg.get("request_id"),
                                                          "response": {"still_queued": []}}})
            continue
        if msg.get("type") != "user":
            continue
        text = user_text(msg)
        with lock:
            out({"type": "system", "subtype": "init", "session_id": sid, "model": "claude-demo", "tools": ["Bash", "Write", "Edit"]})
            out({"type": "user", "session_id": sid, "isReplay": True, "uuid": msg.get("uuid"),
                 "message": {"role": "user", "content": (msg.get("message") or {}).get("content", text)}})
            sc, turn, name = agent.find_turn(text)
            log(f"message {text[:120]!r} -> {name}")
            if turn is None:
                last = "(This is a scripted demo agent: nothing is scripted for that message.)"
                agent.assistant([{"type": "text", "text": last}])
            else:
                agent.state.add(name)
                try:
                    last = agent.run_turn(sc, turn)
                except Exception as exc:  # keep the process: a failed step still ends the turn
                    log(f"turn {name} failed: {exc!r}")
                    last = ""
            out({"type": "result", "subtype": "success", "is_error": False, "session_id": sid, "result": last,
                 "num_turns": 1, "total_cost_usd": 0,
                 "usage": {"input_tokens": 0, "output_tokens": 0, "cache_read_input_tokens": 0,
                           "cache_creation_input_tokens": 0}})
    log("eof")
    return 0


if __name__ == "__main__":
    sys.exit(main())
