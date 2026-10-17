package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

func discoveryAgentToken(t *testing.T, api *API) string {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "fixture")
	return mintRunnerToken(t, private, "fixture", identity.Claims{Sub: "fixture-token", OnBehalfOf: "fixture-owner", Kind: "agent", Aud: coreAuthAudience, Caps: []string{coreAuthRunnerCap}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
}

func TestCredentialDiscoveryUsesAgentOwnerAndExactProof(t *testing.T) {
	var request internalCredentialListRequest
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid proof request")
		}
		_, _ = w.Write([]byte(`{"engines":["codex"]}`))
	}))
	defer core.Close()
	api := NewAPI(NewHub(), nil, "fixture-daemon", nil, "")
	api.SetCoreCredentials(core.URL, "fixture-key")
	token := discoveryAgentToken(t, api)
	rec, _ := callMyCredentials(t, api, token)
	if rec.Code != http.StatusOK || request.AccountID != "fixture-owner" || request.TokenID != "fixture-token" || request.SessionID != "" {
		t.Fatalf("lost agent owner/proof: status=%d request=%+v", rec.Code, request)
	}
}

func TestClusterCatalogAcceptsAccountBoundAgent(t *testing.T) {
	api := cacheTestAPI()
	token := discoveryAgentToken(t, api)
	api.clusterModels.entries = map[clusterModelCacheKey]*clusterModelCacheEntry{
		{"fixture-owner", "codex", "fixture-image"}:   {list: cacheFixture(time.Now(), "gpt-owner-model"), loaded: true},
		{"different-owner", "codex", "fixture-image"}: {list: cacheFixture(time.Now(), "gpt-private-other-model"), loaded: true},
	}
	rec, list := getModels(t, api, "codex?runtime=cluster", token)
	if rec.Code != http.StatusOK || len(list.Models) != 1 || list.Models[0].ID != "gpt-owner-model" {
		t.Fatalf("account-bound agent catalog: %d %+v", rec.Code, list)
	}
}
