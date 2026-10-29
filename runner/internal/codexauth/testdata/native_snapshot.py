# Synthetic access credentials and loopback routing only; no host login or inference.
import json, os, pathlib, select, subprocess, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

root = pathlib.Path(os.environ["CODEX_SNAPSHOT_FIXTURE_ROOT"])
auth = json.loads((root / "auth.json").read_text())
assert auth["tokens"]["refresh_token"] == ""
stats = {"refreshes": 0}

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(json.dumps({"accounts": [{"id": "fixture-account", "workspace_backend_origin": "https://chatgpt.com", "account_routing_override": "NO_CONSTRAINT"}]}).encode())
    def do_POST(self):
        stats["refreshes"] += 1
        self.send_response(401)
        self.end_headers()

server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
thread = threading.Thread(target=server.serve_forever, daemon=True)
thread.start()

def consumer(label, missing_slot):
    home = root / label
    home.mkdir()
    data = json.loads(json.dumps(auth))
    if missing_slot:
        del data["tokens"]["refresh_token"]
    target = home / "auth.json"
    target.write_text(json.dumps(data))
    target.chmod(0o600)
    (home / "config.toml").write_text("chatgpt_base_url=" + json.dumps(f"http://127.0.0.1:{server.server_port}") + "\n")
    env = {"HOME": str(home), "CODEX_HOME": str(home), "TMPDIR": str(root), "PATH": "/usr/local/bin:/usr/bin:/bin", "CODEX_REFRESH_TOKEN_URL_OVERRIDE": f"http://127.0.0.1:{server.server_port}/oauth/token", "HTTPS_PROXY": "http://127.0.0.1:9", "HTTP_PROXY": "http://127.0.0.1:9", "ALL_PROXY": "http://127.0.0.1:9", "NO_PROXY": "127.0.0.1,localhost"}
    child = subprocess.Popen([os.environ["CODEX_SNAPSHOT_FIXTURE_BINARY"], "app-server"], cwd=home, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, bufsize=0)
    def call(i, method, params):
        child.stdin.write((json.dumps({"id": i, "method": method, "params": params}) + "\n").encode())
        child.stdin.flush()
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if select.select([child.stdout], [], [], max(0, deadline - time.monotonic()))[0]:
                raw = child.stdout.readline()
                if not raw: raise RuntimeError("native RPC closed")
                message = json.loads(raw)
                if message.get("id") == i: return message
        raise RuntimeError("native RPC deadline")
    try:
        call(1, "initialize", {"clientInfo": {"name": "fixture", "version": "1"}, "capabilities": {"experimentalApi": True}})
        child.stdin.write(b'{"method":"initialized"}\n')
        child.stdin.flush()
        result = call(2, "account/read", {"refreshToken": False})
        assert not json.loads(target.read_text()).get("tokens", {}).get("refresh_token")
        return bool(result.get("result", {}).get("account"))
    finally:
        child.terminate()
        try: child.wait(timeout=3)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait()

try:
    native = consumer("native", False)
    missing = consumer("missing-slot", True)
    print(json.dumps({"native_account": native, "missing_slot_account": missing, **stats}))
finally:
    server.shutdown()
    server.server_close()
    thread.join()
