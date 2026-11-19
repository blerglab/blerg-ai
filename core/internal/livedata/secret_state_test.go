package livedata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestSecretAuthorityCASAndFailure(t *testing.T) {
	var record secretRecord
	record.APIVersion = "v1"
	record.Kind = "Secret"
	record.Metadata.Name = "blerg-live-data-issuer"
	record.Metadata.Namespace = "blerg"
	record.Metadata.ResourceVersion = "1"
	down, conflict := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-service-account" {
			w.WriteHeader(401)
			return
		}
		if down {
			w.WriteHeader(503)
			return
		}
		if r.Method == "PUT" {
			var next secretRecord
			if json.NewDecoder(r.Body).Decode(&next) != nil {
				w.WriteHeader(400)
				return
			}
			if conflict || next.Metadata.ResourceVersion != record.Metadata.ResourceVersion {
				w.WriteHeader(409)
				return
			}
			v, _ := strconv.Atoi(record.Metadata.ResourceVersion)
			record = next
			record.Metadata.ResourceVersion = strconv.Itoa(v + 1)
		}
		_ = json.NewEncoder(w).Encode(record)
	}))
	defer server.Close()
	backend := &SecretAuthority{HTTP: server.Client(), URL: server.URL, Token: func() (string, error) { return "fixture-service-account", nil }}
	if _, e := backend.Start(context.Background(), false); e == nil {
		t.Fatal("lost Secret initialized despite authority history")
	}
	first, e := backend.Start(context.Background(), true)
	if e != nil || first.Epoch() != 1 {
		t.Fatal("first epoch", e)
	}
	if e = first.AdvanceRevocations(t.Context(), 7); e != nil {
		t.Fatal(e)
	}
	if e = first.AdvanceRevocations(t.Context(), 2); e != nil {
		t.Fatal(e)
	}
	if mark, e := first.RevocationMark(t.Context()); e != nil || mark != 7 {
		t.Fatal("external mark lowered", mark, e)
	}
	firstRecord := record
	restarted, e := backend.Start(context.Background(), false)
	if e != nil || restarted.Epoch() != 1 || first.Epoch() != 1 || restarted.boot.KeyID != first.boot.KeyID {
		t.Fatal("ordinary restart changed authority", e)
	}
	second, e := backend.Fence(context.Background())
	if e != nil || second.Epoch() != 2 || first.Epoch() != 0 {
		t.Fatal("external restart fence", e)
	}
	if mark, e := second.RevocationMark(t.Context()); e != nil || mark != 7 {
		t.Fatal("fence lost mark", mark, e)
	}
	if e = first.AdvanceRevocations(t.Context(), 8); e == nil {
		t.Fatal("old process advanced mark through fence")
	}
	conflict = true
	if e = second.AdvanceRevocations(t.Context(), 8); e == nil {
		t.Fatal("mark CAS conflict ignored")
	}

	if _, e = backend.Fence(context.Background()); e == nil {
		t.Fatal("CAS conflict ignored")
	}
	conflict = false
	down = true
	if _, e = second.RevocationMark(t.Context()); e == nil {
		t.Fatal("cached mark during outage")
	}
	if e = second.AdvanceRevocations(t.Context(), 8); e == nil {
		t.Fatal("mark advanced during outage")
	}
	if second.Epoch() != 0 {
		t.Fatal("cached positive authority during control-plane outage")
	}
	down = false
	if second.Epoch() != 2 {
		t.Fatal("transient failure permanently fenced unchanged authority")
	}
	latest := record
	record = firstRecord
	if first.Epoch() != 0 {
		t.Fatal("observed epoch rollback revived old process")
	}
	record = latest
	record.Data = nil
	if _, e = backend.Start(context.Background(), true); e == nil {
		t.Fatal("initialized Secret silently reinitialized")
	}
}

func TestSecretAuthorityNeverRedirectsServiceCredential(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1); _, _ = w.Write([]byte(`{}`)) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	authority := SecretAuthority{HTTP: redirect.Client(), URL: redirect.URL, Token: func() (string, error) { return "fixture-service-account", nil }}
	if _, e := authority.request(t.Context(), "GET", nil); e == nil {
		t.Fatal("redirect response accepted")
	}
	if targetCalls.Load() != 0 {
		t.Fatal("authority store followed credential-bearing redirect")
	}
}
