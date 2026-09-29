package models

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func readFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ids(ms []Model) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

// The fixture is a real catalog.json (only the "cc" surface kept). Parsing it
// must give the first_party models, main section first in catalog order, with
// the bedrock/vertex-only Opus 4.1 left out.
func TestParseCatalogRealShape(t *testing.T) {
	ms, err := ParseCatalog(readFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claude-opus-5-5", "claude-sonnet-5", "claude-fable-5-1", "claude-haiku-4-5-20251001",
		"claude-opus-5", "claude-fable-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6",
	}
	if got := ids(ms); !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v\nwant %v", got, want)
	}
	opus := ms[0]
	if opus.Name != "Opus 5.5" || opus.Section != "main" || opus.Description != "Most capable for ambitious work" {
		t.Errorf("opus 5.5 = %+v", opus)
	}
	if !reflect.DeepEqual(opus.Efforts, EffortsFor(ClaudeEngine)) || opus.DefaultEffort != "medium" {
		t.Errorf("opus 5.5 efforts = %v default %q", opus.Efforts, opus.DefaultEffort)
	}
	haiku := ms[3]
	if len(haiku.Efforts) != 0 || haiku.DefaultEffort != "" {
		t.Errorf("haiku has no effort levels, got %v / %q", haiku.Efforts, haiku.DefaultEffort)
	}
	if haiku.Efforts == nil {
		t.Errorf("efforts must serialise as [], not null")
	}
	o46 := ms[8]
	if !reflect.DeepEqual(o46.Efforts, []string{"low", "medium", "high", "max"}) {
		t.Errorf("opus 4.6 efforts = %v", o46.Efforts)
	}
	if ms[4].Section != "overflow" {
		t.Errorf("opus 5 section = %q", ms[4].Section)
	}
}

// The compiled-in fallback must be what a live fetch of today's catalog gives,
// so an air-gapped install offers exactly what a connected one does.
func TestBuiltinMatchesFixture(t *testing.T) {
	ms, err := ParseCatalog(readFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ms, Builtin()) {
		t.Fatalf("Builtin() drifted from testdata/catalog.json:\n got %+v\nwant %+v", Builtin(), ms)
	}
}

func catalogJSON(models string) []byte {
	return []byte(`{"schema_version":1,"surfaces":{"cc":{"model_selector_config":[{"id":"cc","models":[` + models + `]}]}}}`)
}

