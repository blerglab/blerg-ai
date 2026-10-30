// Package discovery implements P1's bounded, daemon-private source selection.
// It does not prepare, clone, run or settle a workspace.
package discovery

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

var (
	errUnavailable = errors.New("unavailable")
	errStale       = errors.New("stale")
	errInvalid     = errors.New("invalid_selection")
	errTooMany     = errors.New("too_many_members")
	errOverlap     = errors.New("overlapping_repositories")
	errSubmodule   = errors.New("submodule")
	errBudget      = errors.New("discovery_limit")
)

const (
	maxEntries    = 10000
	pageEntries   = 500
	maxReply      = 1 << 20
	maxDepth      = 32
	maxRemembered = 128
)

type selectionRecord struct {
	resolved       protocol.DiscoveryResolved
	rootName       string
	refs           []protocol.SelectionRef
	runtime        string
	anchorPath     string
	anchorInstance string
	physicalFolder bool
}
type boundRecord struct {
	selectionRecord
	manifest protocol.WorkspaceManifest
}

// Service owns private cursor authentication and bounded source/launch records.
// Root is the one configured live root, never a caller-provided absolute path.
type Service struct {
	root           func() string
	namespace      string
	cursorCipher   cipher.AEAD
	key            [32]byte
	mu             sync.Mutex
	selections     map[string]selectionRecord
	selectionOrder []string
	bound          map[string]boundRecord
	boundOrder     []string
}

func New(root func() string) *Service {
	s := &Service{root: root, selections: map[string]selectionRecord{}, bound: map[string]boundRecord{}}
	if _, err := rand.Read(s.key[:]); err != nil {
		panic("initialize discovery cursor authentication")
	}
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		panic("initialize discovery cursor cipher")
	}
	s.cursorCipher, err = cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		panic("initialize discovery cursor cipher")
	}
	return s
}

type operation struct {
	ctx     context.Context
	visited int
	seen    map[string]bool
}

