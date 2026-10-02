package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
)

// Bounds on user-supplied cron fields.
const (
	maxNameLen        = 100
	maxPromptLen      = 16384
	maxShortFieldLen  = 200
	maxMCPBytes       = 64 * 1024
	minGraceSeconds   = 0
	maxGraceSeconds   = 24 * 3600
	minRuntimeSeconds = 60
	maxRuntimeSeconds = 24 * 3600
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// NewCron is a request to create a cron. The token is minted by the caller
// (core's /internal/tokens/mint) before this is called.
type NewCron struct {
	Owner             string
	Name              string
	Enabled           bool
	Schedule          string
	Timezone          string
	Prompt            string
	Model             *string
	Effort            *string
	Runtime           string // auto (default), cluster or docker
	DaemonID          *string
	BoardID           *string
	MCP               json.RawMessage // a JSON array; default []
	TokenID           string
	TokenExpiresAt    time.Time
	GraceSeconds      *int // nil: the default, 3600; an explicit 0 means no catch-up window
	MaxRuntimeSeconds int  // default 1800
}

// CronPatch is a PATCH-style edit: nil fields are left alone. For the nullable
// text fields (Model, Effort, DaemonID, BoardID) a pointer to "" clears the
// value. Resume clears paused_reason and the failure count.
type CronPatch struct {
	Name              *string
	Enabled           *bool
	Schedule          *string
	Timezone          *string
	Prompt            *string
	Model             *string
	Effort            *string
	Runtime           *string
	DaemonID          *string
	BoardID           *string
	MCP               *json.RawMessage
	GraceSeconds      *int
	MaxRuntimeSeconds *int
	// Renewal: replace the token id and expiry together.
	TokenID        *string
	TokenExpiresAt *time.Time
	Resume         bool
	// ExpectTokenID, when set, makes a token swap a compare-and-set: the cron must still hold
	// this token (ErrConflict otherwise). RefuseWhileClaimed refuses the swap (ErrRunOpen) while a
	// run is claimed, a start in progress or waiting for the reaper that uses the current token.
	// Both are checked under the cron's row lock, the one the scheduler's claim takes.
	ExpectTokenID      string
	RefuseWhileClaimed bool
	// RequireActive makes a renewal (which is not a resume) fail with ErrConflict on a paused cron:
	// a pause revoked its token, and a new live one must not appear on it.
	RequireActive bool
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// CreateCron validates and stores a cron. Its first slot is the first one
// strictly after now. It enforces the per-account limit.
func (s *Scheduler) CreateCron(ctx context.Context, in NewCron) (*db.Cron, error) {
	now := s.clock.Now()
	c := db.Cron{
		OwnerAccountID: in.Owner, Name: in.Name, Enabled: in.Enabled, Schedule: in.Schedule, Timezone: in.Timezone,
		Prompt: in.Prompt, Model: emptyToNil(in.Model), Effort: emptyToNil(in.Effort), Runtime: in.Runtime,
		DaemonID: emptyToNil(in.DaemonID), BoardID: emptyToNil(in.BoardID), MCP: in.MCP,
		TokenID: in.TokenID, TokenExpiresAt: in.TokenExpiresAt,
		GraceSeconds: DefaultGraceSeconds, MaxRuntimeSeconds: in.MaxRuntimeSeconds,
	}
	if in.Owner == "" {
		return nil, invalid("owner is required")
	}
	if c.Runtime == "" {
		c.Runtime = "auto"
	}
	if in.GraceSeconds != nil {
		c.GraceSeconds = *in.GraceSeconds
	}
	if c.MaxRuntimeSeconds == 0 {
		c.MaxRuntimeSeconds = DefaultMaxRuntimeSeconds
	}
	if len(c.MCP) == 0 {
		c.MCP = json.RawMessage("[]")
	}
	if err := validateFields(&c, now); err != nil {
		return nil, err
	}
	sch, err := compileSchedule(c.Schedule, c.Timezone, now)
	if err != nil {
		return nil, err
	}
	c.NextRunAt, _ = sch.Next(now)
	return db.InsertCron(ctx, s.cfg.Pool, c, s.cfg.MaxCronsPerAccount)
}

// UpdateCron applies a patch to an owned cron. Changing the schedule or the
// timezone, resuming a paused cron and re-enabling a disabled one recompute
// next_run_at from now, so the cron does not fire immediately for a slot that
// passed while it was being edited or was off. Another account's cron is
// db.ErrCronNotFound.
func (s *Scheduler) UpdateCron(ctx context.Context, owner, id string, p CronPatch) (*db.Cron, error) {
	now := s.clock.Now()
	return db.UpdateCronTx(ctx, s.cfg.Pool, owner, id, func(ctx context.Context, tx pgx.Tx, c *db.Cron) error {
		if p.ExpectTokenID != "" && c.TokenID != p.ExpectTokenID || p.RequireActive && c.PausedReason != nil {
			return ErrConflict
		}
		if p.RefuseWhileClaimed {
			claimed, err := db.HasClaimedCronRun(ctx, tx, c.ID)
			if err != nil {
				return err
			}
			if claimed {
				return ErrRunOpen
			}
		}
		wasEnabled := c.Enabled
		recompute := false
		if p.Name != nil {
			c.Name = *p.Name
		}
		if p.Prompt != nil {
			c.Prompt = *p.Prompt
		}
		if p.Runtime != nil {
			c.Runtime = *p.Runtime
		}
		if p.Model != nil {
			c.Model = emptyToNil(p.Model)
		}
		if p.Effort != nil {
			c.Effort = emptyToNil(p.Effort)
		}
		if p.DaemonID != nil {
			c.DaemonID = emptyToNil(p.DaemonID)
		}
		if p.BoardID != nil {
			c.BoardID = emptyToNil(p.BoardID)
		}
		if p.MCP != nil {
			c.MCP = *p.MCP
		}
		if p.GraceSeconds != nil {
			c.GraceSeconds = *p.GraceSeconds
		}
		if p.MaxRuntimeSeconds != nil {
			c.MaxRuntimeSeconds = *p.MaxRuntimeSeconds
		}
		if (p.TokenID == nil) != (p.TokenExpiresAt == nil) {
			return invalid("a token renewal needs both the token id and its expiry")
		}
		if p.TokenID != nil {
			c.TokenID, c.TokenExpiresAt = *p.TokenID, *p.TokenExpiresAt
		}
		if p.Schedule != nil && *p.Schedule != c.Schedule {
			c.Schedule, recompute = *p.Schedule, true
		}
		if p.Timezone != nil && *p.Timezone != c.Timezone {
			c.Timezone, recompute = *p.Timezone, true
		}
		if p.Enabled != nil {
			c.Enabled = *p.Enabled
			if c.Enabled && !wasEnabled {
				recompute = true
			}
		}
		if p.Resume && c.PausedReason != nil {
			if !c.TokenExpiresAt.After(now) {
				return invalid("the cron's access token has expired; renew it before resuming")
			}
			c.PausedReason, c.ConsecutiveFailures = nil, 0
			recompute = true
		}
		if err := validateFields(c, now); err != nil {
			return err
		}
		if recompute {
			sch, err := compileSchedule(c.Schedule, c.Timezone, now)
			if err != nil {
				return err
			}
			c.NextRunAt, _ = sch.Next(now)
		}
		return nil
	})
}

// ResumeCron clears a pause (three failures, revoked or expired access) and
// schedules the next slot from now.
func (s *Scheduler) ResumeCron(ctx context.Context, owner, id string) (*db.Cron, error) {
	return s.UpdateCron(ctx, owner, id, CronPatch{Resume: true})
}

// PauseCron pauses an owned cron with a reason. It reports false when it was
// already paused.
func (s *Scheduler) PauseCron(ctx context.Context, owner, id, reason string) (bool, error) {
	c, err := db.GetOwnedCron(ctx, s.cfg.Pool, owner, id)
	if err != nil {
		return false, err
	}
	return db.PauseCron(ctx, s.cfg.Pool, c.ID, reason)
}

// DeleteCron removes an owned cron and its runs and returns it, so the caller
// can revoke its token and stop its sessions.
func (s *Scheduler) DeleteCron(ctx context.Context, owner, id string) (*db.Cron, error) {
	return db.DeleteCron(ctx, s.cfg.Pool, owner, id)
}

func compileSchedule(expr, tz string, now time.Time) (*Schedule, error) {
	sch, err := ParseSchedule(expr, tz)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	if err := sch.Validate(now); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	return sch, nil
}

func validateFields(c *db.Cron, now time.Time) error {
	if n := utf8.RuneCountInString(c.Name); n == 0 || n > maxNameLen {
		return invalid("name must be 1-%d characters", maxNameLen)
	}
	if n := utf8.RuneCountInString(c.Prompt); n == 0 || n > maxPromptLen {
		return invalid("prompt must be 1-%d characters", maxPromptLen)
	}
	switch c.Runtime {
	case "auto", "cluster", "docker":
	default:
		return invalid(`runtime must be "auto", "cluster" or "docker"`)
	}
	for _, f := range []struct {
		name string
		v    *string
	}{{"model", c.Model}, {"effort", c.Effort}, {"board", c.BoardID}} {
		if f.v != nil && utf8.RuneCountInString(*f.v) > maxShortFieldLen {
			return invalid("%s is too long", f.name)
		}
	}
	if c.DaemonID != nil && !uuidPattern.MatchString(*c.DaemonID) {
		return invalid("daemon id must be a UUID")
	}
	if c.GraceSeconds < minGraceSeconds || c.GraceSeconds > maxGraceSeconds {
		return invalid("grace must be between %d seconds and 24 hours", minGraceSeconds)
	}
	if c.MaxRuntimeSeconds < minRuntimeSeconds || c.MaxRuntimeSeconds > maxRuntimeSeconds {
		return invalid("maximum run time must be between %d seconds and 24 hours", minRuntimeSeconds)
	}
	var arr []json.RawMessage
	if len(c.MCP) > maxMCPBytes || json.Unmarshal(c.MCP, &arr) != nil {
		return invalid("mcp must be a JSON array of at most %d bytes", maxMCPBytes)
	}
	if c.TokenID == "" {
		return invalid("the cron has no access token")
	}
	if !c.TokenExpiresAt.After(now) && c.PausedReason == nil {
		return invalid("the access token has already expired")
	}
	return nil
}

func emptyToNil(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	return p
}
