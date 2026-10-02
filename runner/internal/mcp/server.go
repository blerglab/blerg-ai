// Package mcp exposes the runner's session contract (agent contract v1, spec
// §4) as MCP tools over streamable HTTP: JSON-RPC 2.0 at POST /mcp. It is the
// same seven operations the REST contract serves, for agents whose native way
// to call a service is a tool rather than a URL — structured arguments instead
// of hand-built request bodies, and failures that come back as tool errors
// they can act on.
//
// Every tool is a thin wrapper over the API method the REST handler calls, so
// the two surfaces share validation, idempotency and error text by
// construction rather than by review. There are no HTTP round-trips back into
// the runner.
//
// Every failure — a contract rejection, a malformed argument object, a tool
// that does not exist — comes back as `isError: true` with the same body
// shape: {"error": <message>, "status": <the HTTP status the REST path would
// have used>}. An agent can branch on the status without reading prose.
//
// One class of message is deliberately NOT shared: a type error in the
// arguments. REST decodes a request body and MCP decodes a tool-call argument
// object, so "text must be a string" is worded by encoding/json in each case
// against a different structure. Everything the CONTRACT rejects — a missing
// repo, an unusable idempotency key, a session that is not the caller's — is
// byte-for-byte identical on both surfaces.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/server"
)

// serverVersion is the MCP serverInfo version. It tracks the agent contract,
// not the runner build: a client reads it to know which tools exist.
const serverVersion = "1.0.0"

// protocolVersion is the MCP revision this server implements.
const protocolVersion = "2025-06-18"

type Server struct {
	api *server.API
}

func New(a *server.API) *Server { return &Server{api: a} }

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
	// The same credential, checked the same way, as every REST route of the
	// contract — including the 404 an install with the contract switched off
	// answers with. AuthorizeRunner writes the failure itself.
	principal, ok := s.api.AuthorizeRunner(w, r)
	if !ok {
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
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "blerg-runner", "version": serverVersion},
		}, nil)
	case "ping":
		writeRPC(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{"tools": toolDefs}, nil)
	case "tools/call":
		s.handleToolCall(r.Context(), w, req, principal)
	default:
		writeRPC(w, req.ID, nil, &rpcError{Code: -32601, Message: "method not found"})
	}
}

// toolText wraps a JSON-serialisable result as MCP tool output.
func toolText(v any) map[string]any {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Nothing this package returns is unserialisable, but a silent empty
		// block would be a worse answer than saying so.
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": `{"error":"result could not be serialised"}`}},
			"isError": true,
		}
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(b)}}}
}

func toolError(v any) map[string]any {
	out := toolText(v)
	out["isError"] = true
	return out
}

// apiToolError renders a contract failure with the SAME message the REST path
// would have put in the body, plus the status it would have used — an agent
// that has to distinguish "gone" from "busy" can, without parsing prose.
func apiToolError(e *server.APIError) map[string]any {
	return toolError(map[string]any{"error": e.Message, "status": e.Status})
}

func (s *Server) handleToolCall(ctx context.Context, w http.ResponseWriter, req rpcRequest, p server.RunnerPrincipal) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &call); err != nil {
		writeRPC(w, req.ID, nil, &rpcError{Code: -32602, Message: "invalid params"})
		return
	}
	writeRPC(w, req.ID, s.dispatch(ctx, p, call.Name, call.Arguments), nil)
}

// sessionArg is the argument shape of every tool that names a session.
type sessionArg struct {
	SessionID string `json:"session_id"`
}

// decodeArgs unmarshals a tool's arguments, reporting a malformed object as a
// tool error rather than a transport error: the agent, not the transport, is
// what has to be told to try again differently.
func decodeArgs(args json.RawMessage, into any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	if err := json.Unmarshal(args, into); err != nil {
		// Same {error, status} shape as every contract failure, so a client
		// never has to tell two error envelopes apart. 400: the arguments are
		// malformed, exactly as a bad request body would be over REST.
		return toolError(map[string]any{
			"error":  "invalid arguments: " + err.Error(),
			"status": http.StatusBadRequest,
		})
	}
	return nil
}

