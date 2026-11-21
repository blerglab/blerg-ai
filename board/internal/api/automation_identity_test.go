package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

type namedMinter struct{ *fakeMinter }

func (m namedMinter) Identity(_ context.Context, proof string) (string, error) {
	if proof == "" {
		m.t.Error("identity lookup has no proof")
	}
	return "example-user", nil
}

func TestAutomationIdentityShowsNameWithoutExposingCredential(t *testing.T) {
	do, board, human, minter := runAsMeEnv(t)
	srvAPI.Minter = namedMinter{minter}
	resp := do("POST", "/api/boards/"+board.ID+"/automation/run-as-me", human, nil)
	_ = readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run as me = %d", resp.StatusCode)
	}
	resp = do("GET", "/api/boards/"+board.ID+"/automation/identity", human, nil)
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != `{"username":"example-user"}` {
		t.Fatalf("identity = %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("identity may be cached")
	}
	resp = do("GET", "/api/boards/"+board.ID+"/automation/identity", "", nil)
	_ = readAll(t, resp)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous identity = %d", resp.StatusCode)
	}
}
