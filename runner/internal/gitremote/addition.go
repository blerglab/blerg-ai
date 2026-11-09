package gitremote

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
)

// AdditionFolder binds the destination to the complete provider identity.
func AdditionFolder(ref gitprovider.Ref) string {
	sum := sha256.Sum256([]byte(ref.Key()))
	return fmt.Sprintf("%s-%s-%.16x", ref.Provider, ref.Name, sum[:8])
}

func AdditionName(ref gitprovider.Ref) string {
	p, _ := gitprovider.Default.ProviderForCredentialKind(ref.Provider)
	return p.Host() + "/" + ref.FullName()
}

// ParseAddition accepts only credential-free hosted Git URLs. The returned
// identity, never the supplied URL, is used to construct a clone command.
func ParseAddition(raw string) (gitprovider.Ref, bool) {
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\\%?#") || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return gitprovider.Ref{}, false
	}
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		host, path, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !ok || host == "" || strings.ContainsAny(host, "@/:") || strings.HasPrefix(host, "-") || strings.HasPrefix(path, "/") {
			return gitprovider.Ref{}, false
		}
	} else {
		u, err := url.Parse(raw)
		if err != nil || u.Opaque != "" || u.Host == "" || u.Port() != "" || (u.Scheme != "https" && u.Scheme != "ssh") || u.ForceQuery {
			return gitprovider.Ref{}, false
		}
		if u.User != nil && (u.Scheme != "ssh" || u.User.String() != "git") {
			return gitprovider.Ref{}, false
		}
	}
	return ParseRemote(raw)
}
