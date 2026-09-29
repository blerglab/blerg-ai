package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

// testSID stands in for the `sid` of a browser session in tests that drive the resume path.
const testSID = "0d8f2b1e-5c3a-4c0e-9d7a-2f6b7a1c9e10"

// recordingCore answers every internal call 404 and records the request bodies.
func recordingCore(t *testing.T) (*httptest.Server, func() []map[string]string) {
	t.Helper()
	var got []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		got = append(got, m)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]string { return got }
}

// Exactly one named proof goes to core; none (or both) is refused locally, before any request.
func TestFetchCoreCredentialSendsExactlyOneProof(t *testing.T) {
	srv, bodies := recordingCore(t)
	ctx := context.Background()

	if _, _, err := fetchCoreCredential(ctx, nil, srv.URL, "k", "acct-1", "claude", coreProof{TokenID: "tok-1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fetchCoreCredential(ctx, nil, srv.URL, "k", "acct-1", "claude", coreProof{SessionID: testSID}); err != nil {
		t.Fatal(err)
	}
	got := bodies()
	if len(got) != 2 {
		t.Fatalf("core saw %d requests, want 2", len(got))
	}
	if got[0]["token_id"] != "tok-1" || got[0]["session_id"] != "" {
		t.Errorf("agent-token request = %v, want token_id only", got[0])
	}
	if got[1]["session_id"] != testSID || got[1]["token_id"] != "" {
		t.Errorf("browser request = %v, want session_id only", got[1])
	}

	for name, proof := range map[string]coreProof{
		"neither": {},
		"both":    {TokenID: "tok-1", SessionID: testSID},
	} {
		_, found, err := fetchCoreCredential(ctx, nil, srv.URL, "k", "acct-1", "claude", proof)
		if !errors.Is(err, errNoLivenessProof) || found {
			t.Errorf("%s: err = %v found = %v, want errNoLivenessProof", name, err, found)
		}
	}
	if n := len(bodies()); n != 2 {
		t.Errorf("a proof-less fetch reached core (%d requests, want 2)", n)
	}
	// Unconfigured stays the quiet "nothing to fetch", whatever the proof.
	if _, found, err := fetchCoreCredential(ctx, nil, "", "", "acct-1", "claude", coreProof{}); err != nil || found {
		t.Errorf("unconfigured = %v/%v, want quiet not-found", found, err)
	}
}

func TestFetchCorePluginsSendsExactlyOneProof(t *testing.T) {
	srv, bodies := recordingCore(t)
	ctx := context.Background()
	_, _ = fetchCorePlugins(ctx, nil, srv.URL, "k", "acct-1", coreProof{SessionID: testSID})
	_, _ = fetchCorePlugins(ctx, nil, srv.URL, "k", "acct-1", coreProof{TokenID: "tok-1"})
	got := bodies()
	if len(got) != 2 || got[0]["session_id"] != testSID || got[0]["token_id"] != "" ||
		got[1]["token_id"] != "tok-1" || got[1]["session_id"] != "" {
		t.Fatalf("plugin requests = %v", got)
	}
	if _, present := got[0]["human_session"]; present {
		t.Error("human_session is gone from the wire")
	}
	if _, err := fetchCorePlugins(ctx, nil, srv.URL, "k", "acct-1", coreProof{}); !errors.Is(err, errNoLivenessProof) {
		t.Errorf("proof-less plugin fetch err = %v, want errNoLivenessProof", err)
	}
	if len(bodies()) != 2 {
		t.Error("a proof-less plugin fetch reached core")
	}
}

func TestProofFromPrincipal(t *testing.T) {
	human := identity.Principal{Claims: identity.Claims{Kind: "human", Sub: "acct-1", Sid: testSID}}
	if p := proofFromPrincipal(human); p != (coreProof{SessionID: testSID}) {
		t.Errorf("human = %+v", p)
	}
	old := identity.Principal{Claims: identity.Claims{Kind: "human", Sub: "acct-1"}}
	if proofFromPrincipal(old).valid() {
		t.Error("a token without sid must not yield a valid proof")
	}
	agent := identity.Principal{Claims: identity.Claims{Kind: "agent", Sub: "tok-1", OnBehalfOf: "acct-1", Sid: testSID}}
	if p := proofFromPrincipal(agent); p != (coreProof{TokenID: "tok-1"}) {
		t.Errorf("agent = %+v (a token id, never also a session)", p)
	}
}

// A cluster session started from a browser carries THAT browser session's id to core, and the
// launching user's own credential comes back into the pod: the end-to-end path.
func TestBrowserLaunchedClusterSessionFetchesWithSessionID(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{credentialFetchResponses: map[string][]byte{"user-1:claude": []byte("sk-ant-api03-user-one")}}
	jm := newTestJobManager(t, f)
	hub.SetJobManager(jm)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")

	rec := postSpawn(t, api, map[string]any{"no_repo": true, "runtime": "cluster", "kind": "agent", "initial_prompt": "hi"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	f.mu.Lock()
	sessionIDs, tokenIDs := append([]string(nil), f.credentialFetchSessionIDs...), append([]string(nil), f.credentialFetchTokenIDs...)
	secrets := append([]map[string]any(nil), f.createdSecrets...)
	f.mu.Unlock()
	if len(sessionIDs) == 0 {
		t.Fatal("core was never asked for the credential")
	}
	for i, sid := range sessionIDs {
		if sid != testSID || tokenIDs[i] != "" {
			t.Errorf("fetch %d carried session_id %q token_id %q, want the launching session only", i, sid, tokenIDs[i])
		}
	}
	raw, _ := json.Marshal(secrets)
	if !strings.Contains(string(raw), "sk-ant-api03-user-one") {
		t.Errorf("the launching user's credential did not reach the pod: %s", raw)
	}
}

// A token minted before core stamped `sid` fails closed with a clear message: no row, no Job,
// nothing asked of core, and never the operator secret.
func TestClusterStartWithoutSidFailsClosed(t *testing.T) {
	hub := NewHub()
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	hub.SetJobManager(jm)
	api := NewAPI(hub, nil, "daemon-tok-1234567890", nil, "")
	tok := enableBrowserAuthSid(t, api, "")("session.start")

	for name, body := range map[string]string{
		"no repo": `{"no_repo": true, "runtime": "cluster", "kind": "agent"}`,
		"repo":    `{"repo": "org/proj", "runtime": "cluster", "kind": "agent"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		api.HandlePostSessions(rec, req)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Reload the page") {
			t.Errorf("%s: %d %s, want 401 with the reload message", name, rec.Code, rec.Body.String())
		}
	}
	if len(f.created) != 0 || len(f.createdSecrets) != 0 || f.credentialFetchCalls != 0 || len(f.pluginRequests) != 0 {
		t.Errorf("a sid-less start did work: jobs=%d secrets=%d core fetches=%d plugin calls=%d",
			len(f.created), len(f.createdSecrets), f.credentialFetchCalls, len(f.pluginRequests))
	}
}

// The job manager itself refuses a spec that names no proof (resume, legacy path) when core is
// wired, instead of starting on the operator secret.
func TestCreateSessionJobWithoutProofFailsClosed(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s", Repo: "r", SpawningAccountID: "acct-1"})
	if !errors.Is(err, errNoLivenessProof) {
		t.Fatalf("err = %v, want errNoLivenessProof", err)
	}
	if len(f.created) != 0 || f.credentialFetchCalls != 0 {
		t.Errorf("work was done before refusing: jobs=%d fetches=%d", len(f.created), f.credentialFetchCalls)
	}
	// Without core wired there is nothing to prove.
	jm.CoreURL = ""
	if err := jm.CreateSessionJob(SessionJobSpec{SessionID: "s2", Repo: "r", SpawningAccountID: "acct-1"}); err != nil {
		t.Errorf("unwired core: err = %v, want nil", err)
	}
}

// GET /api/me/credentials with a pre-sid token answers "couldn't tell", never asks core.
func TestMyCredentialsWithoutSidIsUnavailable(t *testing.T) {
	asked := false
	api, _, coreSrv := newCredentialsAPI(t, func(http.ResponseWriter, *http.Request) { asked = true })
	defer coreSrv.Close()
	tok := enableBrowserAuthSid(t, api, "")("session.start")
	w, out := callMyCredentials(t, api, tok)
	if w.Code != http.StatusOK || !out.Unavailable || asked {
		t.Errorf("code=%d unavailable=%v asked=%v, want 200/true/false", w.Code, out.Unavailable, asked)
	}
}

// The credential listing names the caller's own session too.
func TestMyCredentialsListSendsSessionID(t *testing.T) {
	var got map[string]string
	api, token, coreSrv := newCredentialsAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string][]string{"engines": {"claude"}})
	})
	defer coreSrv.Close()
	w, out := callMyCredentials(t, api, token)
	if w.Code != http.StatusOK || out.Unavailable {
		t.Fatalf("code=%d out=%+v", w.Code, out)
	}
	if got["session_id"] != testSID || got["token_id"] != "" || got["account_id"] != "acct-1" {
		t.Errorf("list request = %v, want account acct-1 with session_id only", got)
	}
}
