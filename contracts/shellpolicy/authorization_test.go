package shellpolicy

import "testing"

func TestDenialRequiresExactActorAndFreshNonce(t *testing.T) {
	req := AuthorizationRequest{Owner: "owner", Login: "login", Nonce: "nonce"}
	valid := AuthorizationDenial{Version: 1, Outcome: "deny", Owner: req.Owner, Login: req.Login, Nonce: req.Nonce, Reason: "login_unavailable"}
	if !valid.Matches(req) {
		t.Fatal("valid denial refused")
	}
	for _, field := range []string{"version", "outcome", "owner", "login", "nonce", "reason", "empty-request"} {
		t.Run(field, func(t *testing.T) {
			d, r := valid, req
			switch field {
			case "version":
				d.Version++
			case "outcome":
				d.Outcome = "allow"
			case "owner":
				d.Owner = "other"
			case "login":
				d.Login = "other"
			case "nonce":
				d.Nonce = "stale"
			case "reason":
				d.Reason = "unknown"
			case "empty-request":
				d.Nonce = ""
				r.Nonce = ""
			}
			if d.Matches(r) {
				t.Fatal("uncorrelated denial matched")
			}
		})
	}
}

func TestLoginApprovalRequiresFreshExactCorrelation(t *testing.T) {
	req := AuthorizationRequest{Owner: "owner", Login: "login", Nonce: "fresh"}
	valid := AuthorizationAllowed{ShellPolicy: ShellPolicy{Version: 1, Revision: 1, Enabled: true}, AuthorizationVersion: 2, Outcome: "allow", Owner: req.Owner, Login: req.Login, Nonce: req.Nonce, LoginExpiresAt: 2000}
	if !valid.Matches(req, 1000) || valid.Legacy() {
		t.Fatal("typed approval not recognized")
	}
	for _, field := range []string{"owner", "login", "nonce", "version", "outcome", "expiry", "policy-version", "revision"} {
		t.Run(field, func(t *testing.T) {
			next := valid
			switch field {
			case "owner":
				next.Owner = "other"
			case "login":
				next.Login = "other"
			case "nonce":
				next.Nonce = "stale"
			case "version":
				next.AuthorizationVersion++
			case "outcome":
				next.Outcome = "deny"
			case "expiry":
				next.LoginExpiresAt = 1000
			case "policy-version":
				next.Version++
			case "revision":
				next.Revision = 0
			}
			if next.Matches(req, 1000) || next.Legacy() {
				t.Fatal("unverified approval accepted")
			}
		})
	}
	legacy := AuthorizationAllowed{ShellPolicy: valid.ShellPolicy}
	if !legacy.Legacy() || legacy.Matches(req, 1000) {
		t.Fatal("legacy policy silently promoted to typed approval")
	}
}
