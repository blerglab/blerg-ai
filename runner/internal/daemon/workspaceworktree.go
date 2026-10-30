package daemon

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Registered host worktrees may keep metadata outside the member directory,
// but both metadata roots must remain inside the approved explorer root unless a
// signed host child allocation approves their exact external identities. Capture
// the registration proof at launch, pin both descriptors, and never learn a new
// gitdir from a later status request.
// Sandbox/pod .git pointers have no such exception.
type hostWorktreePin struct {
	git, common         *os.Root
	gitPath, commonPath string
	gitInfo, commonInfo os.FileInfo
	pointers            map[string]string
	publicFingerprint   string
}

func (e *LocalExplorer) pinHostWorktree(name string, info os.FileInfo) (hostWorktreePin, bool) {
	return e.pinHostWorktreeAllocation(name, info, nil)
}

// Only the signed child allocation can approve metadata outside the explorer root.
func (e *LocalExplorer) pinHostWorktreeAllocation(name string, info os.FileInfo, allocation *hostChildAllocation) (hostWorktreePin, bool) {
	var result hostWorktreePin
	public, err := e.openFile(filepath.ToSlash(filepath.Join(name, ".git")))
	if err != nil {
		return result, false
	}
	defer func() { _ = public.Close() }()
	publicInfo, err := public.Stat()
	if err != nil || !workspaceRegularFile(publicInfo) {
		return result, false
	}
	raw, err := io.ReadAll(io.LimitReader(public, 4097))
	if err != nil || len(raw) > 4096 {
		return result, false
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
	if !ok || strings.ContainsAny(target, "\r\n\x00") {
		return result, false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(e.folder, name, target)
	}
	gitPath := filepath.Clean(target)
	commonPath := filepath.Dir(filepath.Dir(gitPath))
	// A pointer and its registration cannot approve an arbitrary external root.
	// The exact worktrees/<name> relationship below keeps gitPath inside commonPath.
	relative, err := filepath.Rel(e.folder, commonPath)
	outside := err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
	if outside && (allocation == nil || allocation.GitPath != gitPath || allocation.Source.CommonPath != commonPath || allocation.Root != workspaceRootIdentity(info)) {
		return result, false
	}
	git, err := openWorkspaceExternalRoot(gitPath)
	if err != nil {
		return result, false
	}
	common, err := openWorkspaceExternalRoot(commonPath)
	if err != nil {
		_ = git.Close()
		return result, false
	}
	fail := func() (hostWorktreePin, bool) { _ = git.Close(); _ = common.Close(); return hostWorktreePin{}, false }
	gi, err := git.Stat(".")
	if err != nil {
		return fail()
	}
	ci, err := common.Stat(".")
	if err != nil {
		return fail()
	}
	if allocation != nil && (workspaceRootIdentity(gi) != allocation.GitIdentity || workspaceRootIdentity(ci) != allocation.Source.CommonIdentity) {
		return fail()
	}
	pointers := map[string]string{}
	for _, name := range []string{"commondir", "gitdir"} {
		file, err := workspaceOpenFile(git, name)
		if err != nil {
			return fail()
		}
		st, err := file.Stat()
		if err != nil || !workspaceRegularFile(st) {
			_ = file.Close()
			return fail()
		}
		raw, err := io.ReadAll(io.LimitReader(file, 4097))
		_ = file.Close()
		if err != nil || len(raw) > 4096 {
			return fail()
		}
		pointers[name] = string(raw)
	}
	commonTarget := strings.TrimSpace(pointers["commondir"])
	if !filepath.IsAbs(commonTarget) {
		commonTarget = filepath.Join(gitPath, commonTarget)
	}
	if filepath.Clean(commonTarget) != commonPath || filepath.Dir(gitPath) != filepath.Join(commonPath, "worktrees") || borrowsObjects(commonPath) {
		return fail()
	}
	back := strings.TrimSpace(pointers["gitdir"])
	if filepath.Base(back) != ".git" {
		return fail()
	}
	backRoot, err := openWorkspaceExternalRoot(filepath.Dir(back))
	if err != nil {
		return fail()
	}
	same := workspaceRootUnchanged(backRoot, filepath.Dir(back), info)
	_ = backRoot.Close()
	if !same {
		return fail()
	}
	current, err := public.Stat()
	if err != nil || workspaceMetadataFingerprint(current) != workspaceMetadataFingerprint(publicInfo) {
		return fail()
	}
	result = hostWorktreePin{git: git, common: common, gitPath: gitPath, commonPath: commonPath, gitInfo: gi, commonInfo: ci, pointers: pointers, publicFingerprint: workspaceMetadataFingerprint(publicInfo)}
	return result, true
}
func (p hostWorktreePin) check(ctx context.Context) (string, bool) {
	if !workspaceRootUnchanged(p.git, p.gitPath, p.gitInfo) || !workspaceRootUnchanged(p.common, p.commonPath, p.commonInfo) {
		return "", false
	}
	budget := &metadataBudget{entries: 200000}
	common, safe := metadataTree(ctx, p.common, ".", nil, true, budget)
	if !safe {
		return "", false
	}
	git, safe := metadataTree(ctx, p.git, ".", p.pointers, false, budget)
	return common + git, safe && workspaceRootUnchanged(p.git, p.gitPath, p.gitInfo) && workspaceRootUnchanged(p.common, p.commonPath, p.commonInfo)
}

func (p hostWorktreePin) publicMatches(root *os.Root, name string) bool {
	file, err := workspaceOpenFile(root, filepath.ToSlash(filepath.Join(name, ".git")))
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	return err == nil && workspaceRegularFile(info) && workspaceMetadataFingerprint(info) == p.publicFingerprint
}
