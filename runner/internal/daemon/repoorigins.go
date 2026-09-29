package daemon

import (
	"github.com/blerglab/blerg-ai/runner/internal/gitremote"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// repoReport resolves each checked-out folder's origin once and returns both
// wire shapes a hello/heartbeat carries: repo_origins (every registered git
// provider) and the legacy GitHub-only repo_remotes an older server reads.
// origins is nil when nothing resolved, so the field is omitted.
func repoReport(root string, names []string) (origins map[string]protocol.RepoOrigin, legacy map[string]string) {
	refs := gitremote.Origins(root, names)
	if len(refs) > 0 {
		origins = make(map[string]protocol.RepoOrigin, len(refs))
		for n, ref := range refs {
			origins[n] = protocol.RepoOrigin{Provider: ref.Provider, FullName: ref.FullName()}
		}
	}
	return origins, gitremote.GitHubOnly(refs)
}
