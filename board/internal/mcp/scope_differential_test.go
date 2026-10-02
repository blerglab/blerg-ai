package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Differential scope test. A project-scoped token for board A must never read
// or change anything on board B, whatever the ENCODING of the tool arguments.
// The historical hole: the scope check and the dispatch decoded the same bytes
// differently (case-insensitive struct decode, last key wins, a map round trip
// that re-sorted keys), so the check saw board A's id while the tool used
// board B's. This drives EVERY tool in the catalog with adversarial encodings
// that pair an in-scope value with a foreign one under the same (folded) key.
//
// Invariant per call: the answer never carries a board-B identifier, board B is
// byte-for-byte unchanged afterwards, and a call whose arguments contain the
// same key twice (exact, case-variant or unicode-escaped) is refused.

type diffWorld struct {
	pool   *pgxpool.Pool
	srv    *Server
	scoped auth.Principal
	ids    map[string]map[string]string // "A"/"B" -> board_id/card_id/column_id/review_id
	bMarks []string                     // substrings that must never appear in an answer
}

func newDiffWorld(t *testing.T) diffWorld {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `TRUNCATE boards, tokens, admission_reviews, runner_capacity RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	f := false
	w := diffWorld{pool: pool, ids: map[string]map[string]string{}}
	for _, side := range []string{"A", "B"} {
		b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "board-" + side, RequireRepo: &f})
		if err != nil {
			t.Fatal(err)
		}
		col, err := db.CreateColumn(ctx, pool, b.ID, "Inbox-"+side, false)
		if err != nil {
			t.Fatal(err)
		}
		title := "zebra in " + side
		res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &col.ID}, db.EventMeta{Actor: "service"})
		if err != nil {
			t.Fatal(err)
		}
		var rev string
		if err := pool.QueryRow(ctx, `INSERT INTO admission_reviews (board_id, operation, payload_hash, payload, policy_applied)
			VALUES ($1,'create','\x00','{}','ungated') RETURNING id`, b.ID).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		w.ids[side] = map[string]string{"board_id": b.ID, "card_id": res.Card.ID, "column_id": col.ID, "review_id": rev}
	}
	for _, v := range w.ids["B"] {
		w.bMarks = append(w.bMarks, v)
	}
	w.bMarks = append(w.bMarks, "zebra in B", "board-B", "Inbox-B")
	a := w.ids["A"]["board_id"]
	w.scoped = auth.Principal{Kind: auth.KindAgent, FromCore: true, Token: &db.Token{
		ID: "core:diff", Kind: "agent", BoardID: &a,
		Capabilities: []string{"card.read", "card.write", "column.write"},
	}}
	w.srv = New(api.New(pool, auth.New(pool, "svc-key-123"), nil))
	return w
}

// stateB is everything on board B a write could change, as one string.
func (w diffWorld) stateB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var out strings.Builder
	rows, err := w.pool.Query(ctx, `SELECT 'board', name, updated_at::text, '' FROM boards WHERE id = $1
		UNION ALL SELECT 'card', title, version::text, coalesce(archived_at::text,'') || coalesce(column_id::text,'') FROM cards WHERE board_id = $1
		UNION ALL SELECT 'column', name, rank, '' FROM board_columns WHERE board_id = $1
		UNION ALL SELECT 'event', type, id::text, '' FROM card_events WHERE card_id IN (SELECT id FROM cards WHERE board_id = $1)
		ORDER BY 1, 2, 3`, w.ids["B"]["board_id"])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c, d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "%s|%s|%s|%s\n", a, b, c, d)
	}
	return out.String()
}

func jstr(s string) string { b, _ := json.Marshal(s); return string(b) }

// unicodeEscapeFirst writes the first letter of a key as \u00XX.
func unicodeEscapeFirst(k string) string { return fmt.Sprintf(`\u%04x`, k[0]) + k[1:] }

// encodings returns adversarial argument encodings for one security key K
// carrying the in-scope value a and the foreign value b, every other argument
// being in scope. dup reports whether the encoding repeats a key (and so must
// be refused outright).
func encodings(k, a, b string, rest map[string]string) (out []struct {
	raw string
	dup bool
}) {
	var restParts []string
	for kk, v := range rest {
		if kk != k {
			restParts = append(restParts, jstr(kk)+":"+jstr(v))
		}
	}
	tail := strings.Join(restParts, ",")
	with := func(parts ...string) string {
		all := append([]string{}, parts...)
		if tail != "" {
			all = append(all, tail)
		}
		return "{" + strings.Join(all, ",") + "}"
	}
	kv := func(key, v string) string { return jstr(key) + ":" + jstr(v) }
	upper := strings.ToUpper(k)
	mixed := strings.ToUpper(k[:1]) + k[1:]
	add := func(raw string, dup bool) {
		out = append(out, struct {
			raw string
			dup bool
		}{raw, dup})
	}
	// case-variant duplicates, both orders, in-scope value first and last.
	add(with(kv(k, b), kv(upper, a)), true)
	add(with(kv(upper, a), kv(k, b)), true)
	add(with(kv(k, a), kv(upper, b)), true)
	add(with(kv(mixed, b), kv(k, a)), true)
	// exact duplicates.
	add(with(kv(k, a), kv(k, b)), true)
	add(with(kv(k, b), kv(k, a)), true)
	// a unicode-escaped spelling of the same key.
	add(with(kv(k, b), jstr(k)[:1]+unicodeEscapeFirst(k)+`":`+jstr(a)), true)
	add(with(jstr(k)[:1]+unicodeEscapeFirst(k)+`":`+jstr(a), kv(k, b)), true)
	// a foreign value hidden in a string-wrapped JSON value and in a nested
	// wrapper, next to the in-scope one.
	inner := "{" + jstr(k) + ":" + jstr(b) + "}"
	add(with(kv(k, a), `"fields":`+jstr(inner)), false)
	add(with(kv(k, a), `"fields":`+jstr(jstr(inner))), false)
	add(with(kv(k, a), `"field_schema":`+jstr("["+inner+"]")), false)
	// trailing garbage after a valid object, including a second object.
	add(with(kv(k, a))+"garbage", true)
	add(with(kv(k, a))+with(kv(k, b)), true)
	// not an object at all / wrapped whole.
	add(jstr(with(kv(k, b))), true)
	// the foreign value alone (plain refusal), and the honest in-scope call.
	add(with(kv(k, b)), false)
	add(with(kv(k, a)), false)
	return out
}

