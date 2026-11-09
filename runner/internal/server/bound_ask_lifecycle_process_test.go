package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A separate runner handles real authenticated replies against the same isolated
// PostgreSQL database. End must commit while the reply is blocked on messages;
// releasing that lock must never allow the invalidated decision to commit.
func TestBoundAskLifecycleRaceProcess(t *testing.T) {
	runBoundAskLifecycleRaceProcess(t, []string{"plan", "draft"})
}

func TestBoundCutLifecycleRaceProcess(t *testing.T) {
	runBoundAskLifecycleRaceProcess(t, []string{"cut"})
}

func runBoundAskLifecycleRaceProcess(t *testing.T, kinds []string) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			var cutBinary string
			if kind == "cut" {
				cutBinary = buildCutBoardBinary(t)
			}
			for _, scenario := range []string{"end", "end_resume", "ended_at_resume", "owner_transfer_back", "ended_without_epoch"} {
				t.Run(scenario, func(t *testing.T) {
					pool := runnerContractPool(t)
					u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
					if err != nil {
						t.Fatal(err)
					}
					u.Path = "/" + pool.Config().ConnConfig.Database
					env := []string{"BLERG_BOUND_SUBJECT=" + kind, "BLERG_BOUND_DATABASE=" + u.String()}
					if kind == "cut" {
						board := startCutBoardProcess(t, cutBinary, scenario)
						path := filepath.Join(t.TempDir(), "board.json")
						raw, _ := json.Marshal(board)
						if err := os.WriteFile(path, raw, 0600); err != nil {
							t.Fatal(err)
						}
						env = append(env, "BLERG_BOUND_CUT_BOARD_CONFIG="+path)
					}
					cfg, _ := startBoundProcess(t, env)
					ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
					defer cancel()
					post := func(path, token string, body any) (int, map[string]any, error) {
						raw, _ := json.Marshal(body)
						req, e := http.NewRequestWithContext(ctx, "POST", cfg["url"]+path, bytes.NewReader(raw))
						if e != nil {
							return 0, nil, e
						}
						req.Header.Set("Authorization", "Bearer "+token)
						resp, e := http.DefaultClient.Do(req)
						if e != nil {
							return 0, nil, e
						}
						defer resp.Body.Close()
						out := map[string]any{}
						_ = json.NewDecoder(resp.Body).Decode(&out)
						return resp.StatusCode, out, nil
					}
					subject := map[string]any{"kind": kind, "blackboard_id": cfg["blackboard"], "proposal_id": cfg["proposal"], "revision": 0}
					if kind == "draft" {
						subject = map[string]any{"kind": kind, "draft_id": cfg["draft"], "revision": 0}
					}
					if kind == "cut" {
						subject = map[string]any{"kind": "cut", "blackboard_id": cfg["blackboard"], "milestone_id": "cut-milestone", "revision": 0, "cut": map[string]any{"board_id": cfg["cut_board"], "release_id": cfg["cut_release"], "next_release_id": cfg["cut_next"], "link": true}}
					}
					code, out, err := post("/api/messages", cfg["session_token"], map[string]any{"kind": "ask", "body": "Exact decision", "subject": subject})
					if err != nil || code != 201 {
						t.Fatalf("create: %d %+v %v", code, out, err)
					}
					id, _ := out["id"].(string)
					blocker, err := pool.Acquire(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer blocker.Release()
					tx, err := blocker.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback(context.Background()) }()
					if _, err = tx.Exec(ctx, `LOCK TABLE messages IN ACCESS EXCLUSIVE MODE`); err != nil {
						t.Fatal(err)
					}
					if scenario == "ended_without_epoch" {
						if _, err = pool.Exec(ctx, `ALTER TABLE sessions DISABLE TRIGGER bound_ask_session_end`); err != nil {
							t.Fatal(err)
						}
					}
					type result struct {
						code int
						err  error
					}
					done := make(chan result, 1)
					go func() {
						code, _, err := post("/api/messages/"+id+"/reply", cfg["human_token"], map[string]string{"answer": "yes"})
						done <- result{code, err}
					}()
					awaitLifecycle(t, func() bool {
						var waiting bool
						if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, int(blocker.Conn().PgConn().PID())).Scan(&waiting); err != nil {
							t.Fatal(err)
						}
						return waiting
					})
					endCtx, endCancel := context.WithTimeout(ctx, 3*time.Second)
					switch scenario {
					case "ended_at_resume":
						_, err = pool.Exec(endCtx, `UPDATE sessions SET ended_at=now() WHERE id=$1`, cfg["session"])
						if err == nil {
							_, err = pool.Exec(endCtx, `UPDATE sessions SET ended_at=NULL WHERE id=$1`, cfg["session"])
						}
					case "owner_transfer_back":
						_, err = pool.Exec(endCtx, `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, cfg["session"], topicOther)
						if err == nil {
							_, err = pool.Exec(endCtx, `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, cfg["session"], topicOwner)
						}
					default:
						_, err = pool.Exec(endCtx, `UPDATE sessions SET status='ended' WHERE id=$1`, cfg["session"])
						if err == nil && scenario == "end_resume" {
							_, err = pool.Exec(endCtx, `UPDATE sessions SET status='running',ended_at=NULL WHERE id=$1`, cfg["session"])
						}
					}
					endCancel()
					if err != nil {
						t.Fatalf("session transition waited on the blocked question: %v", err)
					}
					if err = tx.Commit(ctx); err != nil {
						t.Fatal(err)
					}
					r := <-done
					if r.err != nil || r.code == 200 {
						t.Fatalf("invalidated ask committed or transport failed: %+v", r)
					}
					resp, err := http.Get(cfg["url"] + "/test/state")
					if err != nil {
						t.Fatal(err)
					}
					raw, _ := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					var state map[string]any
					if err = json.Unmarshal(raw, &state); err != nil {
						t.Fatal(err)
					}
					if state["BindingStatus"] != "canceled" || state["CanceledCause"] != "session_invalidated" || state["revision"] != float64(0) || state["items"] != float64(0) {
						t.Fatalf("unsafe final state: %s", raw)
					}
				})
			}
		})
	}
}

