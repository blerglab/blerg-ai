package server

// File versions: publishing (or uploading) a name that already exists in the session, from the same
// origin, makes the next version of it. Same fixture as the artifact tests.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

type verMeta struct {
	artMeta
	Origin        string `json:"origin"`
	Version       int    `json:"version"`
	LatestVersion int    `json:"latest_version"`
	Previous      *int   `json:"previous"`
}

func (fx *artFx) pubVer(tok, sess, name, body string) verMeta {
	fx.t.Helper()
	rec := fx.upload(tok, sess, []byte(body), upOpts{name: name})
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("publish %s = %d: %s", name, rec.Code, rec.Body.String())
	}
	var m verMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		fx.t.Fatal(err)
	}
	return m
}

func (fx *artFx) attachVer(sess, name, body string) verMeta {
	fx.t.Helper()
	rec := fx.userUpload(fx.human(artUser), sess, []byte(body), name)
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("attach %s = %d: %s", name, rec.Code, rec.Body.String())
	}
	var m verMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		fx.t.Fatal(err)
	}
	return m
}

func prevOf(m verMeta) string {
	if m.Previous == nil {
		return "null"
	}
	return fmt.Sprint(*m.Previous)
}

func TestArtifactVersionsSecondPublishIsV2WithPrevious(t *testing.T) {
	fx := newArtFx(t)
	v1 := fx.pubVer(fx.sessTok, fx.sess, "report.md", "one")
	if v1.Version != 1 || v1.Previous != nil {
		t.Fatalf("first publish = v%d previous %s, want v1 previous null", v1.Version, prevOf(v1))
	}
	v2 := fx.pubVer(fx.sessTok, fx.sess, "report.md", "two")
	if v2.Version != 2 || v2.Previous == nil || *v2.Previous != 1 {
		t.Fatalf("second publish = v%d previous %s, want v2 previous 1", v2.Version, prevOf(v2))
	}
	// The reply says which version is the latest: the one just made.
	if v1.LatestVersion != 1 || v2.LatestVersion != 2 {
		t.Errorf("latest_version in the upload reply = %d, %d; want 1, 2", v1.LatestVersion, v2.LatestVersion)
	}
	if u := fx.attachVer(fx.sess, "brief.md", "a"); u.LatestVersion != 1 {
		t.Errorf("an attachment reply latest_version = %d, want 1", u.LatestVersion)
	}
	if v2.ID == v1.ID || v2.Name != "report.md" {
		t.Errorf("the new version is its own file: %+v vs %+v", v2, v1)
	}
	if other := fx.pubVer(fx.sessTok, fx.sess, "notes.md", "x"); other.Version != 1 || other.Previous != nil {
		t.Errorf("a different name = v%d previous %s, want v1 null", other.Version, prevOf(other))
	}
	// Another session has its own numbering.
	if o := fx.pubVer(fx.otherTok, fx.otherSess, "report.md", "x"); o.Version != 1 {
		t.Errorf("another session = v%d, want v1", o.Version)
	}
	// Each version keeps its own bytes.
	for _, c := range []struct {
		id, want string
	}{{v1.ID, "one"}, {v2.ID, "two"}} {
		rec := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+c.id+"/raw", fx.human(artUser))
		if rec.Body.String() != c.want {
			t.Errorf("bytes of %s = %q, want %q", c.id, rec.Body.String(), c.want)
		}
	}
}

