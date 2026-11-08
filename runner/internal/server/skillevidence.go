package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/skills"
)

// HandleSkillEvidence records declarations and person-authored corrections. It
// never edits the skill text or lets an agent impersonate a person's decision.
func (a *API) HandleSkillEvidence(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	if a.dbPool == nil {
		writeError(w, 503, noDatabaseMsg)
		return
	}
	source := "agent"
	if p, ok := a.verifyBrowserToken(a.rawBearer(r)); ok {
		if p.Kind != "human" || p.Project != "" {
			writeError(w, 403, "human sign-in required")
			return
		}
		row, err := db.GetSession(r.Context(), a.dbPool, sid)
		if err != nil {
			writeDatabaseFailure(w, "read skill evidence session", "skills", err)
			return
		}
		if !ownsSession(p.Sub, row) {
			writeError(w, 404, "session not found")
			return
		}
		source = "person"
	} else {
		own, ok := a.authMessaging(w, r)
		if !ok {
			return
		}
		if own != sid {
			writeError(w, 403, "own-session credential required")
			return
		}
	}
	var p skills.Evidence
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&p) != nil || !skills.ValidRule(p.Skill, p.Rule) || (p.Result != "followed" && p.Result != "missed" && p.Result != "unknown") {
		writeError(w, 422, "name a skill rule and followed, missed or unknown")
		return
	}
	version, err := hex.DecodeString(p.Version)
	if err != nil || len(version) != 32 {
		writeError(w, 422, "name the exact skill version")
		return
	}
	p.SessionID = sid
	p.Source = source
	p.At = time.Now().UTC()
	raw, _ := json.Marshal(p)
	accepted, err := db.AppendSkillEvidence(r.Context(), a.dbPool, sid, newUUID(), string(raw))
	if errors.Is(err, db.ErrSkillEvidenceVersion) {
		writeError(w, 422, "name a skill version loaded by this session")
		return
	}
	if err != nil {
		writeDatabaseFailure(w, "record skill evidence", "skills", err)
		return
	}
	if !accepted {
		w.Header().Set("Retry-After", "60")
		writeError(w, 429, "Too many skill assessments. Try again in a minute.")
		return
	}
	writeJSON(w, 200, p)
}
