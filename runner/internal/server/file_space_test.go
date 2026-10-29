package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestFileSpaceWaitingAndDeletePermissions(t *testing.T) {
	keepLimits(t)
	maxArtifactBytes = 8
	maxUploadBytesPerSession = 8
	fx := runnerFilesFx(t)
	old := fx.attach(fx.sess, "old.txt", []byte("123456"))
	rec := fx.userUpload(fx.human(artUser), fx.sess, []byte("new"), "new.txt")
	if rec.Code != http.StatusConflict {
		t.Fatalf("full upload = %d: %s", rec.Code, rec.Body.String())
	}
	var refusal struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil || refusal.Code != "file_space_required" {
		t.Fatalf("quota refusal = %s (%v)", rec.Body.String(), err)
	}
	for _, prefix := range []string{"/api/sessions/", "/api/runner/sessions/"} {
		path := prefix + fx.sess + "/artifacts"
		for _, account := range []string{artUser, artOther} {
			rec = fx.get("GET", path, fx.human(account))
			var list struct {
				CanDelete bool `json:"can_delete"`
				Limits    struct {
					Total int64 `json:"max_session_upload_bytes"`
				} `json:"limits"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.CanDelete != (account == artUser) || list.Limits.Total != 8 {
				t.Fatalf("%s %s list = %s (%v)", prefix, account, rec.Body.String(), err)
			}
		}
		rec = fx.get("DELETE", path+"/"+old.ID, fx.human(artOther))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("non-owner delete via %s = %d", prefix, rec.Code)
		}
	}
	rec = fx.get("DELETE", "/api/sessions/"+fx.sess+"/artifacts/"+old.ID, fx.human(artUser))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("owner delete = %d", rec.Code)
	}
	rec = fx.userUpload(fx.human(artUser), fx.sess, []byte("new"), "new.txt")
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload after freeing space = %d: %s", rec.Code, rec.Body.String())
	}
	rec = fx.userUpload(fx.human(artUser), fx.sess, []byte("123456789"), "large.txt")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("per-file excess = %d", rec.Code)
	}
}

func TestFileSpaceAdminDeleteKeepsPublishedHistory(t *testing.T) {
	fx := newArtFx(t)
	published := fx.publish(fx.sessTok, fx.sess, "report.txt", []byte("report"))
	admin := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: artOther, Aud: coreAuthAudience, Kind: "human", Sid: testSID, Caps: []string{coreAuthBrowserCap, "account.manage"}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/artifacts/"+published.ID, admin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("admin delete = %d: %s", rec.Code, rec.Body.String())
	}
	events, err := db.ListReelEvents(context.Background(), fx.api.dbPool, fx.sess, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	// The original artifact event remains; the existing reel marks it deleted when the file list lacks it.
	found := false
	for _, ev := range events {
		if ev.Kind == "artifact" {
			found = true
		}
	}
	if !found {
		t.Fatal("deletion erased the published history event")
	}
}

func TestFileSpaceStreamedQuotaRefusal(t *testing.T) {
	keepLimits(t)
	maxArtifactBytes, maxUploadBytesPerSession = 8, 8
	fx := newArtFx(t)
	fx.attach(fx.sess, "old.txt", []byte("123456"))
	req := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/uploads", bytes.NewBufferString("new"))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+fx.human(artUser))
	req.Header.Set("X-Artifact-Name", "new.txt")
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"file_space_required"`) {
		t.Fatalf("streamed quota refusal = %d: %s", rec.Code, rec.Body.String())
	}
	rows, err := db.ListArtifacts(t.Context(), fx.api.dbPool, fx.sess)
	if err != nil || len(rows) != 1 {
		t.Fatalf("refused upload left a row: %d, %v", len(rows), err)
	}
}

// fileReq sends one request with optional broker headers naming the acting person.
func (fx *artFx) fileReq(method, path, tok string, acting map[string]string) *httptest.ResponseRecorder {
	fx.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	for k, v := range acting {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	return rec
}

type spaceList struct {
	Artifacts []struct {
		ID        string `json:"id"`
		CanDelete bool   `json:"can_delete"`
	} `json:"artifacts"`
	CanDelete   bool `json:"can_delete"`
	CanIncrease bool `json:"can_increase"`
	Limits      struct {
		Total int64 `json:"max_session_upload_bytes"`
		Cap   int64 `json:"max_session_upload_cap_bytes"`
	} `json:"limits"`
}

func decodeSpace(t *testing.T, rec *httptest.ResponseRecorder) spaceList {
	t.Helper()
	var l spaceList
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	return l
}

// fakeCoreAccounts stands in for core: the session-storage settings (none saved) and the standing
// of each named account ("admin", "member", "disabled"; anything else does not exist). down makes
// the standing endpoint fail.
func fakeCoreAccounts(t *testing.T, fx *artFx, standings map[string]string, down *bool) {
	t.Helper()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Key") != "ik" {
			http.Error(w, "key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/internal/session-storage":
			_, _ = w.Write([]byte(`{"set":false}`))
		case "/internal/accounts/standing":
			if down != nil && *down {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			var body struct {
				Account string `json:"account"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch standings[body.Account] {
			case "admin":
				_, _ = w.Write([]byte(`{"exists":true,"active":true,"admin":true}`))
			case "member":
				_, _ = w.Write([]byte(`{"exists":true,"active":true,"admin":false}`))
			case "disabled":
				_, _ = w.Write([]byte(`{"exists":true,"active":false,"admin":false}`))
			default:
				_, _ = w.Write([]byte(`{"exists":false,"active":false,"admin":false}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(core.Close)
	fx.api.coreURL, fx.api.coreInternalKey = core.URL, "ik"
	fx.api.forgetSessionStorage()
}

// Finding 1 (#231 review): the board's chat forwards file requests with the shared runner key,
// which has no account. The board names the signed-in person; the runner decides for them.
func TestFileDeleteForTheBoardsVouchedPerson(t *testing.T) {
	fx := runnerFilesFx(t)
	fakeCoreAccounts(t, fx, map[string]string{artUser: "member", artOther: "member"}, nil)
	ownerFile := fx.attach(fx.sess, "owner.txt", []byte("owner"))
	// A viewer's attachment through the board: attributed to the person the board names.
	req := httptest.NewRequest("POST", "/api/runner/sessions/"+fx.sess+"/uploads", bytes.NewBufferString("viewer"))
	req.Header.Set("Authorization", "Bearer "+runnerTestKey)
	req.Header.Set("X-Artifact-Name", "viewer.txt")
	req.Header.Set(actingAccountHeader, artOther)
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("board upload = %d: %s", rec.Code, rec.Body.String())
	}
	var viewerFile upMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &viewerFile); err != nil {
		t.Fatal(err)
	}
	base := "/api/runner/sessions/" + fx.sess + "/artifacts"
	as := func(account string) map[string]string { return map[string]string{actingAccountHeader: account} }

	// The list answers for the vouched person: the owner may delete everything, the viewer only
	// their own attachment, and a broker that names nobody nothing.
	owner := decodeSpace(t, fx.fileReq("GET", base, runnerTestKey, as(artUser)))
	viewer := decodeSpace(t, fx.fileReq("GET", base, runnerTestKey, as(artOther)))
	nobody := decodeSpace(t, fx.fileReq("GET", base, runnerTestKey, nil))
	if !owner.CanDelete || viewer.CanDelete || nobody.CanDelete {
		t.Fatalf("session-wide can_delete: owner %v viewer %v nobody %v", owner.CanDelete, viewer.CanDelete, nobody.CanDelete)
	}
	perFile := func(l spaceList, id string) bool {
		for _, a := range l.Artifacts {
			if a.ID == id {
				return a.CanDelete
			}
		}
		t.Fatalf("file %s not listed", id)
		return false
	}
	if !perFile(owner, ownerFile.ID) || !perFile(owner, viewerFile.ID) || perFile(viewer, ownerFile.ID) || !perFile(viewer, viewerFile.ID) || perFile(nobody, viewerFile.ID) {
		t.Fatal("per-file can_delete does not match owner / own attachment")
	}

	if rec := fx.fileReq("DELETE", base+"/"+ownerFile.ID, runnerTestKey, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("broker delete naming nobody = %d", rec.Code)
	}
	if rec := fx.fileReq("DELETE", base+"/"+ownerFile.ID, runnerTestKey, as(artOther)); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "only the files you attached") {
		t.Fatalf("viewer deleting the owner's file = %d: %s", rec.Code, rec.Body.String())
	}
	// The headers mean nothing on a credential that is not a broker: an agent token acting for
	// the viewer cannot claim to be the owner.
	if rec := fx.fileReq("DELETE", base+"/"+ownerFile.ID, fx.agent(artOther), map[string]string{actingAccountHeader: artUser, actingAdminHeader: "1"}); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("agent token borrowing the owner's name = %d", rec.Code)
	}
	// #231 rereview: the admin flag is the broker's word, never trusted; a member named with it
	// is still a member.
	if rec := fx.fileReq("DELETE", base+"/"+ownerFile.ID, runnerTestKey, map[string]string{actingAccountHeader: artOther, actingAdminHeader: "1"}); rec.Code != http.StatusForbidden {
		t.Fatalf("member named with a forged admin flag deleting the owner's file = %d", rec.Code)
	}
	if rec := fx.fileReq("DELETE", base+"/"+viewerFile.ID, runnerTestKey, as(artOther)); rec.Code != http.StatusNoContent {
		t.Fatalf("viewer deleting their own attachment = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := fx.fileReq("DELETE", base+"/"+ownerFile.ID, runnerTestKey, as(artUser)); rec.Code != http.StatusNoContent {
		t.Fatalf("owner delete through the board = %d: %s", rec.Code, rec.Body.String())
	}
	// Finding 4 of the rereview: each deletion is recorded with the person and the path.
	rows, err := fx.api.dbPool.Query(t.Context(), `SELECT artifact_id, actor, actor_admin, via FROM session_file_deletions WHERE session_id = $1 ORDER BY id`, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	var audit []string
	for rows.Next() {
		var id, actor, via string
		var admin bool
		if err := rows.Scan(&id, &actor, &admin, &via); err != nil {
			t.Fatal(err)
		}
		audit = append(audit, fmt.Sprintf("%s %s %v %s", id, actor, admin, via))
	}
	rows.Close()
	want := []string{viewerFile.ID + " " + artOther + " false broker:runner_key", ownerFile.ID + " " + artUser + " false broker:runner_key"}
	if strings.Join(audit, "|") != strings.Join(want, "|") {
		t.Fatalf("deletion audit = %v, want %v", audit, want)
	}
}

// #231 rereview, finding 1: a named person is checked with core on every request. An unknown or
// disabled account, a private session that is not theirs, or core being unreachable leaves the
// broker acting for nobody; a service token is held to the same rule; admin is core's answer.
func TestBrokerNamedPersonIsRevalidated(t *testing.T) {
	keepLimits(t)
	maxArtifactBytes, maxUploadBytesPerSession = 8, 8
	fx := runnerFilesFx(t)
	down := false
	fakeCoreAccounts(t, fx, map[string]string{artUser: "member", artOther: "disabled", "boss": "admin"}, &down)
	file := fx.attach(fx.sess, "owner.txt", []byte("owner"))
	del := "/api/runner/sessions/" + fx.sess + "/artifacts/" + file.ID
	raise := "/api/runner/sessions/" + fx.sess + "/storage"
	service := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: "svc-1", Aud: coreAuthAudience, Kind: "service", Caps: []string{coreAuthRunnerCap}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	for _, tok := range []string{runnerTestKey, service} {
		for _, named := range []map[string]string{
			{actingAccountHeader: "ghost", actingAdminHeader: "1"},  // unknown account, forged admin
			{actingAccountHeader: artOther, actingAdminHeader: "1"}, // disabled account
			{actingAccountHeader: artUser, actingAdminHeader: "1"},  // a member with a forged admin flag (not the owner of privSess)
		} {
			if rec := fx.fileReq("POST", raise, tok, named); rec.Code != http.StatusForbidden {
				t.Errorf("raise for %v = %d", named, rec.Code)
			}
		}
		for _, named := range []map[string]string{{actingAccountHeader: "ghost"}, {actingAccountHeader: artOther}} {
			if rec := fx.fileReq("DELETE", del, tok, named); rec.Code != http.StatusForbidden {
				t.Errorf("delete for %v = %d", named, rec.Code)
			}
			if l := decodeSpace(t, fx.fileReq("GET", "/api/runner/sessions/"+fx.sess+"/artifacts", tok, named)); l.CanDelete || l.CanIncrease {
				t.Errorf("list for %v grants %+v", named, l)
			}
		}
	}
	// A private session stays its owner's alone: a broker cannot reach it, whoever it names.
	if rec := fx.fileReq("DELETE", "/api/runner/sessions/"+fx.privSess+"/artifacts/"+file.ID, runnerTestKey, map[string]string{actingAccountHeader: artOther}); rec.Code != http.StatusNotFound {
		t.Errorf("broker delete on a private session = %d", rec.Code)
	}
	// Core unreachable: fail closed, even for the session's owner.
	down = true
	if rec := fx.fileReq("DELETE", del, runnerTestKey, map[string]string{actingAccountHeader: artUser}); rec.Code != http.StatusForbidden {
		t.Fatalf("owner delete with core down = %d", rec.Code)
	}
	down = false
	// Core says administrator (without any admin header): allowed, and audited as that person.
	if rec := fx.fileReq("POST", raise, service, map[string]string{actingAccountHeader: "boss"}); rec.Code != http.StatusOK {
		t.Fatalf("raise for a real admin = %d: %s", rec.Code, rec.Body.String())
	}
	var actor string
	if err := fx.api.dbPool.QueryRow(t.Context(), `SELECT actor FROM session_storage_audit WHERE session_id = $1`, fx.sess).Scan(&actor); err != nil || actor != "boss" {
		t.Fatalf("storage audit actor = %q (%v)", actor, err)
	}
	if rec := fx.fileReq("DELETE", del, runnerTestKey, map[string]string{actingAccountHeader: "boss"}); rec.Code != http.StatusNoContent {
		t.Fatalf("delete for a real admin = %d", rec.Code)
	}
	var via string
	var admin bool
	if err := fx.api.dbPool.QueryRow(t.Context(), `SELECT actor, actor_admin, via FROM session_file_deletions WHERE artifact_id = $1`, file.ID).Scan(&actor, &admin, &via); err != nil || actor != "boss" || !admin || via != "broker:runner_key" {
		t.Fatalf("deletion audit = %q %v %q (%v)", actor, admin, via, err)
	}
}

// A browser deletion is audited too.
func TestBrowserDeletionIsAudited(t *testing.T) {
	fx := newArtFx(t)
	file := fx.attach(fx.sess, "mine.txt", []byte("mine"))
	if rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/artifacts/"+file.ID, fx.human(artUser)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	var actor, via string
	if err := fx.api.dbPool.QueryRow(t.Context(), `SELECT actor, via FROM session_file_deletions WHERE artifact_id = $1`, file.ID).Scan(&actor, &via); err != nil || actor != artUser || via != "browser" {
		t.Fatalf("audit = %q %q (%v)", actor, via, err)
	}
}

// Finding 2: a person who may attach to a shared session may take back their own attachment.
func TestFileDeleteOwnAttachmentOnTheBrowserRoute(t *testing.T) {
	fx := newArtFx(t)
	ownerFile := fx.attach(fx.sess, "owner.txt", []byte("owner"))
	rec := fx.userUpload(fx.human(artOther), fx.sess, []byte("mine"), "mine.txt")
	if rec.Code != http.StatusCreated {
		t.Fatalf("viewer upload = %d: %s", rec.Code, rec.Body.String())
	}
	var mine upMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &mine); err != nil {
		t.Fatal(err)
	}
	base := "/api/sessions/" + fx.sess + "/artifacts/"
	if rec := fx.get("DELETE", base+ownerFile.ID, fx.human(artOther)); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer deleting the owner's attachment = %d", rec.Code)
	}
	if rec := fx.get("DELETE", base+mine.ID, fx.human(artOther)); rec.Code != http.StatusNoContent {
		t.Fatalf("viewer deleting their own attachment = %d: %s", rec.Code, rec.Body.String())
	}
}

func (fx *artFx) admin(account string) string {
	return mintRunnerToken(fx.t, fx.priv, "core-1", identity.Claims{Sub: account, Aud: coreAuthAudience, Kind: "human", Sid: testSID,
		Caps: []string{coreAuthBrowserCap, "account.manage"}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
}

// Owner decision: an administrator raises one session's attachment total in steps up to the
// install maximum; each raise is audited, and uploads then fit.
func TestIncreaseStorageRaisesOneSessionUpToTheMaximum(t *testing.T) {
	keepLimits(t)
	maxArtifactBytes, maxUploadBytesPerSession = 8, 8
	fx := runnerFilesFx(t)
	// Nothing saved in core: the env value (8 bytes here) is the default, 5000 MiB the maximum.
	fakeCoreAccounts(t, fx, map[string]string{artUser: "member", "boss": "admin"}, nil)

	fx.attach(fx.sess, "old.txt", []byte("123456"))
	if rec := fx.userUpload(fx.human(artUser), fx.sess, []byte("new"), "new.txt"); rec.Code != http.StatusConflict {
		t.Fatalf("full upload = %d", rec.Code)
	}
	list := "/api/sessions/" + fx.sess + "/artifacts"
	if l := decodeSpace(t, fx.get("GET", list, fx.human(artUser))); l.CanIncrease || l.Limits.Total != 8 {
		t.Fatalf("owner (not admin) list: %+v", l)
	}
	if l := decodeSpace(t, fx.get("GET", list, fx.admin(artOther))); !l.CanIncrease || l.Limits.Cap <= l.Limits.Total {
		t.Fatalf("admin list: %+v", l)
	}
	raise := "/api/sessions/" + fx.sess + "/storage"
	if rec := fx.get("POST", raise, fx.human(artUser)); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Ask an admin") {
		t.Fatalf("non-admin raise = %d: %s", rec.Code, rec.Body.String())
	}
	rec := fx.get("POST", raise, fx.admin(artOther))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin raise = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := fx.userUpload(fx.human(artUser), fx.sess, []byte("new"), "new.txt"); rec.Code != http.StatusCreated {
		t.Fatalf("upload after the raise = %d: %s", rec.Code, rec.Body.String())
	}
	var actor string
	var prev, next int64
	if err := fx.api.dbPool.QueryRow(t.Context(), `SELECT actor, previous_bytes, new_bytes FROM session_storage_audit WHERE session_id = $1`, fx.sess).Scan(&actor, &prev, &next); err != nil {
		t.Fatal(err)
	}
	if actor != artOther || prev != 8 || next != 8+500<<20 {
		t.Fatalf("audit row: %s %d -> %d", actor, prev, next)
	}
	// Other sessions keep the default.
	if l := decodeSpace(t, fx.get("GET", "/api/sessions/"+fx.otherSess+"/artifacts", fx.human(artOther))); l.Limits.Total != 8 {
		t.Fatalf("another session's total changed: %+v", l)
	}
	// Up to the maximum (5000 MiB by default with nothing saved), then refused.
	for i := 0; i < 20; i++ {
		rec = fx.get("POST", raise, fx.admin(artOther))
		if rec.Code != http.StatusOK {
			break
		}
	}
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "storage_at_maximum") {
		t.Fatalf("raise past the maximum = %d: %s", rec.Code, rec.Body.String())
	}
	if l := decodeSpace(t, fx.get("GET", list, fx.admin(artOther))); l.CanIncrease || l.Limits.Total != l.Limits.Cap {
		t.Fatalf("at the maximum: %+v", l)
	}
	// The board's chat raises for the person it names when core says they are an administrator;
	// a member is refused, with or without the (ignored) admin flag.
	contract := "/api/runner/sessions/" + fx.otherSess + "/storage"
	for _, named := range []map[string]string{{actingAccountHeader: artUser}, {actingAccountHeader: artUser, actingAdminHeader: "1"}} {
		if rec := fx.fileReq("POST", contract, runnerTestKey, named); rec.Code != http.StatusForbidden {
			t.Fatalf("board raise for a member %v = %d", named, rec.Code)
		}
	}
	if rec := fx.fileReq("POST", contract, runnerTestKey, map[string]string{actingAccountHeader: "boss"}); rec.Code != http.StatusOK {
		t.Fatalf("board raise for an admin = %d: %s", rec.Code, rec.Body.String())
	}
}

// Core's saved settings win over the environment: the default and the maximum come from core.
func TestSessionStorageUsesCoresSavedSettings(t *testing.T) {
	keepLimits(t)
	maxArtifactBytes, maxUploadBytesPerSession = 8, 8
	fx := newArtFx(t)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"set":true,"default_upload_mb":2,"max_upload_mb":3}`))
	}))
	t.Cleanup(core.Close)
	fx.api.coreURL, fx.api.coreInternalKey = core.URL, "ik"
	fx.api.forgetSessionStorage()
	l := decodeSpace(t, fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts", fx.admin(artUser)))
	if l.Limits.Total != 2<<20 || l.Limits.Cap != 3<<20 || !l.CanIncrease {
		t.Fatalf("core settings not used: %+v", l)
	}
	if rec := fx.get("POST", "/api/sessions/"+fx.sess+"/storage", fx.admin(artUser)); rec.Code != http.StatusOK {
		t.Fatalf("raise = %d", rec.Code)
	}
	if l := decodeSpace(t, fx.get("GET", "/api/sessions/"+fx.sess+"/artifacts", fx.admin(artUser))); l.Limits.Total != 3<<20 || l.CanIncrease {
		t.Fatalf("a step past the maximum is capped at it: %+v", l)
	}
}

// #231 rereview 2, finding 3: the agent's own unpublish (blerg-runner unpublish, the session's
// token) is audited in the same transaction as every other deletion.
func TestSessionTokenUnpublishIsAudited(t *testing.T) {
	fx := newArtFx(t)
	published := fx.publish(fx.sessTok, fx.sess, "report.md", []byte("report"))
	if rec := fx.get("DELETE", "/api/sessions/"+fx.sess+"/files/"+published.ID, fx.sessTok); rec.Code != http.StatusNoContent {
		t.Fatalf("unpublish = %d: %s", rec.Code, rec.Body.String())
	}
	var actor, via, name string
	var version int
	if err := fx.api.dbPool.QueryRow(t.Context(), `SELECT actor, via, name, version FROM session_file_deletions WHERE artifact_id = $1`, published.ID).Scan(&actor, &via, &name, &version); err != nil {
		t.Fatalf("no deletion audit row: %v", err)
	}
	if actor != "session:"+fx.sess || via != "session_token" || name != "report.md" || version != 1 {
		t.Fatalf("audit = %q %q %q v%d", actor, via, name, version)
	}
}
