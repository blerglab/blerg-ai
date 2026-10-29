//go:build linux || darwin

package hostgit

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type gitValidationInfo struct {
	os.FileInfo
	mode os.FileMode
	uid  uint32
	gid  uint32
}

func (i gitValidationInfo) Mode() os.FileMode { return i.mode }
func (i gitValidationInfo) Sys() any          { return &syscall.Stat_t{Uid: i.uid, Gid: i.gid} }

func TestDarwinGitExecutableValidation(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		mode       os.FileMode
		uid        uint32
		valid      bool
	}{
		{"CLT", "/Library/Developer/CommandLineTools/usr/bin/git", 0755, 0, true},
		{"Xcode", "/Applications/Xcode.app/Contents/Developer/usr/bin/git", 0755, 0, true},
		{"Xcode beta", "/Applications/Xcode Beta.app/Contents/Developer/usr/bin/git", 0755, 0, true},
		{"shim", "/usr/bin/git", 0755, 0, false},
		{"relative", "Library/Developer/usr/bin/git", 0755, 0, false},
		{"developer prefix", "/Library/Developer-evil/usr/bin/git", 0755, 0, false},
		{"app prefix", "/Applications/Xcode.app.evil/usr/bin/git", 0755, 0, false},
		{"other app", "/Applications/Other.app/usr/bin/git", 0755, 0, false},
		{"traversal", "/Library/Developer/../../tmp/git", 0755, 0, false},
		{"multiline", "/Library/Developer/usr/bin/git\n/usr/bin/git", 0755, 0, false},
		{"wrong name", "/Library/Developer/usr/bin/other", 0755, 0, false},
		{"same uid", "/Library/Developer/usr/bin/git", 0755, 501, true},
		{"other uid", "/Library/Developer/usr/bin/git", 0755, 502, false},
		{"group writable", "/Library/Developer/usr/bin/git", 0775, 0, false},
		{"world writable", "/Library/Developer/usr/bin/git", 0757, 0, false},
		{"not executable", "/Library/Developer/usr/bin/git", 0644, 0, false},
		{"directory", "/Library/Developer/usr/bin/git", os.ModeDir | 0755, 0, false},
		{"symlink", "/Library/Developer/usr/bin/git", os.ModeSymlink | 0755, 0, false},
		{"setuid", "/Library/Developer/usr/bin/git", os.ModeSetuid | 0755, 0, false},
		{"setgid", "/Library/Developer/usr/bin/git", os.ModeSetgid | 0755, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDarwinGit(tc.path, gitValidationInfo{mode: tc.mode, uid: tc.uid}, 501)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

// Enumerate independently of the installation allow-list: every ancestor through / matters.
func TestDarwinGitDirectoryChainValidation(t *testing.T) {
	for _, path := range []string{
		"/Library/Developer/CommandLineTools/usr/bin/git",
		"/Applications/Xcode Beta.app/Contents/Developer/usr/bin/git",
	} {
		var chain []string
		for p := path; ; p = filepath.Dir(p) {
			chain = append(chain, p)
			if p == "/" {
				break
			}
		}
		for _, changed := range chain {
			for _, tc := range []struct {
				name        string
				uid         uint32
				permissions os.FileMode
				want        string
			}{
				{"root owned", 0, 0755, ""},
				{"daemon owned", 501, 0755, ""},
				{"other owner", 502, 0755, "owner UID"},
				{"group writable", 0, 0775, "group/world write"},
				{"world writable", 501, 0757, "group/world write"},
				{"symlink", 0, os.ModeSymlink | 0755, "type"},
			} {
				t.Run(changed+"/"+tc.name, func(t *testing.T) {
					visited := map[string]bool{}
					stat := func(p string) (os.FileInfo, error) {
						visited[p] = true
						mode, uid := os.FileMode(0755), uint32(0)
						if p == changed {
							mode, uid = tc.permissions, tc.uid
						}
						if p != path && mode&os.ModeSymlink == 0 {
							mode |= os.ModeDir
						}
						return gitValidationInfo{mode: mode, uid: uid}, nil
					}
					err := validateDarwinGitTree(path, stat, 501)
					if tc.want == "" {
						if err != nil {
							t.Fatal(err)
						}
						if len(visited) != len(chain) {
							t.Fatalf("incomplete chain: %v", visited)
						}
					} else if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), changed) {
						t.Fatalf("missing path/check refusal: %v", err)
					}
				})
			}
		}
		t.Run(path+"/directory replaced by regular file", func(t *testing.T) {
			parent := filepath.Dir(path)
			err := validateDarwinGitTree(path, func(p string) (os.FileInfo, error) {
				mode := os.FileMode(0755)
				if p != path && p != parent {
					mode |= os.ModeDir
				}
				return gitValidationInfo{mode: mode}, nil
			}, 501)
			if err == nil || !strings.Contains(err.Error(), parent) || !strings.Contains(err.Error(), "directory type") {
				t.Fatalf("regular parent: %v", err)
			}
		})
	}
}

