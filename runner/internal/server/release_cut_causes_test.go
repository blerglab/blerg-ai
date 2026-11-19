package server

import (
	"errors"
	"fmt"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCutFinalizationCausePreservesEvidenceAndRetriesTransactions(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"evidence", &boundAskError{kind: blackboard.ErrConflict, cause: "link_after_cut"}, "link_after_cut"},
		{"main_ended", &boundAskError{kind: blackboard.ErrConflict, cause: "reconciliation_main_ended"}, "reconciliation_main_ended"},
		{"wrapped_evidence", fmt.Errorf("finalization: %w", &boundAskError{kind: blackboard.ErrConflict, cause: "missing_release_link_evidence"}), "missing_release_link_evidence"},
		{"revision_conflict", blackboard.ErrConflict, "finalization_conflict"},
		{"serialization", &pgconn.PgError{Code: "40001"}, "finalization_conflict"},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, "finalization_conflict"},
		{"lock_busy", &pgconn.PgError{Code: "55P03"}, "finalization_conflict"},
		{"storage", errors.New("storage unavailable"), "db_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Exercise the same classification the real receipt reconciler uses.
			cause := cutFinalizationCause(tc.err)
			if cause != tc.want {
				t.Fatalf("cause=%s; want %s", cause, tc.want)
			}
		})
	}
}
