package main

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
)

func selectBuildRoot(current, override string) (string, error) {
	if override == "" {
		return current, nil
	}
	root, err := filepath.Abs(override)
	if err != nil {
		return "", err
	}
	for _, module := range []string{"core", "board", "runner"} {
		if _, err = os.Stat(filepath.Join(root, module, "go.mod")); err != nil {
			return "", errors.New("invalid_source_checkout")
		}
	}
	return root, nil
}
func validateBootOutput(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: fixed boot evidence path in this run's private directory.
	if err != nil {
		return err
	}
	var result struct {
		Status string `json:"status"`
		Boot   struct {
			Samples []float64 `json:"samplesMs"`
		} `json:"boot"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return err
	}
	if result.Status != "complete" || len(result.Boot.Samples) != 3 {
		return errors.New("incomplete_boot_samples")
	}
	for _, v := range result.Boot.Samples {
		if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("invalid_boot_sample")
		}
	}
	return nil
}
