package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkerPriorityAPI(t *testing.T) {
	f := newReposRootFixture(t)
	f.mux.HandleFunc("GET /api/daemons/{id}/worker-priority", f.api.HandleWorkerPriority)
	f.mux.HandleFunc("PUT /api/daemons/{id}/worker-priority", f.api.HandleWorkerPriority)
	call := func(method, id, body string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/daemons/"+id+"/worker-priority", bytes.NewBufferString(body))
		if auth {
			r.Header.Set("Authorization", "Bearer "+f.token)
		}
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(&reviewDeadlineWriter{ResponseRecorder: rec}, r)
		return rec
	}
	if r := call("GET", f.dc.ID, "", false); r.Code != 401 {
		t.Fatal(r.Code)
	}
	if r := call("GET", "missing", "", true); r.Code != 404 {
		t.Fatal(r.Code)
	}
	if r := call("PUT", f.dc.ID, `{}`, true); r.Code != 400 {
		t.Fatal(r.Code)
	}
	for _, method := range []string{"GET", "PUT"} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			raw := <-f.dc.send
			var req protocol.WorkerPriorityRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Error(err)
				return
			}
			if req.Type != "worker_priority" || (method == "PUT" && (req.Enabled == nil || *req.Enabled)) {
				t.Error("wrong request", req)
			}
			// An unrelated connection cannot satisfy a desktop request.
			f.hub.deliverWorkerPriorityResult(&DaemonConn{}, protocol.WorkerPriorityResult{RequestID: req.RequestID, OK: true, Enabled: true})
			f.hub.deliverWorkerPriorityResult(f.dc, protocol.WorkerPriorityResult{RequestID: req.RequestID, OK: true, Enabled: false, Backend: "nice + ionice"})
		}()
		r := call(method, f.dc.ID, `{"enabled":false}`, true)
		<-done
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		var result protocol.WorkerPriorityResult
		if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil || result.Enabled || result.Backend != "nice + ionice" {
			t.Fatal(r.Body.String(), err)
		}
	}
	fixtureOwnDesktop(t, f.pool, f.dc, "human:other-owner")
	if r := call("GET", f.dc.ID, "", true); r.Code != 404 {
		t.Fatal("another owner's desktop visible", r.Code)
	}

}