func TestDifferential_EveryToolEveryEncodingScopeAndDispatchAgree(t *testing.T) {
	w := newDiffWorld(t)
	ctx := context.Background()
	extras := map[string]string{
		"title": "t", "name": "n", "text": "x", "kind": "url", "url": "https://x.example",
		"model": "claude-sonnet-5", "query": "zebra", "column_name": "Inbox-A",
	}
	before := w.stateB(t)

	tools := map[string]bool{}
	for _, d := range toolDefs {
		tools[d["name"].(string)] = true
	}
	if len(tools) < 15 {
		t.Fatalf("only %d tools in the catalog", len(tools))
	}
	calls := 0
	for name := range tools {
		for _, k := range []string{"board_id", "card_id", "column_id", "review_id"} {
			a, b := w.ids["A"][k], w.ids["B"][k]
			rest := map[string]string{}
			for kk, v := range w.ids["A"] {
				rest[kk] = v
			}
			for kk, v := range extras {
				rest[kk] = v
			}
			for _, enc := range encodings(k, a, b, rest) {
				calls++
				res, err := w.srv.dispatch(ctx, w.scoped, name, json.RawMessage(enc.raw))
				answer := fmt.Sprint(err)
				if res != nil {
					answer += toolTextOf(res)
				}
				for _, mark := range w.bMarks {
					if strings.Contains(answer, mark) {
						t.Errorf("%s key %s args %s: answer carries board B data %q: %s", name, k, enc.raw, mark, answer)
					}
				}
				if enc.dup && err == nil {
					t.Errorf("%s key %s args %s: repeated key / malformed arguments accepted", name, k, enc.raw)
				}
			}
		}
		// board + number addressing of the foreign card.
		for _, raw := range []string{
			`{"board_id":` + jstr(w.ids["B"]["board_id"]) + `,"number":1,"BOARD_ID":` + jstr(w.ids["A"]["board_id"]) + `,"text":"x","kind":"url","url":"https://x.example","title":"t","column_name":"Inbox-A"}`,
			`{"BOARD_ID":` + jstr(w.ids["A"]["board_id"]) + `,"board_id":` + jstr(w.ids["B"]["board_id"]) + `,"number":1,"text":"x","kind":"url","url":"https://x.example"}`,
			`{"board_id":` + jstr(w.ids["B"]["board_id"]) + `,"number":1,"text":"x","kind":"url","url":"https://x.example","title":"t"}`,
		} {
			calls++
			res, err := w.srv.dispatch(ctx, w.scoped, name, json.RawMessage(raw))
			answer := fmt.Sprint(err)
			if res != nil {
				answer += toolTextOf(res)
			}
			for _, mark := range w.bMarks {
				if strings.Contains(answer, mark) {
					t.Errorf("%s args %s: answer carries board B data %q", name, raw, mark)
				}
			}
		}
	}
	if after := w.stateB(t); after != before {
		t.Errorf("board B changed under a board-A token:\n--- before\n%s--- after\n%s", before, after)
	}
	t.Logf("%d tool calls across %d tools", calls, len(tools))
}

func toolTextOf(res map[string]any) string {
	b, _ := json.Marshal(res)
	return string(b)
}
