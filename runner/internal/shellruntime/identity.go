// Package shellruntime implements the owner shell's isolated runtime side. It
// never sends terminal bytes through an agent event, recorder or logger.
package shellruntime

import (
	"bufio"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrIdentityChanged = errors.New("shell identity changed")

var ErrClosed = errors.New("shell runtime unavailable")

type Identity struct {
	MemberRoot protocol.WorkspaceRoot `json:"member_root,omitzero"`
	Machine    string                 `json:"machine"`
	Account    string                 `json:"account"`
	UID        uint64                 `json:"uid"`
	Shell      string                 `json:"shell"`
	Cwd        string                 `json:"cwd"`
	Home       string                 `json:"home"`
}

// ResolveIdentity reads the actual local account database, never request labels,
// SHELL or inherited provider variables. The caller supplies a trusted prepared cwd.
func ResolveIdentity(cwd string) (Identity, error) {
	if os.Getuid() != os.Geteuid() {
		return Identity{}, ErrClosed
	}
	return resolveAccountIdentity(cwd, os.Geteuid())
}

// ResolvePersistentIdentity ignores machine labels and pins the actual member
// directory as well as the account. It never guesses on unavailable account data.
func ResolvePersistentIdentity(cwd string) (Identity, error) {
	if os.Getuid() != os.Geteuid() {
		return Identity{}, ErrClosed
	}
	return resolveAccountIdentityMode(cwd, os.Geteuid(), true, os.Hostname)
}

func resolveAccountIdentity(cwd string, uid int) (Identity, error) {
	return resolveAccountIdentityMode(cwd, uid, false, os.Hostname)
}

func resolveAccountIdentityMode(cwd string, uid int, persistent bool, hostname func() (string, error)) (Identity, error) {
	var i Identity
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return i, err
	}
	i.UID, err = strconv.ParseUint(u.Uid, 10, 64)
	if err != nil {
		return i, err
	}
	i.Account = u.Username
	i.Home = u.HomeDir
	i.Machine, err = hostname()
	if err != nil {
		if !persistent {
			return i, err
		}
		i.Machine = "Desktop"
	}
	i.Cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil || !filepath.IsAbs(i.Cwd) {
		return Identity{}, ErrClosed
	}
	fi, err := os.Stat(i.Cwd)
	if err != nil || !fi.IsDir() {
		return Identity{}, ErrClosed
	}
	if persistent {
		i.MemberRoot, err = persistentMemberRoot(i.Cwd)
		if err != nil {
			return Identity{}, ownershell.ErrUnknown
		}
	}
	i.Shell = "/bin/sh"
	foundShell := false
	f, err := os.Open("/etc/passwd")
	if err != nil && persistent {
		return Identity{}, ownershell.ErrUnknown
	}
	if err == nil {
		defer func() { _ = f.Close() }()
		scan := bufio.NewScanner(f)
		for scan.Scan() {
			p := strings.Split(scan.Text(), ":")
			if len(p) >= 7 && p[0] == i.Account && p[2] == u.Uid {
				if executable(p[6]) {
					i.Shell = p[6]
					foundShell = true
				}
				break
			}
		}
		if persistent && scan.Err() != nil {
			return Identity{}, ownershell.ErrUnknown
		}
	}
	if persistent && !foundShell {
		return Identity{}, ownershell.ErrUnknown
	}
	if !executable(i.Shell) {
		return Identity{}, ErrClosed
	}
	return i, nil
}
func executable(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	s, e := os.Stat(path)
	return e == nil && s.Mode().IsRegular() && s.Mode().Perm()&0111 != 0
}
func (i Identity) Environment() []string {
	return []string{"HOME=" + i.Home, "USER=" + i.Account, "LOGNAME=" + i.Account, "PATH=/usr/local/bin:/usr/bin:/bin", "TERM=xterm-256color", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
}
