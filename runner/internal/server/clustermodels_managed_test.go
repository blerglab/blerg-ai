package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClusterManagedCatalogUsesAccessOnlyAndFailsClosed(t *testing.T) {
	for _, scenario := range []string{"usable", "reconnect", "revoked", "expired", "near-expiry", "not-managed", "provider"} {
		t.Run(scenario, func(t *testing.T) {
			access := managedAccessFixture()
			if scenario == "expired" {
				access.ExpiresAt = time.Now().Add(-time.Hour)
			}
			if scenario == "near-expiry" {
				access.ExpiresAt = time.Now().Add(10 * time.Second)
			}
			calls := 0
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/internal/credentials/fetch":
					w.WriteHeader(http.StatusConflict)
				case "/internal/credentials/managed/access":
					calls++
					var req managedAccessRequest
					if json.NewDecoder(r.Body).Decode(&req) != nil || req.AccountID != "fixture-owner" || req.SessionID != "fixture-sid" || req.Force {
						t.Error("catalog changed verified principal or requested forced refresh")
					}
					switch scenario {
					case "reconnect":
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write([]byte(`{"code":"reconnect_required"}`))
					case "not-managed":
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write([]byte(`{"code":"not_managed"}`))
					case "revoked":
						w.WriteHeader(http.StatusNotFound)
					case "provider":
						w.WriteHeader(http.StatusServiceUnavailable)
					default:
						// An unexpected private field returned by Core must never reach the pod.
						raw, _ := json.Marshal(access)
						var payload map[string]any
						_ = json.Unmarshal(raw, &payload)
						payload["refresh_token"] = "fixture-refresh-private"
						_ = json.NewEncoder(w).Encode(payload)
					}
				default:
					t.Error("unexpected Core route")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer core.Close()
			f := &catalogFake{phase: "Succeeded", logs: clusterModelFixture}
			j := catalogManager(t, f)
			j.CoreURL = core.URL
			j.CoreInternalKey = "fixture-key"
			j.CredentialClient = core.Client()
			list, err := j.CodexModels(context.Background(), "fixture-owner", coreProof{SessionID: "fixture-sid"})
			if calls != 1 {
				t.Fatalf("managed access calls %d", calls)
			}
			if scenario != "usable" {
				if !errors.Is(err, ErrClusterCodexCatalogAuth) || f.jobs != 0 || f.secrets != 0 {
					t.Fatalf("unusable managed connection reached native probe: %v", err)
				}
				return
			}
			if err != nil || len(list.Models) == 0 {
				t.Fatalf("managed catalog unavailable: %v", err)
			}
			if !strings.Contains(f.credential, access.AccessToken) || !strings.Contains(f.credential, "chatgptAuthTokens") || strings.Contains(f.credential, "refresh_token") {
				t.Fatal("probe did not receive access-only snapshot")
			}
			raw, _ := json.Marshal(f.job)
			if strings.Contains(string(raw), access.AccessToken) || strings.Contains(string(raw), "fixture-refresh-private") {
				t.Fatal("provider authority exposed in Job")
			}
		})
	}
}

// Exercise the shipped probe script: managed access must enter external login,
// never auth.json, and a rejected login must never yield a catalog.
func TestClusterManagedCatalogNativeProbeCommand(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "external-login", true: "login-rejected"}[reject], func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "snapshot.json")
			raw, _ := json.Marshal(managedAccessFixture())
			if err := os.WriteFile(source, raw, 0600); err != nil {
				t.Fatal(err)
			}
			fake := `#!/usr/bin/python3
import json,os,pathlib,sys
home=pathlib.Path(os.environ["CODEX_HOME"])
if (home/"auth.json").exists(): sys.exit(70)
logged_in=False
for line in sys.stdin:
    request=json.loads(line)
    method=request.get("method")
    if method=="account/login/start":
        params=request.get("params",{})
        if params.get("type")!="chatgptAuthTokens" or params.get("accessToken")!="fixture-access-private" or params.get("chatgptAccountId")!="fixture-provider-account": sys.exit(71)
        if os.environ.get("FIXTURE_REJECT")=="true":
            print(json.dumps({"id":request["id"],"error":{"message":"fixture-private-error"}}),flush=True)
            continue
        logged_in=True
    if method=="model/list":
        if not logged_in: sys.exit(72)
        (home/"models_cache.json").write_text('{"identity":"fixture-private-identity","models":[{"slug":"fixture-managed-live"}]}')
    if "id" in request: print(json.dumps({"id":request["id"],"result":{}}),flush=True)
`
			if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			command := strings.ReplaceAll(clusterCodexCatalogCommand, "/var/run/codex-auth/auth.json", source)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
			rejected := "false"
			if reject {
				rejected = "true"
			}
			cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir, "FIXTURE_REJECT=" + rejected}
			out, err := cmd.Output()
			if reject {
				if err == nil || len(out) != 0 {
					t.Fatal("rejected managed login produced catalog")
				}
			} else if err != nil || !strings.Contains(string(out), "fixture-managed-live") {
				t.Fatalf("managed native probe unavailable: %v", err)
			}
			if strings.Contains(string(out), "fixture-access-private") || strings.Contains(string(out), "fixture-private") {
				t.Fatal("native probe disclosed private material")
			}
			matches, err := filepath.Glob(filepath.Join(dir, "*", "auth.json"))
			if err != nil || len(matches) != 0 {
				t.Fatal("managed probe persisted native auth.json")
			}
		})
	}
}
