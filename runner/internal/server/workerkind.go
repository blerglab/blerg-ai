package server

import (
	"fmt"
	"regexp"
)

var workerTicket = regexp.MustCompile(`(^|[[:space:](])([A-Z][A-Z0-9]*-[0-9]+|#[0-9]+|board:[^[:space:]]+#[0-9]+)($|[^[:alnum:]_])`)

func resolveWorkerKind(kind, title string) (string, error) {
	switch kind {
	case "card", "role", "review", "research":
		return kind, nil
	case "":
		if workerTicket.MatchString(title) {
			return "card", nil
		}
		return "", nil
	default:
		return "", fmt.Errorf("kind must be card, role, review or research")
	}
}
