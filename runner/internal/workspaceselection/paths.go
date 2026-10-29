package workspaceselection

import (
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspacepolicy"
)

// MemberPathOptions applies runtime-specific projection requirements without
// changing discovery state or the desktop single-repository layout.
type MemberPathOptions struct {
	// Cluster keeps all clones below the volume root and reserves ext4 metadata.
	Cluster bool
}

// MemberPaths returns public member paths in selection order. Physical folder
// selections preserve their relative paths; a desktop single repo stays at root.
// Separate repositories use safe names, falling back to their stable source IDs.
// Structure layout strips the deepest common ancestor; an unsafe projected
// component falls back to names for the whole selection (the returned boolean).
func MemberPaths(selection protocol.DiscoveryResolved, options ...MemberPathOptions) ([]string, bool) {
	cluster := len(options) > 0 && options[0].Cluster
	if selection.Layout == protocol.WorkspaceLayoutStructure && selection.Folder == nil && len(selection.Members) > 1 {
		if paths, ok := structurePaths(selection.Members); ok {
			return paths, false
		}
	}
	paths := make([]string, len(selection.Members))
	for i, source := range selection.Members {
		switch {
		case selection.Folder != nil:
			folder := selection.Folder.Path
			switch {
			case source.Path == folder:
				paths[i] = "."
			case folder == ".":
				paths[i] = source.Path
			default:
				paths[i] = strings.TrimPrefix(source.Path, folder+"/")
			}
		case len(selection.Members) == 1 && !cluster:
			paths[i] = "."
		default:
			paths[i] = source.ID
			if workspacepolicy.SafeSegment(source.Name) && (!cluster || workspacepolicy.FoldName(source.Name) != "lost+found") {
				candidate := boundedMemberName(source.Name, "")
				if workspacepolicy.SafeSegment(candidate) {
					paths[i] = candidate
				}
			}
		}
	}
	if selection.Folder != nil || len(paths) < 2 {
		return paths, false
	}

	// Reserve every original name before assigning suffixes, including names of
	// later selections. A generated name must never take another member's name.
	reserved := append([]string(nil), paths...)
	used := make([]string, 0, len(paths))
	contains := func(names []string, candidate string) bool {
		for _, name := range names {
			if workspacepolicy.FoldName(name) == workspacepolicy.FoldName(candidate) {
				return true
			}
		}
		return false
	}

	for i, base := range paths {
		candidate := base
		if contains(used, candidate) {
			for suffix := 2; ; suffix++ {
				candidate = boundedMemberName(base, "-"+strconv.Itoa(suffix))
				if !contains(used, candidate) && !contains(reserved, candidate) {
					break
				}
			}
		}
		paths[i] = candidate
		used = append(used, candidate)
	}
	return paths, selection.Layout == protocol.WorkspaceLayoutStructure
}

// structurePaths strips only the common parent components, never a shared text
// prefix. All projected components must be usable by the public explorer.
func structurePaths(sources []protocol.DiscoverySource) ([]string, bool) {
	common := strings.Split(path.Dir(sources[0].Path), "/")
	if common[0] == "." {
		common = nil
	}
	for _, source := range sources[1:] {
		parent := strings.Split(path.Dir(source.Path), "/")
		n := 0
		for n < len(common) && n < len(parent) && common[n] == parent[n] {
			n++
		}
		common = common[:n]
	}
	paths := make([]string, len(sources))
	for i, source := range sources {
		components := strings.Split(source.Path, "/")[len(common):]
		for _, component := range components {
			if !workspacepolicy.SafeSegment(component) {
				return nil, false
			}
		}
		paths[i] = strings.Join(components, "/")
		for _, previous := range paths[:i] {
			// A case-insensitive filesystem must also be able to project the tree.
			left, right := workspacepolicy.FoldName(previous), workspacepolicy.FoldName(paths[i])
			if left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/") {
				return nil, false
			}
		}
	}
	return paths, true
}

// Leave room for the suffix within the portable filesystem component limit.
func boundedMemberName(name, suffix string) string {
	limit := 255 - len(suffix)
	if len(name) > limit {
		name = name[:limit]
		// A cut may also split a literal percent escape accepted by the explorer.
		for len(name) > 0 && (!utf8.ValidString(name) || !workspacepolicy.SafeSegment(name+suffix)) {
			name = name[:len(name)-1]
		}
	}
	return name + suffix
}
