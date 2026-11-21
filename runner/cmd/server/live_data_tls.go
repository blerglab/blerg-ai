package main

import (
	"os"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/runner/internal/server"
)

func configureLiveDataTLS(api *server.API) *livetls.Identity {
	api.SetLiveDataDesktop(os.Getenv("BLERG_LIVE_DATA_DESKTOP") == "true")
	dir := os.Getenv("BLERG_LIVE_DATA_TLS_DIR")
	if dir == "" {
		return nil
	}
	identity, _ := livetls.Available(dir, "runner", time.Now())
	if identity == nil {
		return nil
	}
	url := identity.PeerURL()
	if os.Getenv("BLERG_LIVE_DATA_DESKTOP") == "true" {
		url = identity.DesktopPeerURL()
	}
	api.SetLiveDataAuthority(&ld.ServiceClient{URL: url, HTTP: identity.Client()})
	api.SetLiveDataApprovalURL(url)
	api.SetLiveDataApprovalClient(identity.Client())
	api.SetLiveDataDefinitionIdentity(identity.PeerURI)
	api.SetLiveDataPeerVerifier(identity.VerifyRequest)
	return identity
}
