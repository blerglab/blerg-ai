package coremint

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMintSendsKeyAndProofAndReadsTheToken(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/tokens/board-automation" || r.Header.Get("Authorization") != "Bearer the-key" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"tok-1","token":"raw-token","name":"Board Work automation","expires_at":"2027-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()
	m, err := New(srv.URL+"/", "the-key", nil).Mint(context.Background(), "acct", "b1", "Work", "access")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "tok-1" || m.Token != "raw-token" || m.Name != "Board Work automation" || m.ExpiresAt.Year() != 2027 {
		t.Fatalf("minted = %+v", m)
	}
	if got["account_id"] != "acct" || got["board_id"] != "b1" || got["board_name"] != "Work" || got["access_token"] != "access" {
		t.Fatalf("request body = %v", got)
	}
}

func TestMintErrorsNeverCarryTheBody(t *testing.T) {
	for code, want := range map[int]error{
		http.StatusNotFound:            ErrRefused,
		http.StatusTooManyRequests:     ErrRateLimited,
		http.StatusConflict:            ErrTooManyTokens,
		http.StatusUnauthorized:        ErrUnauthorized,
		http.StatusServiceUnavailable:  ErrDisabled,
		http.StatusForbidden:           ErrForbidden,
		http.StatusInternalServerError: ErrUnavailable,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "secret-detail", code)
		}))
		_, err := New(srv.URL, "k", nil).Mint(context.Background(), "a", "b", "n", "t")
		srv.Close()
		if !errors.Is(err, want) || strings.Contains(err.Error(), "secret-detail") {
			t.Errorf("HTTP %d: err = %v, want %v without the body", code, err, want)
		}
	}
}

// A redirect is never followed: the key and the person's token go to core and nowhere else.
func TestMintDoesNotFollowRedirects(t *testing.T) {
	hit := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "k", nil).Mint(context.Background(), "a", "b", "n", "t"); err == nil || hit {
		t.Fatalf("redirect followed (hit=%v err=%v)", hit, err)
	}
}

func TestRevokeTreatsGoneAsDone(t *testing.T) {
	for code, ok := range map[int]bool{http.StatusOK: true, http.StatusNotFound: true, http.StatusUnauthorized: false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/internal/tokens/board-automation/revoke" {
				t.Errorf("path %s", r.URL.Path)
			}
			w.WriteHeader(code)
		}))
		err := New(srv.URL, "k", nil).Revoke(context.Background(), "a", "t", "b")
		srv.Close()
		if (err == nil) != ok {
			t.Errorf("HTTP %d: err = %v", code, err)
		}
	}
	if err := New("", "", nil).Revoke(context.Background(), "a", "t", "b"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("unconfigured: %v", err)
	}
}
