package api_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

func TestAccountTimeProxyUsesAuthenticatedPerson(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.InternalKey = "clock-proxy-test-key"
	account := insertCredAccount(t, pool, "clock-person")
	svc := deps.Identity.(*identity.Service)
	token, err := svc.MintHumanAccessTokenForSession(t.Context(), account, "blerg-core", insertLiveHumanSession(t, pool, account))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/account-time" || r.Header.Get("X-Blerg-Account") != account || r.Header.Get("X-Internal-Key") != deps.InternalKey {
			t.Error("incorrect authenticated proxy request")
		}
		if r.Method == "PUT" {
			b, _ := io.ReadAll(r.Body)
			if string(b) != `{"timezone":"America/New_York"}` {
				t.Errorf("body=%s", b)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"timezone":"America/New_York"}`))
	}))
	defer runner.Close()
	deps.RunnerURL = runner.URL
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	for _, method := range []string{"GET", "PUT"} {
		req, _ := http.NewRequest(method, srv.URL+"/api/me/time?account=another", strings.NewReader(`{"timezone":"America/New_York"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatal(res.StatusCode)
		}
	}
	res, err := srv.Client().Get(srv.URL + "/api/me/time")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal(res.StatusCode)
	}
	if calls != 2 {
		t.Fatalf("anonymous reached runner: %d calls", calls)
	}
}

func TestAccountTimeUnavailableLogsCause(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	account := insertCredAccount(t, pool, "clock-diagnostic-person")
	svc := deps.Identity.(*identity.Service)
	token, err := svc.MintHumanAccessTokenForSession(t.Context(), account, "blerg-core", insertLiveHumanSession(t, pool, account))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { failurelog.Default = previous })
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	req, err := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/api/me/time", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 503 || !strings.Contains(output.String(), "account_time_proxy") || !strings.Contains(output.String(), "cause=") {
		t.Fatal("missing proxy diagnostic", res.StatusCode, output.String())
	}
	if strings.Contains(output.String(), token) {
		t.Fatal("credential leaked in clock failure log")
	}
}
