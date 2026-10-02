// Package mcp exposes blerg-board's primary agent surface: an MCP server over
// streamable HTTP (JSON-RPC 2.0 at POST /mcp). MCP is primary because card
// writes carry structured `fields` validated against the board schema —
// through argv that is quoting hell; MCP takes objects natively. Denials
// come back as structured tool errors the agent can act on.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

type Server struct {
	api *api.API
}

func New(a *api.API) *Server { return &Server{api: a} }

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *rpcError) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	principal, err := s.api.Auth.Resolve(r)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPC(w, nil, nil, &rpcError{Code: -32700, Message: "parse error"})
		return
	}
	// Notifications (no id) are acknowledged with 202 and no body.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "blerg-board", "version": "0.1.0"},
		}, nil)
	case "ping":
		writeRPC(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{"tools": visibleTools(principal)}, nil)
	case "tools/call":
		s.handleToolCall(r.Context(), w, req, principal)
	default:
		writeRPC(w, req.ID, nil, &rpcError{Code: -32601, Message: "method not found"})
	}
}

// toolText wraps a JSON-serialisable result as MCP tool output.
func toolText(v any) map[string]any {
	b, _ := json.MarshalIndent(v, "", "  ")
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(b)}}}
}

func toolError(v any) map[string]any {
	out := toolText(v)
	out["isError"] = true
	return out
}

func (s *Server) handleToolCall(ctx context.Context, w http.ResponseWriter, req rpcRequest, p auth.Principal) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &call); err != nil {
		writeRPC(w, req.ID, nil, &rpcError{Code: -32602, Message: "invalid params"})
		return
	}
	result, err := s.dispatch(ctx, p, call.Name, call.Arguments)
	if errors.Is(err, errInvalidArguments) {
		writeRPC(w, req.ID, nil, &rpcError{Code: -32602, Message: err.Error()})
		return
	}
	if err != nil {
		writeRPC(w, req.ID, toolError(map[string]string{"error": err.Error()}), nil)
		return
	}
	writeRPC(w, req.ID, result, nil)
}

// resolveCard accepts a card uuid, or a board_id + "#42"-style number.
func (s *Server) resolveCard(ctx context.Context, cardID, boardID string, number int) (db.Card, error) {
	if cardID != "" {
		return db.GetCard(ctx, s.api.Pool, cardID)
	}
	if boardID != "" && number > 0 {
		return db.GetCardByNumber(ctx, s.api.Pool, boardID, number)
	}
	return db.Card{}, fmt.Errorf("pass card_id, or board_id + number")
}

// errInvalidArguments marks tool arguments that are refused before any tool
// (or scope check) sees them; handleToolCall answers them as invalid params.
var errInvalidArguments = errors.New("invalid params")

// canonicalArgs decodes a tool call's arguments exactly ONCE into the single
// form both the scope check and the tool use. Go's struct decode is
// case-insensitive and the last matching key wins, so two spellings of one key
// ("review_id" and "REVIEW_ID") would let a check and a tool read different
// values. Here the arguments must be one JSON object, with nothing after it,
// and no key may appear twice under ANY spelling (exact, case-folded, or
// written with unicode escapes, which the decoder resolves first). The result
// has each key exactly once, in a fixed order, with string-wrapped JSON
// values ("fields", "field_schema") unwrapped; nothing downstream re-decodes
// the original bytes.
func canonicalArgs(raw json.RawMessage) (json.RawMessage, error) {
	if t := bytes.TrimSpace(raw); len(t) == 0 || string(t) == "null" {
		return json.RawMessage(`{}`), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("%w: arguments must be a JSON object", errInvalidArguments)
	}
	vals := map[string]json.RawMessage{}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: malformed arguments", errInvalidArguments)
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("%w: malformed arguments", errInvalidArguments)
		}
		for _, seen := range keys {
			if strings.EqualFold(seen, key) {
				return nil, fmt.Errorf("%w: argument %q is given more than once", errInvalidArguments, key)
			}
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("%w: malformed arguments", errInvalidArguments)
		}
		keys = append(keys, key)
		vals[key] = v
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, fmt.Errorf("%w: malformed arguments", errInvalidArguments)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: unexpected data after the arguments object", errInvalidArguments)
	}
	unwrapStringJSON(vals, "field_schema", "fields")
	out, err := json.Marshal(vals)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed arguments", errInvalidArguments)
	}
	return out, nil
}

