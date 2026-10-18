package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVersionHandlerReportsReleaseAndCommit(t *testing.T) {
	rec := httptest.NewRecorder()
	versionHandler("510d0c0", "0.2.0", "510d0c0", nil)(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))
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

func TestVersionHandlerLatestRelease(t *testing.T) {
	read := func(latest func() (string, string)) map[string]string {
		rec := httptest.NewRecorder()
		versionHandler("v", "0.4.0", "c", latest)(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))
		var got map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := read(func() (string, string) { return "0.4.1", "https://github.com/o/r/releases/tag/v0.4.1" })
	if got["latest_version"] != "0.4.1" || got["latest_url"] != "https://github.com/o/r/releases/tag/v0.4.1" {
		t.Fatalf("got %v", got)
	}
	for _, f := range []func() (string, string){nil, func() (string, string) { return "", "" }} {
		if got := read(f); got["latest_version"] != "" || len(got) != 3 {
			t.Fatalf("got %v, want no latest_ keys", got)
		}
	}
}
