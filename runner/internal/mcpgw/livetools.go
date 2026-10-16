package mcpgw

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// ValidConnectionName reports whether s is usable as a gateway path segment (the shape
// CreateGrants requires of a connection name).
func ValidConnectionName(s string) bool { return connectionName.MatchString(s) }

// LiveToolHashes lists the connection's tools at the upstream right now, through the very
// code path the gateway uses for a session (the netguard client, the credential core hands
// out for proof, the upstream handshake, every page of tools/list) and returns each tool's
// ToolHash by name. It is how a session launch checks that the hash the person confirmed is
// still what the upstream serves (spec 6.1). A name the upstream lists twice maps to "",
// which matches nothing.
//
// urlSnapshot is the connection URL the caller saw; like a grant's snapshot it must share
// scheme, host and port with the URL core returns. Nothing is stored: no grant, no cached
// state, no token. At most hashRefreshMaxPages pages are read (never unbounded, and uncharged
// because nothing is stored to charge). ErrConnectionGone is returned (wrapped) when core does not know the
// connection for this proof.
func (g *Gateway) LiveToolHashes(ctx context.Context, proof Proof, connectionID, urlSnapshot string) (map[string]string, error) {
	if !proof.Valid() {
		return nil, errors.New("no liveness proof for the tool listing")
	}
	ctx, cancel := context.WithTimeout(ctx, g.cfg.CallTimeout)
	defer cancel()
	gr := &db.MCPGrant{
		ConnectionID: connectionID, AccountID: proof.AccountID, URLSnapshot: urlSnapshot,
		ProofKind: proof.Kind(), ProofValue: proof.Value(),
	}
	st := &grantState{g: g, sem: make(chan struct{}, 1)}
	tools, _, rerr, err := st.listUpstream(ctx, gr, hashRefreshMaxPages, false)
	if err != nil {
		return nil, err
	}
	if rerr != nil {
		return nil, errors.New("the upstream server refused to list its tools")
	}
	out := make(map[string]string, len(tools))
	for _, t := range tools {
		if _, dup := out[t.Name]; dup {
			out[t.Name] = ""
			continue
		}
		out[t.Name] = t.Hash
	}
	return out, nil
}

// ListedTool is one tool of a live upstream listing as the tool picker shows it: everything
// a person needs to decide, and the ToolHash the launch will pin. Annotations is what the
// server claims (readOnlyHint and the like); it is display only and never part of the hash.
type ListedTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
	Hash        string          `json:"hash"`
	// Unsafe is "" or the ClassifyDefinition class that makes the definition unfit to select.
	Unsafe string `json:"unsafe,omitempty"`
}

// ListToolsComplete is LiveToolHashes with the tools themselves: the same code path and the same
// nothing-stored guarantee (no grant, no token, no credential in the result). A name the
// upstream lists twice is left out entirely, because its hash matches nothing. complete is
// false when the listing was cut short (page or tool cap), so a tool missing from it may still
// exist. Unsafe on each tool is "" or the ClassifyDefinition class of its definition.
func (g *Gateway) ListToolsComplete(ctx context.Context, proof Proof, connectionID, urlSnapshot string) ([]ListedTool, bool, error) {
	if !proof.Valid() {
		return nil, false, errors.New("no liveness proof for the tool listing")
	}
	ctx, cancel := context.WithTimeout(ctx, g.cfg.CallTimeout)
	defer cancel()
	gr := &db.MCPGrant{
		ConnectionID: connectionID, AccountID: proof.AccountID, URLSnapshot: urlSnapshot,
		ProofKind: proof.Kind(), ProofValue: proof.Value(),
	}
	st := &grantState{g: g, sem: make(chan struct{}, 1)}
	tools, complete, rerr, err := st.listUpstream(ctx, gr, hashRefreshMaxPages, false)
	if err != nil {
		return nil, false, err
	}
	if rerr != nil {
		return nil, false, errors.New("the upstream server refused to list its tools")
	}
	count := map[string]int{}
	for _, t := range tools {
		count[t.Name]++
	}
	out := make([]ListedTool, 0, len(tools))
	for _, t := range tools {
		if count[t.Name] > 1 {
			continue
		}
		canon, _ := CanonicalTool(t.Name, t.Description, t.InputSchema)
		out = append(out, ListedTool{
			Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
			Annotations: t.Annotations, Hash: t.Hash, Unsafe: ClassifyDefinition(canon),
		})
	}
	return out, complete, nil
}

// ListTools is ListToolsComplete without the completeness flag.
func (g *Gateway) ListTools(ctx context.Context, proof Proof, connectionID, urlSnapshot string) ([]ListedTool, error) {
	tools, _, err := g.ListToolsComplete(ctx, proof, connectionID, urlSnapshot)
	return tools, err
}
