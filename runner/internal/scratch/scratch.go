// Package scratch names the workspace of a "No repository" session.
//
// Only a cluster pod is truly folder-less: its filesystem goes away with it.
// A session on a daemon (This machine, or the Local sandbox, which bind-mounts
// the same host folder) still runs in a real folder under the daemon's repos
// root, so "No repository" there means a disposable folder with a generated
// name. That folder is one dot-prefixed path segment, which is what keeps it
// out of the daemon's reported folder list (listReposOnDisk skips dot-dirs)
// and so out of the repo picker, and what marks the session as having no
// repository wherever its repo is shown. The name is the single source of
// truth — there is no separate flag on the session row.
package scratch

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Prefix begins every scratch folder name.
const Prefix = ".scratch-"

// maxSuffix bounds what follows Prefix, so a caller-chosen name stays a
// reasonable single path segment.
const maxSuffix = 64

// Valid reports whether name is a scratch folder name: Prefix followed by 1-64
// letters, digits, '-' or '_', the first a letter or digit. It is therefore
// always exactly one path segment — no '/', no '..' — so the daemon's path
// containment checks apply to it unchanged.
func Valid(name string) bool {
	suffix, ok := strings.CutPrefix(name, Prefix)
	if !ok || suffix == "" || len(suffix) > maxSuffix {
		return false
	}
	for i, c := range suffix {
		alnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !alnum && (i == 0 || (c != '-' && c != '_')) {
			return false
		}
	}
	return true
}

// randomHex is 64 bits from crypto/rand as 16 hex digits. Entropy is defence
// in depth only: the daemon creates a scratch folder with os.Mkdir and never
// reuses an existing one, whatever the name.
func randomHex() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a fixed fallback
		// would collide, so panic rather than hand out a shared folder.
		panic("scratch: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// NewName returns a fresh scratch folder name, e.g. ".scratch-3f9a01c2d4e5f607"
// (64 random bits), for callers that did not choose one (the launch sheet
// picks its own so it can show the name before Launch).
func NewName() string {
	return Prefix + randomHex()
}

// Retry derives a fresh name from one that was already taken: its suffix,
// shortened as needed, plus "-" and 64 new random bits — so a folder the
// person named keeps that name recognisably, and the result is always Valid
// when name was.
func Retry(name string) string {
	base := strings.TrimPrefix(name, Prefix)
	const keep = maxSuffix - 17 // room for "-" + 16 hex digits
	if len(base) > keep {
		base = strings.TrimRight(base[:keep], "-_")
	}
	if base == "" {
		return NewName()
	}
	return Prefix + base + "-" + randomHex()
}

// IsNoRepo reports whether a session's recorded repo means "No repository":
// empty (a cluster session, which has no folder at all) or a scratch folder
// (a daemon session). A real repository's name can never look like either —
// every spawn path refuses an empty repo without no_repo, and a leading dot.
func IsNoRepo(repo string) bool {
	return repo == "" || strings.HasPrefix(repo, Prefix)
}
