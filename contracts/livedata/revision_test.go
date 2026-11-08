package livedata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/livemcp"
	"testing"
	"time"
)

func approvalRevision(now time.Time) Revision {
	r := Revision{Scope: scope(), Arguments: json.RawMessage(`{"symbol":"NVDA","limit":3}`), Mapping: livemcp.Mapping{RowsPath: "/quotes", Fields: []livemcp.Field{{Name: "value", Path: "/close", Type: "number"}}, Keys: []string{"value"}}, IntervalSeconds: 60, ExpiresAt: now.Add(time.Hour), OwnerOnly: true}
	r.ToolDefinition = json.RawMessage(`{"description":"Market quotes","inputSchema":{"additionalProperties":false,"properties":{"limit":{"type":"integer"},"symbol":{"type":"string"}},"required":["symbol"],"type":"object"},"name":"quotes"}`)
	r.ToolDefinition, _ = livemcp.CanonicalObject(r.ToolDefinition)
	sum := sha256.Sum256(r.ToolDefinition)
	r.Scope.Hash = hex.EncodeToString(sum[:])
	return r
}
func TestRevisionDigest(t *testing.T) {
	now := time.Now()
	r := approvalRevision(now)
	good, raw, err := r.Canonical(now)
	if err != nil {
		t.Fatal(err)
	}
	reordered := r
	reordered.Arguments = json.RawMessage(`{ "limit": 3, "symbol": "NVDA" }`)
	same, raw2, err := reordered.Canonical(now)
	if err != nil || same.Scope.Digest != good.Scope.Digest || !bytes.Equal(raw, raw2) {
		t.Fatal("unstable canonical revision")
	}
	for name, mutate := range map[string]func(*Revision){"arguments": func(r *Revision) { r.Arguments = json.RawMessage(`{"symbol":"AMD"}`) }, "connection": func(r *Revision) { r.Scope.Connection = r.Scope.Owner }, "hash": func(r *Revision) { r.Scope.Hash = r.Scope.Digest }, "mapping": func(r *Revision) { r.Mapping.RowsPath = "/changed" }, "interval": func(r *Revision) { r.IntervalSeconds++ }, "expiry": func(r *Revision) { r.ExpiresAt = r.ExpiresAt.Add(time.Minute) }} {
		t.Run(name, func(t *testing.T) {
			changed := approvalRevision(now)
			mutate(&changed)
			other, _, err := changed.Canonical(now)
			if name != "hash" && err != nil || err == nil && other.Scope.Digest == good.Scope.Digest {
				t.Fatal("selection not bound to digest")
			}
		})
	}
	for _, args := range []string{`{"a":1,"a":2}`, `[]`, `{"access_token":"canary"}`, `{"uri":"https://example.com?api_key=canary"}`} {
		r.Arguments = json.RawMessage(args)
		if _, _, err := r.Canonical(now); err == nil {
			t.Fatal("unsafe arguments accepted")
		}
	}
}

