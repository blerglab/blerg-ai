package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestPodConsumesMarketplacePolicyDuringStartupAndLive(t *testing.T) {
	for _, mode := range []string{"live", "replacement", "replacement-core-unavailable"} {
		t.Run(mode, func(t *testing.T) { testPodMarketplacePolicy(t, mode) })
	}
}

func testPodMarketplacePolicy(t *testing.T, mode string) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "fixture")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("CODEX_AUTH_JSON", "")
	t.Setenv("HERMES_ENV_CONTENTS", "")
	t.Setenv("BLERG_RUNNER_PLUGIN_MARKETPLACES", "*")
	bin := t.TempDir()
	calls := filepath.Join(home, "plugin-calls")
	script := "#!/bin/sh\necho called >> \"" + calls + "\"\ncase \"$*\" in \"plugin marketplace list --json\") echo '" + listJSON + "' ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	preparing := make(chan struct{})
	release := make(chan struct{})
	// Hold workspace preparation while policy frames arrive over the real socket.
	ctx, cancel := context.WithCancel(context.Background())
	connections := make(chan *websocket.Conn, 1)
	pongs := make(chan struct{}, 1)
	messages := make(chan json.RawMessage, 64)
	upgrader := websocket.Upgrader{}
	var unavailable atomic.Bool
	unavailable.Store(mode == "replacement-core-unavailable")
	bootstrap := pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{})
	bootstrap.Revision = 3
	if mode == "live" {
		allow, _ := pluginspec.ParseAllowlist("*")
		bootstrap = pluginspec.PolicyFromAllowlist(allow)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session/plugin-marketplace-policy" {
			if r.Header.Get("Authorization") != "Bearer pod-fixture" || r.URL.Query().Get("session_id") != "workspace-pod" {
				t.Error("bootstrap did not authenticate and bind the pod")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if unavailable.Load() {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", SessionID: "workspace-pod", Policy: &bootstrap, LeaseSeconds: 15})
			return
		}
		if r.URL.Path == "/api/agent-config" {
			close(preparing)
			select {
			case <-release:
			case <-ctx.Done():
			}
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/ws/daemon" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetPongHandler(func(string) error { pongs <- struct{}{}; return nil })
		connections <- conn
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			select {
			case messages <- raw:
			case <-ctx.Done():
				return
			}
		}
	}))
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{SessionID: "workspace-pod", DaemonToken: "pod-fixture", NoRepo: true, Home: home, APIKey: "fixture", Plugins: []pluginspec.Entry{ent("selected")}, ServerWS: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/daemon", ServerHTTP: srv.URL})
	}()
	var conn *websocket.Conn
	t.Cleanup(func() {
		cancel()
		if conn != nil {
			_ = conn.Close()
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("pod Run: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("pod Run did not stop")
		}
		srv.Close()
	})
	select {
	case conn = <-connections:
	case <-time.After(15 * time.Second):
		t.Fatal("pod did not connect")
	}
	next := func(kind string) json.RawMessage {
		t.Helper()
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		for {
			select {
			case raw := <-messages:
				var envelope struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(raw, &envelope) == nil && envelope.Type == kind {
					return raw
				}
			case <-timer.C:
				t.Fatalf("missing %s", kind)
				return nil
			}
		}
	}
	var hello struct {
		PluginPolicy bool `json:"plugin_policy"`
	}
	if err := json.Unmarshal(next("daemon_hello"), &hello); err != nil {
		t.Fatal(err)
	}
	if !hello.PluginPolicy {
		t.Fatal("pod did not advertise policy support")
	}
	if err := conn.WriteJSON(map[string]string{"type": "daemon_welcome"}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-preparing:
	case <-time.After(15 * time.Second):
		t.Fatal("workspace preparation never started")
	}
	send := func(p *pluginspec.Policy, session string) {
		t.Helper()
		if err := conn.WriteJSON(protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", SessionID: session, Policy: p, LeaseSeconds: 15}); err != nil {
			t.Fatal(err)
		}
		// The default ping handler runs on the client's same read pump, so its
		// pong proves the preceding policy frame was handled without a timer race.
		if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(15*time.Second)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-pongs:
		case <-time.After(15 * time.Second):
			t.Fatal("policy read pump did not acknowledge ping")
		}
	}
	disabled := pluginspec.PolicyFromAllowlist(pluginspec.Allowlist{})
	disabled.Revision = 3
	if mode == "live" {
		send(&disabled, "workspace-pod")
	} else {
		// A replacement has a fresh HOME, just like workspace-home emptyDir in
		// the generated Job. A stale runner replica still has enabling revision 1.
		oldAllow, _ := pluginspec.ParseAllowlist("*")
		old := pluginspec.PolicyFromAllowlist(oldAllow)
		send(&old, "workspace-pod")
	}
	close(release)
	next("session_started")
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("startup installer ignored delivered revoke", err)
	}
	if mode == "replacement-core-unavailable" {
		// Neither launch environment nor stale socket deliveries establish authority.
		if _, err := os.Stat(filepath.Join(home, ".blerg", "plugins", "claude", "marketplace-policy.json")); !os.IsNotExist(err) {
			t.Fatal("unbootstrapped pod accepted socket policy", err)
		}
		unavailable.Store(false)
		deadline := time.Now().Add(15 * time.Second)
		for {
			raw, _ := os.ReadFile(filepath.Join(home, ".blerg", "plugins", "claude", "marketplace-policy.json"))
			var p pluginspec.Policy
			if json.Unmarshal(raw, &p) == nil && p.Revision == 3 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("pod did not retry policy bootstrap after Core recovery")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	enabledAllow, _ := pluginspec.ParseAllowlist("*")
	enabled := pluginspec.PolicyFromAllowlist(enabledAllow)
	check := func(revision int64, allowed bool) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(home, ".blerg", "plugins", "claude", "marketplace-policy.json"))
		if err != nil {
			t.Fatal("pod did not persist policy update", err)
		}
		var p pluginspec.Policy
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if p.Revision != revision || p.Allowlist().Allows(official) != allowed {
			t.Fatalf("wrong pod policy: %+v", p)
		}
	}
	check(3, false)
	for _, revision := range []int64{1, 3, 2} {
		enabled.Revision = revision
		send(&enabled, "workspace-pod")
		check(3, false)
	}
	enabled.Revision = 4
	send(&enabled, "another-session")
	check(3, false)
	send(&enabled, "")
	check(3, false)
	send(nil, "workspace-pod")
	check(3, false)
	// Persistence precedes opening the bootstrap gate. Wait for observable socket
	// acceptance rather than assuming the disk rename means bootstrap has finished.
	deadline := time.Now().Add(15 * time.Second)
	for {
		send(&enabled, "workspace-pod")
		raw, _ := os.ReadFile(filepath.Join(home, ".blerg", "plugins", "claude", "marketplace-policy.json"))
		var p pluginspec.Policy
		if json.Unmarshal(raw, &p) == nil && p.Revision == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pod did not accept newer policy after bootstrap")
		}
		time.Sleep(10 * time.Millisecond)
	}
	check(4, true)
}

