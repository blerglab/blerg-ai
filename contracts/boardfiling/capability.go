package boardfiling

import (
	"errors"
	"regexp"
	"strings"
)

const CapabilityPrefix = "card.create_once:"

var intentPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Capability encodes the approved intent and payload in one signed, narrowly
// parameterized capability. It grants no general read, write or administration.
func Capability(intent, digest string) (string, error) {
	if !intentPattern.MatchString(intent) || !digestPattern.MatchString(digest) {
		return "", errors.New("invalid board filing intent or digest")
	}
	return CapabilityPrefix + intent + ":" + digest, nil
}

// ParseCapability refuses broader tokens: only one canonical create-once
// capability is valid. Callers must additionally verify the Core signature,
// audience, project, principal/account, liveness and atomic intent consumption.
func ParseCapability(caps []string) (intent, digest string, ok bool) {
	if len(caps) != 1 || !strings.HasPrefix(caps[0], CapabilityPrefix) || len(caps[0]) != len(CapabilityPrefix)+36+1+64 {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(caps[0], CapabilityPrefix), ":")
	if len(parts) != 2 || !intentPattern.MatchString(parts[0]) || !digestPattern.MatchString(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}
