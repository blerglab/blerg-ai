package clusterworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

type failedPreparationGit struct {
	*fakeGit
	err error
}

func (f failedPreparationGit) Prepare(context.Context, Member, string, bool) (string, string, error) {
	return "", "", f.err
}

func TestClusterPreparationFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		err   error
		cause string
	}{
		{ErrClone, "repository_clone_failed"},
		{&CloneFailure{Cause: "deadline"}, "repository_clone_failed"},
		{ErrRetainedStorage, "retained_storage_missing"},
		{ErrPlan, "repository_preparation_failed"},
	} {
		t.Run(tc.cause, func(t *testing.T) {
			var logs bytes.Buffer
			saved := failurelog.Default
			failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { failurelog.Default = saved })
			rt, err := New(testPlan(), failedPreparationGit{newFake(), fmt.Errorf("CANARY_PRIVATE: %w", tc.err)})
			if err != nil {
				t.Fatal(err)
			}
			err = rt.Prepare(t.Context(), false)
			if err == nil || strings.Contains(err.Error(), "CANARY_PRIVATE") {
				t.Fatal("failure missing or unsafe")
			}
			if !errors.Is(tc.err, ErrPlan) && !errors.Is(err, tc.err) {
				t.Fatal("typed preparation cause lost")
			}
			if !strings.Contains(logs.String(), `"cause":"`+tc.cause+`"`) || strings.Contains(logs.String(), "CANARY_PRIVATE") {
				t.Fatal("diagnostic missing or unsafe")
			}
			if CloneCause(tc.err) == "deadline" && (!strings.Contains(logs.String(), `"git_cause":"deadline"`) || CloneCause(err) != "deadline") {
				t.Fatal("precise clone cause lost")
			}
			if strings.Contains(rt.Results()[0].SyncError, "CANARY_PRIVATE") {
				t.Fatal("member result exposed upstream error")
			}
		})
	}
}

func TestRetainedVerificationCarriesRecoveryWithoutLoggingPaths(t *testing.T) {
	var logs bytes.Buffer
	saved := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { failurelog.Default = saved })
	recovery := &RetainedCheckoutError{Location: "/retained/CANARY_PRIVATE"}
	rt, err := New(testPlan(), failedPreparationGit{newFake(), fmt.Errorf("CANARY_RAW: %w", recovery)})
	if err != nil {
		t.Fatal(err)
	}
	err = rt.Prepare(t.Context(), true)
	if !errors.Is(err, ErrRetainedVerification) || !strings.Contains(err.Error(), "Main can recover") {
		t.Fatal("recovery state lost", err)
	}
	result := rt.Results()[0]
	if result.RecoveryLocation != recovery.Location || result.SyncError != recovery.Error() || !result.Pending {
		t.Fatal("member recovery result lost", result)
	}
	if !strings.Contains(logs.String(), "retained_checkout_unverified") || strings.Contains(logs.String(), "CANARY") {
		t.Fatal("diagnostics missing or exposed private data", logs.String())
	}
}
