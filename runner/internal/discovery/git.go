package discovery

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // Git index checksum format validation, never an authority digest
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const maxIndexBytes = 1 << 20
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

// inspectRepository reads descriptors and Git metadata only. No host Git
// process parses a repository-controlled config, index, include or gitdir.
func inspectRepository(root *os.File, rootName, rootTag, rel string, info os.FileInfo) (*repository, error) {
	dotGit := path.Join(rel, ".git")
	git, err := openBeneath(root, dotGit, directoryFlags)
	commonPath := dotGit
	linked := false
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		raw, e := readSmall(root, dotGit, 4096)
		if e != nil {
			return nil, errUnavailable
		}
		line := strings.TrimSpace(string(raw))
		target, ok := strings.CutPrefix(line, "gitdir: ")
		if !ok || strings.ContainsAny(target, "\x00\r\n") {
			return nil, errUnavailable
		}
		gitPath, e := internalRelative(rootName, rel, target)
		if e != nil {
			return nil, e
		}
		// A linked worktree is registered with its own back-reference. A .git
		// file by itself is not submodule detection or repository authority.
		if path.Base(path.Dir(gitPath)) != "worktrees" {
			return nil, errUnavailable
		}
		back, e := readSmall(root, path.Join(gitPath, "gitdir"), 4096)
		if e != nil {
			return nil, errUnavailable
		}
		backRel, e := internalRelative(rootName, gitPath, strings.TrimSpace(string(back)))
		if e != nil || backRel != dotGit {
			return nil, errUnavailable
		}
		common, e := readSmall(root, path.Join(gitPath, "commondir"), 4096)
		if e != nil {
			return nil, errUnavailable
		}
		commonPath, e = internalRelative(rootName, gitPath, strings.TrimSpace(string(common)))
		if e != nil || path.Clean(path.Join(commonPath, "worktrees", path.Base(gitPath))) != gitPath {
			return nil, errUnavailable
		}
		git, e = openBeneath(root, gitPath, directoryFlags)
		if e != nil {
			return nil, errUnavailable
		}
		dotGit, linked = gitPath, true
	}
	defer func() { _ = git.Close() }()
	if !linked {
		if f, e := openBeneath(root, path.Join(dotGit, "commondir"), regularFlags); e == nil {
			_ = f.Close()
			return nil, errUnavailable
		} else if !errors.Is(e, os.ErrNotExist) {
			return nil, errUnavailable
		}
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
	index, e := readSmall(root, path.Join(dotGit, "index"), maxIndexBytes)
	var links []string
	switch {
	case errors.Is(e, os.ErrNotExist):
		links = []string{}
	case e != nil:
		return nil, errUnavailable
	default:
		links, e = gitlinks(index, sha256Objects)
		if e != nil {
			return nil, e
		}
	}
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

// gitlinks decodes v2/v3/v4 binary index entries, including uninitialized
// submodule entries. Sparse-directory indexes are unavailable rather than
// silently assuming there are no gitlinks in an unexpanded tree.
func gitlinks(raw []byte, sha256Objects bool) ([]string, error) {
	hashLen := 20
	if sha256Objects {
		hashLen = 32
	}
	if len(raw) < 12+hashLen || string(raw[:4]) != "DIRC" {
		return nil, errUnavailable
	}
	payload := raw[:len(raw)-hashLen]
	if sha256Objects {
		sum := sha256.Sum256(payload)
		if !bytes.Equal(sum[:], raw[len(payload):]) {
			return nil, errUnavailable
		}
	} else {
		sum := sha1.Sum(payload) //nolint:gosec // validate Git SHA-1 index format; selection authority uses SHA-256
		if !bytes.Equal(sum[:], raw[len(payload):]) {
			return nil, errUnavailable
		}
	}
	version := binary.BigEndian.Uint32(raw[4:8])
	count := binary.BigEndian.Uint32(raw[8:12])
	if version < 2 || version > 4 || count > 10000 {
		return nil, errUnavailable
	}
	offset := 12
	previous := ""
	out := []string{}
	for n := uint32(0); n < count; n++ {
		start := offset
		fixed := 42 + hashLen
		if offset+fixed > len(payload) {
			return nil, errUnavailable
		}
		mode := binary.BigEndian.Uint32(payload[offset+24:offset+28]) & 0o170000
		flags := binary.BigEndian.Uint16(payload[offset+fixed-2 : offset+fixed])
		offset += fixed
		if flags&0x4000 != 0 {
			if version == 2 || offset+2 > len(payload) {
				return nil, errUnavailable
			}
			offset += 2
		}
		remove := 0
		if version == 4 {
			if offset >= len(payload) {
				return nil, errUnavailable
			}
			c := payload[offset]
			offset++
			remove = int(c & 0x7f)
			for bytesRead := 0; c&0x80 != 0; bytesRead++ {
				if bytesRead >= 4 || offset >= len(payload) {
					return nil, errUnavailable
				}
				remove++
				c = payload[offset]
				offset++
				remove = (remove << 7) + int(c&0x7f)
			}
			if remove > len(previous) {
				return nil, errUnavailable
			}
		}
		end := bytes.IndexByte(payload[offset:], 0)
		if end < 0 || end > 4096 {
			return nil, errUnavailable
		}
		name := string(payload[offset : offset+end])
		offset += end + 1
		if version == 4 {
			name = previous[:len(previous)-remove] + name
		} else {
			offset = start + ((offset - start + 7) &^ 7)
		}
		if offset > len(payload) || name == "" || len(name) > 4096 || !utf8.ValidString(name) || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00") {
			return nil, errUnavailable
		}
		if mode == 0o040000 {
			return nil, errUnavailable
		}
		if mode == 0o160000 {
			out = append(out, name)
		}
		previous = name
	}
	for offset < len(payload) {
		if len(payload)-offset < 8 {
			return nil, errUnavailable
		}
		signature := payload[offset : offset+4]
		size := binary.BigEndian.Uint32(payload[offset+4 : offset+8])
		offset += 8
		// Required extensions (notably split and sparse indexes) can hide
		// gitlinks outside the decoded entries. Never infer absence from them.
		if signature[0] < 'A' || signature[0] > 'Z' || uint64(size) > uint64(len(payload)-offset) { //nolint:gosec // offset is bounded by len(payload) before this extension header
			return nil, errUnavailable
		}
		offset += int(size)
	}
	return out, nil
}
