package server

// The configurable file limits, how they are advertised, and the failure replies of the file
// routes (filelimits.go, fileerrors.go). The route tests share the artifact fixture.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// keepLimits restores every limit after the test.
func keepLimits(t *testing.T) {
	t.Helper()
	a, b, c, d, e := maxArtifactBytes, maxArtifactBytesPerSession, maxUploadBytesPerSession, maxArtifactsPerSession, maxUploadsPerSession
	t.Cleanup(func() {
		maxArtifactBytes, maxArtifactBytesPerSession, maxUploadBytesPerSession, maxArtifactsPerSession, maxUploadsPerSession = a, b, c, d, e
	})
}

// captureLog sends the standard logger to a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	return &buf
}

func TestConfigureFileLimits(t *testing.T) {
	keepLimits(t)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	ConfigureFileLimits(env(nil))
	if maxArtifactBytes != 100<<20 || maxUploadBytesPerSession != 500<<20 || maxArtifactBytesPerSession != 1<<30 {
		t.Fatalf("defaults = %d / %d / %d", maxArtifactBytes, maxUploadBytesPerSession, maxArtifactBytesPerSession)
	}
	if maxArtifactsPerSession != 50 || maxUploadsPerSession != 100 {
		t.Fatalf("counts = %d / %d", maxArtifactsPerSession, maxUploadsPerSession)
	}

	ConfigureFileLimits(env(map[string]string{
		"BLERG_RUNNER_MAX_FILE_MB": " 250 ", "BLERG_RUNNER_MAX_SESSION_UPLOADS_MB": "2048", "BLERG_RUNNER_MAX_SESSION_ARTIFACTS_MB": "4096",
	}))
	if maxArtifactBytes != 250<<20 || maxUploadBytesPerSession != 2048<<20 || maxArtifactBytesPerSession != 4096<<20 {
		t.Fatalf("configured = %d / %d / %d", maxArtifactBytes, maxUploadBytesPerSession, maxArtifactBytesPerSession)
	}

	// A file limit above a session total is lowered to it, with one line saying so.
	buf := captureLog(t)
	ConfigureFileLimits(env(map[string]string{"BLERG_RUNNER_MAX_SESSION_UPLOADS_MB": "50"}))
	if maxArtifactBytes != 50<<20 || currentFileLimits().MaxFileBytes != 50<<20 {
		t.Fatalf("max file = %d, want the 50 MiB total", maxArtifactBytes)
	}
	if !strings.Contains(buf.String(), "one file is limited to 50 MiB") || strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("log = %q", buf.String())
	}

	for _, bad := range []string{"0", "-5", "abc", "1.5", "100MB", "99999999999"} {
		t.Run(bad, func(t *testing.T) {
			buf := captureLog(t)
			ConfigureFileLimits(env(map[string]string{"BLERG_RUNNER_MAX_FILE_MB": bad}))
			if maxArtifactBytes != 100<<20 {
				t.Errorf("max file = %d, want the default", maxArtifactBytes)
			}
			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], "BLERG_RUNNER_MAX_FILE_MB") || !strings.Contains(lines[0], "default, 100 MiB") {
				t.Errorf("log = %q, want one line naming the variable and the default", buf.String())
			}
		})
	}
}

