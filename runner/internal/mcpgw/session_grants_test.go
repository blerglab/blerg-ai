package mcpgw

import (
	"encoding/hex"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"testing"
)

func TestScopedGrantFreezesIssuanceAndRefusesInheritance(t *testing.T) {
	const account = "00000000-0000-4000-8000-000000000145"
	const session = "00000000-0000-4000-8000-000000000146"
	const human = "00000000-0000-4000-8000-000000000147"
	scope := db.MCPSessionScope{AccountID: account, RunnerSessionID: session, HumanSessionID: human, RunnerInstallationID: "synthetic-runner", LaunchGeneration: 1, CredentialGeneration: 2}
	spec := GrantSpec{ConnectionID: "00000000-0000-4000-8000-000000000148", Name: "synthetic", URL: "https://resource.example/mcp", SessionScope: &scope}
	proof := Proof{SessionID: human}
	rows, tokens, err := buildGrants(session, account, proof, []GrantSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].SessionScope == nil {
		t.Fatal("scoped grant discarded its binding")
	}
	if rows[0].SessionScope.GrantTokenHash != hex.EncodeToString(hashToken(tokens[spec.ConnectionID])) {
		t.Fatal("binding did not freeze this grant issuance")
	}
	if scope.GrantTokenHash != "" {
		t.Fatal("caller scope mutated")
	}
	for _, tc := range []struct {
		name, session, account string
		proof                  Proof
		scope                  db.MCPSessionScope
	}{
		{"worker", "00000000-0000-4000-8000-000000000149", account, proof, scope},
		{"foreign", session, "00000000-0000-4000-8000-000000000150", proof, scope},
		{"agent", session, account, Proof{TokenID: human}, scope},
		{"other-login", session, account, Proof{SessionID: "00000000-0000-4000-8000-000000000151"}, scope},
		{"inactive", session, account, proof, func() db.MCPSessionScope { s := scope; s.LaunchGeneration = 0; return s }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := spec
			bad.SessionScope = &tc.scope
			if _, _, err := buildGrants(tc.session, tc.account, tc.proof, []GrantSpec{bad}); err == nil {
				t.Fatal("invalid scoped grant accepted")
			}
		})
	}
}
