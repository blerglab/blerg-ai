// Package sessionstorage defines the install-wide attachment storage settings for sessions: how
// much a new session's attachments may weigh together, and the most an administrator may raise
// one session to. Core stores what an administrator saves; the runner enforces it.
package sessionstorage

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

// DefaultUploadMB is a session's attachment total when nothing else is configured.
const DefaultUploadMB = 500

// DefaultMaxUploadMB is the most one session can be raised to when nothing else is configured.
const DefaultMaxUploadMB = 5000

// MaxConfigMB bounds every value (1 TiB), so the byte count cannot overflow.
const MaxConfigMB = 1 << 20

// StepMB is how much one "Increase storage" adds to a session, up to the maximum.
const StepMB = 500

// UploadEnv seeds the per-session default; it was the only setting before core held them.
const UploadEnv = "BLERG_RUNNER_MAX_SESSION_UPLOADS_MB"

// Limits are whole MiB.
type Limits struct {
	DefaultUploadMB int64 `json:"default_upload_mb"`
	MaxUploadMB     int64 `json:"max_upload_mb"`
}

// Ranges are the inclusive bounds an administrator may save.
var Ranges = map[string][2]int64{"default_upload_mb": {1, MaxConfigMB}, "max_upload_mb": {1, MaxConfigMB}}

// Validate checks each value is in range and that the maximum is not below the default.
func (l Limits) Validate() error {
	for _, f := range []struct {
		name  string
		value int64
	}{{"default_upload_mb", l.DefaultUploadMB}, {"max_upload_mb", l.MaxUploadMB}} {
		r := Ranges[f.name]
		if f.value < r[0] || f.value > r[1] {
			return fmt.Errorf("%s must be between %d and %d", f.name, r[0], r[1])
		}
	}
	if l.MaxUploadMB < l.DefaultUploadMB {
		return fmt.Errorf("max_upload_mb (%d) must be at least default_upload_mb (%d)", l.MaxUploadMB, l.DefaultUploadMB)
	}
	return nil
}

// FromEnv seeds the defaults: BLERG_RUNNER_MAX_SESSION_UPLOADS_MB is the per-session default, and
// the maximum is DefaultMaxUploadMB or that default, whichever is larger. Saved settings win.
func FromEnv() Limits {
	def := int64(DefaultUploadMB)
	if raw := strings.TrimSpace(os.Getenv(UploadEnv)); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 || n > MaxConfigMB {
			log.Printf("%s must be a whole number of MiB from 1 to %d; using default %d", UploadEnv, MaxConfigMB, DefaultUploadMB)
		} else {
			def = n
		}
	}
	return Limits{DefaultUploadMB: def, MaxUploadMB: max(def, DefaultMaxUploadMB)}
}
