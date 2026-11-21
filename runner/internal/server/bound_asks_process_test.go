package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/gorilla/websocket"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
)

// The matrix crosses a separate runner process, real HTTP authentication, and
// PostgreSQL. No mocked answer router, store, daemon, or subject executor.
func TestBoundPlanAskProcess(t *testing.T)  { runBoundAskMatrix(t, "plan") }
func TestBoundDraftAskProcess(t *testing.T) { runBoundAskMatrix(t, "draft") }
func TestBoundCutAskProcess(t *testing.T)   { runBoundAskMatrix(t, "cut") }
func runBoundAskMatrix(t *testing.T, kind string) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	cases := []string{"approve", "remove", "move", "decline", "ambiguous", "ambiguous_canary", "wrong_person", "forged_agent", "http_agent", "http_onbehalf", "empty_answer", "empty_stale", "two_asks", "withdraw", "withdraw_forbidden", "live_question", "session_close", "legacy_frame", "socket_approve", "socket_ambiguous", "socket_revoked", "socket_agent", "socket_onbehalf", "expire_cli", "expire_waiter", "daemon_empty", "expire_deadline", "concurrent", "replay", "changed_proposal", "stale_revision", "expired", "session_end", "discard", "archive", "dismiss", "owner_transfer", "child", "ownerless", "browser_away", "input_failure_replay", "untargeted", "rest_untargeted", "server_restart", "stale_notice_restart", "older_loop", "newest_ask", "cli"}
	if kind == "draft" {
		cases = append(cases, "wrong_revision", "wrong_draft", "existing_blackboard", "answer_blackboard_exists", "tx_draft_id", "tx_draft_revision", "tx_draft_digest")
	}
	var cutBinary string
	if kind == "cut" {
		cutBinary = buildCutBoardBinary(t)
	}
	for _, scenario := range cases {
		if kind != "plan" && (scenario == "remove" || scenario == "move") {
			continue
		}
		t.Run(scenario, func(t *testing.T) {
			parallelBoundProcess(t)
			env := []string{"BLERG_BOUND_SUBJECT=" + kind}
			if kind == "cut" {
				board := startCutBoardProcess(t, cutBinary, scenario)
				path := filepath.Join(t.TempDir(), "board.json")
				raw, _ := json.Marshal(board)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				env = append(env, "BLERG_BOUND_CUT_BOARD_CONFIG="+path)
			}
			if kind == "draft" && strings.HasPrefix(scenario, "tx_draft_") {
				env = append(env, "BLERG_BOUND_DISABLE_CLOSE_TRIGGER=1")
			}
			if scenario == "remove" || scenario == "move" {
				env = append(env, "BLERG_BOUND_OPERATION="+scenario)
			}
			if scenario == "server_restart" || scenario == "stale_notice_restart" {
				pool := runnerContractPool(t)
				u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
				if err != nil {
					t.Fatal(err)
				}
				u.Path = "/" + pool.Config().ConnConfig.Database
				env = append(env, "BLERG_BOUND_DATABASE="+u.String())
			}
			env = boundProcessDatabase(t, env)
			cfg, stop := startBoundProcess(t, env)
			call := func(path, token string, body any) (int, map[string]any) {
				t.Helper()
				raw, _ := json.Marshal(body)
				req, _ := http.NewRequest("POST", cfg["url"]+path, bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+token)
				resp, e := http.DefaultClient.Do(req)
				if e != nil {
					t.Fatal(e)
				}
				defer resp.Body.Close()
				out := map[string]any{}
				_ = json.NewDecoder(resp.Body).Decode(&out)
				return resp.StatusCode, out
			}
			mutate := func(action string) {
				t.Helper()
				code, out := call("/test/mutate", "", map[string]string{"action": action})
				if code != 200 {
					t.Fatalf("fixture %s: %d %+v", action, code, out)
				}
			}
			if scenario == "ownerless" || scenario == "child" {
				mutate(scenario)
			}
			if scenario == "ambiguous_canary" {
				mutate("canary_proposal")
			}
			subject := map[string]any{"kind": "plan", "blackboard_id": cfg["blackboard"], "proposal_id": cfg["proposal"], "revision": 0}
			if kind == "cut" {
				subject = map[string]any{"kind": "cut", "blackboard_id": cfg["blackboard"], "milestone_id": "cut-milestone", "revision": 0, "cut": map[string]any{"board_id": cfg["cut_board"], "release_id": cfg["cut_release"], "next_release_id": cfg["cut_next"], "link": true}}
			}
			if kind == "draft" {
				subject = map[string]any{"kind": "draft", "draft_id": cfg["draft"], "revision": 0}
				if scenario == "ambiguous_canary" {
					subject["revision"] = 1
				}
			}
			if scenario == "wrong_revision" {
				subject["revision"] = 99
			}
			if scenario == "wrong_draft" {
				subject["draft_id"] = newUUID()
			}
			if scenario == "existing_blackboard" {
				mutate("existing_blackboard")
			}
			create := func() (int, map[string]any) {
				return call("/api/messages", cfg["session_token"], map[string]any{"kind": "ask", "body": "untrusted agent prose", "subject": subject})
			}
			var live *websocket.Conn
			if scenario == "live_question" {
				dialer := websocket.Dialer{Subprotocols: []string{wsBearerSubprotocol, cfg["human_token"]}}
				var err error
				live, _, err = dialer.Dial("ws"+strings.TrimPrefix(cfg["url"], "http")+"/ws/browser", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer live.Close()
				_ = live.SetReadDeadline(time.Now().Add(10 * time.Second))
				var frame map[string]any
				if err = live.ReadJSON(&frame); err != nil {
					t.Fatal(err)
				}
				if err = live.WriteJSON(map[string]any{"type": "subscribe_agent_events", "session_id": cfg["session"], "after_seq": 0}); err != nil {
					t.Fatal(err)
				}
				for {
					if err = live.ReadJSON(&frame); err != nil {
						t.Fatal(err)
					}
					if frame["type"] == "agent_events_replay_done" {
						break
					}
				}
			}
			code, ask := create()
			if scenario == "ownerless" || scenario == "child" || scenario == "wrong_revision" || scenario == "wrong_draft" || scenario == "existing_blackboard" {
				if code == 201 {
					t.Fatal("ineligible creator accepted")
				}
				resp, err := http.Get(cfg["url"] + "/test/state")
				if err != nil {
					t.Fatal(err)
				}
				var state map[string]any
				_ = json.NewDecoder(resp.Body).Decode(&state)
				_ = resp.Body.Close()
				if state["BindingStatus"] != "" {
					t.Fatal("refused creation left a binding")
				}
				return
			}
			if code != 201 {
				t.Fatalf("create: %d %+v", code, ask)
			}
			id := ask["id"].(string)
			if live != nil {
				var frame map[string]any
				for {
					if err := live.ReadJSON(&frame); err != nil {
						t.Fatal(err)
					}
					if frame["type"] == "agent_event" && frame["kind"] == "ask" {
						break
					}
				}
				payload, _ := frame["payload"].(map[string]any)
				if payload["bound"] != true || strings.Contains(fmt.Sprint(payload["body"]), "untrusted agent prose") {
					t.Fatalf("live question did not carry the server snapshot: %+v", payload)
				}
			}

			if scenario == "expire_cli" || scenario == "expire_waiter" || scenario == "daemon_empty" || scenario == "expire_deadline" {
				if scenario == "expire_deadline" {
					mutate("expired")
				}
				path := "/api/messages/" + id + "/expire"
				body := map[string]any{"waiter_ended": scenario == "expire_waiter"}
				credential := cfg["session_token"]
				if scenario == "expire_waiter" {
					credential = topicDaemonToken
				}
				if scenario == "daemon_empty" {
					path = "/api/messages/" + id + "/reply"
					body = map[string]any{"answer": ""}
					credential = topicDaemonToken
				}
				want := 403
				if scenario == "expire_deadline" {
					want = 200
				}
				for range 2 {
					if code, out := call(path, credential, body); code != want {
						t.Fatalf("waiter close: %d %+v; want %d", code, out, want)
					}
				}
			}
			if scenario == "remove" || scenario == "move" {
				response, err := http.Get(cfg["url"] + "/test/state")
				if err != nil {
					t.Fatal(err)
				}
				var snapshot struct{ Question string }
				_ = json.NewDecoder(response.Body).Decode(&snapshot)
				_ = response.Body.Close()
				// BLG-533: the person-facing question names the item; its raw id ("existing") stays in the bound subject only.
				if !strings.Contains(snapshot.Question, "Existing scope item") {
					t.Fatal("question does not name the existing item")
				}
				if strings.Contains(snapshot.Question, "Item: existing") {
					t.Fatal("question still shows the item's raw id")
				}
			}
			if scenario == "server_restart" {
				stop()
				env = append(env, "BLERG_BOUND_SESSION="+cfg["session"], "BLERG_BOUND_BLACKBOARD="+cfg["blackboard"], "BLERG_BOUND_PROPOSAL="+cfg["proposal"])
				cfg, _ = startBoundProcess(t, env)
			}
			if scenario == "concurrent" {
				results := make(chan error, 2)
				for range 2 {
					go func() {
						req, _ := http.NewRequest("POST", cfg["url"]+"/api/messages/"+id+"/reply", strings.NewReader(`{"answer":"yes"}`))
						req.Header.Set("Authorization", "Bearer "+cfg["human_token"])
						resp, err := http.DefaultClient.Do(req)
						if err == nil {
							_ = resp.Body.Close()
							if resp.StatusCode != 200 {
								err = fmt.Errorf("concurrent reply: HTTP %d", resp.StatusCode)
							}
						}
						results <- err
					}()
				}
				for range 2 {
					if err := <-results; err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario == "stale_notice_restart" {
				mutate("stale_revision")
				mutate("inject_notice_failure")
			}
			answer, token := "yes", cfg["human_token"]
			wantRevision, wantItems := 0, 0
			switch scenario {
			case "decline":
				answer = "no"
			case "ambiguous", "ambiguous_canary", "socket_ambiguous":
				answer = "yes but exclude one"
			case "wrong_person":
				token = cfg["other_human_token"]
			case "forged_agent":
				token = cfg["session_token"]
			case "http_agent":
				token = cfg["agent_browser_token"]
			case "http_onbehalf":
				token = cfg["onbehalf_token"]
			case "empty_answer", "empty_stale":
				answer = ""
				if scenario == "empty_stale" {
					mutate("changed_proposal")
				}
			case "answer_blackboard_exists":
				mutate("existing_blackboard")
				wantRevision, wantItems = 1, 1
			case "tx_draft_id", "tx_draft_revision", "tx_draft_digest":
				mutate(scenario)
			case "withdraw_forbidden":
				for _, credential := range []string{cfg["agent_browser_token"], cfg["other_human_token"]} {
					if code, out := call("/api/messages/"+id+"/expire", credential, map[string]any{"withdraw": true}); code == 200 {
						t.Fatalf("non-Main withdrew ask: %+v", out)
					}
				}
				if code, out := call("/api/messages/"+id+"/expire", cfg["session_token"], map[string]any{"withdraw": true, "waiter_ended": true}); code != 403 {
					t.Fatalf("mixed waiter withdrawal: %d %+v", code, out)
				}
			case "withdraw":
				if code, out := call("/api/messages/"+id+"/expire", cfg["session_token"], map[string]any{"withdraw": true}); code != 200 {
					t.Fatalf("withdraw: %d %+v", code, out)
				}
			case "two_asks":
				code, out := create()
				if kind != "plan" {
					if code == 201 {
						t.Fatal("overlapping draft ask accepted")
					}
				} else {
					if code != 201 {
						t.Fatalf("replace: %d %+v", code, out)
					}
					old := id
					id = fmt.Sprint(out["id"])
					if code, out := call("/api/messages/"+old+"/reply", cfg["human_token"], map[string]any{"answer": "yes"}); code == 200 {
						t.Fatalf("superseded ask committed: %+v", out)
					}
				}
			case "changed_proposal", "stale_revision", "expired", "session_end", "discard", "archive", "dismiss", "session_close", "legacy_frame", "owner_transfer", "older_loop", "newest_ask":
				mutate(scenario)
				if kind == "draft" && scenario == "changed_proposal" {
					response, err := http.Get(cfg["url"] + "/test/state")
					if err != nil {
						t.Fatal(err)
					}
					var after map[string]any
					_ = json.NewDecoder(response.Body).Decode(&after)
					_ = response.Body.Close()
					if after["BindingStatus"] != "canceled" {
						t.Fatal("replacing a draft left its old question open")
					}
				}
			}
			if kind == "plan" && (scenario == "stale_revision" || scenario == "stale_notice_restart") {
				wantRevision = 1
			}
			if scenario == "cli" {
				// Cancel the first ask, then exercise the shipped Python CLI through HTTP.
				mutate("dismiss")
				command := exec.Command("python3", "../../.claude/skills/session-messaging/blerg-runner", "ask", "--person", "--no-wait", "--blackboard", cfg["blackboard"], "--plan-proposal", cfg["proposal"], "--revision", "0", "Commit this proposal?")
				if kind == "draft" {
					command.Args = []string{"python3", "../../.claude/skills/session-messaging/blerg-runner", "ask", "--person", "--no-wait", "--draft", cfg["draft"], "--revision", "0", "Create this draft?"}
				}
				if kind == "cut" {
					command.Args = []string{"python3", "../../.claude/skills/session-messaging/blerg-runner", "ask", "--person", "--no-wait", "--blackboard", cfg["blackboard"], "--cut", cfg["cut_release"], "--cut-board", cfg["cut_board"], "--next-release", cfg["cut_next"], "--milestone", "cut-milestone", "--revision", "0", "--link-release", "Cut this release?"}
				}
				command.Env = append(os.Environ(), "BLERG_RUNNER_SERVER_HTTP="+cfg["url"], "BLERG_RUNNER_SESSION_ID="+cfg["session"], "BLERG_RUNNER_SESSION_TOKEN="+cfg["session_token"], "BLERG_RUNNER_DAEMON_STATE_DIR="+t.TempDir())
				output, e := command.CombinedOutput()
				if e != nil {
					t.Fatalf("CLI: %v %s", e, output)
				}
				fields := strings.Fields(string(output))
				if len(fields) < 2 {
					t.Fatal("CLI omitted ask id")
				}
				id = fields[1]
			}
			switch {
			case strings.HasPrefix(scenario, "socket_"):
				credential := token
				if scenario == "socket_agent" {
					credential = cfg["agent_browser_token"]
				}
				if scenario == "socket_onbehalf" {
					credential = cfg["onbehalf_token"]
				}
				dialer := websocket.Dialer{Subprotocols: []string{wsBearerSubprotocol, credential}}
				conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(cfg["url"], "http")+"/ws/browser", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
				var frame map[string]any
				if err = conn.ReadJSON(&frame); err != nil {
					t.Fatal(err)
				}
				if scenario == "socket_revoked" {
					mutate("revoke_login")
				}
				if err = conn.WriteJSON(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: cfg["session"], Text: answer, AnswerMessageID: id, ClientID: "socket-reply"}); err != nil {
					t.Fatal(err)
				}
				if err = conn.WriteJSON(map[string]any{"type": "subscribe_agent_events", "session_id": cfg["session"], "after_seq": 0}); err != nil {
					t.Fatal(err)
				}
				socketProblem := ""
				for {
					if err = conn.ReadJSON(&frame); err != nil {
						t.Fatal(err)
					}
					if frame["kind"] == "error" {
						if payload, ok := frame["payload"].(map[string]any); ok {
							socketProblem = fmt.Sprint(payload["message"])
						}
					}
					if frame["type"] == "agent_events_replay_done" {
						break
					}
				}
				if scenario != "socket_approve" && scenario != "socket_ambiguous" && (socketProblem == "" || strings.Contains(socketProblem, "revision")) {
					t.Fatalf("wrong socket explanation: %q", socketProblem)
				}
				if scenario == "socket_approve" {
					wantRevision, wantItems = 1, 1
				}
			case scenario == "rest_untargeted":
				code, out := call("/api/v1/sessions/"+cfg["session"]+"/message", token, map[string]string{"text": answer, "source": "human", "client_id": "rest-untargeted"})
				if code != 202 {
					t.Fatalf("REST conversation: %d %+v", code, out)
				}
				mutate("replay")
			case scenario == "browser_away" || scenario == "input_failure_replay" || scenario == "untargeted":
				if scenario == "input_failure_replay" {
					mutate("inject_failure")
				}
				target := id
				if scenario == "untargeted" {
					target = ""
				}
				code, _ = call("/test/browser", token, map[string]string{"text": answer, "answer_message_id": target, "client_id": "bound-input"})
				if code != 200 {
					t.Fatalf("browser: %d", code)
				}
				mutate("replay")
				if scenario == "browser_away" {
					wantRevision, wantItems = 1, 1
				}
			case scenario != "newest_ask" && scenario != "legacy_frame":
				code, out := call("/api/messages/"+id+"/reply", token, map[string]string{"answer": answer})
				accepted := scenario == "approve" || scenario == "remove" || scenario == "move" || scenario == "live_question" || scenario == "expire_cli" || scenario == "expire_waiter" || scenario == "daemon_empty" || scenario == "concurrent" || scenario == "two_asks" || scenario == "withdraw_forbidden" || scenario == "replay" || scenario == "server_restart" || scenario == "cli"
				if accepted {
					if code != 200 {
						t.Fatalf("answer: %d %+v", code, out)
					}
					receipt, ok := out["decision_receipt"].(map[string]any)
					if kind == "plan" && (!ok || len(receipt) != 4 || receipt["ask_id"] != id || receipt["status"] != "committed" || receipt["proposal_id"] != cfg["proposal"] || receipt["revision"] != float64(1)) {
						t.Fatalf("missing or unsafe public receipt: %+v", receipt)
					}
					if kind == "cut" && (!ok || receipt["status"] != "authorized" || receipt["release_id"] != cfg["cut_release"] || receipt["execution"] != "authorized") {
						t.Fatalf("cut authorization receipt: %+v", receipt)
					}
					if kind == "draft" && (!ok || len(receipt) != 5 || receipt["draft_id"] != cfg["draft"] || receipt["status"] != "committed" || receipt["blackboard_id"] == "") {
						t.Fatalf("draft receipt: %+v", receipt)
					}
					wantRevision, wantItems = 1, 1
					if scenario == "replay" {
						code, _ = call("/api/messages/"+id+"/reply", token, map[string]string{"answer": answer})
						if code != 200 {
							t.Fatalf("retry: %d", code)
						}
					}
				} else if scenario != "decline" && scenario != "empty_answer" && scenario != "empty_stale" && code == 200 {
					t.Fatalf("unsafe answer accepted in %s", scenario)
				}
			}
			if scenario == "stale_notice_restart" {
				stop()
				env = append(env, "BLERG_BOUND_SESSION="+cfg["session"], "BLERG_BOUND_BLACKBOARD="+cfg["blackboard"], "BLERG_BOUND_PROPOSAL="+cfg["proposal"])
				cfg, _ = startBoundProcess(t, env)
				mutate("recover_receipts")
			}
			if kind == "cut" {
				if code, out := call("/test/process_cut", token, map[string]any{}); code != 200 {
					t.Fatalf("cut execution: %d %+v", code, out)
				}
			}
			resp, e := http.Get(cfg["url"] + "/test/state")
			if e != nil {
				t.Fatal(e)
			}
			defer resp.Body.Close()
			var state struct {
				Revision          int
				Items             int
				Pending           int
				Sent              int
				Inputs            int
				DraftRows         int
				DraftHistory      int
				CreatedVision     string
				BindingStatus     string
				CanceledCause     string
				CanceledCount     int
				PublicCause       string
				Question          string
				CutVerified       bool
				CutCloseCalls     int
				Conversation      bool
				LegacySettled     bool
				SnapshotPreserved bool
				Receipt           string
			}
			if e = json.NewDecoder(resp.Body).Decode(&state); e != nil {
				t.Fatal(e)
			}
			if (scenario == "expire_cli" || scenario == "expire_waiter" || scenario == "daemon_empty") && state.BindingStatus != "committed" {
				t.Fatalf("held question: %+v", state)
			}
			if scenario == "expire_deadline" && state.BindingStatus != "canceled" {
				t.Fatalf("deadline: %+v", state)
			}
			if strings.HasPrefix(scenario, "socket_") {
				want := "open"
				if scenario == "socket_approve" {
					want = "committed"
				}
				if state.BindingStatus != want {
					t.Fatalf("socket binding: %+v; want %s", state, want)
				}
			}
			if scenario == "stale_notice_restart" {
				var receipt map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(state.Receipt, "Decision receipt from the server: ")), &receipt); err != nil || receipt["status"] != "canceled" || receipt["cause"] != func() string {
					if kind == "draft" {
						return "stale_draft"
					}
					return "stale_revision"
				}() || receipt["ask_id"] != id {
					t.Fatalf("recovered cancellation receipt: %s; %v", state.Receipt, err)
				}
			}
			if scenario == "two_asks" && kind == "plan" && (state.CanceledCount != 1 || state.CanceledCause != "superseded_by="+id) {
				t.Fatalf("replacement audit: %+v", state)
			}
			if scenario == "withdraw" && state.CanceledCause != "withdrawn_by_main" {
				t.Fatalf("withdrawal audit: %+v", state)
			}
			if scenario == "answer_blackboard_exists" && (state.CanceledCause != "blackboard_exists" || state.PublicCause != "blackboard_exists") {
				t.Fatalf("missing typed cancellation: %+v", state)
			}
			if kind == "draft" && strings.Contains(state.Question, "Release:") {
				t.Fatal("unlinked draft displayed an empty release")
			}
			if kind == "cut" {
				if wantItems == 1 && (!state.CutVerified || state.CutCloseCalls != 1) {
					t.Fatalf("cut not verified: final=%v calls=%d", state.CutVerified, state.CutCloseCalls)
				}
				if wantRevision == 0 && state.CutCloseCalls != 0 {
					t.Fatal("refused binding reached board close")
				}
			}
			if !state.SnapshotPreserved {
				t.Fatal("a note changed the authoritative snapshot")
			}
			wantPending, wantInputs, wantSent := 0, 0, 0
			if scenario == "ambiguous" || scenario == "ambiguous_canary" || scenario == "socket_ambiguous" || scenario == "rest_untargeted" || scenario == "untargeted" {
				wantPending, wantInputs = 1, 1
				if scenario == "untargeted" || scenario == "rest_untargeted" {
					wantSent = 1
				}
				if !state.Conversation {
					t.Fatal("person's words were not retained as conversation")
				}
			}
			if scenario == "legacy_frame" {
				wantInputs = 1
				if !state.LegacySettled {
					t.Fatal("old bound frame remained deliverable")
				}
			}
			expectedBinding := "open"
			if wantRevision == 1 && scenario != "answer_blackboard_exists" && scenario != "stale_revision" && scenario != "stale_notice_restart" {
				expectedBinding = "committed"
			} else {
				switch scenario {
				case "decline":
					expectedBinding = "declined"
				case "tx_draft_id", "tx_draft_revision", "tx_draft_digest", "answer_blackboard_exists", "withdraw", "empty_answer", "empty_stale", "changed_proposal", "stale_revision", "stale_notice_restart", "expire_deadline", "expired", "session_end", "discard", "archive", "dismiss", "session_close", "owner_transfer", "older_loop":
					expectedBinding = "canceled"
				}
			}
			if kind == "draft" {
				if state.DraftHistory < 1 {
					t.Fatal("immutable draft evidence is missing")
				}
				if expectedBinding == "committed" || expectedBinding == "declined" {
					if state.DraftRows != 0 {
						t.Fatal("decided draft is still pending")
					}
				}
				if expectedBinding == "committed" && state.CreatedVision != "A useful thing" {
					t.Fatalf("created a different snapshot: %+v", state)
				}
			}
			if state.BindingStatus != expectedBinding {
				t.Fatalf("binding: %+v; want %s", state, expectedBinding)
			}
			if state.Revision != wantRevision || state.Items != wantItems || state.Pending != wantPending || state.Sent != wantSent || state.Inputs != wantInputs {
				t.Fatalf("%s state %+v; want revision=%d items=%d and no deliverable input", scenario, state, wantRevision, wantItems)
			}
		})
	}
}

