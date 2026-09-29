package mcp

// toolDefs is the runner's MCP tool catalog — the seven session operations of
// agent contract v1 (spec §4). Descriptions are prescriptive about WHEN to
// call: an agent picks a tool from its description, not from a manual.

var toolDefs = []map[string]any{
	{
		"name": "start_session",
		"description": "Start an agent session on a repo and return its session_id. " +
			"The call returns as soon as the session is accepted — it does not wait for the work. " +
			"Poll get_session, or pass callback_url to be told when it ends. " +
			"For a one-shot task, pass auto_stop: the runner ends the session itself when the first turn is done, " +
			"so get_result reports terminal with the answer in last_assistant_message. " +
			"ALWAYS pass idempotency_key when a retry is possible: the same key replays the first start instead of spawning a second session.",
		"inputSchema": obj(props{
			"repo":            str("repo to work in: a folder name, or org/name. Required unless no_repo is true"),
			"no_repo":         boolean("true = a session tied to no repository; repo, git_url and provider must then be omitted. On the cluster it works in an empty throwaway directory; on a daemon (docker or daemon runtime) in a new scratch folder under the daemon's repos root. Without it, an empty repo is refused"),
			"prompt":          str("the task for the agent — the first thing it is told"),
			"title":           str("short human label for the session"),
			"model":           str("model id to run on (empty = this install's default): an id from GET /api/models/{engine}, or for claude an alias like sonnet"),
			"effort":          str("reasoning effort (empty = the model's default): one of the chosen model's efforts from GET /api/models/{engine} (claude: low, medium, high, xhigh, max; codex and hermes: none, minimal, low … ultra). An engine with no effort levels (openclaw) refuses any value"),
			"engine":          str("coding engine (empty = claude)"),
			"runtime":         strEnum(`where to run it: "cluster" (a cluster Job), "docker" (the local sandbox container on a connected daemon) or "daemon" (that daemon's bare host). Empty = cluster where one is configured, else docker when the daemon has the sandbox image, else daemon. The sandbox has no git credentials (its agent can commit but not push) — pass "daemon" for work that must push or needs the host`, "cluster", "docker", "daemon"),
			"auto_stop":       boolean("one-shot: stop the session as soon as its first turn is done (default false, i.e. it stays up for more messages)"),
			"idempotency_key": str("retry key, up to 128 characters: the same key and body replays the original start"),
			"callback_url":    str("https URL POSTed the result when the session reaches a terminal state (plain http only to localhost, and only on an install that allows private callback targets)"),
			"callback_secret": str("HMAC key signing that callback; stored, never returned by any endpoint"),
			"git_url":         str("clone URL override, for a repo outside this install's default git base"),
			"provider":        strEnum(`git provider the repo lives on: "github" or "gitlab". Empty = github. Pass it for any repo not on GitHub, or an owner/name will be cloned from GitHub`, "github", "gitlab"),
			"env":             objectProp("extra environment for the session; BLERG_RUNNER_* and ANTHROPIC_API_KEY are reserved"),
		}, req("prompt")),
	},
	{
		"name": "get_session",
		"description": "Check where a session is: lifecycle (starting|running|disconnected|ended|error), " +
			"runtime (where it is hosted), resumable, error_reason when it failed, and end_reason/ended_by once it has ended. Poll this to know when work is done.",
		"inputSchema": obj(props{"session_id": str("session id from start_session")}, req("session_id")),
	},
	{
		"name": "send_message",
		"description": "Send a conversational turn to a running session — an answer, a correction, more context. " +
			"A disconnected cluster session is resumed by it.",
		"inputSchema": obj(props{
			"session_id": str("session id"),
			"text":       str("what to say to the agent"),
			"source":     str(`who is speaking: "human" or an automation's name (empty = "runner")`),
		}, req("session_id", "text")),
	},
	{
		"name":        "interrupt_session",
		"description": "Cancel the turn a session is working on right now, leaving the session alive. Use it to redirect a session that is going the wrong way; use stop_session to end it.",
		"inputSchema": obj(props{"session_id": str("session id")}, req("session_id")),
	},
	{
		"name":        "stop_session",
		"description": "End a session for good and free its slot. Call it on every session you finish with — cluster capacity is bounded, and a session nobody stops holds its slot.",
		"inputSchema": obj(props{"session_id": str("session id")}, req("session_id")),
	},
	{
		"name":        "get_events",
		"description": "Read a window of a session's transcript in order. Page with after_seq: pass the seq of the last event you saw; has_more says whether more are waiting.",
		"inputSchema": obj(props{
			"session_id": str("session id"),
			"after_seq":  integer("return events after this seq (0 = from the start)"),
			"limit":      integer("max events, 1-400 (default 200)"),
		}, req("session_id")),
	},
	{
		"name": "get_result",
		"description": "Get the structured outcome of a session in one object: lifecycle, terminal, repo, branch, last assistant message, error reason, why it ended, timings. " +
			"Prefer this over replaying the whole transcript once a session has ended.",
		"inputSchema": obj(props{"session_id": str("session id")}, req("session_id")),
	},
}

// ── schema helpers ───────────────────────────────────────────────────────────

type props map[string]any

func obj(p props, required []string) map[string]any {
	if p == nil {
		p = props{}
	}
	out := map[string]any{"type": "object", "properties": p}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// strEnum is str with the closed set of values spelled out in the schema, so a
// client picks from them rather than guessing at the prose.
func strEnum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

// integer, not number: these land in Go int fields, and a client that took
// "number" at its word and sent 3.0 would get a decode error rather than 3.
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func objectProp(desc string) map[string]any {
	return map[string]any{"type": "object", "description": desc}
}

func req(names ...string) []string { return names }
