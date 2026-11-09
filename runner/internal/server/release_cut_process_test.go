package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every case owns real runner and board processes and PostgreSQL. Fixture controls
// change the real databases or real API's state; no decision/executor is mocked.
func TestBoundCutExecutionProcess(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	binary := buildCutBoardBinary(t)
	cases := []struct {
		name, action, cause string
		calls, finals       int
	}{
		{"expired_answer_token", "cut_expire_answer_token", "", 1, 1},
		{"transient_db", "cut_db_outage", "", 1, 1},
		{"transient_core", "cut_core_outage", "", 1, 1},
		{"serialization", "after:cut_remove_final_failure", "", 1, 1},
		{"all_done_no_next", "cut_duplicate", "", 1, 1},
		{"missing_destination", "creation:none", "", 0, 0},
		{"released_link", "creation:cut_direct_cut", "", 0, 0},
		{"closed_destination", "creation:cut_next_closed", "", 0, 0},
		{"missing_explicit_link", "creation:none", "", 0, 0},
		{"subject_canary", "cut_duplicate", "", 1, 1},
		{"decline_after_release", "cut_direct_cut", "", 0, 0},
		{"archive_gate_hold", "cut_archive_gate_hold", "archive_gate_hold", 1, 0},
		{"archive_gate_deny", "cut_archive_gate_deny", "archive_gate_deny", 1, 0},
		{"active_session", "cut_active_session", "active_card_sessions", 1, 0},
		{"stale_version", "cut_changed_card", "card_digest_mismatch", 1, 0},
		{"lost_credentials", "cut_lost_credentials", "lost_credentials", 0, 0},
		{"core_login_ended", "cut_core_refused", "lost_credentials", 0, 0},
		{"revoked_lineage", "cut_revoke_lineage", "lost_credentials", 0, 0},
		{"revoked_login", "revoke_login", "lost_credentials", 0, 0},
		{"main_ended", "session_end", "main_ended", 0, 0},
		{"ownership_changed", "owner_transfer", "ownership_changed", 0, 0},
		{"link_changed", "cut_link_changed", "changed_release_link", 0, 0},
		{"deadline", "cut_deadline", "execution_deadline", 0, 0},
		{"already_closed", "cut_direct_cut", "release_already_closed", 0, 0},
		{"direct_old_link", "cut_direct_cut", "release_already_closed", 0, 1},
		{"legacy_receipt", "cut_legacy_cut", "release_already_closed", 0, 0},
		{"lost_response", "cut_drop_close_response", "", 1, 1},
		{"duplicate", "cut_duplicate", "", 1, 1},
		{"startup", "cut_startup", "", 1, 1},
		{"poll", "cut_delay", "", 1, 1},
		{"read_trigger", "after:cut_read", "", 1, 1},
		{"restart", "cut_startup", "", 1, 1},
		{"restart_after_close", "after:cut_remove_final_failure", "", 1, 1},
		{"late_receipt", "cut_drop_close_and_lose_credentials", "", 1, 1},
		{"grant_revoked_during_receipt", "after:cut_pause_evidence", "", 1, 0},
		{"main_ended_during_receipt", "after:cut_pause_evidence", "", 1, 0},
		{"link_changed_during_receipt", "after:cut_pause_evidence", "", 1, 0},
		{"read_grant", "cut_read_grant", "lost_credentials", 0, 0},
		{"finalization_recovery", "after:cut_remove_final_failure", "", 1, 1},
		{"main_ended_after_close", "after:session_end", "", 1, 0},
		{"grant_revoked_after_close", "after:cut_revoke_grant", "", 1, 0},
		{"ownership_changed_after_close", "after:owner_transfer", "", 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parallelBoundProcess(t)
			board := startCutBoardProcess(t, binary, tc.name)
			path := filepath.Join(t.TempDir(), "board.json")
			raw, _ := json.Marshal(board)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			env := []string{"BLERG_BOUND_SUBJECT=cut", "BLERG_BOUND_CUT_BOARD_CONFIG=" + path}
			if strings.HasPrefix(tc.name, "restart") {
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
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				var out map[string]any
				_ = json.NewDecoder(response.Body).Decode(&out)
				return response.StatusCode, out
			}
			mutate := func(action string) {
				t.Helper()
				if code, out := call("/test/mutate", "", map[string]string{"action": action}); code != 200 {
					t.Fatalf("fixture %s: %d %+v", action, code, out)
				}
			}
			subject := map[string]any{"kind": "cut", "blackboard_id": cfg["blackboard"], "milestone_id": "cut-milestone", "revision": 0, "cut": map[string]any{"board_id": cfg["cut_board"], "release_id": cfg["cut_release"], "next_release_id": cfg["cut_next"], "link": true}}
			if tc.name == "all_done_no_next" {
				mutate("cut_all_done")
				subject["cut"].(map[string]any)["next_release_id"] = ""
			}
			if tc.name == "subject_canary" {
				c := subject["cut"].(map[string]any)
				c["card_digest"] = "FORGED"
				c["base_board_id"] = "FORGED"
				c["snapshot"] = []any{map[string]any{"card": map[string]any{"title": "FORGED"}}}
			}
			if strings.HasPrefix(tc.action, "creation:") {
				action := strings.TrimPrefix(tc.action, "creation:")
				if action == "none" {
					if tc.name == "missing_destination" {
						subject["cut"].(map[string]any)["next_release_id"] = ""
					} else {
						subject["cut"].(map[string]any)["link"] = false
					}
				} else {
					mutate(action)
				}
			}
			code, out := call("/api/messages", cfg["session_token"], map[string]any{"kind": "ask", "subject": subject, "body": "not authoritative"})
			if strings.HasPrefix(tc.action, "creation:") {
				if code == 201 {
					t.Fatalf("unsafe link accepted: %+v", out)
				}
				if code, out = call("/test/process_cut", "", nil); code != 200 {
					t.Fatalf("detect unlinked cut: %d %+v", code, out)
				}
				response, err := http.Get(cfg["url"] + "/test/state")
				if err != nil {
					t.Fatal(err)
				}
				var state struct{ Finals, CutCloseCalls, Revision int }
				_ = json.NewDecoder(response.Body).Decode(&state)
				response.Body.Close()
				if state.Finals != 0 || state.CutCloseCalls != 0 || state.Revision != 0 {
					t.Fatalf("unlinked cut finalized: %+v", state)
				}
				return
			}
			if code != 201 {
				t.Fatalf("create: %d %+v", code, out)
			}
			if tc.name == "subject_canary" {
				response, err := http.Get(cfg["url"] + "/test/state")
				if err != nil {
					t.Fatal(err)
				}
				var state struct{ Question string }
				_ = json.NewDecoder(response.Body).Decode(&state)
				response.Body.Close()
				if strings.Contains(state.Question, "FORGED") || !strings.Contains(state.Question, "Finished work") {
					t.Fatal("agent content replaced the server's cut snapshot")
				}
			}
			id := out["id"].(string)
			answer := "yes"
			if tc.name == "decline_after_release" {
				mutate(tc.action)
				answer = "no"
			}
			if code, out = call("/api/messages/"+id+"/reply", cfg["human_token"], map[string]string{"answer": answer}); code != 200 {
				t.Fatalf("authorize: %d %+v", code, out)
			}
			if tc.name == "decline_after_release" {
				response, err := http.Get(cfg["url"] + "/test/state")
				if err != nil {
					t.Fatal(err)
				}
				var state struct {
					BindingStatus    string
					Revision, Finals int
				}
				_ = json.NewDecoder(response.Body).Decode(&state)
				response.Body.Close()
				if state.BindingStatus != "declined" || state.Revision != 0 || state.Finals != 0 {
					t.Fatalf("decline blocked by release state: %+v", state)
				}
				return
			}
			if tc.name == "restart" {
				stop()
				env = append(env, "BLERG_BOUND_SESSION="+cfg["session"], "BLERG_BOUND_BLACKBOARD="+cfg["blackboard"], "BLERG_BOUND_PROPOSAL="+cfg["proposal"])
				cfg, _ = startBoundProcess(t, env)
			}
			if tc.cause != "" {
				mutate("cut_watch_failure")
			}
			if tc.name == "direct_old_link" {
				mutate("cut_age_link")
			}
			if strings.HasPrefix(tc.action, "after:") {
				if tc.name == "serialization" {
					mutate("cut_inject_serialization")
				} else {
					mutate("cut_inject_final_failure")
				}
			} else {
				mutate(tc.action)
			}
			if tc.name == "poll" {
				mutate("cut_startup")
				deadline := time.Now().Add(15 * time.Second)
				for {
					response, err := http.Get(cfg["url"] + "/test/state")
					if err != nil {
						t.Fatal(err)
					}
					var state struct {
						ObservationCause string
						CutCloseCalls    int
					}
					_ = json.NewDecoder(response.Body).Decode(&state)
					response.Body.Close()
					if state.CutCloseCalls != 0 {
						t.Fatal("startup ignored delayed eligibility")
					}
					if state.ObservationCause == "release_open" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("startup reconciliation did not finish")
					}
				}
				mutate("cut_tick")
			}
			if tc.name == "startup" || tc.name == "restart" || tc.name == "poll" {
				deadline := time.Now().Add(15 * time.Second)
				for {
					response, err := http.Get(cfg["url"] + "/test/state")
					if err != nil {
						t.Fatal(err)
					}
					var state struct{ Finals int }
					_ = json.NewDecoder(response.Body).Decode(&state)
					response.Body.Close()
					if state.Finals == 1 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("server loop did not finalize without a daemon")
					}
				}
			}
			if code, out = call("/test/process_cut", "", nil); code != 200 {
				t.Fatalf("execute: %d %+v", code, out)
			}
			if tc.name == "transient_core" || tc.name == "expired_answer_token" || tc.name == "transient_db" {
				response, err := http.Get(cfg["url"] + "/test/state")
				if err != nil {
					t.Fatal(err)
				}
				var state struct {
					ExecutionStatus, ExecutionCause string
					FailureQuestions, CutCloseCalls int
				}
				_ = json.NewDecoder(response.Body).Decode(&state)
				response.Body.Close()
				wantCause := "core_unreachable"
				if tc.name == "transient_db" {
					wantCause = "db_unavailable"
				}
				if state.ExecutionStatus != "authorized" || state.ExecutionCause != wantCause || state.FailureQuestions != 0 || state.CutCloseCalls != 0 {
					t.Fatalf("transient outage became terminal: %+v", state)
				}
				if tc.name == "transient_db" {
					mutate("cut_db_restore")
				} else {
					mutate("cut_core_restore")
				}
				mutate("cut_ready")
				if code, out = call("/test/process_cut", "", nil); code != 200 {
					t.Fatalf("retry: %d %+v", code, out)
				}
			}
			if tc.name == "late_receipt" {
				mutate("cut_ready")
				if code, out = call("/test/process_cut", "", nil); code != 200 {
					t.Fatalf("settle transport ambiguity: %d %+v", code, out)
				}
				response, err := http.Get(cfg["url"] + "/test/state")
				if err != nil {
					t.Fatal(err)
				}
				var state struct {
					ExecutionStatus  string
					FailureQuestions int
				}
				_ = json.NewDecoder(response.Body).Decode(&state)
				response.Body.Close()
				if state.ExecutionStatus != "failed" || state.FailureQuestions != 1 {
					t.Fatalf("missing terminal failure ask: %+v", state)
				}
				mutate("cut_restore_credentials")
				mutate("cut_observation_due")
			}
			if tc.name == "restart_after_close" {
				stop()
				env = append(env, "BLERG_BOUND_SESSION="+cfg["session"], "BLERG_BOUND_BLACKBOARD="+cfg["blackboard"], "BLERG_BOUND_PROPOSAL="+cfg["proposal"])
				cfg, _ = startBoundProcess(t, env)
			}
			if strings.HasPrefix(tc.action, "after:") {
				response, err := http.Get(cfg["url"] + "/test/state")
				if err != nil {
					t.Fatal(err)
				}
				var state struct {
					Stage, ObservationCause string
					Finals                  int
					ExecutionStatus         string
				}
				_ = json.NewDecoder(response.Body).Decode(&state)
				response.Body.Close()
				if state.Finals != 0 || state.ExecutionStatus != "executed" {
					t.Fatalf("injected persistence failure lost its boundary: %+v", state)
				}
				if strings.TrimPrefix(tc.action, "after:") != "cut_remove_final_failure" {
					mutate("cut_remove_final_failure")
				}
				if tc.name == "serialization" && (state.Stage == "Evidence incomplete" || state.ObservationCause != "finalization_conflict") {
					t.Fatal("serialization conflict corrupted evidence state")
				}
				mutate(strings.TrimPrefix(tc.action, "after:"))
				if tc.name == "read_trigger" {
					response, err := http.Get(cfg["url"] + "/test/state")
					if err != nil {
						t.Fatal(err)
					}
					var state struct{ Finals int }
					_ = json.NewDecoder(response.Body).Decode(&state)
					response.Body.Close()
					if state.Finals != 1 {
						t.Fatal("real blackboard read did not finalize")
					}
				}
			}
			if strings.HasSuffix(tc.name, "_during_receipt") {
				completed := make(chan error, 1)
				go func() {
					response, err := http.Post(cfg["url"]+"/test/process_cut", "application/json", nil)
					if err == nil {
						response.Body.Close()
						if response.StatusCode != 200 {
							err = fmt.Errorf("process status %d", response.StatusCode)
						}
					}
					completed <- err
				}()
				// The board pauses an unchanged, real MCP receipt response; no decision
				// or proof lookup is mocked. Revoke after the outer proof read.
				deadline := time.Now().Add(15 * time.Second)
				for {
					response, err := http.Get(cfg["url"] + "/test/state")
					if err != nil {
						t.Fatal(err)
					}
					var state struct{ EvidenceHeld bool }
					_ = json.NewDecoder(response.Body).Decode(&state)
					response.Body.Close()
					if state.EvidenceHeld {
						break
					}
					if time.Now().After(deadline) {
						mutate("cut_resume_evidence")
						t.Fatal("receipt was not held")
					}
				}
				action := "cut_revoke_grant"
				if tc.name == "main_ended_during_receipt" {
					action = "session_end"
				}
				if tc.name == "link_changed_during_receipt" {
					action = "cut_link_changed"
				}
				mutate(action)
				mutate("cut_resume_evidence")
				if err := <-completed; err != nil {
					t.Fatal(err)
				}
			}
			// A terminal result never retries and never raises repeated questions.
			if code, out = call("/test/process_cut", "", nil); code != 200 {
				t.Fatalf("second pass: %d %+v", code, out)
			}
			response, err := http.Get(cfg["url"] + "/test/state")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var state struct {
				ExecutionStatus, ExecutionCause, BindingStatus, Stage, ObservationCause string
				FailureWaiterWoken                                                      bool
				FailureQuestions, CutCloseCalls, Finals                                 int
				CutVerified                                                             bool
			}
			if err = json.NewDecoder(response.Body).Decode(&state); err != nil {
				t.Fatal(err)
			}
			wantStatus := "executed"
			wantQuestions := 0
			if tc.cause != "" {
				wantStatus = "failed"
				wantQuestions = 1
				if !state.FailureWaiterWoken {
					t.Fatal("failure did not wake Main waiter")
				}
			}
			if state.ExecutionStatus != wantStatus || state.ExecutionCause != tc.cause || state.BindingStatus != "committed" || state.FailureQuestions != wantQuestions || state.CutCloseCalls != tc.calls || state.Finals != tc.finals {
				t.Fatalf("cut outcome: %+v; want status=%s cause=%s questions=%d calls=%d finals=%d", state, wantStatus, tc.cause, wantQuestions, tc.calls, tc.finals)
			}
			if tc.finals == 1 && !state.CutVerified {
				t.Fatal("receipt did not freeze exact pre-cut snapshot")
			}
			if tc.name == "already_closed" && state.ObservationCause != "link_after_cut" {
				t.Fatalf("typed evidence cause lost: %+v", state)
			}
			if (tc.name == "legacy_receipt" || tc.name == "already_closed") && state.Stage != "Evidence incomplete" {
				t.Fatalf("unverifiable cut presented as open: %+v", state)
			}
		})
	}
}

