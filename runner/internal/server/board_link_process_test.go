package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type linkProcessLog struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (l *linkProcessLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.data.Write(p)
}
func (l *linkProcessLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.data.String() }

type linkProcessBoard struct {
	blackboardGateway
	denied atomic.Bool
}

func (b *linkProcessBoard) CallBoard(ctx context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	if c.Proof.AccountID != topicOwner || c.Proof.SessionID == "" {
		return nil, errors.New("fixture requires owner login")
	}
	if b.denied.Load() {
		return nil, errors.New("fixture access refused")
	}
	if c.Tool == "blerg_board_get" {
		return json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": `{"id":"board-a","name":"Engineering","key":"ENG"}`}}})
	}
	return b.blackboardGateway.CallBoard(ctx, c)
}
func serveBoardLinkProcess(t *testing.T) {
	logs := &linkProcessLog{}
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { failurelog.Default = previous })
	fx, _, _ := newBoardLevelFx(t)
	if code, body := setLevel(fx, fx.token(topicOwner, "human"), "board-a", "read"); code != 200 {
		t.Fatalf("grant: %d %s", code, body)
	}
	board := &linkProcessBoard{}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	r := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}, WorkspaceID: fx.sess, Version: 1, Name: "Plan without board", Links: []blackboard.Link{{WorkspaceID: fx.sess}}}
	if err := fx.api.blackboardStore().Create(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	mux.HandleFunc("POST /api/messages", fx.api.HandlePostMessages)
	mux.HandleFunc("POST /api/messages/{id}/reply", fx.api.HandlePostMessageReply)
	mux.HandleFunc("POST /test/deny", func(w http.ResponseWriter, r *http.Request) {
		board.denied.Store(true)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /test/state", func(w http.ResponseWriter, req *http.Request) {
		stored, err := fx.api.blackboardStore().Load(req.Context(), r.ID)
		if err != nil {
			t.Fatal(err)
		}
		var indexed int
		if err = fx.pool.QueryRow(req.Context(), `SELECT count(*) FROM blackboard_links WHERE blackboard_id=$1 AND board_id='board-a'`, r.ID).Scan(&indexed); err != nil {
			t.Fatal(err)
		}
		messages, err := db.ListMessagesBySession(req.Context(), fx.pool, fx.sess)
		if err != nil {
			t.Fatal(err)
		}
		writeJSON(w, 200, map[string]any{"messages": messageInfosFor(req.Context(), fx.pool, messages, topicOwner), "record": stored, "indexed": indexed, "logs": logs.String()})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"url": srv.URL, "session": fx.sess, "session_token": fx.sessTok, "human_token": fx.token(topicOwner, "human"), "other_human_token": fx.token(topicOther, "human"), "blackboard": r.ID})
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestBoardLinkBoundAskProcess(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	binary := filepath.Join(t.TempDir(), "blerg-runner")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/runner")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI build: %v %s", err, out)
	}
	for _, scenario := range []string{"link_card_counts", "no_access", "wrong_owner", "unlink", "unlink_cards", "decline"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, _ := startBoundProcess(t, []string{"BLERG_BOUND_BOARD_LINK=1"})
			call := func(method, path, token string, body any) (int, map[string]any) {
				t.Helper()
				raw, _ := json.Marshal(body)
				req, _ := http.NewRequest(method, cfg["url"]+path, bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+token)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				out := map[string]any{}
				_ = json.NewDecoder(resp.Body).Decode(&out)
				return resp.StatusCode, out
			}
			cli := func(changes string) (string, int) {
				t.Helper()
				cmd := exec.Command(binary, "plan", "propose", "--changes", changes)
				cmd.Env = append(os.Environ(), "BLERG_RUNNER_SERVER_HTTP="+cfg["url"], "BLERG_RUNNER_SESSION_ID="+cfg["session"], "BLERG_RUNNER_SESSION_TOKEN="+cfg["session_token"], "BLERG_RUNNER_DAEMON_STATE_DIR="+t.TempDir())
				out, err := cmd.CombinedOutput()
				if err != nil {
					return string(out), 1
				}
				return string(out), 0
			}
			propose := func(operation string) map[string]any {
				t.Helper()
				changes, _ := json.Marshal([]blackboard.Change{{Operation: operation, Item: blackboard.Item{BoardID: "board-a", Reason: "Track work"}}})
				out, code := cli(string(changes))
				if code != 0 {
					t.Fatalf("propose: %s", out)
				}
				var wrapper struct {
					Data string `json:"untrusted_blackboard_data"`
				}
				if err := json.Unmarshal([]byte(out), &wrapper); err != nil {
					t.Fatal(err)
				}
				var data struct {
					Data map[string]any `json:"untrusted_data"`
				}
				_ = json.Unmarshal([]byte(wrapper.Data), &data)
				return data.Data
			}
			ask := func(proposal map[string]any) string {
				t.Helper()
				cmd := exec.Command("python3", "../../.claude/skills/session-messaging/blerg-runner", "ask", "--no-wait", "--blackboard", cfg["blackboard"], "--plan-proposal", proposal["proposal_id"].(string), "--revision", jsonNumber(proposal["revision"]), "Decide link?")
				cmd.Env = append(os.Environ(), "BLERG_RUNNER_SERVER_HTTP="+cfg["url"], "BLERG_RUNNER_SESSION_ID="+cfg["session"], "BLERG_RUNNER_SESSION_TOKEN="+cfg["session_token"], "BLERG_RUNNER_DAEMON_STATE_DIR="+t.TempDir())
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("ask CLI: %v %s", err, out)
				}
				fields := strings.Fields(string(out))
				if len(fields) < 2 {
					t.Fatalf("ask ID missing: %s", out)
				}
				_, state := call("GET", "/test/state", "", nil)
				messages := state["messages"].([]any)
				message := messages[0].(map[string]any)
				body := message["body"].(string)
				if !strings.Contains(body, "Engineering (ENG)") && !strings.Contains(body, "ENG-1 · Visible title") || strings.Contains(body, "board-a") || strings.Contains(body, "proposal ") || !strings.Contains(body, "Reason: Track work") {
					t.Fatalf("decision summary: %s", body)
				}
				if message["decision"] != true {
					t.Fatalf("missing decision: %+v", message)
				}
				return fields[1]
			}
			state := func() (blackboard.Record, int) {
				t.Helper()
				code, out := call("GET", "/test/state", "", nil)
				if code != 200 {
					t.Fatal(out)
				}
				raw, _ := json.Marshal(out["record"])
				var r blackboard.Record
				_ = json.Unmarshal(raw, &r)
				return r, int(out["indexed"].(float64))
			}
			out, exit := cli(`[{"operation":"add","item":{"board_id":"board-a","card_number":1,"reason":"Track work"}}]`)
			if exit != 1 || !strings.Contains(out, "link board board-a first") {
				t.Fatalf("refusal reason: %s", out)
			}
			pid := propose("link")
			id := ask(pid)
			r, indexed := state()
			if len(r.Links) != 1 || indexed != 0 {
				t.Fatal("linked before answer")
			}
			token, answer := cfg["human_token"], "yes"
			if scenario == "no_access" {
				call("POST", "/test/deny", "", nil)
			}
			if scenario == "wrong_owner" {
				token = cfg["other_human_token"]
			}
			if scenario == "decline" {
				answer = "no"
			}
			code, response := call("POST", "/api/messages/"+id+"/reply", token, map[string]string{"answer": answer})
			if scenario == "no_access" || scenario == "wrong_owner" {
				wantBody := "Not applied: board access is unavailable. Check access and try again."
				wantCause := "board_access_revoked"
				if scenario == "wrong_owner" {
					wantBody = "Not applied: sign in as the blackboard owner and answer this question."
					wantCause = "wrong_owner"
				}
				if code != 409 || response["error"] != wantBody {
					t.Fatalf("refusal: %d %+v", code, response)
				}
				_, snapshot := call("GET", "/test/state", "", nil)
				logs, _ := snapshot["logs"].(string)
				if !strings.Contains(logs, `"cause":"`+wantCause+`"`) || strings.Contains(logs, "*server.boundAskError") {
					t.Fatalf("missing typed cause: %s", logs)
				}
				r, indexed = state()
				if len(r.Links) != 1 || r.PlanRevision != 0 || indexed != 0 {
					t.Fatal("refusal mutated plan")
				}
				return
			}
			if code != 200 {
				t.Fatalf("answer: %d %+v", code, response)
			}
			if scenario == "decline" {
				r, indexed = state()
				if indexed != 0 || r.PlanRevision != 0 {
					t.Fatal("decline linked")
				}
				return
			}
			// Retrying the same authenticated answer returns its saved receipt.
			code, response = call("POST", "/api/messages/"+id+"/reply", token, map[string]string{"answer": "yes"})
			if code != 200 {
				t.Fatalf("answer replay: %d %+v", code, response)
			}
			r, indexed = state()
			if len(r.Links) != 2 || r.PlanRevision != 1 || indexed != 1 {
				t.Fatal("link not persisted")
			}
			if scenario == "unlink" {
				id = ask(propose("unlink"))
				code, response = call("POST", "/api/messages/"+id+"/reply", token, map[string]string{"answer": "yes"})
				if code != 200 {
					t.Fatalf("unlink: %d %+v", code, response)
				}
				r, indexed = state()
				if len(r.Links) != 1 || r.Links[0].WorkspaceID != cfg["session"] || indexed != 0 || r.PlanRevision != 2 {
					t.Fatal("unlink lost workspace or left index")
				}
				return
			}
			raw := map[string]any{"changes": []blackboard.Change{{Operation: "add", Item: blackboard.Item{BoardID: "board-a", CardNumber: 1, Reason: "Track work"}}}}
			code, response = call("POST", "/api/sessions/"+cfg["session"]+"/plan", cfg["session_token"], raw)
			if code != 201 {
				t.Fatalf("card: %d %+v", code, response)
			}
			id = ask(response["untrusted_data"].(map[string]any))
			code, response = call("POST", "/api/messages/"+id+"/reply", token, map[string]string{"answer": "yes"})
			if code != 200 {
				t.Fatalf("card answer: %d %+v", code, response)
			}
			if scenario == "unlink_cards" {
				out, exit = cli(`[{"operation":"unlink","item":{"board_id":"board-a","reason":"Stop tracking"}}]`)
				if exit != 1 || !strings.Contains(out, "while plan items or milestones reference it") {
					t.Fatalf("unsafe unlink: %s", out)
				}
				return
			}
			code, response = call("GET", "/api/sessions/"+cfg["session"]+"/plan", cfg["session_token"], nil)
			if code != 200 {
				t.Fatalf("counts: %d %+v", code, response)
			}
			counts := response["untrusted_data"].(map[string]any)["counts"].(map[string]any)
			if counts["all"] != float64(1) {
				t.Fatalf("card counts missing: %+v", counts)
			}
		})
	}
}
func jsonNumber(v any) string { raw, _ := json.Marshal(v); return string(raw) }