func TestArtifactVersionsUserAndAgentSequencesAreSeparate(t *testing.T) {
	fx := newArtFx(t)
	a1 := fx.pubVer(fx.sessTok, fx.sess, "data.csv", "a1")
	u1 := fx.attachVer(fx.sess, "data.csv", "u1")
	if a1.Version != 1 || u1.Version != 1 || u1.Origin != "user" || u1.Previous != nil {
		t.Fatalf("agent v%d, user v%d (%s) previous %s; want both v1", a1.Version, u1.Version, u1.Origin, prevOf(u1))
	}
	u2 := fx.attachVer(fx.sess, "data.csv", "u2")
	a2 := fx.pubVer(fx.sessTok, fx.sess, "data.csv", "a2")
	if u2.Version != 2 || a2.Version != 2 {
		t.Fatalf("user v%d, agent v%d; want both v2", u2.Version, a2.Version)
	}
	// The list says, per file, what the newest version of its own sequence is.
	rec := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts", fx.human(artUser))
	var out struct {
		Artifacts []verMeta `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Artifacts) != 4 {
		t.Fatalf("list = %s", rec.Body.String())
	}
	for _, a := range out.Artifacts {
		if a.LatestVersion != 2 || a.Version < 1 || a.Version > 2 {
			t.Errorf("%s v%d latest_version %d, want 2", a.Origin, a.Version, a.LatestVersion)
		}
	}
}

func TestArtifactVersionsConcurrentPublishesAreDistinctAndConsecutive(t *testing.T) {
	fx := newArtFx(t)
	const n = 10
	var wg sync.WaitGroup
	got := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := fx.upload(fx.sessTok, fx.sess, []byte(fmt.Sprint("body", i)), upOpts{name: "same.txt"})
			if rec.Code != http.StatusCreated {
				t.Errorf("publish %d = %d: %s", i, rec.Code, rec.Body.String())
				return
			}
			var m verMeta
			if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
				t.Error(err)
				return
			}
			got <- m.Version
		}(i)
	}
	wg.Wait()
	close(got)
	seen := map[int]bool{}
	for v := range got {
		if seen[v] {
			t.Errorf("version %d given twice", v)
		}
		seen[v] = true
	}
	for v := 1; v <= n; v++ {
		if !seen[v] {
			t.Errorf("version %d missing", v)
		}
	}
}

func TestArtifactVersionsTwentyFirstIsRefused(t *testing.T) {
	fx := newArtFx(t)
	const msg = "This file already has 20 versions; delete an old one first."
	for i := 1; i <= db.MaxArtifactVersions; i++ {
		if m := fx.pubVer(fx.sessTok, fx.sess, "big.txt", fmt.Sprint(i)); m.Version != i {
			t.Fatalf("publish %d = v%d", i, m.Version)
		}
	}
	rec := fx.upload(fx.sessTok, fx.sess, []byte("21"), upOpts{name: "big.txt"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), msg) {
		t.Fatalf("21st version = %d %s, want 409 %q", rec.Code, rec.Body.String(), msg)
	}
	// Nothing was stored for it, and another name still goes in.
	rows, _ := db.ListArtifacts(context.Background(), fx.api.dbPool, fx.sess)
	if len(rows) != db.MaxArtifactVersions {
		t.Errorf("%d rows after the refusal, want %d", len(rows), db.MaxArtifactVersions)
	}
	if m := fx.pubVer(fx.sessTok, fx.sess, "small.txt", "x"); m.Version != 1 {
		t.Errorf("another name = v%d", m.Version)
	}
	// Deleting an old version makes room.
	first := rows[len(rows)-1]
	if first.Version != 1 {
		t.Fatalf("oldest row is v%d", first.Version)
	}
	if rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/artifacts/"+first.ID, fx.human(artUser)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if m := fx.pubVer(fx.sessTok, fx.sess, "big.txt", "again"); m.Version != 21 {
		t.Errorf("after a delete = v%d, want v21", m.Version)
	}
}

func TestArtifactVersionsUserUploadsHaveTheSameCap(t *testing.T) {
	fx := newArtFx(t)
	old := maxUploadsPerSession
	maxUploadsPerSession = 50 // so the 20-version cap, not the 20-uploads cap, is what is exercised
	t.Cleanup(func() { maxUploadsPerSession = old })
	for i := 1; i <= db.MaxArtifactVersions; i++ {
		fx.attachVer(fx.sess, "in.txt", fmt.Sprint(i))
	}
	rec := fx.userUpload(fx.human(artUser), fx.sess, []byte("21"), "in.txt")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "20 versions") {
		t.Fatalf("21st upload = %d %s", rec.Code, rec.Body.String())
	}
}

func TestArtifactVersionsEveryVersionCountsTowardTheFileCap(t *testing.T) {
	fx := newArtFx(t)
	old := maxArtifactsPerSession
	maxArtifactsPerSession = 3
	t.Cleanup(func() { maxArtifactsPerSession = old })
	for i := 0; i < 3; i++ {
		fx.pubVer(fx.sessTok, fx.sess, "a.txt", fmt.Sprint(i))
	}
	rec := fx.upload(fx.sessTok, fx.sess, []byte("4"), upOpts{name: "a.txt"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "3 files") {
		t.Fatalf("4th file = %d %s, want the file cap 409", rec.Code, rec.Body.String())
	}
}

func TestArtifactVersionsDeletingV1LeavesV2AndTheNextIsV3(t *testing.T) {
	fx := newArtFx(t)
	v1 := fx.pubVer(fx.sessTok, fx.sess, "r.md", "one")
	v2 := fx.pubVer(fx.sessTok, fx.sess, "r.md", "two")
	if rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/artifacts/"+v1.ID, fx.human(artUser)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	rec := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts", fx.human(artUser))
	var out struct {
		Artifacts []verMeta `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Artifacts) != 1 || out.Artifacts[0].ID != v2.ID || out.Artifacts[0].Version != 2 || out.Artifacts[0].LatestVersion != 2 {
		t.Fatalf("after deleting v1 = %s", rec.Body.String())
	}
	v3 := fx.pubVer(fx.sessTok, fx.sess, "r.md", "three")
	if v3.Version != 3 || v3.Previous == nil || *v3.Previous != 2 {
		t.Fatalf("next = v%d previous %s, want v3 previous 2", v3.Version, prevOf(v3))
	}
}

