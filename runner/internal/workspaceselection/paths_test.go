package workspaceselection

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspacepolicy"
)

func TestMemberPaths(t *testing.T) {
	for _, tc := range []struct {
		name           string
		names, sources []string
		folder         *protocol.SelectionRef
		layout         protocol.WorkspaceLayout
		want           []string
		fallback       bool
	}{
		{name: "two repos", names: []string{"ai-playground", "nika"}, want: []string{"ai-playground", "nika"}},
		{name: "duplicates", names: []string{"app", "app", "app"}, want: []string{"app", "app-2", "app-3"}},
		{name: "case insensitive", names: []string{"App", "app", "APP"}, want: []string{"App", "app-2", "APP-3"}},
		{name: "unicode folding", names: []string{"s", "ſ"}, want: []string{"s", "ſ-2"}},
		{name: "reserve later real name", names: []string{"app", "APP", "App-2", "app-3"}, want: []string{"app", "APP-4", "App-2", "app-3"}},
		{name: "reserve earlier real name", names: []string{"app-2", "app", "app", "app"}, want: []string{"app-2", "app", "app-3", "app-4"}},
		{name: "fallback collides with real name", names: []string{".", "id-1", "id-1-2"}, want: []string{"id-1", "id-1-3", "id-1-2"}},
		{name: "single repo", names: []string{"app"}, layout: protocol.WorkspaceLayoutStructure, want: []string{"."}},
		{name: "folder", names: []string{"api", "web"}, sources: []string{"group/api", "group/apps/web"}, folder: &protocol.SelectionRef{Path: "group"}, layout: protocol.WorkspaceLayoutStructure, want: []string{"api", "apps/web"}},
		{name: "root folder", names: []string{"api"}, sources: []string{"group/api"}, folder: &protocol.SelectionRef{Path: "."}, want: []string{"group/api"}},
		{name: "folder is repo", names: []string{"api"}, sources: []string{"group/api"}, folder: &protocol.SelectionRef{Path: "group/api"}, want: []string{"."}},
		{name: "same parent", names: []string{"api", "web"}, sources: []string{"home/projects/api", "home/projects/web"}, layout: protocol.WorkspaceLayoutStructure, want: []string{"api", "web"}},
		{name: "different branches", names: []string{"ai-playground", "api"}, sources: []string{"home/repositories/ai-playground", "home/dev/ll/api"}, layout: protocol.WorkspaceLayoutStructure, want: []string{"repositories/ai-playground", "dev/ll/api"}},
		{name: "same named repos", names: []string{"app", "app"}, sources: []string{"root/client/app", "root/server/app"}, layout: protocol.WorkspaceLayoutStructure, want: []string{"client/app", "server/app"}},
		{name: "root ancestor", names: []string{"app", "api"}, sources: []string{"app", "other/api"}, layout: protocol.WorkspaceLayoutStructure, want: []string{"app", "other/api"}},
		{name: "component boundary", names: []string{"api", "web"}, sources: []string{"root/group/api", "root/group-other/web"}, layout: protocol.WorkspaceLayoutStructure, want: []string{"group/api", "group-other/web"}},
		{name: "unsafe structure falls back entirely", names: []string{"app", "app"}, sources: []string{"root/~private/app", "root/public/app"}, layout: protocol.WorkspaceLayoutStructure, want: []string{"app", "app-2"}, fallback: true},
		{name: "protected structure", names: []string{"api", "web"}, sources: []string{"root/secrets/api", "root/apps/web"}, layout: protocol.WorkspaceLayoutStructure, want: []string{"api", "web"}, fallback: true},
		{name: "case collision in structure", names: []string{"app", "APP"}, sources: []string{"root/a/app", "root/A/APP"}, layout: protocol.WorkspaceLayoutStructure, want: []string{"app", "APP-2"}, fallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := protocol.DiscoveryResolved{Layout: tc.layout, Folder: tc.folder}
			for i, name := range tc.names {
				source := fmt.Sprintf("source/%d", i)
				if tc.sources != nil {
					source = tc.sources[i]
				}
				selection.Members = append(selection.Members, protocol.DiscoverySource{ID: fmt.Sprintf("id-%d", i+1), Name: name, Path: source})
			}
			before := append([]protocol.DiscoverySource(nil), selection.Members...)
			got, fallback := MemberPaths(selection)
			if !reflect.DeepEqual(got, tc.want) || fallback != tc.fallback {
				t.Fatalf("paths=%q fallback=%v; want %q fallback=%v", got, fallback, tc.want, tc.fallback)
			}
			if !reflect.DeepEqual(selection.Members, before) {
				t.Fatal("mutated source order or metadata")
			}
		})
	}
}

func TestMemberPathsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", ".git", ".env", "secrets", "credentials.json", "id_rsa", "a/b", "a\\b", "/absolute", "../bad", "~home", "C:drive", "line\nbreak", "null\x00byte", "bad\xff", "%", "%2e%2e", "%252e%252e%252fescape", strings.Repeat("x", 4097)} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			selection := protocol.DiscoveryResolved{Members: []protocol.DiscoverySource{{ID: "id-1", Name: name}, {ID: "id-2", Name: "safe"}}}
			got, _ := MemberPaths(selection)
			if got[0] != "id-1" {
				t.Fatalf("unsafe name became path %q", got[0])
			}
		})
	}
	// Hidden names follow the explorer's policy rather than a separate denylist.
	for _, name := range []string{".github", "ordinary", "secret-project", "with spaces"} {
		got, _ := MemberPaths(protocol.DiscoveryResolved{Members: []protocol.DiscoverySource{{ID: "id-1", Name: name}, {ID: "id-2", Name: "other"}}})
		if !workspacepolicy.SafeSegment(name) || got[0] != name {
			t.Fatalf("public name %q was not preserved", name)
		}
	}
}

func TestLayoutChangesDigest(t *testing.T) {
	m := protocol.WorkspaceManifest{Layout: protocol.WorkspaceLayoutNames}
	names := Digest(m)
	m.Layout = protocol.WorkspaceLayoutStructure
	if names == Digest(m) {
		t.Fatal("layout missing from immutable digest")
	}
}

func TestReviewedMemberNameLengthAndFolding(t *testing.T) {
	for _, name := range []string{strings.Repeat("a", 254), strings.Repeat("b", 255), strings.Repeat("é", 127), strings.Repeat("é", 127) + "x", strings.Repeat("a", 252) + "%41"} {
		selection := protocol.DiscoveryResolved{Members: []protocol.DiscoverySource{{ID: "one", Name: name}, {ID: "two", Name: name}}}
		paths, _ := MemberPaths(selection)
		if paths[0] != name || len(paths[1]) > 255 || !utf8.ValidString(paths[1]) || !workspacepolicy.SafeSegment(paths[1]) || !strings.HasSuffix(paths[1], "-2") {
			t.Errorf("name bytes=%d: paths lengths=%d,%d valid=%v", len(name), len(paths[0]), len(paths[1]), utf8.ValidString(paths[1]))
		}
	}
	paths, _ := MemberPaths(protocol.DiscoveryResolved{Members: []protocol.DiscoverySource{{ID: "one", Name: "é"}, {ID: "two", Name: "e\u0301"}}})
	if paths[1] != "e\u0301-2" {
		t.Errorf("NFC duplicate was not suffixed: %q", paths)
	}
	for _, pair := range [][2]string{{"ſ", "s/child"}, {"é", "e\u0301/child"}, {"é", "e\u0301"}} {
		_, fallback := MemberPaths(protocol.DiscoveryResolved{Layout: protocol.WorkspaceLayoutStructure, Members: []protocol.DiscoverySource{{ID: "one", Name: "first", Path: "root/" + pair[0]}, {ID: "two", Name: "second", Path: "root/" + pair[1]}}})
		if !fallback {
			t.Errorf("folded structure collision accepted: %q", pair)
		}
	}
}

func TestClusterMemberPaths(t *testing.T) {
	for _, tc := range []struct{ names, want []string }{
		{[]string{"app"}, []string{"app"}},
		{[]string{"app", "app", "app-2"}, []string{"app", "app-3", "app-2"}},
		{[]string{"lost+found"}, []string{"id-1"}},
		{[]string{"LOST+FOUND", "lost+found", ".git"}, []string{"id-1", "id-2", "id-3"}},
	} {
		selection := protocol.DiscoveryResolved{Layout: protocol.WorkspaceLayoutNames}
		for i, name := range tc.names {
			selection.Members = append(selection.Members, protocol.DiscoverySource{ID: fmt.Sprintf("id-%d", i+1), Name: name})
		}
		got, fallback := MemberPaths(selection, MemberPathOptions{Cluster: true})
		if !reflect.DeepEqual(got, tc.want) || fallback {
			t.Errorf("%q: got %q want %q", tc.names, got, tc.want)
		}
	}
	// Desktop policy remains independent from the cluster volume requirements.
	got, _ := MemberPaths(protocol.DiscoveryResolved{Members: []protocol.DiscoverySource{{ID: "one", Name: "lost+found"}}})
	if !reflect.DeepEqual(got, []string{"."}) {
		t.Fatal("desktop single repository changed", got)
	}
}