func TestDarwinGitRootAdminAncestor(t *testing.T) {
	const path = "/Applications/Xcode.app/Contents/Developer/usr/bin/git"
	stat := func(p string) (os.FileInfo, error) {
		info := darwinGitTestInfo(p, 0).(gitValidationInfo)
		if p == "/Applications" {
			info.mode = os.ModeDir | 0775
			info.gid = 80
		}
		return info, nil
	}
	if err := validateDarwinGitTreeWithAdmin(path, stat, 501, true); err != nil {
		t.Fatal(err)
	}
}

func TestGitAncestorAdminPolicy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		allowAdmin bool
		uid, gid   uint32
		mode       os.FileMode
		valid      bool
	}{
		{"darwin root admin", true, 0, 80, 0775, true},
		{"darwin root staff", true, 0, 20, 0775, false},
		{"darwin daemon admin", true, 501, 80, 0775, false},
		{"darwin other owner admin", true, 502, 80, 0775, false},
		{"darwin root admin world writable", true, 0, 80, 0777, false},
		{"darwin unresolved admin", false, 0, 80, 0775, false},
		{"linux root admin", false, 0, 80, 0775, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := gitValidationInfo{mode: os.ModeDir | tc.mode, uid: tc.uid, gid: tc.gid}
			if err := validateGitOwner(info, 501, tc.allowAdmin); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	// Writable executables never inherit the directory-only exception.
	if err := validateDarwinGit("/Library/Developer/usr/bin/git", gitValidationInfo{mode: 0775, gid: 80}, 501); err == nil {
		t.Fatal("group-writable executable accepted")
	}
}

func TestDarwinAdminGroupResolution(t *testing.T) {
	for _, tc := range []struct {
		name            string
		named, numbered *user.Group
		valid           bool
	}{
		{"admin both directions", &user.Group{Name: "admin", Gid: "80"}, &user.Group{Name: "admin", Gid: "80"}, true},
		{"admin wrong number", &user.Group{Name: "admin", Gid: "20"}, &user.Group{Name: "admin", Gid: "80"}, false},
		{"number wrong name", &user.Group{Name: "admin", Gid: "80"}, &user.Group{Name: "staff", Gid: "80"}, false},
		{"name wrong name", &user.Group{Name: "staff", Gid: "80"}, &user.Group{Name: "admin", Gid: "80"}, false},
		{"number wrong number", &user.Group{Name: "admin", Gid: "80"}, &user.Group{Name: "admin", Gid: "20"}, false},
		{"missing name", nil, &user.Group{Name: "admin", Gid: "80"}, false},
		{"missing number", &user.Group{Name: "admin", Gid: "80"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(want string, group *user.Group) func(string) (*user.Group, error) {
				return func(key string) (*user.Group, error) {
					if key != want {
						t.Fatalf("lookup %q, want %q", key, want)
					}
					if group == nil {
						return nil, errors.New("not found")
					}
					return group, nil
				}
			}
			if got := trustedDarwinAdminGroup(lookup("admin", tc.named), lookup("80", tc.numbered)); got != tc.valid {
				t.Fatalf("trusted=%v", got)
			}
		})
	}
}
