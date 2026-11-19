package server

import (
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"testing"
	"time"
)

func TestBoardWorkerIdleNeedsRecentTurnOrMainQuestion(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelRead)
	ctx := t.Context()
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	if _, err := fx.pool.Exec(ctx, `INSERT INTO session_card_links VALUES($1,'board-a',585,'pin',now())`, fx.sess); err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		got, err := fx.api.boardCardLiveWorker(ctx, "board-a", 585, topicOwner, fx.ended, now)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("live=%t want %t", got, want)
		}
	}
	for _, status := range []string{"starting", "running", "waiting"} {
		if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status=$2,ended_at=NULL WHERE id=$1`, fx.sess, status); err != nil {
			t.Fatal(err)
		}
		check(true)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status='idle',parent_session_id=$2 WHERE id=$1`, fx.sess, fx.ended); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := fx.pool.Exec(ctx, `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts) VALUES($1,100000,$2,'turn_done','{}',$3)`, fx.sess, newUUID(), now.Add(-5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := fx.pool.Exec(ctx, `UPDATE agent_events SET ts=$2 WHERE session_id=$1 AND kind='turn_done'`, fx.sess, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := fx.pool.Exec(ctx, `UPDATE agent_events SET ts=$2 WHERE session_id=$1 AND kind='turn_done'`, fx.sess, now.Add(-5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO messages(session_id,kind,body,status) VALUES($1,'ask','Need Main decision','open')`, fx.sess); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := fx.pool.Exec(ctx, `UPDATE messages SET person_required=true WHERE session_id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestBoardWorkerStartingWithFullKeyTitle(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelRead)
	ctx := t.Context()
	// No pin or numeric title link exists yet while this worker starts.
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"Fix ENG-589", true}, {"Fix #589", true},
		{"Fix OTHER-589 SHA-256 UTF-8 RFC-9207 ISO-8601", false},
	} {
		if _, err := fx.pool.Exec(ctx, `DELETE FROM session_card_links WHERE session_id=$1`, fx.ended); err != nil {
			t.Fatal(err)
		}
		if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status='starting',ended_at=NULL,title=$2,parent_session_id=$3 WHERE id=$1`, fx.ended, tc.title, fx.sess); err != nil {
			t.Fatal(err)
		}
		got, err := fx.api.boardCardLiveWorker(ctx, "board-a", 589, topicOwner, fx.sess, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%q: live=%t want %t", tc.title, got, tc.want)
		}
	}
}
