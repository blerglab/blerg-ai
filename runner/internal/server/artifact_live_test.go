package server

// Live files (BLG-470): `publish --live` (X-Artifact-Live: true) updates the same artifact in
// place instead of adding a version, and a Snapshot saves its current content as a normal
// versioned file. Same fixture as the artifact tests.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type liveMeta struct {
	artMeta
	Origin        string `json:"origin"`
	Version       int    `json:"version"`
	LatestVersion int    `json:"latest_version"`
	Live          bool   `json:"live"`
	UpdatedAt     string `json:"updated_at"`
}

func (fx *artFx) publishLive(tok, sess, name string, body []byte) (liveMeta, *httptest.ResponseRecorder) {
	fx.t.Helper()
	rec := fx.upload(tok, sess, body, upOpts{name: name, hdr: map[string]string{"X-Artifact-Live": "true"}})
	var m liveMeta
	if rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			fx.t.Fatal(err)
		}
	}
	return m, rec
}

func TestLiveUploadRepublishNeverAddsAVersionAndKeepsOneID(t *testing.T) {
	fx := newArtFx(t)
	first, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", tinyPNG)
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("first live publish = %d: %s", rec.Code, rec.Body.String())
	}
	if !first.Live || first.Version != 1 {
		t.Fatalf("first live publish = live %v version %d; want true, 1", first.Live, first.Version)
	}

	second, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("not actually a png, but that's fine"))
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("second live publish = %d: %s", rec.Code, rec.Body.String())
	}
	if second.ID != first.ID {
		t.Fatalf("republish got a new id: %s vs %s; a live file must keep one id", second.ID, first.ID)
	}
	if second.Version != 1 || second.Live != true {
		t.Fatalf("second live publish = live %v version %d; want true, 1 (no new version)", second.Live, second.Version)
	}

	third, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("a third republish"))
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("third live publish = %d: %s", rec.Code, rec.Body.String())
	}
	if third.ID != first.ID || third.Version != 1 {
		t.Fatalf("third live publish = id %s version %d; want %s, 1", third.ID, third.Version, first.ID)
	}

	// Exactly one file (not three versions) is on the session's list.
	lrec := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts", fx.human(artUser))
	if lrec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", lrec.Code, lrec.Body.String())
	}
	var list struct {
		Artifacts []liveMeta `json:"artifacts"`
	}
	if err := json.Unmarshal(lrec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Artifacts) != 1 {
		t.Fatalf("session has %d files after 3 live republishes; want 1", len(list.Artifacts))
	}
}

// Two republishes inside the same wall-clock second must still report updated_at values a client
// can order correctly (BLG-470 round 3 #1): truncated to whole seconds, they tie, and a tie falls
// back to comparing sha256, which has nothing to do with which write actually landed last.
func TestLiveUploadUpdatedAtOrdersTwoRepublishesInTheSameSecond(t *testing.T) {
	fx := newArtFx(t)
	first, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("v1 bytes"))
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("first live publish = %d: %s", rec.Code, rec.Body.String())
	}
	second, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("v2 bytes"))
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("second live publish = %d: %s", rec.Code, rec.Body.String())
	}
	t1, err := time.Parse(time.RFC3339Nano, first.UpdatedAt)
	if err != nil {
		t.Fatalf("parse first.updated_at %q: %v", first.UpdatedAt, err)
	}
	t2, err := time.Parse(time.RFC3339Nano, second.UpdatedAt)
	if err != nil {
		t.Fatalf("parse second.updated_at %q: %v", second.UpdatedAt, err)
	}
	if !t2.After(t1) {
		t.Fatalf("second.updated_at (%s) is not after first's (%s): two republishes that land inside the "+
			"same second must still be distinguishable without resorting to a sha comparison", second.UpdatedAt, first.UpdatedAt)
	}
}

