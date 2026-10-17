package protocol

import (
	"strings"
	"time"
	"unicode"
)

const MCPStatusKind = "mcp_status"

// MCPServerStatus is public connection metadata, never an endpoint or credential.
type MCPServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Source string `json:"source"`
	Tools  int    `json:"tools"`
}

// MCPHold describes the bounded wait before the session's first native prompt.
type MCPHold struct {
	Since string `json:"since"`
	Until string `json:"until"`
}

// MCPStatusPayload is the latest native MCP connection inventory and prompt hold.
// Hold is carried on the daemon event so a reconnect can reconstruct live state.
type MCPStatusPayload struct {
	Servers  []MCPServerStatus `json:"servers"`
	Settled  bool              `json:"settled"`
	Since    string            `json:"since"`
	Engine   string            `json:"engine"`
	Released string            `json:"released,omitempty"`
	Hold     *MCPHold          `json:"hold,omitempty"`
}

// ReleaseHeldPrompt releases an existing first-prompt wait. It never starts or resumes an engine.
type ReleaseHeldPrompt struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// SanitizeMCPStatus retains bounded public connection metadata only. Both the
// daemon and the server call it; endpoint/config objects are never forwarded.
func SanitizeMCPStatus(p MCPStatusPayload) MCPStatusPayload {
	out := MCPStatusPayload{Servers: []MCPServerStatus{}, Settled: p.Settled, Since: mcpTime(p.Since), Engine: mcpLabel(p.Engine, 64, false)}
	switch p.Released {
	case "settled", "timeout", "person":
		out.Released = p.Released
	}
	seen := make(map[string]bool)
	for _, server := range p.Servers {
		name := mcpLabel(server.Name, 200, true)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if len(out.Servers) >= 500 {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(server.Status))
		switch status {
		case "pending", "connected", "failed", "needs-auth", "disabled", "unknown":
		default:
			status = "unknown"
		}
		out.Servers = append(out.Servers, MCPServerStatus{Name: name, Status: status, Source: mcpLabel(server.Source, 64, false), Tools: min(max(server.Tools, 0), 1_000_000)})
	}
	if p.Hold != nil && !out.Settled && out.Released == "" {
		since, until := mcpTime(p.Hold.Since), mcpTime(p.Hold.Until)
		start, _ := time.Parse(time.RFC3339Nano, since)
		end, _ := time.Parse(time.RFC3339Nano, until)
		if since != "" && until != "" && end.After(start) {
			out.Hold = &MCPHold{Since: since, Until: until}
		}
	}
	return out
}

func mcpTime(raw string) string {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}
func mcpLabel(raw string, limit int, spaces bool) string {
	value := strings.TrimSpace(raw)
	if value == "" || len([]rune(value)) > limit {
		return ""
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-:+", r) || (spaces && r == ' ') {
			continue
		}
		return ""
	}
	return value
}
