package server

import (
	"context"
	"time"
)

// cleanupFailedSessionJob tolerates a brief API outage without tying cleanup to
// a canceled launch request. Each deletion and the retry budget are bounded.
func (j *JobManager) cleanupFailedSessionJob(parent context.Context, sessionID string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer cancel()
	wait := j.failedCleanupWait
	if wait == nil {
		wait = func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		call, done := context.WithTimeout(ctx, 3*time.Second)
		err = j.deleteSessionJobContext(call, sessionID)
		done()
		if err == nil || attempt == 2 || ctx.Err() != nil {
			return err
		}
		if waitErr := wait(ctx, time.Duration(1<<attempt)*100*time.Millisecond); waitErr != nil {
			return waitErr
		}
	}
	return err
}
