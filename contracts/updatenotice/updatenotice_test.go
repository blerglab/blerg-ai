package updatenotice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		running, tag string
		want         bool
	}{
		{"0.4.0", "v0.4.1", true},
		{"0.4.0", "v0.5.0", true},
		{"0.4.9", "v1.0.0", true},
		{"0.4.0", "0.4.1", true},
		{"0.9.0", "v0.10.0", true},
		{"0.4.0", "v0.4.0", false},
		{"0.4.1", "v0.4.0", false},
		{"1.0.0", "v0.99.99", false},
		{"0.4.0", "v0.5.0-rc1", false},
		{"0.4.0-rc1", "v0.5.0", false},
		{"0.4.0", "v0.5", false},
		{"0.4.0", "latest", false},
		{"0.4.0", "", false},
		{"", "v0.5.0", false},
		{"0.4.0", "v0.5.0+build", false},
		{"0.4.0", "v0.-5.0", false},
		{"0.4.0", "v99999999999.0.0", false},
	} {
		if got := Newer(c.running, c.tag); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.running, c.tag, got, c.want)
		}
	}
}

func TestValidReleaseURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://github.com/blerglab/blerg-ai/releases/tag/v0.4.1": true,
		"http://github.com/x":               false,
		"https://evil.example/github.com/":  false,
		"https://github.com.evil.example/x": false,
		"https://user@github.com/x":         false,
		"javascript:alert(1)":               false,
		"":                                  false,
	} {
		if got := ValidReleaseURL(u); got != want {
			t.Errorf("ValidReleaseURL(%q) = %v, want %v", u, got, want)
		}
	}
}

func checkerFor(srv *httptest.Server, running string) *Checker {
	return &Checker{APIBase: srv.URL, Repo: "o/r", Running: running, UserAgent: "blerg-core/test", Client: srv.Client()}
}