// Serving a live file whose bytes on disk do not match its row — the shape a crash between
// UpdateLiveArtifactContent's commit and its rename leaves behind — must flag it rather than
// silently serve a mismatch with nothing to notice (BLG-470 round 3 #3).
func TestLiveRawFlagsAMismatchBetweenTheRowAndDiskAsUpdating(t *testing.T) {
	fx := newArtFx(t)
	live, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("the committed bytes"))
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("live publish = %d: %s", rec.Code, rec.Body.String())
	}
	// A clean read right after publishing must not be flagged.
	clean := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+live.ID+"/raw", fx.human(artUser))
	if clean.Code != http.StatusOK {
		t.Fatalf("raw before tampering = %d: %s", clean.Code, clean.Body.String())
	}
	if got := clean.Header().Get("X-Artifact-Live-Updating"); got != "" {
		t.Fatalf("X-Artifact-Live-Updating = %q on a clean read; want it absent", got)
	}

	// Models the crash: the row says "the committed bytes", but the rename that should have put
	// them on disk never ran, so the file still holds something else — here, nothing at all was
	// renamed over it, so writing different bytes over the live path stands in for "the old
	// content the interrupted rename left behind".
	if err := os.WriteFile(filepath.Join(fx.path(fx.sess, live.ID), "chart.png"), []byte("stale bytes nobody renamed"), 0o600); err != nil {
		t.Fatal(err)
	}

	tampered := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+live.ID+"/raw", fx.human(artUser))
	if tampered.Code != http.StatusOK {
		t.Fatalf("raw after tampering = %d: %s", tampered.Code, tampered.Body.String())
	}
	if got := tampered.Header().Get("X-Artifact-Live-Updating"); got != "true" {
		t.Fatalf("X-Artifact-Live-Updating = %q after the row and disk diverged; want %q", got, "true")
	}
}

func TestLiveUploadAnotherDeviceSeesTheLatest(t *testing.T) {
	fx := newArtFx(t)
	first, _ := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("v1 bytes"))
	second, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("v2 bytes, refreshed"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("republish = %d: %s", rec.Code, rec.Body.String())
	}
	if second.ID != first.ID {
		t.Fatalf("expected the same id across republishes")
	}
	// A second, independent read (as if from another device/browser) of the SAME id and the SAME
	// raw route sees the republished bytes, not the ones from the first publish.
	raw := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+first.ID+"/raw", fx.human(artUser))
	if raw.Code != http.StatusOK {
		t.Fatalf("raw read = %d: %s", raw.Code, raw.Body.String())
	}
	if got := raw.Body.String(); got != "v2 bytes, refreshed" {
		t.Fatalf("raw read = %q; want the latest republished content", got)
	}
}

func TestLiveUploadSwitchFromVersionedAddsOneVersionThenStopsGrowing(t *testing.T) {
	fx := newArtFx(t)
	v1 := fx.publish(fx.sessTok, fx.sess, "report.csv", []byte("a,b\n1,2\n"))
	live, rec := fx.publishLive(fx.sessTok, fx.sess, "report.csv", []byte("a,b\n3,4\n"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("switch to live = %d: %s", rec.Code, rec.Body.String())
	}
	if live.ID == v1.ID {
		t.Fatalf("the switch should start a new row (the old version keeps its own id)")
	}
	if !live.Live || live.Version != 2 {
		t.Fatalf("switch row = live %v version %d; want true, 2", live.Live, live.Version)
	}
	again, rec := fx.publishLive(fx.sessTok, fx.sess, "report.csv", []byte("a,b\n5,6\n"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("republish after switch = %d: %s", rec.Code, rec.Body.String())
	}
	if again.ID != live.ID || again.Version != 2 {
		t.Fatalf("republish after switch = id %s version %d; want %s, 2 (no further growth)", again.ID, again.Version, live.ID)
	}
}

func TestSnapshotCreatesExactlyOneVersionAndKeepsTheLiveFileLive(t *testing.T) {
	fx := newArtFx(t)
	live, _ := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("current chart bytes"))
	snaprec := fx.get("POST", "/api/sessions/"+fx.sess+"/artifacts/"+live.ID+"/snapshot", fx.human(artUser))
	if snaprec.Code != http.StatusCreated {
		t.Fatalf("snapshot = %d: %s", snaprec.Code, snaprec.Body.String())
	}
	var snap liveMeta
	if err := json.Unmarshal(snaprec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.ID == live.ID {
		t.Fatalf("the snapshot must be its own file, not the live one")
	}
	if snap.Live {
		t.Fatalf("a snapshot is a normal versioned file, not live")
	}
	if snap.Version != 1 {
		t.Fatalf("snapshot version = %d, want 1 (exactly one version)", snap.Version)
	}
	if snap.Name == "chart.png" {
		t.Fatalf("the snapshot should be named with the time, not reuse the live file's name")
	}
	raw := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+snap.ID+"/raw", fx.human(artUser))
	if raw.Code != http.StatusOK || raw.Body.String() != "current chart bytes" {
		t.Fatalf("snapshot bytes = %d %q; want 200 with the live file's content at snapshot time", raw.Code, raw.Body.String())
	}

	// The original stays live and reachable at its own id, unaffected by the snapshot.
	liverec := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+live.ID+"/raw", fx.human(artUser))
	if liverec.Code != http.StatusOK || liverec.Body.String() != "current chart bytes" {
		t.Fatalf("the live file's own read = %d %q", liverec.Code, liverec.Body.String())
	}
	livePub, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("after the snapshot"))
	if rec.Code != http.StatusCreated || livePub.ID != live.ID || livePub.Version != 1 {
		t.Fatalf("republishing the live file after a snapshot = %d id %s version %d; want 201, %s, 1", rec.Code, livePub.ID, livePub.Version, live.ID)
	}
}