func (o *operation) visit() error {
	o.visited++
	if o.visited > maxEntries || o.ctx.Err() != nil {
		return errBudget
	}
	return nil
}
func (o *operation) visitPath(name string) error {
	if o.ctx.Err() != nil {
		return errBudget
	}
	if o.seen == nil {
		o.seen = map[string]bool{}
	}
	if o.seen[name] {
		return nil
	}
	o.seen[name] = true
	return o.visit()
}
func cleanPath(name string) (string, error) {
	if name == "" {
		name = "."
	}
	if len(name) > 4096 || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00") || !utf8.ValidString(name) {
		return "", errInvalid
	}
	if name != "." {
		for _, part := range strings.Split(name, "/") {
			if hiddenName(part) {
				return "", errInvalid
			}
		}
	}
	if strings.Count(name, "/")+1 > maxDepth {
		return "", errBudget
	}
	return name, nil
}
func hiddenName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || name == "node_modules" || name == "secrets" || name == "credentials" || !utf8.ValidString(name) {
		return true
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

type rootHandle struct {
	file      *os.File
	name, tag string
	info      os.FileInfo
}

func (s *Service) openRoot() (*rootHandle, error) {
	name, err := filepath.Abs(s.root())
	if err != nil {
		return nil, errUnavailable
	}
	f, err := openConfiguredRoot(name)
	if err != nil {
		return nil, errUnavailable
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, errUnavailable
	}
	return &rootHandle{file: f, name: name, tag: hashIdentity(s.namespace, rootIdentity(name, info), instanceIdentity(f, info)), info: info}, nil
}
func (s *Service) rootUnchanged(root *rootHandle) bool {
	current, err := filepath.Abs(s.root())
	if err != nil || current != root.name {
		return false
	}
	next, err := openConfiguredRoot(root.name)
	if err != nil {
		return false
	}
	defer func() { _ = next.Close() }()
	info, err := next.Stat()
	return err == nil && os.SameFile(root.info, info)
}

// safeDirectory validates every repository ancestor before opening the target:
// direct path queries cannot bypass a gitlink hidden from a parent listing.
func safeDirectory(root *rootHandle, rel string, op *operation) (*os.File, *repository, error) {
	if _, err := cleanPath(rel); err != nil {
		return nil, nil, err
	}
	prefixes := []string{"."}
	if rel != "." {
		parts := strings.Split(rel, "/")
		for i := range parts {
			prefixes = append(prefixes, strings.Join(parts[:i+1], "/"))
		}
	}
	var current *os.File
	var target *repository
	for _, prefix := range prefixes {
		if err := op.visitPath(prefix); err != nil {
			if current != nil {
				_ = current.Close()
			}
			return nil, nil, err
		}
		f, err := openBeneath(root.file, prefix, directoryFlags)
		if err != nil {
			if current != nil {
				_ = current.Close()
			}
			return nil, nil, errUnavailable
		}
		if current != nil {
			_ = current.Close()
		}
		current = f
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, nil, errUnavailable
		}
		repo, err := inspectRepository(root.file, root.name, root.tag, prefix, info)
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		if repo != nil && prefix != rel {
			rest := rel
			if prefix != "." {
				rest = strings.TrimPrefix(rel, prefix+"/")
			}
			for _, link := range repo.links {
				if rest == link || strings.HasPrefix(rest, link+"/") {
					_ = f.Close()
					return nil, nil, errSubmodule
				}
			}
		}
		target = repo
	}
	return current, target, nil
}
func entryFor(root *rootHandle, rel string, info os.FileInfo, repo *repository) protocol.DiscoveryEntry {
	name := path.Base(rel)
	if rel == "." {
		name = filepath.Base(root.name)
	}
	out := protocol.DiscoveryEntry{Name: name, Path: rel, Kind: "directory", SourceIdentity: directoryIdentity(root.tag, rel, info)}
	if repo != nil {
		out.Kind = "repository"
		out.Origin = repo.source.Origin
		out.SourceIdentity = repo.source.SourceIdentity
	}
	return out
}
func dirStillSame(root *rootHandle, rel string, info os.FileInfo) bool {
	f, err := openBeneath(root.file, rel, directoryFlags)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	return err == nil && os.SameFile(info, after)
}
func children(root *rootHandle, rel string, op *operation) (protocol.DiscoveryEntry, []protocol.DiscoveryEntry, bool, error) {
	f, repo, err := safeDirectory(root, rel, op)
	if err != nil {
		return protocol.DiscoveryEntry{}, nil, false, err
	}
	defer func() { _ = f.Close() }()
	before, err := f.Stat()
	if err != nil {
		return protocol.DiscoveryEntry{}, nil, false, errUnavailable
	}
	entries, err := f.ReadDir(maxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return protocol.DiscoveryEntry{}, nil, false, errUnavailable
	}
	truncated := len(entries) > maxEntries
	if truncated {
		entries = entries[:maxEntries]
	}
	visible := []protocol.DiscoveryEntry{}
	for _, entry := range entries {
		if err := op.visitPath(path.Join(rel, entry.Name())); err != nil {
			return protocol.DiscoveryEntry{}, nil, false, err
		}
		if hiddenName(entry.Name()) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		child := path.Join(rel, entry.Name())
		dir, r, err := safeDirectory(root, child, op)
		if err != nil {
			if errors.Is(err, errBudget) {
				return protocol.DiscoveryEntry{}, nil, false, err
			}
			continue // links, mounts, submodules and unverifiable metadata have no row
		}
		info, e := dir.Stat()
		_ = dir.Close()
		if e != nil {
			continue
		}
		visible = append(visible, entryFor(root, child, info, r))
	}
	after, err := f.Stat()
	if err != nil || !before.ModTime().Equal(after.ModTime()) || !dirStillSame(root, rel, before) {
		return protocol.DiscoveryEntry{}, nil, false, errStale
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].Path < visible[j].Path })
	return entryFor(root, rel, before, repo), visible, truncated, nil
}

type cursor struct {
	Root, Path, Snapshot string
	Offset               int
	Expires              int64
}

