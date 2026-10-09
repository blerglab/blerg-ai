package server

// Session files on the runner contract (runner_files.go): the agent token that started a session
// lists, views, downloads, attaches and deletes its files through the same bodies the browser
// routes use, behind authRunner + requireSessionAccess. Same fixture as the artifact tests.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// runnerFilesFx is the artifact fixture with the runner contract wired on the same mux.
func runnerFilesFx(t *testing.T) *artFx {
	t.Helper()
	fx := newArtFx(t)
	fx.api.SetRunnerKey(runnerTestKey)
	RegisterRunnerContractRoutes(fx.mux, fx.api)
	return fx
}

func (fx *artFx) runnerUpload(tok, sess string, body []byte, name string) *httptest.ResponseRecorder {
	fx.t.Helper()
	req := httptest.NewRequest("POST", "/api/runner/sessions/"+sess+"/uploads", bytes.NewReader(body))
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

type runnerFileList struct {
	Artifacts []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Origin        string `json:"origin"`
		Version       int    `json:"version"`
		LatestVersion int    `json:"latest_version"`
		CreatedAt     string `json:"created_at"`
	} `json:"artifacts"`
}

func (fx *artFx) runnerList(tok, sess string) runnerFileList {
	fx.t.Helper()
	rec := fx.get("GET", "/api/runner/sessions/"+sess+"/artifacts", tok)
	if rec.Code != http.StatusOK {
		fx.t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var out runnerFileList
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		fx.t.Fatal(err)
	}
	return out
}

// The owner's agent token sees the file the session published, gets its bytes both ways, attaches
// a file of its own that then lists with origin user, and deletes it.
func TestRunnerFilesOwnerRoundTrip(t *testing.T) {
	fx := runnerFilesFx(t)
	tok := fx.agent(artUser)
	body := []byte("# Report\n\nall good\n")
	m := fx.publish(fx.sessTok, fx.sess, "report.md", body)

	list := fx.runnerList(tok, fx.sess)
	if len(list.Artifacts) != 1 || list.Artifacts[0].ID != m.ID || list.Artifacts[0].Origin != "agent" ||
		list.Artifacts[0].LatestVersion != 1 || list.Artifacts[0].CreatedAt == "" {
		t.Fatalf("list = %+v", list.Artifacts)
	}

	// The bytes and every header exactly as the browser route serves them: the hardening
	// (server-chosen type, nosniff, sandbox CSP, no-store) is the same code.
	var rec *httptest.ResponseRecorder
	for _, leaf := range []string{"/raw", "/download"} {
		rec = fx.get("GET", "/api/runner/sessions/"+fx.sess+"/artifacts/"+m.ID+leaf, tok)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), body) {
			t.Fatalf("%s = %d %q", leaf, rec.Code, rec.Body.String())
		}
		browser := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+m.ID+leaf, fx.human(artUser))
		if browser.Code != http.StatusOK {
			t.Fatalf("browser %s = %d", leaf, browser.Code)
		}
		for _, h := range []string{"Content-Type", "X-Content-Type-Options", "Content-Security-Policy", "Cache-Control", "Content-Disposition"} {
			if got, want := rec.Header().Get(h), browser.Header().Get(h); got != want {
				t.Errorf("%s %s = %q, want %q (the browser route's)", leaf, h, got, want)
			}
		}
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "attachment; filename*=UTF-8''report.md" {
		t.Errorf("download Content-Disposition = %q", cd)
	}

	// Attach: origin user, the app vouching for the person.
	rec = fx.runnerUpload(tok, fx.sess, []byte("notes"), "notes.txt")
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d: %s", rec.Code, rec.Body.String())
	}
	var up upMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	if up.Origin != "user" || up.Name != "notes.txt" || up.ContentType == "text/html" {
		t.Fatalf("upload reply = %+v", up)
	}
	row, err := db.GetArtifact(t.Context(), fx.api.dbPool, fx.sess, up.ID)
	if err != nil || row == nil || row.Origin != db.OriginUser || row.UploadedBy != artUser {
		t.Fatalf("row = %+v, %v: an agent token's upload is attributed to the account it acts for", row, err)
	}
	list = fx.runnerList(tok, fx.sess)
	if len(list.Artifacts) != 2 || list.Artifacts[0].ID != up.ID || list.Artifacts[0].Origin != "user" {
		t.Fatalf("list after upload = %+v (newest first)", list.Artifacts)
	}

	// Delete: row and bytes gone.
	rec = fx.get("DELETE", "/api/runner/sessions/"+fx.sess+"/artifacts/"+up.ID, tok)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if row, _ := db.GetArtifact(t.Context(), fx.api.dbPool, fx.sess, up.ID); row != nil {
		t.Error("row still present after delete")
	}
	if _, err := os.Stat(fx.path(fx.sess, up.ID)); !os.IsNotExist(err) {
		t.Errorf("files still present after delete: %v", err)
	}
	if list = fx.runnerList(tok, fx.sess); len(list.Artifacts) != 1 {
		t.Fatalf("list after delete = %+v", list.Artifacts)
	}
}

