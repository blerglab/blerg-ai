package discovery

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git index checksum format validation, never an authority digest
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// One GiB bounds work on pathological indexes while allowing large monorepos.
const maxIndexBytes = 1 << 30
const maxGitFileBytes = 64 << 10

type repository struct {
	source     protocol.DiscoverySource
	commonPath string
	links      []string
	info       os.FileInfo
}

func hashIdentity(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
func infoIdentity(info os.FileInfo) string {
	dev, ino := fileIdentity(info)
	return strconv.FormatUint(dev, 10) + ":" + strconv.FormatUint(ino, 10)
}
func rootIdentity(name string, info os.FileInfo) string {
	return hashIdentity("root-v1", name, infoIdentity(info))
}
func directoryIdentity(rootTag, rel string, info os.FileInfo) string {
	return hashIdentity("directory-v1", rootTag, rel, infoIdentity(info), metadataVersion(info))
}
func readSmall(root *os.File, name string, limit int) ([]byte, error) {
	f, err := openBeneath(root, name, regularFlags)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !regularPrivateMetadata(info) || info.Size() > int64(limit) {
		return nil, errUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil, errUnavailable
	}
	return raw, nil
}
func internalRelative(rootName, base, target string) (string, error) {
	candidate := target
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootName, filepath.FromSlash(base), candidate)
	}
	rel, err := filepath.Rel(rootName, filepath.Clean(candidate))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errUnavailable
	}
	return filepath.ToSlash(rel), nil
}
func validHead(raw []byte) bool {
	value := strings.TrimSpace(string(raw))
	if strings.HasPrefix(value, "ref: refs/") {
		return !strings.ContainsAny(value, "\x00\r\n\\") && !strings.Contains(value, "..")
	}
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// resolveGitDir resolves rel's real git directory — handling a linked worktree's ".git"
// file ("gitdir: ...") indirection, with the same root-containment checks inspectRepository
// has always applied — and nothing else: no config, HEAD, index or object is read or
// opened. found is false (err nil) exactly when rel has no ".git" entry at all (not a
// repository); resolveRepositoryIdentity (baseref.go, resolve_base's own identity-only
// path) and inspectRepository (below, the general discovery path that also reads
// config/HEAD/objects/index) both call this rather than duplicating it, so the one place
// that parses a repository-controlled "gitdir:"/"commondir" file stays the one place.
// gitDir is the worktree's own git directory (where HEAD lives — per-worktree even for a
// linked worktree); commonPath is the common directory (where objects/config/refs live —
// the same for every worktree sharing one repository). They are equal for an ordinary,
// non-worktree repository.
func resolveGitDir(root *os.File, rootName, rel string) (gitDir, commonPath string, found bool, err error) {
	dotGit := path.Join(rel, ".git")
	git, err := openBeneath(root, dotGit, directoryFlags)
	commonPath = dotGit
	linked := false
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", false, nil
		}
		raw, e := readSmall(root, dotGit, 4096)
		if e != nil {
			return "", "", false, errUnavailable
		}
		line := strings.TrimSpace(string(raw))
		target, ok := strings.CutPrefix(line, "gitdir: ")
		if !ok || strings.ContainsAny(target, "\x00\r\n") {
			return "", "", false, errUnavailable
		}
		gitPath, e := internalRelative(rootName, rel, target)
		if e != nil {
			return "", "", false, e
		}
		// A linked worktree is registered with its own back-reference. A .git
		// file by itself is not submodule detection or repository authority.
		if path.Base(path.Dir(gitPath)) != "worktrees" {
			return "", "", false, errUnavailable
		}
		back, e := readSmall(root, path.Join(gitPath, "gitdir"), 4096)
		if e != nil {
			return "", "", false, errUnavailable
		}
		backRel, e := internalRelative(rootName, gitPath, strings.TrimSpace(string(back)))
		if e != nil || backRel != dotGit {
			return "", "", false, errUnavailable
		}
		common, e := readSmall(root, path.Join(gitPath, "commondir"), 4096)
		if e != nil {
			return "", "", false, errUnavailable
		}
		commonPath, e = internalRelative(rootName, gitPath, strings.TrimSpace(string(common)))
		if e != nil || path.Clean(path.Join(commonPath, "worktrees", path.Base(gitPath))) != gitPath {
			return "", "", false, errUnavailable
		}
		git, e = openBeneath(root, gitPath, directoryFlags)
		if e != nil {
			return "", "", false, errUnavailable
		}
		dotGit, linked = gitPath, true
	}
	_ = git.Close()
	if !linked {
		if f, e := openBeneath(root, path.Join(dotGit, "commondir"), regularFlags); e == nil {
			_ = f.Close()
			return "", "", false, errUnavailable
		} else if !errors.Is(e, os.ErrNotExist) {
			return "", "", false, errUnavailable
		}
	}
	return dotGit, commonPath, true, nil
}

