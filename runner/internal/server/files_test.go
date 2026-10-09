package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// A session lists and removes what it published, with its own token; a person's attachment is
// listed in the slot count but is theirs alone to remove; another session's files are out of reach.
func TestSessionFilesListAndUnpublish(t *testing.T) {
	fx := newArtFx(t)
	v1 := fx.publish(fx.sessTok, fx.sess, "report.md", []byte("one"))
	v2 := fx.publish(fx.sessTok, fx.sess, "report.md", []byte("two"))
	if rec := fx.userUpload(fx.human(artUser), fx.sess, []byte("theirs"), "notes.txt"); rec.Code != http.StatusCreated {
		t.Fatalf("user upload = %d: %s", rec.Code, rec.Body.String())
	}
	other := fx.publish(fx.otherTok, fx.otherSess, "open.txt", []byte("open"))

	rec := fx.get("GET", "/api/sessions/"+fx.sess+"/files", fx.sessTok)
	if rec.Code != http.StatusOK {
		t.Fatalf("files = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Files []struct {
			ID, Name string
			Version  int `json:"version"`
			Latest   int `json:"latest_version"`
		} `json:"files"`
		Used, Limit int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Files) != 2 || out.Used != 3 || out.Limit != maxArtifactsPerSession {
		t.Fatalf("files = %+v (used %d of %d): want the two published versions, three slots used", out.Files, out.Used, out.Limit)
	}
	for _, f := range out.Files {
		if f.Name != "report.md" || f.Latest != 2 {
			t.Errorf("entry %+v: the person's attachment must not be listed, versions must say latest=2", f)
		}
	}

	// A person's token is not a session: the agent routes refuse it like fetch does.
	if rec := fx.get("GET", "/api/sessions/"+fx.sess+"/files", fx.human(artUser)); rec.Code != http.StatusUnauthorized {
		t.Errorf("person on the session files route = %d, want 401", rec.Code)
	}
	// Another session's token, this session's path: forbidden.
	if rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/files/"+v1.ID, fx.otherTok); rec.Code != http.StatusForbidden {
		t.Errorf("another session's token deleting here = %d, want 403", rec.Code)
	}
	// Another session's file under this session: not found.
	if rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/files/"+other.ID, fx.sessTok); rec.Code != http.StatusNotFound {
		t.Errorf("another session's file = %d, want 404", rec.Code)
	}

	// Unpublish the older version: row and bytes go; the newer one stays.
	if rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/files/"+v1.ID, fx.sessTok); rec.Code != http.StatusNoContent {
		t.Fatalf("unpublish = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(fx.path(fx.sess, v1.ID)); !os.IsNotExist(err) {
		t.Errorf("bytes still there: %v", err)
	}
	if row, _ := db.GetArtifact(context.Background(), fx.api.dbPool, fx.sess, v1.ID); row != nil {
		t.Error("row still there")
	}
	if row, _ := db.GetArtifact(context.Background(), fx.api.dbPool, fx.sess, v2.ID); row == nil {
		t.Error("the newer version went too")
	}

	// The person's attachment: refused with a reason, not deleted.
	rows, _ := db.ListArtifacts(context.Background(), fx.api.dbPool, fx.sess)
	var theirs string
	for _, r := range rows {
		if r.Origin == db.OriginUser {
			theirs = r.ID
		}
	}
	if theirs == "" {
		t.Fatal("no user attachment found")
	}
	rec = fx.get("DELETE", "/api/sessions/"+fx.sess+"/files/"+theirs, fx.sessTok)
	if rec.Code != http.StatusForbidden || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("unpublish of a person's attachment = %d: %s", rec.Code, rec.Body.String())
	}
	if row, _ := db.GetArtifact(context.Background(), fx.api.dbPool, fx.sess, theirs); row == nil {
		t.Error("the person's attachment was deleted")
	}
}