func serve(status int, body string, hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/repos/o/r/releases/latest" || r.Header.Get("User-Agent") != "blerg-core/test" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestCheck(t *testing.T) {
	const good = `{"tag_name":"v0.4.1","html_url":"https://github.com/o/r/releases/tag/v0.4.1"}`
	for _, c := range []struct {
		name    string
		status  int
		body    string
		wantVer string
	}{
		{"newer", 200, good, "0.4.1"},
		{"equal", 200, `{"tag_name":"v0.4.0","html_url":"https://github.com/o/r/releases/tag/v0.4.0"}`, ""},
		{"server error", 500, good, ""},
		{"not found", 404, `{}`, ""},
		{"garbage", 200, `nope`, ""},
		{"non-github url", 200, `{"tag_name":"v0.4.1","html_url":"https://evil.example/x"}`, ""},
		{"prerelease", 200, `{"tag_name":"v0.4.1","prerelease":true,"html_url":"https://github.com/o/r/releases/tag/v0.4.1"}`, ""},
		{"draft", 200, `{"tag_name":"v0.4.1","draft":true,"html_url":"https://github.com/o/r/releases/tag/v0.4.1"}`, ""},
		{"non-semver tag", 200, `{"tag_name":"nightly","html_url":"https://github.com/o/r/releases/tag/nightly"}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := serve(c.status, c.body, &hits)
			defer srv.Close()
			ch := checkerFor(srv, "0.4.0")
			ch.Check(context.Background())
			v, u := ch.Latest()
			if v != c.wantVer {
				t.Fatalf("version = %q, want %q", v, c.wantVer)
			}
			if (v == "") != (u == "") {
				t.Fatalf("version %q and url %q disagree", v, u)
			}
		})
	}
}

func TestCheckKeepsValueWhenGone(t *testing.T) {
	var hits atomic.Int32
	srv := serve(200, `{"tag_name":"v0.4.1","html_url":"https://github.com/o/r/releases/tag/v0.4.1"}`, &hits)
	ch := checkerFor(srv, "0.4.0")
	ch.Check(context.Background())
	if v, _ := ch.Latest(); v != "0.4.1" {
		t.Fatalf("want 0.4.1, got %q", v)
	}
	srv.Close()
	ch.Check(context.Background())
	if v, _ := ch.Latest(); v != "0.4.1" {
		t.Fatalf("after a failed check the last good value stays, got %q", v)
	}
}

func TestCheckTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	ch := checkerFor(srv, "0.4.0")
	ch.Client = &http.Client{Timeout: 50 * time.Millisecond}
	ch.Check(context.Background())
	if v, _ := ch.Latest(); v != "" {
		t.Fatalf("timeout reported %q", v)
	}
}

func TestOptOutAndUnknownRunningMakeNoRequest(t *testing.T) {
	var hits atomic.Int32
	srv := serve(200, `{"tag_name":"v9.9.9","html_url":"https://github.com/o/r/releases/tag/v9.9.9"}`, &hits)
	defer srv.Close()
	off := checkerFor(srv, "0.4.0")
	off.Disabled = true
	off.Check(context.Background())
	off.Start(context.Background())
	checkerFor(srv, "").Check(context.Background())
	checkerFor(srv, "dev").Check(context.Background())
	if hits.Load() != 0 {
		t.Fatalf("%d requests made, want 0", hits.Load())
	}
	if v, _ := off.Latest(); v != "" {
		t.Fatalf("disabled checker reported %q", v)
	}
	t.Setenv("BLERG_UPDATE_CHECK", "0")
	if Enabled() || !NewChecker("0.4.0", "x").Disabled || NewCoreFeed("http://core") != nil {
		t.Fatal("BLERG_UPDATE_CHECK=0 must disable everything")
	}
}

func TestEnvDefaults(t *testing.T) {
	t.Setenv("BLERG_UPDATE_CHECK", "")
	t.Setenv("BLERG_UPDATE_REPO", "")
	if !Enabled() || Repo() != DefaultRepo {
		t.Fatal("defaults wrong")
	}
	t.Setenv("BLERG_UPDATE_REPO", "a/b")
	if Repo() != "a/b" {
		t.Fatal("BLERG_UPDATE_REPO ignored")
	}
}

func TestCoreFeed(t *testing.T) {
	t.Setenv("BLERG_UPDATE_CHECK", "")
	body := `{"latest_version":"0.4.1","latest_url":"https://github.com/o/r/releases/tag/v0.4.1"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/site" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	f := NewCoreFeed(srv.URL)
	f.Latest() // starts the first refresh
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v, u := f.Latest(); v == "0.4.1" && u != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("feed never reported the release")
}

func TestCoreFeedRejectsBadData(t *testing.T) {
	t.Setenv("BLERG_UPDATE_CHECK", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"latest_version":"0.4.1","latest_url":"https://evil.example/x"}`))
	}))
	defer srv.Close()
	f := NewCoreFeed(srv.URL)
	if v, u, _ := f.fetch(); v != "" || u != "" {
		t.Fatalf("bad url accepted: %q %q", v, u)
	}
	var nilFeed *CoreFeed
	if v, _ := nilFeed.Latest(); v != "" {
		t.Fatal("nil feed reported")
	}
}

func TestValidRepo(t *testing.T) {
	for r, want := range map[string]bool{
		"blerglab/blerg-ai": true,
		"a/b.c_d-e":         true,
		"../..":             false,
		"../../users/x":     false,
		"a/..":              false,
		"./b":               false,
		"a/b?x=1":           false,
		"a/b#frag":          false,
		"a/b c":             false,
		"a/b\n":             false,
		"":                  false,
		"/b":                false,
		"a/":                false,
		"a":                 false,
		"a/b/c":             false,
		"a b/c":             false,
		"a/b%2f..":          false,
		"a/b..c":            false,
	} {
		if got := ValidRepo(r); got != want {
			t.Errorf("ValidRepo(%q) = %v, want %v", r, got, want)
		}
	}
}

func TestInvalidRepoDisablesCheck(t *testing.T) {
	t.Setenv("BLERG_UPDATE_CHECK", "")
	t.Setenv("BLERG_UPDATE_REPO", "../..")
	if !NewChecker("0.4.0", "x").Disabled {
		t.Fatal("invalid BLERG_UPDATE_REPO must disable the check")
	}
	t.Setenv("BLERG_UPDATE_REPO", "")
	if NewChecker("0.4.0", "x").Disabled {
		t.Fatal("default repo must be enabled")
	}
}

func TestFailureKeepsLastGoodValue(t *testing.T) {
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"tag_name":"v0.4.1","html_url":"https://github.com/o/r/releases/tag/v0.4.1"}`))
	}))
	defer srv.Close()
	ch := checkerFor(srv, "0.4.0")
	ch.Check(context.Background())
	status = 503
	ch.Check(context.Background())
	if v, _ := ch.Latest(); v != "0.4.1" {
		t.Fatalf("a failed check dropped the known update: %q", v)
	}
	ch.Running = "0.4.1" // now up to date: a good answer clears it
	status = 200
	ch.Check(context.Background())
	if v, _ := ch.Latest(); v != "" {
		t.Fatalf("up to date but still reporting %q", v)
	}
}

func TestOversizedAnswerIgnoredAndRedirectsNotFollowed(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/big/") {
			_, _ = w.Write([]byte(`{"tag_name":"v0.4.1","html_url":"https://github.com/o/r/x","pad":"`))
			_, _ = w.Write(make([]byte, maxBody+10))
			return
		}
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	ch := &Checker{APIBase: srv.URL, Repo: "o/r", Running: "0.4.0", UserAgent: "t", Client: newClient(5 * time.Second)}
	ch.Check(context.Background())
	if hits.Load() != 0 {
		t.Fatal("redirect was followed")
	}
	ch.APIBase, ch.Repo = srv.URL, "big"
	ch.Check(context.Background())
	if v, _ := ch.Latest(); v != "" {
		t.Fatalf("reported %q", v)
	}
}

func TestStartStopsOnCancelWithoutRequest(t *testing.T) {
	var hits atomic.Int32
	srv := serve(200, `{}`, &hits)
	defer srv.Close()
	ch := checkerFor(srv, "0.4.0")
	ch.Delay = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	ch.Start(ctx)
	cancel()
	time.Sleep(150 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("%d requests after cancel", hits.Load())
	}
	ch2 := checkerFor(srv, "0.4.0")
	ch2.Delay = 20 * time.Millisecond
	ch2.Start(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() == 0 {
		t.Fatal("Start never checked")
	}
}
