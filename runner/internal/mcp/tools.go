package mcp

// toolDefs is the runner's MCP tool catalog for agent contract v1 (spec §4). Descriptions are prescriptive about WHEN to
// call: an agent picks a tool from its description, not from a manual.

var toolDefs = []map[string]any{
	{
		"name":        "discover_launch_options",
		"description": "Discover engines actually available to your credential on a runtime and host, including their models and reasoning efforts. Catalogs return promptly while native refresh runs in background. Copy runtime and daemon_id from the selected engine option; automatic discovery can return different hosts for different engines. Never substitute a workstation catalog for Cluster.",
		"inputSchema": obj(props{"session_id": str("existing session: resolve only its native engine and actual execution target"), "runtime": strEnum("execution runtime", "cluster", "docker", "daemon"), "daemon_id": str("connected host ID, not used for Cluster"), "repo": str("repository to resolve on a host"), "restricted": boolean("show only engines supporting restricted execution"), "personal_only": boolean("exclude operator credentials for account-only launches such as crons"), "refresh": boolean("request background catalog refresh")}, nil),
	},
	{
		"name": "start_session",
		"description": "Start an agent session on a repo and return its session_id. " +
			"Use discover_launch_options for the intended runtime and host before choosing engine, model or effort; copy runtime and daemon_id from the selected engine option on start. " +
			"The call returns as soon as the session is accepted — it does not wait for the work. " +
			"Poll get_session, or pass callback_url to be told when it ends. " +
			"For a one-shot task, pass auto_stop: the runner ends the session itself when the first turn is done, " +
			"so get_result reports terminal with the answer in last_assistant_message. " +
			"A session you start is unattended unless you pass interaction: \"interactive\" (only when a person will follow its chat as it works). " +
			"ALWAYS pass idempotency_key when a retry is possible: the same key replays the first start instead of spawning a second session.",
		"inputSchema": obj(props{
			"repo":            str("repo to work in: a folder name, or org/name. Required unless no_repo is true"),
			"no_repo":         boolean("true = a session tied to no repository; repo, git_url and provider must then be omitted. On the cluster it works in an empty throwaway directory; on a daemon (docker or daemon runtime) in a new scratch folder under the daemon's repos root. Without it, an empty repo is refused"),
			"prompt":          str("the task for the agent — the first thing it is told"),
			"title":           str("short human label for the session"),
			"model":           str("model ID from the selected engine catalog in discover_launch_options for the same runtime/host; empty leaves the engine default"),
			"effort":          str("effort from the selected model in target-scoped discover_launch_options; empty leaves the engine default. A known model with an empty efforts list takes no explicit effort"),
			"daemon_id":       str("daemon_id from the selected engine option in discover_launch_options; omit for Cluster"),
			"engine":          str("available engine ID from discover_launch_options for the selected runtime/host; omission retains the legacy claude default and does not select another available engine"),
			"runtime":         strEnum(`where to run it: "cluster" (a cluster Job), "docker" (the local sandbox container on a connected daemon) or "daemon" (that daemon's bare host). Empty = cluster where one is configured, else docker when the daemon has the sandbox image, else daemon. The sandbox has no git credentials (its agent can commit but not push) — pass "daemon" for work that must push or needs the host`, "cluster", "docker", "daemon"),
			"auto_stop":       boolean("one-shot: stop the session as soon as its first turn is done (default false, i.e. it stays up for more messages)"),
			"interaction":     strEnum(`whether a person is reading the session's chat as it works: "unattended" (the default: the agent decides, notes its assumptions and ends with a summary that stands on its own) or "interactive" (a person is reading and answers in the chat, so the agent talks things through and asks before changing anything). Any other value is refused`, "interactive", "unattended"),
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
			"A disconnected cluster session is resumed by it. Sent while the agent waits on a blerg-runner ask, " +
			"it answers that ask (unless the source is \"blerg-board\").",
		"inputSchema": obj(props{
			"session_id": str("session id"),
			"text":       str("what to say to the agent"),
			"client_id":  str("retry-safe message id (at most 256 bytes); reuse the same id and text on retries to avoid a second turn. Persisted until the engine acknowledges consumption"),
			"source":     str(`who is speaking: "human" or an automation's name (empty = "runner"); the sources Blerg writes itself (proposal, review, system, ask_answer, note_reply, limit_resume, child_turn, question_answer) are refused, and so is session (a parent's message to its child, set by the runner)`),
		}, req("session_id", "text")),
	},
	{
		"name":        "set_session_model",
		"description": "Set a running agent session's model or reasoning effort for its next turn. At least one is required. The session's engine cannot change. Success acknowledges dispatch; model_changed reports the effective setting.",
		"inputSchema": obj(props{
			"session_id": str("session id"),
			"model":      str("model id supported by the session's existing engine"),
			"effort":     str("supported effort for the model; omitted keeps the current setting"),
		}, req("session_id")),
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
