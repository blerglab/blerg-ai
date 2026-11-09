package releaseperf

import (
	"fmt"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9]+(?:[.\-][A-Za-z0-9]+)*$`)

// ValidateVersion rejects a --version value that could escape the evidence
// directory layout (main.go joins it straight into a candidate filename) or
// that otherwise isn't a release label: a path separator, a ".."
// traversal segment, or anything outside a semver-ish token of
// dot/hyphen-separated alphanumerics (MED f, Codex review of d7e50390).
func ValidateVersion(v string) error {
	if v == "" {
		return fmt.Errorf("version must not be empty")
	}
	if strings.ContainsAny(v, "/\\") {
		return fmt.Errorf("version %q must not contain a path separator", v)
	}
	if strings.Contains(v, "..") {
		return fmt.Errorf("version %q must not contain \"..\"", v)
	}
	if !versionPattern.MatchString(v) {
		return fmt.Errorf("version %q is not a semver-ish token (letters, digits, '.' and '-' only)", v)
	}
	return nil
}
