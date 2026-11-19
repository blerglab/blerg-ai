package livedata

import (
	"context"
	"time"
)

// ProviderRequest travels only over dedicated service mTLS. Neither it nor the
// credential response is a socket, document, agent or persisted-row type.
type ProviderRequest struct {
	Token   string `json:"token"`
	Attempt string `json:"attempt"`
	Stage   string `json:"stage"`
}
type AttemptReceipt struct {
	Scope   Scope  `json:"scope"`
	Attempt string `json:"attempt"`
	Stage   string `json:"stage"`
}
type ProviderCredential struct {
	URL        string    `json:"url"`
	HeaderName string    `json:"header_name"`
	Value      string    `json:"value"`
	ExpiresAt  time.Time `json:"expires_at"`
}
type providerContext struct{}

func WithProviderRequest(ctx context.Context, request ProviderRequest) context.Context {
	return context.WithValue(ctx, providerContext{}, request)
}
func ProviderRequestFrom(ctx context.Context) (ProviderRequest, bool) {
	r, ok := ctx.Value(providerContext{}).(ProviderRequest)
	return r, ok
}
func (c *ServiceClient) ProviderCredential(ctx context.Context, request ProviderRequest) (ProviderCredential, error) {
	var out ProviderCredential
	err := c.request(ctx, "provider", request, &out)
	return out, err
}