func (s *Server) dispatch(ctx context.Context, p server.RunnerPrincipal, name string, args json.RawMessage) map[string]any {
	switch name {
	case "start_session":
		var a struct {
			server.RunnerStartRequest
			// A pointer so "absent" and "present but empty" stay different
			// things: the second is a caller bug that would silently drop the
			// retry protection the caller thinks it has, exactly as an empty
			// Idempotency-Key header is over REST.
			IdempotencyKey *string `json:"idempotency_key"`
		}
		// MCP connections can only be attached from the launch sheet by a signed-in person: an
		// `mcp` argument is refused, never dropped as an unknown key.
		if apiErr := server.RejectMCPArgument(args); apiErr != nil {
			return apiToolError(apiErr)
		}
		if bad := decodeArgs(args, &a); bad != nil {
			return bad
		}
		idemKey := ""
		if a.IdempotencyKey != nil {
			if apiErr := server.CheckIdempotencyKey(*a.IdempotencyKey); apiErr != nil {
				return apiToolError(apiErr)
			}
			idemKey = *a.IdempotencyKey
		}
		resp, apiErr := s.api.StartSession(ctx, p, a.RunnerStartRequest, idemKey)
		if apiErr != nil {
			return apiToolError(apiErr)
		}
		out := map[string]any{"session_id": resp.SessionID}
		if resp.Replayed {
			// The caller asked twice under one key; say which answer this is
			// so a retry loop does not count it as a second session.
			out["idempotent_replayed"] = true
		}
		return toolText(out)

	case "get_session":
		var a sessionArg
		if bad := decodeArgs(args, &a); bad != nil {
			return bad
		}
		body, apiErr := s.api.SessionStatus(ctx, p, a.SessionID)
		if apiErr != nil {
			return apiToolError(apiErr)
		}
		return toolText(body)

	case "send_message":
		var a struct {
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
			Source    string `json:"source"`
		}
		if bad := decodeArgs(args, &a); bad != nil {
			return bad
		}
		body, apiErr := s.api.SendMessage(ctx, p, a.SessionID, a.Text, a.Source)
		if apiErr != nil {
			return apiToolError(apiErr)
		}
		return toolText(body)

	case "interrupt_session":
		var a sessionArg
		if bad := decodeArgs(args, &a); bad != nil {
			return bad
		}
		body, apiErr := s.api.Interrupt(ctx, p, a.SessionID)
		if apiErr != nil {
			return apiToolError(apiErr)
		}
		return toolText(body)

	case "stop_session":
		var a sessionArg
		if bad := decodeArgs(args, &a); bad != nil {
			return bad
		}
		body, apiErr := s.api.Stop(ctx, p, a.SessionID)
		if apiErr != nil {
			return apiToolError(apiErr)
		}
		return toolText(body)

	case "get_events":
		var a struct {
			SessionID string `json:"session_id"`
			AfterSeq  int64  `json:"after_seq"`
			Limit     int    `json:"limit"`
		}
		if bad := decodeArgs(args, &a); bad != nil {
			return bad
		}
		body, apiErr := s.api.Events(ctx, p, a.SessionID, a.AfterSeq, a.Limit)
		if apiErr != nil {
			return apiToolError(apiErr)
		}
		return toolText(body)

	case "get_result":
		var a sessionArg
		if bad := decodeArgs(args, &a); bad != nil {
			return bad
		}
		res, apiErr := s.api.Result(ctx, p, a.SessionID)
		if apiErr != nil {
			return apiToolError(apiErr)
		}
		return toolText(res)
	}
	// A tool this server does not have is the tool-surface equivalent of an
	// unrouted path, and carries the status to match.
	return toolError(map[string]any{
		"error":  fmt.Sprintf("unknown tool %q", name),
		"status": http.StatusNotFound,
	})
}
