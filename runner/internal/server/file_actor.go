package server

// Who may delete a session's files (#231). The session's owner and an administrator may delete
// any of them; a person who attached a file may delete that attachment, so a viewer who can
// attach to a shared session can also take back what they attached.
//
// The board's chat reaches the runner with the shared runner key, which has no account of its
// own. The board has already decided the signed-in person may change the session (card.write on
// its board) and names them in X-Blerg-Acting-Account. Only the runner key and a service
// credential, the install-level brokers, may name someone; the runner then checks that person
// with core on every request (runnerFileActor). X-Blerg-Acting-Admin, which the board still
// sends, is a hint the runner ignores: administrator status comes from core.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	actingAccountHeader = "X-Blerg-Acting-Account"
	actingAdminHeader   = "X-Blerg-Acting-Admin"
)

// fileActor is the person a file change is decided for, and how the request reached the runner
// (for the deletion audit): "browser", "contract" (an agent or human token acting for its own
// account) or "broker:<kind>" (the runner key or a service token naming a person).
type fileActor struct {
	account string
	admin   bool
	via     string
}

func browserFileActor(p identity.Principal) fileActor {
	return fileActor{account: p.Sub, admin: p.Has("account.manage"), via: "browser"}
}

// accountStanding is core's answer about a named account (POST /internal/accounts/standing).
type accountStanding struct {
	Exists bool `json:"exists"`
	Active bool `json:"active"`
	Admin  bool `json:"admin"`
}

// runnerFileActor is the contract principal's person: the account an agent or human token acts
// for, or the person a broker (the runner key, a service token) names. A named person is checked
// on every request (#231 rereview): core must say the account exists and is active, the person
// must be able to see the session themselves, and administrator status is the account's real
// role in core. X-Blerg-Acting-Admin is never trusted. Anything short of that (no name, an
// unknown or disabled account, a private session that is not theirs, core unreachable or not
// configured) is nobody: the broker may read the session's files but change none.
func (a *API) runnerFileActor(r *http.Request, p runnerPrincipal, sid string) fileActor {
	if p.Kind != runnerKeyPrincipalKind && p.Kind != string(identity.Service) {
		return fileActor{account: p.spawningAccountID(), admin: fileAdmin(p), via: "contract"}
	}
	via := "broker:" + p.Kind
	account := strings.TrimSpace(r.Header.Get(actingAccountHeader))
	if account == "" || len(account) > 256 {
		return fileActor{via: via}
	}
	st, err := a.fetchAccountStanding(r.Context(), account)
	if err != nil || !st.Exists || !st.Active {
		return fileActor{via: via}
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil || !canSeeAccount(account, row) {
		return fileActor{via: via}
	}
	return fileActor{account: account, admin: st.Admin, via: via}
}

// fetchAccountStanding asks core about one account; an error when core is not configured or
// cannot answer, so callers fail closed.
func (a *API) fetchAccountStanding(ctx context.Context, account string) (accountStanding, error) {
	var st accountStanding
	if a.coreURL == "" || a.coreInternalKey == "" {
		return st, errors.New("core is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]string{"account": account})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.coreURL+"/internal/accounts/standing", bytes.NewReader(raw))
	if err != nil {
		return st, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", a.coreInternalKey)
	resp, err := coreHTTPClient(nil).Do(req) // #nosec G704 -- Target is the operator-configured Core URL.
	if err != nil {
		return st, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return st, errors.New("account standing could not be read from core")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(body, &st)
	return st, err
}

// uploadAccount is who a contract upload is attributed to.
func (f fileActor) uploadAccount(p runnerPrincipal) string {
	if f.account != "" {
		return f.account
	}
	return p.spawningAccountID()
}

// managesFiles: the owner or an administrator, who may delete every file of the session.
func (f fileActor) managesFiles(sess *db.SessionRow) bool {
	return sess != nil && (f.admin || (f.account != "" && ownsSession(f.account, sess)))
}

// canDelete is managesFiles, or the person's own attachment.
func (f fileActor) canDelete(sess *db.SessionRow, a db.ArtifactRow) bool {
	return f.managesFiles(sess) || (f.account != "" && a.Origin == db.OriginUser && a.UploadedBy == f.account)
}
