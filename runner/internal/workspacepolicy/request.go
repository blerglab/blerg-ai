package workspacepolicy

import (
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

func workspaceUnsafePath(name string) bool {
	if name == "" || len(name) > 4096 || !utf8.ValidString(name) || filepath.IsAbs(name) || strings.HasPrefix(name, "~") || strings.Contains(name, "\\") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return true
	}
	if len(name) >= 2 && name[1] == ':' && ((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z')) {
		return true
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// CleanRequest validates and normalizes an untrusted workspace path.
func CleanRequest(name string) (string, bool) {
	if workspaceUnsafePath(name) {
		return "", false
	}
	// The HTTP layer decodes once. Repeated encodings must not turn a planted
	// link into traversal in a later peer or viewer. The actual OS name is not
	// decoded here; safe literal percent-encoded filenames remain literal.
	decoded := name
	for i := 0; i < 8; i++ {
		next, err := url.PathUnescape(decoded)
		if err != nil {
			return "", false
		}
		if next == decoded {
			clean := filepath.Clean(name)
			return clean, clean != "."
		}
		if workspaceUnsafePath(next) {
			return "", false
		}
		decoded = next
	}
	return "", false
}

// ExplorerPath applies the explorer's canonical syntax and directory visibility
// policy. An empty request names the workspace root.
func ExplorerPath(name string) (string, bool) {
	if name == "" {
		return ".", true
	}
	clean, ok := CleanRequest(name)
	return clean, ok && clean == name && Directory(clean)
}

// SafeSegment is a single public explorer directory name. Dot-leading names
// follow Directory's visibility policy, just as they do in the explorer.
func SafeSegment(name string) bool {
	_, ok := ExplorerPath(name)
	return ok && name != "" && !strings.ContainsAny(name, "/\\")
}
