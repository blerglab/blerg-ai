package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/childlimits"
)

var errChildLimitsUnavailable = errors.New("child limits could not be read from core")

type childStartLimits struct{ parallel, hourly int }

func childStartLimitsFromEnv() childStartLimits {
	l := childlimits.FromEnv()
	return childStartLimits{l.Parallel, l.StartsPerHour}
}

// Read core on each admission/limits request: an administrator's changes apply
// without a restart. Failed reads retain the last valid value, or env defaults
// before the first successful read, like the Board call-limit policy.
func (a *API) effectiveChildLimits(ctx context.Context) childStartLimits {
	a.childLimitsMu.Lock()
	defer a.childLimitsMu.Unlock()
	if a.childLimits.parallel <= 0 {
		a.childLimits.parallel = maxRunningChildrenPerParent
	}
	if a.childLimits.hourly <= 0 {
		a.childLimits.hourly = defaultMaxChildStartsPerHour
	}
	if a.coreURL != "" && a.coreInternalKey != "" {
		if l, err := a.fetchCoreChildLimits(ctx); err == nil && l.Validate() == nil {
			a.childLimits = childStartLimits{l.Parallel, l.StartsPerHour}
		}
	}
	return a.childLimits
}

func (a *API) fetchCoreChildLimits(ctx context.Context) (childlimits.Limits, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.coreURL+"/internal/child-limits", bytes.NewBufferString("{}"))
	if err != nil {
		return childlimits.Limits{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", a.coreInternalKey)
	resp, err := coreHTTPClient(nil).Do(req)
	if err != nil {
		return childlimits.Limits{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var l childlimits.Limits
	if resp.StatusCode != http.StatusOK {
		return l, errChildLimitsUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return l, err
	}
	err = json.Unmarshal(raw, &l)
	return l, err
}