func startBoundProcess(t *testing.T, env []string) (map[string]string, func()) {
	t.Helper()
	ready := testsupport.Phase(t, "runner_process_ready")
	defer ready.End()
	cmd := exec.Command(os.Args[0], "-test.run=^TestBoundAskProcessServer$")
	cmd.Env = append(append(os.Environ(), "BLERG_BOUND_PROCESS=1"), env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			stopPhase := testsupport.Phase(t, "runner_process_stop")
			defer stopPhase.End()
			_ = stdin.Close()
			if e := cmd.Wait(); e != nil {
				t.Errorf("server exit: %v %s", e, diagnostics.String())
			}
		})
	}
	t.Cleanup(stop)
	cfg := map[string]string{}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if json.Unmarshal(scanner.Bytes(), &cfg) == nil && cfg["url"] != "" {
			break
		}
		diagnostics.Write(scanner.Bytes())
		diagnostics.WriteString("\n")
	}
	if cfg["url"] == "" {
		t.Fatalf("server did not start: %s", diagnostics.String())
	}
	return cfg, stop
}

func TestBoundAskProcessServer(t *testing.T) {
	if os.Getenv("BLERG_BOUND_PROCESS") != "1" {
		t.Skip("subprocess fixture")
	}
	if os.Getenv("BLERG_BOUND_BOARD_LINK") == "1" {
		serveBoardLinkProcess(t)
		return
	}
	var fx *topicFx
	if dsn := os.Getenv("BLERG_BOUND_DATABASE"); dsn != "" {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = "test_blerg_runner_srv"
		pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		fx = newTopicFxWithPool(t, pool)
	} else {
		fx = newTopicFx(t)
	}
	if session := os.Getenv("BLERG_BOUND_SESSION"); session != "" {
		fx.sess = session
		tok, err := db.MintSessionToken(t.Context(), fx.pool, session, []string{"message"}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		fx.sessTok = tok
	}
	fx.api.SetRunnerKey(runnerTestKey)
	kind := os.Getenv("BLERG_BOUND_SUBJECT")
	draftID := ""
	if kind == "draft" {
		if os.Getenv("BLERG_BOUND_SESSION") == "" {
			err := db.SaveBlackboardDraft(t.Context(), fx.pool, fx.sess, fx.sess, json.RawMessage(`{"name":"Exact draft","vision":"A useful thing","goals":["First goal"],"milestone":{"name":"First release"},"thrusts":[{"name":"First thrust","goal_index":0,"reason":"Required"}]}`))
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := fx.pool.QueryRow(t.Context(), `SELECT coalesce(to_jsonb(d)->>'id',workspace_id) FROM blackboard_drafts d WHERE workspace_id=$1`, fx.sess).Scan(&draftID); err != nil {
			t.Fatal(err)
		}
	}
	owner := blackboard.Actor{Kind: "human", ID: topicOwner}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Name: "Process plan", Version: 1}
	var cutConfig map[string]string
	if kind == "cut" {
		cutConfig = installCutBoardFixture(t, fx, &record)
	}
	operation := os.Getenv("BLERG_BOUND_OPERATION")
	if operation != "" {
		record.Items = []blackboard.Item{{ID: "existing", Title: "Existing scope item", State: "planned"}}
	}
	var pid blackboard.ProposalID
	switch bid := os.Getenv("BLERG_BOUND_BLACKBOARD"); {
	case kind == "draft": // No committed blackboard exists before the owner answers.
	case bid != "":
		record.ID = bid
		pid = blackboard.ProposalID(os.Getenv("BLERG_BOUND_PROPOSAL"))
	default:
		if err := fx.api.blackboardStore().Create(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		var err error
		change := blackboard.Change{Operation: "add", Item: blackboard.Item{Title: "Exact proposal item"}}
		if operation != "" {
			change = blackboard.Change{Operation: operation, Item: blackboard.Item{ID: "existing", Reason: "Scope changed"}}
			if operation == "move" {
				change.Item.Destination = "Next release"
			}
		}
		pid, err = (blackboard.Service{Store: fx.api.blackboardStore(), BlackboardID: record.ID}).Propose(context.Background(), blackboard.Actor{Kind: "agent", ID: fx.sess}, "", []blackboard.Change{change})
		if err != nil {
			t.Fatal(err)
		}

	}
	if os.Getenv("BLERG_BOUND_DISABLE_CLOSE_TRIGGER") == "1" {
		if kind == "draft" {
			if _, err := fx.pool.Exec(t.Context(), `ALTER TABLE blackboard_drafts DISABLE TRIGGER blackboard_draft_revision_save`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := fx.pool.Exec(t.Context(), `ALTER TABLE messages DISABLE TRIGGER bound_ask_close`); err != nil {
			t.Fatal(err)
		}
	}
	cutLoopTicks := make(chan time.Time, 1)
	var cutFailureWait <-chan struct{}
	cutLoopCtx, stopCutLoop := context.WithCancel(t.Context())
	var cutLoop sync.Once
	cutLoopDone := make(chan struct{})
	startedCutLoop := false
	defer func() {
		stopCutLoop()
		if startedCutLoop {
			<-cutLoopDone
		}
	}()
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	mux.HandleFunc("/ws/browser", fx.api.hub.ServeBrowser(fx.pool, fx.api.AuthorizeBrowserWS))
	mux.HandleFunc("POST /api/messages", func(w http.ResponseWriter, r *http.Request) { fx.api.HandlePostMessages(w, r) })
	mux.HandleFunc("POST /api/v1/sessions/{id}/message", fx.api.HandleRunnerMessage)
	mux.HandleFunc("POST /api/messages/{id}/expire", fx.api.HandlePostMessageExpire)
	mux.HandleFunc("POST /api/messages/{id}/reply", func(w http.ResponseWriter, r *http.Request) { fx.api.HandlePostMessageReply(w, r) })
	mux.HandleFunc("POST /test/process_cut", func(w http.ResponseWriter, r *http.Request) {
		fx.api.processReleaseCuts(r.Context())
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	sent := 0
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 32)}
	replayInputs := func(ctx context.Context) {
		fx.api.hub.mu.Lock()
		fx.api.hub.daemons[dc.ID] = dc
		fx.api.hub.mu.Unlock()
		replaySessionInputs(ctx, fx.api.hub, fx.pool, dc, fx.sess)
		fx.api.hub.mu.Lock()
		delete(fx.api.hub.daemons, dc.ID)
		fx.api.hub.mu.Unlock()
	}
	mux.HandleFunc("POST /test/browser", func(w http.ResponseWriter, r *http.Request) {
		p, ok := fx.api.authBrowser(w, r)
		if !ok {
			return
		}
		var msg protocol.AgentUserMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		msg.Type = "agent_user_message"
		msg.SessionID = fx.sess
		handleBrowserAgentMessage(r.Context(), fx.api.hub, &BrowserConn{AccountID: p.Sub, SessionID: p.Sid, verified: p}, fx.pool, msg)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /test/mutate", func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Action string }
		_ = json.NewDecoder(r.Body).Decode(&input)
		ctx := r.Context()
		var err error
		switch input.Action {
		case "cut_seed_thrusts", "cut_next_thrust_history":
			err = exerciseCutThrustHistory(ctx, fx, record.ID, input.Action)
		case "cut_changed_card", "cut_active_session", "cut_archive_gate_hold", "cut_archive_gate_deny", "cut_lost_credentials", "cut_direct_cut", "cut_legacy_cut", "cut_drop_close_response", "cut_drop_close_and_lose_credentials", "cut_restore_credentials", "cut_all_done", "cut_next_closed", "cut_pause_evidence", "cut_resume_evidence":
			err = mutateCutBoard(ctx, cutConfig, strings.TrimPrefix(input.Action, "cut_"))
		case "cut_startup":
			cutLoop.Do(func() {
				startedCutLoop = true
				go func() { defer close(cutLoopDone); fx.api.runReleaseCutLoop(cutLoopCtx, cutLoopTicks) }()
			})
		case "cut_inject_evidence_failure":
			_, err = fx.pool.Exec(ctx, `CREATE FUNCTION refuse_cut_evidence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.stage='Evidence incomplete' THEN RAISE EXCEPTION 'injected evidence projection conflict' USING ERRCODE='40001'; END IF; RETURN NEW; END $$; CREATE TRIGGER refuse_cut_evidence BEFORE UPDATE ON milestones FOR EACH ROW EXECUTE FUNCTION refuse_cut_evidence()`)
		case "cut_remove_evidence_failure":
			_, err = fx.pool.Exec(ctx, `DROP TRIGGER refuse_cut_evidence ON milestones`)
		case "cut_observation_due":
			_, err = fx.pool.Exec(ctx, `UPDATE blackboard_cut_observations SET next_check_at=clock_timestamp()`)
		case "cut_watch_failure":
			var id string
			err = fx.pool.QueryRow(ctx, `SELECT ask_id::text FROM blackboard_cut_executions WHERE workspace_id=$1`, fx.sess).Scan(&id)
			if err == nil {
				cutFailureWait = fx.api.waiters.channel(id)
			}
		case "cut_db_outage":
			_, err = fx.pool.Exec(ctx, `ALTER TABLE blackboards RENAME TO blackboards_outage`)
		case "cut_db_restore":
			_, err = fx.pool.Exec(ctx, `ALTER TABLE blackboards_outage RENAME TO blackboards`)
		case "cut_expire_answer_token":
			_, err = fx.pool.Exec(ctx, `ALTER TABLE blackboard_cut_executions DISABLE TRIGGER cut_authorization_immutable; UPDATE blackboard_cut_executions SET principal=jsonb_set(principal,'{exp}',to_jsonb(extract(epoch from clock_timestamp()-interval '1 minute')::bigint)); ALTER TABLE blackboard_cut_executions ENABLE TRIGGER cut_authorization_immutable`)
			fx.api.coreURL = "http://127.0.0.1:1"
		case "cut_core_outage":
			fx.api.coreURL = "http://127.0.0.1:1"
		case "cut_core_refused":
			installCutCoreStatusFixture(t, fx, 404)
		case "cut_core_restore":
			installCutCoreFixture(t, fx)
		case "cut_inject_serialization":
			_, err = fx.pool.Exec(ctx, `CREATE FUNCTION refuse_cut_final() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected serialization conflict' USING ERRCODE='40001'; END $$; CREATE TRIGGER refuse_cut_final BEFORE INSERT ON blackboard_cut_finalizations FOR EACH ROW EXECUTE FUNCTION refuse_cut_final()`)
		case "cut_delay":
			select {
			case <-fx.api.releaseCutWake:
			default:
			}
			_, err = fx.pool.Exec(ctx, `UPDATE blackboard_cut_executions SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE workspace_id=$1`, fx.sess)
		case "cut_ready":
			_, err = fx.pool.Exec(ctx, `UPDATE blackboard_cut_executions SET next_attempt_at=clock_timestamp() WHERE workspace_id=$1`, fx.sess)
		case "cut_tick":
			_, _ = fx.pool.Exec(ctx, `UPDATE blackboard_cut_observations SET next_check_at=clock_timestamp()`)
			_, err = fx.pool.Exec(ctx, `UPDATE blackboard_cut_executions SET next_attempt_at=clock_timestamp() WHERE workspace_id=$1`, fx.sess)
			cutLoopTicks <- time.Now()
		case "cut_inject_final_failure":
			_, err = fx.pool.Exec(ctx, `CREATE FUNCTION refuse_cut_final() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected final persistence failure'; END $$; CREATE TRIGGER refuse_cut_final BEFORE INSERT ON blackboard_cut_finalizations FOR EACH ROW EXECUTE FUNCTION refuse_cut_final()`)
		case "cut_remove_final_failure":
			_, err = fx.pool.Exec(ctx, `DROP TRIGGER refuse_cut_final ON blackboard_cut_finalizations; UPDATE blackboard_cut_observations SET next_check_at=clock_timestamp()`)
		case "cut_read_grant":
			_, err = fx.pool.Exec(ctx, `UPDATE session_proposal_grants SET level='read' WHERE session_id=$1 AND kind='board_file'`, fx.sess)
		case "cut_revoke_grant":
			_, err = fx.pool.Exec(ctx, `UPDATE session_proposal_grants SET revoked_at=clock_timestamp() WHERE session_id=$1 AND kind='board_file'`, fx.sess)
		case "cut_link_changed":
			err = fx.api.blackboardStore().Update(ctx, record.ID, func(r *blackboard.Record) error { r.Milestones[0].ReleaseID = "different-release"; return nil })
		case "cut_deadline":
			_, err = fx.pool.Exec(ctx, `ALTER TABLE blackboard_cut_executions DISABLE TRIGGER cut_authorization_immutable; UPDATE blackboard_cut_executions SET deadline=clock_timestamp()-interval '1 second'; ALTER TABLE blackboard_cut_executions ENABLE TRIGGER cut_authorization_immutable`)
		case "cut_age_link":
			_, err = fx.pool.Exec(ctx, `ALTER TABLE blackboard_release_links DISABLE TRIGGER blackboard_release_links_immutable; UPDATE blackboard_release_links SET linked_at=clock_timestamp()-interval '10 minutes'; ALTER TABLE blackboard_release_links ENABLE TRIGGER blackboard_release_links_immutable`)
		case "cut_seed_legacy":
			err = fx.api.blackboardStore().Update(ctx, record.ID, func(r *blackboard.Record) error {
				r.Milestones[0].Stage = "Finalizing"
				r.Milestones[0].Frozen = json.RawMessage(`{"final_intent":{"request_id":"legacy-intent"}}`)
				return nil
			})
		case "cut_read":
			request := httptest.NewRequest("GET", "/api/blackboards/"+record.ID+"?session="+fx.sess, nil)
			request.SetPathValue("id", record.ID)
			request.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
			response := httptest.NewRecorder()
			fx.api.HandleBlackboard(response, request)
			if response.Code != 200 {
				err = fmt.Errorf("read status %d", response.Code)
			}
		case "cut_duplicate":
			fx.api.processReleaseCuts(ctx)
			fx.api.processReleaseCuts(ctx)
		case "revoke_login", "cut_revoke_lineage":
			revocationKind, revocationValue := "sid", testSID
			if input.Action == "cut_revoke_lineage" {
				revocationKind, revocationValue = "lineage", "cut-login-lineage"
				_, err = fx.pool.Exec(ctx, `ALTER TABLE blackboard_cut_executions DISABLE TRIGGER cut_authorization_immutable; UPDATE blackboard_cut_executions SET principal=jsonb_set(principal,'{lineage}','"cut-login-lineage"'); ALTER TABLE blackboard_cut_executions ENABLE TRIGGER cut_authorization_immutable`)
			}
			core := http.NewServeMux()
			core.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"core-1": base64.RawURLEncoding.EncodeToString(fx.priv.Public().(ed25519.PublicKey))})
			})
			core.HandleFunc("/revocations", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode([]map[string]string{{"kind": revocationKind, "value": revocationValue}})
			})
			provider := httptest.NewServer(core)
			t.Cleanup(provider.Close)
			fx.api.coreAuth = ownedCoreAuthClient(t, provider.URL)
		case "child":
			_, err = fx.pool.Exec(ctx, `UPDATE sessions SET parent_session_id=$2 WHERE id=$1`, fx.sess, fx.ended)
		case "ownerless":
			_, err = fx.pool.Exec(ctx, `UPDATE sessions SET spawning_account_id=NULL WHERE id=$1`, fx.sess)
		case "canary_proposal":
			if kind == "draft" {
				err = db.SaveBlackboardDraft(ctx, fx.pool, fx.sess, fx.sess, json.RawMessage(`{"name":"CANARY_SCOPE\n\nNot applied: part of a title\nKeep this visible","vision":"Useful","goals":["Goal"]}`))
				break
			}
			title, _ := json.Marshal("CANARY_SCOPE\n\nNot applied: part of a title\nKeep this visible")
			_, err = fx.pool.Exec(ctx, `UPDATE plan_proposals SET data=jsonb_set(data,'{changes,0,item,title}',$2::jsonb) WHERE id=$1`, pid, title)
		case "tx_draft_id":
			_, err = fx.pool.Exec(ctx, `UPDATE blackboard_drafts SET id=gen_random_uuid() WHERE workspace_id=$1`, fx.sess)
		case "tx_draft_revision":
			_, err = fx.pool.Exec(ctx, `UPDATE blackboard_drafts SET revision=revision+1 WHERE workspace_id=$1`, fx.sess)
		case "tx_draft_digest":
			_, err = fx.pool.Exec(ctx, `UPDATE blackboard_drafts SET draft=jsonb_set(draft,'{vision}','"Changed"') WHERE workspace_id=$1`, fx.sess)
		case "existing_blackboard":
			err = fx.api.blackboardStore().Create(ctx, record)
		case "changed_proposal":
			if kind == "cut" {
				err = mutateCutBoard(ctx, cutConfig, "changed_card")
				break
			}
			if kind == "draft" {
				err = db.SaveBlackboardDraft(ctx, fx.pool, fx.sess, fx.sess, json.RawMessage(`{"name":"Changed draft"}`))
				break
			}
			_, err = fx.pool.Exec(ctx, `UPDATE plan_proposals SET data=jsonb_set(data,'{changes,0,item,title}','"Changed"') WHERE id=$1`, pid)
		case "stale_revision":
			if kind == "cut" {
				err = fx.api.blackboardStore().Update(ctx, record.ID, func(r *blackboard.Record) error { r.Milestones[0].Revision++; return nil })
				break
			}
			if kind == "draft" {
				err = db.SaveBlackboardDraft(ctx, fx.pool, fx.sess, fx.sess, json.RawMessage(`{"name":"New revision"}`))
				break
			}
			err = fx.api.blackboardStore().Update(ctx, record.ID, func(r *blackboard.Record) error { r.PlanRevision++; return nil })
		case "expired":
			_, err = fx.pool.Exec(ctx, `UPDATE bound_asks SET expires_at=now()-interval '1 second' WHERE workspace_id=$1`, fx.sess)
		case "session_end":
			_, err = fx.pool.Exec(ctx, `UPDATE sessions SET status='ended' WHERE id=$1`, fx.sess)
		case "discard":
			_, err = fx.pool.Exec(ctx, `UPDATE sessions SET discard_requested_at=now() WHERE id=$1`, fx.sess)
		case "archive":
			_, err = fx.pool.Exec(ctx, `UPDATE sessions SET archived_at=now() WHERE id=$1`, fx.sess)
		case "dismiss":
			var id string
			err = fx.pool.QueryRow(ctx, `SELECT message_id::text FROM bound_asks WHERE workspace_id=$1 AND status='open'`, fx.sess).Scan(&id)
			if err == nil {
				_, err = db.DismissMessage(ctx, fx.pool, id, topicOwner)
			}
		case "session_close":
			_, err = db.CloseOpenMessagesForSession(ctx, fx.pool, fx.sess)
		case "legacy_frame":
			var id string
			err = fx.pool.QueryRow(ctx, `SELECT message_id::text FROM bound_asks WHERE workspace_id=$1 AND status='open'`, fx.sess).Scan(&id)
			if err == nil {
				frame, _ := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: fx.sess, AnswerMessageID: id, Text: "yes", ClientID: "old-bound-frame"})
				_, _, err = db.AcceptSessionInput(ctx, fx.pool, fx.sess, "old-bound-frame", string(frame), newUUID(), `{"text":"yes","client_id":"old-bound-frame"}`)
				if err == nil {
					replayInputs(ctx)
				}
			}
		case "owner_transfer":
			if kind == "draft" {
				_, err = fx.pool.Exec(ctx, `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, fx.sess, topicOther)
				break
			}
			_, err = fx.pool.Exec(ctx, `UPDATE blackboards SET owner_id=$2 WHERE id=$1`, record.ID, topicOther)
		case "inject_notice_failure":
			_, err = fx.pool.Exec(ctx, `CREATE FUNCTION refuse_bound_notice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.frame->>'source'='question_answer' THEN RAISE EXCEPTION 'injected notice failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER refuse_bound_notice BEFORE INSERT ON session_inputs FOR EACH ROW EXECUTE FUNCTION refuse_bound_notice()`)
		case "recover_receipts":
			_, err = fx.pool.Exec(ctx, `DROP TRIGGER refuse_bound_notice ON session_inputs`)
			if err == nil {
				var rows []db.MessageRow
				rows, err = db.UndeliveredQuestionAnswers(ctx, fx.pool, fx.sess)
				for _, row := range rows {
					fx.api.sendQuestionAnswer(ctx, row, derefOrEmpty(row.Answer), "")
				}
			}
		case "inject_failure":
			_, err = fx.pool.Exec(ctx, `CREATE FUNCTION refuse_bound_answer() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='user_message' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER refuse_bound_answer BEFORE INSERT ON agent_events FOR EACH ROW EXECUTE FUNCTION refuse_bound_answer()`)
		case "replay":
			replayInputs(ctx)

		case "older_loop":
			var id string
			err = fx.pool.QueryRow(ctx, `SELECT message_id::text FROM bound_asks WHERE workspace_id=$1`, fx.sess).Scan(&id)
			if err == nil {
				_, err = db.AnswerMessageAs(ctx, fx.pool, id, "yes", false, "")
			}
		case "newest_ask":
			answerAskFromChatAs(ctx, fx.api.hub, fx.pool, fx.sess, "yes", "legacy-input", "", topicOwner)
		}
		if err != nil {
			http.Error(w, "fixture mutation failed", 500)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /test/state", func(w http.ResponseWriter, r *http.Request) {
		stored, e := fx.api.blackboardStore().Load(r.Context(), record.ID)
		if kind == "draft" {
			e = nil
			var total int
			_ = fx.pool.QueryRow(r.Context(), `SELECT count(*) FROM blackboards WHERE record->>'workspace_id'=$1`, fx.sess).Scan(&total)
			stored = blackboard.Record{PlanRevision: total, Items: make([]blackboard.Item, total)}
		}
		if kind == "cut" {
			e = nil
			var executions, finals int
			_ = fx.pool.QueryRow(r.Context(), `SELECT count(*) FROM blackboard_cut_executions WHERE workspace_id=$1`, fx.sess).Scan(&executions)
			_ = fx.pool.QueryRow(r.Context(), `SELECT count(*) FROM blackboard_cut_finalizations WHERE blackboard_id=$1`, record.ID).Scan(&finals)
			stored = blackboard.Record{PlanRevision: executions, Items: make([]blackboard.Item, finals)}
		}
		if e != nil {
			http.Error(w, "state unavailable", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(func() map[string]any {
			var pending int
			_ = fx.pool.QueryRow(r.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1 AND settled_at IS NULL AND coalesce(frame::jsonb->>'source','')<>'question_answer'`, fx.sess).Scan(&pending)
			var inputs int
			_ = fx.pool.QueryRow(r.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1 AND coalesce(frame::jsonb->>'source','')<>'question_answer'`, fx.sess).Scan(&inputs)
			for len(dc.send) > 0 {
				raw := <-dc.send
				var frame protocol.AgentUserMessage
				_ = json.Unmarshal(raw, &frame)
				if frame.Source != questionAnswerSource {
					sent++
				}
			}
			var bindingStatus string
			_ = fx.pool.QueryRow(r.Context(), `SELECT status FROM bound_asks WHERE workspace_id=$1 ORDER BY expires_at DESC LIMIT 1`, fx.sess).Scan(&bindingStatus)
			var conversation, legacySettled, snapshotPreserved bool
			_ = fx.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_events WHERE session_id=$1 AND kind='input_accepted' AND payload->>'text' IN ('yes but exclude one','yes'))`, fx.sess).Scan(&conversation)
			_ = fx.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM session_inputs WHERE session_id=$1 AND client_id='old-bound-frame' AND settled_at IS NOT NULL)`, fx.sess).Scan(&legacySettled)
			_ = fx.pool.QueryRow(r.Context(), `SELECT coalesce(bool_and(left(m.body,length(e.payload->>'body'))=e.payload->>'body'),false) FROM messages m JOIN bound_asks b ON b.message_id=m.id JOIN agent_events e ON e.session_id=m.session_id AND e.kind='ask' AND e.payload->>'message_id'=m.id::text WHERE m.session_id=$1`, fx.sess).Scan(&snapshotPreserved)
			var receipt string
			_ = fx.pool.QueryRow(r.Context(), `SELECT frame->>'text' FROM session_inputs WHERE session_id=$1 AND frame->>'source'='question_answer' ORDER BY event_seq DESC LIMIT 1`, fx.sess).Scan(&receipt)
			var question string
			_ = fx.pool.QueryRow(r.Context(), `SELECT m.body FROM messages m JOIN bound_asks b ON b.message_id=m.id WHERE b.workspace_id=$1 ORDER BY m.created_at DESC LIMIT 1`, fx.sess).Scan(&question)
			cutVerified := false
			cutCloseCalls := 0
			evidenceHeld := false
			if kind == "cut" {
				actual, e := readCutBoardState(r.Context(), cutConfig)
				if e == nil {
					_ = json.Unmarshal(actual["close_calls"], &cutCloseCalls)
					_ = json.Unmarshal(actual["evidence_held"], &evidenceHeld)
					var release cutReleaseView
					_ = json.Unmarshal(actual["release"], &release)
					var cards []struct {
						ReleaseID  *string `json:"release_id"`
						ArchivedAt *string `json:"archived_at"`
					}
					_ = json.Unmarshal(actual["cards"], &cards)
					current, e := fx.api.blackboardStore().Load(r.Context(), record.ID)
					if e == nil && len(current.Milestones) == 1 && len(cards) == 2 && release.Receipt != nil {
						m := current.Milestones[0]
						var frozen blackboard.SliceView
						_ = json.Unmarshal(m.Frozen, &frozen)
						finalEvents := 0
						for _, event := range current.Events {
							if event.Kind == "final" && event.Source == "derived" {
								finalEvents++
							}
						}
						cutVerified = m.Stage == "Final" && m.ShippedAt != nil && m.ShippedAt.Equal(release.Receipt.ReleasedAt) && m.FrozenAt != nil && m.CutReceiptID == release.Receipt.ID && frozen.BoardCut != nil && frozen.BoardCut.ID == release.Receipt.ID && frozen.RunnerRecord != nil && len(frozen.BoardCut.Snapshot) == 2 && len(frozen.RunnerRecord.Proposals) == 0 && len(frozen.RunnerRecord.Revisions) > 0 && len(frozen.RunnerRecord.Snapshots) > 0 && len(frozen.RunnerRecord.Events) >= 2 && release.Release.Status == "released" && ((release.Receipt.NextReleaseID == "" && cards[0].ArchivedAt != nil) || (cards[0].ReleaseID != nil && *cards[0].ReleaseID == cutConfig["next_release_id"] && cards[0].ArchivedAt == nil)) && cards[1].ArchivedAt != nil && finalEvents == 1
					}
				}
			}
			var canceledCause string
			var canceledCount int
			_ = fx.pool.QueryRow(r.Context(), `SELECT count(*),coalesce(max(note),'') FROM bound_asks WHERE workspace_id=$1 AND status='canceled'`, fx.sess).Scan(&canceledCount, &canceledCause)
			var latestID, publicCause string
			_ = fx.pool.QueryRow(r.Context(), `SELECT message_id::text FROM bound_asks WHERE workspace_id=$1 ORDER BY expires_at DESC LIMIT 1`, fx.sess).Scan(&latestID)
			if receipt, _, e := fx.api.boundReceipt(r.Context(), latestID); e == nil {
				publicCause, _ = receipt["cause"].(string)
			}
			var draftRows, draftHistory int
			var createdVision string
			if kind == "draft" {
				_ = fx.pool.QueryRow(r.Context(), `SELECT count(*) FROM blackboard_drafts WHERE workspace_id=$1`, fx.sess).Scan(&draftRows)
				_ = fx.pool.QueryRow(r.Context(), `SELECT count(*) FROM blackboard_draft_revisions WHERE workspace_id=$1`, fx.sess).Scan(&draftHistory)
				_ = fx.pool.QueryRow(r.Context(), `SELECT record->>'vision' FROM blackboards WHERE record->>'workspace_id'=$1`, fx.sess).Scan(&createdVision)
			}
			var executionStatus, executionCause, observationCause string
			_ = fx.pool.QueryRow(r.Context(), `SELECT cause FROM blackboard_cut_observations WHERE milestone_id=$1`, "cut-milestone").Scan(&observationCause)
			_ = fx.pool.QueryRow(r.Context(), `SELECT status,cause FROM blackboard_cut_executions WHERE workspace_id=$1 LIMIT 1`, fx.sess).Scan(&executionStatus, &executionCause)
			var failureQuestions, finals, legacyEvidence int
			_ = fx.pool.QueryRow(r.Context(), `SELECT count(*) FROM messages m JOIN agent_events e ON e.session_id=m.session_id AND e.kind='ask' AND e.payload->>'message_id'=m.id::text WHERE m.session_id=$1 AND m.person_required AND m.owner_only AND m.status='open' AND m.body LIKE 'Cut authorized, not executed:%'`, fx.sess).Scan(&failureQuestions)
			_ = fx.pool.QueryRow(r.Context(), `SELECT count(*) FROM blackboard_cut_finalizations WHERE blackboard_id=$1`, record.ID).Scan(&finals)
			_ = fx.pool.QueryRow(r.Context(), `SELECT count(*) FROM blackboard_cut_legacy_evidence WHERE blackboard_id=$1`, record.ID).Scan(&legacyEvidence)
			current, _ := fx.api.blackboardStore().Load(r.Context(), record.ID)
			stage := ""
			if len(current.Milestones) > 0 {
				stage = current.Milestones[0].Stage
			}
			failureWaiterWoken := false
			select {
			case <-cutFailureWait:
				failureWaiterWoken = true
			default:
			}
			return map[string]any{"FailureWaiterWoken": failureWaiterWoken, "EvidenceHeld": evidenceHeld, "ObservationCause": observationCause, "ExecutionStatus": executionStatus, "ExecutionCause": executionCause, "FailureQuestions": failureQuestions, "Finals": finals, "LegacyEvidence": legacyEvidence, "Stage": stage, "CutVerified": cutVerified, "CutCloseCalls": cutCloseCalls, "PublicCause": publicCause, "CanceledCount": canceledCount, "CanceledCause": canceledCause, "DraftRows": draftRows, "DraftHistory": draftHistory, "CreatedVision": createdVision, "Question": question, "Receipt": receipt, "SnapshotPreserved": snapshotPreserved, "Conversation": conversation, "LegacySettled": legacySettled, "BindingStatus": bindingStatus, "revision": stored.PlanRevision, "items": len(stored.Items), "pending": pending, "sent": sent, "Inputs": inputs}
		}())
	})
	if os.Getenv("BLERG_BOUND_RECONCILE") == "1" {
		go fx.api.RunBoundAskReconciliation(t.Context())
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cutBoard, cutRelease, cutNext := "", "", ""
	if cutConfig != nil {
		cutBoard = cutConfig["board_id"]
		cutRelease = cutConfig["release_id"]
		cutNext = cutConfig["next_release_id"]
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"fixture_database": fx.pool.Config().ConnConfig.Database, "cut_board": cutBoard, "cut_release": cutRelease, "cut_next": cutNext, "url": srv.URL, "session_token": fx.sessTok, "human_token": fx.token(topicOwner, "human", coreAuthBrowserCap, coreAuthRunnerCap), "other_human_token": fx.token(topicOther, "human"), "session": fx.sess, "blackboard": record.ID, "proposal": string(pid), "draft": draftID, "agent_browser_token": fx.token(topicOwner, "agent"), "onbehalf_token": mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Kind: "human", Sub: topicOwner, Sid: testSID, OnBehalfOf: "agent-source", Caps: []string{coreAuthBrowserCap}, Aud: coreAuthAudience, ExpiresAt: time.Now().Add(time.Minute).Unix()})})
	_, _ = io.Copy(io.Discard, os.Stdin)

}
