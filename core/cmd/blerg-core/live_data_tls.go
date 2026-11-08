package main

import (
	"context"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/core/internal/api"
)

func runLiveDataPKI(args []string) error {
	flags := flag.NewFlagSet("live-data-pki", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "private provisioning root")
	uid := flags.Int("uid", -1, "runtime uid")
	gid := flags.Int("gid", -1, "runtime gid")
	issuerDir := flags.String("issuer-state-dir", "", "Core-only writable issuer state")
	if flags.Parse(args) != nil || *root == "" || flags.NArg() != 0 {
		return livetls.ErrIdentity
	}
	if *issuerDir != "" {
		if os.MkdirAll(*issuerDir, 0700) != nil || os.Chmod(*issuerDir, 0700) != nil { // #nosec G302 -- execute access is required on this Core-only directory, not a file.
			return livetls.ErrIdentity
		}
		if *uid >= 0 && os.Chown(*issuerDir, *uid, *gid) != nil {
			return livetls.ErrIdentity
		}
	}
	if e := livetls.Provision(*root); e != nil {
		return e
	}
	if *uid >= 0 {
		for _, role := range []string{"core", "runner"} {
			bundle, e := os.OpenRoot(filepath.Join(*root, role))
			if e != nil {
				return livetls.ErrIdentity
			}
			for _, name := range []string{".", "key.pem", "cert.pem", "ca.pem", "identity.json"} {
				if e = bundle.Lchown(name, *uid, *gid); e != nil {
					_ = bundle.Close()
					return livetls.ErrIdentity
				}
			}
			if e = bundle.Close(); e != nil {
				return livetls.ErrIdentity
			}
		}
	}
	return nil
}
func configureLiveDataTLS(deps *api.Deps) *livetls.Identity {
	deps.LiveDataDesktop = os.Getenv("BLERG_LIVE_DATA_DESKTOP") == "true"
	dir := os.Getenv("BLERG_LIVE_DATA_TLS_DIR")
	if dir == "" {
		return nil
	}
	identity, _ := livetls.Available(dir, "core", time.Now())
	if identity == nil {
		return nil
	}
	deps.LiveDataRunnerURL = identity.PeerURL()
	if os.Getenv("BLERG_LIVE_DATA_DESKTOP") == "true" {
		deps.LiveDataRunnerURL = identity.DesktopPeerURL()
	}
	deps.LiveDataRunnerClient = identity.Client()
	deps.LiveDataRunnerIdentity = func(r *http.Request) (ld.RunnerIdentity, error) {
		if e := identity.VerifyRequest(r); e != nil {
			return ld.RunnerIdentity{}, e
		}
		return ld.AuthenticatedRunner(r)
	}
	return identity
}
func startLiveDataTLS(ctx context.Context, identity *livetls.Identity, handler http.Handler, onFailure func()) (io.Closer, error) {
	if identity == nil {
		return nil, nil
	}
	return identity.Listen(ctx, ":8443", handler, onFailure)
}

func runLiveDataRenew(args []string) error { return livetls.RunRenew(args) }