func TestBoundAskLifecycleReconcileRestartProcess(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	for _, kind := range []string{"plan", "draft"} {
		t.Run(kind, func(t *testing.T) {
			pool := runnerContractPool(t)
			u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			u.Path = "/" + pool.Config().ConnConfig.Database
			env := []string{"BLERG_BOUND_SUBJECT=" + kind, "BLERG_BOUND_DATABASE=" + u.String()}
			cfg, stop := startBoundProcess(t, env)
			subject := map[string]any{"kind": kind, "blackboard_id": cfg["blackboard"], "proposal_id": cfg["proposal"], "revision": 0}
			if kind == "draft" {
				subject = map[string]any{"kind": kind, "draft_id": cfg["draft"], "revision": 0}
			}
			raw, _ := json.Marshal(map[string]any{"kind": "ask", "body": "Exact decision", "subject": subject})
			req, _ := http.NewRequest("POST", cfg["url"]+"/api/messages", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+cfg["session_token"])
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != 201 {
				t.Fatalf("create: %d", resp.StatusCode)
			}
			stop()
			// Both transitions occur with the runner absent. No daemon or answer wakes it.
			if _, err = pool.Exec(t.Context(), `UPDATE sessions SET status='ended' WHERE id=$1`, cfg["session"]); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(t.Context(), `UPDATE sessions SET status='running',ended_at=NULL WHERE id=$1`, cfg["session"]); err != nil {
				t.Fatal(err)
			}
			env = append(env, "BLERG_BOUND_SESSION="+cfg["session"], "BLERG_BOUND_BLACKBOARD="+cfg["blackboard"], "BLERG_BOUND_PROPOSAL="+cfg["proposal"], "BLERG_BOUND_RECONCILE=1")
			restarted, _ := startBoundProcess(t, env)
			awaitLifecycle(t, func() bool {
				var state, cause string
				var answered bool
				if err := pool.QueryRow(t.Context(), `SELECT b.status,b.note,m.status='answered' FROM bound_asks b JOIN messages m ON m.id=b.message_id WHERE b.workspace_id=$1`, cfg["session"]).Scan(&state, &cause, &answered); err != nil {
					t.Fatal(err)
				}
				return state == "canceled" && cause == "session_invalidated" && answered
			})
			var events int
			if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='update' AND payload->>'body' LIKE '%session_invalidated%'`, cfg["session"]).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if events != 1 {
				t.Fatalf("cancellation notice count: %d", events)
			}
			// A fresh ask after resume captures the new epoch and remains usable.
			request, _ := http.NewRequest("POST", restarted["url"]+"/api/messages", bytes.NewReader(raw))
			request.Header.Set("Authorization", "Bearer "+restarted["session_token"])
			created, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			var next map[string]any
			_ = json.NewDecoder(created.Body).Decode(&next)
			_ = created.Body.Close()
			if created.StatusCode != 201 {
				t.Fatalf("fresh ask: %d %+v", created.StatusCode, next)
			}
			answer, _ := json.Marshal(map[string]string{"answer": "yes"})
			request, _ = http.NewRequest("POST", restarted["url"]+"/api/messages/"+next["id"].(string)+"/reply", bytes.NewReader(answer))
			request.Header.Set("Authorization", "Bearer "+restarted["human_token"])
			decided, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = decided.Body.Close()
			if decided.StatusCode != 200 {
				t.Fatalf("fresh epoch could not commit: %d", decided.StatusCode)
			}
		})
	}
}

// The inverse ordering must linearize too: once the answer has checked and
// locked the live session, session end cannot overtake its decision commit.
func TestBoundAskLifecycleAnswerFirstProcess(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	for _, kind := range []string{"plan", "draft"} {
		t.Run(kind, func(t *testing.T) {
			pool := runnerContractPool(t)
			u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			u.Path = "/" + pool.Config().ConnConfig.Database
			cfg, _ := startBoundProcess(t, []string{"BLERG_BOUND_SUBJECT=" + kind, "BLERG_BOUND_DATABASE=" + u.String()})
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			post := func(path, token string, body any) (int, map[string]any, error) {
				raw, _ := json.Marshal(body)
				req, e := http.NewRequestWithContext(ctx, "POST", cfg["url"]+path, bytes.NewReader(raw))
				if e != nil {
					return 0, nil, e
				}
				req.Header.Set("Authorization", "Bearer "+token)
				resp, e := http.DefaultClient.Do(req)
				if e != nil {
					return 0, nil, e
				}
				defer resp.Body.Close()
				out := map[string]any{}
				_ = json.NewDecoder(resp.Body).Decode(&out)
				return resp.StatusCode, out, nil
			}
			subject := map[string]any{"kind": kind, "blackboard_id": cfg["blackboard"], "proposal_id": cfg["proposal"], "revision": 0}
			if kind == "draft" {
				subject = map[string]any{"kind": kind, "draft_id": cfg["draft"], "revision": 0}
			}
			code, out, err := post("/api/messages", cfg["session_token"], map[string]any{"kind": "ask", "body": "Exact decision", "subject": subject})
			if err != nil || code != 201 {
				t.Fatalf("create: %d %+v %v", code, out, err)
			}
			id, _ := out["id"].(string)
			if _, err = pool.Exec(ctx, `CREATE FUNCTION pause_bound_decision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(496147); RETURN NEW; END $$;
   CREATE TRIGGER pause_bound_decision BEFORE UPDATE ON bound_asks FOR EACH ROW WHEN (NEW.status='committed') EXECUTE FUNCTION pause_bound_decision()`); err != nil {
				t.Fatal(err)
			}
			blocker, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Release()
			tx, err := blocker.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(496147)`); err != nil {
				t.Fatal(err)
			}
			type result struct {
				code int
				err  error
			}
			answer := make(chan result, 1)
			go func() {
				code, _, err := post("/api/messages/"+id+"/reply", cfg["human_token"], map[string]string{"answer": "yes"})
				answer <- result{code, err}
			}()
			awaitLifecycle(t, func() bool {
				var waiting bool
				if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, int(blocker.Conn().PgConn().PID())).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				return waiting
			})
			endConn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer endConn.Release()
			ended := make(chan error, 1)
			go func() {
				_, err := endConn.Exec(ctx, `UPDATE sessions SET status='ended' WHERE id=$1`, cfg["session"])
				ended <- err
			}()
			awaitLifecycle(t, func() bool {
				select {
				case err := <-ended:
					t.Fatalf("session end overtook the answer transaction: %v", err)
				default:
				}
				var waiting bool
				if err := pool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, int(endConn.Conn().PgConn().PID())).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				return waiting
			})
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			r := <-answer
			if r.err != nil || r.code != 200 {
				t.Fatalf("answer first: %+v", r)
			}
			if err = <-ended; err != nil {
				t.Fatal(err)
			}
			var status string
			if err = pool.QueryRow(ctx, `SELECT status FROM bound_asks WHERE message_id=$1`, id).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "committed" {
				t.Fatalf("decision lost after committing before end: %s", status)
			}
		})
	}
}