// Read reconciliation is a bounded evidence check, not a board read per slice.
// This uses real HTTP, runner/board processes and PostgreSQL, including concurrent reads.
func TestBoundCutReadReconciliationProcess(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	board := startCutBoardProcess(t, buildCutBoardBinary(t), "read_throttle")
	path := filepath.Join(t.TempDir(), "board.json")
	raw, _ := json.Marshal(board)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := startBoundProcess(t, []string{"BLERG_BOUND_SUBJECT=cut", "BLERG_BOUND_CUT_BOARD_CONFIG=" + path})
	request := func(method, path, token string, body any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, cfg["url"]+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var result map[string]any
		_ = json.NewDecoder(response.Body).Decode(&result)
		if response.StatusCode != 200 && response.StatusCode != 201 {
			t.Fatalf("%s %s: %d %+v", method, path, response.StatusCode, result)
		}
		return result
	}
	mutate := func(action string) {
		t.Helper()
		request("POST", "/test/mutate", "", map[string]string{"action": action})
	}
	subject := map[string]any{"kind": "cut", "blackboard_id": cfg["blackboard"], "milestone_id": "cut-milestone", "revision": 0, "cut": map[string]any{"board_id": cfg["cut_board"], "release_id": cfg["cut_release"], "next_release_id": cfg["cut_next"], "link": true}}
	ask := request("POST", "/api/messages", cfg["session_token"], map[string]any{"kind": "ask", "subject": subject})
	request("POST", "/api/messages/"+ask["id"].(string)+"/reply", cfg["human_token"], map[string]string{"answer": "yes"})
	mutate("cut_delay")
	counts := func() int {
		t.Helper()
		state, err := readCutBoardState(t.Context(), board)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err = json.Unmarshal(state["evidence_calls"], &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := counts()
	base := "/api/blackboards/" + cfg["blackboard"]
	read := func(path string) { request("GET", base+path+"?session="+cfg["session"], cfg["human_token"], nil) }
	t.Run("pagination_never_reconciles", func(t *testing.T) {
		read("/slice")
		read("/milestones/cut-milestone/slice")
		if counts() != before {
			t.Fatal("pagination triggered evidence reads")
		}
	})
	t.Run("read_throttle", func(t *testing.T) {
		read("")
		after := counts()
		if after != before+1 {
			t.Fatalf("first check: %d -> %d", before, after)
		}
		read("")
		read("")
		if counts() != after {
			t.Fatal("repeated read bypassed interval")
		}
		mutate("cut_observation_due")
		done := make(chan error, 8)
		for range 8 {
			go func() {
				req, _ := http.NewRequest("GET", cfg["url"]+base+"?session="+cfg["session"], nil)
				req.Header.Set("Authorization", "Bearer "+cfg["human_token"])
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode != 200 {
						err = fmt.Errorf("read status %d", resp.StatusCode)
					}
				}
				done <- err
			}()
		}
		for range 8 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		if counts() != after+1 {
			t.Fatal("read did not resume after interval")
		}
	})
	t.Run("legacy_stops_polling", func(t *testing.T) {
		mutate("cut_inject_evidence_failure")
		mutate("cut_legacy_cut")
		mutate("cut_observation_due")
		request("POST", "/test/process_cut", "", nil)
		state := request("GET", "/test/state", "", nil)
		if state["ObservationCause"] == "released_without_receipt" {
			t.Fatal("stopped reconciliation before evidence status persisted")
		}
		mutate("cut_remove_evidence_failure")
		mutate("cut_observation_due")
		request("POST", "/test/process_cut", "", nil)
		state = request("GET", "/test/state", "", nil)
		if state["Stage"] != "Evidence incomplete" {
			t.Fatal("evidence status did not recover")
		}
		n := counts()
		mutate("cut_observation_due")
		request("POST", "/test/process_cut", "", nil)
		read("")
		if counts() != n {
			t.Fatal("legacy release without a receipt was polled again")
		}
	})
}