func TestParseCatalogRejectsHostileEntries(t *testing.T) {
	in := catalogJSON(`
		{"id":"claude-good-1","name":"Good","section":"main","offered_on":["first_party"],
		 "runtime":{"effort_levels":["high","bogus","low","high","max; rm -rf /"],"default_effort":"ultra"}},
		{"id":"claude-x --dangerously-skip-permissions","name":"Bad","section":"main"},
		{"id":"-claude","name":"Bad"},
		{"id":"claude-UPPER","name":"Bad"},
		{"id":"gpt-5","name":"Bad"},
		{"id":"claude-` + strings.Repeat("a", 65) + `","name":"TooLong"},
		{"id":"claude-a;b","name":"Bad"},
		{"id":"claude-bedrock-only","name":"Bedrock","offered_on":["bedrock","vertex"]},
		{"id":"claude-good-1","name":"Duplicate"},
		{"id":"claude-good-2","section":"weird","description":"` + strings.Repeat("d", 1000) + `"}
	`)
	ms, err := ParseCatalog(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(ms); !reflect.DeepEqual(got, []string{"claude-good-1", "claude-good-2"}) {
		t.Fatalf("ids = %v", got)
	}
	g1 := ms[0]
	if !reflect.DeepEqual(g1.Efforts, []string{"low", "high"}) {
		t.Errorf("efforts = %v, want the allowlisted subset in canonical order", g1.Efforts)
	}
	if g1.DefaultEffort != "high" {
		t.Errorf("an invalid default falls back to high when offered, got %q", g1.DefaultEffort)
	}
	g2 := ms[1]
	if g2.Name != "claude-good-2" {
		t.Errorf("missing name falls back to the id, got %q", g2.Name)
	}
	if g2.Section != "overflow" {
		t.Errorf("unknown section is overflow, got %q", g2.Section)
	}
	if len([]rune(g2.Description)) > maxDescriptionLen {
		t.Errorf("description not capped: %d runes", len([]rune(g2.Description)))
	}
}

func TestParseCatalogMalformed(t *testing.T) {
	for name, in := range map[string]string{
		"not json":      `{nope`,
		"no surfaces":   `{"schema_version":1}`,
		"no cc":         `{"surfaces":{"chat":{"model_selector_config":[{"models":[{"id":"claude-a"}]}]}}}`,
		"no models":     string(catalogJSON(``)),
		"only hostile":  string(catalogJSON(`{"id":"rm -rf /"}`)),
		"wrong types":   `{"surfaces":{"cc":{"model_selector_config":"x"}}}`,
		"models is obj": `{"surfaces":{"cc":{"model_selector_config":[{"models":{"id":"claude-a"}}]}}}`,
	} {
		if _, err := ParseCatalog([]byte(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestParseCatalogCapsModelCount(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxModels+20; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"claude-m-%d","name":"M"}`, i)
	}
	ms, err := ParseCatalog(catalogJSON(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != maxModels {
		t.Fatalf("len = %d, want cap %d", len(ms), maxModels)
	}
}

func TestCatalogFallsBackToBuiltinWhenNeverFetched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := NewCatalog(srv.URL)
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("want an error from a 502")
	}
	s := c.Snapshot()
	if s.Source != SourceBuiltin || !reflect.DeepEqual(s.Models, Builtin()) || s.FetchedAt != nil {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestCatalogDisabledUsesBuiltin(t *testing.T) {
	c := NewCatalog("")
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("a disabled catalog is not an error: %v", err)
	}
	if s := c.Snapshot(); s.Source != SourceBuiltin {
		t.Fatalf("source = %q", s.Source)
	}
}

func TestCatalogRefreshKeepsLastGood(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			_, _ = w.Write([]byte(`{"garbage":true}`))
			return
		}
		_, _ = w.Write(catalogJSON(`{"id":"claude-live-1","name":"Live","section":"main"}`))
	}))
	defer srv.Close()
	c := NewCatalog(srv.URL)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := c.Snapshot()
	if s.Source != SourceLive || !reflect.DeepEqual(ids(s.Models), []string{"claude-live-1"}) || s.FetchedAt == nil {
		t.Fatalf("after good fetch: %+v", s)
	}
	fail.Store(true)
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("want an error from a garbage catalog")
	}
	s2 := c.Snapshot()
	if s2.Source != SourceLive || !reflect.DeepEqual(ids(s2.Models), []string{"claude-live-1"}) || !s2.FetchedAt.Equal(*s.FetchedAt) {
		t.Fatalf("a failed refresh must keep the last good copy: %+v", s2)
	}
}

func TestCatalogRejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Valid JSON prefix, then padding past the cap.
		_, _ = w.Write(catalogJSON(`{"id":"claude-big","name":"Big"}`))
		_, _ = w.Write([]byte(strings.Repeat(" ", maxBodyBytes)))
	}))
	defer srv.Close()
	c := NewCatalog(srv.URL)
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("want an error for a body over the cap")
	}
	if s := c.Snapshot(); s.Source != SourceBuiltin {
		t.Fatalf("source = %q", s.Source)
	}
}

func TestCatalogFetchTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c := NewCatalog(srv.URL)
	c.timeout = 50 * time.Millisecond
	start := time.Now()
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("want a timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("refresh took %v", time.Since(start))
	}
}

const oneModel = `{"id":"claude-live-1","name":"Live","section":"main"}`

// The real shape: an https catalog server.
func TestCatalogFetchesOverTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(catalogJSON(oneModel))
	}))
	defer srv.Close()
	c := NewCatalog(srv.URL)
	c.client.Transport = srv.Client().Transport // trust the test cert; keep our redirect policy
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := c.Snapshot(); s.Source != SourceLive {
		t.Fatalf("source = %q", s.Source)
	}
}

// Plain http is only for loopback test servers; any real host must be https.
func TestCatalogRefusesPlainHTTP(t *testing.T) {
	for _, u := range []string{"http://example.test/catalog.json", "ftp://example.test/c", "http://192.0.2.1/c"} {
		if err := NewCatalog(u).Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("%s: err = %v, want an https refusal", u, err)
		}
	}
}

func TestCatalogRedirects(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(catalogJSON(oneModel))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			_, _ = w.Write(catalogJSON(oneModel))
		case "/elsewhere":
			http.Redirect(w, r, other.URL+"/x", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer srv.Close()
	if err := NewCatalog(srv.URL + "/same").Refresh(context.Background()); err != nil {
		t.Errorf("same-host redirect: %v", err)
	}
	if err := NewCatalog(srv.URL + "/elsewhere").Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "another host") {
		t.Errorf("cross-host redirect: err = %v", err)
	}
	if err := NewCatalog(srv.URL + "/loop").Refresh(context.Background()); err == nil {
		t.Error("redirect loop should fail")
	}
}

func TestParseCatalogStripsFormatCharacters(t *testing.T) {
	// U+202E right-to-left override and U+200B zero-width space.
	ms, err := ParseCatalog(catalogJSON(`{"id":"claude-a","name":"Op\u202eus\u200b","description":"x\u200by"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ms[0].Name != "Opus" || ms[0].Description != "xy" {
		t.Errorf("name %q description %q", ms[0].Name, ms[0].Description)
	}
}
