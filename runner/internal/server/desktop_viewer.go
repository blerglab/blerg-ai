package server

import (
	"context"
	"net/http"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

const desktopViewerHeader = "X-Blerg-Desktop-Viewer"

type desktopViewerKey struct{}

func withDesktopViewer(ctx context.Context, account string) context.Context {
	return context.WithValue(ctx, desktopViewerKey{}, account)
}
func desktopViewer(ctx context.Context, fallback string) string {
	if value, ok := ctx.Value(desktopViewerKey{}).(string); ok {
		return value
	}
	return fallback
}
func (a *API) boardDesktopViewer(r *http.Request) string {
	raw := r.Header.Get(desktopViewerHeader)
	if raw == "" || a.coreAuth == nil {
		return ""
	}
	p, err := identity.Verify(raw, "blerg-board", a.coreAuth.KeySet(), a.coreAuth, coreAuthSensitiveCaps)
	if err != nil || p.Kind != string(identity.Human) || p.Sid == "" || p.OnBehalfOf != "" || !p.Has(coreAuthBrowserCap) {
		return ""
	}
	return p.Sub
}

// A broker's stored automation bearer is not the identity of its human
// recipient. Signed viewer proof may only reduce the actor's desktop scope.
func (a *API) desktopRecipient(r *http.Request, actor string) string {
	if r.Header.Get(desktopViewerHeader) == "" {
		return actor
	}
	viewer := a.boardDesktopViewer(r)
	if viewer == "" || viewer != actor {
		return ""
	}
	return actor
}
