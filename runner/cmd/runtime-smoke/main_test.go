package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceipt(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"pass", `{"Action":"pass","Test":"TestAcceptanceHostSmoke"}`, "PASS"},
		{"skip", `{"Action":"skip","Test":"TestAcceptanceHostSmoke"}`, "SKIP"},
		{"missing", `{"Action":"pass","Package":"server"}`, "FAIL"},
		{"wrong test", `{"Action":"pass","Test":"Other"}`, "FAIL"},
		{"subtest", `{"Action":"pass","Test":"TestAcceptanceHostSmoke/child"}`, "FAIL"},
		{"duplicate", "{\"Action\":\"pass\",\"Test\":\"TestAcceptanceHostSmoke\"}\n{\"Action\":\"pass\",\"Test\":\"TestAcceptanceHostSmoke\"}", "FAIL"},
		{"failed", `{"Action":"fail","Test":"TestAcceptanceHostSmoke"}`, "FAIL"},
		{"invalid", `invalid`, "FAIL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := receipt(strings.NewReader(tc.input), "TestAcceptanceHostSmoke"); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestRuntimeSelection(t *testing.T) {
	for _, bad := range []string{"", "host,host", "local", "host,"} {
		if _, err := selectRuntimes(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	got, err := selectRuntimes("host,sandbox,cluster")
	if err != nil || len(got) != 3 {
		t.Fatalf("selection: %v %v", got, err)
	}
}

func TestClusterReceipt(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"status":"pass","cases":[{"id":"cluster-smoke","status":"pass"}]}`, "PASS"},
		{`{"status":"pass-rbac-not-enforced","cases":[{"id":"cluster-smoke","status":"pass-rbac-not-enforced"}]}`, "PASS-RBAC-NOT-ENFORCED"},
		{`{"status":"deployed-not-accepted"}`, "FAIL"},
		{`{"status":"pass","cases":[{"id":"other","status":"pass"}]}`, "FAIL"},
		{`{"status":"pass","cases":[{"id":"cluster-smoke","status":"skip"}]}`, "FAIL"},
		{`invalid`, "FAIL"},
	} {
		if got := clusterReceipt([]byte(tc.raw)); got != tc.want {
			t.Fatalf("%s: got %s want %s", tc.raw, got, tc.want)
		}
	}
}

func TestReviewGuards(t *testing.T) {
	if got := outcomeCode("PASS-RBAC-NOT-ENFORCED"); got != 3 {
		t.Fatalf("limited RBAC exit = %d", got)
	}
	if validClusterFlags([]string{"host"}, "ctx", "acceptance-test", "values") {
		t.Fatal("ignored cluster flags accepted")
	}
	if buildBudget+2*runtimeBudget+cleanupBudget >= 15*time.Minute {
		t.Fatal("local budgets exceed suite timeout")
	}
}

func TestStaleContainerSweep(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\nps) echo 'old fresh';;\ninspect) case \"$4\" in old) echo '2020-01-01T00:00:00Z';; *) echo '2026-01-01T00:00:00Z';; esac;;\nrm) echo \"$*\" >> \"$SWEEP_LOG\";;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "removed")
	t.Setenv("PATH", dir)
	t.Setenv("SWEEP_LOG", log)
	if err := sweepStaleContainers(time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil || string(raw) != "rm -f old\n" {
		t.Fatalf("sweep: %q %v", raw, err)
	}
}
