package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"github.com/gorilla/websocket"
)

// Exercise both the RPC reply and asynchronous EOF paths over a real owner socket.
type diagnosticSocketRuntime struct {
	*shellTestRuntime
	stage string
	async bool
}

func (s *diagnosticSocketRuntime) Failure(ownershell.SignedGrant) error {
	return ownershell.RefuseStart(ownershell.ReasonOpenRuntime, s.stage)
}
func (s *diagnosticSocketRuntime) Input(ctx context.Context, g ownershell.SignedGrant, seq uint64, data []byte) error {
	if s.async {
		return s.shellTestRuntime.Input(ctx, g, seq, data)
	}
	return s.Failure(g)
}

func TestLiveDiagnosticReachesOwnerSocket(t *testing.T) {
	for _, stage := range shellDiagnosticStages {
		for _, async := range []bool{false, true} {
			name := "reply"
			if async {
				name = "async"
			}
			t.Run(stage+"/"+name, func(t *testing.T) {
				pub, key, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				a := NewAPI(NewHub(), nil, "", nil, "")
				a.coreAuth = newTestCoreAuthClient(t, pub, "shell-key")
				token := mintTestToken(t, key, "shell-key", identity.Claims{Kind: "human", Sub: "owner", Aud: coreAuthAudience, Sid: "login", Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Hour).Unix()})
				rt := &diagnosticSocketRuntime{&shellTestRuntime{binding: shellTestBinding(), out: make(chan []byte, 8), input: make(chan []byte, 8)}, stage, async}
				m, err := ownershell.New(shellTestAuthority{}, rt, rt, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				a.shells = m
				m.SetPolicy(t.Context(), protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true})
				t.Cleanup(func() { m.Unavailable(context.Background()) })
				info, err := m.Open(t.Context(), ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"}, "session")
				if err != nil {
					t.Fatal(err)
				}
				mux := http.NewServeMux()
				a.RegisterShellRoutes(mux)
				srv := httptest.NewServer(mux)
				defer srv.Close()
				dial := websocket.Dialer{Subprotocols: []string{"bearer", token}}
				conn, res, err := dial.DialContext(t.Context(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/shells/"+info.Handle+"/socket", http.Header{"Origin": []string{srv.URL}})
				if res != nil {
					defer res.Body.Close()
				}
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if err = conn.WriteJSON(map[string]any{"type": "input", "seq": 1, "data": []byte("private-fixture")}); err != nil {
					t.Fatal(err)
				}
				if async {
					select {
					case <-rt.input:
					case <-time.After(3 * time.Second):
						t.Fatal("input did not reach attached shell")
					}
					close(rt.out)
				}
				if err = conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Fatal(err)
				}
				kind, data, err := conn.ReadMessage()
				if err != nil {
					t.Fatal("lost final diagnostic", err)
				}
				var frame map[string]string
				if kind != websocket.TextMessage || json.Unmarshal(data, &frame) != nil || frame["type"] != "error" || frame["cause"] != stage || len(frame) != 2 {
					t.Fatalf("unsafe or missing diagnostic: %s", data)
				}
			})
		}
	}
}

func TestLiveDiagnosticAuthenticatesDaemonAndGeneration(t *testing.T) {
	for _, stage := range append(append([]string{}, shellDiagnosticStages...), "Bearer private-fixture\nforged", "__proto__") {
		t.Run(stage, func(t *testing.T) {
			hub := NewHub()
			dc := &DaemonConn{ID: "desktop", send: make(chan []byte, 8)}
			hub.Register(dc)
			hub.SetSessionOwner("session", dc.ID)
			a := NewAPI(hub, nil, "", nil, "")
			remote := newRemoteShell(a)
			b := shellTestBinding()
			g := ownershell.SignedGrant{}
			g.Grant.ShellGeneration = "shell"
			remote.live["shell"] = b
			remote.peers["shell"] = dc
			entry, out := testShellOutput(dc, "session")
			hub.shellRPC.outputs = map[string]shellOutput{"shell": entry}
			frame := shellruntime.RPC{Version: 1, Type: "owner_shell_output", SessionID: "session", Shell: "shell", StartCause: stage}
			foreign := &DaemonConn{ID: dc.ID, send: make(chan []byte, 1)}
			hub.deliverShell(foreign, frame)
			if remote.Failure(g) != nil {
				t.Fatal("foreign connection supplied diagnostic")
			}
			frame.Shell = "other"
			hub.deliverShell(dc, frame)
			if remote.Failure(g) != nil {
				t.Fatal("foreign shell supplied diagnostic")
			}
			frame.Shell = "shell"
			hub.deliverShell(dc, frame)
			want := ownershell.SafeStartCause(stage)
			if ownershell.StartCause(remote.Failure(g)) != want {
				t.Fatal("cause lost or injected")
			}
			if want != "" {
				if _, ok := <-out; ok {
					t.Fatal("terminal cause did not close output")
				}
				hub.Register(&DaemonConn{ID: dc.ID, send: make(chan []byte, 1)})
				if remote.Failure(g) != nil {
					t.Fatal("stale daemon diagnostic survived replacement")
				}
				hub.abandonShellRequests(dc)
			}
		})
	}
}

var shellDiagnosticStages = []string{
	"helper_start",
	"workspace_admission",
	"bootstrap_read",
	"bootstrap_validate",
	"resolve_root",
	"resolve_identity",
	"verify_identity",
	"verify_cwd",
	"verify_root",
	"verify_grant",
	"open_root",
	"open_cwd",
	"verify_uid",
	"pty_open",
	"exec_shell",
	"pty_configure",
	"containment_setup",
	"lease_create",
	"helper_ready",
	"helper_stopped",
	"helper_input_pipe",
	"helper_output_pipe",
	"helper_record",
	"helper_write",
	"helper_wait",
	"helper_reply",
	"admit_shell",
	"verify_binding",
	"verify_reservation",
	"verify_policy",
	"cleanup_shell",
	"control_read",
	"control_write",
	"control_grant",
	"lease_check",
	"input_sequence",
	"resize_sequence",
	"control_operation",
	"control_apply",
}