func TestRevisionSchemaRefusals(t *testing.T) {
	now := time.Now()
	for name, schema := range map[string]string{
		"password":             `{"type":"object","additionalProperties":false,"properties":{"symbol":{"type":"string","format":"password"}}}`,
		"secret annotation":    `{"type":"object","additionalProperties":false,"properties":{"symbol":{"type":"string","writeOnly":true}}}`,
		"remote ref":           `{"type":"object","$ref":"https://example.com/schema"}`,
		"open object":          `{"type":"object","properties":{"symbol":{"type":"string"}}}`,
		"wrong type":           `{"type":"object","additionalProperties":false,"properties":{"symbol":{"type":"integer"}}}`,
		"malformed constraint": `{"type":"object","additionalProperties":false,"properties":{"symbol":{"type":"string","maxLength":"5"}}}`,
		"missing required":     `{"type":"object","additionalProperties":false,"properties":{"symbol":{"type":"string"},"missing":{"type":"string"}},"required":["missing"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := approvalRevision(now)
			r.Arguments = json.RawMessage(`{"symbol":"NVDA"}`)
			r.ToolDefinition = json.RawMessage(`{"description":"Market quotes","inputSchema":` + schema + `,"name":"quotes"}`)
			r.ToolDefinition, _ = livemcp.CanonicalObject(r.ToolDefinition)
			sum := sha256.Sum256(r.ToolDefinition)
			r.Scope.Hash = hex.EncodeToString(sum[:])
			if _, _, err := r.Canonical(now); err == nil {
				t.Fatal("ineligible schema accepted")
			}
		})
	}
}

func TestRevisionNumericBounds(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name, schema, args string
		allowed            bool
	}{
		{"decimal boundary", `{"type":"number","maximum":0.1}`, `0.1`, true},
		{"decimal overflow", `{"type":"number","maximum":0.1}`, `0.10000000000000001`, false},
		{"large integer", `{"type":"integer","maximum":9007199254740992}`, `9007199254740992`, true},
		{"large integer overflow", `{"type":"integer","maximum":9007199254740992}`, `9007199254740993`, false},
		{"fractional integer", `{"type":"integer"}`, `1.5`, false},
		{"exponent allocation", `{"type":"number"}`, `1e999999999`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := approvalRevision(now)
			r.Arguments = json.RawMessage(`{"n":` + tc.args + `}`)
			r.ToolDefinition = json.RawMessage(`{"description":"Market quotes","inputSchema":{"additionalProperties":false,"properties":{"n":` + tc.schema + `},"type":"object"},"name":"quotes"}`)
			r.ToolDefinition, _ = livemcp.CanonicalObject(r.ToolDefinition)
			sum := sha256.Sum256(r.ToolDefinition)
			r.Scope.Hash = hex.EncodeToString(sum[:])
			_, _, err := r.Canonical(now)
			if (err == nil) != tc.allowed {
				t.Fatal("numeric schema boundary", err)
			}
		})
	}
}

func TestSecretArgumentCanaries(t *testing.T) {
	for _, name := range []string{"key", "pwd", "passwd", "passphrase", "sig", "session", "X-Amz-Signature"} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"nested": map[string]string{name: "secret-canary"}})
			if !SecretArguments(raw) {
				t.Fatal("credential field missed")
			}
		})
	}
	for _, value := range []string{"https://market.example?key=secret-canary", "market.example?token=secret-canary", "https://market.example?X-Amz-Signature=secret-canary", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjYW5hcnkifQ.c2ln", "ghp_secretcanary", "Bearer\tsecret-canary", "Bearer\u00a0secret-canary", "xoxb-secretcanary", "AKIAABCDEFGHIJKLMNOP", "glpat-secretcanary", "token abc", "token\tsecret-canary", "token\u00a0secret-canary"} {
		t.Run(value, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"symbol": value})
			if !SecretArguments(raw) {
				t.Fatal("credential-shaped value missed")
			}
		})
	}
	if SecretArguments([]byte(`{"symbol":"NVDA"}`)) {
		t.Fatal("ordinary argument refused")
	}
}

func TestRevisionApprovalBoundaries(t *testing.T) {
	now := time.Now()
	for name, mutate := range map[string]func(*Revision){
		"owner visibility": func(r *Revision) { r.OwnerOnly = false }, "interval": func(r *Revision) { r.IntervalSeconds = 14 }, "expiry": func(r *Revision) { r.ExpiresAt = now }, "card": func(r *Revision) { r.Card = &CardSpec{Kind: "line"} }, "arguments malformed": func(r *Revision) { r.Arguments = json.RawMessage(`{`) }, "mapping secret": func(r *Revision) {
			r.Mapping.Fields[0].Constant = json.RawMessage(`"Bearer canary"`)
			r.Mapping.Fields[0].Path = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := approvalRevision(now)
			mutate(&r)
			if _, _, e := r.Canonical(now); e == nil {
				t.Fatal("unsafe approval selection accepted")
			}
		})
	}
	r, raw, e := approvalRevision(now).Canonical(now)
	if e != nil {
		t.Fatal(e)
	}
	good := Grant{Configuration: raw, Scope: r.Scope, Arguments: r.Arguments, ExpiresAt: r.ExpiresAt}
	if _, e := good.ApprovedRevision(); e != nil {
		t.Fatal("valid grant refused")
	}
	for name, mutate := range map[string]func(*Grant){"malformed config": func(g *Grant) { g.Configuration = json.RawMessage(`{`) }, "scope": func(g *Grant) { g.Scope.Owner = g.Scope.Runner }, "arguments": func(g *Grant) { g.Arguments = json.RawMessage(`{}`) }, "expiry": func(g *Grant) { g.ExpiresAt = g.ExpiresAt.Add(time.Second) }} {
		t.Run(name, func(t *testing.T) {
			g := good
			mutate(&g)
			if _, e := g.ApprovedRevision(); e == nil {
				t.Fatal("tampered approved grant accepted")
			}
		})
	}
}
