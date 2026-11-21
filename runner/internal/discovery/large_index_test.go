//go:build linux || darwin

package discovery

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLargeIndexRepositoryAndGitlink(t *testing.T) {
	root := fixtureRoot(t)
	dir := fixtureRepo(t, root, "repo", true)
	var b bytes.Buffer
	b.WriteString("DIRC")
	_ = binary.Write(&b, binary.BigEndian, uint32(2))
	_ = binary.Write(&b, binary.BigEndian, uint32(20001))
	for i := 0; i < 20001; i++ {
		name := fmt.Sprintf("file-%06d", i)
		mode := uint32(0100644)
		if i == 20000 {
			name = "sub"
			mode = 0160000
		}
		entry := make([]byte, 62)
		binary.BigEndian.PutUint32(entry[24:28], mode)
		binary.BigEndian.PutUint16(entry[60:62], uint16(len(name)))
		b.Write(entry)
		b.WriteString(name)
		b.WriteByte(0)
		for (62+len(name)+1)%8 != 0 {
			b.WriteByte(0)
			name += " "
		}
	}
	sum := sha1.Sum(b.Bytes())
	b.Write(sum[:])
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	s := New(func() string { return root })
	find(t, list(t, s, "."), "repo")
	req := request("list")
	req.Path = "repo/sub"
	if reply := s.Handle(t.Context(), req); reply.OK || reply.ErrorCode != "submodule" {
		t.Fatalf("gitlink: %+v", reply)
	}
}

func TestMalformedIndexKeepsRepository(t *testing.T) {
	root := fixtureRoot(t)
	dir := fixtureRepo(t, root, "repo", true)
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(func() string { return root })
	entry := find(t, list(t, s, "."), "repo")
	if entry.Kind != "repository" {
		t.Fatalf("kind: %s", entry.Kind)
	}
}

// zeroStream supplies a large optional extension without allocating its contents.
type zeroStream struct{}

func (zeroStream) Read(p []byte) (int, error) { clear(p); return len(p), nil }
func TestLargeExtensionStreamsSHA256(t *testing.T) {
	const length = 101 << 20
	header := make([]byte, 20)
	copy(header, "DIRC")
	binary.BigEndian.PutUint32(header[4:8], 4)
	copy(header[12:], "TEST")
	binary.BigEndian.PutUint32(header[16:], length)
	h := sha256.New()
	_, _ = h.Write(header)
	_, err := io.Copy(h, io.LimitReader(zeroStream{}, length))
	if err != nil {
		t.Fatal(err)
	}
	input := io.MultiReader(bytes.NewReader(header), io.LimitReader(zeroStream{}, length), bytes.NewReader(h.Sum(nil)))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	links, err := readGitlinks(context.Background(), input, int64(len(header)+length+sha256.Size), true)
	runtime.ReadMemStats(&after)
	if err != nil || len(links) != 0 {
		t.Fatalf("parse: %v %v", links, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("allocated %d bytes", allocated)
	}
}

func TestIndexChecksumGuard(t *testing.T) {
	raw := make([]byte, 12+20)
	copy(raw, "DIRC")
	binary.BigEndian.PutUint32(raw[4:8], 2)
	if _, err := readGitlinks(context.Background(), bytes.NewReader(raw), int64(len(raw)), false); err == nil {
		t.Fatal("accepted invalid checksum")
	}
}

func TestLargeIndexBrowseAndExpand(t *testing.T) {
	root := fixtureRoot(t)
	dir := fixtureRepo(t, root, "folder/repo", true)
	var b bytes.Buffer
	b.WriteString("DIRC")
	_ = binary.Write(&b, binary.BigEndian, uint32(2))
	_ = binary.Write(&b, binary.BigEndian, uint32(20000))
	for i := 0; i < 20000; i++ {
		name := fmt.Sprintf("file-%06d", i)
		fixed := make([]byte, 62)
		binary.BigEndian.PutUint32(fixed[24:28], 0100644)
		binary.BigEndian.PutUint16(fixed[60:62], uint16(len(name)))
		b.Write(fixed)
		b.WriteString(name)
		b.WriteByte(0)
		for j := (62 + len(name) + 1) % 8; j != 0 && j < 8; j++ {
			b.WriteByte(0)
		}
	}
	sum := sha1.Sum(b.Bytes())
	b.Write(sum[:])
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("dir-%03d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	s := New(func() string { return root })
	start := time.Now()
	page := list(t, s, "folder/repo")
	elapsed := time.Since(start)
	if len(page.Entries) != 300 {
		t.Fatalf("got %d dirs", len(page.Entries))
	}
	t.Logf("list: %v", elapsed)
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("list exceeded performance budget: %v", elapsed)
	}
	folder := ref(find(t, list(t, s, "."), "folder"))
	req := request("expand")
	req.Folder = &folder
	start = time.Now()
	reply := s.Handle(t.Context(), req)
	elapsed = time.Since(start)
	t.Logf("expand: %v", elapsed)
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("expand exceeded performance budget: %v", elapsed)
	}
	if !reply.OK {
		t.Fatalf("expand: %s", reply.ErrorCode)
	}
}

