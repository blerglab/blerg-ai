package daemon

import (
	"encoding/csv"
	"reflect"
	"strings"
	"testing"
)

func TestSandboxGitMountCSVDoesNotCreateAdditionalFields(t *testing.T) {
	for _, source := range []string{"/repos/code:child/.git", "/repos/code,readonly/.git", "/repos/\"quoted\"/.git", "/repos/code,dst=/etc/.git"} {
		target := "/target/child.git"
		fields, err := csv.NewReader(strings.NewReader(sandboxBindMount(source, target))).Read()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fields, []string{"type=bind", "src=" + source, "dst=" + target}) {
			t.Fatalf("source became extra Docker mount fields: %q", fields)
		}
	}
}

func TestSandboxGitMountTargetCannotShadowRuntimeRoots(t *testing.T) {
	for _, path := range []string{"/", "/workspace", "/workspace/nested.git", "/home/agent/.git", "/etc/repo.git", "/usr/local/repo.git", "/proc/123/git", "/run/repo.git", "/root/repo.git"} {
		if safeSandboxGitTarget(path) {
			t.Fatalf("reserved mount target accepted: %s", path)
		}
	}
	for _, path := range []string{"/workspace-other/repo.git", "/home/another/repo.git", "/tmp/repo.git", "/srv/repos/repo/.git"} {
		if !safeSandboxGitTarget(path) {
			t.Fatalf("ordinary absolute metadata target rejected: %s", path)
		}
	}
}
