package server

import (
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http/httptest"
	"testing"
)

func TestFolderProjectTokenCannotUseScalarRepo(t *testing.T) {
	s := &db.SessionRow{Repo: "org/one", WorkspaceKind: "folder"}
	if dataPlaneProject(httptest.NewRecorder(), s, "org/one") {
		t.Fatal("folder scalar authorized project")
	}
	s.WorkspaceKind = "repo"
	if !dataPlaneProject(httptest.NewRecorder(), s, "org/one") {
		t.Fatal("legacy repo denied")
	}
	if dataPlaneProject(httptest.NewRecorder(), s, "org/other") {
		t.Fatal("other project authorized")
	}
}
