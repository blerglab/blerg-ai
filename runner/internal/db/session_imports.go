package db

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/runner/internal/fileview"
)

// NextImportNumber is the `n` a `session import` about to run should try first (design §4 step
// 9): one more than this child's count of already-completed imports. Read and the eventual
// ReportImport insert are not one transaction — a crash between them is exactly what step 9's
// local-probe-and-step-past recovery is for, not something this function needs to prevent.
func NextImportNumber(ctx context.Context, pool *pgxpool.Pool, sessionID string) (int, error) {
	n, err := CountCompletedImports(ctx, pool, sessionID)
	if err != nil {
		return 0, err
	}
	return n + 1, nil
}

// CountCompletedImports is how many of this child's result bundles have been reported as
// imported.
func CountCompletedImports(ctx context.Context, pool *pgxpool.Pool, sessionID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM session_imports WHERE session_id = $1::uuid`, sessionID).Scan(&n)
	return n, err
}

// ReportImport records one completed import. Reporting the same (sessionID, artifactID,
// version) twice — a retried report after a lost reply, for instance — is a harmless no-op, not
// an error: this table is bookkeeping for "--list" and NextImportNumber, never the thing that
// makes an import safe (that's §4's ancestor check and CAS, done before this is ever called).
func ReportImport(ctx context.Context, pool *pgxpool.Pool, sessionID, artifactID string, version, n int) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO session_imports (session_id, artifact_id, artifact_version, n)
		VALUES ($1::uuid, $2, $3, $4)
		ON CONFLICT (session_id, artifact_id, artifact_version) DO NOTHING`,
		sessionID, artifactID, version, n)
	return err
}

// UnimportedBundles lists a child's published git-bundle artifacts that session_imports has no
// row for yet — what `session import --list` and the worker card's outstanding count both read.
// Newest first, matching ListArtifacts' own order.
func UnimportedBundles(ctx context.Context, pool *pgxpool.Pool, sessionID string) ([]ArtifactRow, error) {
	all, err := ListArtifacts(ctx, pool, sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `SELECT artifact_id, artifact_version FROM session_imports WHERE session_id = $1::uuid`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	imported := map[[2]string]bool{}
	for rows.Next() {
		var id string
		var version int
		if err := rows.Scan(&id, &version); err != nil {
			return nil, err
		}
		imported[[2]string{id, strconv.Itoa(version)}] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []ArtifactRow{}
	for _, a := range all {
		if a.Origin != OriginAgent || a.ContentType != fileview.GitBundle {
			continue
		}
		if imported[[2]string{a.ID, strconv.Itoa(a.Version)}] {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}