func TestArtifactVersionsDownloadNames(t *testing.T) {
	fx := newArtFx(t)
	disp := func(sess, id string) string {
		t.Helper()
		rec := fx.get("GET", "/api/sessions/"+sess+"/artifacts/"+id+"/download", fx.human(artUser))
		if rec.Code != 200 {
			t.Fatalf("download = %d", rec.Code)
		}
		for h, want := range map[string]string{"X-Content-Type-Options": "nosniff", "Content-Security-Policy": "sandbox", "Cache-Control": "private, no-store"} {
			if rec.Header().Get(h) != want {
				t.Errorf("%s = %q, want %q", h, rec.Header().Get(h), want)
			}
		}
		return rec.Header().Get("Content-Disposition")
	}
	v1 := fx.pubVer(fx.sessTok, fx.sess, "report.md", "1")
	if got, want := disp(fx.sess, v1.ID), "attachment; filename*=UTF-8''report.md"; got != want {
		t.Errorf("sole version = %q, want %q", got, want)
	}
	v2 := fx.pubVer(fx.sessTok, fx.sess, "report.md", "2")
	if got, want := disp(fx.sess, v2.ID), "attachment; filename*=UTF-8''report.md"; got != want {
		t.Errorf("latest = %q, want %q", got, want)
	}
	if got, want := disp(fx.sess, v1.ID), "attachment; filename*=UTF-8''report-v1.md"; got != want {
		t.Errorf("older = %q, want %q", got, want)
	}
	n1 := fx.pubVer(fx.sessTok, fx.sess, "LICENSE", "1")
	fx.pubVer(fx.sessTok, fx.sess, "LICENSE", "2")
	if got, want := disp(fx.sess, n1.ID), "attachment; filename*=UTF-8''LICENSE-v1"; got != want {
		t.Errorf("no extension = %q, want %q", got, want)
	}
	// Deleting the latest promotes the previous one: it downloads under the plain name again.
	if rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/artifacts/"+v2.ID, fx.human(artUser)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if got, want := disp(fx.sess, v1.ID), "attachment; filename*=UTF-8''report.md"; got != want {
		t.Errorf("promoted = %q, want %q", got, want)
	}
	// A person's upload of the same name is a different sequence, so it is not "older" than the agent's.
	u := fx.attachVer(fx.sess, "report.md", "mine")
	if got, want := disp(fx.sess, u.ID), "attachment; filename*=UTF-8''report.md"; got != want {
		t.Errorf("user file = %q, want %q", got, want)
	}
	// raw (the in-app viewer) never sends a file name.
	if d := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+v1.ID+"/raw", fx.human(artUser)).Header().Get("Content-Disposition"); d != "" {
		t.Errorf("raw Content-Disposition = %q", d)
	}
}

