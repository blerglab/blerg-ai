package testreport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

// SuiteBudget is a project's explicit development target, never gate policy.
type SuiteBudget struct {
	Layer   string   `json:"layer"`
	Seconds *float64 `json:"budget_seconds"`
}

// ValidLayer accepts only the documented proof layers; unknown is the empty string.
func ValidLayer(layer string) bool {
	switch layer {
	case "1", "2", "3a", "3b", "4", "5":
		return true
	}
	return false
}

// ValidBudget accepts a finite positive target no larger than the run bound.
func ValidBudget(value *float64) bool { return value != nil && seconds(*value) && *value > 0 }

// ReadBudgets reads an explicit, versioned, bounded project-owned configuration.
func ReadBudgets(reader io.Reader) (map[string]SuiteBudget, error) {
	data, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if err != nil {
		return nil, fmt.Errorf("budget configuration read failed (%s)", failurelog.Cause(err))
	}
	if len(data) > 64<<10 {
		return nil, errors.New("budget configuration exceeds 64 KiB")
	}
	var config struct {
		Version int                    `json:"version"`
		Suites  map[string]SuiteBudget `json:"suites"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF || config.Version != 1 || config.Suites == nil || len(config.Suites) > 128 {
		return nil, errors.New("invalid budget configuration")
	}
	for name, budget := range config.Suites {
		if !suiteName.MatchString(name) || !ValidLayer(budget.Layer) || !ValidBudget(budget.Seconds) {
			return nil, errors.New("invalid suite budget or layer")
		}
	}
	return config.Suites, nil
}