func TestV4LongPrefixRemoval(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("DIRC")
	_ = binary.Write(&b, binary.BigEndian, uint32(4))
	_ = binary.Write(&b, binary.BigEndian, uint32(2))
	first := strings.Repeat("a", 128)
	for i, name := range []string{first, "sub"} {
		fixed := make([]byte, 62)
		if i == 1 {
			binary.BigEndian.PutUint32(fixed[24:28], 0160000)
		} else {
			binary.BigEndian.PutUint32(fixed[24:28], 0100644)
		}
		b.Write(fixed)
		if i == 0 {
			b.WriteByte(0)
		} else {
			b.Write([]byte{0x80, 0x00})
		}
		b.WriteString(name)
		b.WriteByte(0)
	}
	sum := sha1.Sum(b.Bytes())
	b.Write(sum[:])
	links, err := readGitlinks(t.Context(), bytes.NewReader(b.Bytes()), int64(b.Len()), false)
	if err != nil || len(links) != 1 || links[0] != "sub" {
		t.Fatalf("links %v: %v", links, err)
	}
}

func TestSparseDirectoryBlocksOnlyItsSubtree(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("DIRC")
	_ = binary.Write(&b, binary.BigEndian, uint32(3))
	_ = binary.Write(&b, binary.BigEndian, uint32(1))
	fixed := make([]byte, 62)
	binary.BigEndian.PutUint32(fixed[24:28], 0040000)
	binary.BigEndian.PutUint16(fixed[60:62], 0x4000)
	b.Write(fixed)
	b.Write([]byte{0x40, 0x00})
	b.WriteString("sparse/")
	b.WriteByte(0)
	for i := (62 + 2 + len("sparse/") + 1) % 8; i != 0 && i < 8; i++ {
		b.WriteByte(0)
	}
	b.WriteString("sdir")
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	sum := sha1.Sum(b.Bytes())
	b.Write(sum[:])
	links, err := readGitlinks(t.Context(), bytes.NewReader(b.Bytes()), int64(b.Len()), false)
	if err != nil || len(links) != 1 || links[0] != "sparse" {
		t.Fatalf("sparse links %v: %v", links, err)
	}
}

func TestIndexCacheInvalidatesOnRewrite(t *testing.T) {
	root := fixtureRoot(t)
	dir := fixtureRepo(t, root, "repo", true)
	if err := os.Mkdir(filepath.Join(dir, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	s := New(func() string { return root })
	req := request("list")
	req.Path = "repo/child"
	if reply := s.Handle(t.Context(), req); !reply.OK {
		t.Fatal(reply.ErrorCode)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if reply := s.Handle(t.Context(), req); reply.OK {
		t.Fatal("stale index cache allowed traversal")
	}
}

func BenchmarkBrowse100kIndex(b *testing.B) {
	root := b.TempDir()
	dir := filepath.Join(root, "repo")
	git := filepath.Join(dir, ".git")
	for _, p := range []string{filepath.Join(git, "objects"), filepath.Join(git, "refs")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			b.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(git, "HEAD"), []byte("ref: refs/heads/main\n"), 0600); err != nil {
		b.Fatal(err)
	}
	var raw bytes.Buffer
	raw.WriteString("DIRC")
	_ = binary.Write(&raw, binary.BigEndian, uint32(2))
	_ = binary.Write(&raw, binary.BigEndian, uint32(100000))
	for i := 0; i < 100000; i++ {
		name := fmt.Sprintf("file-%06d", i)
		entry := make([]byte, 62)
		binary.BigEndian.PutUint32(entry[24:28], 0100644)
		binary.BigEndian.PutUint16(entry[60:62], uint16(len(name)))
		raw.Write(entry)
		raw.WriteString(name)
		raw.WriteByte(0)
		for j := (62 + len(name) + 1) % 8; j != 0 && j < 8; j++ {
			raw.WriteByte(0)
		}
	}
	sum := sha1.Sum(raw.Bytes())
	raw.Write(sum[:])
	if err := os.WriteFile(filepath.Join(git, "index"), raw.Bytes(), 0600); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("dir-%03d", i)), 0700); err != nil {
			b.Fatal(err)
		}
	}
	s := New(func() string { return root })
	req := request("list")
	req.Path = "repo"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reply := s.Handle(b.Context(), req)
		if !reply.OK {
			b.Fatal(reply.ErrorCode)
		}
	}
}

