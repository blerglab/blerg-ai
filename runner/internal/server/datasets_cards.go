package server

import (
	"context"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

type datasetViewerContextKey struct{}

// Current Core/Board proof is required on collection AND queued delivery. A
// dashboard's visibility cannot grant its viewer another person's card data.
func (a *API) datasetCardAccess(ctx context.Context, account, sid string, p map[string]string) string {
	viewer, ok := ctx.Value(datasetViewerContextKey{}).(identity.Principal)
	if !ok || viewer.Kind != "human" || viewer.Sub != account || viewer.Sid == "" || (viewer.Project != "" && viewer.Project != p["board"]) {
		return "access_removed"
	}
	key := datasetRowKey(account, viewer.Sid, viewer.Project, p["board"], p["card"])
	if delivery, _ := ctx.Value(datasetDeliveryContextKey{}).(bool); delivery {
		a.hub.datasets.mu.Lock()
		expires := a.hub.datasets.boardChecks[key]
		a.hub.datasets.mu.Unlock()
		if time.Now().Before(expires) {
			return ""
		}
	}
	caller := a.sessionBoardCaller()
	if caller == nil {
		datasetFailure("board_not_configured")
		return "board_unavailable"
	}
	n, err := strconv.Atoi(p["card"])
	if err != nil || n <= 0 {
		return "access_removed"
	}
	_, apiErr := readLiveBoardCard(ctx, caller, mcpgw.Proof{AccountID: account, SessionID: viewer.Sid}, p["board"], n, sid)
	a.hub.datasets.mu.Lock()
	defer a.hub.datasets.mu.Unlock()
	if a.hub.datasets.boardChecks == nil {
		a.hub.datasets.boardChecks = map[string]time.Time{}
	}
	// Bounded cache, scoped to the current human sign-in and card. Collection
	// always checks live; only its immediate queued deliveries reuse the proof.
	for k, expiry := range a.hub.datasets.boardChecks {
		if time.Now().After(expiry) {
			delete(a.hub.datasets.boardChecks, k)
		}
	}
	if apiErr == nil {
		if len(a.hub.datasets.boardChecks) < 1024 {
			a.hub.datasets.boardChecks[key] = time.Now().Add(time.Second)
		}
		return ""
	}
	delete(a.hub.datasets.boardChecks, key)
	if apiErr.Status == 401 || apiErr.Status == 403 || apiErr.Status == 404 {
		return "access_removed"
	}
	datasetFailure("board_check_unavailable")
	return "board_unavailable"
}

func cardTimeDatasetRows(ctx context.Context, pool *pgxpool.Pool, account string, p map[string]string) ([]protocol.DatasetRow, time.Time, string) {
	now := time.Now().UTC()
	number, _ := strconv.Atoi(p["card"])
	seconds, known, pending, err := db.CardTimeTotal(ctx, pool, account, p["board"], number)
	if err != nil {
		return nil, now, "card_time_unavailable"
	}
	if pending {
		return nil, now, "card_time_pending"
	}
	if !known {
		return nil, now, "card_time_unknown"
	}
	return []protocol.DatasetRow{{"key": datasetRowKey(p["board"], p["card"]), "seconds": seconds}}, now, ""
}
