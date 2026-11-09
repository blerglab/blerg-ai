package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/skills"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Runtime observations are bounded facts, not free-form agent assertions. Resolve
// session identity, engine and role from the durable session, not tool input.
func canonicalSkillEvent(ctx context.Context, pool *pgxpool.Pool, msg *protocol.AgentEvent) (bool, error) {
	var p skills.Load
	if json.Unmarshal(msg.Payload, &p) != nil || p.Bytes < 0 || p.Bytes > 2<<20 || len(p.ParentCallID) > 256 || len(p.Skill) > 80 {
		return false, nil //nolint:nilerr // Malformed telemetry is dropped; only storage failures are retried.
	}
	version, err := hex.DecodeString(p.Version)
	if err != nil || len(version) != 32 {
		return false, nil //nolint:nilerr // Malformed telemetry is dropped; only storage failures are retried.
	}
	known := false
	for _, e := range skills.Inventory(skills.Resolve(true)) {
		if e.Name == p.Skill {
			known = true
		}
	}
	if !known || (p.Mode != "always" && p.Mode != "on_demand") {
		return false, nil
	}
	switch p.Basis {
	case "native_skill_input_accepted", "explicit_skill_input_accepted", "compact_fallback", "partial_native_observation", "complete_body_observed", "native_skill_success", "content_mismatch", "file_unavailable":
	default:
		return false, nil
	}
	if (p.Basis == "content_mismatch" || p.Basis == "file_unavailable" || p.Basis == "partial_native_observation") && msg.Kind != "skill_reporting" {
		return false, nil
	}
	if pool == nil {
		return false, nil
	}
	row, err := db.GetSession(ctx, pool, msg.SessionID)
	if err != nil {
		failurelog.Default.Event("skill_event_session", slog.LevelError, "cause", failurelog.Cause(err))
		return false, err
	}
	if row == nil {
		return false, nil
	}
	p.SessionID = msg.SessionID
	p.Engine = derefOrEmpty(row.Engine)
	if p.Engine == "" {
		p.Engine = "claude"
	}
	p.Role = "Main"
	if row.ParentSessionID != nil {
		p.Role = "unknown"
		if row.DelegationVia == "person" {
			p.Role = "channel"
		} else if row.DelegationVia != "" || row.StartedByKind == "session" {
			p.Role = "worker"
		}
	}
	if p.ParentCallID != "" {
		p.Role = "subagent"
	}
	p.At = time.Time{}
	msg.Payload, _ = json.Marshal(p)
	msg.Transient = false
	return true, nil
}

func canonicalSkillApplicability(msg *protocol.AgentEvent) bool {
	var p skills.Applicability
	if json.Unmarshal(msg.Payload, &p) != nil || (p.Skill != "ux-design" && p.Skill != "test-driven-development") || p.Basis != "successful_direct_edit" || len(p.ParentCallID) > 256 {
		return false
	}
	p.SessionID = msg.SessionID
	p.At = time.Time{}
	msg.Payload, _ = json.Marshal(p)
	msg.Transient = false
	return true
}

// A daemon observation can never become a person's correction. Human-authored
// evidence is appended only by the authenticated HTTP owner route.
func canonicalDaemonSkillEvidence(msg *protocol.AgentEvent) bool {
	var p skills.Evidence
	if json.Unmarshal(msg.Payload, &p) != nil || !skills.ValidRule(p.Skill, p.Rule) || (p.Result != "followed" && p.Result != "missed" && p.Result != "unknown") {
		return false
	}
	version, err := hex.DecodeString(p.Version)
	if err != nil || len(version) != 32 {
		return false
	}
	p.Source = "agent"
	p.SessionID = msg.SessionID
	p.At = time.Time{}
	msg.Payload, _ = json.Marshal(p)
	msg.Transient = false
	return true
}
