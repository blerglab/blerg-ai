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
)

const workspaceFileAbsoluteMax int64 = 25 << 20
const workspaceProtectedNameMessage = "Blerg doesn't open files that usually hold secrets (protected file names or directories)."

type workspaceFileLimits struct {
	Text, Media int64
	// Launch-time directory identity prevents retargeting a session's workspace
	// before the read begins. The pinned Root is compared against it as well.
	expectedRoot os.FileInfo
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
// foldName folds a name for the secret checks: upper then lower case, so Unicode letters that only
// map to ASCII one way (ſ → S → s, the Kelvin sign → k) cannot dodge a match.
func foldName(name string) string { return strings.ToLower(strings.ToUpper(name)) }

func workspaceSecretDirectory(name string) bool {
	switch foldName(name) {
	case ".git", ".ssh", ".aws", ".gnupg", ".kube", ".docker", ".claude", ".codex", "secrets", ".vault":
		return true
	default:
		return false
	}
}
func workspaceSecretFile(name string) bool {
	name = foldName(name)
	switch name {
	case ".env", ".netrc", ".npmrc", ".pypirc", ".git-credentials", ".pgpass", ".htpasswd", ".vault-token",
		"kubeconfig", ".kubeconfig":
		return true
	}
	for _, prefix := range []string{".env.", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa", "credentials"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	if strings.Contains(name, "secret") {
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".kdbx", ".ppk", ".jks", ".keystore", ".tfstate", ".tfstate.backup"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}
func workspaceSecretPath(name string) bool {
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if workspaceSecretDirectory(part) {
			return true
		}
	}
	return workspaceSecretFile(filepath.Base(name))
}
func workspaceHasSecretDirectory(name string) bool {
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if workspaceSecretDirectory(part) {
			return true
		}
	}
	return false
}

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
func workspaceContained(root, name string) (string, bool) {
	rel, err := filepath.Rel(root, name)
	return rel, err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Resolve each link explicitly so an intermediate secret alias cannot hide
// behind a benign final EvalSymlinks result. Root confines metadata operations
// while a separate no-follow descriptor walk confines the eventual open.
func workspaceResolve(root *os.Root, rootName, requested string) (string, os.FileInfo, error) {
	pending := strings.Split(filepath.ToSlash(requested), "/")
	var parts []string
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if len(parts) == 0 {
				return "", nil, os.ErrPermission
			}
			parts = parts[:len(parts)-1]
			continue
		}
		if workspaceSecretDirectory(part) {
			return "", nil, errWorkspaceSecret
		}
		current := filepath.Join(append(append([]string(nil), parts...), part)...)
		info, err := root.Lstat(current)
		if err != nil {
			return "", nil, err
		}
		if !info.IsDir() && workspaceSecretFile(part) {
			return "", nil, errWorkspaceSecret
		}
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", nil, os.ErrPermission
			}
			target, err := root.Readlink(current)
			if err != nil {
				return "", nil, err
			}
			if strings.Contains(target, "\\") || strings.IndexFunc(target, unicode.IsControl) >= 0 {
				return "", nil, os.ErrPermission
			}
			if workspaceSecretPath(target) {
				return "", nil, errWorkspaceSecret
			}
			var rel string
			if filepath.IsAbs(target) {
				prefix := strings.TrimSuffix(rootName, string(filepath.Separator)) + string(filepath.Separator)
				if !strings.HasPrefix(target, prefix) {
					return "", nil, os.ErrPermission
				}
				rel = strings.TrimPrefix(target, prefix)
				parts = nil
			} else {
				rel = target
			}
			// Do not Clean before walking: a symlink followed by /.. must
			// still have its target checked, even if the final path looks safe.
			pending = append(strings.Split(filepath.ToSlash(rel), "/"), pending...)
			continue
		}
		parts = append(parts, part)
		if len(pending) == 0 {
			if !info.Mode().IsRegular() {
				return "", nil, os.ErrPermission
			}
			return filepath.Join(parts...), info, nil
		}
		if !info.IsDir() {
			return "", nil, os.ErrNotExist
		}
	}
	return "", nil, os.ErrPermission
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
	current, err := os.Stat(name)
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
	resolvedRoot, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return workspaceFileError(clean, err)
	}
	if workspaceHasSecretDirectory(resolvedRoot) {
		return workspaceFileFailure(clean, "forbidden", workspaceProtectedNameMessage)
	}
	rootInfo, err := os.Stat(resolvedRoot)
	if err != nil {
		return workspaceFileError(clean, err)
	}
	if !rootInfo.IsDir() {
		return workspaceFileFailure(clean, "forbidden", "The session workspace is not a directory.")
	}
	if limits.expectedRoot != nil && !os.SameFile(limits.expectedRoot, rootInfo) {
		return workspaceFileError(clean, os.ErrPermission)
	}
	root, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return workspaceFileError(clean, err)
	}
	defer func() { _ = root.Close() }()
	if !workspaceRootUnchanged(root, resolvedRoot, rootInfo) || (limits.expectedRoot != nil && !workspaceRootUnchanged(root, resolvedRoot, limits.expectedRoot)) {
		return workspaceFileError(clean, os.ErrPermission)
	}
	resolved, checked, err := workspaceResolve(root, resolvedRoot, clean)
	if err != nil {
		return workspaceFileError(clean, err)
	}
	full, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, clean))
	if err != nil {
		return workspaceFileError(clean, err)
	}
	rel, inside := workspaceContained(resolvedRoot, full)
	if !inside || rel != resolved {
		return workspaceFileError(clean, os.ErrPermission)
	}
	if workspaceSecretPath(rel) {
		return workspaceFileFailure(clean, "forbidden", workspaceProtectedNameMessage)
	}
	// Check the pathname identity independently of the pinned-root resolver.
	named, err := os.Lstat(full)
	if err != nil {
		return workspaceFileError(clean, err)
	}
	if !os.SameFile(named, checked) || !workspaceRegularFile(checked) {
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
	finalResolved, finalInfo, err := workspaceResolve(root, resolvedRoot, clean)
	if err != nil {
		return workspaceFileError(clean, err)
	}
	if finalResolved != resolved || !os.SameFile(finalInfo, final) {
		return workspaceFileError(clean, os.ErrPermission)
	}
	return protocol.WorkspaceFileResult{OK: true, Path: filepath.ToSlash(clean), Size: final.Size(), View: view, ContentType: contentType, ContentB64: base64.StdEncoding.EncodeToString(body), Mtime: final.ModTime().UTC().Format(time.RFC3339Nano)}
}
