//go:build linux

package clusterworkspace

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"golang.org/x/sys/unix"
)

// DiskGit pins public/member/Git/object identities in memory. Push never reads
// agent-written Git config, index, alternates, remote names or URL rewrites: it
// copies bounded regular objects into an isolated bare Git directory and pushes
// an explicit verified SHA to the immutable authorized URL. Concurrent object
// mutation causes refusal/retry, never a fallback to the live metadata.
type objectSnapshot struct {
	path, head string
	bytes      int64
	entries    int
}

type DiskGit struct {
	snapshots       map[string]objectSnapshot
	cachedBytes     int64
	cachedEntries   int
	roundChecked    bool
	roundError      error
	memberErrors    map[string]error
	enforceCapacity bool
	cleanupFailed   bool
	privateRoot     *os.File
	fresh           map[string]bool
	snapshotBytes   int64
	snapshotEntries int
	expected        map[string]MemberIdentity
	mu              sync.Mutex
	root            *os.File
	rootID          protocol.WorkspaceRoot
	branch          string
	members         map[string]*diskMember
}
type diskMember struct {
	dir, git, objects      *os.File
	root, gitID, objectsID protocol.WorkspaceRoot
}

var oid = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var hexDirectory = regexp.MustCompile(`^[0-9a-f]{2}$`)
var objectName = regexp.MustCompile(`^(?:[0-9a-f]{38}|[0-9a-f]{62}|pack-[0-9a-f]{40}\.(?:pack|idx|rev)|pack-[0-9a-f]{64}\.(?:pack|idx|rev))$`)

