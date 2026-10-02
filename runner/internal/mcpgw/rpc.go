package mcpgw

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Protocol versions the gateway speaks to sessions. initialize is answered locally:
// the client's requested version is echoed only when it is in this set.
var supportedVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

const latestProtocolVersion = "2025-11-25"

func negotiateVersion(requested string) (version string, ok bool) {
	for _, v := range supportedVersions {
		if v == requested {
			return v, true
		}
	}
	return latestProtocolVersion, false
}

// JSON-RPC error codes used by the gateway.
const (
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeParseError     = -32700
	codeGateway        = -32000 // gateway-level refusal (budget, busy, upstream failure)
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcMessage is any JSON-RPC message, in either direction.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (m rpcMessage) hasID() bool {
	id := bytes.TrimSpace(m.ID)
	return len(id) > 0 && string(id) != "null"
}

func (m rpcMessage) idString() string { return string(bytes.TrimSpace(m.ID)) }

// validID reports whether raw is a JSON string or number, the only legal request ids.
func validID(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	return raw[0] == '"' || raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9')
}

var errNoResponse = errors.New("stream ended without a response")

// readSSEResponse reads a text/event-stream body until the JSON-RPC response whose id is
// wantID arrives and returns it. Notifications and server-initiated requests interleaved
// in the stream are ignored (the gateway supports neither).
func readSSEResponse(r io.Reader, wantID string) (rpcMessage, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var data []string
	flush := func() (rpcMessage, bool) {
		if len(data) == 0 {
			return rpcMessage{}, false
		}
		payload := strings.Join(data, "\n")
		data = nil
		var m rpcMessage
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			return rpcMessage{}, false
		}
		if m.Method == "" && m.hasID() && m.idString() == wantID {
			return m, true
		}
		return rpcMessage{}, false
	}
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if m, ok := flush(); ok {
					return m, nil
				}
			case strings.HasPrefix(line, ":"):
				// comment / keepalive
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if m, ok := flush(); ok {
					return m, nil
				}
				return rpcMessage{}, errNoResponse
			}
			return rpcMessage{}, err
		}
	}
}
