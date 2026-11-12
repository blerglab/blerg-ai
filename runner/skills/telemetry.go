package skills

import "time"

// Load records observed context entry, never installation or an inferred use.
// ParentCallID attributes native subagent work without counting it twice.
type Load struct {
	SessionID    string `json:"session_id"`
	ParentCallID string `json:"parent_call_id,omitempty"`
	Role         string `json:"role"`
	Engine       string `json:"engine"`
	Skill        string `json:"skill"`
	Version      string `json:"version"`
	Mode         string `json:"mode"`
	Bytes        int    `json:"bytes"`
	Basis        string `json:"basis"`
	// File names the linked file read, e.g. "examples.md"; empty means SKILL.md.
	File string    `json:"file,omitempty"`
	At   time.Time `json:"at,omitempty"`
}

type Report struct {
	Facts         []Facts         `json:"facts"`
	Applicability []Applicability `json:"applicability"`
	Evidence      []Evidence      `json:"evidence"`
	Loads         []Load          `json:"loads"`
	Reporting     []Load          `json:"reporting"`
	Truncated     bool            `json:"truncated"`
	Unavailable   bool            `json:"unavailable,omitempty"`
}

// Applicability is a positive observed signal, never an exhaustive census.
type Applicability struct {
	SessionID    string    `json:"session_id"`
	Skill        string    `json:"skill"`
	ParentCallID string    `json:"parent_call_id,omitempty"`
	Basis        string    `json:"basis"`
	At           time.Time `json:"at"`
}

// Facts retain existing evidence without converting incomplete observations into
// compliance. Image dimensions and the intent of a failing test are not stored.
type Facts struct {
	SessionID                      string `json:"session_id"`
	ImageArtifacts                 int    `json:"image_artifacts"`
	FullSuiteRuns                  int    `json:"full_suite_runs"`
	ReportedTestFailuresBeforeEdit int    `json:"reported_test_failures_before_edit"`
}
