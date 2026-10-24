package runner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDesktopViewerProofComesOnlyFromVerifiedRequestContext(t *testing.T) {
	seen := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("X-Blerg-Desktop-Viewer")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	k := NewBlergRunner(srv.URL, "automation-owner-A", "")
	ctx := WithDesktopViewerProof(context.Background(), "signed-person-B")
	if _, err := k.doAs(ctx, "automation-owner-A", "GET", "/api/runner/session", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "signed-person-B" {
		t.Fatal("lost the actual human recipient")
	}
	res, err := k.RawAs(ctx, "automation-owner-A", "GET", "/api/runner/session", nil, map[string]string{"X-Blerg-Desktop-Viewer": "forged-person-A"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if got := <-seen; got != "signed-person-B" {
		t.Fatal("caller-controlled viewer header replaced verified proof")
	}
	res, err = k.RawAs(context.Background(), "automation-owner-A", "GET", "/api/runner/session", nil, map[string]string{"X-Blerg-Desktop-Viewer": "forged-person-A"})
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if got := <-seen; got != "none" {
		t.Fatal("unverified viewer header was forwarded")
	}
}

func TestDesktopViewerProofNeverFollowsRedirects(t *testing.T) {
	var outward bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { outward = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	k := NewBlergRunner(redirect.URL, "automation", "")
	ctx := WithDesktopViewerProof(context.Background(), "person-proof")
	if _, err := k.doAs(ctx, "automation", "GET", "/private", nil, nil); err == nil {
		t.Fatal("redirect was treated as a successful runner call")
	}
	res, err := k.RawAs(ctx, "automation", "GET", "/private", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 302 || outward {
		t.Fatal("runner redirect forwarded a person's credential")
	}
}
