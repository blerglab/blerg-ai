// Package releasecut defines the immutable evidence shared by board and runner.
package releasecut

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

type CardVersion struct {
	ID         string `json:"id"`
	Version    int    `json:"version"`
	Terminal   bool   `json:"terminal"`
	ColumnName string `json:"column_name"`
}

// Versions bind the exact active set. Every board card edit increments its version.
func Digest(cards []CardVersion) string {
	values := append([]CardVersion{}, cards...)
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	raw, _ := json.Marshal(values)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type SnapshotCard struct {
	Card       json.RawMessage `json:"card"`
	Terminal   bool            `json:"terminal"`
	ColumnName string          `json:"column_name"`
}
type Preview struct {
	Digest   string         `json:"digest"`
	Snapshot []SnapshotCard `json:"snapshot"`
}
type Receipt struct {
	ID                string         `json:"id"`
	BoardID           string         `json:"board_id"`
	ReleaseID         string         `json:"release_id"`
	NextReleaseName   string         `json:"next_release_name,omitempty"`
	NextReleaseID     string         `json:"next_release_id"`
	AskID             string         `json:"ask_id"`
	OwnerID           string         `json:"owner_id"`
	PersonAct         bool           `json:"person_act"`
	ReleasedAt        time.Time      `json:"released_at"`
	ExecutionDeadline time.Time      `json:"execution_deadline,omitempty"`
	CardDigest        string         `json:"card_digest"`
	Snapshot          []SnapshotCard `json:"snapshot"`
	Archived          []string       `json:"archived"`
	Moved             []string       `json:"moved"`
}

// Intent is server-held consent, not an engine-supplied approval statement.
type Intent struct {
	AskID      string    `json:"ask_id"`
	CardDigest string    `json:"card_digest"`
	OwnerID    string    `json:"owner_id"`
	PersonAct  bool      `json:"person_act"`
	Deadline   time.Time `json:"execution_deadline"`
}

// SnapshotDigest validates the immutable membership before deriving a Final.
func SnapshotDigest(cards []SnapshotCard, board, release string) (string, error) {
	versions := make([]CardVersion, 0, len(cards))
	seen := map[string]bool{}
	for _, entry := range cards {
		var card struct {
			ID        string `json:"id"`
			BoardID   string `json:"board_id"`
			ReleaseID string `json:"release_id"`
			Version   *int   `json:"version"`
		}
		if err := json.Unmarshal(entry.Card, &card); err != nil {
			return "", err
		}
		if card.ID == "" || seen[card.ID] || card.BoardID != board || card.ReleaseID != release || card.Version == nil || *card.Version < 0 {
			return "", errors.New("invalid cut snapshot membership")
		}
		seen[card.ID] = true
		versions = append(versions, CardVersion{ID: card.ID, Version: *card.Version, Terminal: entry.Terminal, ColumnName: entry.ColumnName})
	}
	return Digest(versions), nil
}
