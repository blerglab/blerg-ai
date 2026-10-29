package clusterworkspace

import (
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"testing"
)

func TestReviewedClusterPlanNamesAndSingleDirectory(t *testing.T) {
	for _, single := range []bool{false, true} {
		p := testPlan()
		p.Manifest.Layout = protocol.WorkspaceLayoutNames
		p.Manifest.Members[0].Name = "app"
		p.Manifest.Members[0].Path = "app"
		p.Manifest.Members[1].Name = "app"
		p.Manifest.Members[1].Path = "app-2"
		if single {
			p.Manifest.Kind = "repo"
			p.Manifest.Members = p.Manifest.Members[:1]
			p.Members = p.Members[:1]
			p.Manifest.Members[0].Path = "app"
		}
		if err := p.Validate(); err != nil {
			t.Errorf("single=%v canonical layout rejected: %v", single, err)
		}
		p.Manifest.Layout = protocol.WorkspaceLayoutStructure
		if err := p.Validate(); err == nil {
			t.Error("cluster structure layout accepted")
		}
	}
}

func TestReviewedClusterChildRetainsNamingContext(t *testing.T) {
	p := testPlan()
	p.Manifest.Members[0].Name, p.Manifest.Members[0].Path = "app", "app"
	p.Manifest.Members[1].Name, p.Manifest.Members[1].Path = "app", "app-2"
	p.LayoutSources = []protocol.DiscoverySource{{ID: "one", Name: "app"}, {ID: "two", Name: "app"}}
	p.Manifest.Kind = "repo"
	p.Manifest.Members = append([]protocol.WorkspaceMember(nil), p.Manifest.Members[1:]...)
	p.Members = append([]Member(nil), p.Members[1:]...)
	runtime, err := New(p, newFake())
	if err != nil {
		t.Fatal("child was renamed", err)
	}
	if runtime.plan.Members[0].Path != "app-2" {
		t.Fatal("clone did not receive immutable child path")
	}
	if runtime.results["two"].RecoveryLocation != "projects/app-2" {
		t.Fatal("recovery used member ID")
	}
	p.LayoutSources[1].Name = "changed-after-admission"
	if runtime.plan.LayoutSources[1].Name != "app" {
		t.Fatal("naming context was not copied")
	}
}

func TestReviewedLegacyClusterPlanRecovery(t *testing.T) {
	for _, single := range []bool{true, false} {
		p := testPlan()
		p.Manifest.Layout = ""
		for i := range p.Manifest.Members {
			p.Manifest.Members[i].Name = "app"
			p.Manifest.Members[i].Path = p.Manifest.Members[i].ID
		}
		if single {
			p.Manifest.Kind = "repo"
			p.Manifest.Members = p.Manifest.Members[:1]
			p.Members = p.Members[:1]
		}
		runtime, err := New(p, newFake())
		if err != nil {
			t.Errorf("single=%v legacy recovery rejected: %v", single, err)
			continue
		}
		if err := runtime.Prepare(t.Context(), true); err != nil {
			t.Fatal(err)
		}
		for i, result := range runtime.Stop(t.Context()) {
			if result.RecoveryLocation != "projects/"+p.Manifest.Members[i].ID {
				t.Fatal("legacy recovery relocated", result)
			}
		}
		p.Manifest.Layout = protocol.WorkspaceLayoutNames
		if err := p.Validate(); err == nil {
			t.Error("legacy paths accepted with explicit names layout")
		}
	}
}

func TestReviewedClusterLostFoundReserved(t *testing.T) {
	for _, name := range []string{"lost+found", "LOST+FOUND"} {
		p := testPlan()
		p.Manifest.Layout = protocol.WorkspaceLayoutNames
		p.Manifest.Kind = "repo"
		p.Manifest.Members = p.Manifest.Members[:1]
		p.Members = p.Members[:1]
		p.Manifest.Members[0].Name = name
		p.Manifest.Members[0].Path = "one"
		if err := p.Validate(); err != nil {
			t.Errorf("reserved name did not fall back: %v", err)
		}
		p.Manifest.Members[0].Path = name
		if err := p.Validate(); err == nil {
			t.Error("lost+found directory accepted")
		}
		p.Manifest.Members[0].Path = "."
		if err := p.Validate(); err == nil {
			t.Error("volume root accepted")
		}
	}
}
