package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/fileview"
)

func TestChildImportsStatusAndReportRoundTrip(t *testing.T) {
	t.Setenv("BLERG_RUNNER_DATA_DIR", t.TempDir())
	fx := newDelegFx(t)
	fx.api.registerArtifactRoutes(fx.mux)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, private: true})

	art, err := db.InsertArtifact(context.Background(), fx.pool, db.ArtifactRow{
		ID: newPublishID(), SessionID: child, Name: "result.bundle", Size: 1, ContentType: fileview.GitBundle, SHA256: "x",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	code, out, _ := fx.do("GET", childRoute(fx.parent, child, "/imports"), fx.parentTok, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("status (fresh): %d %v", code, out)
	}
	if int(out["next_n"].(float64)) != 1 {
		t.Fatalf("next_n (fresh) = %v, want 1", out["next_n"])
	}
	outstanding := out["outstanding"].([]any)
	if len(outstanding) != 1 || outstanding[0].(map[string]any)["id"] != art.ID {
		t.Fatalf("outstanding (fresh) = %v, want just %s", outstanding, art.ID)
	}

	code, out, _ = fx.do("POST", childRoute(fx.parent, child, "/imports"), fx.parentTok,
		map[string]any{"artifact_id": art.ID, "version": art.Version, "n": 1}, nil)
	if code != http.StatusOK {
		t.Fatalf("report: %d %v", code, out)
	}

	code, out, _ = fx.do("GET", childRoute(fx.parent, child, "/imports"), fx.parentTok, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("status (after report): %d %v", code, out)
	}
	if int(out["next_n"].(float64)) != 2 {
		t.Fatalf("next_n (after report) = %v, want 2", out["next_n"])
	}
	if outstanding := out["outstanding"].([]any); len(outstanding) != 0 {
		t.Fatalf("outstanding (after report) = %v, want none", outstanding)
	}
}

func TestChildImportsReportRefusesAWrongVersionOrForeignArtifact(t *testing.T) {
	t.Setenv("BLERG_RUNNER_DATA_DIR", t.TempDir())
	fx := newDelegFx(t)
	fx.api.registerArtifactRoutes(fx.mux)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, private: true})
	other, _ := fx.session(sessOpts{owner: "owner-1"})

	art, err := db.InsertArtifact(context.Background(), fx.pool, db.ArtifactRow{
		ID: newPublishID(), SessionID: child, Name: "result.bundle", Size: 1, ContentType: fileview.GitBundle, SHA256: "x",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	code, out, _ := fx.do("POST", childRoute(fx.parent, child, "/imports"), fx.parentTok,
		map[string]any{"artifact_id": art.ID, "version": art.Version + 1, "n": 1}, nil)
	if code != http.StatusNotFound {
		t.Fatalf("wrong version: %d %v", code, out)
	}

	otherArt, err := db.InsertArtifact(context.Background(), fx.pool, db.ArtifactRow{
		ID: newPublishID(), SessionID: other, Name: "result.bundle", Size: 1, ContentType: fileview.GitBundle, SHA256: "y",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ = fx.do("POST", childRoute(fx.parent, child, "/imports"), fx.parentTok,
		map[string]any{"artifact_id": otherArt.ID, "version": otherArt.Version, "n": 1}, nil)
	if code != http.StatusNotFound {
		t.Fatalf("foreign artifact under this child: %d %v", code, out)
	}
}

func TestChildImportsRefusesForeignParent(t *testing.T) {
	t.Setenv("BLERG_RUNNER_DATA_DIR", t.TempDir())
	fx := newDelegFx(t)
	fx.api.registerArtifactRoutes(fx.mux)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, private: true})
	foreign, foreignTok := fx.session(sessOpts{owner: "owner-1"})

	code, _, _ := fx.do("GET", childRoute(foreign, child, "/imports"), foreignTok, nil, nil)
	if code != http.StatusNotFound {
		t.Fatalf("unrelated parent reading imports status: %d", code)
	}
}