func TestSizeText(t *testing.T) {
	for n, want := range map[int64]string{
		100 << 20:       "100 MiB",
		100<<20 + 1:     "100.1 MiB",
		130<<20 + 1<<19: "130.5 MiB",
		1 << 30:         "1024 MiB",
		1536:            "1.5 KiB",
		12:              "12 bytes",
	} {
		if got := sizeText(n); got != want {
			t.Errorf("sizeText(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestStorageKindSentences(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{&fs.PathError{Op: "write", Path: "/data/artifacts/x", Err: syscall.ENOSPC}, "disk is full"},
		{&fs.PathError{Op: "open", Path: "/data/artifacts/x", Err: syscall.EROFS}, "read-only"},
		{&fs.PathError{Op: "mkdir", Path: "/data/artifacts/x", Err: syscall.EACCES}, "permission denied"},
		{&fs.PathError{Op: "open", Path: "/data/artifacts/x", Err: syscall.ENOENT}, "missing"},
		{errors.New("something odd"), "unexpected storage error"},
	} {
		rec := httptest.NewRecorder()
		buf := captureLog(t)
		id := writeStorageFailure(rec, "save the file", accessWrite, "test ctx", c.err)
		var body struct {
			Error   string `json:"error"`
			ErrorID string `json:"error_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusInternalServerError || body.ErrorID != id || !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(id) {
			t.Errorf("%v: status %d, id %q / %q", c.err, rec.Code, body.ErrorID, id)
		}
		if !strings.HasPrefix(body.Error, "The runner could not save the file because ") || !strings.Contains(body.Error, c.want) {
			t.Errorf("%v: error = %q, want it to say %q", c.err, body.Error, c.want)
		}
		if strings.Contains(body.Error, "/data") {
			t.Errorf("the reply carries a path: %q", body.Error)
		}
		if !strings.Contains(buf.String(), "error_id="+id) || !strings.Contains(buf.String(), "test ctx") {
			t.Errorf("log = %q, want the context and the id", buf.String())
		}
	}
}

// A read that is refused says read, not write.
func TestStorageReadPermissionSaysRead(t *testing.T) {
	captureLog(t)
	rec := httptest.NewRecorder()
	writeStorageFailure(rec, "read the file", accessRead, "test", &fs.PathError{Op: "open", Path: "/data/x", Err: syscall.EACCES})
	if b := rec.Body.String(); !strings.Contains(b, "could not read the file because it is not allowed to read its data folder") || strings.Contains(b, "write") {
		t.Errorf("reply = %s", b)
	}
}

// The limits are in the files list (the CLI), the runner contract's session GET (an app's chat)
// and the browser's initial_state.
func TestFileLimitsAreAdvertised(t *testing.T) {
	keepLimits(t)
	maxArtifactBytes, maxUploadBytesPerSession, maxArtifactBytesPerSession = 7<<20, 70<<20, 700<<20
	want := protocol.FileLimits{MaxFileBytes: 7 << 20, MaxSessionUploadBytes: 70 << 20, MaxSessionArtifactBytes: 700 << 20}
	fx := runnerFilesFx(t)

	var files struct {
		Limits protocol.FileLimits `json:"limits"`
	}
	rec := fx.get("GET", "/api/sessions/"+fx.sess+"/files", fx.sessTok)
	if rec.Code != 200 {
		t.Fatalf("files = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &files); err != nil || files.Limits != want {
		t.Fatalf("files limits = %+v (%v)", files.Limits, err)
	}

	var status struct {
		Lifecycle string              `json:"lifecycle"`
		Limits    protocol.FileLimits `json:"limits"`
	}
	rec = fx.get("GET", "/api/runner/sessions/"+fx.sess, fx.agent(artUser))
	if rec.Code != 200 {
		t.Fatalf("session = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || status.Limits != want || status.Lifecycle == "" {
		t.Fatalf("session limits = %+v (%v)", status, err)
	}

	hub, _, _, bc := sidebarHubFixture(t)
	sendInitialState(context.Background(), bc, hub, nil)
	var st protocol.InitialState
	if err := json.Unmarshal(<-bc.send, &st); err != nil {
		t.Fatal(err)
	}
	if st.Limits == nil || *st.Limits != want {
		t.Fatalf("initial_state limits = %+v", st.Limits)
	}
}

// A declared size over the limit is refused before a byte is read, and the reply names both.
func TestTooLargeNamesBothSizes(t *testing.T) {
	keepLimits(t)
	maxArtifactBytes = 1 << 20
	fx := newArtFx(t)
	for _, path := range []string{"/api/sessions/" + fx.sess + "/artifacts", "/api/sessions/" + fx.sess + "/uploads"} {
		tok := fx.sessTok
		if strings.HasSuffix(path, "/uploads") {
			tok = fx.human(artUser)
		}
		req := httptest.NewRequest("POST", path, strings.NewReader("x"))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("X-Artifact-Name", "big.bin")
		req.ContentLength = 3<<20 + 1<<19
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s: status = %d: %s", path, rec.Code, rec.Body.String())
		}
		var body struct{ Error string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if !strings.Contains(body.Error, "3.5 MiB") || !strings.Contains(body.Error, "1 MiB per file") {
			t.Errorf("%s: error = %q, want both sizes", path, body.Error)
		}
	}
	// A body with no declared length that runs over says the limit.
	rec := fx.upload(fx.sessTok, fx.sess, bytes.Repeat([]byte("a"), 1<<20+1), upOpts{name: "big.txt"})
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "1 MiB per file") {
		t.Errorf("chunked = %d: %s", rec.Code, rec.Body.String())
	}
}

// The agent's published files have a per-session total, checked before reading when the size is
// declared, and after it otherwise.
func TestArtifactsSessionTotal(t *testing.T) {
	keepLimits(t)
	maxArtifactBytesPerSession = 2000
	fx := newArtFx(t)
	fx.publish(fx.sessTok, fx.sess, "a.txt", bytes.Repeat([]byte("a"), 1500))
	rec := fx.upload(fx.sessTok, fx.sess, bytes.Repeat([]byte("b"), 600), upOpts{name: "b.txt"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "delete one first") {
		t.Fatalf("over the total = %d: %s", rec.Code, rec.Body.String())
	}
	ents, _ := os.ReadDir(filepath.Join(fx.dir, "artifacts", fx.sess))
	if len(ents) != 1 {
		t.Errorf("%d directories on disk, want 1", len(ents))
	}
	// A person's attachments have their own total.
	if rec := fx.userUpload(fx.human(artUser), fx.sess, bytes.Repeat([]byte("c"), 600), "c.txt"); rec.Code != 201 {
		t.Errorf("attachment after the agent's total = %d: %s", rec.Code, rec.Body.String())
	}
}

// A data folder the runner cannot write to is a 500 that says so, with an id that is in the log.
func TestUploadStorageFailureSaysWhatHappened(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only directory anyway")
	}
	fx := newArtFx(t)
	arts := filepath.Join(fx.dir, "artifacts")
	if err := os.MkdirAll(arts, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(arts, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(arts, 0o750) })
	buf := captureLog(t)
	rec := fx.upload(fx.sessTok, fx.sess, []byte("hello"), upOpts{name: "a.txt"})
	var body struct {
		Error   string `json:"error"`
		ErrorID string `json:"error_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(body.Error, "permission denied") || body.ErrorID == "" {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(body.Error, fx.dir) || strings.Contains(rec.Body.String(), fx.dir) {
		t.Errorf("the reply carries the data directory: %s", rec.Body.String())
	}
	if !strings.Contains(buf.String(), fmt.Sprintf("error_id=%s", body.ErrorID)) {
		t.Errorf("log = %q, want error_id=%s", buf.String(), body.ErrorID)
	}
}

// An attachment whose bytes are gone is a 410 for the agent too, not a 404.
func TestAttachmentFileGone(t *testing.T) {
	fx := newArtFx(t)
	u := fx.attach(fx.sess, "doc.pdf", []byte("bytes"))
	if err := os.RemoveAll(filepath.Join(fx.dir, "artifacts", fx.sess, u.ID)); err != nil {
		t.Fatal(err)
	}
	rec := fx.get("GET", "/api/sessions/"+fx.sess+"/attachments/"+u.ID+"/file", fx.sessTok)
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}
