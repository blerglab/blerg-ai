// Package releasenotes contains the release's own bounded, offline, person-facing notes.
package releasenotes

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed notes/*.json
var files embed.FS

const MaxBytes = 32 << 10

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func Valid(v string) bool { return versionPattern.MatchString(v) }

func Compare(a, b string) int {
	for i, part := range strings.Split(strings.TrimPrefix(a, "v"), ".") {
		av, _ := strconv.ParseUint(part, 10, 64)
		bp := strings.Split(strings.TrimPrefix(b, "v"), ".")
		if len(bp) != 3 {
			return 1
		}
		bv, _ := strconv.ParseUint(bp[i], 10, 64)
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

type Release struct {
	Version string   `json:"version"`
	Title   string   `json:"title"`
	Items   []string `json:"items"`
}

func Through(current string) ([]Release, error) {
	out := []Release{}
	if !Valid(current) {
		return out, nil
	}
	entries, err := files.ReadDir("notes")
	if err != nil {
		return nil, err
	}
	total := 0
	for _, entry := range entries {
		raw, err := files.ReadFile("notes/" + entry.Name())
		if err != nil {
			return nil, err
		}
		total += len(raw)
		if total > MaxBytes {
			return nil, fmt.Errorf("release notes exceed %d bytes", MaxBytes)
		}
		var release Release
		if err := json.Unmarshal(raw, &release); err != nil {
			return nil, err
		}
		if !Valid(release.Version) || release.Title == "" || len(release.Items) == 0 {
			return nil, fmt.Errorf("invalid release notes: %s", entry.Name())
		}
		if Compare(release.Version, current) <= 0 {
			out = append(out, release)
		}
	}
	sort.Slice(out, func(i, j int) bool { return Compare(out[i].Version, out[j].Version) < 0 })
	return out, nil
}
