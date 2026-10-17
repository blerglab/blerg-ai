package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Each callback owns its path even if an earlier fixture's pending delivery
// reaches a reused loopback port. Reject strays before invoking the test handler.
func newWebhookReceiver(t *testing.T, handler http.HandlerFunc) (*httptest.Server, string) {
	t.Helper()
	path := "/hook/" + newUUID()
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		handler(w, r)
	}))
	return receiver, receiver.URL + path
}

func waitForWebhookRecorded(t *testing.T, pool *pgxpool.Pool, sessionID string) {
	t.Helper()
	waitFor(t, "the completion delivery to finish recording", func() bool {
		events := webhookEvents(t, pool, sessionID)
		return len(events) == 1 && events[0]["status"] == float64(http.StatusOK) && events[0]["ok"] == true
	})
}

func TestWebhookReceiverRejectsUnrelatedDeliveries(t *testing.T) {
	var hits atomic.Int32
	receiver, callback := newWebhookReceiver(t, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	defer receiver.Close()
	for _, request := range []struct {
		method, target string
		status         int
	}{
		{http.MethodPost, receiver.URL + "/hook/previous-fixture", http.StatusNotFound},
		{http.MethodGet, callback, http.StatusNotFound},
		{http.MethodPost, callback, http.StatusNoContent},
	} {
		req, err := http.NewRequest(request.method, request.target, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		response, err := receiver.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != request.status {
			t.Errorf("%s: status %d, want %d", request.method, response.StatusCode, request.status)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("receiver counted %d deliveries, want only the one matching this fixture", got)
	}
}
