package gitremote

import "testing"

func TestParseAddition(t *testing.T) {
	for _, raw := range []string{"https://github.com/example-org/example-repo.git", "git@github.com:example-org/example-repo.git", "ssh://git@github.com/example-org/example-repo.git"} {
		if ref, ok := ParseAddition(raw); !ok || ref.FullName() != "example-org/example-repo" {
			t.Fatalf("valid %q: %+v %v", raw, ref, ok)
		}
	}
	for _, raw := range []string{"", "example-org/example-repo", "/tmp/repo", "../repo", "file:///tmp/repo", "ext::evil", "-oProxyCommand=evil", "git@-oProxyCommand=evil:example-org/example-repo", "https://token@github.com/example-org/example-repo", "https://git:secret@github.com/example-org/example-repo", "ssh://evil@github.com/example-org/example-repo", "ssh://git:secret@github.com/example-org/example-repo", "https://github.com:443/example-org/example-repo", "http://github.com/example-org/example-repo", "https://github.com/example-org/example-repo?x", "https://github.com/example-org/example-repo#x", "https://github.com/example-org/%2e%2e", "https://github.com/example-org/../repo", "https://github.com/example-org/example-repo\n", "https://github.com\\@example.com/example-org/example-repo", "git@github.com:/example-org/example-repo", "https://example.com/example-org/example-repo"} {
		if ref, ok := ParseAddition(raw); ok {
			t.Errorf("accepted %q: %+v", raw, ref)
		}
	}
}

func TestAdditionFolderIncludesFullIdentity(t *testing.T) {
	a, _ := ParseAddition("https://github.com/owner-a/tools.git")
	b, _ := ParseAddition("https://github.com/owner-b/tools.git")
	same, _ := ParseAddition("git@github.com:OWNER-A/tools.git")
	if AdditionFolder(a) == AdditionFolder(b) || AdditionFolder(a) != AdditionFolder(same) {
		t.Fatal("destination identity collision")
	}
	if AdditionName(a) != "github.com/owner-a/tools" {
		t.Fatal(AdditionName(a))
	}
}