// NewDiskGit requires private storage outside agent write authority. A path
// outside the public tree alone is insufficient when the agent has the same UID.
func NewDiskGit(public, private, session string, expected ...MemberIdentity) (*DiskGit, error) {
	return newDiskGit(public, private, session, true, expected...)
}
func newDiskGit(public, private, session string, enforceCapacity bool, expected ...MemberIdentity) (*DiskGit, error) {
	if err := os.MkdirAll(public, 0700); err != nil {
		return nil, ErrPlan
	}
	fd, e := unix.Open(public, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, ErrPlan
	}
	root := os.NewFile(uintptr(fd), public)
	identity, e := fileID(root)
	if e != nil {
		closeRead(root)
		return nil, ErrPlan
	}
	if enforceCapacity {
		if e := boundedVolume(root); e != nil {
			closeRead(root)
			return nil, e
		}
	}
	if err := os.MkdirAll(private, 0700); err != nil {
		closeRead(root)
		return nil, ErrPlan
	}
	privateFD, e := unix.Open(private, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		closeRead(root)
		return nil, ErrPlan
	}
	privateRoot := os.NewFile(uintptr(privateFD), private)
	rel, e := filepath.Rel(public, private)
	if e != nil || rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..") {
		closeRead(root)
		closeRead(privateRoot)
		return nil, ErrPlan
	}
	identities := map[string]MemberIdentity{}
	for _, identity := range expected {
		identities[identity.ID] = identity
	}
	return &DiskGit{snapshots: map[string]objectSnapshot{}, enforceCapacity: enforceCapacity, privateRoot: privateRoot, fresh: map[string]bool{}, expected: identities, root: root, rootID: identity, branch: "wip/" + session, members: map[string]*diskMember{}}, nil
}
func (d *DiskGit) Binding(ref protocol.WorkspaceRuntimeRef, digest string) protocol.WorkspaceRuntimeBinding {
	return protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: ref, ManifestDigest: digest, Root: d.rootID}
}
func (d *DiskGit) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, snapshot := range d.snapshots {
		if os.RemoveAll(snapshot.path) != nil {
			d.cleanupFailed = true
		}
	}
	d.snapshots = nil
	for _, m := range d.members {
		closeRead(m.dir)
		closeRead(m.git)
		closeRead(m.objects)
	}
	closeRead(d.root)
	closeRead(d.privateRoot)
}
func fileID(f *os.File) (protocol.WorkspaceRoot, error) {
	var s unix.Stat_t
	if e := unix.Fstat(int(f.Fd()), &s); e != nil {
		return protocol.WorkspaceRoot{}, e
	}
	return protocol.WorkspaceRoot{Device: s.Dev, Inode: s.Ino}, nil
}
func beneath(root *os.File, path string, dir bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_NOFOLLOW
	if dir {
		flags |= unix.O_DIRECTORY
	}
	fd, e := unix.Openat2(int(root.Fd()), path, &unix.OpenHow{Flags: uint64(flags), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if e != nil {
		return nil, ErrPlan
	}
	f := os.NewFile(uintptr(fd), path)
	s, e := f.Stat()
	if e != nil || (!dir && (!s.Mode().IsRegular() || !singleLink(s))) {
		closeRead(f)
		return nil, ErrPlan
	}
	return f, nil
}
func (d *DiskGit) valid(m Member) (*diskMember, error) {
	pin := d.members[m.ID]
	if pin == nil {
		return nil, ErrPlan
	}
	currentFD, openErr := unix.Open(d.root.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if openErr != nil {
		return nil, ErrPlan
	}
	current := os.NewFile(uintptr(currentFD), d.root.Name())
	currentID, currentErr := fileID(current)
	closeRead(current)
	if currentErr != nil || currentID != d.rootID {
		return nil, ErrPlan
	}
	root, e := fileID(d.root)
	if e != nil || root != d.rootID {
		return nil, ErrPlan
	}
	for _, check := range []struct {
		path string
		want protocol.WorkspaceRoot
	}{{m.ID, pin.root}, {m.ID + "/.git", pin.gitID}, {m.ID + "/.git/objects", pin.objectsID}} {
		f, e := beneath(d.root, check.path, true)
		if e != nil {
			return nil, ErrPlan
		}
		id, e := fileID(f)
		closeRead(f)
		if e != nil || id != check.want {
			return nil, ErrPlan
		}
	}
	// A linked gitdir or alternates cannot redirect this member's object access.
	var st unix.Stat_t
	if e := unix.Fstatat(int(pin.git.Fd()), "commondir", &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(e, unix.ENOENT) {
		return nil, ErrPlan
	}
	info, e := beneath(pin.objects, "info", true)
	if e != nil {
		return nil, ErrPlan
	}
	defer closeRead(info)
	for _, name := range []string{"alternates", "http-alternates"} {
		if e := unix.Fstatat(int(info.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(e, unix.ENOENT) {
			return nil, ErrPlan
		}
	}

	return pin, nil
}
func gitCall(ctx context.Context, dir string, m Member, args ...string) error {
	u, e := endpoint(m.CloneURL)
	if e != nil {
		return ErrPlan
	}
	cfg := []string{"http.followRedirects=false", "http.proxy=", "http.extraHeader="}
	if ca := os.Getenv("SSL_CERT_FILE"); ca != "" {
		cfg = append(cfg, "http.sslCAInfo="+ca)
	}
	var env []string
	if m.Token != "" {
		env = []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http." + u.Scheme + "://" + u.Host + "/.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(gitprovider.Default.TokenUserForHost(u.Hostname())+":"+m.Token))}
	}
	return hostgit.Run(ctx, hostgit.Options{Dir: dir, Stdout: io.Discard, Stderr: io.Discard, Protocols: []string{u.Scheme}, Config: cfg, Env: env, MemoryLimit: true, LimitKB: 512 << 10, Untrusted: true, Timeout: 80 * time.Second}, args...)
}
func (d *DiskGit) Prepare(ctx context.Context, m Member, branch string, resume bool) (string, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if branch != d.branch || !stableID.MatchString(m.ID) {
		return "", "", ErrPlan
	}
	dest := filepath.Join(d.root.Name(), m.ID)
	// Fresh allocation only. Existing contents require explicit recovery; never
	// delete a directory whose allocation/identity cannot be proven unused.
	_, statErr := os.Lstat(dest) //nolint:gosec // m.ID passed stableID (single ASCII component) above; later access uses pinned openat2 descriptors.
	allocated := os.IsNotExist(statErr)
	if statErr == nil && !resume {
		return "", "", ErrPlan
	}
	if os.IsNotExist(statErr) {
		if resume {
			return "", "", ErrPlan
		} // local retained work must not silently disappear
		if e := gitCall(ctx, d.root.Name(), m, "clone", "--no-local", "--", m.CloneURL, dest); e != nil {
			return "", "", ErrPlan
		}
		if e := gitCall(ctx, dest, m, "checkout", "-b", branch); e != nil {
			return "", "", ErrPlan
		}
	} else if statErr != nil {
		return "", "", ErrPlan
	}
	pin := &diskMember{}
	var e error
	pin.dir, e = beneath(d.root, m.ID, true)
	if e != nil {
		return "", "", e
	}
	pin.git, e = beneath(pin.dir, ".git", true)
	if e != nil {
		closeRead(pin.dir)
		return "", "", e
	}
	pin.objects, e = beneath(pin.git, "objects", true)
	if e != nil {
		closeRead(pin.dir)
		closeRead(pin.git)
		return "", "", e
	}
	pin.root, _ = fileID(pin.dir)
	pin.gitID, _ = fileID(pin.git)
	pin.objectsID, _ = fileID(pin.objects)
	if resume {
		old, ok := d.expected[m.ID]
		if !ok || old.Directory.Inode != pin.root.Inode || old.GitDirectory.Inode != pin.gitID.Inode || old.Objects.Inode != pin.objectsID.Inode {
			closeRead(pin.dir)
			closeRead(pin.git)
			closeRead(pin.objects)
			return "", "", ErrPlan
		}
	}
	d.members[m.ID] = pin
	if allocated {
		d.fresh[m.ID] = true
	}
	if _, e = d.valid(m); e != nil {
		return "", "", e
	}
	if e = d.checkBudget(ctx); e != nil {
		return "", "", e
	}
	head, e := d.head(m)
	if e != nil {
		return "", "", e
	}
	return head, head, nil
}
func (d *DiskGit) head(m Member) (string, error) {
	pin, e := d.valid(m)
	if e != nil {
		return "", e
	}
	read := func(path string) (string, error) {
		f, e := beneath(pin.git, path, false)
		if e != nil {
			return "", e
		}
		defer closeRead(f)
		raw, e := io.ReadAll(io.LimitReader(f, 256))
		return strings.TrimSpace(string(raw)), e
	}
	head, e := read("HEAD")
	if e != nil {
		return "", ErrPlan
	}
	if oid.MatchString(head) {
		return head, nil
	} // detached HEAD is ordinary Git work
	if !strings.HasPrefix(head, "ref: refs/heads/") {
		return "", ErrPlan
	}
	ref := strings.TrimPrefix(head, "ref: ")
	if filepath.Clean(ref) != ref || strings.ContainsAny(ref, "\\\x00\r\n") {
		return "", ErrPlan
	}
	head, e = read(ref)
	if e != nil {
		packed, err := beneath(pin.git, "packed-refs", false)
		if err != nil {
			return "", ErrPlan
		}
		defer closeRead(packed)
		data, err := io.ReadAll(io.LimitReader(packed, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			return "", ErrPlan
		}
		lines := strings.Split(string(data), "\n")
		if len(lines) > 10000 {
			return "", ErrPlan
		}
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[1] == ref && oid.MatchString(fields[0]) {
				head = fields[0]
				e = nil
				break
			}
		}
	}
	if e != nil || !oid.MatchString(head) {
		return "", ErrPlan
	}

	return head, nil
}
func (d *DiskGit) Head(ctx context.Context, m Member) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return d.head(m)
}

// Snapshot copies while the runtime's writer containment is frozen. Network
// I/O consumes only this private immutable copy, after thaw, including retries.
func (d *DiskGit) Snapshot(ctx context.Context, m Member, head string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshot(ctx, m, head)
}
func (d *DiskGit) snapshot(ctx context.Context, m Member, head string) error {
	if d.cleanupFailed || !oid.MatchString(head) {
		return ErrPlan
	}
	pin, err := d.valid(m)
	if err != nil {
		return err
	}
	if err := d.roundBudget(ctx, m.ID); err != nil {
		return err
	}
	current, err := d.head(m)
	if err != nil || current != head {
		return ErrPlan
	}
	if old, ok := d.snapshots[m.ID]; ok {
		if old.head == head {
			return nil
		}
		if os.RemoveAll(old.path) != nil {
			d.cleanupFailed = true
			return ErrPlan
		}
		d.cachedBytes -= old.bytes
		d.cachedEntries -= old.entries
		delete(d.snapshots, m.ID)
	}
	shadow, err := os.MkdirTemp(fmt.Sprintf("/proc/self/fd/%d", d.privateRoot.Fd()), "git-")
	if err != nil {
		return ErrPlan
	}
	kept := false
	defer func() {
		if !kept && os.RemoveAll(shadow) != nil {
			d.cleanupFailed = true
		}
	}()
	if err := gitCall(ctx, shadow, m, "init", "--bare", "-q"); err != nil {
		return ErrPlan
	}
	// Both live retained cache and work attempted this round share the limits.
	bytes := max(d.cachedBytes, d.snapshotBytes)
	entries := max(d.cachedEntries, d.snapshotEntries)
	beforeBytes, beforeEntries := bytes, entries
	err = copyObjects(ctx, pin.objects, filepath.Join(shadow, "objects"), &bytes, &entries, 0)
	spentBytes, spentEntries := bytes-beforeBytes, entries-beforeEntries
	d.snapshotBytes += spentBytes
	d.snapshotEntries += spentEntries
	if err != nil {
		return err
	}
	current, err = d.head(m)
	if err != nil || current != head {
		return ErrPlan
	}
	d.snapshots[m.ID] = objectSnapshot{path: shadow, head: head, bytes: spentBytes, entries: spentEntries}
	d.cachedBytes += spentBytes
	d.cachedEntries += spentEntries
	kept = true
	return nil
}
func (d *DiskGit) PushSnapshot(ctx context.Context, m Member, branch, head string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pushSnapshot(ctx, m, branch, head)
}
func (d *DiskGit) pushSnapshot(ctx context.Context, m Member, branch, head string) error {
	if d.cleanupFailed || !m.PushAuthorized || m.Token == "" || branch != d.branch || !oid.MatchString(head) {
		return ErrPlan
	}
	if _, err := d.valid(m); err != nil {
		return err
	}
	snapshot, ok := d.snapshots[m.ID]
	if !ok || snapshot.head != head {
		return ErrPlan
	}
	// Never consult live config/index or recopy agent files during a retry.
	return gitCall(ctx, snapshot.path, m, "push", "--force", "--", m.PushURL, head+":refs/heads/"+branch)
}

// Stop has already proven all writers gone and may combine these phases.
func (d *DiskGit) Push(ctx context.Context, m Member, branch, head string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.snapshot(ctx, m, head); err != nil {
		return err
	}
	return d.pushSnapshot(ctx, m, branch, head)
}
func copyObjects(ctx context.Context, src *os.File, dest string, bytes *int64, entries *int, depth int) error {
	if depth > 1 {
		return ErrPlan
	}
	listing, e := beneath(src, ".", true)
	if e != nil {
		return e
	}
	defer closeRead(listing)
	for {
		batch, e := listing.ReadDir(256)
		if e != nil && !errors.Is(e, io.EOF) {
			return ErrPlan
		}
		for _, entry := range batch {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			*entries++
			if *entries > 200000 {
				return ErrPlan
			}
			name := entry.Name()
			if name == "info" {
				continue
			}
			if entry.IsDir() {
				if depth != 0 || (name != "pack" && (len(name) != 2 || !hexDirectory.MatchString(name))) {
					return ErrPlan
				}
				f, e := beneath(src, name, true)
				if e != nil {
					return e
				}
				e = os.MkdirAll(filepath.Join(dest, name), 0700)
				if e == nil {
					e = copyObjects(ctx, f, filepath.Join(dest, name), bytes, entries, depth+1)
				}
				closeRead(f)
				if e != nil {
					return e
				}
				continue
			}
			if depth != 1 || !objectName.MatchString(name) {
				return ErrPlan
			}
			f, e := beneath(src, name, false)
			if e != nil {
				return e
			}
			before, e := f.Stat()
			if e != nil {
				closeRead(f)
				return ErrPlan
			}
			if before.Size() < 0 || before.Size() > protocol.DefaultWorkspaceBudget().FetchedBytes-*bytes {
				closeRead(f)
				return ErrPlan
			}
			*bytes += before.Size()
			//nolint:gosec // name is a validated object basename; dest is the private shadow directory.
			out, e := os.OpenFile(filepath.Join(dest, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				closeRead(f)
				return ErrPlan
			}
			n, e := io.Copy(out, cancelReader{ctx: ctx, reader: io.LimitReader(f, before.Size()+1)})
			closeErr := out.Close()
			after, statErr := f.Stat()
			closeRead(f)
			if e != nil || closeErr != nil || statErr != nil || n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
				return fmt.Errorf("object snapshot unavailable")
			}
		}
		if errors.Is(e, io.EOF) {
			return nil
		}
	}
}

func (d *DiskGit) Identities() []MemberIdentity {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]MemberIdentity, 0, len(d.members))
	for id, m := range d.members {
		out = append(out, MemberIdentity{ID: id, Directory: m.root, GitDirectory: m.gitID, Objects: m.objectsID})
	}
	return out
}

func (d *DiskGit) BeginRound() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.roundChecked = false
	d.roundError = nil
	d.memberErrors = nil
	d.snapshotBytes = 0
	d.snapshotEntries = 0
}

// Admission and settlement share one aggregate accounting pass. Physical
// capacity must additionally be enforced by the session volume's storage class.
var errAggregateBudget = errors.New("aggregate workspace budget exhausted")

func (d *DiskGit) checkBudget(ctx context.Context) error { return d.scanBudget(ctx, nil) }

// ValidateRound walks each tree at most once per round. A member policy
// refusal is local only while complete aggregate accounting remains possible.
// Inaccessible or cross-mount usage is unknown, never zero, and fails the round.
func (d *DiskGit) ValidateRound(ctx context.Context) map[string]error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_ = d.roundBudget(ctx, "")
	out := map[string]error{}
	for id := range d.members {
		if d.roundError != nil {
			out[id] = d.roundError
		} else if err := d.memberErrors[id]; err != nil {
			out[id] = err
		}
	}
	return out
}
func (d *DiskGit) roundBudget(ctx context.Context, id string) error {
	if !d.roundChecked {
		d.roundChecked = true
		d.memberErrors = map[string]error{}
		if d.enforceCapacity {
			d.roundError = boundedVolume(d.root)
		}
		if d.roundError == nil {
			d.roundError = d.scanBudget(ctx, d.memberErrors)
		}
	}
	if d.roundError != nil {
		return d.roundError
	}
	return d.memberErrors[id]
}

func (d *DiskGit) scanBudget(ctx context.Context, memberErrors map[string]error) error {
	budget := protocol.DefaultWorkspaceBudget()
	var checkout, objects, metadata int64
	var entries, metaEntries int
	var walk func(*os.File, string, string, int) error
	walk = func(dir *os.File, prefix, member string, depth int) error {
		// Refuse settlement at the member policy depth, but keep counting with
		// pinned, no-symlink/no-mount descriptors. Bound accounting separately:
		// deeper or inaccessible usage makes the entire budget unproven.
		if depth > 128 {
			if memberErrors == nil || member == "" {
				return ErrPlan
			}
			memberErrors[member] = ErrPlan
		}
		if depth > 256 {
			return ErrPlan
		}
		listing, e := beneath(dir, ".", true)
		if e != nil {
			return e
		}
		defer closeRead(listing)
		for {
			batch, e := listing.ReadDir(256)
			if e != nil && !errors.Is(e, io.EOF) {
				return ErrPlan
			}
			for _, entry := range batch {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				entries++
				if entries > budget.CheckoutEntries {
					return errAggregateBudget
				}
				path := prefix + "/" + entry.Name()
				isGit := strings.Contains(path, "/.git/") || strings.HasSuffix(path, "/.git")
				if isGit {
					metaEntries++
					if metaEntries > budget.MetadataEntries {
						return errAggregateBudget
					}
				}
				var st unix.Stat_t
				if unix.Fstatat(int(dir.Fd()), entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
					return ErrPlan
				}
				if st.Size < 0 || st.Size > budget.CheckoutBytes {
					return errAggregateBudget
				}
				mode := st.Mode & unix.S_IFMT
				if mode == unix.S_IFLNK {
					continue
				}
				if mode == unix.S_IFDIR {
					f, e := beneath(dir, entry.Name(), true)
					if e == nil {
						owner := member
						if depth == 0 && d.members[entry.Name()] != nil {
							owner = entry.Name()
						}
						e = walk(f, path, owner, depth+1)
						closeRead(f)
					}
					if e != nil {
						return e
					}
					continue
				}
				if mode != unix.S_IFREG {
					continue
				}
				switch {
				case strings.Contains(path, "/.git/objects/"):
					objects += st.Size
				case isGit:
					metadata += st.Size
				default:
					checkout += st.Size
				}
				if checkout > budget.CheckoutBytes || objects > budget.FetchedBytes || metadata > budget.MetadataBytes {
					return errAggregateBudget
				}
			}
			if errors.Is(e, io.EOF) {
				return nil
			}
		}
	}
	return walk(d.root, "", "", 0)
}

// Read-only descriptor close failures do not affect data/identity decisions.
func closeRead(f *os.File) { _ = f.Close() }
func singleLink(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Nlink == 1
}

// Called only before readiness/agent admission under the Runtime mutex. An
// identity mismatch retains the allocation; only proven unused clones go away.
func (d *DiskGit) RollbackUnused() []string {
	removed := []string{}
	d.mu.Lock()
	defer d.mu.Unlock()
	for id := range d.fresh {
		pin, e := d.valid(Member{ID: id})
		if e != nil {
			continue
		}
		if os.RemoveAll(filepath.Join(d.root.Name(), id)) != nil {
			continue
		}
		closeRead(pin.dir)
		closeRead(pin.git)
		closeRead(pin.objects)
		delete(d.members, id)
		delete(d.fresh, id)
		removed = append(removed, id)
	}
	return removed
}

var ErrStorage = errors.New("cluster workspace requires an enforced dedicated volume byte and inode limit")

// statfs describes the filesystem's enforced physical ceiling, not PVC request
// metadata (which local-path/shared-directory provisioners do not enforce).
// A provider without a small dedicated filesystem must refuse preparation.
func boundedVolume(root *os.File) error {
	var fs unix.Statfs_t
	if unix.Fstatfs(int(root.Fd()), &fs) != nil {
		return ErrStorage
	}
	budget := protocol.DefaultWorkspaceBudget()
	maxBytes := budget.CheckoutBytes + budget.FetchedBytes + budget.MetadataBytes
	//nolint:gosec // block size is positive and bounded before conversion; frozen budgets are positive.
	if fs.Bsize <= 0 || fs.Bsize > 1<<20 || fs.Blocks == 0 || fs.Blocks > uint64(maxBytes/fs.Bsize) || fs.Files == 0 || fs.Files > uint64(budget.CheckoutEntries+budget.MetadataEntries) || fs.Flags&unix.ST_RDONLY != 0 {
		return ErrStorage
	}
	parent, e := os.Open(filepath.Dir(root.Name()))
	if e != nil {
		return ErrStorage
	}
	defer closeRead(parent)
	parentID, e := fileID(parent)
	rootID, rootErr := fileID(root)
	if e != nil || rootErr != nil || parentID.Device == rootID.Device {
		return ErrStorage
	}
	return nil
}

type cancelReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r cancelReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.reader.Read(p)
}
