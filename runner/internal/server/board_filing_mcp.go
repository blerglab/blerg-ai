package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
)

func filingToolDefinition() any {
	stringField := map[string]string{"type": "string"}
	return map[string]any{"name": "blerg_ticket_file", "description": "Search before filing a ticket on any Board available to your person. list_boards lists targets; search finds duplicates; propose queues an exact draft for human approval and never writes before approval.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action"}, "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"list_boards", "search", "propose"}}, "board": stringField, "query": stringField, "title": stringField, "body": stringField, "type": stringField, "sensitive": map[string]string{"type": "boolean"}, "repos": map[string]any{"type": "array", "items": stringField, "maxItems": 20}}}}
}

func pluginRequestToolDefinition() any {
	return map[string]any{"name": "blerg_request_plugin", "description": "Request an allowed plugin or bundled skill for your person to approve. Never installs or changes settings without human approval. Restricted sessions may request an always-on setting for future unrestricted sessions.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"name", "reason"}, "properties": map[string]any{"name": map[string]any{"type": "string", "description": "plugin@owner/repo, or a plugin name when exactly one marketplace is allowed"}, "reason": map[string]any{"type": "string", "maxLength": 2000}}}}
}

type filingToolCapture struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (c *filingToolCapture) Header() http.Header { return c.header }
func (c *filingToolCapture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}
func (c *filingToolCapture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = 200
	}
	return c.body.Write(b)
}

// The built-in session tool is a transport adapter over the same authenticated
// REST handlers as the CLI. It grants no direct Board mutation and never accepts
// an account, origin, credential or arbitrary destination from a tool argument.
func (a *API) HandleFilingTools(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	owner, ok := a.authMessaging(w, r)
	if !ok {
		return
	}
	if owner != "" && owner != sid {
		writeError(w, 403, "forbidden")
		return
	}
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*(64<<10)))
	if err != nil || json.Unmarshal(raw, &req) != nil || req.JSONRPC != "2.0" || len(req.ID) > 256 {
		writeError(w, 400, "Invalid session tool request.")
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(202)
		return
	}
	respond := func(result any, rpcError any) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		out := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rpcError != nil {
			out["error"] = rpcError
		} else {
			out["result"] = result
		}
		_ = json.NewEncoder(w).Encode(out)
	}
	switch req.Method {
	case "initialize":
		respond(map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "blerg-session", "version": "1.0.0"}}, nil)
	case "ping":
		respond(map[string]any{}, nil)
	case "tools/list":
		respond(map[string]any{"tools": []any{filingToolDefinition(), pluginRequestToolDefinition(), boardSessionToolDefinition()}}, nil)
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &params) != nil || (params.Name != "blerg_ticket_file" && params.Name != "blerg_request_plugin" && params.Name != "blerg_board") {
			respond(nil, map[string]any{"code": -32602, "message": "Unknown session tool."})
			return
		}
		if params.Name == "blerg_request_plugin" {
			inner := r.Clone(r.Context())
			inner.Method = http.MethodPost
			inner.Body = io.NopCloser(bytes.NewReader(params.Arguments))
			capture := &filingToolCapture{header: make(http.Header)}
			a.HandleSessionPluginRequest(capture, inner)
			respond(map[string]any{"isError": capture.status >= 400, "content": []any{map[string]any{"type": "text", "text": capture.body.String()}}}, nil)
			return
		}
		if params.Name == "blerg_board" {
			respond(a.boardToolResult(r, params.Arguments), nil)
			return
		}
		var args struct {
			Action    string   `json:"action"`
			Board     string   `json:"board"`
			Query     string   `json:"query"`
			Title     string   `json:"title"`
			Body      string   `json:"body"`
			Type      string   `json:"type"`
			Sensitive bool     `json:"sensitive"`
			Repos     []string `json:"repos"`
		}
		if filingDecode(params.Arguments, &args) != nil {
			respond(nil, map[string]any{"code": -32602, "message": "Invalid filing arguments."})
			return
		}
		inner := r.Clone(r.Context())
		copyURL := *r.URL
		inner.URL = &copyURL
		capture := &filingToolCapture{header: make(http.Header)}
		switch args.Action {
		case "list_boards":
			inner.Method = "GET"
			a.HandleFilingBoards(capture, inner)
		case "search":
			inner.Method = "GET"
			inner.URL.RawQuery = url.Values{"board": {args.Board}, "q": {args.Query}}.Encode()
			a.HandleFilingSearch(capture, inner)
		case "propose":
			inner.Method = "POST"
			data, _ := json.Marshal(map[string]any{"board": args.Board, "title": args.Title, "body": args.Body, "type": args.Type, "sensitive": args.Sensitive, "repos": args.Repos})
			inner.Body = io.NopCloser(bytes.NewReader(data))
			a.HandleFileTicket(capture, inner)
		default:
			respond(nil, map[string]any{"code": -32602, "message": "Choose list_boards, search or propose."})
			return
		}
		if capture.status == 0 {
			capture.status = 200
		}
		respond(map[string]any{"isError": capture.status >= 400, "content": []any{map[string]any{"type": "text", "text": capture.body.String()}}}, nil)
	default:
		respond(nil, map[string]any{"code": -32601, "message": "Method not found."})
	}
}

// boardToolResult runs the blerg_board session tool through the same authenticated handlers as the
// CLI (board-access and board-tools) and answers an MCP tool result: the board's own result, or the
// refusal as an error result.
func (a *API) boardToolResult(r *http.Request, arguments json.RawMessage) any {
	text := func(isError bool, body string) any {
		return map[string]any{"isError": isError, "content": []any{map[string]any{"type": "text", "text": body}}}
	}
	var args struct {
		Tool      string          `json:"tool"`
		Board     string          `json:"board"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if filingDecode(arguments, &args) != nil || args.Tool == "" {
		return text(true, `Invalid blerg_board arguments: {"tool": …, "board": …, "arguments": {…}}.`)
	}
	inner := r.Clone(r.Context())
	copyURL := *r.URL
	inner.URL = &copyURL
	capture := &filingToolCapture{header: make(http.Header)}
	if args.Tool == "access" {
		inner.Method = http.MethodGet
		inner.Body = http.NoBody
		a.HandleSessionBoardAccess(capture, inner)
		return text(capture.status >= 400, capture.body.String())
	}
	data, _ := json.Marshal(boardToolRequest{Board: args.Board, Tool: args.Tool, Arguments: args.Arguments})
	inner.Method = http.MethodPost
	inner.Body = io.NopCloser(bytes.NewReader(data))
	a.HandleSessionBoardTool(capture, inner)
	if capture.status == 0 || capture.status == http.StatusOK {
		var result map[string]any
		if json.Unmarshal(capture.body.Bytes(), &result) == nil && result["content"] != nil {
			return result
		}
		return text(true, "The board returned an unreadable result.")
	}
	var refusal struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(capture.body.Bytes(), &refusal) == nil && refusal.Error != "" {
		return text(true, refusal.Error)
	}
	return text(true, capture.body.String())
}
