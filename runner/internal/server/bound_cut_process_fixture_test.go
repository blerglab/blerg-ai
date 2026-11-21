package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
)

func startCutBoardProcess(t *testing.T, binary, scenario string) map[string]string {
	t.Helper()
	ready := testsupport.Phase(t, "board_process_ready")
	defer ready.End()
	command := exec.Command(binary, "-test.run=^TestBoundCutBoardProcess$")
	command.Env = append(os.Environ(), "BLERG_CUT_BOARD_PROCESS=1", "BLERG_CUT_OWNER="+topicOwner, "BLERG_CUT_LOGIN="+testSID)
	if scenario == "archive_gate_deny" {
		command.Env = append(command.Env, "BLERG_CUT_BOARD_GATE=deny")
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			stopPhase := testsupport.Phase(t, "board_process_stop")
			defer stopPhase.End()
			_ = stdin.Close()
			if err := command.Wait(); err != nil {
				t.Errorf("real board process: %v; %s", err, diagnostics.String())
			}
		})
	})
	var cfg map[string]string
	if err = json.NewDecoder(bufio.NewReader(stdout)).Decode(&cfg); err != nil {
		t.Fatalf("real board readiness: %v; %s", err, diagnostics.String())
	}
	return cfg
}
func installCutBoardFixture(t *testing.T, fx *topicFx, record *blackboard.Record) map[string]string {
	t.Helper()
	var cfg map[string]string
	raw, err := os.ReadFile(os.Getenv("BLERG_BOUND_CUT_BOARD_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	installCutCoreFixture(t, fx)
	gateway := mcpgw.New(mcpgw.Config{DB: fx.pool, Board: &mcpgw.HTTPBoardExchanger{BaseURL: cfg["url"], InternalKey: "cut-fixture-key"}, BoardMCPURL: cfg["url"] + "/mcp", Policy: netguard.Policy{AllowHTTPHosts: []string{"127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}}})
	fx.api.hub.mcpStart.set(&mcpStartConfig{Board: gateway})
	if _, err = db.InsertProposalGrant(t.Context(), fx.pool, db.ProposalGrant{SessionID: fx.sess, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: cfg["board_id"], BoardName: "Cut fixture", ProofSessionID: testSID, CreatedVia: "panel", Level: db.BoardLevelFull}); err != nil {
		t.Fatal(err)
	}
	decision := blackboard.Event{ID: blackboard.NewID(), MilestoneID: "cut-milestone", Kind: "decision", Source: "person", AuthenticatedActor: record.Owner, RecordedAt: time.Now(), Payload: json.RawMessage(`{"reason":"Preserve this decision"}`)}
	record.Events = []blackboard.Event{decision}
	record.Revisions = []blackboard.Revision{{MilestoneID: "cut-milestone", Number: 0, EventID: decision.ID, Items: []blackboard.Item{}}}
	record.Snapshots = []blackboard.Snapshot{{MilestoneID: "cut-milestone", Day: "2026-11-01", ObservedAt: time.Now(), Complete: true, Counts: map[string]int{"next": 2}}}
	record.Milestones = []blackboard.Milestone{{ID: "cut-milestone", Owner: record.Owner, Name: "0.6.0", Version: 1}}
	return cfg
}
func mutateCutBoard(ctx context.Context, cfg map[string]string, kind string) error {
	raw, _ := json.Marshal(map[string]string{"kind": kind})
	req, err := http.NewRequestWithContext(ctx, "POST", cfg["url"]+"/fixture/mutate", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg["control_token"])
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func readCutBoardState(ctx context.Context, cfg map[string]string) (map[string]json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", cfg["url"]+"/fixture/state", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg["control_token"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var state map[string]json.RawMessage
	err = json.NewDecoder(resp.Body).Decode(&state)
	return state, err
}

// Only the external identity issuer is a fixture; executor, routing, gateway,
// board and persistence run through their real implementations.
func installCutCoreFixture(t *testing.T, fx *topicFx) {
	installCutCoreStatusFixture(t, fx, 200)
}
func installCutCoreStatusFixture(t *testing.T, fx *topicFx, status int) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/credentials/list", func(w http.ResponseWriter, r *http.Request) {
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		var p struct {
			AccountID string `json:"account_id"`
			SessionID string `json:"session_id"`
		}
		if json.NewDecoder(r.Body).Decode(&p) != nil || p.AccountID != topicOwner || p.SessionID != testSID {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"engines": []string{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	fx.api.SetCoreCredentials(srv.URL, "cut-fixture-key")
}
