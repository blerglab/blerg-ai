package server

// User uploads to a session (uploads.go): a person attaches files from the chat box, the session's
// agent lists and fetches them with its per-session token (`blerg-runner fetch`). Same fixture as
// the artifact tests.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

type upMeta struct {
	artMeta
	Origin string `json:"origin"`
}

func (fx *artFx) userUpload(tok, sess string, body []byte, name string) *httptest.ResponseRecorder {
	fx.t.Helper()
	req := httptest.NewRequest("POST", "/api/sessions/"+sess+"/uploads", bytes.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if name != "" {
		req.Header.Set("X-Artifact-Name", url.PathEscape(name))
	}
	req.Header.Set("Content-Type", "text/html") // must never be used
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec
}

func (fx *artFx) attach(sess, name string, body []byte) upMeta {
	fx.t.Helper()
	rec := fx.userUpload(fx.human(artUser), sess, body, name)
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("attach %s = %d: %s", name, rec.Code, rec.Body.String())
	}
	var m upMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		fx.t.Fatal(err)
	}
	return m
}

func TestUploadsAuth(t *testing.T) {
	fx := newArtFx(t)
	cases := []struct {
		name string
		tok  string
		sess string
		want int
	}{
		{"no token", "", fx.sess, 401},
		{"garbage", "zz", fx.sess, 401},
		{"a session messaging token is not a person", fx.sessTok, fx.sess, 401},
		{"the daemon token is not a person", "daemon-tok-1234567890", fx.sess, 401},
		{"agent token", fx.agent(artUser), fx.sess, 403},
		{"person", fx.human(artUser), fx.sess, 201},
		{"any person on a public session", fx.human(artOther), fx.sess, 201},
		{"a private session of someone else", fx.human(artUser), fx.privSess, 404},
		{"the private session's owner", fx.human(artOther), fx.privSess, 201},
		{"unknown session", fx.human(artUser), newUUID(), 404},
		{"not a uuid", fx.human(artUser), "abc", 404},
		{"ended session", fx.human(artUser), fx.endedSess, 409},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := fx.userUpload(c.tok, c.sess, []byte("hello"), "a.txt")
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

func TestUploadsHappyPathStoresUserOriginAndRecordsNoEvent(t *testing.T) {
	fx := newArtFx(t)
	body := []byte("%PDF-1.4 not really\n")
	m := fx.attach(fx.sess, "report one.pdf", body)
	if !validPublishID(m.ID) || m.Name != "report one.pdf" || m.Size != int64(len(body)) || m.Origin != "user" {
		t.Fatalf("response = %+v", m)
	}
	if m.ContentType == "text/html" || strings.Contains(m.ContentType, "html") {
		t.Errorf("content_type = %q: the uploader's Content-Type must never be used", m.ContentType)
	}
	got, err := os.ReadFile(filepath.Join(fx.path(fx.sess, m.ID), "report one.pdf"))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("stored = %q, %v", got, err)
	}
	row, err := db.GetArtifact(context.Background(), fx.api.dbPool, fx.sess, m.ID)
	if err != nil || row == nil || row.Origin != "user" || row.UploadedBy != artUser {
		t.Fatalf("row = %+v, %v", row, err)
	}
	if n := countEvents(t, fx, "artifact"); n != 0 {
		t.Errorf("%d artifact events recorded for a user upload, want 0", n)
	}
	select {
	case raw := <-fx.events:
		t.Fatalf("broadcast for a user upload: %s", raw)
	default:
	}
}

func TestUploadsNamesAndQueryName(t *testing.T) {
	fx := newArtFx(t)
	m := fx.attach(fx.sess, "../../etc/passwd", []byte("x"))
	if m.Name != "passwd" {
		t.Errorf("name = %q, want passwd", m.Name)
	}
	req := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/uploads?name="+url.QueryEscape("a/b\\c.txt"), strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer "+fx.human(artUser))
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("query name = %d %s", rec.Code, rec.Body.String())
	}
	var q upMeta
	_ = json.Unmarshal(rec.Body.Bytes(), &q)
	if strings.ContainsAny(q.Name, "/\\") || q.Name == "" {
		t.Errorf("name = %q", q.Name)
	}
}

