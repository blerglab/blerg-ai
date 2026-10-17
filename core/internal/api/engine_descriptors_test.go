package api

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/engines"
	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/signing"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCredentialKindsFollowRegisteredImports(t *testing.T) {
	t.Cleanup(engines.Register(engines.Descriptor{ID: "fixture", Name: "Fixture", CredentialImport: true}))
	for kind, want := range map[string]bool{"fixture": true, "codex": true, "github": true, "gitlab": true, "openclaw": false, "": false, "unknown": false} {
		if validCredentialKind(kind) != want {
			t.Errorf("kind %q support mismatch", kind)
		}
	}
}
func TestEngineDescriptorsAreHumanOnlyMetadata(t *testing.T) {
	t.Cleanup(engines.Register(engines.Descriptor{ID: "fixture", Name: "Fixture", CredentialImport: true, Plugins: true, CredentialEnvKeys: []string{"PRIVATE_WIRING"}}))
	kp, _ := signing.GenerateKeyPair()
	d := Deps{Identity: stubIdentity{keys: cid.KeySet{kp.Kid: kp.Pub}}, Audience: "blerg-core"}
	h := NewRouter(d)
	for _, tc := range []struct {
		kind   string
		caps   []string
		status int
	}{{"", nil, 401}, {"agent", []string{"card.read"}, 403}, {"human", nil, 403}, {"human", []string{"card.read"}, 200}} {
		req := httptest.NewRequest(http.MethodGet, "/api/engines", nil)
		if tc.kind != "" {
			token, _ := kp.Sign(cid.Claims{Sub: "fixture-account", Aud: "blerg-core", Kind: tc.kind, Caps: tc.caps, ExpiresAt: farFuture()})
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%s: status=%d want=%d body=%s", tc.kind, rec.Code, tc.status, rec.Body.String())
		}
		if rec.Code == 200 {
			var body struct {
				Engines       []engines.Descriptor `json:"engines"`
				PluginEngines []string             `json:"plugin_engines"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range body.Engines {
				if e.ID == "fixture" {
					found = true
				}
			}
			if !found {
				t.Fatal("registered descriptor missing")
			}
			for _, id := range body.PluginEngines {
				if id == "fixture" {
					t.Fatal("descriptor alone advertised unimplemented Core plugin validation")
				}
			}
			if strings.Contains(rec.Body.String(), "PRIVATE_WIRING") || strings.Contains(rec.Body.String(), "available") || strings.Contains(rec.Body.String(), "access_token") {
				t.Fatal("descriptor leaked private wiring or implied login availability")
			}
		}
	}
}
