package releaseperf

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Boot uses the same 446 sessions and 176,000 events on both releases. Older
// schemas predate session_card_links and worker_kind; neither is needed to
// render the seeded Main transcript. Keep the regular scenario seed unchanged.
func SeedBootDataset(ctx context.Context, pool *pgxpool.Pool, account string) (SeedSummary, error) {
	return seedRunnerDataset(ctx, pool, account, true)
}
func insertBootSession(ctx context.Context, pool *pgxpool.Pool, id, daemonID, status, path, repo, title, model string) error {
	_, err := pool.Exec(ctx, `INSERT INTO sessions(id,daemon_id,status,project_path,repo,title,model) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''))`, id, daemonID, status, path, repo, title, model)
	return err
}
