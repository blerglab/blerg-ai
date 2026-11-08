package protocol

import (
	"regexp"
	"strconv"
	"time"
)

type DatasetReference struct {
	Kind       string            `json:"kind"`
	Name       string            `json:"name"`
	Parameters map[string]string `json:"parameters"`
}
type DatasetSubscribe struct {
	Cause          string           `json:"cause,omitempty"`
	Type           string           `json:"type"`
	SessionID      string           `json:"session_id"`
	SubscriptionID string           `json:"subscription_id"`
	Data           DatasetReference `json:"data"`
}
type DatasetRow map[string]any
type DatasetDelta struct {
	Type                    string       `json:"type"`
	SessionID               string       `json:"session_id"`
	SubscriptionID          string       `json:"subscription_id"`
	Epoch                   string       `json:"epoch"`
	BaseSequence            int64        `json:"base_sequence"`
	Sequence                int64        `json:"sequence"`
	Snapshot                bool         `json:"snapshot"`
	Upserts                 []DatasetRow `json:"upserts"`
	Removals                []string     `json:"removals"`
	ValidUntil              time.Time    `json:"valid_until,omitzero"`
	ExpectedIntervalSeconds int          `json:"expected_interval_seconds,omitempty"`
	NextAttemptAt           time.Time    `json:"next_attempt_at,omitzero"`
	ObservedAt              time.Time    `json:"observed_at"`
	Status                  string       `json:"status"`
	Cause                   string       `json:"cause,omitempty"`
}

var datasetSuiteName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

var datasetBoardID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

var datasetRefName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*$`)

func (d DatasetReference) Valid() bool {
	if d.Kind != "dataset" || d.Parameters == nil {
		return false
	}
	allowed := map[string]bool{}
	switch d.Name {
	case "blerg.mcp":
		allowed["binding"] = true
		if !validDatasetBinding(d.Parameters["binding"]) {
			return false
		}
	case "blerg.workers":
	case "blerg.workers.timeline":
		allowed["since"] = true
	case "blerg.gates.durations":
		allowed["since"] = true
		allowed["suite"] = true
		if suite := d.Parameters["suite"]; suite != "" && !datasetSuiteName.MatchString(suite) {
			return false
		}
	case "blerg.cards.time", "blerg.reviews.pairs":
		allowed["board"], allowed["card"] = true, true
		n, err := strconv.Atoi(d.Parameters["card"])
		if err != nil || n <= 0 || n > 999999999 || strconv.Itoa(n) != d.Parameters["card"] || !datasetBoardID.MatchString(d.Parameters["board"]) {
			return false
		}
	case "blerg.engine.usage":
		allowed["engine"] = true
		if e := d.Parameters["engine"]; e != "claude" && e != "codex" && e != "hermes" {
			return false
		}
	case "blerg.gates.bins":
		allowed["since"] = true
		allowed["width"], allowed["suite"] = true, true
		width, err := strconv.Atoi(d.Parameters["width"])
		if err != nil || width < 1 || width > 86400 || strconv.Itoa(width) != d.Parameters["width"] {
			return false
		}
		if suite := d.Parameters["suite"]; suite != "" && !datasetSuiteName.MatchString(suite) {
			return false
		}
	case "blerg.cards.merged", "blerg.cards.merged.heatmap":
		for _, k := range []string{"bucket", "timezone", "release", "base", "member"} {
			allowed[k] = true
		}
		if b := d.Parameters["bucket"]; b != "day" && b != "hour" {
			return false
		}
		if d.Parameters["timezone"] == "" {
			return false
		}
		if _, err := time.LoadLocation(d.Parameters["timezone"]); err != nil {
			return false
		}
		if d.Parameters["release"] != "" && d.Parameters["base"] == "" {
			return false
		}
		if b := d.Parameters["base"]; b != "" && !datasetRefName.MatchString(b) {
			return false
		}
	default:
		return false
	}
	if since, ok := d.Parameters["since"]; ok {
		if _, err := time.Parse(time.RFC3339, since); err != nil {
			return false
		}
	}
	for k, v := range d.Parameters {
		if !allowed[k] || v == "" || len(v) > 128 {
			return false
		}
	}
	return true
}

func validDatasetBinding(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