func (s *Service) cursorFor(value cursor) string {
	raw, _ := json.Marshal(value)
	sealed := s.cursorCipher.Seal(nil, nil, raw, nil)
	return base64.RawURLEncoding.EncodeToString(sealed)
}
func (s *Service) readCursor(raw string) (cursor, error) {
	var value cursor
	if len(raw) > 16384 {
		return value, errStale
	}
	sealed, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return value, errStale
	}
	decoded, err := s.cursorCipher.Open(nil, nil, sealed, nil)
	if err != nil || json.Unmarshal(decoded, &value) != nil || value.Expires < time.Now().Unix() || value.Offset < 0 || value.Offset > maxEntries {
		return value, errStale
	}
	return value, nil
}
func (s *Service) list(root *rootHandle, req protocol.DiscoverWorkspace, op *operation) (*protocol.DiscoveryPage, error) {
	rel, err := cleanPath(req.Path)
	if err != nil {
		return nil, err
	}
	dir, repo, err := safeDirectory(root, rel, op)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	before, err := dir.Stat()
	if err != nil {
		return nil, errUnavailable
	}
	rawEntries, err := dir.ReadDir(maxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errUnavailable
	}
	truncated := len(rawEntries) > maxEntries
	if truncated {
		rawEntries = rawEntries[:maxEntries]
	}
	names := []string{}
	for _, entry := range rawEntries {
		if hiddenName(entry.Name()) {
			continue
		}
		if err := op.visitPath(path.Join(rel, entry.Name())); err != nil {
			return nil, err
		}
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	rootEntry := entryFor(root, rel, before, repo)
	encodedNames, _ := json.Marshal(names)
	snapshot := hashIdentity(root.tag, rel, rootEntry.SourceIdentity, strconv.FormatInt(before.ModTime().UnixNano(), 10), string(encodedNames))
	offset := 0
	if req.Cursor != "" {
		cur, e := s.readCursor(req.Cursor)
		if e != nil || cur.Root != root.tag || cur.Path != rel || cur.Snapshot != snapshot || cur.Offset > len(names) {
			return nil, errStale
		}
		offset = cur.Offset
	}
	page := &protocol.DiscoveryPage{RootIdentity: root.tag, Root: rootEntry, Entries: []protocol.DiscoveryEntry{}, Truncated: truncated}
	bytesUsed := 32 << 10
	next := offset
	for next < len(names) && len(page.Entries) < pageEntries {
		child := path.Join(rel, names[next])
		f, r, e := safeDirectory(root, child, op)
		if e != nil {
			if errors.Is(e, errBudget) {
				return nil, e
			}
			next++
			continue
		}
		info, e := f.Stat()
		_ = f.Close()
		if e != nil {
			next++
			continue
		}
		entry := entryFor(root, child, info, r)
		encoded, _ := json.Marshal(entry)
		if bytesUsed+len(encoded) > maxReply {
			break
		}
		page.Entries = append(page.Entries, entry)
		bytesUsed += len(encoded)
		next++
	}
	after, err := dir.Stat()
	if err != nil || !before.ModTime().Equal(after.ModTime()) || !dirStillSame(root, rel, before) {
		return nil, errStale
	}
	if next < len(names) {
		page.Truncated = true
		page.NextCursor = s.cursorFor(cursor{Root: root.tag, Path: rel, Snapshot: snapshot, Offset: next, Expires: time.Now().Add(2 * time.Minute).Unix()})
	}
	return page, nil
}

func collectRepositories(root *rootHandle, rel string, op *operation, out *[]protocol.DiscoverySource) error {
	dir, repo, err := safeDirectory(root, rel, op)
	if err != nil {
		return err
	}
	_ = dir.Close()
	if repo != nil {
		*out = append(*out, repo.source)
		if len(*out) > protocol.MaxWorkspaceMembers {
			return errTooMany
		}
	}
	_, entries, truncated, err := children(root, rel, op)
	if err != nil {
		return err
	}
	if truncated {
		return errBudget
	}
	for _, entry := range entries {
		if err := collectRepositories(root, entry.Path, op, out); err != nil {
			return err
		}
	}
	return nil
}
func validateSources(sources []protocol.DiscoverySource) error {
	if len(sources) > protocol.MaxWorkspaceMembers {
		return errTooMany
	}
	ids, commons := map[string]bool{}, map[string]bool{}
	for i, source := range sources {
		if ids[source.SourceIdentity] || commons[source.CommonDirectoryIdentity] {
			return errOverlap
		}
		ids[source.SourceIdentity], commons[source.CommonDirectoryIdentity] = true, true
		for _, previous := range sources[:i] {
			if source.Path == previous.Path || source.Path == "." || previous.Path == "." || strings.HasPrefix(source.Path, previous.Path+"/") || strings.HasPrefix(previous.Path, source.Path+"/") {
				return errOverlap
			}
		}
	}
	return nil
}
func membershipSnapshot(sources []protocol.DiscoverySource) string {
	parts := make([]string, 0, len(sources))
	for _, source := range sources {
		parts = append(parts, source.SourceIdentity)
	}
	sort.Strings(parts)
	return hashIdentity(append([]string{"membership-v1"}, parts...)...)
}
func (s *Service) resolve(root *rootHandle, req protocol.DiscoverWorkspace, op *operation) (selectionRecord, error) {
	if !req.Layout.Valid() {
		return selectionRecord{}, errInvalid
	}
	if req.RootIdentity != "" && req.RootIdentity != root.tag {
		return selectionRecord{}, errStale
	}
	if len(req.Members) > protocol.MaxWorkspaceMembers {
		return selectionRecord{}, errTooMany
	}
	resolved := protocol.DiscoveryResolved{Layout: req.Layout, RootIdentity: root.tag, Members: []protocol.DiscoverySource{}}
	refs := append([]protocol.SelectionRef(nil), req.Members...)
	anchor := ""
	if req.Folder != nil {
		rel, err := cleanPath(req.Folder.Path)
		if err != nil {
			return selectionRecord{}, err
		}
		dir, repo, err := safeDirectory(root, rel, op)
		if err != nil {
			return selectionRecord{}, err
		}
		info, err := dir.Stat()
		_ = dir.Close()
		if err != nil {
			return selectionRecord{}, errUnavailable
		}
		entry := entryFor(root, rel, info, repo)
		if entry.SourceIdentity != req.Folder.SourceIdentity {
			return selectionRecord{}, errStale
		}
		snapshot := ""
		explicit := req.Operation != "expand" && (req.MembershipSnapshot == "" || strings.HasPrefix(req.MembershipSnapshot, "explicit:"))
		if explicit {
			if len(refs) == 0 {
				return selectionRecord{}, errInvalid
			}
			values := make([]string, 0, len(refs))
			for _, ref := range refs {
				values = append(values, ref.Path+":"+ref.SourceIdentity)
			}
			sort.Strings(values)
			snapshot = "explicit:" + hashIdentity(values...)
			if req.MembershipSnapshot != "" && req.MembershipSnapshot != snapshot {
				return selectionRecord{}, errStale
			}
		} else {
			all := []protocol.DiscoverySource{}
			if err := collectRepositories(root, rel, op, &all); err != nil {
				return selectionRecord{}, err
			}
			snapshot = membershipSnapshot(all)
			if req.Operation == "expand" {
				refs = []protocol.SelectionRef{}
				for _, source := range all {
					refs = append(refs, protocol.SelectionRef{Path: source.Path, SourceIdentity: source.SourceIdentity})
				}
			} else if snapshot != req.MembershipSnapshot {
				return selectionRecord{}, errStale
			}
		}

		anchor = rel
		resolved.Folder = &protocol.SelectionRef{Path: rel, SourceIdentity: entry.SourceIdentity}
		resolved.MembershipSnapshot = snapshot
	}
	for _, ref := range refs {
		rel, err := cleanPath(ref.Path)
		if err != nil {
			return selectionRecord{}, err
		}
		if anchor != "" && anchor != "." && rel != anchor && !strings.HasPrefix(rel, anchor+"/") {
			return selectionRecord{}, errInvalid
		}
		dir, repo, err := safeDirectory(root, rel, op)
		if err != nil {
			return selectionRecord{}, err
		}
		_ = dir.Close()
		if repo == nil || repo.source.SourceIdentity != ref.SourceIdentity {
			return selectionRecord{}, errStale
		}
		resolved.Members = append(resolved.Members, repo.source)
	}
	if err := validateSources(resolved.Members); err != nil {
		return selectionRecord{}, err
	}
	if len(resolved.Members) == 0 && (resolved.Folder == nil || req.Runtime != "host") {
		return selectionRecord{}, errInvalid
	}
	if anchor == "" {
		anchor = resolved.Members[0].Path
	}
	dir, _, err := safeDirectory(root, anchor, op)
	if err != nil {
		return selectionRecord{}, err
	}
	info, err := dir.Stat()
	if err != nil {
		_ = dir.Close()
		return selectionRecord{}, errUnavailable
	}
	anchorInstance := instanceIdentity(dir, info)
	_ = dir.Close()
	resolved.Root.Device, resolved.Root.Inode = fileIdentity(info)
	if len(resolved.Members) == 0 {
		resolved.Root.Identity = root.tag
	}
	raw, _ := json.Marshal(resolved)
	resolved.SelectionIdentity = hashIdentity("selection-v1", string(raw), req.Runtime)
	return selectionRecord{resolved: resolved, refs: refs, rootName: root.name, runtime: req.Runtime, anchorPath: anchor, anchorInstance: anchorInstance, physicalFolder: resolved.Folder != nil}, nil
}
func (s *Service) remember(record selectionRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := record.resolved.SelectionIdentity
	if _, exists := s.selections[id]; !exists {
		s.selectionOrder = append(s.selectionOrder, id)
	}
	record.resolved = copyResolved(record.resolved)
	record.refs = append([]protocol.SelectionRef(nil), record.refs...)
	s.selections[id] = record
	for len(s.selectionOrder) > maxRemembered {
		delete(s.selections, s.selectionOrder[0])
		s.selectionOrder = s.selectionOrder[1:]
	}
}
func (s *Service) bind(root *rootHandle, req protocol.DiscoverWorkspace, op *operation) error {
	if req.Manifest == nil || req.Manifest.Validate() != nil || req.Manifest.Digest != workspaceselection.Digest(*req.Manifest) {
		return errInvalid
	}
	s.mu.Lock()
	record, ok := s.selections[req.SelectionIdentity]
	s.mu.Unlock()
	if !ok {
		return errStale
	}
	current, err := s.resolve(root, protocol.DiscoverWorkspace{Operation: "resolve", Layout: record.resolved.Layout, RootIdentity: record.resolved.RootIdentity, Members: record.refs, Folder: record.resolved.Folder, MembershipSnapshot: record.resolved.MembershipSnapshot, Runtime: record.runtime}, op)
	if err != nil || current.resolved.SelectionIdentity != record.resolved.SelectionIdentity {
		return errStale
	}
	m := *req.Manifest
	m.Members = append([]protocol.WorkspaceMember(nil), m.Members...)
	if m.Layout != current.resolved.Layout || m.Root != current.resolved.Root || m.Runtime != record.runtime || len(m.Members) != len(current.resolved.Members) {
		return errInvalid
	}
	expectedKind := "repo"
	if current.resolved.Folder != nil || len(current.resolved.Members) != 1 {
		expectedKind = "folder"
	}
	if m.Kind != expectedKind {
		return errInvalid
	}
	paths, _ := workspaceselection.MemberPaths(current.resolved)
	for i, member := range m.Members {
		source := current.resolved.Members[i]
		if member.Path != paths[i] || member.Destination != source.SourceIdentity {
			return errInvalid
		}
		if member.Provider != "" && (source.Origin == nil || member.Provider != source.Origin.Provider) {
			return errInvalid
		}
		if member.Project != "" && (source.Origin == nil || member.Project != source.Origin.FullName) {
			return errInvalid
		}

		if member.ID != source.ID || member.Name != source.Name || member.SourceIdentity != source.SourceIdentity || member.CommonDirectoryIdentity != source.CommonDirectoryIdentity {
			return errInvalid
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, exists := s.bound[m.LaunchGeneration]; exists {
		if old.manifest.Digest != m.Digest {
			return errStale
		}
	} else {
		if len(s.bound) >= maxRemembered {
			return errBudget
		}
		s.boundOrder = append(s.boundOrder, m.LaunchGeneration)
	}

	s.bound[m.LaunchGeneration] = boundRecord{selectionRecord: current, manifest: m}

	return nil
}

// Handle returns only fixed error codes. Kernel errors never expose private
// absolute paths, metadata contents or failed entry names to the browser.
func (s *Service) Handle(ctx context.Context, req protocol.DiscoverWorkspace) protocol.WorkspaceDiscoveryResult {
	result := protocol.WorkspaceDiscoveryResult{Type: "workspace_discovery_result", RequestID: req.RequestID}
	if req.RequestID == "" || len(req.RequestID) > 100 {
		result.ErrorCode = errInvalid.Error()
		return result
	}
	if req.Operation == "release" {
		if req.Manifest == nil {
			result.ErrorCode = errInvalid.Error()
			return result
		}
		s.Release(*req.Manifest)
		result.OK = true
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	op := &operation{ctx: ctx}
	root, err := s.openRoot()
	if err != nil {
		result.ErrorCode = errUnavailable.Error()
		return result
	}
	defer func() { _ = root.file.Close() }()
	switch req.Operation {
	case "list":
		result.Page, err = s.list(root, req, op)
	case "expand", "resolve":
		if req.Runtime != "host" && req.Runtime != "sandbox" {
			err = errInvalid
			break
		}
		if req.Operation == "expand" && req.Folder == nil {
			err = errInvalid
			break
		}
		var record selectionRecord
		record, err = s.resolve(root, req, op)
		if err == nil {
			s.remember(record)
			result.Resolved = &record.resolved
		}
	case "bind":
		err = s.bind(root, req, op)
	case "bind_child":
		err = s.bindChild(root, req, op)
	default:
		err = errInvalid
	}
	if err == nil && (!s.rootUnchanged(root) || ctx.Err() != nil) {
		err = errStale
	}
	if err != nil {
		result.Page, result.Resolved = nil, nil
		switch {
		case errors.Is(err, errStale), errors.Is(err, errInvalid), errors.Is(err, errTooMany), errors.Is(err, errOverlap), errors.Is(err, errSubmodule), errors.Is(err, errBudget):
			result.ErrorCode = err.Error()
		default:
			result.ErrorCode = errUnavailable.Error()
		}
		return result
	}
	result.OK = true
	raw, _ := json.Marshal(result)
	if len(raw) > maxReply {
		result.OK = false
		result.Page, result.Resolved = nil, nil
		result.ErrorCode = errBudget.Error()
	}
	return result
}

func copyResolved(in protocol.DiscoveryResolved) protocol.DiscoveryResolved {
	out := in
	out.Members = append([]protocol.DiscoverySource(nil), in.Members...)
	if in.Folder != nil {
		folder := *in.Folder
		out.Folder = &folder
	}
	for i := range out.Members {
		if in.Members[i].Origin != nil {
			origin := *in.Members[i].Origin
			out.Members[i].Origin = &origin
		}
	}
	return out
}

// SafeRelativePath is shared with the server's control-plane DTO validator.
func SafeRelativePath(name string) bool { _, err := cleanPath(name); return err == nil }

// Inventory is the bounded legacy hello/heartbeat hint. The launch picker uses
// pages and explicit references instead; this hint never chooses launch members.
func (s *Service) Inventory(ctx context.Context) (names []string, origins map[string]protocol.RepoOrigin, limited bool) {
	names = []string{}
	origins = map[string]protocol.RepoOrigin{}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	root, err := s.openRoot()
	if err != nil {
		return names, origins, true
	}
	defer func() { _ = root.file.Close() }()
	op := &operation{ctx: ctx}
	cursor := ""
	for {
		page, err := s.list(root, protocol.DiscoverWorkspace{Path: ".", Cursor: cursor}, op)
		if err != nil || !s.rootUnchanged(root) {
			return []string{}, map[string]protocol.RepoOrigin{}, true
		}
		for _, entry := range page.Entries {
			if entry.Kind != "repository" {
				continue
			}
			names = append(names, entry.Name)
			if entry.Origin != nil {
				origins[entry.Name] = *entry.Origin
			}
			if len(names) == 2000 {
				return names, origins, true
			}
		}
		if page.NextCursor == "" {
			return names, origins, page.Truncated
		}
		cursor = page.NextCursor
	}
}

// NewOwned qualifies physical identities with the enrolled desktop ID.
func NewOwned(root func() string, desktopID string) *Service {
	s := New(root)
	s.namespace = desktopID
	return s
}

// Release drops a completed runtime's private source handoff. Active parents
// are never evicted by a stream of ended children: this is not a lifetime cap.
func (s *Service) Release(m protocol.WorkspaceManifest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.bound[m.LaunchGeneration]; ok && old.manifest.Digest == m.Digest {
		delete(s.bound, m.LaunchGeneration)
		for i, id := range s.boundOrder {
			if id == m.LaunchGeneration {
				s.boundOrder = append(s.boundOrder[:i], s.boundOrder[i+1:]...)
				break
			}
		}
	}
}