// inspectRepository reads descriptors and Git metadata only. No host Git
// process parses a repository-controlled config, index, include or gitdir.
func inspectRepository(op *operation, root *os.File, rootName, rootTag, rel string, info os.FileInfo) (*repository, error) {
	dotGit, commonPath, found, err := resolveGitDir(root, rootName, rel)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	head, e := readSmall(root, path.Join(dotGit, "HEAD"), 4096)
	if e != nil || !validHead(head) {
		return nil, errUnavailable
	}
	common, e := openBeneath(root, commonPath, directoryFlags)
	if e != nil {
		return nil, errUnavailable
	}
	defer func() { _ = common.Close() }()
	commonInfo, e := common.Stat()
	if e != nil {
		return nil, errUnavailable
	}
	for _, name := range []string{"objects", "refs"} {
		f, e := openBeneath(root, path.Join(commonPath, name), directoryFlags)
		if e != nil {
			return nil, errUnavailable
		}
		_ = f.Close()
	}
	config, e := readSmall(root, path.Join(commonPath, "config"), maxGitFileBytes)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, errUnavailable
	}
	origin, sha256Objects := parseConfig(config)
	links := op.gitlinks(root, rootTag+"/"+dotGit, path.Join(dotGit, "index"), sha256Objects)

	commonID := hashIdentity("common-v1", infoIdentity(commonInfo))
	sourceDirectory, e := openBeneath(root, rel, directoryFlags)
	if e != nil {
		return nil, errStale
	}
	sourceInfo, e := sourceDirectory.Stat()
	if e != nil || !os.SameFile(info, sourceInfo) {
		_ = sourceDirectory.Close()
		return nil, errStale
	}
	sourceVersion := instanceIdentity(sourceDirectory, sourceInfo)
	_ = sourceDirectory.Close()
	sourceID := hashIdentity("source-v1", rootTag, rel, infoIdentity(info), sourceVersion, commonID, instanceIdentity(common, commonInfo))
	name := path.Base(rel)
	if rel == "." {
		name = filepath.Base(rootName)
	}
	source := protocol.DiscoverySource{ID: sourceID[:24], Name: name, Path: rel, SourceIdentity: sourceID, CommonDirectoryIdentity: commonID}
	if ref, ok := gitprovider.Default.ParseRemoteURL(origin); ok {
		source.Origin = &protocol.RepoOrigin{Provider: ref.Provider, FullName: ref.FullName()}
	}
	return &repository{source: source, commonPath: commonPath, links: links, info: info}, nil
}

// Includes and executable configuration are never interpreted. A remote not
// explicitly present in this local config is unknown, rather than guessed.
func parseConfig(raw []byte) (origin string, sha256Objects bool) {
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if section == `remote "origin"` && key == "url" && !strings.ContainsAny(value, "\x00\r\n") {
			origin = value
		}
		if section == "extensions" && key == "objectformat" && value == "sha256" {
			sha256Objects = true
		}
	}
	return origin, sha256Objects
}

