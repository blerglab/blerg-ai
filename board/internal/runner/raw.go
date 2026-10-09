package runner

import (
	"context"
	"io"
	"net/http"
	"time"
)

// RawDoer is implemented by drivers that can pass a request through to the
// runner and hand back the response as it is — body unread, however long it
// runs. The chat proxy needs exactly that: a session's live event stream and
// its file bytes are not JSON to decode and may stay open for hours, which
// the Driver's own verbs (bounded, decoded) cannot carry.
type RawDoer interface {
	// Raw sends method path (path includes any query) to the runner with the
	// driver's own credential and the given extra headers. The caller closes
	// the response body. ctx bounds the request: cancel it and the runner's
	// side is cancelled too.
	Raw(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error)
}

// streamClient has no overall timeout — a live stream is open-ended and ctx is
// what ends it — but still gives up on a runner that accepts the connection and
// never answers.
var streamClient = &http.Client{Transport: streamTransport()}

func streamTransport() http.RoundTripper {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	t = t.Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	return t
}

// Raw implements RawDoer with the shared key, the same credential that polls,
// messages and stops. It is never the start identity: nothing here starts a
// session.
func (k *BlergRunner) Raw(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, k.url+path, body)
	if err != nil {
		return nil, err
	}
	for name, v := range headers {
		req.Header.Set(name, v)
	}
	req.Header.Set("Authorization", "Bearer "+k.key)
	return streamClient.Do(req)
}
