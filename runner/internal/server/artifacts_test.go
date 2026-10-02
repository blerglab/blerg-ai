package server

// Session artifacts (artifacts.go, routes_artifacts.go, db/artifacts.go) against a real database:
// an agent uploads with its per-session token, a signed-in person lists, views, downloads and
// deletes. Files live under BLERG_RUNNER_DATA_DIR, which every test points at a temp dir.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	artUser  = "user-1"
	artOther = "user-2"
)

// tinyPNG is a 1x1 PNG: real image bytes, so the extension/sniff agreement check passes.
var tinyPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0,
	0x1f, 0x15, 0xc4, 0x89, 0, 0, 0, 0x0d, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0xf8, 0xff, 0xff, 0x3f, 0, 0x05, 0xfe, 0x02,
	0xfe, 0xa7, 0x35, 0x81, 0x84, 0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

type artFx struct {
	t    *testing.T
	api  *API
	hub  *Hub
	mux  *http.ServeMux
	priv ed25519.PrivateKey
	dir  string

	sess      string // public, started by artUser
	otherSess string // public, started by artOther
	privSess  string // private, started by artOther
	endedSess string // stopped
	sessTok   string // per-session messaging token of sess
	otherTok  string // per-session messaging token of otherSess
	privTok   string // per-session messaging token of privSess
	endedTok  string
	events    chan []byte // what the hub fans out to a browser watching sess
}