// readGitlinks hashes and parses one forward pass; only the previous path and
// gitlinks are retained. The checksum and every extension are validated.
func readGitlinks(ctx context.Context, file io.Reader, size int64, sha256Objects bool) ([]string, error) {
	hashLen := 20
	h := sha1.New() //nolint:gosec // Git index checksum
	if sha256Objects {
		hashLen = 32
		h = sha256.New()
	}
	if size < int64(12+hashLen) || size > maxIndexBytes {
		return nil, errUnavailable
	}
	payload := &io.LimitedReader{R: file, N: size - int64(hashLen)}
	r := bufio.NewReaderSize(io.TeeReader(payload, h), 32*1024)
	read := func(b []byte) error {
		if ctx.Err() != nil {
			return errUnavailable
		}
		_, err := io.ReadFull(r, b)
		return err
	}
	header := make([]byte, 12)
	if read(header) != nil || string(header[:4]) != "DIRC" {
		return nil, errUnavailable
	}
	version := binary.BigEndian.Uint32(header[4:8])
	count := binary.BigEndian.Uint32(header[8:12])
	if version < 2 || version > 4 || int64(count) > size/64 {
		return nil, errUnavailable
	}
	previous := ""
	out := []string{}
	for n := uint32(0); n < count; n++ {
		fixed := make([]byte, 42+hashLen)
		if read(fixed) != nil {
			return nil, errUnavailable
		}
		mode := binary.BigEndian.Uint32(fixed[24:28]) & 0170000
		flags := binary.BigEndian.Uint16(fixed[len(fixed)-2:])
		used := len(fixed)
		var extended uint16
		if flags&0x4000 != 0 {
			var ext [2]byte
			if version == 2 || read(ext[:]) != nil {
				return nil, errUnavailable
			}
			extended = binary.BigEndian.Uint16(ext[:])
			used += 2
		}
		remove := 0
		if version == 4 {
			var one [1]byte
			if read(one[:]) != nil {
				return nil, errUnavailable
			}
			c := one[0]
			remove = int(c & 127)
			for k := 0; c&128 != 0; k++ {
				if k >= 4 || read(one[:]) != nil {
					return nil, errUnavailable
				}
				c = one[0]
				remove = ((remove + 1) << 7) + int(c&127)
			}
			if remove > len(previous) {
				return nil, errUnavailable
			}
		}
		nameBytes, err := r.ReadSlice(0)
		if err != nil || len(nameBytes) > 4097 || ctx.Err() != nil {
			return nil, errUnavailable
		}
		used += len(nameBytes)
		name := string(nameBytes[:len(nameBytes)-1])
		if version == 4 {
			name = previous[:len(previous)-remove] + name
		} else {
			padding := (8 - used%8) % 8
			var pad [7]byte
			if read(pad[:padding]) != nil {
				return nil, errUnavailable
			}
		}
		sparse := mode == 0040000 && extended&0x4000 != 0 && strings.HasSuffix(name, "/")
		validatedName := name
		if sparse {
			validatedName = strings.TrimSuffix(name, "/")
		}
		if validatedName == "" || len(name) > 4096 || !utf8.ValidString(name) || path.IsAbs(validatedName) || path.Clean(validatedName) != validatedName || strings.HasPrefix(validatedName, "../") || strings.ContainsAny(name, "\\\x00") || (mode == 0040000 && !sparse) {
			return nil, errUnavailable
		}
		// Sparse trees can hide gitlinks. Block their subtree while allowing
		// discovery elsewhere; the unexpanded tree cannot prove its contents.
		if mode == 0160000 || sparse {
			out = append(out, validatedName)
		}
		previous = name
	}
	// Buffered bytes are part of the limited payload; track both remaining sources.
	for int64(r.Buffered())+payload.N > 0 {
		if int64(r.Buffered())+payload.N < 8 {
			return nil, errUnavailable
		}
		var ext [8]byte
		if read(ext[:]) != nil {
			return nil, errUnavailable
		}
		length := int64(binary.BigEndian.Uint32(ext[4:]))
		if ((ext[0] < 'A' || ext[0] > 'Z') && string(ext[:4]) != "sdir") || length > int64(r.Buffered())+payload.N || (string(ext[:4]) == "sdir" && length != 0) {
			return nil, errUnavailable
		}
		var buf [32768]byte
		for length > 0 {
			n := int64(len(buf))
			if n > length {
				n = length
			}
			if read(buf[:n]) != nil {
				return nil, errUnavailable
			}
			length -= n
		}
	}
	checksum := make([]byte, hashLen)
	if _, err := io.ReadFull(file, checksum); err != nil || !bytes.Equal(h.Sum(nil), checksum) {
		return nil, errUnavailable
	}
	return out, nil
}

