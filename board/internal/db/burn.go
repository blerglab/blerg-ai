package db

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type BurnPoint struct {
	Date      string  `json:"date"`
	Scope     float64 `json:"scope"`
	Done      float64 `json:"done"`
	Confirmed float64 `json:"confirmed"`
	Added     float64 `json:"added"`
	Finished  float64 `json:"finished"`
}
type BurnChart struct {
	Restored    int         `json:"restored,omitempty"`
	Unavailable bool        `json:"unavailable,omitempty"`
	Count       []BurnPoint `json:"count"`
	Size        []BurnPoint `json:"size"`
	Estimated   bool        `json:"estimated"`
	HistoryNote string      `json:"history_note,omitempty"`
}
type burnState struct {
	Archived bool     `json:"archived,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Release  string   `json:"release"`
	Size     string   `json:"size"`
	Done     bool     `json:"done"`
	ToTest   bool     `json:"to_test"`
}
type burnChange struct {
	At    time.Time
	Card  string
	After burnState
}

func burnStateOf(c Card) burnState {
	return burnState{Archived: c.ArchivedAt != nil, Release: deref(c.ReleaseID), Size: deref(c.Size), Tags: c.Tags, ToTest: slices.Contains(c.Tags, "to-test")}
}
func sizeWeight(size string) float64 {
	switch size {
	case "M":
		return 2
	case "L":
		return 4
	case "XL":
		return 8
	default:
		return 1
	}
}
func inBurn(s burnState, group string) bool {
	if s.Archived && !s.Done {
		return false
	}
	if strings.HasPrefix(group, "tag:") {
		return slices.Contains(s.Tags, strings.TrimPrefix(group, "tag:"))
	}
	return s.Release == group
}
func buildBurn(release string, changes []burnChange, end time.Time) BurnChart {
	out := BurnChart{Count: []BurnPoint{}, Size: []BurnPoint{}}
	if len(changes) == 0 {
		return out
	}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].At.Before(changes[j].At) })
	first := -1
	for i, c := range changes {
		if inBurn(c.After, release) {
			first = i
			break
		}
	}
	if first < 0 {
		return out
	}
	changes = changes[first:]
	states := map[string]burnState{}
	i := 0
	start := changes[0].At.UTC().Truncate(24 * time.Hour)
	end = end.UTC().Truncate(24 * time.Hour)
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		count := BurnPoint{Date: day.Format("2006-01-02")}
		size := count
		for i < len(changes) && changes[i].At.Before(day.AddDate(0, 0, 1)) {
			c := changes[i]
			old := states[c.Card]
			next := c.After
			if inBurn(next, release) && !inBurn(old, release) {
				count.Added++
				size.Added += sizeWeight(next.Size)
			}
			if inBurn(next, release) && next.Done && (!old.Done || !inBurn(old, release)) {
				count.Finished++
				size.Finished += sizeWeight(next.Size)
			}
			states[c.Card] = next
			i++
		}
		for _, s := range states {
			if !inBurn(s, release) {
				continue
			}
			w := sizeWeight(s.Size)
			if s.Size != "" {
				out.Estimated = true
			}
			count.Scope++
			size.Scope += w
			if s.Done {
				count.Done++
				size.Done += w
				if !s.ToTest {
					count.Confirmed++
					size.Confirmed += w
				}
			}
		}
		out.Count = append(out.Count, count)
		out.Size = append(out.Size, size)
	}
	return out
}

// ReleaseBurn replays the audit trail. Before snapshots existed, moves can be reconstructed
// with current column semantics; missing assignment/tag/size values are disclosed explicitly.
func ReleaseBurn(ctx context.Context, pool *pgxpool.Pool, board, release string) (BurnChart, error) {
	rows, err := pool.Query(ctx, `SELECT c.id,coalesce(c.release_id::text,''),coalesce(c.size,''),coalesce(col.is_terminal,false) OR (c.archived_at IS NOT NULL AND coalesce((SELECT fc.is_terminal FROM card_events e LEFT JOIN board_columns fc ON fc.id=e.from_column_id WHERE e.card_id=c.id AND e.type='archived' ORDER BY e.id DESC LIMIT 1),false)) OR EXISTS(SELECT 1 FROM release_done_cards d WHERE d.card_id=c.id AND d.release_id=c.release_id AND d.restored_at IS NULL),EXISTS(SELECT 1 FROM card_tags t WHERE t.card_id=c.id AND t.tag='to-test'),c.archived_at IS NOT NULL,coalesce((SELECT jsonb_agg(tag ORDER BY tag) FROM card_tags t WHERE t.card_id=c.id),'[]') FROM cards c LEFT JOIN board_columns col ON col.id=c.column_id WHERE c.board_id=$1`, board)
	if err != nil {
		return BurnChart{}, err
	}
	current := map[string]burnState{}
	for rows.Next() {
		var id string
		var s burnState
		var tags []byte
		if err = rows.Scan(&id, &s.Release, &s.Size, &s.Done, &s.ToTest, &s.Archived, &tags); err != nil {
			rows.Close()
			return BurnChart{}, err
		}
		if err = json.Unmarshal(tags, &s.Tags); err != nil {
			rows.Close()
			return BurnChart{}, err
		}
		current[id] = s
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return BurnChart{}, err
	}
	rows, err = pool.Query(ctx, `SELECT e.card_id,e.type,e.created_at,e.data,coalesce(fc.is_terminal,false),coalesce(tc.is_terminal,false),e.from_column_id IS NULL FROM card_events e JOIN cards c ON c.id=e.card_id LEFT JOIN board_columns fc ON fc.id=e.from_column_id LEFT JOIN board_columns tc ON tc.id=e.to_column_id WHERE c.board_id=$1 ORDER BY e.created_at DESC,e.id DESC`, board)
	if err != nil {
		return BurnChart{}, err
	}
	defer rows.Close()
	changes := []burnChange{}
	incomplete := map[string]bool{}
	for rows.Next() {
		var id, typ string
		var at time.Time
		var raw []byte
		var fromDone, toDone, fromArchived bool
		if err = rows.Scan(&id, &typ, &at, &raw, &fromDone, &toDone, &fromArchived); err != nil {
			return BurnChart{}, err
		}
		var data struct {
			Fields []string   `json:"fields"`
			After  *burnState `json:"burn_after"`
			Before *burnState `json:"burn_before"`
			From   *string    `json:"from_release"`
			To     *string    `json:"to_release"`
			Flow   *struct {
				Done *bool `json:"done"`
			} `json:"flow"`
		}
		if err = json.Unmarshal(raw, &data); err != nil {
			return BurnChart{}, err
		}
		s := current[id]
		if data.After != nil {
			s = *data.After
		} else if typ == "created" || typ == "moved" || typ == "archived" || typ == "updated" {
			incomplete[id] = true
		}
		// Retain only events that change chart state, plus creation baselines.
		if typ == "moved" || typ == "created" {
			if data.After == nil {
				s.Done = toDone
				if data.Flow != nil && data.Flow.Done != nil {
					s.Done = *data.Flow.Done
				}
				s.Archived = false
			}
		}
		if typ == "archived" && data.After == nil {
			s.Archived = true
		}
		changes = append(changes, burnChange{At: at, Card: id, After: s})
		if data.Before != nil {
			s.Archived = data.Before.Archived
			s.Release = data.Before.Release
			s.Size = data.Before.Size
			if slices.Contains(data.Fields, "tags") {
				s.Tags = data.Before.Tags
				s.ToTest = data.Before.ToTest
			}
		} else {
			if data.From != nil {
				s.Release = *data.From
			}
			if typ == "moved" {
				s.Done = fromDone
				s.Archived = fromArchived
			}
			if typ == "archived" {
				s.Archived = false
				s.Done = fromDone
			}
		}
		current[id] = s
	}
	if err = rows.Err(); err != nil {
		return BurnChart{}, err
	}
	// Reverse the descending query before stable time sorting to preserve equal-time event order.
	slices.Reverse(changes)
	end := time.Now()
	var released *time.Time
	if !strings.HasPrefix(release, "tag:") {
		if err = pool.QueryRow(ctx, `SELECT released_at FROM board_releases WHERE board_id=$1 AND id=$2`, board, release).Scan(&released); err != nil {
			return BurnChart{}, err
		}
	}
	if released != nil {
		end = *released
	}
	out := buildBurn(release, changes, end)
	if released != nil {
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM release_done_cards WHERE release_id=$1 AND restored_at IS NOT NULL`, release).Scan(&out.Restored); err != nil {
			return BurnChart{}, err
		}
	}
	for _, change := range changes {
		if !incomplete[change.Card] || !inBurn(change.After, release) {
			continue
		}
		out.HistoryNote = "Older events lack release, tag, or size snapshots. Earlier scope and confirmed totals use the values available today; move dates use recorded history."
		break
	}
	return out, nil
}
