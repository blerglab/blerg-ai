package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// DataPlaneConfig points the memory/rules/knowledge tools at the blerg-runner
// server. Embedding happens server-side — no embedding key here.
type DataPlaneConfig struct {
	Base    string
	Token   string
	Project string
}

func (c DataPlaneConfig) call(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("data plane: %d: %s", resp.StatusCode, raw)
	}
	return raw, nil
}

// MemoryWrite returns the "memory_write" tool: persist agent knowledge for
// this project in blerg-runner (never in the repo's human documents).
func MemoryWrite(cfg DataPlaneConfig) agent.Tool {
	return fnTool{
		def: agent.ToolDef{
			Name:        "memory_write",
			Description: "Save/update a project memory (agent knowledge that persists across sessions, stored in blerg-runner — never committed to the repo). kinds: user|feedback|project|reference. Upserts by name.",
			InputSchema: schema(`{"type":"object","properties":{"name":{"type":"string","description":"kebab-case slug"},"kind":{"type":"string","enum":["user","feedback","project","reference"]},"content":{"type":"string"}},"required":["name","content"]}`),
		},
		mutating: true,
		run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var args struct{ Name, Kind, Content string }
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			_, err := cfg.call(ctx, http.MethodPost, "/api/agent/memories", map[string]string{
				"project": cfg.Project, "name": args.Name, "kind": args.Kind, "content": args.Content,
			})
			if err != nil {
				return "", err
			}
			return "memory saved: " + args.Name, nil
		},
	}
}

// MemoryDelete returns the "memory_delete" tool.
func MemoryDelete(cfg DataPlaneConfig) agent.Tool {
	return fnTool{
		def: agent.ToolDef{
			Name:        "memory_delete",
			Description: "Delete a project memory that turned out to be wrong or obsolete.",
			InputSchema: schema(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
		},
		mutating: true,
		run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var args struct{ Name string }
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			q := url.Values{"project": {cfg.Project}, "name": {args.Name}}
			_, err := cfg.call(ctx, http.MethodDelete, "/api/agent/memories?"+q.Encode(), nil)
			if err != nil {
				return "", err
			}
			return "memory deleted: " + args.Name, nil
		},
	}
}

// RulePropose returns the "rule_propose" tool: rules change agent behavior,
// so they land disabled until the user approves them in the blerg-runner UI.
func RulePropose(cfg DataPlaneConfig) agent.Tool {
	return fnTool{
		def: agent.ToolDef{
			Name:        "rule_propose",
			Description: "Propose a standing rule for this project (injected into every future session's instructions). Rules take effect only after the user approves them in the blerg-runner UI.",
			InputSchema: schema(`{"type":"object","properties":{"content":{"type":"string"}},"required":["content"]}`),
		},
		mutating: true,
		run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var args struct{ Content string }
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			raw, err := cfg.call(ctx, http.MethodPost, "/api/agent/rules", map[string]string{
				"project": cfg.Project, "content": args.Content,
			})
			if err != nil {
				return "", err
			}
			return "rule proposed (pending user approval): " + string(raw), nil
		},
	}
}

// KnowledgeSearch returns the "knowledge_search" tool.
func KnowledgeSearch(cfg DataPlaneConfig) agent.Tool {
	return fnTool{
		def: agent.ToolDef{
			Name:        "knowledge_search",
			Description: "Search this project's saved memories (semantic when embeddings are configured, keyword otherwise). Returns scored snippets.",
			InputSchema: schema(`{"type":"object","properties":{"query":{"type":"string"},"k":{"type":"integer"}},"required":["query"]}`),
		},
		run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var args struct {
				Query string `json:"query"`
				K     int    `json:"k"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			raw, err := cfg.call(ctx, http.MethodPost, "/api/agent/knowledge-search", map[string]any{
				"project": cfg.Project, "query": args.Query, "k": args.K,
			})
			if err != nil {
				return "", err
			}
			return capOutput(string(raw)), nil
		},
	}
}
