package api_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/contracts/identity"
)

const filingIntent = "11111111-1111-4111-8111-111111111111"
const filingAccount = "22222222-2222-4222-8222-222222222222"

func filingToken(t *testing.T, boardID, intent string, draft boardfiling.Draft) string {
	t.Helper()
	capability, err := boardfiling.Capability(intent, boardfiling.Digest(boardID, draft))
	if err != nil {
		t.Fatal(err)
	}
	claims := identity.Claims{Sub: intent, Aud: "blerg-board", Kind: "agent", Project: boardID, OnBehalfOf: filingAccount, Lineage: filingAccount, Caps: []string{capability}, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(30 * time.Second).Unix()}
	hb, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": coreTestKID})
	pb, _ := json.Marshal(claims)
	input := identity.EncodeSigningInput(hb, pb)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(corePriv, []byte(input)))
}
func TestFileCardSignedIntentCreatesOnlyApprovedCard(t *testing.T) {
	srv, pool := testServer(t)
	board, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Work", Columns: []string{"Todo"}})
	if err != nil {
		t.Fatal(err)
	}
	draft := boardfiling.Draft{Title: "Report", Body: "Fixture restricted reproduction", Type: "bug", Sensitive: true, Origin: boardfiling.Origin{SessionURL: "https://runner.example.test/sessions/fixture", Repo: "fixture-repo", Commit: "abcdef1", Engine: "codex"}}
	resp := request(t, srv, http.MethodPost, "/api/boards/"+board.ID+"/file-card", filingToken(t, board.ID, filingIntent, draft), nil, draft)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("file-card=%d, want201", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestFileCardSensitiveBodyOnlyHydratesForActualAdministrators(t *testing.T) {
	srv, pool := testServer(t)
	board, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Work", Columns: []string{"Todo"}})
	if err != nil {
		t.Fatal(err)
	}
	draft := boardfiling.Draft{Title: "Public report", Body: "fixture-private-reproduction-749", Sensitive: true, Origin: boardfiling.Origin{SessionURL: "https://runner.example.test/sessions/fixture", Repo: "origin-project", Commit: "abcdef1", Engine: "codex", ComponentVersion: "test-version", ComponentCommit: "abcdef2"}}
	resp := request(t, srv, "POST", "/api/boards/"+board.ID+"/file-card", filingToken(t, board.ID, filingIntent, draft), nil, draft)
	var receipt db.FilingReceipt
	decodeBody(t, resp, &receipt)
	if receipt.ID == "" {
		t.Fatal("filing receipt missing")
	}
	for _, tok := range []string{memberToken(t), testCoreToken(t, "agent", []string{"card.read"})} {
		for _, path := range []string{"/api/cards/" + receipt.ID, "/api/boards/" + board.ID + "/cards", "/api/cards/" + receipt.ID + "/events", "/api/boards/" + board.ID + "/cards/search"} {
			resp := request(t, srv, "GET", path, tok, nil, nil)
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 200 || strings.Contains(string(raw), draft.Body) {
				t.Fatalf("sensitive data escaped read route %s (status%d)", path, resp.StatusCode)
			}
		}
	}
	for _, tok := range []string{adminToken(t), testCoreToken(t, "agent", []string{"card.read", "board.admin"})} {
		resp := request(t, srv, "GET", "/api/cards/"+receipt.ID, tok, nil, nil)
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(raw), draft.Body) {
			t.Fatal("actual board administrator could not read restricted reproduction")
		}
		if strings.Contains(string(raw), "test-version") {
			t.Fatal("component metadata bypassed per-board opt-in")
		}
	}
	resp = request(t, srv, "GET", "/api/boards/"+board.ID+"/cards/search?q="+draft.Body, memberToken(t), nil, nil)
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.Contains(string(raw), receipt.ID) {
		t.Fatal("private reproduction entered public search index")
	}
	resp = request(t, srv, "PATCH", "/api/cards/"+receipt.ID, memberToken(t), nil, map[string]string{"body": "new-private"})
	if resp.StatusCode != 403 {
		t.Fatalf("member changed sensitive body: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = request(t, srv, "PATCH", "/api/cards/"+receipt.ID, adminToken(t), nil, map[string]string{"body": "new-private"})
	if resp.StatusCode != 200 {
		t.Fatalf("admin edit: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	card, err := db.GetCard(t.Context(), pool, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !card.Sensitive || card.Body != nil {
		t.Fatal("sensitive update put reproduction back into public card row")
	}
	body, err := db.SensitiveCardBody(t.Context(), pool, receipt.ID)
	if err != nil || body == nil || *body != "new-private" {
		t.Fatal("admin edit was not stored privately")
	}
}

func TestFileCardRejectsUnapprovedMutationAndCannotUseIntentForOtherCard(t *testing.T) {
	srv, pool := testServer(t)
	b, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Work", Columns: []string{"Todo"}})
	if err != nil {
		t.Fatal(err)
	}
	draft := boardfiling.Draft{Title: "Report", Body: "Approved body", Origin: boardfiling.Origin{SessionURL: "https://runner.example.test/sessions/fixture", Engine: "codex"}}
	token := filingToken(t, b.ID, filingIntent, draft)
	for _, mutate := range []func(*boardfiling.Draft){func(d *boardfiling.Draft) { d.Title = "Different" }, func(d *boardfiling.Draft) { d.Body = "Different" }, func(d *boardfiling.Draft) { d.Sensitive = true }, func(d *boardfiling.Draft) { d.Origin.Engine = "claude" }, func(d *boardfiling.Draft) { d.Repos = []string{"injected"} }} {
		changed := draft
		mutate(&changed)
		resp := request(t, srv, "POST", "/api/boards/"+b.ID+"/file-card", token, nil, changed)
		if resp.StatusCode != 403 {
			t.Fatalf("unapproved mutation=%d", resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	for _, path := range []string{"/api/boards/" + b.ID + "/cards", "/api/boards", "/api/boards/" + b.ID + "/cards/search"} {
		resp := request(t, srv, "GET", path, token, nil, nil)
		if resp.StatusCode == 200 {
			t.Fatal("create-only credential gained a read permission")
		}
		_ = resp.Body.Close()
	}
	resp := request(t, srv, "POST", "/api/boards/"+b.ID+"/cards", token, nil, map[string]string{"title": "Unapproved"})
	if resp.StatusCode != 403 {
		t.Fatal("create-only credential used general card writes")
	}
	_ = resp.Body.Close()
	resp = request(t, srv, "POST", "/api/boards/"+b.ID+"/file-card", token, nil, draft)
	if resp.StatusCode != 201 {
		t.Fatalf("approved filing=%d", resp.StatusCode)
	}
	var first db.FilingReceipt
	decodeBody(t, resp, &first)
	resp = request(t, srv, "POST", "/api/boards/"+b.ID+"/file-card", token, nil, draft)
	if resp.StatusCode != 200 {
		t.Fatalf("safe retry=%d", resp.StatusCode)
	}
	var second db.FilingReceipt
	decodeBody(t, resp, &second)
	if first != second {
		t.Fatal("same intent created another card")
	}
	other, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Other", Columns: []string{"Todo"}})
	if err != nil {
		t.Fatal(err)
	}
	resp = request(t, srv, "POST", "/api/boards/"+other.ID+"/file-card", filingToken(t, other.ID, filingIntent, draft), nil, draft)
	if resp.StatusCode != 409 {
		t.Fatalf("consumed intent retargeted: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM cards`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("refusals/retry created %d cards", count)
	}
}

func TestFileCardConcurrentRetriesAndDeletionNeverCreateSecondCard(t *testing.T) {
	srv, pool := testServer(t)
	b, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Work", Columns: []string{"Todo"}})
	if err != nil {
		t.Fatal(err)
	}
	d := boardfiling.Draft{Title: "Report", Body: "Public body", Origin: boardfiling.Origin{SessionURL: "https://runner.example.test/sessions/fixture", Engine: "codex"}}
	token := filingToken(t, b.ID, filingIntent, d)
	raw, _ := json.Marshal(d)
	type result struct {
		status  int
		receipt db.FilingReceipt
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			req, err := http.NewRequestWithContext(t.Context(), "POST", srv.URL+"/api/boards/"+b.ID+"/file-card", bytes.NewReader(raw))
			if err != nil {
				results <- result{err: err}
				return
			}
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := srv.Client().Do(req)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer func() { _ = resp.Body.Close() }()
			var receipt db.FilingReceipt
			err = json.NewDecoder(resp.Body).Decode(&receipt)
			results <- result{resp.StatusCode, receipt, err}
		}()
	}
	a, c := <-results, <-results
	if a.err != nil || c.err != nil || (a.status != 201 && a.status != 200) || (c.status != 201 && c.status != 200) || a.receipt != c.receipt {
		t.Fatal("concurrent intent retry failed to return one stable receipt")
	}
	if a.status == c.status {
		t.Fatal("exactly one request must create, the other must replay")
	}
	if err := db.DeleteCard(t.Context(), pool, a.receipt.ID); err != nil {
		t.Fatal(err)
	}
	resp := request(t, srv, "POST", "/api/boards/"+b.ID+"/file-card", token, nil, d)
	if resp.StatusCode != 200 {
		t.Fatalf("deleted-card retry=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM cards`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("consumed intent resurrected a deleted card")
	}
}

func TestFileCardMCPKeepsDetailsRestrictedAndCreateTokenCannotRead(t *testing.T) {
	srv, pool := testServer(t)
	b, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Work", Columns: []string{"Todo"}})
	if err != nil {
		t.Fatal(err)
	}
	d := boardfiling.Draft{Title: "Report", Body: "fixture-private-mcp-reproduction", Sensitive: true, Origin: boardfiling.Origin{SessionURL: "https://runner.example.test/sessions/fixture", Engine: "codex"}}
	token := filingToken(t, b.ID, filingIntent, d)
	resp := request(t, srv, "POST", "/api/boards/"+b.ID+"/file-card", token, nil, d)
	var receipt db.FilingReceipt
	decodeBody(t, resp, &receipt)
	fx := scopeFixture{srv: srv}
	if out := fx.mcpCall(t, memberToken(t), "blerg_card_get", map[string]any{"card_id": receipt.ID}); out.IsError || strings.Contains(out.Text, d.Body) {
		t.Fatal("MCP member read leaked reproduction")
	}
	if out := fx.mcpCall(t, testCoreToken(t, "agent", []string{"card.read", "board.admin"}), "blerg_card_get", map[string]any{"card_id": receipt.ID}); out.IsError || !strings.Contains(out.Text, d.Body) {
		t.Fatal("MCP administrator could not read reproduction")
	}
	if out := fx.mcpCall(t, token, "blerg_card_get", map[string]any{"card_id": receipt.ID}); !out.IsError || strings.Contains(out.Text, d.Body) {
		t.Fatal("create-once capability gained MCP card read")
	}
	if out := fx.mcpCall(t, memberToken(t), "blerg_card_update", map[string]any{"card_id": receipt.ID, "body": "unauthorized replacement"}); !out.IsError {
		t.Fatal("MCP member edited restricted reproduction")
	}
}

func TestFileCardRepositoryChoiceAndComponentMetadataOptIn(t *testing.T) {
	srv, pool := testServer(t)
	yes := true
	b, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Multi repo", Columns: []string{"Todo"}, Repos: []string{"target-one", "target-two"}, IncludeComponentVersion: &yes})
	if err != nil {
		t.Fatal(err)
	}
	d := boardfiling.Draft{Title: "Report", Body: "Public", Origin: boardfiling.Origin{SessionURL: "https://runner.example.test/sessions/fixture", Repo: "different-origin", Engine: "codex", ComponentVersion: "test-version", ComponentCommit: "abcdef1"}}
	resp := request(t, srv, "POST", "/api/boards/"+b.ID+"/file-card", filingToken(t, b.ID, filingIntent, d), nil, d)
	if resp.StatusCode != 422 {
		t.Fatalf("ambiguous target repo=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	var consumed int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM filed_card_intents`).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != 0 {
		t.Fatal("invalid repository choice consumed approval")
	}
	d.Repos = []string{"target-two"}
	resp = request(t, srv, "POST", "/api/boards/"+b.ID+"/file-card", filingToken(t, b.ID, filingIntent, d), nil, d)
	if resp.StatusCode != 201 {
		t.Fatalf("explicit target repo=%d", resp.StatusCode)
	}
	var receipt db.FilingReceipt
	decodeBody(t, resp, &receipt)
	card, err := db.GetCard(t.Context(), pool, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(card.Repos) != 1 || card.Repos[0] != "target-two" || !strings.Contains(string(card.Origin), "test-version") || !strings.Contains(string(card.Origin), "different-origin") {
		t.Fatal("target repo, provenance, or opted-in component metadata changed")
	}
	single, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Single repo", Columns: []string{"Todo"}, Repos: []string{"sole-target"}})
	if err != nil {
		t.Fatal(err)
	}
	d.Repos = nil
	resp = request(t, srv, "POST", "/api/boards/"+single.ID+"/file-card", filingToken(t, single.ID, "33333333-3333-4333-8333-333333333333", d), nil, d)
	if resp.StatusCode != 201 {
		t.Fatalf("single default=%d", resp.StatusCode)
	}
	decodeBody(t, resp, &receipt)
	card, err = db.GetCard(t.Context(), pool, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(card.Repos) != 1 || card.Repos[0] != "sole-target" {
		t.Fatal("single board default used originating repo")
	}
}

func TestFileCardRequiresNarrowCoreAgentAndMatchingAccountBindings(t *testing.T) {
	srv, pool := testServer(t)
	b, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Work", Columns: []string{"Todo"}})
	if err != nil {
		t.Fatal(err)
	}
	d := boardfiling.Draft{Title: "Report", Origin: boardfiling.Origin{SessionURL: "https://runner.example.test/sessions/fixture", Engine: "codex"}}
	capability, err := boardfiling.Capability(filingIntent, boardfiling.Digest(b.ID, d))
	if err != nil {
		t.Fatal(err)
	}
	base := identity.Claims{Sub: filingIntent, Aud: "blerg-board", Kind: "agent", Project: b.ID, OnBehalfOf: filingAccount, Lineage: filingAccount, Caps: []string{capability}, ExpiresAt: time.Now().Add(30 * time.Second).Unix()}
	for name, mutate := range map[string]func(*identity.Claims){"human": func(c *identity.Claims) { c.Kind = "human" }, "service": func(c *identity.Claims) { c.Kind = "service" }, "broad-caps": func(c *identity.Claims) { c.Caps = []string{capability, "card.read"} }, "wrong-subject": func(c *identity.Claims) { c.Sub = "33333333-3333-4333-8333-333333333333" }, "wrong-lineage": func(c *identity.Claims) { c.Lineage = "33333333-3333-4333-8333-333333333333" }, "no-account": func(c *identity.Claims) { c.OnBehalfOf = "" }} {
		t.Run(name, func(t *testing.T) {
			c := base
			mutate(&c)
			hb, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": coreTestKID})
			pb, _ := json.Marshal(c)
			input := identity.EncodeSigningInput(hb, pb)
			token := input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(corePriv, []byte(input)))
			resp := request(t, srv, "POST", "/api/boards/"+b.ID+"/file-card", token, nil, d)
			if resp.StatusCode != 403 {
				t.Fatalf("invalid principal binding=%d", resp.StatusCode)
			}
			_ = resp.Body.Close()
		})
	}
	for _, tok := range []string{"svc-key-123", memberToken(t), adminToken(t)} {
		resp := request(t, srv, "POST", "/api/boards/"+b.ID+"/file-card", tok, nil, d)
		if resp.StatusCode != 403 {
			t.Fatal("ordinary credential entered signed approval route")
		}
		_ = resp.Body.Close()
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM cards`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("refused signed approval created a card")
	}
}

func TestFileCardSensitiveBodyCannotReachCuratorOrPublicRow(t *testing.T) {
	srv, pool := testServer(t)
	yes := true
	b, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Gated", Columns: []string{"Todo"}, GateEnabled: &yes})
	if err != nil {
		t.Fatal(err)
	}
	d := boardfiling.Draft{Title: "Report", Body: "fixture-private-original", Sensitive: true, Origin: boardfiling.Origin{SessionURL: "https://runner.example.test/sessions/fixture", Engine: "codex"}}
	resp := request(t, srv, "POST", "/api/boards/"+b.ID+"/file-card", filingToken(t, b.ID, filingIntent, d), nil, d)
	var receipt db.FilingReceipt
	decodeBody(t, resp, &receipt)
	privateEdit := "fixture-private-edited-body"
	adminAgent := testCoreToken(t, "agent", []string{"card.read", "card.write", "board.admin"})
	resp = request(t, srv, "PATCH", "/api/cards/"+receipt.ID, adminAgent, nil, map[string]string{"body": privateEdit})
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.Contains(string(raw), privateEdit) {
		t.Fatal("curator response leaked private edit")
	}
	var reviews string
	if err := pool.QueryRow(t.Context(), `SELECT COALESCE(string_agg(payload::text,' '),'') FROM admission_reviews`).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reviews, privateEdit) || strings.Contains(reviews, d.Body) {
		t.Fatal("sensitive body entered curator history")
	}
	if _, err := pool.Exec(t.Context(), `UPDATE cards SET body=$2 WHERE id=$1`, receipt.ID, "must-never-be-public"); err == nil {
		t.Fatal("database allowed sensitive body in public row")
	}
}
