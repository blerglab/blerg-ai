package daemon

import (
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/fileview"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspacepolicy"
)

const workspaceFileAbsoluteMax int64 = 25 << 20
const workspaceProtectedNameMessage = "Blerg doesn't open files that usually hold secrets (protected file names or directories)."

type workspaceFileLimits struct {
	Text, Media int64
	// Launch-time directory identity prevents retargeting a session's workspace
	// before the read begins. The pinned Root is compared against it as well.
	expectedRoot os.FileInfo
	pinnedRoot   *os.Root
	// Per-call test hooks exercise the check/open/read boundaries without
	// changing process-wide state or exposing a runtime configuration hook.
	beforeOpen, afterOpen func()
}

func parseWorkspaceFileLimits() workspaceFileLimits {
	return normalizedWorkspaceFileLimits(workspaceFileLimits{
		Text:  workspaceLimitEnv("BLERG_RUNNER_WORKSPACE_TEXT_MAX_BYTES", 2<<20),
		Media: workspaceLimitEnv("BLERG_RUNNER_WORKSPACE_MEDIA_MAX_BYTES", 25<<20),
	})
}
func workspaceLimitEnv(name string, fallback int64) int64 {
	n, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if errors.Is(err, strconv.ErrRange) && n > 0 {
		return workspaceFileAbsoluteMax
	}
	if err != nil || n <= 0 {
		return fallback
	}
	return min(n, workspaceFileAbsoluteMax)
}
func normalizedWorkspaceFileLimits(limits workspaceFileLimits) workspaceFileLimits {
	if limits.Text <= 0 {
		limits.Text = 2 << 20
	}
	if limits.Media <= 0 {
		limits.Media = 25 << 20
	}
	limits.Text = min(limits.Text, workspaceFileAbsoluteMax)
	limits.Media = min(limits.Media, workspaceFileAbsoluteMax)
	return limits
}

// Secret paths have one policy, used for requested names, resolved names and
// every intermediate symlink target. Directory names are protected anywhere;
// file globs apply to basenames, so a project called secret-project is allowed.
func workspaceSecretFile(name string) bool { return !workspacepolicy.File(name) }
func workspaceSecretPath(name string) bool { return !workspacepolicy.File(name) }

