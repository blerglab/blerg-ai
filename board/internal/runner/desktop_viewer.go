package runner

import "context"

type desktopViewerKey struct{}

// WithDesktopViewerProof carries only the current human's already verified
// Board-audience access token. Never retain it in shared metadata caches.
func WithDesktopViewerProof(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, desktopViewerKey{}, token)
}
func desktopViewerProof(ctx context.Context) string {
	token, _ := ctx.Value(desktopViewerKey{}).(string)
	return token
}