func TestUploadsLimits(t *testing.T) {
	fx := newArtFx(t)
	ob, oc, ot := maxArtifactBytes, maxUploadsPerSession, maxUploadBytesPerSession
	maxArtifactBytes, maxUploadsPerSession, maxUploadBytesPerSession = 1024, 3, 2000
	t.Cleanup(func() { maxArtifactBytes, maxUploadsPerSession, maxUploadBytesPerSession = ob, oc, ot })
	h := fx.human(artUser)

	if rec := fx.userUpload(h, fx.sess, bytes.Repeat([]byte("a"), 1025), "big.txt"); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize = %d: %s", rec.Code, rec.Body.String())
	}
	if n, _ := db.CountArtifacts(context.Background(), fx.api.dbPool, fx.sess); n != 0 {
		t.Errorf("%d rows after a refused upload", n)
	}
	fx.attach(fx.sess, "a.txt", bytes.Repeat([]byte("a"), 1000))
	fx.attach(fx.sess, "b.txt", bytes.Repeat([]byte("a"), 1000))
	// Total bytes: 2000 used, cap 2000.
	if rec := fx.userUpload(h, fx.sess, []byte("x"), "c.txt"); rec.Code != http.StatusConflict {
		t.Fatalf("over the byte total = %d: %s", rec.Code, rec.Body.String())
	}
	ents, _ := os.ReadDir(filepath.Join(fx.dir, "artifacts", fx.sess))
	if len(ents) != 2 {
		t.Errorf("%d directories on disk, want 2 (the refused one removed)", len(ents))
	}
	// Count cap: a third tiny file would fit the bytes; the fourth is refused by count.
	if err := os.RemoveAll(filepath.Join(fx.dir, "artifacts", fx.sess)); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.api.dbPool.Exec(context.Background(), `DELETE FROM session_artifacts WHERE session_id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		fx.attach(fx.sess, "n.txt", []byte("x"))
	}
	if rec := fx.userUpload(h, fx.sess, []byte("x"), "n.txt"); rec.Code != http.StatusConflict {
		t.Fatalf("over the count = %d: %s", rec.Code, rec.Body.String())
	}
	// The caps are separate: the agent can still publish, another session is unaffected.
	if rec := fx.upload(fx.sessTok, fx.sess, []byte("x"), upOpts{name: "agent.txt"}); rec.Code != 201 {
		t.Errorf("agent publish after user cap = %d", rec.Code)
	}
	if rec := fx.userUpload(fx.human(artOther), fx.otherSess, []byte("x"), "a.txt"); rec.Code != 201 {
		t.Errorf("another session = %d", rec.Code)
	}
}

func TestAttachmentsPodRoutes(t *testing.T) {
	fx := newArtFx(t)
	body := []byte("PDF bytes <script>alert(1)</script>")
	u := fx.attach(fx.sess, "doc.pdf", body)
	ag := fx.publish(fx.sessTok, fx.sess, "agent-made.txt", []byte("agent file"))
	otherUser := fx.attach(fx.otherSess, "other.pdf", []byte("other"))
	list := "/api/sessions/" + fx.sess + "/attachments"
	file := func(sess, id string) string { return "/api/sessions/" + sess + "/attachments/" + id + "/file" }

	cases := []struct {
		name, path, tok string
		want            int
	}{
		{"list: no token", list, "", 401},
		{"list: garbage", list, "zz", 401},
		{"list: a person's browser token", list, fx.human(artUser), 401},
		{"list: another session's token", list, fx.otherTok, 403},
		{"list: own token", list, fx.sessTok, 200},
		{"list: daemon token", list, "daemon-tok-1234567890", 200},
		{"list: unknown session", "/api/sessions/" + newUUID() + "/attachments", "daemon-tok-1234567890", 404},
		{"list: not a uuid", "/api/sessions/abc/attachments", "daemon-tok-1234567890", 404},
		{"file: no token", file(fx.sess, u.ID), "", 401},
		{"file: another session's token", file(fx.sess, u.ID), fx.otherTok, 403},
		{"file: own token", file(fx.sess, u.ID), fx.sessTok, 200},
		{"file: a person's token", file(fx.sess, u.ID), fx.human(artUser), 401},
		{"file: an agent-origin file is not an attachment", file(fx.sess, ag.ID), fx.sessTok, 404},
		{"file: another session's attachment under my session", file(fx.sess, otherUser.ID), fx.sessTok, 404},
		{"file: unknown id", file(fx.sess, newPublishID()), fx.sessTok, 404},
		{"file: traversal id", file(fx.sess, "..%2F"+u.ID), fx.sessTok, 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := fx.get("GET", c.path, c.tok)
			if rec.Code != c.want {
				t.Fatalf("%s = %d, want %d: %s", c.path, rec.Code, c.want, rec.Body.String())
			}
		})
	}

	rec := fx.get("GET", list, fx.sessTok)
	var out struct {
		Attachments []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Size        int64  `json:"size"`
			ContentType string `json:"content_type"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Attachments) != 1 || out.Attachments[0].ID != u.ID || out.Attachments[0].Name != "doc.pdf" || out.Attachments[0].Size != int64(len(body)) || out.Attachments[0].ContentType == "" {
		t.Fatalf("list = %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "agent-made") {
		t.Errorf("list leaks an agent file: %s", rec.Body.String())
	}

	dl := fx.get("GET", file(fx.sess, u.ID), fx.sessTok)
	if !bytes.Equal(dl.Body.Bytes(), body) {
		t.Errorf("downloaded %q, want the uploaded bytes", dl.Body.String())
	}
	if ct := dl.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if dl.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff")
	}

	// Empty list is [], not null.
	rec = fx.get("GET", "/api/sessions/"+fx.privSess+"/attachments", fx.privTok)
	if !strings.Contains(rec.Body.String(), `"attachments":[]`) {
		t.Errorf("empty list = %s", rec.Body.String())
	}
}