type indexCacheEntry struct {
	fingerprint string
	links       []string
}

// gitlinks reuses only results from a private, unchanged index; failures are not
// retained across operations, so an inaccessible index can recover immediately.
func (op *operation) gitlinks(root *os.File, key, name string, sha256Objects bool) []string {
	if op.indexes == nil {
		op.indexes = make(map[string][]string)
	}
	if links, ok := op.indexes[key]; ok {
		return links
	}
	var links []string
	cause := "unreadable"
	f, err := openBeneath(root, name, regularFlags)
	if errors.Is(err, os.ErrNotExist) {
		links = []string{}
		err = nil
	} else if err == nil {
		info, statErr := f.Stat()
		switch {
		case statErr != nil || !regularPrivateMetadata(info):
			err = errUnavailable
		case info.Size() > maxIndexBytes:
			cause = "too_large"
			err = errUnavailable
		default:
			started := time.Now()
			stable := indexCacheSafe(info, started)
			fingerprint := indexFingerprint(info, sha256Objects)
			var cached indexCacheEntry
			var ok bool
			if op.service != nil {
				op.service.mu.Lock()
				if !stable {
					delete(op.service.indexCache, key)
				}
				cached, ok = op.service.indexCache[key]
				op.service.mu.Unlock()
			}
			if stable && ok && cached.fingerprint == fingerprint {
				links = cached.links
			} else {
				source := &indexReader{reader: f}
				links, err = readGitlinks(op.ctx, source, info.Size(), sha256Objects)
				if err != nil {
					cause = "malformed"
					if source.failed {
						cause = "unreadable"
					}
					if op.ctx.Err() != nil {
						cause = "timed_out"
					}
				} else {
					after, e := f.Stat()
					if e != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || metadataVersion(info) != metadataVersion(after) {
						err = errUnavailable
						cause = "changed"
					} else if stable && op.service != nil {
						op.service.mu.Lock()
						if op.service.indexCache == nil || len(op.service.indexCache) >= 128 {
							op.service.indexCache = make(map[string]indexCacheEntry)
						}
						op.service.indexCache[key] = indexCacheEntry{fingerprint, links}
						op.service.mu.Unlock()
					}
				}
			}
		}
		_ = f.Close()
	}
	if err != nil {
		failurelog.Default.Event("discovery_index:"+cause, slog.LevelWarn, "component", "discovery", "operation", "read_index", "cause", cause)
		links = nil
	}
	op.indexes[key] = links
	return links
}

// indexReader preserves only whether an underlying I/O error occurred; raw
// errors can contain private paths and must not reach diagnostics.
type indexReader struct {
	reader io.Reader
	failed bool
}

func (r *indexReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		r.failed = true
	}
	return n, err
}

// indexCacheAge is how old both index timestamps must be before a result is
// cached. FAT keeps 2 s mtimes and network mounts can lag the client clock, so
// the margin is wider than git's own racy-git window; per-request reuse keeps
// browsing fast either way.
const indexCacheAge = 5 * time.Second

// Recently changed indexes can retain identical stat identities on filesystems
// with coarse timestamps. Only cache after both timestamps have aged indexCacheAge.
func indexCacheSafe(info os.FileInfo, started time.Time) bool {
	changed, ok := indexChangeTime(info)
	cutoff := started.Add(-indexCacheAge)
	return ok && info.ModTime().Before(cutoff) && changed.Before(cutoff)
}
func indexFingerprint(info os.FileInfo, sha256Objects bool) string {
	return infoIdentity(info) + ":" + metadataVersion(info) + ":" + strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + ":" + strconv.FormatBool(sha256Objects)
}
