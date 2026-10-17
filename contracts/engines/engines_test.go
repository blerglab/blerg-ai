package engines

import "testing"

func TestDescriptorsAreIndependentAndCapabilitiesExplicit(t *testing.T) {
	all := All()
	if len(all) < 4 {
		t.Fatal("missing registered engines")
	}
	for _, d := range all {
		if d.ID == "" || d.Name == "" || len(d.Runtimes) == 0 {
			t.Fatal("incomplete descriptor")
		}
		if d.ID == "openclaw" && d.SupportsRuntime("cluster") {
			t.Fatal("advertised unimplemented runtime")
		}
	}
	all[0].Runtimes[0] = "invalid"
	first, _ := Lookup(all[0].ID)
	if first.SupportsRuntime("invalid") {
		t.Fatal("caller mutated registry")
	}
	t.Cleanup(Register(Descriptor{ID: "fixture-engine", Name: "Fixture", Runtimes: []string{"daemon"}}))
	d, ok := Lookup("fixture-engine")
	if !ok || d.Restricted || d.Plugins || !d.SupportsRuntime("daemon") {
		t.Fatal("optional capabilities did not default deny")
	}
	if _, ok := Lookup("unregistered"); ok {
		t.Fatal("unknown engine accepted")
	}
}
