package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

const metadataBoard = "11111111-1111-4111-8111-111111111111"

type metadataBoards struct{ filingBoardList }

func (*metadataBoards) ListSessionBoards(context.Context, mcpgw.Proof) ([]SessionBoard, error) {
	return []SessionBoard{{ID: metadataBoard, Name: "A"}}, nil
}

type metadataFixture struct {
	*fixtureFiling
	calls       int
	patch       boardfiling.MetadataPatch
	proof       mcpgw.Proof
	destination string
}

func (f *metadataFixture) SnapshotURL() string {
	if f.destination != "" {
		return f.destination
	}
	return f.fixtureFiling.SnapshotURL()
}
func (f *metadataFixture) UpdateMetadata(_ context.Context, p mcpgw.Proof, _ string, d boardfiling.MetadataPatch) (json.RawMessage, error) {
	f.calls++
	f.patch = d
	f.proof = p
	return json.RawMessage(`{"status":"updated"}`), nil
}
func TestBoardMetadataProposalQueuesWithoutWriting(t *testing.T) {
	fx, up := newFilingFx(t)
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &metadataBoards{}})
	updater := &metadataFixture{fixtureFiling: up}
	fx.api.boardFiling = updater
	human := fx.token(topicOwner, "human")
	if code, b := setLevel(fx, human, metadataBoard, "read"); code != 200 {
		t.Fatalf("access=%d %s", code, b)
	}
	bad := filingRequest(fx, http.MethodPost, "/api/sessions/"+fx.sess+"/board-metadata", fx.sessTok, map[string]any{"board": metadataBoard, "run_runtime": "host"})
	if bad.Code != 400 {
		t.Fatalf("execution setting accepted=%d", bad.Code)
	}
	rec := filingRequest(fx, http.MethodPost, "/api/sessions/"+fx.sess+"/board-metadata", fx.sessTok, map[string]any{"board": metadataBoard, "name": "Renamed", "description": "", "key": "NEW", "repos": []string{"team/app"}, "require_repo": false, "columns": []map[string]any{{"name": "Review"}}, "tags": []map[string]any{{"from": "old", "to": "new"}}})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("propose=%d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID string `json:"proposal_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	p, err := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, out.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/filing-mcp", nil)
	req.SetPathValue("id", fx.sess)
	req.Header.Set("Authorization", "Bearer "+fx.sessTok)
	result, _ := json.Marshal(fx.api.boardToolResult(req, json.RawMessage(`{"tool":"propose_metadata","board":"`+metadataBoard+`","arguments":{"name":"Renamed","description":"","key":"NEW","repos":["team/app"],"require_repo":false,"columns":[{"name":"Review"}],"tags":[{"from":"old","to":"new"}]}}`)))
	if !strings.Contains(string(result), p.ID) || strings.Contains(string(result), `"isError":true`) {
		t.Fatalf("MCP did not reuse proposal: %s", result)
	}
	if viewOfProposal(p).Kind != "board_metadata" || proposalEventKind(p) != "board_metadata" {
		t.Fatal("metadata proposal is mislabeled")
	}
	if updater.calls != 0 {
		t.Fatal("changed before approval")
	}
	updater.destination = "https://other-board.example.test"
	if got := approveBoard(fx, human, p, nil); got.Code != 409 {
		t.Fatalf("destination change=%d", got.Code)
	}
	retry := filingRequest(fx, http.MethodPost, "/api/sessions/"+fx.sess+"/board-metadata", fx.sessTok, map[string]any{"board": metadataBoard, "name": "Renamed", "description": "", "key": "NEW", "repos": []string{"team/app"}, "require_repo": false, "columns": []map[string]any{{"name": "Review"}}, "tags": []map[string]any{{"from": "old", "to": "new"}}})
	var retargeted struct {
		ID string `json:"proposal_id"`
	}
	if retry.Code != 202 || json.Unmarshal(retry.Body.Bytes(), &retargeted) != nil || retargeted.ID == p.ID {
		t.Fatal("new destination reused old approval")
	}
	updater.destination = ""
	if got := approveBoard(fx, fx.sessTok, p, nil); got.Code < 400 {
		t.Fatal("session approved itself")
	}
	if got := approveBoard(fx, human, p, map[string]any{"allow_for_session": true}); got.Code != 422 {
		t.Fatalf("allow=%d %s", got.Code, got.Body.String())
	}
	if got := approveBoard(fx, human, p, map[string]any{"expected_arguments_raw": "stale"}); got.Code != 409 {
		t.Fatalf("stale=%d", got.Code)
	}
	if code, _ := setLevel(fx, human, metadataBoard, "propose"); code != 200 {
		t.Fatal(code)
	}
	if got := approveBoard(fx, human, p, nil); got.Code != 403 {
		t.Fatalf("revoked=%d %s", got.Code, got.Body.String())
	}
	if code, _ := setLevel(fx, human, metadataBoard, "read"); code != 200 {
		t.Fatal(code)
	}
	if got := approveBoard(fx, human, p, nil); got.Code != 200 {
		t.Fatalf("approval=%d %s", got.Code, got.Body.String())
	}
	if updater.calls != 1 || updater.proof.SessionID == "" || updater.patch.Name == nil || *updater.patch.Name != "Renamed" || updater.patch.Description == nil || *updater.patch.Description != "" {
		t.Fatal("approved patch or proof lost")
	}
	if updater.patch.Repos == nil || len(*updater.patch.Repos) != 1 || updater.patch.RequireRepo == nil || *updater.patch.RequireRepo || len(updater.patch.Columns) != 1 || len(updater.patch.Tags) != 1 {
		t.Fatal("configuration fields lost")
	}
	if got := approveBoard(fx, human, p, nil); got.Code != 409 || updater.calls != 1 {
		t.Fatalf("replay=%d calls=%d", got.Code, updater.calls)
	}
}

