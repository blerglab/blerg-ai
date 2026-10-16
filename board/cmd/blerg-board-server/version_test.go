package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVersionHandlerReportsReleaseAndCommit(t *testing.T) {
	rec := httptest.NewRecorder()
	versionHandler("510d0c0", "0.2.0", "510d0c0")(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
	}
	want := map[string]string{"version": "510d0c0", "release": "0.2.0", "commit": "510d0c0"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (body %s)", k, got[k], v, rec.Body.String())
		}
	}
}
