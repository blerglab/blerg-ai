package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Browser-owned real HTTP/socket fixture; draft writes use the shipped API and hub.
func TestBlackboardBrowserHTTPFixture(t *testing.T) {
	path := os.Getenv("BLERG_BLACKBOARD_FIXTURE")
	if path == "" {
		t.Skip("browser-owned fixture")
	}
	fx, _, _ := newBoardLevelFx(t)
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	mux.HandleFunc("POST /api/messages", fx.api.HandlePostMessages)
	mux.HandleFunc("POST /api/messages/{id}/reply", fx.api.HandlePostMessageReply)
	mux.HandleFunc("GET /api/messages/{id}/thread", fx.api.HandleGetQuestionThread)
	mux.HandleFunc("POST /api/messages/{id}/thread", fx.api.HandlePostQuestionThread)
	mux.HandleFunc("GET /api/messages/{id}/answer", fx.api.HandleGetMessageAnswer)
	mux.HandleFunc("GET /api/sessions/{id}/live", fx.api.HandleGetLive)
	mux.HandleFunc("/ws/browser", fx.api.hub.ServeBrowser(fx.pool, fx.api.AuthorizeBrowserWS))
	channel := newUUID()
	if err := db.InsertSession(t.Context(), fx.pool, channel, topicDaemonID, "running", "/repos/app", "app", "Channel", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET parent_session_id=$2, spawning_account_id=$3, kind='agent' WHERE id=$1`, channel, fx.sess, topicOwner); err != nil {
		t.Fatal(err)
	}
	token := fx.token(topicOwner, "human")
	done := make(chan struct{}, 1)
	loopCtx, cancelLoop := context.WithCancel(t.Context())
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); fx.api.RunBlackboardReleaseCuts(loopCtx) }()
	defer func() { cancelLoop(); <-loopDone }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Blackboard-Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/fixture/cut" && r.Header.Get("Authorization") == "Bearer "+token {
			cfg := startCutBoardProcess(t, buildCutBoardBinary(t), "browser")
			cfgPath := t.TempDir() + "/cut.json"
			raw, _ := json.Marshal(cfg)
			if err := os.WriteFile(cfgPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BLERG_BOUND_CUT_BOARD_CONFIG", cfgPath)
			actor := blackboard.Actor{Kind: "human", ID: topicOwner}
			record := blackboard.Record{ID: blackboard.NewID(), Owner: actor, WorkspaceID: fx.sess, Name: "Blerg", Version: 1}
			installCutBoardFixture(t, fx, &record)
			if err := fx.api.blackboardStore().Create(r.Context(), record); err != nil {
				t.Fatal(err)
			}
			writeJSON(w, 201, map[string]any{"blackboard_id": record.ID, "milestone_id": "cut-milestone", "revision": 0, "cut": map[string]any{"board_id": cfg["board_id"], "release_id": cfg["release_id"], "next_release_id": cfg["next_release_id"], "link": true}})
			return
		}
		if r.URL.Path == "/fixture/plan" && r.Header.Get("Authorization") == "Bearer "+token {
			actor := blackboard.Actor{Kind: "human", ID: topicOwner}
			record := blackboard.Record{ID: blackboard.NewID(), Owner: actor, WorkspaceID: fx.sess, Name: "Plan", Version: 1, Goals: []blackboard.Goal{{ID: "goal", Owner: actor, Name: "Reliable sessions", Version: 1}}, Milestones: []blackboard.Milestone{{ID: "milestone", Owner: actor, Name: "0.6.0", Version: 1}}, Thrusts: []blackboard.Thrust{{ID: "thrust", Owner: actor, Name: "Reliable sessions", GoalID: "goal", MilestoneID: "milestone", Version: 1}}}
			record.Thrusts = nil
			for i, name := range []string{"Reliable sessions", "Readable decisions", "Safe upgrades", "Useful history", "Accessible workspace"} {
				record.Thrusts = append(record.Thrusts, blackboard.Thrust{ID: fmt.Sprintf("thrust-%d", i), Owner: actor, Name: name, GoalID: "goal", MilestoneID: "milestone", Version: 1})
			}
			if err := fx.api.blackboardStore().Create(r.Context(), record); err != nil {
				http.Error(w, "fixture setup failed", 500)
				return
			}
			titles := []string{"Restore interrupted sessions", "Group plan-change decisions", "Verify upgrade backups", "Keep decision receipts", "Improve keyboard navigation", "Reconnect workers safely", "Show reasons beside cards", "Explain migration failures", "Find past scope changes", "Keep phone controls reachable", "Recover pending questions", "Name every affected card", "Check daemon compatibility", "Preserve release evidence", "Improve color-blind contrast", "Resume retained worktrees", "Show destination milestones", "Test desktop upgrades", "Link commits to decisions", "Keep dialog focus visible", "Detect disconnected workers", "Make thrust headings readable", "Document rollback steps", "Explain unplanned work", "Keep narrow panes readable", "Retain unanswered questions"}
			var changes []blackboard.Change
			for i := 1; i <= 26; i++ {
				changes = append(changes, blackboard.Change{Operation: "add", Item: blackboard.Item{Title: fmt.Sprintf("DEMO #%d · %s", i, titles[i-1]), Reason: "Bug", ThrustID: fmt.Sprintf("thrust-%d", (i-1)%5)}})
			}
			proposal, err := (blackboard.Service{Store: fx.api.blackboardStore(), BlackboardID: record.ID}).Propose(r.Context(), blackboard.Actor{Kind: "agent", ID: fx.sess}, "milestone", changes)
			if err != nil {
				http.Error(w, "fixture proposal failed", 500)
				return
			}
			writeJSON(w, 201, map[string]any{"blackboard_id": record.ID, "proposal_id": proposal, "revision": 0})
			return
		}
		if r.URL.Path == "/fixture/stop" && r.Header.Get("Authorization") == "Bearer "+token {
			writeJSON(w, 200, map[string]bool{"ok": true})
			done <- struct{}{}
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	raw, _ := json.Marshal(map[string]string{"url": server.URL, "token": token, "agentToken": fx.sessTok, "sessionId": fx.sess, "channelId": channel})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("browser fixture timed out")
	}
}
