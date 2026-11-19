package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/skills"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const skillPreference = "workflow_skills"
const skillPolicyEnv = "BLERG_RUNNER_SKILL_POLICY"

type skillSettings struct {
	Install    skills.Overrides       `json:"install"`
	Account    skills.Overrides       `json:"account"`
	Workspace  skills.Overrides       `json:"workspace"`
	Effective  skills.Policy          `json:"effective"`
	Admin      bool                   `json:"admin"`
	Workspaces []skillWorkspaceOption `json:"workspaces"`
}

type skillWorkspaceOption struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func readSkillSettings(ctx context.Context, pool *pgxpool.Pool, account, workspace string) (skillSettings, error) {
	var out skillSettings
	if pool == nil {
		out.Effective = skills.Resolve(false)
		return out, nil
	}
	stored, err := db.GetSettings(ctx, pool)
	if err != nil {
		return out, err
	}
	for key, dst := range map[string]**bool{"test-driven-development": &out.Install.TDD, "ux-design": &out.Install.UX, "visualizing": &out.Install.Visualizing} {
		if value, ok := stored["skill_"+key]; ok {
			enabled := value != 0
			*dst = &enabled
		}
	}
	for key, dst := range map[string]*skills.Overrides{skillPreference: &out.Account, skillPreference + ":" + workspace: &out.Workspace} {
		if account == "" || (key != skillPreference && workspace == "") {
			continue
		}
		raw, err := db.GetAccountPreference(ctx, pool, account, key)
		if err != nil {
			return out, err
		}
		if raw != nil {
			if err := json.Unmarshal(raw, dst); err != nil {
				return out, err
			}
		}
	}
	out.Effective = skills.Resolve(false, out.Install, out.Account, out.Workspace)
	return out, nil
}

// sessionSkills freezes the inventory at launch, including across pod recovery.
// A worker or channel uses its root workspace's off switches but never gets coordinating.
func sessionSkills(ctx context.Context, pool *pgxpool.Pool, id string) (policy *skills.Policy, launchErr error) {
	main := false
	// Skill inventory is optional launch metadata. Unknown ancestry never grants
	// coordinating; a verified Main retains it even when settings are unavailable.
	defer func() {
		if launchErr != nil {
			p := skills.Resolve(main)
			policy, launchErr = &p, nil
		}
	}()
	if pool == nil {
		p := skills.Resolve(false)
		return &p, nil
	}
	row, err := db.GetSession(ctx, pool, id)
	if err != nil || row == nil {
		return nil, fmt.Errorf("read session skills: session unavailable")
	}
	main = row.ParentSessionID == nil
	var raw []byte
	err = pool.QueryRow(ctx, `SELECT policy FROM session_skill_policy WHERE session_id=$1`, id).Scan(&raw)
	if err == nil {
		var p skills.Policy
		err = json.Unmarshal(raw, &p)
		return &p, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	account := derefOrEmpty(row.SpawningAccountID)
	root := row
	// Session ancestry is durable and server-owned. Bound corrupt cycles.
	for depth := 0; root.ParentSessionID != nil; depth++ {
		if depth >= 64 {
			return nil, fmt.Errorf("session ancestry too deep")
		}
		root, err = db.GetSession(ctx, pool, *root.ParentSessionID)
		if err != nil || root == nil {
			return nil, fmt.Errorf("read workspace skills: workspace unavailable")
		}
	}
	settings, err := readSkillSettings(ctx, pool, account, root.ID)
	if err != nil {
		return nil, err
	}
	p := skills.Resolve(main, settings.Install, settings.Account, settings.Workspace)
	raw, err = json.Marshal(p)
	if err != nil {
		return nil, err
	}
	_, err = pool.Exec(ctx, `INSERT INTO session_skill_policy(session_id,policy) VALUES($1,$2) ON CONFLICT(session_id) DO NOTHING`, id, raw)
	if err != nil {
		return nil, err
	}
	// Concurrent launch/recovery must return the same persisted inventory.
	err = pool.QueryRow(ctx, `SELECT policy FROM session_skill_policy WHERE session_id=$1`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &p)
	}
	return &p, err
}

