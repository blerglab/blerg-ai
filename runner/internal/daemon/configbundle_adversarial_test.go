package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConfigBundleDoesNotDereferenceWhitelistedFileSymlink(t *testing.T) {
	home := t.TempDir()
	const marker = "fixture-private-credential-marker"
	writeBundleFixture(t, home, ".codex/auth.json", marker)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".codex", "auth.json"), filepath.Join(home, ".claude", "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildConfigBundle(home)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(bundle))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), marker) {
			t.Fatal("config bundle dereferenced a symlink into private credentials")
		}
	}
}

func TestConfigBundleRejectsLinkedParentsAndHardlinkedFiles(t *testing.T) {
	for _, scenario := range []string{"parent-inside", "parent-outside", "hardlink", "plugin-file"} {
		t.Run(scenario, func(t *testing.T) {
			home, outside := t.TempDir(), t.TempDir()
			const marker = "fixture-private-linked-config-marker"
			writeBundleFixture(t, home, ".codex/auth.json", marker)
			writeBundleFixture(t, outside, "CLAUDE.md", marker)
			switch scenario {
			case "parent-inside":
				writeBundleFixture(t, home, "private/CLAUDE.md", marker)
				if err := os.Symlink(filepath.Join(home, "private"), filepath.Join(home, ".claude")); err != nil {
					t.Fatal(err)
				}
			case "parent-outside":
				if err := os.Symlink(outside, filepath.Join(home, ".claude")); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Mkdir(filepath.Join(home, ".claude"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(home, ".codex", "auth.json"), filepath.Join(home, ".claude", "CLAUDE.md")); err != nil {
					t.Fatal(err)
				}
			case "plugin-file":
				dir := filepath.Join(home, ".claude", "plugins", "cache", "fixture", "plugin", "1.0.0")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(home, ".codex", "auth.json"), filepath.Join(dir, "secret.md")); err != nil {
					t.Fatal(err)
				}
			}
			bundle, err := BuildConfigBundle(home)
			if err != nil {
				t.Fatal(err)
			}
			gz, err := gzip.NewReader(bytes.NewReader(bundle))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gz.Close() }()
			raw, err := io.ReadAll(gz)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), marker) {
				t.Fatal("linked private content entered config bundle")
			}
		})
	}
}

func extractionBundleFixture(t *testing.T, name string, size int64) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: size}); err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("x"), 32768)
	for left := size; left > 0; {
		n := min(left, int64(len(chunk)))
		if _, err := tw.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
		left -= n
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestExtractConfigBundleRejectsLinkedDestinations(t *testing.T) {
	for _, kind := range []string{"parent", "leaf", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			home, outside := t.TempDir(), t.TempDir()
			target := filepath.Join(outside, "private")
			if err := os.WriteFile(target, []byte("fixture-private-original"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "parent" {
				if err := os.Symlink(outside, filepath.Join(home, ".claude")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(filepath.Join(home, ".claude"), 0700); err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(home, ".claude", "private")
				var err error
				if kind == "leaf" {
					err = os.Symlink(target, dest)
				} else {
					err = os.Link(target, dest)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := ExtractConfigBundle(bytes.NewReader(extractionBundleFixture(t, ".claude/private", 4)), home); err == nil {
				t.Error("linked destination accepted")
			}
			raw, err := os.ReadFile(target)
			if err != nil || string(raw) != "fixture-private-original" {
				t.Fatal("extraction overwrote linked private file")
			}
		})
	}
}

func TestExtractConfigBundleRejectsOversizedPayloadWithoutTruncating(t *testing.T) {
	home := t.TempDir()
	writeBundleFixture(t, home, ".claude/CLAUDE.md", "fixture-original")
	if err := ExtractConfigBundle(bytes.NewReader(extractionBundleFixture(t, ".claude/CLAUDE.md", bundleFileCap+1)), home); err == nil {
		t.Error("oversized bundle accepted")
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if err != nil || string(raw) != "fixture-original" {
		t.Fatal("oversized payload replaced valid configuration")
	}
}

func TestExtractConfigBundleBoundsAggregateAndEntryCount(t *testing.T) {
	for _, scenario := range []string{"aggregate", "entries"} {
		t.Run(scenario, func(t *testing.T) {
			var b bytes.Buffer
			gz := gzip.NewWriter(&b)
			tw := tar.NewWriter(gz)
			count, size := 14, int64(bundleFileCap)
			if scenario == "entries" {
				count = bundleEntryCap + 1
				size = 0
			}
			chunk := bytes.Repeat([]byte("x"), 32768)
			for i := 0; i < count; i++ {
				if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf(".claude/skills/file-%d", i), Mode: 0600, Size: size}); err != nil {
					t.Fatal(err)
				}
				for left := size; left > 0; {
					n := min(left, int64(len(chunk)))
					if _, err := tw.Write(chunk[:n]); err != nil {
						t.Fatal(err)
					}
					left -= n
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			if err := ExtractConfigBundle(bytes.NewReader(b.Bytes()), t.TempDir()); err == nil {
				t.Fatal("unbounded bundle accepted")
			}
		})
	}
}

func TestExtractConfigBundleRejectsEscapesAndReplacesRegularFiles(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", ".", strings.Repeat("deep/", 129) + "file", strings.Repeat("long", 1025)} {
		t.Run(name, func(t *testing.T) {
			if err := ExtractConfigBundle(bytes.NewReader(extractionBundleFixture(t, name, 4)), t.TempDir()); err == nil {
				t.Fatal("invalid destination accepted")
			}
		})
	}
	home := t.TempDir()
	writeBundleFixture(t, home, ".claude/CLAUDE.md", "old")
	if err := ExtractConfigBundle(bytes.NewReader(extractionBundleFixture(t, ".claude/CLAUDE.md", 4)), home); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if err != nil || string(raw) != "xxxx" {
		t.Fatal("regular configuration update failed")
	}
}

func TestConfigBundleTransferErrorsDoNotReflectCredentials(t *testing.T) {
	const token = "fixture-config-private-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, r.Header.Get("Authorization"))
	}))
	defer server.Close()
	for _, transfer := range []func(context.Context, string, string, string) error{UploadConfigBundle, DownloadConfigBundle} {
		err := transfer(context.Background(), server.URL, token, t.TempDir())
		if err == nil || strings.Contains(err.Error(), token) {
			t.Fatal("credential-bearing server diagnostic was exposed")
		}
	}
}

func TestConfigBundleTransfersRefuseRedirects(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { followed.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	for _, transfer := range []func(context.Context, string, string, string) error{UploadConfigBundle, DownloadConfigBundle} {
		if err := transfer(context.Background(), origin.URL, "fixture-config-private-token", t.TempDir()); err == nil {
			t.Fatal("redirect accepted")
		}
	}
	if followed.Load() != 0 {
		t.Fatal("credential-bearing transfer followed a redirect")
	}
}