// unwrapStringJSON re-parses named args that arrived as string-encoded JSON
// ("[{...}]" instead of [{...}]), in place. Some MCP clients stringify values
// whose schema property they can't type; strict unmarshals downstream would
// 400. The keys are already unique (canonicalArgs), so this cannot change
// which value any key resolves to.
func unwrapStringJSON(m map[string]json.RawMessage, keys ...string) {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok || len(raw) == 0 || raw[0] != '"' {
			continue
		}
		var inner string
		if json.Unmarshal(raw, &inner) != nil {
			continue
		}
		trimmed := strings.TrimSpace(inner)
		if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
			continue
		}
		if !json.Valid([]byte(trimmed)) {
			continue
		}
		m[k] = json.RawMessage(trimmed)
	}
}

func (s *Server) dispatch(ctx context.Context, p auth.Principal, name string, args json.RawMessage) (map[string]any, error) {
	pool := s.api.Pool
	// Decode once; the scope check and the tool both read this same value.
	args, err := canonicalArgs(args)
	if err != nil {
		return nil, err
	}
	if err := s.scopeCheck(ctx, p, name, args); err != nil {
		return nil, err
	}
	switch name {
	case "blerg_board_list":
		boards, err := db.ListBoards(ctx, pool)
		if err != nil {
			return nil, err
		}
		if p.Token != nil && p.Token.BoardID != nil {
			scoped := boards[:0]
			for _, b := range boards {
				if b.ID == *p.Token.BoardID {
					scoped = append(scoped, b)
				}
			}
			boards = scoped
		}
		return toolText(boards), nil

	case "blerg_board_get", "blerg_board_schema":
		var a struct {
			BoardID string `json:"board_id"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.BoardID == "" {
			return nil, fmt.Errorf("board_id required")
		}
		if err := p.RequireBoard(a.BoardID, "card.read"); err != nil {
			return nil, err
		}
		b, err := db.GetBoard(ctx, pool, a.BoardID)
		if err != nil {
			return nil, err
		}
		if name == "blerg_board_schema" {
			return toolText(b.FieldSchema), nil
		}
		return toolText(b), nil

	case "blerg_board_create":
		if err := p.RequireAdmin(""); err != nil {
			return nil, err
		}
		var params db.BoardParams
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		b, err := db.CreateBoard(ctx, pool, params)
		if err != nil {
			return nil, err
		}
		return toolText(b), nil

	case "blerg_board_update":
		var a struct {
			BoardID string `json:"board_id"`
			db.BoardParams
		}
		if err := json.Unmarshal(args, &a); err != nil || a.BoardID == "" {
			return nil, fmt.Errorf("board_id required")
		}
		if err := p.RequireAdmin(a.BoardID); err != nil {
			return nil, err
		}
		b, err := db.UpdateBoard(ctx, pool, a.BoardID, a.BoardParams)
		if err != nil {
			return nil, err
		}
		return toolText(b), nil

	case "blerg_column_list":
		var a struct {
			BoardID string `json:"board_id"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.BoardID == "" {
			return nil, fmt.Errorf("board_id required")
		}
		if err := p.RequireBoard(a.BoardID, "card.read"); err != nil {
			return nil, err
		}
		cols, err := db.ListColumns(ctx, pool, a.BoardID)
		if err != nil {
			return nil, err
		}
		return toolText(cols), nil

	case "blerg_column_create":
		var a struct {
			BoardID    string `json:"board_id"`
			Name       string `json:"name"`
			IsTerminal bool   `json:"is_terminal"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.BoardID == "" || a.Name == "" {
			return nil, fmt.Errorf("board_id and name required")
		}
		if err := p.RequireBoard(a.BoardID, "column.write"); err != nil {
			return nil, err
		}
		col, err := db.CreateColumn(ctx, pool, a.BoardID, a.Name, a.IsTerminal)
		if err != nil {
			return nil, err
		}
		return toolText(col), nil

	case "blerg_column_move":
		var a struct {
			ColumnID string  `json:"column_id"`
			BeforeID *string `json:"before_id"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.ColumnID == "" {
			return nil, fmt.Errorf("column_id required")
		}
		col, err := db.GetColumn(ctx, pool, a.ColumnID)
		if err != nil {
			return nil, err
		}
		if err := p.RequireBoard(col.BoardID, "column.write"); err != nil {
			return nil, err
		}
		moved, err := db.MoveColumn(ctx, pool, a.ColumnID, a.BeforeID)
		if err != nil {
			return nil, err
		}
		return toolText(moved), nil

	case "blerg_card_search":
		var a struct {
			BoardID         string            `json:"board_id"`
			Query           string            `json:"query"`
			Type            string            `json:"type"`
			Tag             string            `json:"tag"`
			Priority        string            `json:"priority"`
			Fields          map[string]string `json:"fields"`
			IncludeArchived bool              `json:"include_archived"`
			Limit           int               `json:"limit"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.BoardID == "" {
			return nil, fmt.Errorf("board_id required")
		}
		if err := p.RequireBoard(a.BoardID, "card.read"); err != nil {
			return nil, err
		}
		cards, err := db.SearchCards(ctx, pool, db.SearchParams{
			BoardID: a.BoardID, Query: a.Query, Type: a.Type, Tag: a.Tag,
			Priority: a.Priority, Fields: a.Fields,
			IncludeArchived: a.IncludeArchived, Limit: a.Limit,
		})
		if err != nil {
			return nil, err
		}
		return toolText(cards), nil

	case "blerg_card_get":
		var a struct {
			CardID  string `json:"card_id"`
			BoardID string `json:"board_id"`
			Number  int    `json:"number"`
		}
		_ = json.Unmarshal(args, &a)
		card, err := s.resolveCard(ctx, a.CardID, a.BoardID, a.Number)
		if err != nil {
			return nil, err
		}
		if err := p.RequireBoard(card.BoardID, "card.read"); err != nil {
			return nil, err
		}
		return toolText(card), nil

	case "blerg_card_create":
		var a struct {
			BoardID string `json:"board_id"`
			db.CardParams
			DisputeOf string `json:"dispute_of"`
			Rebuttal  string `json:"rebuttal"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.BoardID == "" {
			return nil, fmt.Errorf("board_id required")
		}
		res, err := s.api.CreateCardGated(ctx, p, a.BoardID, a.CardParams, a.DisputeOf, a.Rebuttal)
		if err != nil {
			return nil, err
		}
		if res.Denied != nil {
			return toolError(res.Denied.Body), nil
		}
		out := map[string]any{"card": res.Card, "refreshed": res.Refreshed}
		return toolText(out), nil

	case "blerg_card_update":
		var a struct {
			CardID  string `json:"card_id"`
			BoardID string `json:"board_id"`
			Number  int    `json:"number"`
			db.CardParams
			DisputeOf string `json:"dispute_of"`
			Rebuttal  string `json:"rebuttal"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		card, err := s.resolveCard(ctx, a.CardID, a.BoardID, a.Number)
		if err != nil {
			return nil, err
		}
		res, err := s.api.UpdateCardGated(ctx, p, card.ID, a.CardParams, a.DisputeOf, a.Rebuttal)
		if err != nil {
			return nil, err
		}
		if res.Denied != nil {
			return toolError(res.Denied.Body), nil
		}
		return toolText(res.Card), nil

	case "blerg_card_move":
		var a struct {
			CardID     string  `json:"card_id"`
			BoardID    string  `json:"board_id"`
			Number     int     `json:"number"`
			ColumnID   string  `json:"column_id"`
			ColumnName string  `json:"column_name"`
			Before     *string `json:"before_card_id"`
			IfMatch    *int    `json:"if_match"`
			DisputeOf  string  `json:"dispute_of"`
			Rebuttal   string  `json:"rebuttal"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		card, err := s.resolveCard(ctx, a.CardID, a.BoardID, a.Number)
		if err != nil {
			return nil, err
		}
		columnID := a.ColumnID
		if columnID == "" && a.ColumnName != "" {
			cols, err := db.ListColumns(ctx, pool, card.BoardID)
			if err != nil {
				return nil, err
			}
			for _, c := range cols {
				if c.Name == a.ColumnName {
					columnID = c.ID
					break
				}
			}
			if columnID == "" {
				return nil, fmt.Errorf("no column named %q", a.ColumnName)
			}
		}
		if columnID == "" {
			return nil, fmt.Errorf("column_id or column_name required")
		}
		res, err := s.api.MoveCardGated(ctx, p, card.ID, columnID, a.Before, a.IfMatch, a.DisputeOf, a.Rebuttal)
		if err != nil {
			return nil, err
		}
		if res.Denied != nil {
			return toolError(res.Denied.Body), nil
		}
		return toolText(res.Card), nil

	case "blerg_card_archive":
		var a struct {
			CardID  string `json:"card_id"`
			BoardID string `json:"board_id"`
			Number  int    `json:"number"`
		}
		_ = json.Unmarshal(args, &a)
		card, err := s.resolveCard(ctx, a.CardID, a.BoardID, a.Number)
		if err != nil {
			return nil, err
		}
		res, err := s.api.ArchiveCardGated(ctx, p, card.ID)
		if err != nil {
			return nil, err
		}
		if res.Denied != nil {
			return toolError(res.Denied.Body), nil
		}
		return toolText(res.Card), nil

	case "blerg_card_link":
		var a struct {
			CardID  string `json:"card_id"`
			BoardID string `json:"board_id"`
			Number  int    `json:"number"`
			Kind    string `json:"kind"`
			URL     string `json:"url"`
			Label   string `json:"label"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.Kind == "" || a.URL == "" {
			return nil, fmt.Errorf("kind and url required")
		}
		card, err := s.resolveCard(ctx, a.CardID, a.BoardID, a.Number)
		if err != nil {
			return nil, err
		}
		if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
			return nil, err
		}
		links := []db.Link{{Kind: a.Kind, URL: a.URL, Label: &a.Label}}
		res, err := s.api.UpdateCardGated(ctx, p, card.ID, db.CardParams{AddLinks: &links}, "", "")
		if err != nil {
			return nil, err
		}
		if res.Denied != nil {
			return toolError(res.Denied.Body), nil
		}
		return toolText(res.Card), nil

	case "blerg_card_comment":
		var a struct {
			CardID  string `json:"card_id"`
			BoardID string `json:"board_id"`
			Number  int    `json:"number"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.Text == "" {
			return nil, fmt.Errorf("text required")
		}
		card, err := s.resolveCard(ctx, a.CardID, a.BoardID, a.Number)
		if err != nil {
			return nil, err
		}
		if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
			return nil, err
		}
		if err := db.AppendComment(ctx, pool, card.ID, a.Text, p.EventMeta()); err != nil {
			return nil, err
		}
		s.api.Hub.Broadcast(card.BoardID, "card_changed")
		return toolText(map[string]bool{"ok": true}), nil

	case "blerg_review_get":
		var a struct {
			ReviewID string `json:"review_id"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.ReviewID == "" {
			return nil, fmt.Errorf("review_id required")
		}
		rev, err := db.GetReview(ctx, pool, a.ReviewID)
		if err != nil {
			return nil, err
		}
		// Authorize on the review's own board, like REST: the scope check
		// only covers project-scoped tokens, a native board-scoped agent
		// token must be confined here.
		if err := p.RequireBoard(rev.BoardID, "card.read"); err != nil {
			return nil, err
		}
		return toolText(rev), nil

	case "blerg_session_set_model":
		var a struct {
			Model           string `json:"model"`
			Reason          string `json:"reason"`
			RunnerSessionID string `json:"runner_session_id"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.Model == "" {
			return nil, fmt.Errorf("model required")
		}
		// Empty session id means "self" — resolved from the caller's own
		// session token, so an agent never has to know its session id.
		change, err := s.api.SetSessionModel(ctx, p, a.RunnerSessionID, a.Model, a.Reason)
		if err != nil {
			return nil, err
		}
		return toolText(change), nil
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

var _ = strconv.Itoa
