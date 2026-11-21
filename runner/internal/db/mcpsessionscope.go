package db

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"regexp"
	"strings"
)

// MCPSessionScope is a frozen control-plane binding, never a provider secret.
// Account grants have nil scope. The hash names one exact gateway grant issuance.
type MCPSessionScope struct {
	AccountID            string `json:"account_id"`
	RunnerInstallationID string `json:"runner_installation_id"`
	RunnerSessionID      string `json:"runner_session_id"`
	HumanSessionID       string `json:"human_session_id"`
	LaunchGeneration     int64  `json:"launch_generation"`
	CredentialGeneration int64  `json:"credential_generation"`
	GrantTokenHash       string `json:"grant_token_hash"`
}

var ErrMCPSessionScope = errors.New("scoped MCP binding is not valid")

var mcpScopeUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s MCPSessionScope) Valid() bool {
	hash, err := hex.DecodeString(s.GrantTokenHash)
	return mcpScopeUUID.MatchString(s.AccountID) && mcpScopeUUID.MatchString(s.RunnerSessionID) && mcpScopeUUID.MatchString(s.HumanSessionID) && s.LaunchGeneration > 0 && s.CredentialGeneration > 0 && len(s.RunnerInstallationID) > 0 && len(s.RunnerInstallationID) <= 128 && !strings.ContainsAny(s.RunnerInstallationID, "\r\n\x00") && err == nil && len(hash) == 32
}
func scopeJSON(scope *MCPSessionScope) ([]byte, error) {
	if scope == nil {
		return nil, nil
	}
	if !scope.Valid() {
		return nil, ErrMCPSessionScope
	}
	return json.Marshal(scope)
}
func validGrantScope(g MCPGrant) bool {
	return g.SessionScope == nil || (g.SessionScope.Valid() && g.Builtin == "" && g.ProofKind == "session_id" && g.SessionScope.AccountID == g.AccountID && g.SessionScope.RunnerSessionID == g.SessionID && g.SessionScope.HumanSessionID == g.ProofValue && g.SessionScope.GrantTokenHash == hex.EncodeToString(g.TokenHash))
}

// MCPScopedGrantLive rechecks the exact grant issuance and current runtime immediately
// before network dispatch. A grant belonging to a prior launch never authorizes a new one.
func MCPScopedGrantLive(ctx context.Context, pool *pgxpool.Pool, scope MCPSessionScope, connectionID string) (bool, error) {
	if pool == nil || !scope.Valid() || !mcpScopeUUID.MatchString(connectionID) {
		return false, ErrMCPSessionScope
	}
	encoded, err := scopeJSON(&scope)
	if err != nil {
		return false, err
	}
	hash, _ := hex.DecodeString(scope.GrantTokenHash)
	var live bool
	err = pool.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM session_mcp_grants g JOIN sessions s ON s.id=g.session_id
 WHERE g.token_hash=$1 AND g.connection_id=$2::uuid AND g.session_scope=$3::jsonb
 AND s.id=$4::uuid AND s.spawning_account_id=$5 AND s.mcp_launch_generation=$6
 AND s.status IN ('running','idle','waiting') AND s.ended_at IS NULL
 AND s.discard_requested_at IS NULL AND s.delegation_ended_at IS NULL
 AND COALESCE(s.paused_reason,'')=''
 )`, hash, connectionID, encoded, scope.RunnerSessionID, scope.AccountID, scope.LaunchGeneration).Scan(&live)
	return live, err
}
