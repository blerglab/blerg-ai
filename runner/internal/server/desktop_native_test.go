package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestDesktopNativeHelpersRequireCurrentOwnerAndSessionScope(t *testing.T) {
	fx := newPrivFixture(t)
	ctx := t.Context()
	if _, err := db.AssignDesktop(ctx, fx.pool, fx.dc.ID, privAcctA, 1); err != nil {
		t.Fatal(err)
	}
	b := desktopauth.Binding{DesktopID: fx.dc.ID, OwnerID: privAcctA, Revision: 2, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
	gen := newUUID()
	if err := db.BindDesktopConnection(ctx, fx.pool, b, "workstation", "local", "/repos", gen); err != nil {
		t.Fatal(err)
	}
	fx.dc.DesktopBinding, fx.dc.DesktopGeneration = &b, gen
	token, err := db.MintSessionToken(ctx, fx.pool, fx.pubSession, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionSpawningAccount(ctx, fx.pool, fx.privSession, privAcctB); err != nil {
		t.Fatal(err)
	}
	other, err := db.MintSessionToken(ctx, fx.pool, fx.privSession, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := func(raw string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/agent/memories?project=app", nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		return r
	}
	w := httptest.NewRecorder()
	row, ok := fx.api.authDataPlane(w, request(token))
	if !ok || row.ID != fx.pubSession {
		t.Fatalf("native session refused: %d", w.Code)
	}
	if dataPlaneProject(httptest.NewRecorder(), row, "other/project") {
		t.Fatal("native helper crossed project scope")
	}
	// Invalid image bytes reach validation only after the scoped credential is
	// accepted. This exercises the real endpoint, without creating any artifact.
	imageRequest := func(raw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/screenshots", strings.NewReader("invalid image"))
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		fx.api.HandlePostScreenshot(w, r)
		return w
	}
	if w := imageRequest(token); w.Code != 400 {
		t.Fatalf("native publication endpoint refused scoped credential: %d", w.Code)
	}
	if w := imageRequest(other); w.Code != 401 {
		t.Fatal("publication endpoint accepted another owner")
	}
	if !fx.api.authNativePublish(httptest.NewRecorder(), request(token)) {
		t.Fatal("native publishing lost session auth")
	}
	for _, raw := range []string{other, "bdt_" + strings.Repeat("a", 64)} {
		w := httptest.NewRecorder()
		if _, ok := fx.api.authDataPlane(w, request(raw)); ok || w.Code != 401 {
			t.Fatal("wrong owner/desktop bearer reached project tools")
		}
		w = httptest.NewRecorder()
		if fx.api.authNativePublish(w, request(raw)) || w.Code != 401 {
			t.Fatal("wrong owner/desktop bearer reached publication")
		}
	}
	if err := db.BindDesktopConnection(ctx, fx.pool, b, "workstation", "local", "/repos", newUUID()); err != nil {
		t.Fatal(err)
	}
	if _, ok := fx.api.authDataPlane(httptest.NewRecorder(), request(token)); ok {
		t.Fatal("native helper used an obsolete connection generation")
	}
	if err := db.BindDesktopConnection(ctx, fx.pool, b, "workstation", "local", "/repos", gen); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AssignDesktop(ctx, fx.pool, fx.dc.ID, privAcctB, 2); err != nil {
		t.Fatal(err)
	}
	if _, ok := fx.api.authDataPlane(httptest.NewRecorder(), request(token)); ok {
		t.Fatal("native helper survived ownership reassignment")
	}
}