func (a *API) skillWorkspace(w http.ResponseWriter, r *http.Request, account string) (string, bool) {
	id := r.URL.Query().Get("workspace")
	if id == "" {
		return "", true
	}
	row, err := db.GetSession(r.Context(), a.dbPool, id)
	if err != nil {
		writeError(w, 503, "could not read workspace")
		return "", false
	}
	if row == nil || row.ParentSessionID != nil || derefOrEmpty(row.SpawningAccountID) != account {
		writeError(w, 404, "workspace not found")
		return "", false
	}
	return id, true
}

func (a *API) HandleSkillSettings(w http.ResponseWriter, r *http.Request) {
	account, ok := a.personAccount(w, r)
	if !ok {
		return
	}
	workspace, ok := a.skillWorkspace(w, r, account)
	if !ok {
		return
	}
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	admin := principal.Has("account.manage")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPut {
		var in struct {
			Scope  string           `json:"scope"`
			Skills skills.Overrides `json:"skills"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if dec.Decode(&in) != nil || dec.Decode(&struct{}{}) != io.EOF || (in.Skills.TDD == nil && in.Skills.UX == nil && in.Skills.Visualizing == nil) {
			writeError(w, 400, "provide skill switches and a scope")
			return
		}
		if in.Scope == "install" {
			if !admin {
				writeError(w, 403, "administrator required")
				return
			}
			tx, err := a.dbPool.Begin(r.Context())
			if err != nil {
				writeError(w, 503, "could not save skills")
				return
			}
			defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
			for key, value := range map[string]*bool{"test-driven-development": in.Skills.TDD, "ux-design": in.Skills.UX, "visualizing": in.Skills.Visualizing} {
				if value == nil {
					continue
				}
				n := 0
				if *value {
					n = 1
				}
				_, err = tx.Exec(r.Context(), `INSERT INTO runner_settings(key,value,updated_by) VALUES($1,$2,$3) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_by=EXCLUDED.updated_by,updated_at=now()`, "skill_"+key, n, account)
				if err != nil {
					writeError(w, 503, "could not save skills")
					return
				}
			}
			if tx.Commit(r.Context()) != nil {
				writeError(w, 503, "could not save skills")
				return
			}
		} else {
			key := skillPreference
			if in.Scope == "workspace" && workspace != "" {
				key += ":" + workspace
			} else if in.Scope != "account" {
				writeError(w, 400, "unknown skill scope")
				return
			}
			// Merge the supplied switches atomically so concurrent edits to different skills survive.
			raw, _ := json.Marshal(in.Skills)
			_, err := a.dbPool.Exec(r.Context(), `INSERT INTO account_preferences(account_id,key,value) VALUES($1,$2,$3) ON CONFLICT(account_id,key) DO UPDATE SET value=account_preferences.value || EXCLUDED.value,updated_at=now()`, account, key, raw)
			if err != nil {
				writeError(w, 503, "could not save skills")
				return
			}
		}
	}
	settings, err := readSkillSettings(r.Context(), a.dbPool, account, workspace)
	if err != nil {
		writeError(w, 503, "could not read skills")
		return
	}
	settings.Admin = admin
	rows, err := a.dbPool.Query(r.Context(), `SELECT id::text,COALESCE(NULLIF(title,''),repo) FROM sessions WHERE spawning_account_id=$1 AND parent_session_id IS NULL AND archived_at IS NULL ORDER BY started_at DESC`, account)
	if err != nil {
		writeError(w, 503, "could not read workspaces")
		return
	}
	defer rows.Close()
	settings.Workspaces = []skillWorkspaceOption{}
	for rows.Next() {
		var option skillWorkspaceOption
		if rows.Scan(&option.ID, &option.Title) != nil {
			writeError(w, 503, "could not read workspaces")
			return
		}
		settings.Workspaces = append(settings.Workspaces, option)
	}
	if rows.Err() != nil {
		writeError(w, 503, "could not read workspaces")
		return
	}
	writeJSON(w, 200, settings)
}

func (a *API) HandleSessionSkills(w http.ResponseWriter, r *http.Request) {
	account, ok := a.personAccount(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !a.browserCanSee(r.Context(), account, id) {
		writeError(w, 404, "session not found")
		return
	}
	var raw json.RawMessage
	if err := a.dbPool.QueryRow(r.Context(), `SELECT policy FROM session_skill_policy WHERE session_id=$1`, id).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, 200, nil)
		} else {
			writeError(w, 503, "could not read session skills")
		}
		return
	}
	writeJSON(w, 200, raw)
}
