package mcp

// toolDefs is the MCP tool catalog. Descriptions are prescriptive about WHEN
// to call — agents follow trigger conditions in descriptions.
var toolDefs = []map[string]any{
	{
		"name":        "blerg_board_list",
		"description": "List the blerg-board boards this token can see. Call first to find the board to work on.",
		"inputSchema": obj(nil, nil),
	},
	{
		"name":        "blerg_board_get",
		"description": "Get one board: name, repos, gate policy, field schema.",
		"inputSchema": obj(props{"board_id": str("board uuid")}, req("board_id")),
	},
	{
		"name":        "blerg_board_schema",
		"description": "Read a board's declared field schema. Call BEFORE writing any custom `fields` — unknown keys and type mismatches are rejected.",
		"inputSchema": obj(props{"board_id": str("board uuid")}, req("board_id")),
	},
	{
		"name":        "blerg_board_create",
		"description": "Create a board (requires board.admin). Optionally declare repos, initial columns, and a field schema.",
		"inputSchema": obj(props{
			"name":           str("board name"),
			"description":    str("what this board tracks"),
			"repos":          arrStr("repos cards may reference"),
			"columns":        arrStr("initial column names in order"),
			"field_schema":   fieldSchemaProp("declared custom fields for cards on this board"),
			"require_repo":   boolean("require ≥1 repo per card (default true)"),
			"gate_enabled":   boolean("run the admission gate on agent writes"),
			"concurrency":    integer("how many cards a board run works at once (default 1). 0 parks the board: the run stays active and in-flight cards finish, but nothing new is dispatched. Negatives are rejected. Shapes ONE board, and is not a spend ceiling — there is no global cap across boards, and it counts cards in the work column, not live sessions."),
			"model":          str("Claude model for this board's sessions, e.g. claude-sonnet-5 (empty = CLI default)"),
			"reviewer_model": str("model for adversarial review sessions (empty = same as model)"),
			"discuss_model":  str("model for discuss-mode card sessions (empty = same as model)"),
			"chat_model":     str("model for board-header chat sessions (empty = same as model)"),
			"git_base":       str("git base URL for this board's repos, e.g. https://github.com/myorg — empty uses the server default"),
			"deploy_url":     str("where the project runs, if deployed (rendered as a LIVE link)"),
			"ci_policy":      str("what auto-merge does when a PR head reports NO CI at all: \"required\" (default) waits for a green check, so a repo with no CI workflow never auto-merges; \"if_present\" treats absent checks as nothing to fail and merges on the adversarial approval alone. Governs ABSENCE only — a red or still-running check blocks under both."),
			"driven_by":      str("name of the external system that owns this board's card lifecycle, e.g. \"blerg-ops\" — cards become read-only mirrors here (no sessions, no review flow). Empty = blerg-board-native board."),
		}, req("name")),
	},
	{
		"name":        "blerg_board_update",
		"description": "Update board settings, repos, or field schema (requires board.admin).",
		"inputSchema": obj(props{
			"board_id":          str("board uuid"),
			"name":              str("new name"),
			"field_schema":      fieldSchemaProp("replacement field schema"),
			"repos":             arrStr("replacement repo list"),
			"gate_enabled":      boolean("toggle the admission gate"),
			"concurrency":       integer("how many cards this board's run works at once (default 1). Takes effect on the run already going, not just the next one — this is the cross-board priority dial: raise the board being pushed on, leave the rest at 1. 0 parks the board (run stays active, in-flight cards finish, nothing new dispatched). Negatives are rejected. NOT a spend or resource ceiling: there is no global cap above it, so total parallelism is the SUM across running boards (eleven boards at 3 is up to 33, not 3), and it bounds cards in the work column, not live sessions — reviewer, discuss and board-chat sessions are invisible to it."),
			"deploy_url":        str("where the project runs, if deployed"),
			"git_base":          str("git base URL for this board's repos (empty = server default)"),
			"model":             str("Claude model for sessions (empty = CLI default)"),
			"reviewer_model":    str("model for adversarial reviews (empty = same as model)"),
			"discuss_model":     str("model for discuss-mode card sessions (empty = same as model)"),
			"chat_model":        str("model for board-header chat sessions (empty = same as model)"),
			"description":       str("board description"),
			"driven_by":         str("name of the external system that owns this board's card lifecycle, e.g. \"blerg-ops\" — empty clears it back to blerg-board-native"),
			"ci_policy":         str("what auto-merge does when a PR head reports NO CI at all: \"required\" (default) waits for a green check, so cards on a repo with no CI workflow pile up in review; \"if_present\" treats absent checks as nothing to fail and merges on the adversarial approval alone. Governs ABSENCE only — a red or still-running check blocks under both."),
			"automation_engine": str("engine every session this board starts runs: \"claude\" (default), \"codex\" or \"hermes\". Sessions run on the credential of whoever owns the board's automation token; that token is set by a person in the board UI, never through this tool."),
		}, req("board_id")),
	},
	{
		"name":        "blerg_column_list",
		"description": "List a board's columns in order.",
		"inputSchema": obj(props{"board_id": str("board uuid")}, req("board_id")),
	},
	{
		"name":        "blerg_column_create",
		"description": "Append a column to a board (requires column.write).",
		"inputSchema": obj(props{
			"board_id":    str("board uuid"),
			"name":        str("column name"),
			"is_terminal": boolean("marks 'done'-style columns"),
		}, req("board_id", "name")),
	},
	{
		"name":        "blerg_column_move",
		"description": "Reposition a column among its board's siblings (requires column.write). Place it before before_id, or at the end if before_id is omitted.",
		"inputSchema": obj(props{
			"column_id": str("column uuid to move"),
			"before_id": str("place before this column (default: end)"),
		}, req("column_id")),
	},
	{
		"name":        "blerg_card_search",
		"description": "Search a board's cards by text and filters. ALWAYS call before filing a new card — the admission gate denies semantic duplicates.",
		"inputSchema": obj(props{
			"board_id":         str("board uuid"),
			"query":            str("free text over title+body (typo-tolerant)"),
			"type":             str("filter: card type"),
			"tag":              str("filter: tag"),
			"priority":         str("filter: low|medium|high|urgent"),
			"fields":           objectProp("equality filters on declared fields keys, e.g. {\"severity\":\"sev1\"}"),
			"include_archived": boolean("include archived cards"),
			"limit":            num("max results (default 20)"),
		}, req("board_id")),
	},
	{
		"name":        "blerg_card_get",
		"description": "Get one card with repos, tags, links, dependencies, and version. Address by card_id, or by board_id + number (#42).",
		"inputSchema": cardRef(nil, nil),
	},
	{
		"name":        "blerg_card_create",
		"description": "Create a card. Choose a stable dedup_key (an intent hash like 'missing-tests:internal/daemon/screenstate.go') so re-running a sweep refreshes instead of duplicating. May be denied (duplicate) or sent back for revision by the admission gate — read the error body and act on it. To contest a denial once, resubmit with dispute_of=<review_id> and a rebuttal.",
		"inputSchema": obj(props{
			"board_id":   str("board uuid"),
			"title":      str("what the card is — specific, not vague"),
			"body":       str("details, evidence, acceptance criteria"),
			"type":       str("feature|bug|task|chore|incident|… (lowercased)"),
			"priority":   str("low|medium|high|urgent"),
			"size":       str("XS|S|M|L|XL"),
			"repos":      arrStr("repos this card touches (first = primary working dir)"),
			"tags":       arrStr("board-scoped tags"),
			"fields":     objectProp("custom fields matching the board schema"),
			"model":      str("Claude model for THIS card's sessions, overriding the board's (empty = board default)"),
			"dedup_key":  str("stable intent hash for idempotent re-filing"),
			"column_id":  str("target column (default: first column)"),
			"dispute_of": str("review_id of a denial being contested (once per review)"),
			"rebuttal":   str("why the denial was wrong"),
		}, req("board_id", "title")),
	},
	{
		"name":        "blerg_card_update",
		"description": "Update a card's content/fields/tags/repos. Pass if_match=<version> to fail (409) if someone changed it since you read it.",
		"inputSchema": cardRef(props{
			"title":    str("new title"),
			"body":     str("new body"),
			"priority": str("low|medium|high|urgent"),
			"size":     str("XS|S|M|L|XL"),
			"fields":   objectProp("custom fields matching the board schema"),
			"model":    str("Claude model for THIS card's sessions, overriding the board's (\"\" clears it)"),
			"tags":     arrStr("replacement tag set"),
			"repos":    arrStr("replacement repo set"),
			"if_match": num("expected version"),
		}, nil),
	},
	{
		"name":        "blerg_card_move",
		"description": "Move a card to a column (by column_id or column_name), optionally before another card.",
		"inputSchema": cardRef(props{
			"column_id":      str("target column uuid"),
			"column_name":    str("target column by name"),
			"before_card_id": str("place before this card (default: end)"),
			"if_match":       num("expected version"),
		}, nil),
	},
	{
		"name":        "blerg_card_archive",
		"description": "Archive a card (leaves the board, stays queryable). Use for done/invalid cards.",
		"inputSchema": cardRef(nil, nil),
	},
	{
		"name":        "blerg_card_link",
		"description": "Attach a link to a card: kind ∈ session|pr|rcca|doc|url|artifact. An artifact is a file a runner session published: url is its /sessions/<session id>?artifact=<file id> path.",
		"inputSchema": cardRef(props{
			"kind":  str("session|pr|rcca|doc|url|artifact"),
			"url":   str("the link"),
			"label": str("display label"),
		}, req("kind", "url")),
	},
	{
		"name":        "blerg_card_comment",
		"description": "Post a progress comment on a card — the engineer's log. Use it WHILE working: what you're investigating, findings with links, decisions and why. Short and frequent beats one long final edit.",
		"inputSchema": cardRef(props{"text": str("the comment (markdown-ish plain text; include links)")}, req("text")),
	},
	{
		"name":        "blerg_review_get",
		"description": "Fetch an admission review by id — check the outcome of a held (202) submission.",
		"inputSchema": obj(props{"review_id": str("review uuid")}, req("review_id")),
	},
	{
		"name": "blerg_session_set_model",
		"description": "Change the model YOUR OWN session runs on, mid-task, without losing context. " +
			"Call it when the work turns out to need a stronger model than you were spawned with — or a cheaper one once the hard part is done. " +
			"Takes effect on your NEXT turn (the turn you are in finishes on the current model), so say what you're doing and let the turn end. " +
			"Say WHY in `reason`: it lands on the card's trail.",
		"inputSchema": obj(props{
			"model":             str("model id to switch to, e.g. claude-sonnet-5"),
			"reason":            str("why the switch is warranted — recorded on the card"),
			"runner_session_id": str("session to re-model; omit for your own"),
		}, req("model")),
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

func cardRef(extra props, required []string) map[string]any {
	p := props{
		"card_id":  str("card uuid"),
		"board_id": str("board uuid (with number)"),
		"number":   num("human card number, e.g. 42"),
	}
	for k, v := range extra {
		p[k] = v
	}
	return obj(p, required)
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func num(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}

// integer, not num: these land in Go `int` fields, and a client that took
// "number" at its word and sent 3.0 would get a decode error rather than 3.
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func arrStr(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// fieldSchemaProp: typed as an array of FieldDef objects — a type-less
// property makes some MCP clients pass the value as a JSON string.
func fieldSchemaProp(desc string) map[string]any {
	return map[string]any{
		"type": "array", "description": desc,
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key":        map[string]any{"type": "string"},
				"label":      map[string]any{"type": "string"},
				"type":       map[string]any{"type": "string", "enum": []string{"enum", "text", "number", "url", "timestamp", "bool"}},
				"values":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"display":    map[string]any{"type": "string", "enum": []string{"badge", "chip", "link", "inline", "hidden"}},
				"filterable": map[string]any{"type": "boolean"},
				"order":      map[string]any{"type": "integer"},
			},
			"required": []string{"key", "type"},
		},
	}
}

func objectProp(desc string) map[string]any {
	return map[string]any{"type": "object", "description": desc}
}
func req(names ...string) []string { return names }