func TestVersionedDownloadName(t *testing.T) {
	cases := []struct {
		name    string
		version int
		latest  int
		want    string
	}{
		{"report.md", 3, 3, "report.md"},
		{"report.md", 1, 3, "report-v1.md"},
		{"report.md", 2, 3, "report-v2.md"},
		{"LICENSE", 1, 2, "LICENSE-v1"},
		{"archive.tar.gz", 1, 2, "archive.tar-v1.gz"},
		{".env", 1, 2, ".env-v1"},
		{"résumé.txt", 4, 5, "résumé-v4.txt"},
		{"x.md", 0, 0, "x.md"},
		{"x.md", 5, 3, "x.md"},
	}
	for _, c := range cases {
		if got := versionedDownloadName(c.name, c.version, c.latest); got != c.want {
			t.Errorf("versionedDownloadName(%q, %d, %d) = %q, want %q", c.name, c.version, c.latest, got, c.want)
		}
	}
}

func TestArtifactVersionsEventPayloadCarriesVersion(t *testing.T) {
	fx := newArtFx(t)
	fx.pubVer(fx.sessTok, fx.sess, "r.md", "one")
	v2 := fx.pubVer(fx.sessTok, fx.sess, "r.md", "two")
	evs, err := db.ListAgentEvents(context.Background(), fx.api.dbPool, fx.sess, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var versions []int
	for _, e := range evs {
		if e.Kind != "artifact" {
			continue
		}
		var p verMeta
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, p.Version)
		if strings.Contains(e.Payload, `"previous"`) {
			t.Errorf("the event payload need not carry previous: %s", e.Payload)
		}
	}
	if fmt.Sprint(versions) != "[1 2]" {
		t.Fatalf("event versions = %v, want [1 2]", versions)
	}
	// The live fan-out carries it too.
	var last verMeta
	for i := 0; i < 2; i++ {
		select {
		case raw := <-fx.events:
			var ev struct {
				Payload verMeta `json:"payload"`
			}
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatal(err)
			}
			last = ev.Payload
		case <-time.After(time.Second):
			t.Fatal("no broadcast")
		}
	}
	if last.ID != v2.ID || last.Version != 2 {
		t.Errorf("broadcast payload = %+v, want v2 %s", last, v2.ID)
	}
}

func TestAttachmentsPodRoutesExposeVersions(t *testing.T) {
	fx := newArtFx(t)
	fx.attachVer(fx.sess, "brief.pdf", "1")
	b2 := fx.attachVer(fx.sess, "brief.pdf", "2")
	fx.attachVer(fx.sess, "other.txt", "x")
	fx.pubVer(fx.sessTok, fx.sess, "brief.pdf", "agent's own") // not an attachment, not part of the sequence
	rec := fx.get("GET", "/api/sessions/"+fx.sess+"/attachments", fx.sessTok)
	var out struct {
		Attachments []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Version       int    `json:"version"`
			LatestVersion int    `json:"latest_version"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range out.Attachments {
		got = append(got, fmt.Sprintf("%s v%d/%d", a.Name, a.Version, a.LatestVersion))
		if a.Name == "brief.pdf" && a.Version == 2 && a.ID != b2.ID {
			t.Errorf("v2 is %s, want %s", a.ID, b2.ID)
		}
	}
	// Oldest first, the order the person attached them.
	if want := "[brief.pdf v1/2 brief.pdf v2/2 other.txt v1/1]"; fmt.Sprint(got) != want {
		t.Fatalf("attachments = %v, want %s", got, want)
	}
}
