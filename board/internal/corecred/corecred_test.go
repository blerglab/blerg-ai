package corecred

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const internalKey = "internal-key-0123456789abcdef"

// fakeCore mimics core's POST /internal/credentials/fetch.
func fakeCore(t *testing.T, status int, plaintext string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/credentials/fetch" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Internal-Key") != internalKey {
			http.Error(w, "invalid internal key", http.StatusUnauthorized)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["account_id"] != "acct-1" || body["engine"] != "hermes" || body["token_id"] != "tok-1" {
			t.Errorf("body = %v", body)
		}
		if status != http.StatusOK {
			http.Error(w, "echo "+internalKey, status) // a body the client must not repeat
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"plaintext_base64": base64.StdEncoding.EncodeToString([]byte(plaintext)),
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetch(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		status    int
		plaintext string
		key       string
		want      error
	}{
		{"ok", http.StatusOK, "OPENAI_BASE_URL=http://box/v1\n", internalKey, nil},
		{"no credential or dead token", http.StatusNotFound, "", internalKey, ErrNotFound},
		{"wrong internal key", http.StatusOK, "x", "wrong-key", ErrUnauthorized},
		{"core broken", http.StatusInternalServerError, "", internalKey, ErrUnavailable},
		{"empty credential", http.StatusOK, "  \n", internalKey, ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeCore(t, tc.status, tc.plaintext)
			got, err := New(srv.URL+"/", tc.key, nil).Fetch(ctx, "acct-1", "hermes", "tok-1")
			if tc.want == nil {
				if err != nil || string(got) != tc.plaintext {
					t.Fatalf("Fetch = %q, %v", got, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), internalKey) || strings.Contains(err.Error(), "wrong-key") {
				t.Fatalf("error leaks a key: %v", err)
			}
		})
	}
}

// Without token_id core would answer for any logged-in account, so board
// never sends such a request.
func TestFetchRequiresTokenID(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	_, err := New(srv.URL, internalKey, nil).Fetch(context.Background(), "acct-1", "claude", "")
	if !errors.Is(err, ErrNoTokenID) || called {
		t.Fatalf("err = %v, called = %v — want a local refusal", err, called)
	}
}

func TestFetchNotConfigured(t *testing.T) {
	for _, f := range []*Fetcher{New("", internalKey, nil), New("http://core", "", nil), nil} {
		if _, err := f.Fetch(context.Background(), "a", "claude", "t"); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("err = %v, want ErrNotConfigured", err)
		}
	}
}

func TestFetchCoreUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	_, err := New(srv.URL, internalKey, nil).Fetch(context.Background(), "a", "claude", "t")
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), internalKey) {
		t.Fatalf("err = %v", err)
	}
}