func TestPodPluginPolicyBootstrapRejectsUntrustedResponses(t *testing.T) {
	for _, mode := range []string{"valid", "no-token", "status", "redirect", "oversize", "foreign-session", "nil", "version", "revision", "missing-default", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("BLERG_RUNNER_PLUGIN_MARKETPLACES", "*")
			allow, _ := pluginspec.ParseAllowlist("*")
			p := pluginspec.PolicyFromAllowlist(allow)
			var redirected atomic.Bool
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "no-token" {
					t.Error("unauthenticated bootstrap attempted")
					return
				}
				if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("missing runtime credential")
				}
				msg := protocol.PluginMarketplacePolicy{Type: "plugin_marketplace_policy", SessionID: "pod", Policy: &p, LeaseSeconds: 15}
				switch mode {
				case "status":
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat(" ", 33<<10)))
					return
				case "foreign-session":
					msg.SessionID = "other"
				case "nil":
					msg.Policy = nil
				case "version":
					p.Version = 99
				case "revision":
					p.Revision = 0
				case "missing-default":
					p.Marketplaces = p.Marketplaces[1:]
				}
				_ = json.NewEncoder(w).Encode(msg)
				if mode == "trailing" {
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer srv.Close()
			workshop := daemon.NewPodPluginWorkshop(t.TempDir())
			cfg := Config{SessionID: "pod", ServerHTTP: srv.URL, DaemonToken: "fixture"}
			if mode == "no-token" {
				cfg.DaemonToken = ""
			}
			stop := startPodPluginPolicy(t.Context(), cfg, workshop.BootstrapPolicy)
			stop()
			if redirected.Load() {
				t.Fatal("bootstrap followed redirect with runtime credential")
			}
			if workshop.CurrentAllow().Allows(official) != (mode == "valid") {
				t.Fatal("invalid authority opened plugin gate", mode)
			}
		})
	}
}
