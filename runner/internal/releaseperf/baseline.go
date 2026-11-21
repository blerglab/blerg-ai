package releaseperf

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Verdict is a run's overall outcome. The three are never conflated (the
// design's failure rule): a noisy or mismatched host gives no verdict, never
// a false clean or a false regressed.
type Verdict string

const (
	VerdictClean     Verdict = "clean"
	VerdictRegressed Verdict = "regressed"
	VerdictNoVerdict Verdict = "no_verdict"
)

// RegressionMargin is the minimum percentage a scenario's median must move
// before it's even considered — the design's "start at 15%".
const RegressionMargin = 0.15

// BaselineScenario is one scenario's recorded numbers in a baseline file.
type BaselineScenario struct {
	Unit          string  `json:"unit"`
	N             int     `json:"n"`
	Median        float64 `json:"median"`
	P95           float64 `json:"p95"`
	BudgetP95     float64 `json:"budget_p95,omitempty"`
	NoiseMedianMs float64 `json:"noise_median_ms"`
}

// Baseline is the committed, per-release file format from
// docs/design/release-performance.md.
type Baseline struct {
	Version     string                      `json:"version"`
	Commit      string                      `json:"commit"`
	RecordedAt  time.Time                   `json:"recorded_at"`
	AcceptedAt  time.Time                   `json:"accepted_at,omitempty"`
	ReleaseTag  string                      `json:"release_tag,omitempty"`
	SeedVersion int                         `json:"seed_version"`
	Host        HostFingerprint             `json:"host"`
	Scenarios   map[string]BaselineScenario `json:"scenarios"`
	// Verdict is carried on a candidate file only, so AcceptBaseline can
	// refuse to promote anything but a clean run. It is not part of the
	// design's documented official-baseline schema and is omitted once a
	// baseline is empty.
	Verdict Verdict `json:"verdict,omitempty"`
}

// LoadBaseline reads a baseline (official or candidate) JSON file.
func LoadBaseline(path string) (*Baseline, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: an evidence/baseline path the operator passes via --baseline or --logs

	if err != nil {
		return nil, err
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("parse baseline %s: %w", path, err)
	}
	return &b, nil
}

// SaveBaseline writes a baseline (official or candidate) JSON file.
func SaveBaseline(path string, b Baseline) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// AcceptBaseline promotes a candidate to the official, tag-stamped baseline
// — but only when the candidate's own run was clean. This is the explicit,
// owner-gated step docs/design/release-performance.md calls "Baseline
// acceptance (M5)": a run producing numbers and those numbers becoming the
// thing future releases are judged against are two separate decisions.
func AcceptBaseline(candidatePath, officialPath, releaseTag string) error {
	candidate, err := LoadBaseline(candidatePath)
	if err != nil {
		return err
	}
	if candidate.Verdict != VerdictClean {
		return fmt.Errorf("refusing to accept %s as a baseline: its run verdict was %q, not clean", candidatePath, candidate.Verdict)
	}
	candidate.AcceptedAt = time.Now().UTC()
	candidate.ReleaseTag = releaseTag
	candidate.Verdict = ""
	return SaveBaseline(officialPath, *candidate)
}

// ScenarioComparison is one scenario's comparison result.
type ScenarioComparison struct {
	Name           string
	Verdict        Verdict // clean, regressed, or no_verdict is never set here (host mismatch is run-level)
	MedianBefore   float64
	P95Before      float64
	MedianNow      float64
	PercentChange  float64
	P95Now         float64
	BudgetExceeded bool
	New            bool
}

// RunComparison is a whole run's comparison against a baseline.
type RunComparison struct {
	Verdict           Verdict
	HostMismatchField string
	Scenarios         []ScenarioComparison
}

// CompareRun compares a run's scenarios against a baseline, applying the
// host-fingerprint check first (a mismatch short-circuits to no verdict —
// docs/design/release-performance.md's H3), then each scenario's
// margin-and-noise rule and budget rule (H4, and the comparison rule).
func CompareRun(run Run, baseline Baseline, host HostFingerprint) RunComparison {
	if field, mismatched := baseline.Host.Mismatch(host); mismatched {
		return RunComparison{Verdict: VerdictNoVerdict, HostMismatchField: field}
	}

	out := RunComparison{Verdict: VerdictClean}
	for _, s := range run.Scenarios {
		comparison := ScenarioComparison{Name: s.Name, Verdict: VerdictClean}
		median := Median(s.ClientSamplesMs)
		p95 := P95(s.ClientSamplesMs)
		comparison.MedianNow = median
		comparison.P95Now = p95

		base, known := baseline.Scenarios[s.Name]
		if !known {
			comparison.New = true
			out.Scenarios = append(out.Scenarios, comparison)
			continue
		}
		comparison.MedianBefore = base.Median
		comparison.P95Before = base.P95
		if base.Median > 0 {
			comparison.PercentChange = (median - base.Median) / base.Median
		}

		regressedOnMargin := comparison.PercentChange > RegressionMargin &&
			(median-base.Median) > base.NoiseMedianMs
		budgetExceeded := base.BudgetP95 > 0 && p95 >= base.BudgetP95
		comparison.BudgetExceeded = budgetExceeded

		if regressedOnMargin || budgetExceeded {
			comparison.Verdict = VerdictRegressed
			out.Verdict = VerdictRegressed
		}
		out.Scenarios = append(out.Scenarios, comparison)
	}
	return out
}