func newArtFx(t *testing.T) *artFx {
	t.Helper()
	pool := runnerContractPool(t)
	dir := t.TempDir()
	t.Setenv("BLERG_RUNNER_DATA_DIR", dir)
	ctx := context.Background()
	daemonID := "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0d"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	api := NewAPI(hub, pool, "daemon-tok-1234567890", nil, "")
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	mux := http.NewServeMux()
	api.registerArtifactRoutes(mux)
	fx := &artFx{t: t, api: api, hub: hub, mux: mux, priv: priv, dir: dir}

	mk := func(account, status string) (string, string) {
		id := newUUID()
		if err := db.InsertSession(ctx, pool, id, daemonID, status, "/repos/app", "app", "t", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.SetSessionSpawningAccount(ctx, pool, id, account); err != nil {
			t.Fatal(err)
		}
		tok, err := db.MintSessionToken(ctx, pool, id, []string{"message"}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return id, tok
	}
	fx.sess, fx.sessTok = mk(artUser, "running")
	fx.otherSess, fx.otherTok = mk(artOther, "running")
	fx.privSess, fx.privTok = mk(artOther, "running")
	if err := api.MarkPrivate(ctx, fx.privSess); err != nil {
		t.Fatal(err)
	}
	fx.endedSess, fx.endedTok = mk(artUser, "stopped")
	fx.events = make(chan []byte, 16)
	hub.Subscribe(fx.sess, "watcher", fx.events)
	return fx
}

// human mints a signed-in person's token for account; agent one acting for it.
func (fx *artFx) human(account string) string {
	return mintRunnerToken(fx.t, fx.priv, "core-1", identity.Claims{
		Sub: account, Aud: coreAuthAudience, Kind: "human", Sid: testSID,
		Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
}

func (fx *artFx) agent(account string) string {
	return mintRunnerToken(fx.t, fx.priv, "core-1", identity.Claims{
		Sub: "tok-" + account, OnBehalfOf: account, Aud: coreAuthAudience, Kind: "agent",
		Caps: []string{coreAuthBrowserCap}, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
}

type upOpts struct {
	name, ctype, query string
	hdr                map[string]string
}

func (fx *artFx) upload(tok, sess string, body []byte, o upOpts) *httptest.ResponseRecorder {
	fx.t.Helper()
	target := "/api/sessions/" + sess + "/artifacts" + o.query
	req := httptest.NewRequest("POST", target, bytes.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if o.name != "" {
		req.Header.Set("X-Artifact-Name", o.name)
	}
	if o.ctype != "" {
		req.Header.Set("Content-Type", o.ctype)
	}
	for k, v := range o.hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec
}

func (fx *artFx) get(method, path, tok string) *httptest.ResponseRecorder {
	fx.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec
}

type artMeta struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	View        string `json:"view"`
	URL         string `json:"url"`
}

func (fx *artFx) publish(tok, sess, name string, body []byte) artMeta {
	fx.t.Helper()
	rec := fx.upload(tok, sess, body, upOpts{name: name})
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("upload %s = %d: %s", name, rec.Code, rec.Body.String())
	}
	var m artMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		fx.t.Fatal(err)
	}
	return m
}

func (fx *artFx) path(sess, id string) string {
	return filepath.Join(fx.dir, "artifacts", sess, id)
}

func TestArtifactsUploadAuth(t *testing.T) {
	fx := newArtFx(t)
	body := []byte("hello")
	cases := []struct {
		name string
		tok  string
		sess string
		want int
	}{
		{"no token", "", fx.sess, 401},
		{"garbage token", "zz", fx.sess, 401},
		{"valid hex that matches nothing", strings.Repeat("ab", 32), fx.sess, 401},
		{"another session's token", fx.otherTok, fx.sess, 403},
		{"a person's browser token is not a session credential", fx.human(artUser), fx.sess, 401},
		{"own session token", fx.sessTok, fx.sess, 201},
		{"daemon token", "daemon-tok-1234567890", fx.sess, 201},
		{"private session, its own token", fx.privTok, fx.privSess, 201},
		{"private session, a foreign token", fx.sessTok, fx.privSess, 403},
		{"unknown session", "daemon-tok-1234567890", newUUID(), 404},
		{"path traversal as a session id", "daemon-tok-1234567890", "..%2F..%2Fetc", 404},
		{"not a uuid", "daemon-tok-1234567890", "abc", 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := fx.upload(c.tok, c.sess, body, upOpts{name: "a.txt"})
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

func TestArtifactsUploadEndedSessionIs409(t *testing.T) {
	fx := newArtFx(t)
	rec := fx.upload("daemon-tok-1234567890", fx.endedSess, []byte("x"), upOpts{name: "a.txt"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("ended session upload = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	_ = fx.endedTok
}

func TestArtifactsUploadStoresFileRowAndRecordsOneEvent(t *testing.T) {
	fx := newArtFx(t)
	body := []byte("# Report\n\nall good\n")
	sum := sha256.Sum256(body)
	rec := fx.upload(fx.sessTok, fx.sess, body, upOpts{name: "report.md", ctype: "text/html"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var m artMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if !validPublishID(m.ID) || m.Name != "report.md" || m.Size != int64(len(body)) || m.View != "markdown" {
		t.Fatalf("response = %+v", m)
	}
	if m.ContentType != "text/markdown; charset=utf-8" {
		t.Errorf("content_type = %q: the uploader's Content-Type (text/html) must never be used", m.ContentType)
	}
	if m.URL != "/sessions/"+fx.sess+"?artifact="+m.ID {
		t.Errorf("url = %q", m.URL)
	}
	got, err := os.ReadFile(filepath.Join(fx.path(fx.sess, m.ID), "report.md"))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("stored file = %q, %v", got, err)
	}
	row, err := db.GetArtifact(context.Background(), fx.api.dbPool, fx.sess, m.ID)
	if err != nil || row == nil || row.SHA256 != hex.EncodeToString(sum[:]) || row.Size != int64(len(body)) {
		t.Fatalf("row = %+v, %v", row, err)
	}

	// Persisted, exactly once, so it survives a reload.
	evs, err := db.ListAgentEvents(context.Background(), fx.api.dbPool, fx.sess, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind != "artifact" {
			continue
		}
		n++
		var p artMeta
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil || p.ID != m.ID || p.Name != "report.md" || p.View != "markdown" || p.Size != int64(len(body)) {
			t.Errorf("event payload = %s (%v)", e.Payload, err)
		}
	}
	if n != 1 {
		t.Fatalf("%d artifact events persisted, want 1", n)
	}
	// Broadcast to the session's watchers, exactly once, carrying the assigned seq.
	select {
	case raw := <-fx.events:
		var ev struct {
			Type      string  `json:"type"`
			SessionID string  `json:"session_id"`
			Kind      string  `json:"kind"`
			Seq       int64   `json:"seq"`
			Payload   artMeta `json:"payload"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != "agent_event" || ev.Kind != "artifact" || ev.SessionID != fx.sess || ev.Seq < 1 || ev.Payload.ID != m.ID {
			t.Errorf("broadcast = %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("no broadcast")
	}
	select {
	case raw := <-fx.events:
		t.Fatalf("second broadcast: %s", raw)
	default:
	}
}

func TestArtifactsUploadLimits(t *testing.T) {
	fx := newArtFx(t)
	oldBytes, oldCount := maxArtifactBytes, maxArtifactsPerSession
	maxArtifactBytes, maxArtifactsPerSession = 1024, 3
	t.Cleanup(func() { maxArtifactBytes, maxArtifactsPerSession = oldBytes, oldCount })

	t.Run("a body over the cap is 413 and leaves nothing behind", func(t *testing.T) {
		rec := fx.upload(fx.sessTok, fx.sess, bytes.Repeat([]byte("a"), 1025), upOpts{name: "big.txt"})
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(filepath.Join(fx.dir, "artifacts", fx.sess)); err == nil {
			ents, _ := os.ReadDir(filepath.Join(fx.dir, "artifacts", fx.sess))
			if len(ents) != 0 {
				t.Errorf("left %d entries behind", len(ents))
			}
		}
		if n, _ := db.CountArtifacts(context.Background(), fx.api.dbPool, fx.sess); n != 0 {
			t.Errorf("%d rows", n)
		}
		if n := countEvents(t, fx, "artifact"); n != 0 {
			t.Errorf("%d events recorded for a refused upload", n)
		}
	})
	t.Run("a declared length over the cap is refused before reading", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/artifacts", strings.NewReader("x"))
		req.Header.Set("Authorization", "Bearer "+fx.sessTok)
		req.ContentLength = 5000
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("exactly the cap is fine", func(t *testing.T) {
		if rec := fx.upload(fx.sessTok, fx.sess, bytes.Repeat([]byte("a"), 1024), upOpts{name: "edge.txt"}); rec.Code != 201 {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("the per-session cap is 409", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			fx.publish(fx.sessTok, fx.sess, "n.txt", []byte("x"))
		}
		rec := fx.upload(fx.sessTok, fx.sess, []byte("x"), upOpts{name: "one-too-many.txt"})
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		ents, _ := os.ReadDir(filepath.Join(fx.dir, "artifacts", fx.sess))
		if len(ents) != 3 {
			t.Errorf("%d directories on disk, want 3 (the refused one removed)", len(ents))
		}
		// Another session is unaffected.
		if rec := fx.upload(fx.otherTok, fx.otherSess, []byte("x"), upOpts{name: "a.txt"}); rec.Code != 201 {
			t.Errorf("another session = %d", rec.Code)
		}
	})
}

func countEvents(t *testing.T, fx *artFx, kind string) int {
	t.Helper()
	evs, err := db.ListAgentEvents(context.Background(), fx.api.dbPool, fx.sess, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func TestSanitizeArtifactName(t *testing.T) {
	long := strings.Repeat("x", 300) + ".pdf"
	cases := []struct{ in, want string }{
		{"report.md", "report.md"},
		{"../../x", "x"},
		{`..\..\win\x.txt`, "x.txt"},
		{"/etc/passwd", "passwd"},
		{"a\x00b.txt", "ab.txt"},
		{"tab\tnew\nline.txt", "tabnewline.txt"},
		{"", "artifact"},
		{"..", "artifact"},
		{".", "artifact"},
		{"dir/", "artifact"},
		{"   ", "artifact"},
		{"bad\xffutf8.txt", "badutf8.txt"},
		{"héllo wörld.txt", "héllo wörld.txt"},
		{"evil" + string(rune(0x202e)) + "gpj.exe", "evilgpj.exe"},
		{long, strings.Repeat("x", 116) + ".pdf"},
		{strings.Repeat("é", 100) + ".txt", strings.Repeat("é", 58) + ".txt"},
	}
	for _, c := range cases {
		got := sanitizeArtifactName(c.in)
		if got != c.want {
			t.Errorf("sanitizeArtifactName(%q) = %q, want %q", c.in, got, c.want)
		}
		if len(got) > 120 || strings.ContainsAny(got, "/\\\x00") {
			t.Errorf("sanitizeArtifactName(%q) = %q is not safe", c.in, got)
		}
	}
}

func TestArtifactsUploadNames(t *testing.T) {
	fx := newArtFx(t)
	cases := []struct {
		name string
		o    upOpts
		want string
	}{
		{"traversal in the header", upOpts{name: "../../x.txt"}, "x.txt"},
		{"percent-encoded header (non-ASCII)", upOpts{name: "r%C3%A9sum%C3%A9%20v2.txt"}, "résumé v2.txt"},
		{"encoded traversal", upOpts{name: "..%2F..%2Fsecret.txt"}, "secret.txt"},
		{"query parameter", upOpts{query: "?name=q.txt"}, "q.txt"},
		{"header wins over query", upOpts{name: "h.txt", query: "?name=q.txt"}, "h.txt"},
		{"no name", upOpts{}, "artifact"},
		{"NUL in the name", upOpts{name: "a%00b.txt"}, "ab.txt"},
		{"very long", upOpts{name: strings.Repeat("y", 500) + ".txt"}, strings.Repeat("y", 116) + ".txt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := fx.upload(fx.sessTok, fx.sess, []byte("data"), c.o)
			if rec.Code != 201 {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			var m artMeta
			_ = json.Unmarshal(rec.Body.Bytes(), &m)
			if m.Name != c.want {
				t.Fatalf("name = %q, want %q", m.Name, c.want)
			}
			if _, err := os.Stat(filepath.Join(fx.path(fx.sess, m.ID), c.want)); err != nil {
				t.Errorf("file not stored under the safe name: %v", err)
			}
			// Nothing escaped the artifacts tree.
			if _, err := os.Stat(filepath.Join(fx.dir, "x.txt")); err == nil {
				t.Error("file escaped the artifact directory")
			}
		})
	}
}

func TestClassifyArtifact(t *testing.T) {
	html := []byte("<!doctype html><html><script>alert(1)</script></html>")
	cases := []struct {
		name string
		head []byte
		size int64
		ct   string
		view string
	}{
		{"a.md", []byte("# x"), 3, "text/markdown; charset=utf-8", "markdown"},
		{"a.MARKDOWN", []byte("# x"), 3, "text/markdown; charset=utf-8", "markdown"},
		{"run.log", []byte("x"), 1, "text/plain; charset=utf-8", "text"},
		{"main.go", []byte("package x"), 9, "text/plain; charset=utf-8", "text"},
		{"conf.yaml", []byte("a: 1"), 4, "text/plain; charset=utf-8", "text"},
		{"data.xml", []byte("<a/>"), 4, "application/xml", "text"},
		{"d.json", []byte("{}"), 2, "application/json", "json"},
		{"d.csv", []byte("a,b"), 3, "text/csv; charset=utf-8", "csv"},
		{"d.tsv", []byte("a\tb"), 3, "text/tab-separated-values; charset=utf-8", "csv"},
		{"p.png", tinyPNG, int64(len(tinyPNG)), "image/png", "image"},
		{"p.png", html, 50, "application/octet-stream", "none"}, // not a PNG: no preview
		{"p.jpg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF"), 10, "image/jpeg", "image"},
		{"v.svg", []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>"), 70, "image/svg+xml", "image"},
		{"r.pdf", []byte("%PDF-1.7\n"), 9, "application/pdf", "pdf"},
		{"r.pdf", html, 50, "application/octet-stream", "none"},
		{"a.mp3", []byte("ID3\x03"), 4, "audio/mpeg", "audio"},
		{"a.wav", []byte("RIFF"), 4, "audio/wav", "audio"},
		{"a.ogg", []byte("OggS"), 4, "audio/ogg", "audio"},
		{"a.m4a", []byte("xxxx"), 4, "audio/mp4", "audio"},
		{"c.mp4", []byte("xxxx"), 4, "video/mp4", "video"},
		{"c.webm", []byte("xxxx"), 4, "video/webm", "video"},
		{"page.html", html, 50, "text/html; charset=utf-8", "html"},
		{"page.htm", html, 50, "text/html; charset=utf-8", "html"},
		{"memo.docx", []byte("PK\x03\x04"), 4, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "none"},
		{"sheet.xlsx", []byte("PK\x03\x04"), 4, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "none"},
		{"deck.pptx", []byte("PK\x03\x04"), 4, "application/vnd.openxmlformats-officedocument.presentationml.presentation", "none"},
		{"bundle.zip", []byte("PK\x03\x04"), 4, "application/zip", "none"},
		{"tool.exe", []byte("MZ\x90\x00"), 4, "application/octet-stream", "none"},
		{"Dockerfile", []byte("FROM x"), 6, "text/plain; charset=utf-8", "text"},
		{"NOTES", []byte("plain words"), 11, "text/plain; charset=utf-8", "text"},              // unknown extension, text bytes
		{"NOTES", html, 50, "text/plain; charset=utf-8", "text"},                               // sniffs as HTML: still only text
		{"NOTES", []byte("plain words"), maxSniffText + 1, "application/octet-stream", "none"}, // too big to offer
		{"blob", []byte{0, 1, 2, 3, 0xff, 0xfe}, 6, "application/octet-stream", "none"},
		{"shot", tinyPNG, int64(len(tinyPNG)), "image/png", "image"}, // no extension: sniffed
	}
	for _, c := range cases {
		ct, view := classifyArtifact(c.name, c.head, c.size)
		if ct != c.ct || view != c.view {
			t.Errorf("classifyArtifact(%q) = (%q, %q), want (%q, %q)", c.name, ct, view, c.ct, c.view)
		}
		// The database keeps only the type; the list recovers the view from it.
		if got := viewForContentType(ct); got != view {
			t.Errorf("viewForContentType(%q) = %q, but %q was classified %q", ct, got, c.name, view)
		}
	}
}

func TestArtifactServedType(t *testing.T) {
	cases := []struct {
		stored        string
		raw, download string
	}{
		{"image/png", "image/png", "image/png"},
		{"image/jpeg", "image/jpeg", "image/jpeg"},
		{"image/gif", "image/gif", "image/gif"},
		{"image/webp", "image/webp", "image/webp"},
		{"application/pdf", "application/pdf", "application/pdf"},
		{"text/html; charset=utf-8", octetStream, octetStream},
		{"image/svg+xml", octetStream, octetStream},
		{"application/xml", octetStream, octetStream},
		{"text/xml", octetStream, octetStream},
		{"application/xhtml+xml", octetStream, octetStream},
		{"text/javascript", octetStream, octetStream},
		{"application/javascript", octetStream, octetStream},
		{"text/plain; charset=utf-8", octetStream, "text/plain; charset=utf-8"},
		{"text/markdown; charset=utf-8", octetStream, "text/markdown; charset=utf-8"},
		{"application/json", octetStream, "application/json"},
		{"audio/mpeg", octetStream, "audio/mpeg"},
		{"video/mp4", octetStream, "video/mp4"},
		{"application/zip", octetStream, "application/zip"},
		{"something/else", octetStream, octetStream},
		{"", octetStream, octetStream},
	}
	for _, c := range cases {
		if got := artifactServedType(c.stored, true); got != c.raw {
			t.Errorf("raw %q = %q, want %q", c.stored, got, c.raw)
		}
		if got := artifactServedType(c.stored, false); got != c.download {
			t.Errorf("download %q = %q, want %q", c.stored, got, c.download)
		}
	}
}

func TestArtifactsDownloadAndRawNeverServeRenderableTypes(t *testing.T) {
	fx := newArtFx(t)
	tok := fx.human(artUser)
	// What is uploaded, what the uploader claims it is, and what each route must answer.
	cases := []struct {
		file     string
		body     []byte
		claimed  string
		view     string
		rawType  string
		downType string
	}{
		{"page.html", []byte("<script>alert(1)</script>"), "text/html", "html", octetStream, octetStream},
		{"logo.svg", []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>"), "image/svg+xml", "image", octetStream, octetStream},
		{"feed.xml", []byte("<?xml version='1.0'?><a/>"), "application/xml", "text", octetStream, octetStream},
		{"app.js", []byte("alert(1)"), "text/javascript", "text", octetStream, "text/plain; charset=utf-8"},
		{"fake.png", []byte("<script>alert(1)</script>"), "image/png", "none", octetStream, octetStream},
		{"shot.png", tinyPNG, "text/html", "image", "image/png", "image/png"},
		{"doc.pdf", []byte("%PDF-1.4 hello"), "text/html", "pdf", "application/pdf", "application/pdf"},
		{"notes.txt", []byte("hi"), "text/html", "text", octetStream, "text/plain; charset=utf-8"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			rec := fx.upload(fx.sessTok, fx.sess, c.body, upOpts{name: c.file, ctype: c.claimed})
			if rec.Code != 201 {
				t.Fatalf("upload = %d: %s", rec.Code, rec.Body.String())
			}
			var m artMeta
			_ = json.Unmarshal(rec.Body.Bytes(), &m)
			if m.View != c.view {
				t.Fatalf("view = %q, want %q", m.View, c.view)
			}
			base := "/api/sessions/" + fx.sess + "/artifacts/" + m.ID
			raw := fx.get("GET", base+"/raw", tok)
			if raw.Code != 200 || raw.Header().Get("Content-Type") != c.rawType {
				t.Errorf("raw = %d %q, want 200 %q", raw.Code, raw.Header().Get("Content-Type"), c.rawType)
			}
			if !bytes.Equal(raw.Body.Bytes(), c.body) {
				t.Errorf("raw body differs")
			}
			for _, h := range []string{"X-Content-Type-Options:nosniff", "Content-Security-Policy:sandbox", "Cache-Control:private, no-store"} {
				k, v, _ := strings.Cut(h, ":")
				if raw.Header().Get(k) != v {
					t.Errorf("raw %s = %q, want %q", k, raw.Header().Get(k), v)
				}
			}
			dl := fx.get("GET", base+"/download", tok)
			if dl.Code != 200 || dl.Header().Get("Content-Type") != c.downType {
				t.Errorf("download = %d %q, want 200 %q", dl.Code, dl.Header().Get("Content-Type"), c.downType)
			}
			if !bytes.Equal(dl.Body.Bytes(), c.body) {
				t.Errorf("download body differs")
			}
			for _, h := range []string{"X-Content-Type-Options:nosniff", "Content-Security-Policy:sandbox", "Cache-Control:private, no-store"} {
				k, v, _ := strings.Cut(h, ":")
				if dl.Header().Get(k) != v {
					t.Errorf("download %s = %q, want %q", k, dl.Header().Get(k), v)
				}
			}
		})
	}
}

func TestArtifactsDownloadDisposition(t *testing.T) {
	fx := newArtFx(t)
	m := fx.publish(fx.sessTok, fx.sess, "résumé \"v2\".txt", []byte("x"))
	dl := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+m.ID+"/download", fx.human(artUser))
	want := "attachment; filename*=UTF-8''r%C3%A9sum%C3%A9%20%22v2%22.txt"
	if got := dl.Header().Get("Content-Disposition"); got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	raw := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+m.ID+"/raw", fx.human(artUser))
	if d := raw.Header().Get("Content-Disposition"); d != "" {
		t.Errorf("raw Content-Disposition = %q, want none", d)
	}
}

func TestArtifactsHumanRoutesAuthAndVisibility(t *testing.T) {
	fx := newArtFx(t)
	mine := fx.publish(fx.sessTok, fx.sess, "mine.txt", []byte("mine"))
	priv := fx.publish(fx.privTok, fx.privSess, "secret.txt", []byte("secret"))
	open := fx.publish(fx.otherTok, fx.otherSess, "open.txt", []byte("open"))

	art := func(sess, id, tail string) string { return "/api/sessions/" + sess + "/artifacts/" + id + tail }
	cases := []struct {
		name, method, path, tok string
		want                    int
	}{
		{"list without a token", "GET", "/api/sessions/" + fx.sess + "/artifacts", "", 401},
		{"download without a token", "GET", art(fx.sess, mine.ID, "/download"), "", 401},
		{"raw without a token", "GET", art(fx.sess, mine.ID, "/raw"), "", 401},
		{"delete without a token", "DELETE", art(fx.sess, mine.ID, ""), "", 401},
		{"a session messaging token is not a person", "GET", "/api/sessions/" + fx.sess + "/artifacts", fx.sessTok, 401},
		{"the daemon token is not a person", "GET", "/api/sessions/" + fx.sess + "/artifacts", "daemon-tok-1234567890", 401},
		{"agent token: list", "GET", "/api/sessions/" + fx.sess + "/artifacts", fx.agent(artUser), 403},
		{"agent token: download", "GET", art(fx.sess, mine.ID, "/download"), fx.agent(artUser), 403},
		{"agent token: raw", "GET", art(fx.sess, mine.ID, "/raw"), fx.agent(artUser), 403},
		{"agent token: delete", "DELETE", art(fx.sess, mine.ID, ""), fx.agent(artUser), 403},
		{"owner lists", "GET", "/api/sessions/" + fx.sess + "/artifacts", fx.human(artUser), 200},
		{"any person sees a non-private session's files", "GET", art(fx.otherSess, open.ID, "/download"), fx.human(artUser), 200},
		{"another account's private session: list", "GET", "/api/sessions/" + fx.privSess + "/artifacts", fx.human(artUser), 404},
		{"another account's private session: download", "GET", art(fx.privSess, priv.ID, "/download"), fx.human(artUser), 404},
		{"another account's private session: raw", "GET", art(fx.privSess, priv.ID, "/raw"), fx.human(artUser), 404},
		{"another account's private session: delete", "DELETE", art(fx.privSess, priv.ID, ""), fx.human(artUser), 404},
		{"the private session's owner", "GET", art(fx.privSess, priv.ID, "/download"), fx.human(artOther), 200},
		{"an unknown session looks the same", "GET", "/api/sessions/" + newUUID() + "/artifacts", fx.human(artUser), 404},
		{"unknown artifact", "GET", art(fx.sess, newPublishID(), "/download"), fx.human(artUser), 404},
		{"another session's artifact under my session", "GET", art(fx.sess, open.ID, "/download"), fx.human(artUser), 404},
		{"traversal as an artifact id", "GET", art(fx.sess, "..%2F..%2F..%2Fx", "/download"), fx.human(artUser), 404},
		{"traversal as an artifact id (raw)", "GET", art(fx.sess, "..%2F"+mine.ID, "/raw"), fx.human(artUser), 404},
		{"a short id", "GET", art(fx.sess, "abc", "/download"), fx.human(artUser), 404},
		{"traversal as a session id", "GET", "/api/sessions/..%2F..%2Fx/artifacts", fx.human(artUser), 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := fx.get(c.method, c.path, c.tok)
			if rec.Code != c.want {
				t.Fatalf("%s %s = %d, want %d: %s", c.method, c.path, rec.Code, c.want, rec.Body.String())
			}
			if c.want == 404 && strings.Contains(rec.Body.String(), "secret") {
				t.Errorf("404 leaks: %s", rec.Body.String())
			}
		})
	}
	// The private artifact survived all those refused deletes.
	if row, _ := db.GetArtifact(context.Background(), fx.api.dbPool, fx.privSess, priv.ID); row == nil {
		t.Error("a refused delete removed the private artifact")
	}
}

func TestArtifactsListNewestFirst(t *testing.T) {
	fx := newArtFx(t)
	a := fx.publish(fx.sessTok, fx.sess, "a.txt", []byte("1"))
	time.Sleep(5 * time.Millisecond)
	b := fx.publish(fx.sessTok, fx.sess, "b.png", tinyPNG)
	time.Sleep(5 * time.Millisecond)
	c := fx.publish(fx.sessTok, fx.sess, "c.docx", []byte("PK\x03\x04"))
	rec := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts", fx.human(artUser))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Artifacts []struct {
			artMeta
			CreatedAt string `json:"created_at"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Artifacts) != 3 || out.Artifacts[0].ID != c.ID || out.Artifacts[1].ID != b.ID || out.Artifacts[2].ID != a.ID {
		t.Fatalf("order = %+v", out.Artifacts)
	}
	if out.Artifacts[0].View != "none" || out.Artifacts[1].View != "image" || out.Artifacts[1].Size != int64(len(tinyPNG)) ||
		out.Artifacts[1].CreatedAt == "" || out.Artifacts[1].ContentType != "image/png" {
		t.Errorf("fields = %+v", out.Artifacts[1])
	}
	// No artifacts is an empty list, not null.
	rec = fx.get("GET", "/api/sessions/"+fx.otherSess+"/artifacts", fx.human(artUser))
	if !strings.Contains(rec.Body.String(), `"artifacts":[]`) {
		t.Errorf("empty list = %s", rec.Body.String())
	}
}

func TestArtifactsDeleteRemovesRowAndFile(t *testing.T) {
	fx := newArtFx(t)
	m := fx.publish(fx.sessTok, fx.sess, "gone.txt", []byte("x"))
	if _, err := os.Stat(fx.path(fx.sess, m.ID)); err != nil {
		t.Fatal(err)
	}
	rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/artifacts/"+m.ID, fx.human(artOther)) // anyone who can see the session
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(fx.path(fx.sess, m.ID)); !os.IsNotExist(err) {
		t.Errorf("file still there: %v", err)
	}
	if row, _ := db.GetArtifact(context.Background(), fx.api.dbPool, fx.sess, m.ID); row != nil {
		t.Error("row still there")
	}
	if rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/artifacts/"+m.ID, fx.human(artUser)); rec.Code != 404 {
		t.Errorf("second delete = %d, want 404", rec.Code)
	}
	if rec := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+m.ID+"/download", fx.human(artUser)); rec.Code != 404 {
		t.Errorf("download after delete = %d, want 404", rec.Code)
	}
}

func TestArtifactsCascadeAndPrune(t *testing.T) {
	fx := newArtFx(t)
	ctx := context.Background()
	gone := fx.publish(fx.sessTok, fx.sess, "a.txt", []byte("x"))
	keep := fx.publish(fx.otherTok, fx.otherSess, "b.txt", []byte("y"))

	// An orphan: a directory of a session that never existed, and one that is not a session id at all.
	orphan := newUUID()
	if err := os.MkdirAll(filepath.Join(fx.dir, "artifacts", orphan, newPublishID()), 0o750); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(fx.dir, "artifacts", "not-a-session")
	if err := os.MkdirAll(stray, 0o750); err != nil {
		t.Fatal(err)
	}

	// Deleting the session row cascades the index. (The agent_events rows, which have no cascade, are
	// cleared first: a session that ran is ended, never deleted, so this is the test's shortcut.)
	if _, err := fx.api.dbPool.Exec(ctx, `DELETE FROM agent_events WHERE session_id = $1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSession(ctx, fx.api.dbPool, fx.sess); err != nil {
		t.Fatal(err)
	}
	if n, err := db.CountArtifacts(ctx, fx.api.dbPool, fx.sess); err != nil || n != 0 {
		t.Fatalf("rows after session delete = %d, %v", n, err)
	}
	if _, err := os.Stat(fx.path(fx.sess, gone.ID)); err != nil {
		t.Fatalf("files are removed by the server or the prune, not by the database: %v", err)
	}

	removed, err := fx.api.pruneOrphanArtifacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("pruned %d directories, want 2 (the deleted session's and the made-up one)", removed)
	}
	for _, p := range []string{filepath.Join(fx.dir, "artifacts", fx.sess), filepath.Join(fx.dir, "artifacts", orphan)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the prune", p)
		}
	}
	if _, err := os.Stat(fx.path(fx.otherSess, keep.ID)); err != nil {
		t.Errorf("a live session's artifact was pruned: %v", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("a directory that is not a session id was touched: %v", err)
	}
	// A second pass finds nothing.
	if n, _ := fx.api.pruneOrphanArtifacts(ctx); n != 0 {
		t.Errorf("second prune removed %d", n)
	}
}

func TestArtifactsRemoveSessionFiles(t *testing.T) {
	fx := newArtFx(t)
	m := fx.publish(fx.sessTok, fx.sess, "a.txt", []byte("x"))
	removeSessionArtifactFiles(fx.sess)
	if _, err := os.Stat(fx.path(fx.sess, m.ID)); !os.IsNotExist(err) {
		t.Errorf("files survived: %v", err)
	}
	removeSessionArtifactFiles("../../etc") // not a session id: a no-op, never a path walk
	removeSessionArtifactFiles("")
}

func TestArtifactsNoDatabase(t *testing.T) {
	api := NewAPI(NewHub(), nil, "daemon-tok-1234567890", nil, "")
	mux := http.NewServeMux()
	api.registerArtifactRoutes(mux)
	req := httptest.NewRequest("POST", "/api/sessions/"+newUUID()+"/artifacts", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer daemon-tok-1234567890")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
