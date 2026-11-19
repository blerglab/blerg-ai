package failurelog

import (
	"fmt"
	"log/slog"
	"net/http"
)

// Transport records upstream status and typed transport causes without URLs or payloads.
// The caller supplies fixed component/upstream labels, never a user-controlled host.
func (l *Recorder) Transport(component, upstream string, next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return transportFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := next.RoundTrip(r)
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		if err != nil || status >= 300 {
			cause := "http_status"
			if err != nil {
				cause = Cause(err)
			}
			l.Event(fmt.Sprintf("upstream:%s:%s:%d", component, upstream, status), slog.LevelError, "component", component, "upstream", upstream, "status", status, "cause", cause)
		}
		return resp, err
	})
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
