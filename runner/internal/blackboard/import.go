package blackboard

import "time"

// ImportedRelease decodes existing historical evidence. It never establishes
// current approval or Final. The Import writer and blackboard-seed command are retired.
type ImportedRelease struct {
	Tag    string    `json:"tag"`
	Date   time.Time `json:"date"`
	Notes  string    `json:"notes"`
	Commit string    `json:"commit"`
}