func TestMetadataHTTPBindsPatchAndRefusesUnconfirmedReceipt(t *testing.T) {
	name := "Renamed"
	d := boardfiling.MetadataPatch{BoardID: metadataBoard, Name: &name}
	for _, mode := range []string{"ok", "wrong-receipt", "unknown", "refused"} {
		t.Run(mode, func(t *testing.T) {
			board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/boards/"+metadataBoard+"/metadata-approved" || r.Header.Get("Authorization") != "Bearer fixture-approved" {
					t.Error("wrong board request")
				}
				var patch boardfiling.MetadataPatch
				if json.NewDecoder(r.Body).Decode(&patch) != nil || boardfiling.MetadataDigest(patch) != boardfiling.MetadataDigest(d) {
					t.Error("patch changed")
				}
				switch mode {
				case "wrong-receipt":
					_, _ = w.Write([]byte(`{}`))
					return
				case "unknown":
					w.WriteHeader(500)
					return
				}
				_, _ = w.Write([]byte(`{"board_id":"` + metadataBoard + `","status":"updated"}`))
			}))
			defer board.Close()
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var got map[string]string
				if json.NewDecoder(r.Body).Decode(&got) != nil || r.URL.Path != "/internal/tokens/exchange/board-metadata" || r.Header.Get("X-Internal-Key") != "fixture-internal" || got["board_id"] != metadataBoard || got["digest"] != boardfiling.MetadataDigest(d) || got["session_id"] != filingHTTPProof.SessionID {
					t.Error("wrong core approval")
				}
				if mode == "refused" {
					w.WriteHeader(403)
					return
				}
				_, _ = w.Write([]byte(`{"token":"fixture-approved"}`))
			}))
			defer core.Close()
			up := &httpBoardFiling{base: board.URL, core: &HTTPMCPCoreClient{BaseURL: core.URL, InternalKey: "fixture-internal", HTTP: core.Client()}}
			_, err := up.UpdateMetadata(t.Context(), filingHTTPProof, metadataBoard, d)
			switch mode {
			case "ok":
				if err != nil {
					t.Fatal(err)
				}
			case "wrong-receipt", "unknown":
				if !errors.Is(err, mcpgw.ErrOutcomeUnknown) {
					t.Fatalf("outcome=%v", err)
				}
			case "refused":
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Status != 403 {
					t.Fatalf("refusal=%v", err)
				}
			}
		})
	}
}
