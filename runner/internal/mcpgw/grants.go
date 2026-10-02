package mcpgw

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultCallBudget is the per-grant call budget when a GrantSpec does not set one
// (spec 5.4: 200, counting tools/list and propose calls).
const DefaultCallBudget = 200

// ToolGrant is one tool entry of a grant: mode "allow" or "propose" and the pinned hash.
type ToolGrant = db.MCPGrantTool

// Tool modes.
const (
	ModeAllow   = "allow"
	ModePropose = "propose"
)

var connectionName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// GrantSpec describes one connection a session may use.
type GrantSpec struct {
	ConnectionID string
	Name         string               // the connection name; it is the gateway path segment
	URL          string               // the connection URL at grant time (url_snapshot)
	Tools        map[string]ToolGrant // only allow/propose entries matter; the rest are off
	CallBudget   int                  // 0: DefaultCallBudget
	// Builtin marks a connection the runner provides (BuiltinBoard); BuiltinRef is what it needs
	// (the target board id). Use BoardGrantSpec; buildGrants fixes the connection id and tools.
	Builtin    string
	BuiltinRef string
}

// hashToken is how a raw gateway token is stored and looked up.
func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// newToken returns a random 256-bit token.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return "gw_" + hex.EncodeToString(b), nil
}

// CreateGrants stores one grant per spec for the session, in one transaction, and returns
// the raw gateway token for each connection id. The raw tokens exist only in the returned
// map: the database keeps their hash. The caller puts them into the session's MCP config.
// It fails if the session already has a grant for one of the connections; use
// ReissueGrants to replace the tokens on resume.
func CreateGrants(ctx context.Context, pool *pgxpool.Pool, sessionID, accountID string, proof Proof, specs []GrantSpec) (map[string]string, error) {
	rows, tokens, err := buildGrants(sessionID, accountID, proof, specs)
	if err != nil {
		return nil, err
	}
	if err := db.InsertMCPGrants(ctx, pool, rows); err != nil {
		return nil, err
	}
	return tokens, nil
}

// ReissueGrants replaces every grant of the session with new ones (new random tokens, the
// given proof) in ONE transaction, and returns the new raw tokens by connection id. The old
// tokens stop working when it commits; if it fails they are untouched.
func ReissueGrants(ctx context.Context, pool *pgxpool.Pool, sessionID, accountID string, proof Proof, specs []GrantSpec) (map[string]string, error) {
	rows, tokens, err := buildGrants(sessionID, accountID, proof, specs)
	if err != nil {
		return nil, err
	}
	if err := db.ReplaceMCPGrants(ctx, pool, sessionID, rows); err != nil {
		return nil, err
	}
	return tokens, nil
}

// buildGrants validates the specs and turns them into rows plus the raw token of each.
func buildGrants(sessionID, accountID string, proof Proof, specs []GrantSpec) ([]db.MCPGrant, map[string]string, error) {
	if sessionID == "" || accountID == "" {
		return nil, nil, errors.New("session and account are required")
	}
	if !proof.Valid() {
		return nil, nil, errors.New("exactly one of token id or session id must prove liveness")
	}
	tokens := make(map[string]string, len(specs))
	names := make(map[string]bool, len(specs))
	rows := make([]db.MCPGrant, 0, len(specs))
	for _, sp := range specs {
		if sp.Builtin != "" {
			if sp.Builtin != BuiltinBoard || sp.BuiltinRef == "" || sp.Name != BuiltinBoard {
				return nil, nil, fmt.Errorf("invalid built-in connection %q", sp.Builtin)
			}
			// The id and the tools of a built-in are not the caller's to choose.
			sp.ConnectionID, sp.Tools = BoardConnectionID, BoardTools()
		}
		if sp.ConnectionID == "" || sp.URL == "" {
			return nil, nil, errors.New("connection id and url are required")
		}
		if !connectionName.MatchString(sp.Name) {
			return nil, nil, fmt.Errorf("invalid connection name %q", sp.Name)
		}
		if names[sp.Name] {
			return nil, nil, fmt.Errorf("duplicate connection name %q", sp.Name)
		}
		names[sp.Name] = true
		if _, dup := tokens[sp.ConnectionID]; dup {
			return nil, nil, fmt.Errorf("duplicate connection %q", sp.ConnectionID)
		}
		raw, err := newToken()
		if err != nil {
			return nil, nil, err
		}
		tokens[sp.ConnectionID] = raw
		budget := sp.CallBudget
		if budget <= 0 {
			budget = DefaultCallBudget
		}
		tools := map[string]ToolGrant{}
		for n, t := range sp.Tools {
			if t.Mode == ModeAllow || t.Mode == ModePropose {
				tools[n] = t
			}
		}
		rows = append(rows, db.MCPGrant{
			SessionID: sessionID, ConnectionID: sp.ConnectionID, Name: sp.Name, AccountID: accountID,
			Tools: tools, URLSnapshot: sp.URL, ProofKind: proof.Kind(), ProofValue: proof.Value(),
			TokenHash: hashToken(raw), CallBudget: budget, Builtin: sp.Builtin, BuiltinRef: sp.BuiltinRef,
		})
	}
	return rows, tokens, nil
}

// DeleteGrantsForSession removes every grant of the session; their tokens stop working
// on the next request.
func DeleteGrantsForSession(ctx context.Context, pool *pgxpool.Pool, sessionID string) error {
	_, err := db.DeleteMCPGrantsForSession(ctx, pool, sessionID)
	return err
}