// Another account's agent token is answered as if the session did not exist, on every route —
// never a listing, never the bytes; and a credential that is not a runner credential at all is
// refused before any session is looked at.
func TestRunnerFilesScopeAndAuth(t *testing.T) {
	fx := runnerFilesFx(t)
	m := fx.publish(fx.sessTok, fx.sess, "report.md", []byte("secret"))
	base := "/api/runner/sessions/" + fx.sess
	routes := []struct{ method, path string }{
		{"GET", base + "/artifacts"},
		{"GET", base + "/artifacts/" + m.ID + "/raw"},
		{"GET", base + "/artifacts/" + m.ID + "/download"},
		{"DELETE", base + "/artifacts/" + m.ID},
		{"POST", base + "/uploads"},
	}
	for _, tok := range []struct{ name, tok string }{
		{"another account's agent token", fx.agent(artOther)},
	} {
		for _, rt := range routes {
			rec := fx.get(rt.method, rt.path, tok.tok)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: %s %s = %d, want 404: %s", tok.name, rt.method, rt.path, rec.Code, rec.Body.String())
			}
			if bytes.Contains(rec.Body.Bytes(), []byte("secret")) || bytes.Contains(rec.Body.Bytes(), []byte(m.ID)) {
				t.Errorf("%s: %s %s leaked the file: %s", tok.name, rt.method, rt.path, rec.Body.String())
			}
		}
	}
	for _, tok := range []struct{ name, tok string }{
		{"no credential", ""},
		{"the session's own messaging token", fx.sessTok},
		{"garbage", "zz"},
	} {
		for _, rt := range routes {
			rec := fx.get(rt.method, rt.path, tok.tok)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s: %s %s = %d, want 401: %s", tok.name, rt.method, rt.path, rec.Code, rec.Body.String())
			}
		}
	}
	// The file is still there: nothing above deleted it.
	if row, _ := db.GetArtifact(t.Context(), fx.api.dbPool, fx.sess, m.ID); row == nil {
		t.Fatal("the file was deleted by a refused caller")
	}
	// The operator key is an install-wide operator and may see it; an unknown session is a 404.
	if rec := fx.get("GET", base+"/artifacts", runnerTestKey); rec.Code != http.StatusOK {
		t.Errorf("runner key list = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := fx.get("GET", "/api/runner/sessions/"+newUUID()+"/artifacts", fx.agent(artUser)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown session = %d", rec.Code)
	}
	if rec := fx.get("GET", "/api/runner/sessions/not-a-uuid/artifacts", fx.agent(artUser)); rec.Code != http.StatusNotFound {
		t.Errorf("malformed session id = %d", rec.Code)
	}
}

// A private session's files are its owner's alone, even for a token that may otherwise see every
// session; and an ended session takes no more uploads, as on the browser route.
func TestRunnerFilesPrivateAndEnded(t *testing.T) {
	fx := runnerFilesFx(t)
	m := fx.publish(fx.privTok, fx.privSess, "p.txt", []byte("p"))
	if rec := fx.get("GET", "/api/runner/sessions/"+fx.privSess+"/artifacts/"+m.ID+"/raw", runnerTestKey); rec.Code != http.StatusNotFound {
		t.Errorf("runner key on a private session = %d, want 404", rec.Code)
	}
	if rec := fx.get("GET", "/api/runner/sessions/"+fx.privSess+"/artifacts/"+m.ID+"/raw", fx.agent(artOther)); rec.Code != http.StatusOK {
		t.Errorf("owner's agent token on its private session = %d, want 200", rec.Code)
	}
	if rec := fx.runnerUpload(fx.agent(artUser), fx.endedSess, []byte("x"), "a.txt"); rec.Code != http.StatusConflict {
		t.Errorf("upload to an ended session = %d, want 409", rec.Code)
	}
}