func workspaceUnsafePath(name string) bool {
	if name == "" || len(name) > 4096 || !utf8.ValidString(name) || filepath.IsAbs(name) || strings.HasPrefix(name, "~") || strings.Contains(name, "\\") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return true
	}
	if len(name) >= 2 && name[1] == ':' && ((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z')) {
		return true
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}
func workspaceCleanRequest(name string) (string, bool) {
	if workspaceUnsafePath(name) {
		return "", false
	}
	// The HTTP layer decodes once. Repeated encodings must not turn a planted
	// link into traversal in a later peer or viewer. The actual OS name is not
	// decoded here; safe literal percent-encoded filenames remain literal.
	decoded := name
	for i := 0; i < 8; i++ {
		next, err := url.PathUnescape(decoded)
		if err != nil {
			return "", false
		}
		if next == decoded {
			clean := filepath.Clean(name)
			return clean, clean != "."
		}
		if workspaceUnsafePath(next) {
			return "", false
		}
		decoded = next
	}
	return "", false
}

var errWorkspaceSecret = errors.New("protected workspace file")

func workspaceFileFailure(path, code, message string) protocol.WorkspaceFileResult {
	return protocol.WorkspaceFileResult{Path: filepath.ToSlash(path), ErrorCode: code, Error: message}
}
func workspaceFileTooLarge(path string, size int64) protocol.WorkspaceFileResult {
	res := workspaceFileFailure(path, "too_large", "The workspace file exceeds the viewing size limit.")
	res.Size = size
	return res
}
func workspaceFileError(path string, err error) protocol.WorkspaceFileResult {
	if errors.Is(err, errWorkspaceSecret) {
		return workspaceFileFailure(path, "forbidden", workspaceProtectedNameMessage)
	}
	if os.IsNotExist(err) {
		return workspaceFileFailure(path, "not_found", "The workspace file does not exist.")
	}
	return workspaceFileFailure(path, "forbidden", "Blerg cannot safely open this workspace file.")
}
func workspaceRootUnchanged(root *os.Root, name string, checked os.FileInfo) bool {
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(pinned, checked) {
		return false
	}
	current, err := os.Lstat(name) //nolint:gosec // launch root identity revalidation only; reads use the pinned no-follow descriptor
	return err == nil && current.IsDir() && os.SameFile(current, pinned)
}

// readWorkspaceFile never logs content, endpoints or raw OS errors. A returned
// path is relative to the session folder; errors carry no file data. Oversize
// files are refused in full (no partial text response).
func readWorkspaceFile(folder, name string, limits workspaceFileLimits) protocol.WorkspaceFileResult {
	clean, ok := workspaceCleanRequest(name)
	if !ok {
		return workspaceFileFailure("", "forbidden", "Workspace file paths must stay inside the session folder.")
	}
	if workspaceSecretPath(clean) {
		return workspaceFileFailure(clean, "forbidden", workspaceProtectedNameMessage)
	}
	if folder == "" {
		return workspaceFileFailure(clean, "unavailable", "This session has no workspace folder.")
	}
	absolute, err := filepath.Abs(folder)
	if err != nil {
		return workspaceFileError(clean, err)
	}
	resolvedRoot := absolute
	rootInfo, err := os.Lstat(resolvedRoot)
	if err != nil {
		return workspaceFileError(clean, err)
	}
	if !rootInfo.IsDir() || !workspacePublicRoot(resolvedRoot) || !workspacePublicPath(clean) {
		return workspaceFileError(clean, os.ErrPermission)
	}
	if limits.expectedRoot != nil && !os.SameFile(limits.expectedRoot, rootInfo) {
		return workspaceFileError(clean, os.ErrPermission)
	}
	root := limits.pinnedRoot
	if root == nil {
		root, err = os.OpenRoot(resolvedRoot)
		if err != nil {
			return workspaceFileError(clean, err)
		}
		defer func() { _ = root.Close() }()
	}
	if !workspaceRootUnchanged(root, resolvedRoot, rootInfo) {
		return workspaceFileError(clean, os.ErrPermission)
	}
	resolved := clean
	checkedFile, err := workspaceOpenFile(root, resolved)
	if err != nil {
		return workspaceFileError(clean, err)
	}
	checked, err := checkedFile.Stat()
	_ = checkedFile.Close()
	if err != nil || !workspaceRegularFile(checked) {
		return workspaceFileError(clean, os.ErrPermission)
	}
	limits = normalizedWorkspaceFileLimits(limits)
	if limits.beforeOpen != nil {
		limits.beforeOpen()
	}
	file, err := workspaceOpenFile(root, resolved)
	if err != nil {
		return workspaceFileError(clean, err)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return workspaceFileError(clean, err)
	}
	if !workspaceRegularFile(opened) || !os.SameFile(checked, opened) || !workspaceRootUnchanged(root, resolvedRoot, rootInfo) {
		return workspaceFileError(clean, os.ErrPermission)
	}
	if limits.afterOpen != nil {
		limits.afterOpen()
	}
	// Initial sniffing fits even the smaller configured cap. Reuse these bytes
	// instead of seeking, so the entire read never exceeds the chosen cap+1.
	head, err := io.ReadAll(io.LimitReader(file, min(int64(512), min(limits.Text, limits.Media)+1)))
	if err != nil {
		return workspaceFileError(clean, err)
	}
	contentType, view := fileview.Classify(clean, head, opened.Size())
	byteLimit := limits.Text
	if view == fileview.Image || view == fileview.PDF {
		byteLimit = limits.Media
	}
	if opened.Size() > byteLimit {
		return workspaceFileTooLarge(clean, opened.Size())
	}
	tail, err := io.ReadAll(io.LimitReader(file, byteLimit+1-int64(len(head))))
	if err != nil {
		return workspaceFileError(clean, err)
	}
	head = append(head, tail...)
	body := head
	if int64(len(body)) > byteLimit {
		size := int64(len(body))
		if current, statErr := file.Stat(); statErr == nil {
			size = max(size, current.Size())
		}
		return workspaceFileTooLarge(clean, size)
	}
	final, err := file.Stat()
	if err != nil {
		return workspaceFileError(clean, err)
	}
	if !workspaceRegularFile(final) || !os.SameFile(opened, final) || final.Size() != opened.Size() || !final.ModTime().Equal(opened.ModTime()) || int64(len(body)) != final.Size() || !workspaceRootUnchanged(root, resolvedRoot, rootInfo) {
		return workspaceFileError(clean, os.ErrPermission)
	}
	finalFile, err := workspaceOpenFile(root, resolved)
	if err != nil {
		return workspaceFileError(clean, err)
	}
	finalInfo, err := finalFile.Stat()
	_ = finalFile.Close()
	if err != nil || !os.SameFile(finalInfo, final) || !workspacePublicRoot(resolvedRoot) {
		return workspaceFileError(clean, os.ErrPermission)
	}
	return protocol.WorkspaceFileResult{OK: true, Path: filepath.ToSlash(clean), Size: final.Size(), View: view, ContentType: contentType, ContentB64: base64.StdEncoding.EncodeToString(body), Mtime: final.ModTime().UTC().Format(time.RFC3339Nano)}
}

func workspacePublicPath(name string) bool          { return workspacepolicy.File(name) }
func workspacePublicDirectoryPath(name string) bool { return workspacepolicy.Directory(name) }

// Root names come from trusted runtime configuration, not request paths. Check
// both the configured spelling and its physical location so an ancestor alias
// cannot hide a protected root. Identity is checked separately before delivery.
func workspacePublicRoot(name string) bool {
	absolute, err := filepath.Abs(name)
	if err != nil || !workspacePublicDirectoryPath(absolute) {
		return false
	}
	physical, err := filepath.EvalSymlinks(absolute)
	return err == nil && workspacePublicDirectoryPath(physical)
}