func TestUploadsHumanListDeleteAndOrigin(t *testing.T) {
	fx := newArtFx(t)
	u := fx.attach(fx.sess, "mine.pdf", []byte("pdf"))
	ag := fx.publish(fx.sessTok, fx.sess, "agent.txt", []byte("a"))

	rec := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts", fx.human(artUser))
	var out struct {
		Artifacts []struct {
			ID     string `json:"id"`
			Origin string `json:"origin"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	origins := map[string]string{}
	for _, a := range out.Artifacts {
		origins[a.ID] = a.Origin
	}
	if origins[u.ID] != "user" || origins[ag.ID] != "agent" {
		t.Fatalf("origins = %v", origins)
	}

	// The in-app viewer path serves it safely too.
	if rec := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+u.ID+"/download", fx.human(artUser)); rec.Code != 200 {
		t.Errorf("download = %d", rec.Code)
	}
	if rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/artifacts/"+u.ID, fx.agent(artUser)); rec.Code != 403 {
		t.Errorf("agent delete = %d", rec.Code)
	}
	if rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/artifacts/"+u.ID, fx.human(artUser)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if _, err := os.Stat(fx.path(fx.sess, u.ID)); !os.IsNotExist(err) {
		t.Errorf("file still there: %v", err)
	}
	if rec := fx.get("GET", "/api/sessions/"+fx.sess+"/attachments/"+u.ID+"/file", fx.sessTok); rec.Code != 404 {
		t.Errorf("attachment after delete = %d", rec.Code)
	}
}

func TestUploadsCascadeWithSession(t *testing.T) {
	fx := newArtFx(t)
	u := fx.attach(fx.sess, "a.pdf", []byte("x"))
	if _, err := fx.api.dbPool.Exec(context.Background(), `DELETE FROM sessions WHERE id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetArtifact(context.Background(), fx.api.dbPool, fx.sess, u.ID); row != nil {
		t.Error("row survived its session")
	}
	if n, err := fx.api.pruneOrphanArtifacts(context.Background()); err != nil || n != 1 {
		t.Errorf("prune = %d, %v; want the orphaned directory removed", n, err)
	}
}