func TestIndexFailureLogIsPrivateAndRateLimited(t *testing.T) {
	var output bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&output, nil)))
	defer func() { failurelog.Default = previous }()
	root := fixtureRoot(t)
	dir := fixtureRepo(t, root, "private-canary", true)
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("private-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(func() string { return root })
	for i := 0; i < 3; i++ {
		req := request("list")
		req.Path = "private-canary"
		if reply := s.Handle(t.Context(), req); !reply.OK {
			t.Fatal(reply.ErrorCode)
		}
	}
	logs := output.String()
	if strings.Contains(logs, "private-canary") || strings.Count(logs, `"cause":"malformed"`) != 1 || !strings.Contains(logs, `"suppressed":0`) {
		t.Fatalf("unsafe or unbounded logging: %s", logs)
	}
}

func TestRecentIndexIsNotCachedAcrossOperations(t *testing.T) {
	root := fixtureRoot(t)
	dir := fixtureRepo(t, root, "repo", true)
	head := fixtureGit(t, dir, "rev-parse", "HEAD")
	fixtureGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",newmoe")
	index := filepath.Join(dir, ".git", "index")
	old, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	s := New(func() string { return root })
	req := request("list")
	req.Path = "repo"
	if reply := s.Handle(t.Context(), req); !reply.OK {
		t.Fatal(reply.ErrorCode)
	}
	s.mu.Lock()
	count := len(s.indexCache)
	s.mu.Unlock()
	if count != 0 {
		t.Fatal("recent index was cached across operations")
	}
	fixtureGit(t, dir, "update-index", "--force-remove", "newmoe")
	fixtureGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",newmod")
	if err := os.Chtimes(index, old.ModTime(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != old.Size() {
		t.Fatal("fixture index sizes differ")
	}
	if err := os.Mkdir(filepath.Join(dir, "newmod"), 0700); err != nil {
		t.Fatal(err)
	}
	handle, err := s.openRoot()
	if err != nil {
		t.Fatal(err)
	}
	cacheKey := handle.tag + "/repo/.git"
	_ = handle.file.Close()
	// Simulate two rewrites whose stat identities collide on a coarse clock.
	s.mu.Lock()
	if s.indexCache == nil {
		s.indexCache = make(map[string]indexCacheEntry)
	}
	s.indexCache[cacheKey] = indexCacheEntry{fingerprint: indexFingerprint(after, false), links: []string{"newmoe"}}
	s.mu.Unlock()
	req.Path = "repo/newmod"
	if reply := s.Handle(t.Context(), req); reply.OK || reply.ErrorCode != "submodule" {
		t.Fatalf("rewritten gitlink: %s", reply.ErrorCode)
	}
	s.mu.Lock()
	_, retained := s.indexCache[cacheKey]
	s.mu.Unlock()
	if retained {
		t.Fatal("racy cache entry was retained")
	}
}

type testIndexInfo struct {
	os.FileInfo
	system   any
	modified time.Time
}

func (i testIndexInfo) Sys() any           { return i.system }
func (i testIndexInfo) ModTime() time.Time { return i.modified }

func TestIndexCacheFingerprintIncludesCtime(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "index")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	stat := reflect.ValueOf(info.Sys())
	copyStat := func(seconds int64) any {
		clone := reflect.New(stat.Elem().Type())
		clone.Elem().Set(stat.Elem())
		change := clone.Elem().FieldByName("Ctim")
		if !change.IsValid() {
			change = clone.Elem().FieldByName("Ctimespec")
		}
		if !change.IsValid() {
			t.Fatal("missing ctime")
		}
		change.FieldByName("Sec").SetInt(seconds)
		return clone.Interface()
	}
	now := time.Now()
	old := now.Add(-10 * time.Second)
	first := testIndexInfo{FileInfo: info, system: copyStat(old.Unix()), modified: old}
	second := testIndexInfo{FileInfo: info, system: copyStat(old.Unix() + 1), modified: old}
	if !indexCacheSafe(first, now) || !indexCacheSafe(second, now) {
		t.Fatal("old indexes should be cacheable")
	}
	if indexFingerprint(first, false) == indexFingerprint(second, false) {
		t.Fatal("ctime change did not invalidate cache")
	}
	recent := testIndexInfo{FileInfo: info, system: copyStat(now.Unix()), modified: old}
	if indexCacheSafe(recent, now) {
		t.Fatal("recent ctime was cacheable")
	}
	recent.modified = now
	recent.system = copyStat(old.Unix())
	if indexCacheSafe(recent, now) {
		t.Fatal("recent mtime was cacheable")
	}
}
