package server

// NoOperatorFallback (spec 7.4): a cron session on the cluster never runs on the shared operator
// credential. The existing fallback quietly uses the operator Secret when the personal credential
// fetch returns nothing; this flag turns that into a typed credential error and no Job.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// cronJobSpec is the spec a cron's cluster start builds: no repo, the owner's account, the cron
// token as proof, no operator fallback.
func cronJobSpec(id string) SessionJobSpec {
	return SessionJobSpec{
		SessionID: id, NoRepo: true, Engine: "claude", InitialPrompt: "summarise", Title: "cron",
		SpawningAccountID: "acct-1", TokenID: "cron-token-1", NoOperatorFallback: true,
	}
}

// coreCreds is a stand-in for core's credential fetch with a chosen answer.
func coreCreds(t *testing.T, status int, plaintext string) (url string, calls func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"plaintext_base64": base64.StdEncoding.EncodeToString([]byte(plaintext))})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

func assertNoJob(t *testing.T, f *fakeK8s, err error) {
	t.Helper()
	if !errors.Is(err, ErrNoPersonalCredential) {
		t.Fatalf("err = %v, want ErrNoPersonalCredential", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) != 0 || len(f.createdSecrets) != 0 {
		t.Fatalf("a Job or Secret was created despite the credential error: %d jobs, %d secrets", len(f.created), len(f.createdSecrets))
	}
}

func TestNoOperatorFallbackCore404(t *testing.T) {
	f := &fakeK8s{} // no credential configured: core answers 404
	jm := newTestJobManager(t, f)
	assertNoJob(t, f, jm.CreateSessionJob(cronJobSpec("s-404")))
	if f.credentialFetchCalls == 0 {
		t.Error("the personal credential was never asked for")
	}
}

func TestNoOperatorFallbackCore500(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	url, calls := coreCreds(t, http.StatusInternalServerError, "")
	jm.CoreURL, jm.CredentialClient = url, http.DefaultClient
	assertNoJob(t, f, jm.CreateSessionJob(cronJobSpec("s-500")))
	if calls() != 1 {
		t.Errorf("core was called %d times, want 1", calls())
	}
}

func TestNoOperatorFallbackNetworkError(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	srv := httptest.NewServer(http.NotFoundHandler())
	jm.CoreURL, jm.CredentialClient = srv.URL, http.DefaultClient
	srv.Close() // connection refused from here on
	assertNoJob(t, f, jm.CreateSessionJob(cronJobSpec("s-net")))
}

func TestNoOperatorFallbackCoreNotConfigured(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	jm.CoreURL, jm.CoreInternalKey = "", ""
	assertNoJob(t, f, jm.CreateSessionJob(cronJobSpec("s-nocore")))
}

func TestNoOperatorFallbackEmptyCredentialAndNoAccount(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	url, _ := coreCreds(t, http.StatusOK, "   ") // whitespace is not a credential
	jm.CoreURL, jm.CredentialClient = url, http.DefaultClient
	assertNoJob(t, f, jm.CreateSessionJob(cronJobSpec("s-empty")))

	spec := cronJobSpec("s-noacct")
	spec.SpawningAccountID, spec.TokenID = "", ""
	assertNoJob(t, f, jm.CreateSessionJob(spec))
}

func TestNoOperatorFallbackPersonalCredentialStillWorks(t *testing.T) {
	f := &fakeK8s{credentialFetchResponses: map[string][]byte{"acct-1:claude": []byte("sk-ant-oat-personal")}}
	jm := newTestJobManager(t, f)
	if err := jm.CreateSessionJob(cronJobSpec("s-ok")); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 {
		t.Fatalf("jobs = %d, want 1", len(f.created))
	}
	raw, _ := json.Marshal(f.created[0])
	body := string(raw)
	if !strings.Contains(body, "blerg-runner-session-s-ok") || strings.Contains(body, "sk-ant-oat-personal") {
		t.Errorf("the credential must come from the per-session Secret and never appear in the Job: %s", body)
	}
	if got := f.credentialFetchTokenIDs; len(got) != 1 || got[0] != "cron-token-1" {
		t.Errorf("credential fetched with proofs %v, want the cron token", got)
	}
}

// Without the flag nothing changed: a miss still falls back to the operator Secret.
func TestOperatorFallbackUnchangedWithoutFlag(t *testing.T) {
	f := &fakeK8s{}
	jm := newTestJobManager(t, f)
	spec := cronJobSpec("s-plain")
	spec.NoOperatorFallback = false
	if err := jm.CreateSessionJob(spec); err != nil {
		t.Fatalf("a plain session must still fall back to the operator credential: %v", err)
	}
	if len(f.created) != 1 {
		t.Fatalf("jobs = %d, want 1", len(f.created))
	}
}