func TestPlainPublishRefusedOnALiveName(t *testing.T) {
	fx := newArtFx(t)
	live, _ := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("v1"))
	rec := fx.upload(fx.sessTok, fx.sess, []byte("v2 without --live"), upOpts{name: "chart.png"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("plain publish over a live name = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	// Refused, so the live row is exactly as it was: still the one and only row of that name.
	raw := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+live.ID+"/raw", fx.human(artUser))
	if raw.Code != http.StatusOK || raw.Body.String() != "v1" {
		t.Fatalf("live file after the refused publish = %d %q; want unchanged", raw.Code, raw.Body.String())
	}
}

// Two concurrent `--live` republishes of the same file must leave the row's own size matching
// the bytes actually on disk: the advisory lock has to cover the file rename, not just the row
// update, or one request's bytes can land after another's commit with a mismatched row.
func TestLiveUploadConcurrentRepublishesKeepBytesAndRowInSync(t *testing.T) {
	fx := newArtFx(t)
	if _, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("seed")); rec.Code != http.StatusCreated {
		t.Fatalf("seed publish: %d", rec.Code)
	}
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := strings.Repeat(fmt.Sprintf("%02d", i), 50+i) // a distinct, recognizable size per request
			rec := fx.upload(fx.sessTok, fx.sess, []byte(body), upOpts{name: "chart.png", hdr: map[string]string{"X-Artifact-Live": "true"}})
			if rec.Code != http.StatusCreated {
				t.Errorf("republish %d = %d: %s", i, rec.Code, rec.Body.String())
			}
		}(i)
	}
	wg.Wait()

	listRec := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts", fx.human(artUser))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", listRec.Code, listRec.Body.String())
	}
	var list struct {
		Artifacts []liveMeta `json:"artifacts"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Artifacts) != 1 {
		t.Fatalf("session has %d files after %d concurrent live republishes; want 1", len(list.Artifacts), n)
	}
	row := list.Artifacts[0]

	rawRec := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts/"+row.ID+"/raw", fx.human(artUser))
	if rawRec.Code != http.StatusOK {
		t.Fatalf("raw = %d: %s", rawRec.Code, rawRec.Body.String())
	}
	onDisk := int64(rawRec.Body.Len())
	if onDisk != row.Size {
		t.Fatalf("row says size %d but the bytes on disk are %d: the row and the file disagree about which republish won", row.Size, onDisk)
	}
}

func TestSnapshotRefusedOnAnOrdinaryVersionedFile(t *testing.T) {
	fx := newArtFx(t)
	v1 := fx.publish(fx.sessTok, fx.sess, "report.md", []byte("hello"))
	rec := fx.get("POST", "/api/sessions/"+fx.sess+"/artifacts/"+v1.ID+"/snapshot", fx.human(artUser))
	if rec.Code != http.StatusConflict {
		t.Fatalf("snapshot of a non-live file = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// An in-place republish of a live file must not grow the session's persisted transcript: the
// first publish (the switch to live) is the only "artifact" event ever recorded for that id.
// Later republishes are pushed live (transient, seq 0) and never reach agent_events at all.
func TestLiveUploadRepublishNeverGrowsTheTranscript(t *testing.T) {
	fx := newArtFx(t)
	first, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("v1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first live publish = %d: %s", rec.Code, rec.Body.String())
	}
	for i, body := range [][]byte{[]byte("v2"), []byte("v3"), []byte("v4")} {
		if _, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", body); rec.Code != http.StatusCreated {
			t.Fatalf("republish %d = %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	rows, err := db.ListAgentEvents(context.Background(), fx.api.dbPool, fx.sess, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var artifactEvents int
	for _, r := range rows {
		if r.Kind == "artifact" {
			artifactEvents++
			if !strings.Contains(r.Payload, first.ID) {
				t.Fatalf("a persisted artifact event names a different id: %s", r.Payload)
			}
		}
	}
	if artifactEvents != 1 {
		t.Fatalf("session has %d persisted artifact events after 1 switch + 3 republishes; want exactly 1 (the switch)", artifactEvents)
	}
}

// The live push itself must be marked transient (never persisted), the same way a streaming
// delta is, so a client that does apply it to its own event store (rather than routing it through
// onArtifactUpdated) still cannot mistake it for transcript history.
func TestLiveUploadPushIsTransient(t *testing.T) {
	fx := newArtFx(t)
	if _, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("v1")); rec.Code != http.StatusCreated {
		t.Fatalf("first live publish = %d: %s", rec.Code, rec.Body.String())
	}
	msgs := make(chan []byte, 8)
	fx.hub.Subscribe(fx.sess, "live-watcher", msgs)
	if _, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("v2")); rec.Code != http.StatusCreated {
		t.Fatalf("republish = %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case raw := <-msgs:
		var ev protocol.AgentEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Kind != "artifact" || !ev.Transient || ev.Seq != 0 {
			t.Fatalf("live push = kind %q transient %v seq %d; want artifact, true, 0", ev.Kind, ev.Transient, ev.Seq)
		}
	default:
		t.Fatal("no live push reached the subscriber")
	}
}

// The session's own list must report the live row as latest_version even when a row inserted
// directly (simulating an old server during a rolling update, which does not know the live
// column and so can publish a plain version over a live name) outranks it by version number.
func TestArtifactsListLatestVersionPrefersTheLiveRowOverAnOrphan(t *testing.T) {
	fx := newArtFx(t)
	live, rec := fx.publishLive(fx.sessTok, fx.sess, "chart.png", []byte("v2-live"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("switch to live = %d: %s", rec.Code, rec.Body.String())
	}
	if live.Version != 1 {
		t.Fatalf("live version = %d, want 1 (first publish of this name)", live.Version)
	}
	// An old server's plain insert: bypasses this server's own refusal, as an old one would.
	if _, err := db.InsertArtifactCapped(context.Background(), fx.api.dbPool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 8001), SessionID: fx.sess, Name: "chart.png", Size: 3, ContentType: "image/png", SHA256: "v2-orphan",
	}, 0, 0); err != nil {
		t.Fatalf("simulated old-server insert: %v", err)
	}
	listRec := fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts", fx.human(artUser))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", listRec.Code, listRec.Body.String())
	}
	var out struct {
		Artifacts []liveMeta `json:"artifacts"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, a := range out.Artifacts {
		if a.ID == live.ID && a.LatestVersion != 1 {
			t.Fatalf("the live row's latest_version = %d; want 1 (its own), not 2 (the orphan that outranks it)", a.LatestVersion)
		}
	}
}
