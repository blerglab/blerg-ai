package daemon

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
)

var errConnectedCloneDestinationExists = errors.New("the clone destination already exists with different contents")
var errConnectedCloneFailed = errors.New("git could not complete the approved clone")

func connectedCloneOptions(extraEnv []string) hostgit.Options {
	env := append(gitSafetyEnv(), extraEnv...)
	// hostgit deliberately drops inherited GIT_ variables. These two contain
	// trust-store paths only; no other Git configuration enters this call.
	for _, name := range []string{"GIT_SSL_CAINFO", "GIT_SSL_CAPATH"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return hostgit.Options{Protocols: []string{"https"}, PassEnv: []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"}, Env: env, Config: []string{"http.followRedirects=false"}}
}

func gitCloneConnected(ctx context.Context, timeout time.Duration, remote, dest string, extraEnv []string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// No inherited configuration, credential helpers, URL rewrites, hooks or
	// redirects. The token header is scoped to the registered host.
	if err := hostgit.Run(ctx, connectedCloneOptions(extraEnv), "clone", "--no-recurse-submodules", "--", remote, dest); err != nil {
		return errors.New("approved repository clone failed")
	}
	return nil
}
